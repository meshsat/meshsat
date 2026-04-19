/* MeshSat service worker. [MESHSAT-581]
 *
 * The bridge serves the SPA on the same origin as the REST API, so
 * we keep the policy conservative:
 *
 *   /api/*               → always network (never cached)
 *   /events, /api/spectrum/stream etc. → always network (SSE)
 *   index.html, navigation requests    → network-first (fall back
 *                                       to cached shell only when
 *                                       offline; otherwise we would
 *                                       pin an old bundle name
 *                                       after a deploy)
 *   every other GET      → stale-while-revalidate (hashed assets —
 *                          safe because filenames change per build)
 */

const CACHE = 'meshsat-shell-v4'
const ASSETS = [
  '/manifest.webmanifest',
  '/logo.png',
  '/logo-nav.png',
  '/logo-bg.png'
]

self.addEventListener('install', (ev) => {
  ev.waitUntil(
    caches.open(CACHE).then(c => c.addAll(ASSETS)).catch(() => null)
  )
  self.skipWaiting()
})

self.addEventListener('activate', (ev) => {
  ev.waitUntil(
    caches.keys().then(keys =>
      Promise.all(keys.filter(k => k !== CACHE).map(k => caches.delete(k)))
    )
  )
  self.clients.claim()
})

self.addEventListener('fetch', (ev) => {
  const url = new URL(ev.request.url)

  // Never touch the API, SSE streams, or any stream endpoint.
  if (url.pathname.startsWith('/api/')) return
  if (url.pathname.startsWith('/events')) return
  if (url.pathname.endsWith('/stream')) return

  if (ev.request.method !== 'GET') return

  // Navigation requests (SPA routing) + the bare index.html: always
  // prefer the network so a deploy takes effect on the next load.
  if (ev.request.mode === 'navigate' || url.pathname === '/' || url.pathname === '/index.html') {
    ev.respondWith(
      fetch(ev.request).catch(() =>
        caches.match('/') || caches.match(ev.request)
      )
    )
    return
  }

  // Everything else: stale-while-revalidate. Hashed asset names
  // (index-XXXXXX.js) are immutable per build, so caching them
  // aggressively is safe.
  ev.respondWith(
    caches.open(CACHE).then(cache =>
      cache.match(ev.request).then(cached => {
        const network = fetch(ev.request).then(res => {
          if (res && res.status === 200 && res.type === 'basic') {
            cache.put(ev.request, res.clone())
          }
          return res
        }).catch(() => cached)
        return cached || network
      })
    )
  )
})
