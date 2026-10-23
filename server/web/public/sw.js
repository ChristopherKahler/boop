// Boop service worker: turns Web Push messages into notifications, handles
// their buttons, and shows a small offline page when the hub is unreachable.
// Kept dependency-free on purpose; it is served as-is from /sw.js.

const CACHE = 'boop-shell-v1'
const OFFLINE = '/offline.html'
const ICON = '/icons/icon-192.png'
const BADGE = '/icons/badge-96.png'

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(CACHE)
      .then((c) => c.addAll([OFFLINE, ICON, BADGE]))
      .then(() => self.skipWaiting()),
  )
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      for (const key of await caches.keys()) if (key !== CACHE) await caches.delete(key)
      await self.clients.claim()
    })(),
  )
})

// Page loads always go to the hub; only when it cannot be reached at all is
// the cached offline page shown. API calls are never cached.
self.addEventListener('fetch', (event) => {
  if (event.request.mode !== 'navigate') return
  event.respondWith(fetch(event.request).catch(async () => (await caches.match(OFFLINE)) || Response.error()))
})

self.addEventListener('push', (event) => {
  let p = {}
  try {
    p = event.data ? event.data.json() : {}
  } catch {
    p = { title: 'Boop', body: event.data ? event.data.text() : '' }
  }
  const all = Array.isArray(p.actions) ? p.actions : []
  const max = (self.Notification && Notification.maxActions) || 2
  const links = {}
  for (const a of all) if (a.url) links[a.action] = a.url
  event.waitUntil(
    self.registration.showNotification(p.title || 'Boop', {
      body: p.body || '',
      tag: p.tag || undefined,
      renotify: Boolean(p.tag && p.renotify),
      requireInteraction: Boolean(p.require_interaction),
      icon: ICON,
      badge: BADGE,
      timestamp: p.ts || Date.now(),
      actions: all.slice(0, max).map((a) => ({ action: a.action, title: a.title })),
      data: { url: p.url || '/', ack: p.ack || '', links },
    }),
  )
})

self.addEventListener('notificationclick', (event) => {
  const n = event.notification
  const d = n.data || {}
  n.close()
  if (event.action === 'ack' && d.ack) {
    event.waitUntil(acknowledge(n, d))
    return
  }
  const url = (event.action && d.links && d.links[event.action]) || d.url || '/'
  event.waitUntil(openPage(url))
})

async function acknowledge(n, d) {
  try {
    const res = await fetch('/api/v1/push/action', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token: d.ack }),
    })
    if (!res.ok) throw new Error('HTTP ' + res.status)
    await self.registration.showNotification('Acknowledged', {
      body: n.title,
      tag: n.tag || undefined,
      renotify: false,
      silent: true,
      icon: ICON,
      badge: BADGE,
      data: { url: d.url },
    })
  } catch {
    await self.registration.showNotification('Could not acknowledge', {
      body: 'Open the alert to try again: ' + n.title,
      tag: n.tag || undefined,
      icon: ICON,
      badge: BADGE,
      data: { url: d.url },
    })
  }
}

// Focus the open app and route it to url, or open a new window. Links to
// other sites (an event's own action buttons) open in the browser.
async function openPage(url) {
  const target = new URL(url, self.location.origin)
  if (target.origin === self.location.origin) {
    const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
    for (const w of wins) {
      if (new URL(w.url).origin !== self.location.origin) continue
      w.postMessage({ type: 'navigate', path: target.pathname + target.search })
      return w.focus()
    }
  }
  return self.clients.openWindow(target.href)
}

// The browser rotated the subscription: subscribe again with the same key and
// tell the hub (the app's login cookie rides along on this same-origin call).
self.addEventListener('pushsubscriptionchange', (event) => {
  event.waitUntil(
    (async () => {
      const info = await fetch('/api/v1/webpush').then((r) => r.json())
      if (!info.public_key) return
      const sub = await self.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(info.public_key) })
      await fetch('/api/v1/webpush/subscriptions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ subscription: sub.toJSON(), name: 'Web app' }),
      })
    })(),
  )
})

function urlBase64ToUint8Array(s) {
  const pad = '='.repeat((4 - (s.length % 4)) % 4)
  const raw = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'))
  return Uint8Array.from(raw, (c) => c.charCodeAt(0))
}
