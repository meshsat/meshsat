// Answers every request a page makes, without a server process.
//
// Fixture mode: the built web app (web/dist) is read from disk and /api is
// answered from the synthetic fixtures in fixtures/rich. Live mode: requests
// go to a real Bridge, read-only. Either way nothing else on the network is
// reached: map tiles get one dark tile, everything else is aborted.
import fs from 'node:fs'
import path from 'node:path'

export const ORIGIN = 'http://localhost:6050'

const MIME = {
  '.html': 'text/html', '.js': 'text/javascript', '.mjs': 'text/javascript', '.css': 'text/css',
  '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.jpg': 'image/jpeg',
  '.webp': 'image/webp', '.ico': 'image/x-icon', '.woff2': 'font/woff2', '.woff': 'font/woff',
  '.wasm': 'application/wasm', '.webmanifest': 'application/manifest+json', '.txt': 'text/plain'
}

// One 1x1 tile in the panel's surface colour, stretched to 256 px by Leaflet.
const TILE = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGMQFZUGAACIAEYCjKTAAAAAAElFTkSuQmCC',
  'base64')
const TILE_HOSTS = /(^|\.)basemaps\.cartocdn\.com$|(^|\.)tile\.openstreetmap\.org$/

// Event streams: a 204 closes an EventSource for good, so nothing reconnects.
const STREAMS = new Set(['events', 'spectrum/stream'])

/** Load the fixtures of one profile. `empty` is `rich` with every list emptied. */
export function loadFixtures (dir, profile) {
  const out = new Map()
  for (const f of fs.readdirSync(dir)) {
    if (!f.endsWith('.json')) continue
    const data = JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'))
    out.set(f.slice(0, -5), profile === 'empty' ? emptied(data) : data)
  }
  return out
}

export function emptied (v) {
  if (Array.isArray(v)) return []
  if (v && typeof v === 'object') {
    const o = {}
    for (const [k, x] of Object.entries(v)) o[k] = emptied(x)
    // A list's companion count follows the list.
    for (const k of ['count', 'total', 'num_nodes']) if (typeof o[k] === 'number') o[k] = 0
    return o
  }
  return v
}

/** Fixture mode: the built app from `dist`, /api from `fixtures`. */
export async function routeFixtures (ctx, { dist, fixtures, missing }) {
  const indexHtml = fs.readFileSync(path.join(dist, 'index.html'))
  await ctx.route('**/*', async (route) => {
    const req = route.request()
    const url = new URL(req.url())
    if (TILE_HOSTS.test(url.hostname)) return route.fulfill({ status: 200, contentType: 'image/png', body: TILE })
    if (url.origin !== ORIGIN) return route.abort()

    if (url.pathname.startsWith('/api/')) {
      const p = url.pathname.slice('/api/'.length).replace(/\/+$/, '')
      if (req.method() !== 'GET') {
        return route.fulfill({ status: 405, contentType: 'application/json', body: '{"error":"read-only accessibility run"}' })
      }
      if (STREAMS.has(p)) return route.fulfill({ status: 204, body: '' })
      const key = p.replace(/\//g, '_')
      if (fixtures.has(key)) {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(fixtures.get(key)) })
      }
      missing?.add(p)
      return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"not found"}' })
    }
    if (url.pathname === '/health') {
      return route.fulfill({ status: 200, contentType: 'application/json', body: '{"status":"healthy"}' })
    }

    const rel = decodeURIComponent(url.pathname).replace(/^\/+/, '')
    const file = path.join(dist, rel)
    if (rel && file.startsWith(dist) && fs.existsSync(file) && fs.statSync(file).isFile()) {
      return route.fulfill({ status: 200, contentType: MIME[path.extname(file)] || 'application/octet-stream', body: fs.readFileSync(file) })
    }
    // History-mode routes (/map, /settings) load the app; a missing asset is a 404.
    if (path.extname(rel)) return route.fulfill({ status: 404, body: '' })
    return route.fulfill({ status: 200, contentType: 'text/html', body: indexHtml })
  })
}

/**
 * Live mode: a real Bridge at `base` (http://host:6050), read-only. Every
 * request that is not a GET is refused here, in the browser, before it
 * leaves; the one exception is the token login when a token is given.
 */
export async function routeLive (ctx, { base, shapes }) {
  const kit = new URL(base)
  await ctx.route('**/*', async (route) => {
    const req = route.request()
    const url = new URL(req.url())
    if (TILE_HOSTS.test(url.hostname)) return route.fulfill({ status: 200, contentType: 'image/png', body: TILE })
    if (url.host !== kit.host) return route.abort()
    const isLogin = url.pathname === '/api/auth/login' && req.method() === 'POST'
    if (req.method() !== 'GET' && !isLogin) {
      return route.fulfill({ status: 418, contentType: 'application/json', body: '{"error":"read-only accessibility run"}' })
    }
    if (!shapes || !url.pathname.startsWith('/api/')) return route.continue()
    const p = url.pathname.slice('/api/'.length).replace(/\/+$/, '')
    if (STREAMS.has(p)) return route.continue()
    try {
      const res = await route.fetch()
      if (res.ok() && (res.headers()['content-type'] || '').includes('json')) {
        try { shapes.set(p.replace(/\//g, '_'), keyShape(JSON.parse(await res.text()))) } catch { /* not JSON after all */ }
      }
      return route.fulfill({ response: res })
    } catch {
      return route.abort()
    }
  })
}

/** The key structure of a JSON value, without any of its data. */
export function keyShape (v) {
  if (Array.isArray(v)) return v.length ? [keyShape(v[0])] : []
  if (v && typeof v === 'object') {
    const o = {}
    for (const k of Object.keys(v).sort()) o[k] = keyShape(v[k])
    return o
  }
  return v === null ? null : typeof v
}
