package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"sync"
)

//go:embed extension.json
var manifestBytes []byte

func providerSpec() object {
	var manifest object
	if decode(string(manifestBytes), &manifest) != nil {
		panic("invalid embedded manifest")
	}
	return obj(list(manifest["providers"])[0])
}

type operation struct {
	ctx    context.Context
	cancel context.CancelFunc
}
type sidecar struct {
	writeMu              sync.Mutex
	mu                   sync.Mutex
	send                 func(object)
	auth                 map[string]*oauthSession
	streams, compactions map[string]*operation
	ctx                  context.Context
	cancel               context.CancelFunc
	workers              sync.WaitGroup
	compact              func(context.Context, object) (object, error)
}

func newSidecar(out io.Writer) *sidecar {
	ctx, cancel := context.WithCancel(context.Background())
	s := &sidecar{auth: map[string]*oauthSession{}, streams: map[string]*operation{}, compactions: map[string]*operation{}, ctx: ctx, cancel: cancel, compact: compactCodex}
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	s.send = func(v object) { s.writeMu.Lock(); defer s.writeMu.Unlock(); _ = encoder.Encode(v) }
	return s
}
func (s *sidecar) reply(id, result any) { s.send(object{"jsonrpc": "2.0", "id": id, "result": result}) }
func (s *sidecar) fail(id any, message string, code int) {
	s.send(object{"jsonrpc": "2.0", "id": id, "error": object{"code": code, "message": safeError(errors.New(message))}})
}
func (s *sidecar) shutdown() {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.auth {
		v.close()
	}
	for _, v := range s.streams {
		v.cancel()
	}
	for _, v := range s.compactions {
		v.cancel()
	}
}
func (s *sidecar) handle(m object) {
	method, id, p := str(m["method"]), m["id"], obj(m["params"])
	switch method {
	case "initialize":
		s.reply(id, object{"tools": []any{}, "commands": []any{}, "providers": []any{providerSpec()}})
	case "provider.auth.start":
		requestID := str(p["requestId"])
		if requestID == "" || p["provider"] != "openai-codex" {
			s.reply(id, object{"accepted": false})
			return
		}
		session := newOAuthSession(s.ctx, p, s.send)
		s.mu.Lock()
		if old := s.auth[requestID]; old != nil {
			old.close()
		}
		s.auth[requestID] = session
		s.mu.Unlock()
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			session.run()
			s.mu.Lock()
			if s.auth[requestID] == session {
				delete(s.auth, requestID)
			}
			s.mu.Unlock()
		}()
		s.reply(id, object{"accepted": true})
	case "provider.auth.input":
		s.mu.Lock()
		session := s.auth[str(p["requestId"])]
		s.mu.Unlock()
		if session == nil {
			s.fail(id, "auth request not found", -32000)
			return
		}
		select {
		case session.manual <- str(get(p, "value", "")):
			s.reply(id, object{"accepted": true})
		default:
			s.fail(id, "auth input already pending", -32000)
		}
	case "provider.auth.cancel":
		s.mu.Lock()
		session := s.auth[str(p["requestId"])]
		s.mu.Unlock()
		if session != nil {
			session.close()
		}
	case "provider.auth.refresh":
		credential, refreshed, err := refreshCredential(s.ctx, obj(p["credential"]))
		if err != nil {
			s.fail(id, safeError(err), -32000)
		} else if refreshed {
			s.reply(id, object{"refreshed": true, "credential": credential})
		} else {
			s.reply(id, object{})
		}
	case "provider.compact":
		if id == nil || p["provider"] != "openai-codex" {
			if id != nil {
				s.fail(id, "invalid Codex compaction request", -32602)
			}
			return
		}
		key := str(id)
		ctx, cancel := context.WithCancel(s.ctx)
		operation := &operation{ctx, cancel}
		s.mu.Lock()
		if s.compactions[key] != nil {
			s.mu.Unlock()
			cancel()
			s.fail(id, "Codex compaction request is already running", -32600)
			return
		}
		s.compactions[key] = operation
		s.mu.Unlock()
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			defer cancel()
			result, err := s.compact(ctx, p)
			if err != nil {
				code := -32040
				var upstream *httpFailure
				var network *url.Error
				var netErr net.Error
				if errors.As(err, &upstream) {
					code = upstream.status
				} else if ctx.Err() != nil {
					code = -32800
				} else if errors.As(err, &network) || errors.As(err, &netErr) {
					code = -32000
				}
				s.fail(id, safeError(err), code)
			} else {
				s.reply(id, result)
			}
			s.mu.Lock()
			if s.compactions[key] == operation {
				delete(s.compactions, key)
			}
			s.mu.Unlock()
		}()
	case "provider.stream.start":
		requestID := str(p["requestId"])
		payload := obj(p["request"])
		if requestID == "" || payload["provider"] != "openai-codex" {
			s.reply(id, object{"accepted": false})
			return
		}
		ctx, cancel := context.WithCancel(s.ctx)
		operation := &operation{ctx, cancel}
		s.mu.Lock()
		if s.streams[requestID] != nil {
			s.mu.Unlock()
			cancel()
			s.reply(id, object{"accepted": false})
			return
		}
		s.streams[requestID] = operation
		s.mu.Unlock()
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			defer cancel()
			if err := streamCodex(ctx, payload, s.send, requestID); err != nil && ctx.Err() == nil {
				s.send(object{"jsonrpc": "2.0", "method": "provider.stream.event", "params": object{"requestId": requestID, "type": "error", "error": safeError(err)}})
			}
			s.mu.Lock()
			if s.streams[requestID] == operation {
				delete(s.streams, requestID)
			}
			s.mu.Unlock()
		}()
		s.reply(id, object{"accepted": true})
	case "provider.stream.cancel":
		s.mu.Lock()
		operation := s.streams[str(p["requestId"])]
		s.mu.Unlock()
		if operation != nil {
			operation.cancel()
		}
	case "cancel":
		s.mu.Lock()
		operation := s.compactions[str(p["id"])]
		s.mu.Unlock()
		if operation != nil {
			operation.cancel()
		}
	case "shutdown":
		s.shutdown()
		if id != nil {
			s.reply(id, object{})
		}
	default:
		if id != nil {
			s.reply(id, object{})
		}
	}
}
func (s *sidecar) serve(in io.Reader) error {
	defer s.shutdown()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 65*1024*1024)
	for scanner.Scan() {
		var m object
		if decode(scanner.Text(), &m) == nil && m != nil {
			s.handle(m)
		}
	}
	return scanner.Err()
}
func main() {
	s := newSidecar(os.Stdout)
	if err := s.serve(os.Stdin); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
