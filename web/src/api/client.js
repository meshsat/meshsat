/**
 * MeshSat API Client
 *
 * Fetch wrapper with session cookie auth support.
 * When auth is enabled, the browser sends the meshsat_session cookie automatically.
 * On 401 responses, redirects to the login page.
 */

const BASE = '/api'

async function request(method, path, body = null, params = null) {
  let url = `${BASE}${path}`
  if (params) {
    const qs = new URLSearchParams()
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== null && v !== '') qs.set(k, v)
    }
    const s = qs.toString()
    if (s) url += `?${s}`
  }

  const headers = { 'Content-Type': 'application/json' }

  // Pass tenant ID header when auth is enabled (extracted from user context by the backend)
  // This ensures the API client works correctly for service-to-service calls with API keys
  // that may set tenant via header rather than OIDC claim.

  const opts = {
    method,
    headers,
    credentials: 'same-origin' // ensures cookies are sent
  }
  if (body && method !== 'GET') {
    opts.body = JSON.stringify(body)
  }

  const res = await fetch(url, opts)

  // Handle auth failures — redirect to login
  if (res.status === 401) {
    // Check if auth is enabled before redirecting
    try {
      const statusRes = await fetch('/auth/status')
      const statusData = await statusRes.json()
      if (statusData.enabled && !statusData.authenticated) {
        window.location.href = '/login'
        return null
      }
    } catch {
      // Fall through to normal error handling
    }
  }

  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
  if (res.status === 204 || res.headers.get('content-length') === '0') return null
  const text = await res.text()
  return text ? JSON.parse(text) : null
}

export default {
  get: (path, params) => request('GET', path, null, params),
  post: (path, body) => request('POST', path, body),
  put: (path, body) => request('PUT', path, body),
  del: (path) => request('DELETE', path),
  patch: (path, body) => request('PATCH', path, body),

  /**
   * Open an SSE stream using native EventSource.
   * @param {string} path - API path (e.g., /events)
   * @param {Function} onEvent - Called with parsed event data
   * @param {Function} [onError] - Called on error
   * @returns {{ close: Function }} - Call close() to disconnect
   */
  sse(path, onEvent, onError) {
    const url = `${BASE}${path}`
    const source = new EventSource(url, { withCredentials: true })

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
