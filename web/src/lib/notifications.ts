// Session completion notifications.
//
// The WebUI learns that a run finished from the tab's push stream
// (agent_end on GET /v1/events, tagged with the session id). That stream stays
// open for background sessions too, so a completion still reaches the browser
// while the user works in another session or another program; the browser
// forwards it to the OS notification center. We stay silent only for the
// session a focused ki tab is showing (see tab-focus.ts): switched to another
// session, or another application entirely, and the completion notifies.
// Subagent sessions are always silent: they are an implementation detail of a
// parent run whose own completion already notifies.
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
// never notify for a subagent session, otherwise notify unless a focused ki tab
// is showing exactly this session.
export function shouldNotify(opts: {
  enabled: boolean
  permission: NotifyPermission
  sessionId: string
  focusedSession: string | null
  subagent: boolean
}): boolean {
  if (opts.subagent) return false
  return opts.enabled && opts.permission === 'granted' && opts.focusedSession !== opts.sessionId
}

export async function notifyCompletion(opts: {
  enabled: boolean
  sessionId: string
  focusedSession: string | null
  subagent: boolean
  title: string
  body: string
}): Promise<boolean> {
  if (!shouldNotify({
    enabled: opts.enabled,
    permission: currentPermission(),
    sessionId: opts.sessionId,
    focusedSession: opts.focusedSession,
    subagent: opts.subagent,
  })) return false
  // One tag per session: several ki tabs observing the same completion, or a
  // run finishing twice, collapse into a single OS notification instead of a
  // stack of duplicates. The Web Push worker uses the same tag, so a completion
  // that both the page and the push service report collapses too.
  return showNotification({ title: opts.title, body: opts.body, tag: `ki-run-${opts.sessionId}` })
}

// showNotification delivers a notification through whichever channel this
// browser actually has, and reports whether one was raised.
//
// Why the service worker comes first: Android Chrome has no Notification
// constructor at all — `new Notification()` throws "Illegal constructor", and
// only a ServiceWorkerRegistration can raise a notification there. Leading with
// the constructor made the settings test button look dead on a phone while Web
// Push completions, which go through the worker, arrived fine. Using the worker
// whenever a registration exists also gives the page path the same tag and the
// same click behavior as the push path. The constructor stays as the fallback
// for a browser with no service worker.
export async function showNotification(req: {
  title: string
  body: string
  tag?: string
  data?: Record<string, unknown>
}, onClick?: () => void): Promise<boolean> {
  if (currentPermission() !== 'granted') return false
  if (await showViaServiceWorker(req)) return true
  return showViaConstructor(req, onClick)
}

async function showViaServiceWorker(req: { title: string; body: string; tag?: string; data?: Record<string, unknown> }): Promise<boolean> {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return false
  try {
    const registration = await navigator.serviceWorker.getRegistration('/')
    if (!registration) return false
    await registration.showNotification(req.title, {
      body: req.body,
      ...(req.tag ? { tag: req.tag } : {}),
      ...(req.data ? { data: req.data } : {}),
      icon: '/icon-192.png',
      badge: '/icon-192.png',
    })
    return true
  } catch {
    return false
  }
}

function showViaConstructor(req: { title: string; body: string; tag?: string }, onClick?: () => void): boolean {
  if (typeof Notification === 'undefined') return false
  try {
    const notification = new Notification(req.title, {
      body: req.body,
      icon: '/icon-192.png',
      ...(req.tag ? { tag: req.tag } : {}),
    })
    notification.onclick = () => {
      // Bring the WebUI back so the finished conversation is on screen; the OS
      // notification itself is dismissed once it has served that purpose.
      window.focus()
      onClick?.()
      notification.close()
    }
    return true
  } catch {
    return false
  }
}
