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
    tag: 'ki-run-' + data.sessionId,
    data: { sessionId: data.sessionId },
    icon: '/icon-192.png',
    badge: '/icon-192.png',
  })
}

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
