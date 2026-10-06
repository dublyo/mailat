// Mailat service worker: web push only. No fetch handler and no caching, so
// the app is always loaded from the network.

self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()))

self.addEventListener('push', event => {
  let data = {}
  try {
    data = event.data ? event.data.json() : {}
  } catch {
    return
  }
  if (data.type !== 'new_email' || typeof data.uuid !== 'string') return
  const title = data.from || 'New message'
  event.waitUntil(self.registration.showNotification(title, {
    body: data.subject || '(no subject)',
    tag: data.uuid,
    icon: '/logo.jpg',
    badge: '/favicon.jpg',
    data: { url: '/inbox?message=' + encodeURIComponent(data.uuid) },
  }))
})

self.addEventListener('notificationclick', event => {
  event.notification.close()
  const url = new URL((event.notification.data && event.notification.data.url) || '/inbox', self.location.origin)
  if (url.origin !== self.location.origin) return
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
    const client = windows.find(c => new URL(c.url).origin === url.origin)
    if (client) {
      // The open app routes in place instead of reloading.
      client.postMessage({ type: 'mailat:open', url: url.pathname + url.search })
      return client.focus()
    }
    return self.clients.openWindow(url.href)
  })())
})
