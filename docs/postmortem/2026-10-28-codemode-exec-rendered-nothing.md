# Code Mode tool rows rendered nothing

## Discovery

The WebUI treated every `exec` / `wait` result as the older concatenated form
`Script completed{…}{…}` and passed the whole text to `nestedCalls`. The current
runtime returns separate content blocks: a status header such as
`Script completed`, followed by the explicit `text()` output of the cell. When
the first output line was ordinary text, `nestedCalls` returned `null`,
`CodeModeResult` rendered `null`, and the expanded tool row showed neither the
JavaScript source nor the output. The collapsed row fell back to the first line
of the concatenated result, so it showed an opaque header instead of what ran.

## Why the implementation is structured this way

The result body is now split from the status header before rendering. When the
transcript carries content blocks, each text block is kept separate and joined
with a newline; the legacy concatenated string is still parsed as a fallback.
The `exec` source comes from `args.code` for function calls and `args.input` for
freeform calls, so both provider encodings render identically. Nested tool calls
remain separate transcript rows; only the old envelope-shaped result is expanded
into `cell-call` blocks.

`outputBlocks` is now part of the tool node projection instead of existing only
on trajectory records. Live `tool_execution_end` events copy their result
content into it, and reloaded `toolResult` messages do the same, so the first
live render and a later history reload use one code path.

## Regression coverage

Unit tests cover block-based output, concatenated fallback, running cell IDs,
failure and termination headers, error extraction, images, and both exec argument
shapes. The tool-card browser test renders one legacy envelope result and one
current block-based result, asserting the code, output, status, and collapsed
preview.
