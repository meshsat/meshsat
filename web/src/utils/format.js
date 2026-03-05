/**
 * Shared formatting utilities for MeshSat dashboard.
 * Import these instead of duplicating inline functions in each view.
 */

// ── Time formatting ──

export function formatRelativeTime(val) {
  if (!val) return 'N/A'
  const ts = typeof val === 'string' ? new Date(val).getTime() : (val < 1e12 ? val * 1000 : val)
  const diff = Math.floor((Date.now() - ts) / 1000)
  if (diff < 0) return 'just now'
  if (diff < 60) return `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

export function formatLastHeard(val) {
  if (!val) return 'Never'
  const ts = typeof val === 'number' && val < 1e12 ? val * 1000 : val
  const d = new Date(ts)
  if (isNaN(d.getTime())) return String(val)
  const diff = Math.floor((Date.now() - d.getTime()) / 1000)
  if (diff < 60) return `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  if (diff < 604800) return `${Math.floor(diff / 86400)}d ago`
  return d.toLocaleDateString()
}

export function formatTimestamp(val) {
  if (!val) return 'N/A'
  const ts = typeof val === 'string' ? new Date(val) : new Date(val < 1e12 ? val * 1000 : val)
  if (isNaN(ts.getTime())) return String(val)
  return ts.toISOString().replace('T', ' ').slice(0, 19) + 'Z'
}

export function formatTimeShort(ts) {
  if (!ts) return ''
  const d = new Date(ts)
  if (isNaN(d.getTime())) return String(ts)
  return d.toISOString().slice(11, 19) + 'Z'
}

export function formatTimeHHMM(unix) {
  if (!unix) return ''
  return new Date(unix * 1000).toISOString().slice(11, 16)
}

export function formatUptime(secs) {
  if (!secs || secs <= 0) return 'N/A'
  const d = Math.floor(secs / 86400)
  const h = Math.floor((secs % 86400) / 3600)
  const m = Math.floor((secs % 3600) / 60)
  if (d > 0) return `${d}d ${h}h ${m}m`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

export function formatDuration(min) {
  if (!min) return ''
  return `${Math.round(min)}m`
}

export function formatAccuracy(km) {
  if (km == null) return ''
  if (km < 1) return `${(km * 1000).toFixed(0)}m`
  return `${km.toFixed(0)}km`
}

// ── Node helpers ──

export function shortId(id) {
  if (!id) return ''
  if (id.startsWith('!') && id.length > 6) return id.slice(0, 3) + '..' + id.slice(-4)
  return id
}

export function isNodeActive(node, nowSec) {
  if (!node.last_heard) return false
  return (nowSec - node.last_heard) < 7200
}

export function isNodeRecent(node, nowSec) {
  if (!node.last_heard) return false
  return (nowSec - node.last_heard) < 86400
}

export function signalQualityClass(q) {
  if (!q) return 'text-gray-600'
  const u = q.toUpperCase()
  if (u === 'GOOD') return 'text-emerald-400'
  if (u === 'FAIR') return 'text-amber-400'
  return 'text-red-400'
}

export function nodeStatusDot(node, nowSec) {
  if (isNodeActive(node, nowSec)) return 'bg-emerald-400'
  if (isNodeRecent(node, nowSec)) return 'bg-amber-400'
  return 'bg-gray-600'
}

// ── Transport / Message helpers ──

export function transportBadge(t) {
  if (t === 'radio') return { label: 'Mesh', cls: 'bg-emerald-500/20 text-emerald-400 border-emerald-500/30' }
  if (t === 'iridium') return { label: 'Sat', cls: 'bg-blue-500/20 text-blue-400 border-blue-500/30' }
  if (t === 'mqtt') return { label: 'MQTT', cls: 'bg-purple-500/20 text-purple-400 border-purple-500/30' }
  return { label: t || '?', cls: 'bg-gray-500/20 text-gray-400 border-gray-500/30' }
}

export function portnumLabel(pn, name) {
  if (pn === 1) return null
  if (pn === 3 || name === 'POSITION_APP') return 'Position'
  if (pn === 4 || name === 'NODEINFO_APP') return 'Node Info'
  if (pn === 67 || name === 'TELEMETRY_APP') return 'Telemetry'
  if (pn === 8 || name === 'WAYPOINT_APP') return 'Waypoint'
  if (pn === 70 || name === 'TRACEROUTE_APP') return 'Traceroute'
  return name || `Port ${pn}`
}

// ── Queue / Bridge helpers ──

export function priorityLabel(p) {
  return p === 0 ? 'Critical' : p === 2 ? 'Low' : 'Normal'
}

export function priorityColor(p) {
  return p === 0 ? 'text-red-400' : p === 2 ? 'text-gray-500' : 'text-amber-400'
}

export function gatewayStatusColor(gw) {
  if (!gw) return 'text-gray-500'
  if (gw.connected) return 'text-emerald-400'
  return 'text-red-400'
}

export function gatewayStatusLabel(gw) {
  if (!gw) return 'Not Configured'
  if (gw.connected) return 'Connected'
  return 'Disconnected'
}
