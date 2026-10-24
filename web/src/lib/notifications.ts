export const NOTIFY_KEY = 'ki-notify'

export type NotifyPermission = 'granted' | 'denied' | 'default' | 'unsupported' | 'insecure'

export function loadNotifyPref(): boolean {
  try { return localStorage.getItem(NOTIFY_KEY) === 'on' } catch { return false }
}

export function saveNotifyPref(enabled: boolean): void {
  try { localStorage.setItem(NOTIFY_KEY, enabled ? 'on' : 'off') } catch {}
}

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
  if (perm === 'insecure' || perm === 'unsupported') return perm
  try {
    return (await Notification.requestPermission()) as NotifyPermission
  } catch {
    return 'denied'
  }
}

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
  return showNotification({ title: opts.title, body: opts.body, tag: `ki-run-${opts.sessionId}` })
}

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
      window.focus()
      onClick?.()
      notification.close()
    }
    return true
  } catch {
    return false
  }
}
