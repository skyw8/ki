// ki service worker: Web Push completion notifications.
//
// It deliberately has no fetch handler. The SPA and its API must always reach
// the network — an offline cache would only serve a stale UI behind a live
// session, and session data must never be cached on a shared device.
//
// The notification rule mirrors the page's (src/lib/notifications.ts): a run
// that finishes while a *focused* ki tab is on screen is left to that tab,
// which learns about it over its own push stream. The worker only sees whether
// some window client is focused, not which session it shows, but that is
// enough: a focused page holds a live push stream for every session and already
// applies the exact-session rule there. If no window is focused (phone locked,
// app switched, tab closed), the push is the only signal left.

self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()))
self.addEventListener('push', event => event.waitUntil(handlePush(event)))
self.addEventListener('notificationclick', event => event.waitUntil(handleNotificationClick(event)))

async function handlePush(event) {
  let data = null
  try {
    data = event.data ? event.data.json() : null
  } catch {
    data = null
  }
  if (!data || data.type !== 'run_complete' || !data.sessionId) return

  const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
  if (windows.some(client => client.focused && client.visibilityState === 'visible')) return

  await self.registration.showNotification(data.title || 'ki', {
    body: notificationBody(data),
    // Same tag as the page's Notification: a completion observed by both the
    // page and the worker collapses into one notification instead of stacking.
    tag: 'ki-run-' + data.sessionId,
    data: { sessionId: data.sessionId },
    icon: '/icon-192.png',
    badge: '/icon-192.png',
  })
}

// notificationBody mirrors the page's notify.doneBody / notify.doneBodyDir
// strings, which are not reachable from the worker's own script.
function notificationBody(data) {
  const language = (self.navigator && self.navigator.language) || ''
  const done = language.toLowerCase().startsWith('zh') ? '会话已完成' : 'Session finished'
  return data.cwd ? data.cwd + ' · ' + done : done
}

async function handleNotificationClick(event) {
  event.notification.close()
  const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
  const existing = windows.find(client => client.url.startsWith(self.registration.scope))
  if (existing) {
    await existing.focus()
    return
  }
  await self.clients.openWindow('/')
}
