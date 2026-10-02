// Package builtin assembles model-aware built-in tool families. Set.Build
// selects rich/text read and one editor family: Responses models explicitly
// opting into freeform apply_patch use it; other targets use write/edit.
// File, shell and agent adapters
// depend on tool contracts and their own runtime services, never on the loop.
//
// Set.Catalog includes every model-selectable editor so a hidden editor retains
// its global toggle. FilterBuiltins applies only to builtin results; extension
// enablement is separate. The lightweight catalog owns reserved identifiers.
// The server owns shared mutation queues, process managers, agent runtimes and
// output stores and injects them when it assembles one turn's tools.
// Cross-package contracts: docs/tools.md.
// The settings catalog also includes exec/wait, which the server adds only
// after the ordinary nested capabilities have been filtered for Code Mode.
// search_tool appears in the global catalog but is bound only after MCP policy
// filtering; disabling it restores direct schema disclosure for allowed tools.
package builtin
