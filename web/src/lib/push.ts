// Web Push subscription lifecycle for the settings toggle.
//
// The page raises a notification from its own push stream while it is running.
// Web Push extends that to a frozen page: the browser's push service wakes
// /sw.js, which shows the OS notification with no tab involved. Subscribing is
// therefore an extension of the same "session completion" preference, not a
// second setting.
//
// Everything here is best-effort. A browser without Push, a server without a
// VAPID key, or a push service that refuses the subscription all leave the
// feature off and the live-tab path untouched.

import type { Client } from '../api/client'

export function pushSupported(): boolean {
  return typeof navigator !== 'undefined'
    && 'serviceWorker' in navigator
    && typeof window !== 'undefined'
    && 'PushManager' in window
}

// urlBase64ToUint8Array converts the base64url VAPID key into the Uint8Array
// PushManager.subscribe expects as applicationServerKey.
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

/**
 * Ensure this browser holds a push subscription the server knows about.
 * Returns true only when the server can reach this browser with the page
 * closed. Idempotent: the server upserts by endpoint, so the re-POST on every
 * load is also how a subscription heals after the server pruned it (a 404/410
 * from the push service) or after KI_HOME was reset.
 */
export async function ensurePushSubscription(api: Client): Promise<boolean> {
  if (!pushSupported()) return false
  const config = await api.pushConfig().catch(() => null)
  if (!config?.enabled || !config.publicKey) return false
  const registration = await registerWorker()
  if (!registration) return false
  try {
    // subscribe() requires an *active* registration; register() resolves as
    // soon as the script is fetched, which may still be installing.
    await navigator.serviceWorker.ready
    const applicationServerKey = urlBase64ToUint8Array(config.publicKey)
    let subscription = await registration.pushManager.getSubscription()
    // A reused localhost port can now point at another server/KI_HOME. Push
    // subscriptions belong to the origin, but only their original VAPID key
    // can send to them; re-POSTing one with an old key cannot heal delivery.
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

/**
 * Drop the push subscription with the server and the browser. Called when the
 * notification toggle goes off; failures are ignored because the browser's
 * unsubscribe is the authoritative half.
 */
export async function dropPushSubscription(api: Client): Promise<void> {
  if (!pushSupported()) return
  try {
    const registration = await navigator.serviceWorker.getRegistration('/')
    const subscription = await registration?.pushManager.getSubscription()
    if (!subscription) return
    await api.deletePushSubscription(subscription.endpoint).catch(() => {})
    await subscription.unsubscribe()
  } catch {
    // Nothing to drop.
  }
}
