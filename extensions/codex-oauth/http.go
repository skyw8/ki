package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"

	"ki/pkg/codexclient"
)

const clientID = "app_EMoamEEZ73f0CkXaXp7hrann"
const originator = codexclient.Originator
const betaFeatures = codexclient.BetaFeatures

var streamHeaderTimeout = 60 * time.Second
var streamIdleTimeout = 300 * time.Second

func uuid() string { return codexclient.UUID() }
func codexURL(model object) string {
	base := strings.TrimRight(str(get(model, "baseUrl", "https://chatgpt.com/backend-api")), "/")
	if strings.HasSuffix(base, "/codex/responses") {
		return base
	}
	if strings.HasSuffix(base, "/codex") {
		return base + "/responses"
	}
	return base + "/codex/responses"
}
func codexRequest(ctx context.Context, payload, body object, compaction bool) (*http.Request, error) {
	cred := obj(obj(payload["credential"])["value"])
	if !truth(cred["access"]) || !truth(cred["accountId"]) {
		return nil, errors.New("invalid Codex OAuth credential")
	}
	// Only this top-level field changes. Deep-copying history and tool schemas
	// allocated thousands of objects per request without protecting any mutation.
	body = maps.Clone(body)
	scope := codexScope(payload, body)
	headers, err := codexclient.PrepareScoped(body, str(obj(payload["request"])["sessionId"]), str(obj(payload["request"])["turnId"]), compaction, scope)
	if err != nil {
		return nil, err
	}
	raw, err := codexclient.Encode(body)
	if err != nil {
		return nil, err
	}
	raw, err = codexclient.Compress(raw)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, codexScopeKey{}, scope), "POST", codexURL(obj(payload["model"])), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")
	req.Header.Set("Authorization", "Bearer "+str(cred["access"]))
	req.Header.Set("chatgpt-account-id", str(cred["accountId"]))
	req.Header.Set("Accept", "text/event-stream")
	return req, nil
}

type httpFailure struct {
	status  int
	message string
}

func (e *httpFailure) Error() string { return e.message }
func (e *httpFailure) NonRetryable() bool {
	switch e.status {
	case 400, 401, 403, 404, 405, 410, 413, 415, 422:
		return true
	default:
		return false
	}
}

// Once the backend has started a stream, host-level retries are just as unsafe
// as transport fallback: the original inference may still execute tool calls.
type unsafeStreamError struct{ err error }

func (e *unsafeStreamError) Error() string      { return e.err.Error() }
func (e *unsafeStreamError) Unwrap() error      { return e.err }
func (e *unsafeStreamError) NonRetryable() bool { return true }

func streamError(err error, observed bool) error {
	if observed {
		return &unsafeStreamError{err: err}
	}
	return err
}
func upstreamFailure(response *http.Response) error {
	body, _ := io.ReadAll(response.Body)
	detail := strings.TrimSpace(string(bytes.ToValidUTF8(body, []byte("�"))))
	var v any
	if decode(detail, &v) == nil {
		detail = jsonString(v)
	}
	message := fmt.Sprintf("HTTP Error %d: %s", response.StatusCode, http.StatusText(response.StatusCode))
	if detail != "" {
		message += ": " + detail
	}
	return &httpFailure{response.StatusCode, safeError(errors.New(message))}
}
func safeError(err error) string {
	s := strings.TrimSpace(err.Error())
	r := []rune(s)
	if len(r) > 1000 {
		s = string(r[:1000])
	}
	if s == "" {
		return "provider authentication failed"
	}
	return s
}

// Every stream owns its connection: idle deadlines and cancellation must not
// affect an unrelated session, including on gateways supporting HTTP/2.
func openStream(req *http.Request) (*http.Response, net.Conn, func(), error) {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: streamHeaderTimeout, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: streamHeaderTimeout, ResponseHeaderTimeout: streamHeaderTimeout, DisableKeepAlives: true}
	var conn net.Conn
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		conn = info.Conn
		_ = conn.SetWriteDeadline(time.Now().Add(streamHeaderTimeout))
	}}))
	response, err := (&http.Client{Transport: transport}).Do(req)
	cleanup := func() { transport.CloseIdleConnections() }
	if err != nil {
		cleanup()
		return nil, nil, cleanup, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		err = upstreamFailure(response)
		response.Body.Close()
		cleanup()
		return nil, nil, cleanup, err
	}
	if conn != nil {
		_ = conn.SetReadDeadline(time.Now().Add(streamIdleTimeout))
	}
	return response, conn, cleanup, nil
}

type idleReader struct {
	body io.Reader
	conn net.Conn
}

