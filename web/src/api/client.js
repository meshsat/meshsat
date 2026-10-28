/**
 * MeshSat API Client
 *
 * Fetch wrapper for standalone deployment. All endpoints are relative to
 * /api on the same origin.
 *
 * The API token [MESHSAT-1738]: a kit may require one (MESHSAT_API_TOKEN).
 * The token lives in localStorage `meshsat.apiToken` and goes out as
 * `Authorization: Bearer` on every request here. A 401 fires a
 * `meshsat:unauthorized` event on window, once per lock-out, and the
 * ApiLogin overlay in App.vue asks for the token; login() hands it to
 * POST /api/auth/login, which sets the cookie the event stream needs
 * (an EventSource cannot carry a header). A kit that needs no token
 * never answers 401, so none of this shows: the kiosk on the kit itself
 * is trusted by the Bridge and never sees it either.
 */

const BASE = '/api'
const TOKEN_KEY = 'meshsat.apiToken'

export function apiToken() {
  try { return window.localStorage?.getItem(TOKEN_KEY) || '' } catch { return '' }
}

export function setApiToken(token) {
  try {
    if (token) window.localStorage?.setItem(TOKEN_KEY, token)
    else window.localStorage?.removeItem(TOKEN_KEY)
  } catch { /* private mode */ }
}

function authHeaders(token = apiToken()) {
  return token ? { Authorization: `Bearer ${token}` } : {}
}

// One event per lock-out: every poll in flight gets its 401, the overlay
// must open once. login() re-arms it.
let unauthorizedTold = false

function tellUnauthorized() {
  if (unauthorizedTold || typeof window === 'undefined') return
  unauthorizedTold = true
  window.dispatchEvent(new CustomEvent('meshsat:unauthorized'))
}

// `timeoutMs` aborts the fetch so a caller that must not wait forever (the
// booth route selector during a bridge restart) gets an error instead of
// a hung promise. [MESHSAT-826]
//
// `headers` adds request headers (Idempotency-Key on a send). A failed
// request's error carries `status` and the parsed `body`, so a caller can
// show what the bridge said per route instead of the raw JSON; a request
// that never got an answer has no `status`. [MESHSAT-1584]
async function request(method, path, body = null, params = null, timeoutMs = 0, headers = null) {
  let url = `${BASE}${path}`
  if (params) {
    const qs = new URLSearchParams()
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== null && v !== '') qs.set(k, v)
    }
    const s = qs.toString()
    if (s) url += `?${s}`
  }

  const opts = {
    method,
    headers: { 'Content-Type': 'application/json', ...authHeaders(), ...(headers || {}) }
  }
  if (body && method !== 'GET') {
    opts.body = JSON.stringify(body)
  }
  if (timeoutMs > 0 && typeof AbortSignal !== 'undefined' && AbortSignal.timeout) {
    opts.signal = AbortSignal.timeout(timeoutMs)
  }

  const res = await fetch(url, opts)
  if (!res.ok) {
    if (res.status === 401) tellUnauthorized()
    const text = await res.text().catch(() => '')
    let msg = text || `HTTP ${res.status}`
    let parsed = null
    try { parsed = JSON.parse(text); if (parsed.error) msg = parsed.error } catch {}
    const err = new Error(msg)
    err.status = res.status
    err.body = parsed
    throw err
  }
  if (res.status === 204 || res.headers.get('content-length') === '0') return null
  const text = await res.text()
  return text ? JSON.parse(text) : null
}

export default {
  get: (path, params) => request('GET', path, null, params),
  post: (path, body, headers = null) => request('POST', path, body, null, 0, headers),
  put: (path, body, timeoutMs = 0) => request('PUT', path, body, null, timeoutMs),
  del: (path) => request('DELETE', path),
  patch: (path, body) => request('PATCH', path, body),

  /**
   * Hand a token to the Bridge: POST /api/auth/login with it as the Bearer
   * (not the stored one). A 204 means it is right: it is kept for every
   * later request, the Bridge has set the event stream's cookie, and the
   * caller reloads so every poll and stream starts over with it. A 401
   * keeps nothing. [MESHSAT-1738]
   */
  async login(token) {
    const t = (token || '').trim()
    const res = await fetch(`${BASE}/auth/login`, { method: 'POST', headers: { ...authHeaders(t) } })
    if (res.status === 401) return false
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    setApiToken(t)
    unauthorizedTold = false
    return true
  },

  /** Forget the token here and clear the Bridge's cookie. */
  async logout() {
    setApiToken('')
    try { await fetch(`${BASE}/auth/logout`, { method: 'POST' }) } catch { /* the kit may be away */ }
  },

  /** Upload a file via multipart/form-data. */
  async upload(path, file, fields = {}) {
    const form = new FormData()
    form.append('file', file)
    for (const [k, v] of Object.entries(fields)) {
      form.append(k, v)
    }
    const res = await fetch(`${BASE}${path}`, { method: 'POST', body: form, headers: { ...authHeaders() } })
    if (!res.ok) {
      if (res.status === 401) tellUnauthorized()
      const text = await res.text().catch(() => '')
      let msg = text || `HTTP ${res.status}`
      try { const j = JSON.parse(text); if (j.error) msg = j.error } catch {}
      throw new Error(msg)
    }
    const text = await res.text()
    return text ? JSON.parse(text) : null
  },

  /**
   * Open an SSE stream using native EventSource.
   * @param {string} path - API path (e.g., /events)
   * @param {Function} onEvent - Called with parsed event data
   * @param {Function} [onError] - Called on error
   * @returns {{ close: Function }} - Call close() to disconnect
   */
  sse(path, onEvent, onError) {
    const url = `${BASE}${path}`
    const source = new EventSource(url)

    source.onmessage = (e) => {
      try {
        onEvent(JSON.parse(e.data))
      } catch {
        onEvent(e.data)
      }
    }

    source.onerror = (e) => {
      if (onError) onError(e)
    }

    return {
      close: () => source.close()
    }
  }
}
