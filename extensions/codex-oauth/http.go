package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

const clientID = "app_EMoamEEZ73f0CkXaXp7hrann"
const originator = "codex_cli_rs"
const betaFeatures = "remote_compaction_v2"

var streamHeaderTimeout = 60 * time.Second
var streamIdleTimeout = 300 * time.Second
var installationID = uuid()
var windowIDs = map[string]string{}
var windowMu sync.Mutex

func uuid() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func sessionWindowID(id string) string {
	windowMu.Lock()
	defer windowMu.Unlock()
	if windowIDs[id] == "" {
		windowIDs[id] = uuid()
	}
	return windowIDs[id]
}
func codexUserAgent() string {
	system := runtime.GOOS
	switch system {
	case "linux":
		system = "Linux"
	case "darwin":
		system = "Mac OS"
	case "windows":
		system = "Windows"
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	terminal := os.Getenv("TERM_PROGRAM")
	if terminal == "" {
		terminal = os.Getenv("TERM")
	}
	if terminal == "" {
		terminal = "unknown"
	}
	return fmt.Sprintf("%s/0.0.0 (%s %s; %s) %s", originator, system, osVersion(), arch, terminal)
}
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
func turnMetadata(session, thread, window, turn string, compaction bool) string {
	kind := "turn"
	if compaction {
		kind = "compaction"
	}
	m := object{"session_id": session, "thread_id": thread, "window_id": window, "turn_id": turn, "root_turn_id": turn, "request_kind": kind}
	if compaction {
		m["compaction"] = object{"trigger": "manual", "reason": "user_requested", "implementation": "responses_compaction_v2", "phase": "standalone_turn", "strategy": "memento"}
	}
	return jsonString(m)
}
func codexRequest(ctx context.Context, payload, body object, compaction bool) (*http.Request, error) {
	cred := obj(obj(payload["credential"])["value"])
	if !truth(cred["access"]) || !truth(cred["accountId"]) {
		return nil, errors.New("invalid Codex OAuth credential")
	}
	session := str(fallback(obj(payload["request"])["sessionId"], uuid()))
	window, turn := sessionWindowID(session), uuid()
	metadata := turnMetadata(session, session, window, turn, compaction)
	// Only this top-level field changes. Deep-copying history and tool schemas
	// allocated thousands of objects per request without protecting any mutation.
	body = maps.Clone(body)
	body["client_metadata"] = object{"x-codex-installation-id": installationID, "session_id": session, "thread_id": session, "x-codex-window-id": window, "turn_id": turn, "x-codex-turn-metadata": metadata}
	req, err := http.NewRequestWithContext(ctx, "POST", codexURL(obj(payload["model"])), strings.NewReader(jsonString(body)))
	if err != nil {
		return nil, err
	}
	for k, v := range map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + str(cred["access"]), "chatgpt-account-id": str(cred["accountId"]), "OpenAI-Beta": "responses=experimental", "originator": originator, "User-Agent": codexUserAgent(), "Accept": "text/event-stream", "x-codex-beta-features": betaFeatures, "session-id": session, "thread-id": session, "x-client-request-id": session, "x-codex-window-id": window, "x-codex-turn-metadata": metadata} {
		req.Header.Set(k, v)
	}
	return req, nil
}

type httpFailure struct {
	status  int
	message string
}

func (e *httpFailure) Error() string { return e.message }
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
	body, err := buildRequest(payload)
	if err != nil {
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
	builder := newStreamBuilder(send, id, obj(payload["model"]))
	if err := consumeStream(ctx, response, conn, builder.feed); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	if !builder.terminal {
		return errors.New("Codex stream ended before a terminal response event")
	}
	kind := "done"
	values := object{"requestId": id}
	if builder.message["stopReason"] == "error" {
		kind = "error"
		values["error"] = get(builder.message, "errorMessage", "Codex response failed")
	}
	emitEvent(send, kind, builder.message, values)
	return nil
}
func compactCodex(ctx context.Context, payload object) (object, error) {
	if ctx.Err() != nil {
		return nil, errors.New("Codex compaction was cancelled")
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
		return nil, err
	}
	defer cleanup()
	defer response.Body.Close()
	builder := newCompactBuilder()
	if err = consumeStream(ctx, response, conn, builder.feed); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("Codex compaction was cancelled")
		}
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, errors.New("Codex compaction was cancelled")
	}
	if !builder.terminal {
		return nil, errors.New("Codex compaction stream ended before response.completed")
	}
	result, err := builder.result()
	if err == nil {
		result["items"] = append(retained, list(result["items"])...)
	}
	return result, err
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
