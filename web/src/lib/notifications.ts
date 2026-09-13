// Session completion notifications.
//
// The WebUI learns that a run finished from the session notification stream
// (agent_end on GET /v1/sessions/{id}/events?notifications=1). That stream stays
// open for background sessions too, so a completion still reaches the browser
// while the user works in another program; the browser forwards it to the OS
// notification center. We stay silent only for the session a focused ki tab is
// showing (see tab-focus.ts): switched to another session, or another
// application entirely, and the completion notifies.
//
// The on/off preference lives in localStorage (per browser). Permission is owned
// by the browser and can only be requested from a user gesture, which is why the
// settings toggle is the entry point rather than the completion watcher.
//
// Port-forward caveat: the Notification API is a secure-context feature. A
// forward that presents `http://localhost:<port>` / `http://127.0.0.1:<port>`
// (ssh -L, IDE/Codespaces, kubectl port-forward) is trustworthy and works, but
// reaching the same server as `http://<host>:<port>` (LAN IP, Tailscale IP) is
// not, and the browser then hides the API entirely. That case reports
// `insecure` so the settings page can point at localhost/HTTPS instead of
// failing silently.

export const NOTIFY_KEY = 'ki-notify'

export type NotifyPermission = 'granted' | 'denied' | 'default' | 'unsupported' | 'insecure'

export function loadNotifyPref(): boolean {
  try { return localStorage.getItem(NOTIFY_KEY) === 'on' } catch { return false }
}

export function saveNotifyPref(enabled: boolean): void {
  try { localStorage.setItem(NOTIFY_KEY, enabled ? 'on' : 'off') } catch { /* private mode */ }
}

// Pure so the secure-context rules are testable without a browser.
export function permissionFrom(opts: { secure: boolean; supported: boolean; permission?: string }): NotifyPermission {
  if (!opts.secure) return 'insecure'
  if (!opts.supported) return 'unsupported'
  return (opts.permission ?? 'default') as NotifyPermission
}

export function currentPermission(): NotifyPermission {
  const secure = typeof window === 'undefined' || window.isSecureContext !== false
  const supported = typeof Notification !== 'undefined'
  return permissionFrom({ secure, supported, permission: supported ? Notification.permission : undefined })
}

export async function requestPermission(): Promise<NotifyPermission> {
  const perm = currentPermission()
  // Asking is pointless without a secure origin or the API itself; report why.
  if (perm === 'insecure' || perm === 'unsupported') return perm
  try {
    return (await Notification.requestPermission()) as NotifyPermission
  } catch {
    // A browser that still rejects (e.g. a permission policy) is a refusal.
    return 'denied'
  }
}

// The whole gating rule in one place so it can be exercised without a browser:
// notify unless a focused ki tab is showing exactly this session.
export function shouldNotify(opts: {
  enabled: boolean
  permission: NotifyPermission
  sessionId: string
  focusedSession: string | null
}): boolean {
  return opts.enabled && opts.permission === 'granted' && opts.focusedSession !== opts.sessionId
}

export function notifyCompletion(opts: {
  enabled: boolean
  sessionId: string
  focusedSession: string | null
  title: string
  body: string
  onClick?: () => void
}): Notification | null {
  if (!shouldNotify({
    enabled: opts.enabled,
    permission: currentPermission(),
    sessionId: opts.sessionId,
    focusedSession: opts.focusedSession,
  })) return null
  // One tag per session: several ki tabs observing the same completion, or a
  // run finishing twice, collapse into a single OS notification instead of a
  // stack of duplicates.
  return showNotification(opts.title, opts.body, opts.onClick, `ki-run-${opts.sessionId}`)
}

// Shared by the run-completion ping and the settings test button.
export function showNotification(title: string, body: string, onClick?: () => void, tag?: string): Notification | null {
  if (typeof Notification === 'undefined') return null
  try {
    const notification = new Notification(title, { body, ...(tag ? { tag } : {}) })
    notification.onclick = () => {
      // Bring the WebUI back so the finished conversation is on screen; the OS
      // notification itself is dismissed once it has served that purpose.
      window.focus()
      onClick?.()
      notification.close()
    }
    return notification
  } catch {
    return null
  }
}
