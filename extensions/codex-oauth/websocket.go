package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"ki/pkg/codexclient"
)

const websocketPoolLimit = 64
const websocketIdleTTL = 5 * time.Minute
const websocketBaselineLimit = 4 << 20
const websocketBaselineBudget = 32 << 20

var socketAdmission = make(chan struct{}, websocketPoolLimit)
var errSocketCapacity = errors.New("Codex WebSocket concurrent request limit reached")
var baselineBudget struct {
	sync.Mutex
	bytes int
}

type codexSocket struct {
	gate         chan struct{}
	connection   atomic.Pointer[websocket.Conn]
	baselineMu   sync.Mutex
	properties   string
	prefix       []string
	baselineSize int
	lastResponse string
	httpOnly     bool
	used         time.Time
	active       int
}

var sockets = struct {
	sync.Mutex
	values map[string]*codexSocket
}{values: make(map[string]*codexSocket)}

func acquireSocket(ctx context.Context, key string) (*codexSocket, func(), error) {
	// Bound both active leases and same-key gate waiters. Never create an
	// untracked overflow connection or an unbounded transport-side queue.
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case socketAdmission <- struct{}{}:
	default:
		return nil, nil, errSocketCapacity
	}
	sockets.Lock()
	now := time.Now()
	for k, s := range sockets.values {
		if s.active == 0 && now.Sub(s.used) > websocketIdleTTL {
			s.close()
			delete(sockets.values, k)
		}
	}
	s := sockets.values[key]
	if s == nil {
		if len(sockets.values) >= websocketPoolLimit {
			oldestKey := ""
			var oldest time.Time
			for k, candidate := range sockets.values {
				if candidate.active == 0 && (oldestKey == "" || candidate.used.Before(oldest)) {
					oldestKey, oldest = k, candidate.used
				}
			}
			if oldestKey != "" {
				sockets.values[oldestKey].close()
				delete(sockets.values, oldestKey)
			}
		}
		s = &codexSocket{gate: make(chan struct{}, 1)}
		s.gate <- struct{}{}
		sockets.values[key] = s
	}
	s.active++
	s.used = now
	sockets.Unlock()
	release := func() {
		sockets.Lock()
		s.active--
		s.used = time.Now()
		if sockets.values[key] != s && s.active == 0 {
			s.close()
		}
		sockets.Unlock()
		<-socketAdmission
	}
	select {
	case <-ctx.Done():
		release()
		return nil, nil, ctx.Err()
	case <-s.gate:
		return s, func() { s.gate <- struct{}{}; release() }, nil
	}
}

func (s *codexSocket) close() {
	if conn := s.connection.Swap(nil); conn != nil {
		_ = conn.Close()
	}
	s.clearBaseline()
}

func (s *codexSocket) clearBaseline() {
	s.baselineMu.Lock()
	defer s.baselineMu.Unlock()
	baselineBudget.Lock()
	baselineBudget.bytes -= s.baselineSize
	baselineBudget.Unlock()
	s.properties, s.prefix, s.lastResponse, s.baselineSize = "", nil, "", 0
}

func closeCodexTransports() {
	sockets.Lock()
	defer sockets.Unlock()
	for _, s := range sockets.values {
		s.close()
	}
	clear(sockets.values)
}

func invalidateWebsocket(sessionID string) {
	sockets.Lock()
	defer sockets.Unlock()
	for key, s := range sockets.values {
		if strings.HasPrefix(key, sessionID+"\x00") {
			s.close()
			delete(sockets.values, key)
		}
	}
}

func requestProperties(body object) object {
	properties := maps.Clone(body)
	delete(properties, "input")
	delete(properties, "client_metadata")
	return properties
}

func (s *codexSocket) continuation(body object) ([]any, string) {
	s.baselineMu.Lock()
	defer s.baselineMu.Unlock()
	if s.lastResponse == "" || jsonString(requestProperties(body)) != s.properties {
		return nil, ""
	}
	current := list(body["input"])
	if len(current) < len(s.prefix) {
		return nil, ""
	}
	for index, item := range s.prefix {
		if item != jsonString(current[index]) {
			return nil, ""
		}
	}
	return current[len(s.prefix):], s.lastResponse
}

