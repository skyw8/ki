const FOCUS_PREFIX = 'ki-focused-session:'
export const FOCUS_LEASE_MS = 30_000

type FocusEntry = { tab: string; session: string | null; expires: number }

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

export function focusedSession(serverId: string): string | null {
  return readEntry(serverId)?.session ?? null
}

export function clearTabFocus(serverId: string, tabId: string): void {
  try {
    if (readEntry(serverId)?.tab === tabId) localStorage.removeItem(FOCUS_PREFIX + serverId)
  } catch {
  }
}

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
  }
}
