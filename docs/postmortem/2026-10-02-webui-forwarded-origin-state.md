# Forwarded origins are not backend identities

## Failure

A fresh server without the Codex extension rejected WebUI session creation with
`provider "openai-codex" is unavailable`. The browser submitted a remembered
model even when the new server's credential-filtered model catalog was empty.

The audit found the same assumption elsewhere: fixed login/CSRF cookies collide
between localhost ports, an existing origin-bound Push subscription can retain
another server's VAPID key, and reconnects refreshed sessions but not models.
Workspace IDs persisted in browser storage also accepted malformed JSON values.

## Cause

`localStorage` is scoped by scheme/host/port, not by the server behind a forward.
Cookies do not even distinguish ports. Changing a forwarding destination can
therefore leave browser state intact while replacing every backend resource.
Browser state must be a preference, not authority for a server resource.

## Fix and prevention

- Keep presentation preferences origin-local, but use server last-used model
  defaults and page-local workspace expansion. Never submit an unvalidated
  cached model or its thinking selection.
- Give each server process a random instance identity and isolate login/CSRF
  cookie names. Advertise identity through existing auth status and push ready;
  remount runtime state and recheck auth on backend changes or unauthorized SSE.
- Bind API requests to the discovered instance and reject mismatches with 421
  before side effects, covering switches not yet observed by the push stream.
- Refresh model catalogs on reconnect, without silently changing existing
  session models. Disable implicit caching for dynamic JSON.
- Scope cross-tab focus to the instance and renew a bounded lease.
- Compare VAPID keys before reusing browser subscriptions.

Regression coverage uses cheap model, focus, push and cookie/client tests plus
isolated browser cases for stale preferences and backend/authorization changes.

## Verification and cost

- Composer unit suite: 10 cases in 27ms before, 12 in 33ms after. Full Bun unit
  coverage increased from 284 to 298 cases; measured wall time stayed around 4s.
- Five isolated backend-scope browser cases took 11.22s in the serial runner,
  including startup. Adding SSE 421 and stale-login recovery to the initial
  three cases cost 2.77s, from two browser cases and the real reconnect backoff;
  all five also passed independently and opt into parallel scheduling.
- Fresh typecheck/build and the embedded Go regression passed Go packages and
  189 of 190 browser cases. The remaining legacy browser assertion required
  origin-persisted thinking after reload; it now asserts server-default thinking
  on cold startup and preserved `high` when reopening the saved session. Its
  focused rerun passed, retaining the other valid regression results.