func (s *codexSocket) retainBaseline(body object, output []any, responseID string) {
	properties := jsonString(requestProperties(body))
	size := len(properties) + len(responseID)
	prefix := make([]string, 0, len(list(body["input"]))+len(output))
	for _, items := range [][]any{list(body["input"]), output} {
		for _, item := range items {
			raw := jsonString(item)
			size += len(raw)
			if size > websocketBaselineLimit {
				// Keep the connection but use full canonical requests: history
				// compression is an optimization, not a correctness requirement.
				s.clearBaseline()
				return
			}
			prefix = append(prefix, raw)
		}
	}
	s.baselineMu.Lock()
	defer s.baselineMu.Unlock()
	baselineBudget.Lock()
	defer baselineBudget.Unlock()
	baselineBudget.bytes -= s.baselineSize
	s.properties, s.prefix, s.lastResponse, s.baselineSize = "", nil, "", 0
	if baselineBudget.bytes+size > websocketBaselineBudget {
		return
	}
	s.properties, s.prefix, s.lastResponse, s.baselineSize = properties, prefix, responseID, size
	baselineBudget.bytes += size
}

func websocketURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported Codex endpoint scheme %q", u.Scheme)
	}
	return u.String(), nil
}

func dialCodexSocket(ctx context.Context, endpoint string, headers map[string]string, credential object) (*websocket.Conn, *http.Response, error) {
	address, err := websocketURL(endpoint)
	if err != nil {
		return nil, nil, err
	}
	h := make(http.Header)
	for key, value := range headers {
		h.Set(key, value)
	}
	h.Set("Authorization", "Bearer "+str(credential["access"]))
	h.Set("chatgpt-account-id", str(credential["accountId"]))
	h.Set("OpenAI-Beta", codexclient.WebSocketBeta)
	dialer := websocket.Dialer{
		Proxy: http.ProxyFromEnvironment, HandshakeTimeout: streamHeaderTimeout,
		NetDialContext:    (&net.Dialer{Timeout: streamHeaderTimeout, KeepAlive: 30 * time.Second}).DialContext,
		EnableCompression: true,
	}
	return dialer.DialContext(ctx, address, h)
}

