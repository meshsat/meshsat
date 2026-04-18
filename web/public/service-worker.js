/* MeshSat service worker — offline shell + stale-while-revalidate
 * fetch strategy. [MESHSAT-581]
 *
 * The bridge serves the SPA on the same origin as the REST API so
 * we keep the policy conservative: cache the SPA shell + static
 * assets for offline boot, but NEVER cache /api/* — every API call
 * must hit live state. Service worker version string is embedded
 * so a Vite rebuild invalidates old caches on first load.
 */

const CACHE = 'meshsat-shell-v2'
const SHELL = [
  '/',
  '/index.html',
  '/manifest.webmanifest',
  '/logo.png',
  '/logo-nav.png',
  '/logo-bg.png'
]

self.addEventListener('install', (ev) => {
  ev.waitUntil(
    caches.open(CACHE).then(c => c.addAll(SHELL)).catch(() => null)
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
  // Never cache the API; bridge telemetry, deliveries, and live
  // state must always be fresh.
  if (url.pathname.startsWith('/api/')) return
  // Never cache SSE / streaming endpoints.
  if (url.pathname.startsWith('/events')) return

  // Stale-while-revalidate for everything else (SPA shell + assets).
  if (ev.request.method !== 'GET') return
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
