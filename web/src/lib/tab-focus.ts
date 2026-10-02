// Cross-tab "which session is the user looking at" marker.
//
// A completion notification must be suppressed only for the session the user is
// actually looking at. With several ki tabs open that session can live in a
// different tab than the one that observes the completion, and when the user is
// in another application no ki tab is focused at all. localStorage is shared by
// every tab of the origin and readable synchronously. Scope the marker to the
// server instance: a forwarded origin may later point at a different backend.
// A short renewed lease prevents a crashed tab from suppressing notifications
// indefinitely when unload cannot clear its marker.

const FOCUS_PREFIX = 'ki-focused-session:'
export const FOCUS_LEASE_MS = 30_000

type FocusEntry = { tab: string; session: string | null; expires: number }

/** True when this document is the visible, focused tab. */
export function documentActive(): boolean {
  if (typeof document === 'undefined') return true
  return !document.hidden && document.hasFocus()
}

function readEntry(serverId: string): FocusEntry | null {
  if (!serverId) return null
  try {
    const raw = localStorage.getItem(FOCUS_PREFIX + serverId)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<FocusEntry>
    if (!parsed || typeof parsed.tab !== 'string' || !parsed.tab
      || !(parsed.session === null || typeof parsed.session === 'string')
      || typeof parsed.expires !== 'number' || !Number.isFinite(parsed.expires)
      || parsed.expires <= Date.now() || parsed.expires > Date.now() + FOCUS_LEASE_MS) return null
    return parsed as FocusEntry
  } catch {
    return null
  }
}

/** The session a focused ki tab is showing, or null when no ki tab has focus. */
export function focusedSession(serverId: string): string | null {
  return readEntry(serverId)?.session ?? null
}

/** Drop this tab's marker if it owns it (blur, unload). */
export function clearTabFocus(serverId: string, tabId: string): void {
  try {
    if (readEntry(serverId)?.tab === tabId) localStorage.removeItem(FOCUS_PREFIX + serverId)
  } catch {
    // Nothing to clear in private mode.
  }
}

/**
 * Record or clear this tab's focus. Only the focused tab is trusted, so a
 * blurred tab clears the marker only when it still owns it; clearing blindly
 * would erase the entry a newly focused tab just wrote.
 */
export function publishTabFocus(serverId: string, tabId: string, sessionId: string | null): void {
  if (!serverId) return
  try {
    if (!documentActive()) {
      clearTabFocus(serverId, tabId)
      return
    }
    localStorage.setItem(FOCUS_PREFIX + serverId, JSON.stringify({
      tab: tabId, session: sessionId, expires: Date.now() + FOCUS_LEASE_MS,
    } satisfies FocusEntry))
  } catch {
    // Private mode / quota: focusedSession() stays null, which errs toward
    // notifying instead of silently swallowing a completion.
  }
}
