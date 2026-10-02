// The telegram-bot executable bridges Managed Bot messages to Ki's global
// language-independent extension protocol. It keeps chat/topic session mappings
// and polling offsets in private atomic versioned state. Lifecycle output is
// ordered, previews are paced, and durable final replies retry throttling.
// Stdin EOF closes pending host calls and briefly drains request replies so
// piped initialization completes without leaving a stalled sidecar running.
// Attachments stream into temporary files in the target directory; only a
// complete download within the 50 MiB ceiling replaces the destination.
// The binary runs without Go source files or a Go toolchain; configuration and
// locales remain external extension-owned data. See docs/extension.md and README.md.
package main
