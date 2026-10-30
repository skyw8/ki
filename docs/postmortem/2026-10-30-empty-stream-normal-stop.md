# An empty provider stream ended a run as a normal stop

## What happened

A gateway kept a Chat Completions request open with `200` and `: keepalive` while it tried
several upstream accounts. All attempts failed (two rate limits and one permission refusal), so
the gateway could no longer report an HTTP status and wrote the failure into the open stream as
`data: {"error": …}`, after a synthetic `chatcmpl-…` start chunk and without a `finish_reason`
or `[DONE]`.

The Completions parser only read `choices`; the top-level `error` object was ignored. Because
Completions does not require a terminal event, the stream then ended with no content, no tool
calls, and no stop reason. `postStream` defaulted the stop reason to `stop`, and the loop treated
that empty assistant message as a final answer: the run ended silently with no error and no
retry.

## Why the implementation is structured this way

`parseCompletionsSSE` now turns a top-level `error` object into an error result, preferring its
message, then its type, then its code. This stays in the stream's normal failure path, so the loop
can retry it like any other transient provider failure instead of handing back an empty reply.

`streamWithRetry` also treats a completed assistant message with no text, thinking, or tool call
as a retryable failure (`errEmptyAssistant`). The check runs before telemetry and before the
message is finalized, so an empty attempt is not persisted as the answer. When retries are
exhausted, the loop's existing synthesized error message reaches the client, which makes the
failure visible; a provider error is never converted into a silent stop. Aborted and already
failed messages keep their existing handling, and Responses/Anthropic terminal events remain
untouched.

## Regression coverage

`pkg/llmprotocol` covers an empty Completions start chunk followed by an `error` event: it must
return an error carrying the upstream message. `internal/loop` covers two empty replies followed
by a real answer (three stream calls, one successful assistant end) and persistent empty replies
(three attempts with `MaxRetries: 2`, then an error assistant end containing the empty-response
reason).
