/**
 * MeshSat API Client
 *
 * Simple unauthenticated fetch wrapper for standalone deployment.
 * All endpoints are relative to /api on the same origin.
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

  const opts = {
    method,
    headers: { 'Content-Type': 'application/json' }
  }
  if (body && method !== 'GET') {
    opts.body = JSON.stringify(body)
  }

  const res = await fetch(url, opts)
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

  /** Upload a file via multipart/form-data. */
  async upload(path, file, fields = {}) {
    const form = new FormData()
    form.append('file', file)
    for (const [k, v] of Object.entries(fields)) {
      form.append(k, v)
    }
    const res = await fetch(`${BASE}${path}`, { method: 'POST', body: form })
    if (!res.ok) {
      const text = await res.text().catch(() => '')
      throw new Error(text || `HTTP ${res.status}`)
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
