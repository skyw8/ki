// Package agenttools adapts the six asynchronous collaboration tools to the
// host's agent.Runtime. Build scopes launch, messaging, observation and interrupt
// requests to the calling session. The server supplies the runtime because it
// owns session creation, history selection, providers and run orchestration.
// Agent identity, admission, generation statistics and durable records belong
// to internal/agent rather than these schema/result adapters.
// Cross-package contracts: docs/tools.md.
package agenttools
