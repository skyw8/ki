# zvec-grep index errors disappeared without recovery

Date: 2026-10-01
Scope: zvec-grep background jobs and the WebUI extension inspector.

The Rust engine requires index version 2; the workspace still had the previous
JavaScript engine's version 1. Searches and ordinary index updates correctly
failed, but their text did not consistently name Ki's `/zg-index --rebuild`.
The extension inspector clipped that diagnosis in its navigation item and
showed only configuration in its main area.

Failed jobs also cleared their status after 120 seconds. The WebUI then fell
back to the ready sidecar's green chip, even though the index remained unusable.
A previous successful job's 30-second expiry could erase a newer job's status
for the same reason: the timer did not belong to the status it was clearing.

The fix names the exact slash command in both search and index-job rebuild
errors, keeps failures until a subsequent index job replaces them, and cancels
an earlier success expiry before starting that job. The inspector renders full
operation and runtime errors above both tabs using the shared plain red notice.
Sidecar readiness and operation success remain separate facts.

Protocol regression tests cover version errors, a success followed by failure
across the old timer deadline, and a successful retry. Browser tests cover full
error text, details/config switching, reopening the session after a reload,
and narrow-screen wrapping. Cancellation rows reuse the same plain notice
without the former left rule or decorative dot.