func (r idleReader) Read(p []byte) (int, error) {
	if r.conn != nil {
		_ = r.conn.SetReadDeadline(time.Now().Add(streamIdleTimeout))
	}
	return r.body.Read(p)
}
func consumeStream(ctx context.Context, response *http.Response, conn net.Conn, feed func(string) (bool, error)) error {
	reader := bufio.NewReader(idleReader{response.Body, conn})
	for {
		line, err := reader.ReadString('\n')
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(line) > 0 {
			done, feedErr := feed(strings.TrimRight(string(bytes.ToValidUTF8([]byte(line), []byte("�"))), "\r\n"))
			if feedErr != nil {
				return feedErr
			}
			if done {
				return nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
func streamCodex(ctx context.Context, payload object, send func(object), id string) error {
	payload = requestIdentity(payload)
	body, err := buildRequest(payload)
	if err != nil {
		return err
	}
	used, err := streamWebsocket(ctx, payload, body, send, id)
	if used || err != nil {
		return err
	}
	req, err := codexRequest(ctx, payload, body, false)
	if err != nil {
		return err
	}
	response, conn, cleanup, err := openStream(req)
	if err != nil {
		return err
	}
	defer cleanup()
	defer response.Body.Close()
	captureHTTPState(req, response)
	builder := newStreamBuilder(send, id, obj(payload["model"]))
	observed := false
	if err := consumeStream(ctx, response, conn, func(line string) (bool, error) {
		if strings.HasPrefix(line, "data:") && strings.TrimSpace(line[5:]) != "" {
			observed = true
		}
		return builder.feed(line)
	}); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return streamError(err, observed)
	}
	if ctx.Err() != nil {
		return nil
	}
	if !builder.terminal {
		return streamError(errors.New("Codex stream ended before a terminal response event"), observed)
	}
	kind := "done"
	values := object{"requestId": id}
	if builder.message["stopReason"] == "error" {
		kind = "error"
		values["error"] = get(builder.message, "errorMessage", "Codex response failed")
		values["nonRetryable"] = true
	}
	emitEvent(send, kind, builder.message, values)
	return nil
}
func compactCodex(ctx context.Context, payload object) (object, error) {
	payload = requestIdentity(payload)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("Codex compaction was cancelled: %w", ctx.Err())
	}
	body, err := buildCompactRequest(payload)
	if err != nil {
		return nil, err
	}
	input := list(body["input"])
	retained := retainedCompactionItems(input[:len(input)-1])
	req, err := codexRequest(ctx, payload, body, true)
	if err != nil {
		return nil, err
	}
	response, conn, cleanup, err := openStream(req)
	if err != nil {
		// Server-side header flushing does not fence Client.Do completion.
		// Cancellation during that race must match cancellation during reads,
		// rather than leaking a transport-specific error to the RPC caller.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Codex compaction was cancelled: %w", ctx.Err())
		}
		return nil, err
	}
	defer cleanup()
	defer response.Body.Close()
	captureHTTPState(req, response)
	builder := newCompactBuilder()
	if err = consumeStream(ctx, response, conn, builder.feed); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Codex compaction was cancelled: %w", ctx.Err())
		}
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("Codex compaction was cancelled: %w", ctx.Err())
	}
	if !builder.terminal {
		return nil, errors.New("Codex compaction stream ended before response.completed")
	}
	result, err := builder.result()
	if err == nil {
		result["items"] = append(retained, list(result["items"])...)
		session := req.Header.Get("thread-id")
		codexclient.AdvanceWindow(session)
		invalidateWebsocket(session)
	}
	return result, err
}

func requestIdentity(payload object) object {
	request := maps.Clone(obj(payload["request"]))
	if !truth(request["turnId"]) {
		request["turnId"] = uuid()
	}
	if !truth(request["sessionId"]) {
		request["sessionId"] = uuid()
	}
	copy := maps.Clone(payload)
	copy["request"] = request
	return copy
}

func captureHTTPState(req *http.Request, response *http.Response) {
	var metadata object
	if decode(req.Header.Get("x-codex-turn-metadata"), &metadata) == nil {
		scope, _ := req.Context().Value(codexScopeKey{}).(string)
		codexclient.CaptureTurnStateScoped(req.Header.Get("thread-id"), str(metadata["turn_id"]), response.Header.Get("x-codex-turn-state"), scope)
	}
}

type codexScopeKey struct{}

func codexScope(payload, body object) string {
	credential := obj(obj(payload["credential"])["value"])
	return shortHash(jsonString([]any{codexURL(obj(payload["model"])), credential["accountId"], credential["access"], body["model"], body["service_tier"]}))
}
func httpJSON(ctx context.Context, method, endpoint string, body any) (int, object, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(jsonString(body)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, err
	}
	var result object
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	err = decode(string(raw), &result)
	if err != nil && response.StatusCode >= 400 {
		result = object{}
		err = nil
	}
	return response.StatusCode, result, err
}
func formJSON(ctx context.Context, endpoint string, values url.Values) (object, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("token request failed (%d)", response.StatusCode)
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	var result object
	err = decode(string(raw), &result)
	return result, err
}