// streamWebsocket returns used=false only when HTTP fallback is safe. Once any
// response event is observed, an error is returned rather than re-submitting
// a possibly-running inference and duplicating its output or tool calls.
func streamWebsocket(ctx context.Context, payload, original object, send func(object), requestID string) (used bool, err error) {
	credential := obj(obj(payload["credential"])["value"])
	if !truth(credential["access"]) || !truth(credential["accountId"]) {
		return true, errors.New("invalid Codex OAuth credential")
	}
	body := maps.Clone(original)
	request := obj(payload["request"])
	scope := codexScope(payload, body)
	headers, err := codexclient.PrepareScoped(body, str(request["sessionId"]), str(request["turnId"]), false, scope)
	if err != nil {
		return true, err
	}
	session, turn := headers["thread-id"], str(obj(body["client_metadata"])["turn_id"])
	endpoint := codexURL(obj(payload["model"]))
	key := session + "\x00" + scope
	socket, release, err := acquireSocket(ctx, key)
	if err != nil {
		if errors.Is(err, errSocketCapacity) {
			// Admission rejection is local and no inference has been sent.
			// HTTP remains available without overflowing retained WS resources.
			return false, nil
		}
		return true, err
	}
	defer release()
	if socket.httpOnly {
		return false, nil
	}
	fullRetry := false
	for {
		conn := socket.connection.Load()
		if conn == nil {
			socket.clearBaseline()
			var response *http.Response
			conn, response, err = dialCodexSocket(ctx, endpoint, headers, credential)
			if err != nil {
				if ctx.Err() != nil {
					return true, nil
				}
				if response != nil {
					defer response.Body.Close()
					switch response.StatusCode {
					case http.StatusUpgradeRequired:
						socket.httpOnly = true
						return false, nil
					default:
						return true, upstreamFailure(response)
					}
				}
				socket.httpOnly = true
				return false, nil
			}
			codexclient.CaptureTurnStateScoped(session, turn, response.Header.Get("x-codex-turn-state"), scope)
			socket.connection.Store(conn)
			conn.SetReadLimit(128 << 20)
		}
		// A socket is exclusively leased to this request; cancellation never
		// touches another session and invalidates the incremental baseline.
		stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
		wire := maps.Clone(body)
		wire["type"] = "response.create"
		wire["reasoning"] = body["reasoning"] // Codex WS serializes null when absent.
		if delta, previous := socket.continuation(body); previous != "" {
			wire["previous_response_id"], wire["input"] = previous, delta
		}
		metadata := maps.Clone(obj(wire["client_metadata"]))
		// A reused socket has no new handshake. Carry the turn routing state
		// in body metadata as Codex does for incremental response.create.
		if updated, prepareErr := codexclient.PrepareScoped(body, session, turn, false, scope); prepareErr == nil {
			headers = updated
		} else {
			stopCancel()
			return true, prepareErr
		}
		metadata = maps.Clone(obj(body["client_metadata"]))
		if sticky := headers["x-codex-turn-state"]; sticky != "" {
			metadata["x-codex-turn-state"] = sticky
		}
		wire["client_metadata"] = metadata
		raw, encodeErr := codexclient.Encode(wire)
		if encodeErr != nil {
			stopCancel()
			return true, encodeErr
		}
		_ = conn.SetWriteDeadline(time.Now().Add(streamHeaderTimeout))
		if err = conn.WriteMessage(websocket.TextMessage, raw); err != nil {
			stopCancel()
			socket.close()
			if ctx.Err() != nil {
				return true, nil
			}
			return false, nil
		}
		var builder *streamBuilder
		var output []any
		observed := false
		retry := false
		for {
			_ = conn.SetReadDeadline(time.Now().Add(streamIdleTimeout))
			kind, data, readErr := conn.ReadMessage()
			if readErr != nil {
				stopCancel()
				socket.close()
				if ctx.Err() != nil {
					return true, nil
				}
				if !observed {
					return false, nil
				}
				return true, streamError(fmt.Errorf("Codex WebSocket ended before a terminal response event: %w", readErr), true)
			}
			if kind != websocket.TextMessage && kind != websocket.BinaryMessage {
				continue
			}
			var event object
			if err := decode(string(data), &event); err != nil {
				stopCancel()
				socket.close()
				return true, streamError(errors.New("Codex WebSocket contained invalid JSON"), true)
			}
			typ := str(event["type"])
			code := str(obj(event["error"])["code"])
			if typ == "error" && !observed && !fullRetry &&
				(code == "previous_response_not_found" && wire["previous_response_id"] != nil || code == "websocket_connection_limit_reached") {
				// These explicit rejection events mean inference did not start.
				// Reconnect and send the full canonical input exactly once.
				stopCancel()
				socket.close()
				fullRetry, retry = true, true
				break
			}
			observed = true
			for key, value := range obj(event["headers"]) {
				if strings.EqualFold(key, "x-codex-turn-state") {
					codexclient.CaptureTurnStateScoped(session, turn, str(value), scope)
				}
			}
			if builder == nil {
				builder = newStreamBuilder(send, requestID, obj(payload["model"]))
			}
			if typ == "response.output_item.done" {
				output = append(output, clone(event["item"]))
			}
			if typ == "response.completed" || typ == "response.done" {
				if terminalOutput := list(obj(event["response"])["output"]); len(terminalOutput) > 0 {
					output = clone(terminalOutput).([]any)
				}
			}
			if err := builder.process(typ, event); err != nil {
				stopCancel()
				socket.close()
				return true, streamError(err, true)
			}
			if !builder.terminal {
				continue
			}
			stopCancel()
			if ctx.Err() != nil {
				socket.close()
				return true, nil
			}
			if builder.message["stopReason"] == "error" || builder.message["stopReason"] == "aborted" || builder.message["stopReason"] == "length" {
				socket.close()
			} else {
				socket.retainBaseline(body, output, str(builder.message["responseId"]))
			}
			emitKind := "done"
			values := object{"requestId": requestID}
			if builder.message["stopReason"] == "error" {
				emitKind = "error"
				values["error"] = get(builder.message, "errorMessage", "Codex response failed")
				values["nonRetryable"] = true
			}
			emitEvent(send, emitKind, builder.message, values)
			return true, nil
		}
		if !retry {
			return true, errors.New("Codex WebSocket retry state is invalid")
		}
	}
}
