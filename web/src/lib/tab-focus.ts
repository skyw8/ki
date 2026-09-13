// Cross-tab "which session is the user looking at" marker.
//
// A completion notification must be suppressed only for the session the user is
// actually looking at. With several ki tabs open that session can live in a
// different tab than the one that observes the completion, and when the user is
// in another application no ki tab is focused at all. localStorage is shared by
// every tab of the origin and readable synchronously, so the focused tab records
// its current session here and any tab consults it at completion time.

const FOCUS_KEY = 'ki-focused-session'

type FocusEntry = { tab: string; session: string | null }

/** True when this document is the visible, focused tab. */
export function documentActive(): boolean {
  if (typeof document === 'undefined') return true
  return !document.hidden && document.hasFocus()
}

function readEntry(): FocusEntry | null {
  try {
    const raw = localStorage.getItem(FOCUS_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<FocusEntry>
    return typeof parsed.tab === 'string' ? { tab: parsed.tab, session: parsed.session ?? null } : null
  } catch {
    return null
  }
}

/** The session a focused ki tab is showing, or null when no ki tab has focus. */
export function focusedSession(): string | null {
  return readEntry()?.session ?? null
}

/** Drop this tab's marker if it owns it (blur, unload). */
export function clearTabFocus(tabId: string): void {
  try {
    if (readEntry()?.tab === tabId) localStorage.removeItem(FOCUS_KEY)
  } catch {
    // Nothing to clear in private mode.
  }
}

/**
 * Record or clear this tab's focus. Only the focused tab is trusted, so a
 * blurred tab clears the marker only when it still owns it; clearing blindly
 * would erase the entry a newly focused tab just wrote.
 */
export function publishTabFocus(tabId: string, sessionId: string | null): void {
  try {
    if (!documentActive()) {
      clearTabFocus(tabId)
      return
    }
    localStorage.setItem(FOCUS_KEY, JSON.stringify({ tab: tabId, session: sessionId } satisfies FocusEntry))
  } catch {
    // Private mode / quota: focusedSession() stays null, which errs toward
    // notifying instead of silently swallowing a completion.
  }
}
