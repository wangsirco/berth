// Berth service worker: shows Web Push alerts and opens the panel when one is clicked.
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()))

self.addEventListener('push', (e) => {
  let m = {}
  try { m = e.data ? e.data.json() : {} } catch { m = { title: 'Berth', body: e.data ? e.data.text() : '' } }
  e.waitUntil(self.registration.showNotification(m.title || 'Berth', {
    body: m.body || '',
    tag: m.tag || undefined,
    renotify: !!m.tag,
    icon: '/berth-app-icon-192.png',
    badge: '/icon.svg',
    requireInteraction: m.severity === 'critical',
    data: { url: m.url || '/' },
  }))
})

self.addEventListener('notificationclick', (e) => {
  e.notification.close()
  const url = new URL((e.notification.data && e.notification.data.url) || '/', self.location.origin)
  if (url.origin !== self.location.origin) return
  e.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
    for (const c of list) {
      if (new URL(c.url).origin === url.origin && 'focus' in c) { c.navigate(url.href); return c.focus() }
    }
    return self.clients.openWindow(url.href)
  }))
})
