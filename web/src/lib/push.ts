import type { Client } from '../api/client'

export function pushSupported(): boolean {
  return typeof navigator !== 'undefined'
    && 'serviceWorker' in navigator
    && typeof window !== 'undefined'
    && 'PushManager' in window
}

export function urlBase64ToUint8Array(base64: string): Uint8Array<ArrayBuffer> {
  const normalized = base64.replace(/-/g, '+').replace(/_/g, '/')
  const padded = normalized + '='.repeat((4 - (normalized.length % 4)) % 4)
  const raw = atob(padded)
  const out = new Uint8Array(new ArrayBuffer(raw.length))
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

async function registerWorker(): Promise<ServiceWorkerRegistration | null> {
  if (!pushSupported()) return null
  try {
    return await navigator.serviceWorker.register('/sw.js')
  } catch {
    return null
  }
}

function sameApplicationServerKey(previous: ArrayBuffer | null, current: Uint8Array<ArrayBuffer>): boolean {
  if (!previous) return false
  const bytes = new Uint8Array(previous)
  return bytes.length === current.length && bytes.every((value, index) => value === current[index])
}

export async function ensurePushSubscription(api: Client): Promise<boolean> {
  if (!pushSupported()) return false
  const config = await api.pushConfig().catch(() => null)
  if (!config?.enabled || !config.publicKey) return false
  const registration = await registerWorker()
  if (!registration) return false
  try {
    await navigator.serviceWorker.ready
    const applicationServerKey = urlBase64ToUint8Array(config.publicKey)
    let subscription = await registration.pushManager.getSubscription()
    if (subscription && !sameApplicationServerKey(subscription.options.applicationServerKey, applicationServerKey)) {
      if (!await subscription.unsubscribe()) return false
      await api.deletePushSubscription(subscription.endpoint).catch(() => {})
      subscription = null
    }
    subscription ??= await registration.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey,
    })
    const json = subscription.toJSON()
    if (!json.endpoint || !json.keys?.p256dh || !json.keys?.auth) return false
    await api.putPushSubscription({ endpoint: json.endpoint, keys: { p256dh: json.keys.p256dh, auth: json.keys.auth } })
    return true
  } catch {
    return false
  }
}

export async function dropPushSubscription(api: Client): Promise<void> {
  if (!pushSupported()) return
  try {
    const registration = await navigator.serviceWorker.getRegistration('/')
    const subscription = await registration?.pushManager.getSubscription()
    if (!subscription) return
    await api.deletePushSubscription(subscription.endpoint).catch(() => {})
    await subscription.unsubscribe()
  } catch {
  }
}
