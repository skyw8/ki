// Package cli is the command-line shell: load config, start or attach to
// the local HTTP server, stream SSE to the terminal.
//
//	ki serve [--addr]   foreground server + WebUI; writes ~/.ki/server.json
//	ki -d               detach a serve process (setsid)
//	ki [flags] <text>   client: create/resume session, POST prompt, print events
//
// If server.json is healthy the client connects; otherwise it listens on
// 127.0.0.1:0 in-process and tears the server down on exit.
// --session is required to resume. --model is sent with the prompt and
// persisted on that session only. KI_FAKE=1 injects provider.Scripted.
//
// Request flow: docs/architecture.md.
package cli
