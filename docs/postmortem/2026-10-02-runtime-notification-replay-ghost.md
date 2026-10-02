# Runtime notifications resurfaced after the final reply

## Symptoms

Reconnecting to a long-running transcript appeared to load runtime notifications
separately from history. A completed conversation could end with a notification
instead of the assistant answer; reopening it restored the correct history.

## Cause

`message_end` is persisted before it enters the run replay buffer. A compact
snapshot can therefore include a hidden notification while replay's buffered
sequence cutoff still precedes it. Its `message_start` escaped the cutoff, but
its durable end was correctly skipped as already covered by the snapshot.
The browser created an optimistic user row from that start. With the end
filtered out, nothing acknowledged the row, so subsequent reconciliation
preserved it after the final answer.

`steer_accepted` had the same problem: acceptance is not proof that a message
entered model history. Queue-only runtime mail can remain undrained when a turn
naturally completes. Giving such mail an optimistic transcript row also left
a notification that disappeared only on a fresh opening.

## Fix

- Snapshot replay tracks persisted user `clientRequestId` values on the selected
  branch, including body-free metadata, and filters corresponding starts and
  acceptances without waiting for their end to reach the replay buffer.
- Acceptance filtering uses identity, not text or a sequence cutoff; genuinely
  pending human input and concurrent tools must still replay.
- Runtime mail enters the displayed transcript only at its durable
  `message_end`. Human and extension-relayed prompts retain optimistic feedback.
- Both detailed and compact readers send the committed leaf as snapshot replay
  coverage. Detailed mode previously replayed older notifications outside its
  tail window, unnecessarily downloading and appending their bodies again.
- No notification-specific loader or extra transcript fetch was introduced.

## Regression coverage

Go tests reproduce the persist-before-buffer window deterministically, preserve
uncovered input and concurrent tools, and verify human/runtime acceptance.
Bun tests cover compact snapshots, acceptance/start replay permutations,
mid-stream undrained mail, terminal answers, hydration, and durable notice order.

The affected Bun suites went from 52 tests in 0.44s to 55 in 0.51s under the
same command. The added cases use no sleeps or browser processes. Focused Go
server coverage increased from 0.143s to 0.164s; the metadata test took 0.012s.

## Follow-up: trailing durable notification consumed the final reply slot

The first fix did **not** address the compact classification rule. Read-only
inspection of session `01a0fa9060837461913312204071a1c8` confirmed a different,
durable case: context-only mail was persisted after the terminal assistant.
Both Go compact projection and browser folding classified runtime user messages
as replies, so `keep=1` retained the notification and hid the answer. Refreshing
could not fix this projection.

The interim fix made runtime notifications and directives follow the compaction/cancellation
display rule: always visible in chronological order, outside `keep` and
`hiddenCount`. They still belong to their original human turn (or the existing
machine-only anchor); turn boundaries, model context and statistics do not change.
Go↔TS fixtures cover middle/trailing notices and machine-only directives at
keep 0/1. A browser regression checks the real compact endpoint, reload,
expansion/collapse and keep=0, rather than only replaying synthetic events.

Read-only reprojection of the reported session retains its original final
assistant `01a0fa96ae06731688ac1e306d056cf1` and all three runtime messages; that
turn's hidden count changes from 71 to 68. Focused Bun coverage increased from
60 to 66 tests (0.45s → 0.43s), and focused Go compact coverage from 0.115s to
0.118s. The new isolated browser case takes 2.9s (5.96s including build/startup);
that cost covers the real endpoint and browser reload, which pure reducers
cannot verify. The full regression passed all other cases; this case initially
failed because the test expected reload to auto-select a session. After using
the normal reopen action, its focused rerun passed without changing assertions.

## Refinement: foldable is not the same as keep-eligible

Making every runtime message visible fixed the final reply but flooded long
turns with intermediate reports. The clarified rule separates two decisions:
runtime messages can fold (and count in `hiddenCount`), but only normal
assistant/tool replies consume `keep`.

The first kept or still-live real reply defines the visible suffix. Older
notifications fold with older replies; newer suffix notifications stay in their
chronological positions without displacing the answer. `keep=0` folds notices
too unless live work pins that suffix. Compaction/cancellation remain genuinely
always-visible metadata. Runtime-only anchors retain their identity/body for
expansion, not an artificial keep slot.

Reprojecting the reported turn now retains the final answer and trailing
notification, folds its two middle reports, and reports `hiddenCount=70`.
Focused Bun coverage is 67 tests (0.50s versus 66 in 0.87s before); focused Go
compact coverage is 0.115s versus 0.135s. The updated browser case remains 2.9s.
The full run passed all Go/Bun cases and other browser cases except an unrelated
diagram-fit timing failure; its focused rerun passed. The notification test also
needed to distinguish a fold preview from a mounted message row: previews may
contain a hidden notice's text. The corrected row assertion and the full
notification reopen/expansion/keep=0 scenario passed together.
