// Package cli is the Cobra command shell: load config, start or attach to the
// local HTTP server, and stream SSE to the terminal.
//
//	ki                  detached server + WebUI, then best-effort browser open
//	ki serve [-d]       foreground or detached server + WebUI
//	ki run [flags] text create/resume session, POST prompt, print events
//	ki session compact  compact an existing session
//	ki session fork     fork an existing session
//	ki reload           POST /v1/reload on a live daemon (no in-process serve)
//	ki config path      print config file locations
//	ki version          print the build version
//
// If server.json is healthy the client connects; otherwise it listens on
// 127.0.0.1:0 in-process and tears the server down on exit.
// --session is required to resume. --model is sent with the prompt and
// persisted on that session only. --steer / --queue override the busy-message
// default from toggles.json. KI_FAKE=1 injects provider.Scripted.
//
// Request flow: docs/architecture.md.
package cli
