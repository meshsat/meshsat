<script setup>
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import { buildPolyline, buildAreaPath } from '@/composables/useSVGChart'
import { formatRelativeTime, formatTimestamp, formatLastHeard, formatAccuracy, formatTimeHHMM, shortId, isNodeActive, nodeStatusDot } from '@/utils/format'

const store = useMeshsatStore()

// ── Manual mailbox check ──
const dashCheckingMailbox = ref(false)
async function dashCheckMailbox() {
  dashCheckingMailbox.value = true
  try { await store.manualMailboxCheck() } catch {}
  dashCheckingMailbox.value = false
}

// ── Iridium geolocation trigger ──
const dashGeoLoading = ref(false)
async function dashTriggerGeo() {
  dashGeoLoading.value = true
  try { await store.triggerIridiumGeolocation() } catch {}
  dashGeoLoading.value = false
}

// ── Activity log ──
const activityLog = ref([])
const MAX_LOG = 50
const logPaused = ref(false)

// ── SOS state ──
const sosArming = ref(false)

// ── Stats modal ──
const statsModal = ref(false)
const statsTitle = ref('')
const statsData = ref(null)

// ── Queue item detail modal ──
const queueDetailModal = ref(false)
const queueDetailItem = ref(null)

// ── Widget drag-and-drop ──
const DEFAULT_WIDGET_ORDER = ['iridium', 'mesh', 'cellular', 'sos', 'location', 'queue', 'activity']
function loadWidgetOrder() {
  try {
    const stored = JSON.parse(localStorage.getItem('meshsat-widget-order'))
    if (Array.isArray(stored) && stored.length === DEFAULT_WIDGET_ORDER.length) return stored
  } catch { /* corrupt data */ }
  return [...DEFAULT_WIDGET_ORDER]
}
const widgetOrder = ref(loadWidgetOrder())
const dragWidget = ref(null)
const dragOver = ref(null)

function onDragStart(e, widgetId) {
  dragWidget.value = widgetId
  e.dataTransfer.effectAllowed = 'move'
  e.dataTransfer.setData('text/plain', widgetId)
}

function onDragOver(e, widgetId) {
  e.preventDefault()
  e.dataTransfer.dropEffect = 'move'
  dragOver.value = widgetId
}

function onDragLeave() {
  dragOver.value = null
}

function onDrop(e, targetId) {
  e.preventDefault()
  dragOver.value = null
  const sourceId = dragWidget.value
  if (!sourceId || sourceId === targetId) return
  const order = [...widgetOrder.value]
  const srcIdx = order.indexOf(sourceId)
  const tgtIdx = order.indexOf(targetId)
  if (srcIdx === -1 || tgtIdx === -1) return
  order.splice(srcIdx, 1)
  order.splice(tgtIdx, 0, sourceId)
  widgetOrder.value = order
  localStorage.setItem('meshsat-widget-order', JSON.stringify(order))
}

function onDragEnd() {
  dragWidget.value = null
  dragOver.value = null
}

// ── Helpers from NodesView ──
const nowSec = ref(Date.now() / 1000)

function signalDot(node) {
  return nodeStatusDot(node, nowSec.value)
}


// ── Computed: Iridium SBD panel ──
const iridiumGw = computed(() => (store.gateways || []).find(g => g.type === 'iridium'))
const satBars = computed(() => store.iridiumSignal?.bars ?? -1)
const satAssessment = computed(() => store.iridiumSignal?.assessment || 'none')
const iridiumStatus = computed(() => {
  if (!iridiumGw.value) return { dot: 'bg-gray-600', text: 'Not Configured' }
  if (iridiumGw.value.connected) return { dot: 'bg-tactical-iridium', text: 'Connected' }
  return { dot: 'bg-red-400', text: 'Disconnected' }
})
const dlqPending = computed(() => {
  // Count pending from deliveries (unified) first, fall back to DLQ
  const dels = store.deliveries || []
  if (dels.length) return dels.filter(d => d.status === 'pending' || d.status === 'held').length
  return (store.dlq || []).filter(d => d.status === 'pending' || !d.status).length
})
const lastSatTx = computed(() => {
  const sent = (store.dlq || []).filter(d => d.status === 'sent' && d.direction === 'outbound')
  if (sent.length) {
    return formatRelativeTime(sent[0].updated_at || sent[0].created_at)
  }
  return 'N/A'
})
const lastSatRx = computed(() => {
  const recv = (store.dlq || []).filter(d => d.direction === 'inbound')
  if (recv.length) {
    return formatRelativeTime(recv[0].updated_at || recv[0].created_at)
  }
  return 'N/A'
})

// Signal sparkline from history
const sparklinePoints = computed(() => {
  const hist = store.signalHistory || []
  if (hist.length < 2) return ''
  const sorted = [...hist].sort((a, b) => a.timestamp - b.timestamp)
  return buildPolyline(sorted, p => p.value, 200, 40, 0, 5)
})
const sparklineArea = computed(() => {
  const hist = store.signalHistory || []
  if (hist.length < 2) return ''
  const sorted = [...hist].sort((a, b) => a.timestamp - b.timestamp)
  return buildAreaPath(sorted, p => p.value, 200, 40, 0, 5)
})

// Read min elevation from Passes page setting (shared via localStorage)
const dashMinElev = computed(() => {
  try {
    const v = parseInt(localStorage.getItem('meshsat-min-elev'))
    if (v >= 0 && v <= 90) return v
  } catch {}
  return 5
})

// Mini pass+signal+GSS chart for widget (6h window)
const miniChartData = computed(() => {
  const W = 400, H = 80, padL = 20, padR = 20
  const now = Date.now() / 1000
  const windowSec = 6 * 3600
  const startTs = now - windowSec * 0.5
  const endTs = now + windowSec * 0.5

  function xPos(ts) { return padL + ((ts - startTs) / (endTs - startTs)) * (W - padL - padR) }
  function signalY(val) { return H - (val / 5) * H }

  function elevY(deg) { return H - (deg / 90) * H }

  // Pass triangles
  const passes = (store.passes || []).map(p => {
    const x1 = Math.max(padL, xPos(p.aos))
    const x2 = Math.min(W - padR, xPos(p.los))
    const xMid = (x1 + x2) / 2
    const peakY = elevY(p.peak_elev_deg)
    return { path: `M ${x1},${H} L ${xMid},${peakY} L ${x2},${H} Z`, x1, x2, xMid, peakY, sat: p.satellite, elev: p.peak_elev_deg, active: p.is_active }
  }).filter(p => p.x2 > padL && p.x1 < W - padR)

  // Signal line + dots
  const signals = (store.signalHistory || []).map(s => {
    const val = s.value || s.avg || 0
    return {
      x: xPos(s.timestamp || s.bucket),
      y: signalY(val),
      val
    }
  }).filter(s => s.x >= padL && s.x <= W - padR)

  let signalLine = ''
  let signalArea = ''
  if (signals.length > 1) {
    signalLine = signals.map(s => `${s.x},${s.y}`).join(' ')
    signalArea = `M ${signals[0].x},${H} L ${signals.map(s => `${s.x},${s.y}`).join(' L ')} L ${signals[signals.length - 1].x},${H} Z`
  }

  // GSS dots
  const gss = (store.gssHistory || []).map(g => ({
    x: xPos(g.timestamp || g.bucket),
    y: H - 4,
    success: (g.value || g.avg || 0) >= 1
  })).filter(g => g.x >= 0 && g.x <= W)

  // Now line
  const nowX = xPos(now)

  // Time labels
  const labels = []
  const step = 3600
  let t = Math.ceil(startTs / step) * step
  while (t < endTs) {
    labels.push({ x: xPos(t), label: formatTimeHHMM(t) })
    t += step
  }

  return { passes, signals, signalLine, signalArea, gss, nowX, labels, W, H, padL, padR }
})

// Fetch passes using resolved location + shared min elevation
async function fetchDashPasses() {
  const resolved = store.locationSources?.resolved
  if (!resolved) return
  const now = Math.floor(Date.now() / 1000)
  await store.fetchPasses({
    lat: resolved.lat, lon: resolved.lon,
    alt_m: (resolved.alt_km || 0) * 1000,
    hours: 6, min_elev: dashMinElev.value, start: now - 3 * 3600
  })
}

// Scheduler status
const schedulerMode = computed(() => store.schedulerStatus?.mode || 'legacy')
const schedulerModeName = computed(() => store.schedulerStatus?.mode_name || 'Manual')
const schedulerEnabled = computed(() => store.schedulerStatus?.enabled === true)
const schedulerNextPass = computed(() => store.schedulerStatus?.next_pass || null)
const schedulerNextTransition = computed(() => {
  const t = store.schedulerStatus?.next_transition
  if (!t) return ''
  return formatRelativeTime(t)
})

function schedulerBadgeClass(mode) {
  if (mode === 'active') return 'bg-emerald-400/10 text-emerald-400'
  if (mode === 'pre_wake') return 'bg-amber-400/10 text-amber-400'
  if (mode === 'post_pass') return 'bg-blue-400/10 text-blue-400'
  return 'bg-gray-700/50 text-gray-500' // idle or legacy
}

// Location sources
const locationResolved = computed(() => store.locationSources?.resolved || null)
const locationGps = computed(() => (store.locationSources?.sources || []).find(s => s.source === 'gps'))
const iridiumPasses = computed(() => store.locationSources?.iridium_passes || [])
const iridiumCentroid = computed(() => store.locationSources?.iridium_centroid || null)
const dashCellInfo = computed(() => store.cellInfo?.latest || store.cellInfo?.live || null)
const unackedAlerts = computed(() => (store.cellBroadcasts || []).filter(a => !a.acknowledged))

// SIM PIN state
const pinInput = ref('')
const pinUnlocking = ref(false)
const pinError = ref('')

async function unlockPIN() {
  pinError.value = ''
  pinUnlocking.value = true
  try {
    await store.submitCellularPIN(pinInput.value)
    pinInput.value = ''
  } catch (e) {
    pinError.value = e.message || 'PIN rejected'
  }
  pinUnlocking.value = false
}

function cbsAlertBg(severity) {
  if (severity === 'extreme') return 'bg-red-900/30'
  if (severity === 'severe') return 'bg-orange-900/30'
  if (severity === 'amber') return 'bg-amber-900/30'
  if (severity === 'test') return 'bg-blue-900/20'
  return 'bg-tactical-bg/50'
}

function cbsAlertColor(severity) {
  if (severity === 'extreme') return 'text-red-400'
  if (severity === 'severe') return 'text-orange-400'
  if (severity === 'amber') return 'text-amber-400'
  if (severity === 'test') return 'text-blue-400'
  return 'text-gray-400'
}


// Credits from store
const creditsToday = computed(() => store.creditSummary?.today ?? 0)
const creditsMonth = computed(() => store.creditSummary?.month ?? 0)
const dailyBudget = computed(() => store.creditSummary?.daily_budget || 0)
const monthlyBudget = computed(() => store.creditSummary?.monthly_budget || 0)


// ── Computed: Meshtastic Mesh panel ──
const radioConnected = computed(() => store.status?.connected === true)
const nodeName = computed(() => store.status?.node_name || 'Unknown')
const activeNodes = computed(() => (store.nodes || []).filter(n => isNodeActive(n, nowSec.value)))
const totalNodes = computed(() => (store.nodes || []).length)
const staleNodes = computed(() => (store.nodes || []).filter(n => !isNodeActive(n, nowSec.value)))
const staleCount = computed(() => staleNodes.value.length)
const topNodes = computed(() => {
  const sorted = [...(store.nodes || [])].sort((a, b) => (b.last_heard || 0) - (a.last_heard || 0))
  return sorted.slice(0, 6)
})
const neighborCount = computed(() => (store.neighborInfo || []).length)

// Mesh SNR sparkline — per-node SNR as bars
const meshSNRBars = computed(() => {
  const nodes = activeNodes.value.filter(n => n.snr != null && Math.abs(n.snr) < 100)
  return nodes.map(n => ({
    name: n.short_name || n.long_name || '?',
    snr: n.snr,
    // Normalize SNR from -20..+10 to 0..1
    height: Math.max(0.05, Math.min(1, (n.snr + 20) / 30))
  })).slice(0, 12)
})

// ── Mesh widget actions ──
const broadcastingPosition = ref(false)
async function dashBroadcastPosition() {
  broadcastingPosition.value = true
  try { await store.sendPosition() } catch {}
  broadcastingPosition.value = false
}

// ── Cellular signal history (local tracking + persisted seed) ──
const cellSignalHistory = ref([])
const MAX_CELL_HISTORY = 30

function seedCellSignalFromHistory() {
  const hist = store.cellularSignalHistory || []
  if (hist.length && cellSignalHistory.value.length === 0) {
    // Seed from persisted DB history (bars field from CellSignalRecorder)
    const seeded = hist.slice(-MAX_CELL_HISTORY).map(h => ({
      ts: new Date(h.recorded_at || h.timestamp).getTime(),
      val: h.bars ?? 0
    })).filter(h => h.val >= 0)
    if (seeded.length) cellSignalHistory.value = seeded
  }
}

function trackCellularSignal() {
  const bars = store.cellularSignal?.bars
  if (bars != null && bars >= 0) {
    cellSignalHistory.value.push({ ts: Date.now(), val: bars })
    if (cellSignalHistory.value.length > MAX_CELL_HISTORY) cellSignalHistory.value.shift()
  }
}

const cellSparklinePoints = computed(() => {
  const hist = cellSignalHistory.value
  if (hist.length < 2) return ''
  const W = 200, H = 72
  return hist.map((h, i) => {
    const x = (i / (hist.length - 1)) * W
    const y = H - (h.val / 5) * H
    return `${x},${y}`
  }).join(' ')
})
const cellSparklineArea = computed(() => {
  const pts = cellSparklinePoints.value
  if (!pts) return ''
  const pairs = pts.split(' ')
  const first = pairs[0]?.split(',')[0] || '0'
  const last = pairs[pairs.length - 1]?.split(',')[0] || '200'
  return `M ${first},72 L ${pts.replace(/ /g, ' L ')} L ${last},72 Z`
})
const cellSparklineNoData = computed(() => cellSignalHistory.value.length < 2)

// ── Computed: Cellular panel ──
const cellularGw = computed(() => (store.gateways || []).find(g => g.type === 'cellular'))
const cellBars = computed(() => store.cellularSignal?.bars ?? -1)
const smsTxCount = computed(() => (store.smsMessages || []).filter(m => m.direction === 'tx').length)
const smsRxCount = computed(() => (store.smsMessages || []).filter(m => m.direction === 'rx').length)
const cellStatus = computed(() => {
  // Check transport status first (direct modem connection)
  const cs = store.cellularStatus
  if (cs?.connected) return { dot: 'bg-sky-400', text: 'Connected' }
  // Check gateway status
  if (cellularGw.value?.connected) return { dot: 'bg-sky-400', text: 'Connected' }
  if (cellularGw.value) return { dot: 'bg-red-400', text: 'Disconnected' }
  // No gateway but transport has data (standalone mode)
  if (cs?.sim_state === 'READY') return { dot: 'bg-sky-400', text: 'Modem Ready' }
  if (cs?.sim_state === 'PIN_REQUIRED') return { dot: 'bg-amber-400', text: 'PIN Required' }
  if (cs?.sim_state) return { dot: 'bg-amber-400', text: cs.sim_state }
  // Modem port known but not connected — show Disconnected, not Initializing
  if (cs?.port) return { dot: 'bg-red-400', text: 'Disconnected' }
  return { dot: 'bg-gray-600', text: 'No Modem' }
})

// RSRP/RSRQ color classes (3GPP thresholds)
function cellRsrpClass(rsrp) {
  if (rsrp == null) return 'text-gray-500'
  if (rsrp >= -80) return 'text-emerald-400'  // excellent
  if (rsrp >= -90) return 'text-sky-400'      // good
  if (rsrp >= -100) return 'text-amber-400'   // fair
  return 'text-red-400'                        // poor
}
function cellRsrqClass(rsrq) {
  if (rsrq == null) return 'text-gray-500'
  if (rsrq >= -10) return 'text-emerald-400'
  if (rsrq >= -15) return 'text-sky-400'
  if (rsrq >= -20) return 'text-amber-400'
  return 'text-red-400'
}

function formatBytes(bytes) {
  if (!bytes || bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return (bytes / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0) + ' ' + units[i]
}

// ── Computed: GPS Position panel ──
// Use location sources API (not local node position which can be stale/cached)
const gpsLat = computed(() => {
  const gps = locationGps.value
  if (gps?.lat && gps.lat !== 0) return gps.lat.toFixed(6)
  return locationResolved.value?.lat?.toFixed(6) ?? 'N/A'
})
const gpsLon = computed(() => {
  const gps = locationGps.value
  if (gps?.lon && gps.lon !== 0) return gps.lon.toFixed(6)
  return locationResolved.value?.lon?.toFixed(6) ?? 'N/A'
})
const gpsAlt = computed(() => {
  const resolved = locationResolved.value
  if (resolved?.alt_km != null) return `${(resolved.alt_km * 1000).toFixed(0)}m`
  return 'N/A'
})
const gpsSats = computed(() => {
  const gpsStatus = store.locationSources?.gps
  if (gpsStatus) return gpsStatus.sats
  return 'N/A'
})
const gpsFix = computed(() => {
  const gpsStatus = store.locationSources?.gps
  if (gpsStatus) return gpsStatus.fix
  // Fallback: check if GPS source has non-zero position
  const gps = locationGps.value
  return gps && gps.lat !== 0 && gps.lon !== 0
})

// ── Computed: SBD Queue panel (filter out expired) ──
const dlqItems = computed(() => {
  return (store.dlq || []).filter(d => d.status !== 'expired').slice(0, 20)
})
const satMessages = computed(() => {
  return (store.messages || []).filter(m => m.transport === 'iridium').slice(0, 5)
})

function dlqStatusColor(status) {
  if (status === 'sent' || status === 'delivered') return 'text-emerald-400 bg-emerald-400/10'
  if (status === 'received') return 'text-blue-400 bg-blue-400/10'
  if (status === 'pending') return 'text-amber-400 bg-amber-400/10'
  if (status === 'queued' || !status) return 'text-gray-400 bg-gray-400/10'
  if (status === 'failed') return 'text-red-400 bg-red-400/10'
  if (status === 'expired') return 'text-orange-400 bg-orange-400/10'
  if (status === 'cancelled') return 'text-gray-500 bg-gray-500/10'
  return 'text-gray-400 bg-gray-400/10'
}

// ── Unified message queue (all sources, sorted by date newest first) ──
// Channel label/color mapping for deliveries
function deliveryLabel(channel) {
  if (channel?.startsWith('iridium')) return { label: 'SBD', color: 'text-tactical-iridium' }
  if (channel?.startsWith('cellular')) return { label: 'SMS', color: 'text-sky-400' }
  if (channel?.startsWith('mqtt')) return { label: 'MQTT', color: 'text-purple-400' }
  if (channel?.startsWith('astrocast')) return { label: 'ASTR', color: 'text-orange-400' }
  if (channel?.startsWith('webhook')) return { label: 'HOOK', color: 'text-pink-400' }
  if (channel?.startsWith('mesh')) return { label: 'MESH', color: 'text-emerald-400' }
  if (channel?.startsWith('zigbee')) return { label: 'ZB', color: 'text-yellow-400' }
  return { label: channel || '?', color: 'text-gray-500' }
}

function deliveryStatusColor(status) {
  if (status === 'sent' || status === 'delivered') return 'bg-emerald-400/10 text-emerald-400'
  if (status === 'pending' || status === 'held') return 'bg-amber-400/10 text-amber-400'
  if (status === 'failed' || status === 'dead') return 'bg-red-400/10 text-red-400'
  return 'bg-gray-600/20 text-gray-400'
}

const unifiedQueue = computed(() => {
  const items = []
  const dels = store.deliveries || []
  const seenSmsIds = new Set()

  // Primary source: unified delivery ledger (has all channels)
  for (const d of dels) {
    const ch = deliveryLabel(d.channel)
    const isInbound = d.visited && !d.visited.includes(d.channel)
    items.push({
      _type: d.channel?.startsWith('iridium') ? 'sbd' : d.channel?.startsWith('cellular') ? 'sms' : d.channel || 'unknown',
      _key: 'del-' + d.id,
      _time: d.updated_at || d.created_at,
      _dir: isInbound ? 'IN' : 'OUT',
      _dirClass: ch.color,
      _label: ch.label + (isInbound ? '\u2193' : '\u2191'),
      _status: d.status || 'queued',
      _statusClass: deliveryStatusColor(d.status),
      _text: d.text_preview || '(binary)',
      _opacity: d.status === 'sent' ? 'opacity-60' : '',
      _raw: d
    })
    // Track cellular deliveries so we don't double-count with smsMessages
    if (d.channel?.startsWith('cellular') && d.msg_ref) seenSmsIds.add(d.msg_ref)
  }

  // Fallback: SBD DLQ items (legacy path if deliveries not available)
  if (!dels.length) {
    for (const d of (store.dlq || []).filter(d => d.status !== 'expired')) {
      items.push({
        _type: 'sbd',
        _key: 'sbd-' + d.id,
        _time: d.updated_at || d.created_at,
        _dir: d.direction === 'inbound' ? 'IN' : 'OUT',
        _dirClass: d.direction === 'inbound' ? 'text-blue-400' : 'text-tactical-iridium',
        _label: d.direction === 'inbound' ? 'SBD\u2193' : 'SBD\u2191',
        _status: d.status === 'sent' ? 'delivered' : d.status === 'received' ? 'received' : d.status || 'queued',
        _statusClass: dlqStatusColor(d.status),
        _text: d.text_preview || '(binary)',
        _opacity: d.status === 'sent' || d.status === 'received' ? 'opacity-60' : '',
        _raw: d
      })
    }
  }

  // Always include SMS messages (not just as fallback) — skip those already in deliveries
  for (const sms of (store.smsMessages || [])) {
    if (seenSmsIds.has(String(sms.id))) continue
    items.push({
      _type: 'sms',
      _key: 'sms-' + sms.id,
      _time: sms.created_at,
      _dir: sms.direction === 'tx' ? 'OUT' : 'IN',
      _dirClass: sms.direction === 'tx' ? 'text-sky-400' : 'text-emerald-400',
      _label: sms.direction === 'tx' ? 'SMS\u2191' : 'SMS\u2193',
      _status: sms.status || 'queued',
      _statusClass: sms.status === 'sent' || sms.status === 'delivered' ? 'bg-emerald-400/10 text-emerald-400' : sms.status === 'failed' ? 'bg-red-400/10 text-red-400' : 'bg-gray-600/20 text-gray-400',
      _text: (sms.phone ? sms.phone + ': ' : '') + (sms.text || '(empty)'),
      _opacity: '',
      _raw: sms
    })
  }

  // Sort by time, newest first
  items.sort((a, b) => {
    const ta = new Date(a._time || 0).getTime()
    const tb = new Date(b._time || 0).getTime()
    return tb - ta
  })

  return items.slice(0, 30)
})

// ── Computed: SOS panel ──
const sosActive = computed(() => store.sosStatus?.active === true)

async function toggleSOS() {
  sosArming.value = true
  try {
    if (sosActive.value) {
      await store.cancelSOS()
    } else {
      await store.activateSOS()
    }
  } catch { /* store error */ }
  sosArming.value = false
}

async function testSOS() {
  try {
    await store.sendMessage({ text: 'SOS TEST', transport: 'satellite' })
  } catch { /* store error */ }
}

// ── Activity log event handler ──
function eventTag(type) {
  if (type === 'signal' || type === 'iridium' || type === 'satellite') return { label: 'IRID', color: 'bg-tactical-iridium/20 text-tactical-iridium' }
  if (type === 'message' || type === 'text') return { label: 'MSG', color: 'bg-tactical-lora/20 text-tactical-lora' }
  if (type === 'telemetry') return { label: 'TELEM', color: 'bg-amber-400/20 text-amber-400' }
  if (type === 'position') return { label: 'GPS', color: 'bg-tactical-gps/20 text-tactical-gps' }
  if (type === 'sos') return { label: 'SOS', color: 'bg-tactical-sos/20 text-tactical-sos' }
  if (type === 'node_update' || type === 'nodeinfo') return { label: 'NODE', color: 'bg-tactical-lora/20 text-tactical-lora' }
  if (type === 'rule_match') return { label: 'RULE', color: 'bg-purple-400/20 text-purple-400' }
  if (type === 'forward') return { label: 'FWD', color: 'bg-tactical-iridium/20 text-tactical-iridium' }
  if (type === 'forward_error') return { label: 'ERR', color: 'bg-red-400/20 text-red-400' }
  if (type === 'relay') return { label: 'RELAY', color: 'bg-amber-400/20 text-amber-400' }
  if (type === 'inbound') return { label: 'IN', color: 'bg-blue-400/20 text-blue-400' }
  if (type === 'cellular') return { label: 'CELL', color: 'bg-sky-400/20 text-sky-400' }
  if (type === 'mailbox') return { label: 'MAIL', color: 'bg-tactical-iridium/20 text-tactical-iridium' }
  if (type === 'scheduler') return { label: 'SCHED', color: 'bg-indigo-400/20 text-indigo-400' }
  if (type === 'dlq') return { label: 'QUEUE', color: 'bg-amber-400/20 text-amber-400' }
  return { label: 'SYS', color: 'bg-gray-700/50 text-gray-400' }
}

function humanizePortnum(msg) {
  if (!msg) return ''
  // Extract portnum from SSE message like "from !70833f60 portnum=TELEMETRY_APP"
  const match = msg.match(/portnum=(\w+)/)
  if (!match) return msg
  const portnum = match[1]
  const nodeMatch = msg.match(/from\s+(!?[a-f0-9]+)/i)
  const node = nodeMatch ? nodeMatch[1] : 'unknown'
  const labels = {
    'TELEMETRY_APP': 'telemetry (battery, voltage, channel utilization)',
    'POSITION_APP': 'position update (GPS coordinates)',
    'NODEINFO_APP': 'node info (name, hardware, role)',
    'TEXT_MESSAGE_APP': 'text message',
    'ADMIN_APP': 'admin command',
    'ROUTING_APP': 'routing data',
    'TRACEROUTE_APP': 'traceroute response',
    'WAYPOINT_APP': 'waypoint',
    'NEIGHBORINFO_APP': 'neighbor info (nearby nodes)',
    'RANGE_TEST_APP': 'range test',
    'SERIAL_APP': 'serial data',
    'STORE_FORWARD_APP': 'store & forward',
    'PAXCOUNTER_APP': 'people counter'
  }
  return `${labels[portnum] || portnum.toLowerCase().replace(/_APP$/, '')} from ${node}`
}

function telemetryFromData(data, msg) {
  if (!data) return null
  const parts = []
  const nodeId = msg?.match(/!([a-f0-9]+)/)?.[0] || ''
  if (data.battery_level > 0) parts.push(`bat ${Math.round(data.battery_level)}%`)
  if (data.voltage > 0 && data.voltage < 10) parts.push(`${data.voltage.toFixed(1)}V`)
  if (data.channel_util > 0) parts.push(`ch ${data.channel_util.toFixed(0)}%`)
  if (data.air_util_tx > 0) parts.push(`air ${data.air_util_tx.toFixed(0)}%`)
  if (data.temperature != null && data.temperature !== 0) parts.push(`${data.temperature.toFixed(1)}°C`)
  if (parts.length > 0) return `telemetry ${nodeId}: ${parts.join(', ')}`
  return null
}

function eventDescription(event) {
  const type = event?.type ?? ''
  const msg = event?.message ?? ''
  const data = event?.data || null
  if (type === 'message' || type === 'text') {
    // For telemetry messages, try to show actual values from data
    if (msg.includes('TELEMETRY') && data) {
      const desc = telemetryFromData(data, msg)
      if (desc) return desc
    }
    if (msg.includes('POSITION') && data?.latitude && data?.longitude) {
      const nodeId = msg.match(/!([a-f0-9]+)/)?.[0] || ''
      return `position ${nodeId}: ${data.latitude.toFixed(4)}, ${data.longitude.toFixed(4)}`
    }
    if (msg.includes('portnum=')) return humanizePortnum(msg)
    return msg || 'New message received'
  }
  if (type === 'telemetry') {
    const desc = telemetryFromData(data, msg)
    if (desc) return desc
    return humanizePortnum(msg) || 'Device telemetry received'
  }
  if (type === 'node_update' || type === 'nodeinfo') {
    if (msg.includes('telemetry') && data) {
      const desc = telemetryFromData(data, msg)
      if (desc) return desc
    }
    if (data?.long_name && !msg.includes('telemetry')) return `node ${data.long_name} (${data.hw_model_name || 'unknown hw'})`
    return msg || 'Node info updated'
  }
  if (type === 'position') {
    if (data?.latitude && data?.longitude) {
      const nodeId = msg.match(/!([a-f0-9]+)/)?.[0] || ''
      return `position ${nodeId}: ${data.latitude.toFixed(4)}, ${data.longitude.toFixed(4)}${data.altitude ? ` ${data.altitude}m` : ''}`
    }
    return msg || 'GPS position update received'
  }
  if (type === 'connected') return 'Meshtastic radio connected'
  if (type === 'disconnected') return 'Meshtastic radio disconnected'
  if (type === 'signal') {
    // Parse signal bar value if available
    const barMatch = msg.match(/bars?[=: ]+(\d)/i)
    if (barMatch) return `Iridium signal: ${barMatch[1]}/5 bars`
    return msg || 'Iridium signal update'
  }
  if (type === 'config_complete') return 'Radio configuration sync complete'
  if (type === 'rule_match') return msg || 'Bridge forwarding rule matched'
  if (type === 'forward') return msg || 'Message forwarded via satellite'
  if (type === 'forward_error') return msg || 'Satellite forward failed'
  if (type === 'relay') return msg || 'Cross-gateway relay'
  if (type === 'inbound') return msg || 'Inbound satellite message received'
  if (type === 'cellular') return msg || 'Cellular modem event'
  if (type === 'mailbox') return msg || 'Mailbox check completed'
  if (type === 'scheduler') return msg || 'Pass scheduler state change'
  if (type === 'dlq') return msg || 'Queue state changed'
  if (type === 'subscribed') return msg || `subscribed to MeshSat event stream`
  return msg || type || 'Event'
}

let saveLogTimer = null
function handleSSEEvent(event) {
  const type = event?.type ?? ''
  // Parse data JSON if present
  let parsedData = null
  if (event?.data) {
    try {
      parsedData = typeof event.data === 'string' ? JSON.parse(event.data) : event.data
    } catch { /* not JSON */ }
  }
  const enriched = { ...event, data: parsedData }
  activityLog.value.unshift({
    time: new Date().toISOString(),
    type,
    description: eventDescription(enriched)
  })
  if (activityLog.value.length > MAX_LOG) {
    activityLog.value.length = MAX_LOG
  }
  // Debounced save to localStorage
  if (saveLogTimer) clearTimeout(saveLogTimer)
  saveLogTimer = setTimeout(() => {
    localStorage.setItem('meshsat-activity-log', JSON.stringify(activityLog.value))
  }, 500)
}

function formatLogTime(t) {
  if (!t) return ''
  return new Date(t).toISOString().slice(11, 19)
}

// ── DLQ cancel ──
async function cancelDLQ(id) {
  try {
    await store.cancelQueueItem(id)
  } catch { /* ignore */ }
}

// ── Stats modal helpers ──
function openWidgetStats(widgetId) {
  statsTitle.value = widgetId.toUpperCase().replace('_', ' ')
  statsData.value = getWidgetDiagnostics(widgetId)
  statsModal.value = true
}

function getWidgetDiagnostics(widgetId) {
  switch (widgetId) {
    case 'iridium':
      return {
        'Gateway': iridiumGw.value || 'Not configured',
        'Signal': store.iridiumSignal || 'No data',
        'Signal History (last 10)': (store.signalHistory || []).slice(-10),
        'Scheduler': store.schedulerStatus || 'Not available',
        'Credits': store.creditSummary || 'No data',
        'Queue Depth': dlqPending.value,
        'DLQ Total': (store.dlq || []).length,
        'Last TX': lastSatTx.value,
        'Last RX': lastSatRx.value
      }
    case 'mesh':
      return {
        'Status': store.status || 'Not connected',
        'Total Nodes': totalNodes.value,
        'Active Nodes': activeNodes.value.length,
        'All Nodes': store.nodes || [],
        'Config': store.config || 'Not loaded'
      }
    case 'cellular':
      return {
        'Connection': cellStatus.value.text,
        'Model': store.cellularStatus?.model || 'N/A',
        'IMEI': store.cellularStatus?.imei || 'N/A',
        'Operator': store.cellularStatus?.operator || 'N/A',
        'Network Type': store.cellularStatus?.network_type || 'N/A',
        'SIM State': store.cellularStatus?.sim_state || 'N/A',
        'Registration': store.cellularStatus?.registration || 'N/A',
        'Signal': store.cellularSignal || 'No data',
        'Cell Info': store.cellInfo || 'No data',
        'Signal History (last 10)': (store.cellularSignalHistory || []).slice(-10),
        'SMS Messages': (store.smsMessages || []).length,
        'SMS Contacts': (store.smsContacts || []).length,
        'Cell Broadcasts': (store.cellBroadcasts || []).length,
        'Gateway': cellularGw.value || 'Not configured (standalone mode)',
        'Messages Out': cellularGw.value?.messages_out ?? (store.smsMessages || []).filter(m => m.direction === 'tx').length,
        'Messages In': cellularGw.value?.messages_in ?? (store.smsMessages || []).filter(m => m.direction === 'rx').length
      }
    case 'queue':
      return {
        'All Items (unfiltered)': store.dlq || [],
        'Stats': {
          pending: (store.dlq || []).filter(d => d.status === 'pending' || !d.status).length,
          sent: (store.dlq || []).filter(d => d.status === 'sent').length,
          failed: (store.dlq || []).filter(d => d.status === 'failed').length,
          expired: (store.dlq || []).filter(d => d.status === 'expired').length,
          cancelled: (store.dlq || []).filter(d => d.status === 'cancelled').length,
          received: (store.dlq || []).filter(d => d.status === 'received').length
        }
      }
    case 'location':
      return {
        'Resolved': locationResolved.value || 'No fix',
        'Sources': store.locationSources?.sources || [],
        'GPS Fix': gpsFix.value,
        'Satellites': gpsSats.value,
        'Altitude': gpsAlt.value,
        'Custom Locations': (store.locations || []).length
      }
    case 'sos':
      return {
        'SOS Status': store.sosStatus || { active: false },
        'GPS Fix': gpsFix.value,
        'Position': gpsFix.value ? `${gpsLat.value}, ${gpsLon.value}` : 'N/A',
        'Altitude': gpsAlt.value
      }
    case 'activity':
      return {
        'Event Count': activityLog.value.length,
        'SSE Connected': store.sseConnected,
        'Events by Type': activityLog.value.reduce((acc, e) => {
          acc[e.type] = (acc[e.type] || 0) + 1
          return acc
        }, {}),
        'Recent Events (last 10)': activityLog.value.slice(0, 10)
      }
    default:
      return {}
  }
}

function openQueueItemDetail(item) {
  // item is a unified queue entry with _type, _raw, etc.
  queueDetailItem.value = item
  queueDetailModal.value = true
}

function queueItemFlowSteps(item) {
  const steps = []
  if (item.created_at) steps.push({ label: 'Queued', time: item.created_at, active: true })
  if (item.retries > 0) steps.push({ label: `Retrying (${item.retries}x)`, time: item.updated_at, active: item.status === 'pending' })
  if (item.status === 'sent') steps.push({ label: 'Sent', time: item.updated_at, active: true })
  else if (item.status === 'failed') steps.push({ label: 'Failed', time: item.updated_at, active: false })
  else if (item.status === 'expired') steps.push({ label: 'Expired', time: item.updated_at, active: false })
  else if (item.status === 'cancelled') steps.push({ label: 'Cancelled', time: item.updated_at, active: false })
  else if (item.status === 'received') steps.push({ label: 'Received', time: item.updated_at, active: true })
  return steps
}

function toHex(str) {
  if (!str) return ''
  return [...str].map(c => c.charCodeAt(0).toString(16).padStart(2, '0')).join(' ')
}

// ── Interface selector (for multi-device widgets) ──
const selectedInterface = reactive({})
const openDropdown = ref(null) // which channel type dropdown is open

function loadSelectedInterfaces() {
  try {
    const saved = JSON.parse(localStorage.getItem('meshsat-selected-interfaces'))
    if (saved && typeof saved === 'object') Object.assign(selectedInterface, saved)
  } catch { /* ignore */ }
}

function selectInterface(channelType, ifaceId) {
  selectedInterface[channelType] = ifaceId
  localStorage.setItem('meshsat-selected-interfaces', JSON.stringify(selectedInterface))
  openDropdown.value = null
}

function toggleDropdown(channelType) {
  openDropdown.value = openDropdown.value === channelType ? null : channelType
}

function closeDropdowns() {
  openDropdown.value = null
}

const interfacesByType = computed(() => {
  const m = {}
  for (const iface of (store.interfaces || [])) {
    const t = iface.channel_type || iface.type
    if (!m[t]) m[t] = []
    m[t].push(iface)
  }
  return m
})

function widgetInterfaces(channelType) {
  return interfacesByType.value[channelType] || []
}

function widgetSelectedId(channelType) {
  const ifaces = widgetInterfaces(channelType)
  if (!ifaces.length) return null
  const sel = selectedInterface[channelType]
  if (sel && ifaces.find(i => i.id === sel)) return sel
  return ifaces[0].id
}

function widgetSelectedLabel(channelType, fallback) {
  const ifaces = widgetInterfaces(channelType)
  if (!ifaces.length) return fallback
  const selId = widgetSelectedId(channelType)
  const found = ifaces.find(i => i.id === selId)
  return found?.label || found?.id || fallback
}

const widgetTypeColors = {
  iridium: { text: 'text-red-400', hover: 'hover:text-red-300', bg: 'bg-red-400', activeBg: 'bg-red-400/10', chevron: '%23f87171' },
  mesh: { text: 'text-tactical-lora', hover: 'hover:text-emerald-300', bg: 'bg-emerald-400', activeBg: 'bg-emerald-400/10', chevron: '%2334d399' },
  cellular: { text: 'text-sky-400', hover: 'hover:text-sky-300', bg: 'bg-sky-400', activeBg: 'bg-sky-400/10', chevron: '%2338bdf8' },
}

// ── Lifecycle ──
let pollTimer = null

async function fetchAll() {
  await Promise.all([
    store.fetchStatus(),
    store.fetchNodes(),
    store.fetchMessageStats(),
    store.fetchGateways(),
    store.fetchIridiumSignalFast(),
    store.fetchDLQ(),
    store.fetchMessages({ limit: 20 }),
    store.fetchSOSStatus(),
    store.fetchSignalHistory({ from: Math.floor(Date.now() / 1000) - 6 * 3600 }),
    store.fetchGSSHistory({ from: Math.floor(Date.now() / 1000) - 6 * 3600 }),
    store.fetchCredits(),
    store.fetchSchedulerStatus(),
    store.fetchLocationSources(),
    store.fetchCellularSignal(),
    store.fetchCellularStatus(),
    store.fetchCellularSignalHistory({ from: Math.floor(Date.now() / 1000) - 6 * 3600 }),
    store.fetchDeliveries({ limit: 30 }),
    store.fetchSMSMessages({ limit: 10 }),
    store.fetchCellBroadcasts({ limit: 10 }),
    store.fetchCellInfo(),
    store.fetchCellularDataStatus(),
    store.fetchDynDNSStatus(),
    store.fetchNeighborInfo(),
    store.fetchInterfaces()
  ])
}

onMounted(() => {
  // Restore activity log from localStorage
  try {
    const saved = localStorage.getItem('meshsat-activity-log')
    if (saved) {
      const parsed = JSON.parse(saved)
      if (Array.isArray(parsed)) activityLog.value = parsed.slice(0, MAX_LOG)
    }
  } catch { /* ignore corrupt data */ }

  loadSelectedInterfaces()
  fetchAll().then(() => { seedCellSignalFromHistory(); trackCellularSignal(); fetchDashPasses() })
  store.connectSSE(handleSSEEvent)
  pollTimer = setInterval(() => {
    nowSec.value = Date.now() / 1000
    store.fetchIridiumSignalFast()
    store.fetchNodes()
    store.fetchDLQ()
    store.fetchDeliveries({ limit: 30 })
    store.fetchSchedulerStatus()
    store.fetchLocationSources()
    store.fetchCellularSignal().then(trackCellularSignal)
  }, 15000)
})

onUnmounted(() => {
  store.closeSSE()
  if (pollTimer) clearInterval(pollTimer)
  if (saveLogTimer) clearTimeout(saveLogTimer)
})

// Widget component map for drag-and-drop rendering
const widgetComponents = {
  iridium: 'iridium',
  mesh: 'mesh',
  cellular: 'cellular',
  sos: 'sos',
  location: 'location',
  queue: 'queue',
  activity: 'activity'
}

// Widget-specific grid classes
// ── Node detail modal ──
const nodeDetailModal = ref(false)
const nodeDetailData = ref(null)
const nodeDetailTelemetry = ref([])
const nodeDetailNeighbors = ref([])
const nodeDetailLoading = ref(false)

async function openNodeDetail(node) {
  nodeDetailData.value = node
  nodeDetailModal.value = true
  nodeDetailLoading.value = true
  const nodeId = node.user_id || `!${node.num.toString(16).padStart(8, '0')}`
  try {
    const data = await store.fetchTelemetry({ node: nodeId, limit: 50 })
    nodeDetailTelemetry.value = data || []
  } catch { nodeDetailTelemetry.value = [] }
  try {
    await store.fetchNeighborInfo()
    nodeDetailNeighbors.value = (store.neighborInfo || []).filter(n => n.node_id === node.num)
  } catch { nodeDetailNeighbors.value = [] }
  nodeDetailLoading.value = false
}

function widgetGridClass(id) {
  if (id === 'queue') return 'md:col-span-2 lg:col-span-1'
  if (id === 'activity') return 'md:col-span-2'
  return ''
}
</script>

<template>
  <div class="max-w-[1400px] mx-auto" @click="closeDropdowns">
    <!-- 7-Panel Grid (drag-and-drop reorderable) -->
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">

      <template v-for="wid in widgetOrder" :key="wid">

      <!-- ═══ Iridium SBD ═══ -->
      <div v-if="wid === 'iridium'"
        :class="['bg-tactical-surface rounded-lg border border-tactical-border p-4', widgetGridClass(wid), dragOver === wid ? 'ring-1 ring-tactical-iridium/40' : '']"
        draggable="true" @dragstart="onDragStart($event, wid)" @dragover="onDragOver($event, wid)" @dragleave="onDragLeave" @drop="onDrop($event, wid)" @dragend="onDragEnd">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <svg class="w-3.5 h-3.5 text-gray-600 cursor-grab active:cursor-grabbing" viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/><circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/><circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/></svg>
            <div class="relative">
              <button class="font-display font-semibold text-sm text-red-400 tracking-wide flex items-center gap-1.5 hover:text-red-300 transition-colors"
                @click.stop="toggleDropdown('iridium')">
                {{ widgetSelectedLabel('iridium', 'IRIDIUM SBD').toUpperCase() }}
                <svg class="w-3 h-3 transition-transform" :class="openDropdown === 'iridium' ? 'rotate-180' : ''" viewBox="0 0 12 8" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M1 1l5 5 5-5"/></svg>
              </button>
              <div v-if="openDropdown === 'iridium'"
                class="absolute top-full left-0 mt-1 z-50 min-w-[180px] bg-gray-900 border border-tactical-border rounded-lg shadow-xl overflow-hidden"
                @click.stop>
                <div v-for="iface in widgetInterfaces('iridium')" :key="iface.id"
                  class="flex items-center gap-2 px-3 py-2 cursor-pointer hover:bg-white/[0.06] transition-colors"
                  :class="widgetSelectedId('iridium') === iface.id ? 'bg-red-400/10' : ''"
                  @click="selectInterface('iridium', iface.id)">
                  <span class="w-1.5 h-1.5 rounded-full" :class="iface.state === 'online' ? 'bg-emerald-400' : 'bg-gray-600'" />
                  <span class="text-xs font-medium" :class="widgetSelectedId('iridium') === iface.id ? 'text-red-400' : 'text-gray-300'">{{ iface.label || iface.id }}</span>
                  <span class="text-[9px] text-gray-600 ml-auto font-mono">{{ iface.device_path || '' }}</span>
                </div>
                <div v-if="!widgetInterfaces('iridium').length"
                  class="px-3 py-2 text-[11px] text-gray-500">No interfaces configured</div>
                <div class="border-t border-tactical-border px-3 py-1.5 cursor-pointer hover:bg-white/[0.04]"
                  @click="openWidgetStats('iridium'); openDropdown = null">
                  <span class="text-[10px] text-gray-500">View diagnostics</span>
                </div>
              </div>
            </div>
          </div>
          <span class="w-2 h-2 rounded-full" :class="iridiumStatus.dot" />
        </div>

        <!-- Signal bars + sparkline -->
        <div class="flex items-center gap-3 mb-3">
          <div class="flex items-end gap-[3px] h-6">
            <span v-for="i in 5" :key="i"
              class="w-[5px] rounded-sm transition-colors"
              :class="satBars >= i ? 'bg-tactical-iridium' : 'bg-gray-700/50'"
              :style="{ height: `${6 + i * 4}px` }" />
          </div>
          <div>
            <span class="font-mono text-lg font-bold" :class="satBars >= 0 ? 'text-tactical-iridium' : 'text-gray-600'">
              {{ satBars >= 0 ? satBars : '--' }}
            </span>
            <span class="text-[10px] text-gray-500 ml-1">/5</span>
          </div>
          <span class="text-[10px] text-gray-500 uppercase">{{ satAssessment }}</span>
          <span v-if="schedulerEnabled"
            class="text-[9px] font-mono px-1.5 py-0.5 rounded ml-auto"
            :class="schedulerBadgeClass(schedulerMode)">
            {{ schedulerModeName }}
          </span>
          <span v-else class="text-[9px] font-mono px-1.5 py-0.5 rounded bg-gray-700/50 text-gray-500 ml-auto">
            Manual
          </span>
        </div>

        <!-- Signal vs Passes overlay chart (6h) -->
        <div v-if="miniChartData.passes.length > 0 || miniChartData.signalLine || miniChartData.gss.length > 0" class="mb-3">
          <svg :viewBox="`0 0 ${miniChartData.W} ${miniChartData.H + 12}`" class="w-full h-24" preserveAspectRatio="xMidYMid meet">
            <!-- Grid lines -->
            <line v-for="v in [1,2,3,4,5]" :key="'mg'+v"
              :x1="miniChartData.padL" :x2="miniChartData.W - miniChartData.padR"
              :y1="miniChartData.H - (v / 5) * miniChartData.H" :y2="miniChartData.H - (v / 5) * miniChartData.H"
              stroke="#374151" stroke-width="0.3" stroke-dasharray="2 3" />
            <!-- Pass triangles -->
            <path v-for="(p, i) in miniChartData.passes" :key="'mp'+i"
              :d="p.path" :fill="p.active ? 'rgba(165,180,252,0.35)' : 'rgba(129,140,248,0.15)'"
              :stroke="p.active ? 'rgba(165,180,252,0.5)' : 'rgba(129,140,248,0.2)'" stroke-width="0.5" />
            <!-- Pass peak labels -->
            <text v-for="(p, i) in miniChartData.passes.filter(pp => (pp.x2 - pp.x1) > 15)" :key="'mpl'+i"
              :x="p.xMid" :y="p.peakY - 3" text-anchor="middle" fill="#a5b4fc" font-size="5" opacity="0.6">
              {{ p.elev.toFixed(0) }}
            </text>
            <!-- Signal area fill -->
            <path v-if="miniChartData.signalArea" :d="miniChartData.signalArea" fill="rgba(16,185,129,0.08)" />
            <!-- Signal line -->
            <polyline v-if="miniChartData.signalLine"
              :points="miniChartData.signalLine" fill="none" stroke="#10b981" stroke-width="1.2" opacity="0.7" />
            <!-- Signal dots -->
            <circle v-for="(s, i) in miniChartData.signals" :key="'ms'+i"
              :cx="s.x" :cy="s.y" r="1.5"
              :fill="s.val >= 3 ? '#10b981' : s.val >= 1 ? '#f59e0b' : '#ef4444'" opacity="0.85" />
            <!-- GSS dots -->
            <circle v-for="(g, i) in miniChartData.gss" :key="'mg2'+i"
              :cx="g.x" :cy="g.y" r="2"
              :fill="g.success ? '#e879f9' : '#f87171'" :opacity="g.success ? 0.9 : 0.6" />
            <!-- Now line -->
            <line :x1="miniChartData.nowX" :x2="miniChartData.nowX" y1="0" :y2="miniChartData.H"
              stroke="#f59e0b" stroke-width="0.5" stroke-dasharray="2 1" opacity="0.5" />
            <!-- Time labels -->
            <text v-for="(l, i) in miniChartData.labels" :key="'ml'+i"
              :x="l.x" :y="miniChartData.H + 9" text-anchor="middle" fill="#4b5563" font-size="6">{{ l.label }}</text>
          </svg>
          <div class="flex items-center justify-between text-[8px] text-gray-600 mt-0.5">
            <span class="flex items-center gap-2">
              <span class="flex items-center gap-0.5"><svg width="8" height="6"><polygon points="0,6 4,1 8,6" fill="rgba(129,140,248,0.3)"/></svg> Pass</span>
              <span class="flex items-center gap-0.5"><span class="w-1.5 h-1.5 rounded-full bg-emerald-400 inline-block"></span> Sig</span>
              <span class="flex items-center gap-0.5"><span class="w-1.5 h-1.5 rounded-full bg-fuchsia-400 inline-block"></span> GSS</span>
            </span>
            <span>6h window</span>
          </div>
        </div>

        <!-- Status rows -->
        <div class="space-y-1.5 text-[11px]">
          <div class="flex justify-between">
            <span class="text-gray-500">Gateway</span>
            <span class="text-gray-300">{{ iridiumStatus.text }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Queue</span>
            <span :class="dlqPending > 0 ? 'text-amber-400' : 'text-gray-300'">{{ dlqPending }} pending</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Last TX</span>
            <span class="text-gray-400 font-mono">{{ lastSatTx }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Last RX</span>
            <span class="text-gray-400 font-mono">{{ lastSatRx }}</span>
          </div>
          <div v-if="schedulerEnabled && schedulerNextPass" class="flex justify-between">
            <span class="text-gray-500">Next Pass</span>
            <span class="text-gray-400 font-mono text-[10px]">
              {{ schedulerNextPass.is_active ? 'NOW' : schedulerNextTransition }}
              <span class="text-gray-600">({{ schedulerNextPass.priority }})</span>
            </span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Credits Today</span>
            <span class="font-mono" :class="dailyBudget > 0 && creditsToday >= dailyBudget ? 'text-red-400' : 'text-gray-300'">
              {{ creditsToday }}{{ dailyBudget > 0 ? `/${dailyBudget}` : '' }}
            </span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Credits Month</span>
            <span class="font-mono" :class="monthlyBudget > 0 && creditsMonth >= monthlyBudget ? 'text-red-400' : 'text-gray-300'">
              {{ creditsMonth }}{{ monthlyBudget > 0 ? `/${monthlyBudget}` : '' }}
            </span>
          </div>
        </div>

        <!-- Check Mailbox Now -->
        <button @click="dashCheckMailbox" :disabled="dashCheckingMailbox || !iridiumGw?.connected"
          class="mt-3 w-full px-3 py-1.5 rounded bg-gray-800 border border-gray-700 text-[11px] text-gray-400 hover:text-tactical-iridium hover:border-tactical-iridium/30 transition-colors disabled:opacity-40 disabled:cursor-not-allowed">
          {{ dashCheckingMailbox ? 'Checking...' : 'Check Mailbox Now' }}
        </button>

        <!-- Iridium Geolocation -->
        <button @click="dashTriggerGeo" :disabled="dashGeoLoading || !iridiumGw?.connected"
          class="mt-1.5 w-full px-3 py-1.5 rounded bg-gray-800 border border-gray-700 text-[11px] text-gray-400 hover:text-tactical-gps hover:border-tactical-gps/30 transition-colors disabled:opacity-40 disabled:cursor-not-allowed">
          {{ dashGeoLoading ? 'Querying...' : 'Satellite Geolocation' }}
        </button>
        <div v-if="store.iridiumGeolocation" class="mt-1 text-[10px] text-gray-500 font-mono">
          {{ store.iridiumGeolocation.lat?.toFixed(4) }}, {{ store.iridiumGeolocation.lon?.toFixed(4) }}
          <span v-if="store.iridiumGeolocation.accuracy" class="text-gray-600">+/-{{ store.iridiumGeolocation.accuracy }}km</span>
        </div>
      </div>

      <!-- ═══ Meshtastic Mesh ═══ -->
      <div v-if="wid === 'mesh'"
        :class="['bg-tactical-surface rounded-lg border border-tactical-border p-4', widgetGridClass(wid), dragOver === wid ? 'ring-1 ring-tactical-iridium/40' : '']"
        draggable="true" @dragstart="onDragStart($event, wid)" @dragover="onDragOver($event, wid)" @dragleave="onDragLeave" @drop="onDrop($event, wid)" @dragend="onDragEnd">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <svg class="w-3.5 h-3.5 text-gray-600 cursor-grab active:cursor-grabbing" viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/><circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/><circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/></svg>
            <div class="relative">
              <button class="font-display font-semibold text-sm text-tactical-lora tracking-wide flex items-center gap-1.5 hover:text-emerald-300 transition-colors"
                @click.stop="toggleDropdown('mesh')">
                {{ widgetSelectedLabel('mesh', 'MESHTASTIC MESH').toUpperCase() }}
                <svg class="w-3 h-3 transition-transform" :class="openDropdown === 'mesh' ? 'rotate-180' : ''" viewBox="0 0 12 8" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M1 1l5 5 5-5"/></svg>
              </button>
              <div v-if="openDropdown === 'mesh'"
                class="absolute top-full left-0 mt-1 z-50 min-w-[180px] bg-gray-900 border border-tactical-border rounded-lg shadow-xl overflow-hidden"
                @click.stop>
                <div v-for="iface in widgetInterfaces('mesh')" :key="iface.id"
                  class="flex items-center gap-2 px-3 py-2 cursor-pointer hover:bg-white/[0.06] transition-colors"
                  :class="widgetSelectedId('mesh') === iface.id ? 'bg-emerald-400/10' : ''"
                  @click="selectInterface('mesh', iface.id)">
                  <span class="w-1.5 h-1.5 rounded-full" :class="iface.state === 'online' ? 'bg-emerald-400' : 'bg-gray-600'" />
                  <span class="text-xs font-medium" :class="widgetSelectedId('mesh') === iface.id ? 'text-tactical-lora' : 'text-gray-300'">{{ iface.label || iface.id }}</span>
                  <span class="text-[9px] text-gray-600 ml-auto font-mono">{{ iface.device_path || '' }}</span>
                </div>
                <div v-if="!widgetInterfaces('mesh').length"
                  class="px-3 py-2 text-[11px] text-gray-500">No interfaces configured</div>
                <div class="border-t border-tactical-border px-3 py-1.5 cursor-pointer hover:bg-white/[0.04]"
                  @click="openWidgetStats('mesh'); openDropdown = null">
                  <span class="text-[10px] text-gray-500">View diagnostics</span>
                </div>
              </div>
            </div>
          </div>
          <span class="w-2 h-2 rounded-full" :class="radioConnected ? 'bg-emerald-400' : 'bg-red-400'" />
        </div>

        <div class="flex items-center gap-2 mb-3">
          <span class="text-xs" :class="radioConnected ? 'text-emerald-400' : 'text-red-400'">
            {{ radioConnected ? 'Connected' : 'Disconnected' }}
          </span>
          <span class="text-[10px] text-gray-500 font-mono">{{ nodeName }}</span>
        </div>

        <div class="flex items-center gap-2 mb-3">
          <span class="font-mono text-lg font-bold text-tactical-lora">{{ activeNodes.length }}</span>
          <span class="text-[10px] text-gray-500">/ {{ totalNodes }} nodes</span>
          <span v-if="staleCount > 0" class="text-[9px] font-mono px-1.5 py-0.5 rounded bg-amber-400/10 text-amber-400">
            {{ staleCount }} stale
          </span>
          <span v-if="neighborCount > 0" class="text-[9px] font-mono px-1.5 py-0.5 rounded bg-tactical-lora/10 text-tactical-lora/70">
            {{ neighborCount }} neighbors
          </span>
        </div>

        <!-- Mesh SNR bar chart (per-node) — same height as Iridium chart -->
        <div v-if="meshSNRBars.length > 0" class="mb-3">
          <svg viewBox="0 0 200 80" class="w-full h-24" preserveAspectRatio="xMidYMid meet">
            <!-- Grid lines at -20, -10, 0, +10 dB -->
            <line v-for="v in [-20, -10, 0, 10]" :key="'snrg'+v"
              x1="0" x2="200" :y1="80 - ((v + 20) / 30) * 72" :y2="80 - ((v + 20) / 30) * 72"
              stroke="#374151" stroke-width="0.3" stroke-dasharray="2 3" />
            <text v-for="v in [-20, -10, 0, 10]" :key="'snrl'+v"
              x="2" :y="80 - ((v + 20) / 30) * 72 - 2" fill="#4b5563" font-size="6">{{ v }}dB</text>
            <!-- Bars -->
            <rect v-for="(bar, i) in meshSNRBars" :key="'snrb'+i"
              :x="10 + i * (180 / meshSNRBars.length) + 2"
              :y="80 - bar.height * 72"
              :width="Math.max(4, 180 / meshSNRBars.length - 4)"
              :height="bar.height * 72"
              rx="1"
              :fill="bar.snr >= 0 ? 'rgba(16,185,129,0.5)' : bar.snr >= -10 ? 'rgba(245,158,11,0.5)' : 'rgba(239,68,68,0.5)'" />
            <!-- Node name labels -->
            <text v-for="(bar, i) in meshSNRBars" :key="'snrn'+i"
              :x="10 + i * (180 / meshSNRBars.length) + Math.max(4, 180 / meshSNRBars.length - 4) / 2 + 2"
              y="78" text-anchor="middle" fill="#6b7280" font-size="5">{{ bar.name.slice(0,4) }}</text>
          </svg>
          <div class="flex items-center justify-between text-[8px] text-gray-600 mt-0.5">
            <span>SNR per node</span>
            <span>{{ meshSNRBars.filter(b => b.snr >= 0).length }}/{{ meshSNRBars.length }} good</span>
          </div>
        </div>

        <div class="space-y-1">
          <div v-for="node in topNodes" :key="node.num"
            class="flex items-center gap-2 py-1 px-2 rounded hover:bg-white/[0.04] transition-colors cursor-pointer"
            @click="openNodeDetail(node)">
            <span class="w-1.5 h-1.5 rounded-full shrink-0" :class="signalDot(node)" />
            <span class="text-[11px] text-gray-300 truncate flex-1">{{ node.long_name || 'Unknown' }}</span>
            <span class="text-[9px] font-mono text-gray-600 shrink-0">{{ shortId(node.user_id) }}</span>
            <span v-if="node.snr != null && Math.abs(node.snr) < 100" class="text-[9px] font-mono shrink-0"
              :class="node.snr >= 0 ? 'text-emerald-400/60' : node.snr >= -10 ? 'text-amber-400/60' : 'text-red-400/60'">
              {{ Number(node.snr).toFixed(0) }}dB
            </span>
            <span class="text-[9px] text-gray-600 shrink-0">{{ formatLastHeard(node.last_heard) }}</span>
          </div>
          <div v-if="!topNodes.length" class="text-[11px] text-gray-600 text-center py-2">No nodes discovered</div>
        </div>

        <div class="flex items-center justify-between mt-2 pt-2 border-t border-tactical-border">
          <div class="flex gap-3">
            <router-link to="/nodes" class="text-[10px] text-tactical-lora/60 hover:text-tactical-lora transition-colors">
              View All Nodes
            </router-link>
            <router-link v-if="staleCount > 0" to="/nodes" class="text-[10px] text-amber-400/60 hover:text-amber-400 transition-colors">
              Manage Stale
            </router-link>
          </div>
          <button v-if="radioConnected" @click="dashBroadcastPosition" :disabled="broadcastingPosition"
            class="text-[10px] px-2 py-0.5 rounded border border-tactical-lora/30 text-tactical-lora/70 hover:bg-tactical-lora/10 transition-colors disabled:opacity-40">
            {{ broadcastingPosition ? 'Sending...' : 'Broadcast Position' }}
          </button>
        </div>
      </div>

      <!-- ═══ Cellular 4G/LTE ═══ -->
      <div v-if="wid === 'cellular'"
        :class="['bg-tactical-surface rounded-lg border border-tactical-border p-4', widgetGridClass(wid), dragOver === wid ? 'ring-1 ring-tactical-iridium/40' : '']"
        draggable="true" @dragstart="onDragStart($event, wid)" @dragover="onDragOver($event, wid)" @dragleave="onDragLeave" @drop="onDrop($event, wid)" @dragend="onDragEnd">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <svg class="w-3.5 h-3.5 text-gray-600 cursor-grab active:cursor-grabbing" viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/><circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/><circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/></svg>
            <div class="relative">
              <button class="font-display font-semibold text-sm text-sky-400 tracking-wide flex items-center gap-1.5 hover:text-sky-300 transition-colors"
                @click.stop="toggleDropdown('cellular')">
                {{ widgetSelectedLabel('cellular', 'CELLULAR MODEM').toUpperCase() }}
                <svg class="w-3 h-3 transition-transform" :class="openDropdown === 'cellular' ? 'rotate-180' : ''" viewBox="0 0 12 8" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M1 1l5 5 5-5"/></svg>
              </button>
              <div v-if="openDropdown === 'cellular'"
                class="absolute top-full left-0 mt-1 z-50 min-w-[180px] bg-gray-900 border border-tactical-border rounded-lg shadow-xl overflow-hidden"
                @click.stop>
                <div v-for="iface in widgetInterfaces('cellular')" :key="iface.id"
                  class="flex items-center gap-2 px-3 py-2 cursor-pointer hover:bg-white/[0.06] transition-colors"
                  :class="widgetSelectedId('cellular') === iface.id ? 'bg-sky-400/10' : ''"
                  @click="selectInterface('cellular', iface.id)">
                  <span class="w-1.5 h-1.5 rounded-full" :class="iface.state === 'online' ? 'bg-emerald-400' : 'bg-gray-600'" />
                  <span class="text-xs font-medium" :class="widgetSelectedId('cellular') === iface.id ? 'text-sky-400' : 'text-gray-300'">{{ iface.label || iface.id }}</span>
                  <span class="text-[9px] text-gray-600 ml-auto font-mono">{{ iface.device_path || '' }}</span>
                </div>
                <div v-if="!widgetInterfaces('cellular').length"
                  class="px-3 py-2 text-[11px] text-gray-500">No interfaces configured</div>
                <div class="border-t border-tactical-border px-3 py-1.5 cursor-pointer hover:bg-white/[0.04]"
                  @click="openWidgetStats('cellular'); openDropdown = null">
                  <span class="text-[10px] text-gray-500">View diagnostics</span>
                </div>
              </div>
            </div>
          </div>
          <span class="w-2 h-2 rounded-full" :class="cellStatus.dot" />
        </div>

        <div class="flex items-center gap-3 mb-3">
          <div class="flex items-end gap-[3px] h-6">
            <span v-for="i in 5" :key="i"
              class="w-[5px] rounded-sm transition-colors"
              :class="cellBars >= i ? 'bg-sky-400' : 'bg-gray-700/50'"
              :style="{ height: `${6 + i * 4}px` }" />
          </div>
          <div>
            <span class="font-mono text-lg font-bold" :class="cellBars >= 0 ? 'text-sky-400' : 'text-gray-600'">
              {{ cellBars >= 0 ? cellBars : '--' }}
            </span>
            <span class="text-[10px] text-gray-500 ml-1">/5</span>
          </div>
          <span v-if="store.cellularSignal?.assessment && store.cellularSignal.assessment !== 'none'"
            class="text-[10px] font-medium px-1.5 py-0.5 rounded"
            :class="{
              'bg-emerald-900/30 text-emerald-400': store.cellularSignal.assessment === 'excellent',
              'bg-sky-900/30 text-sky-400': store.cellularSignal.assessment === 'good',
              'bg-amber-900/30 text-amber-400': store.cellularSignal.assessment === 'fair',
              'bg-red-900/30 text-red-400': store.cellularSignal.assessment === 'poor'
            }">
            {{ store.cellularSignal.assessment }}
          </span>
          <span v-if="store.cellularSignal?.dbm" class="text-[10px] text-gray-600 font-mono">{{ store.cellularSignal.dbm }} dBm</span>
          <span v-if="store.cellularStatus?.network_type" class="text-[10px] text-gray-500 uppercase">
            {{ store.cellularStatus.network_type }}
          </span>
        </div>

        <!-- Signal history chart — same height as Iridium chart -->
        <div class="mb-3">
          <svg viewBox="0 0 200 84" class="w-full h-24" preserveAspectRatio="xMidYMid meet">
            <!-- Grid lines at 1-5 bars -->
            <line v-for="v in [1,2,3,4,5]" :key="'cg'+v"
              x1="0" x2="200" :y1="72 - (v / 5) * 72" :y2="72 - (v / 5) * 72"
              stroke="#374151" stroke-width="0.3" stroke-dasharray="2 3" />
            <text v-for="v in [1,3,5]" :key="'cl'+v"
              x="2" :y="72 - (v / 5) * 72 - 2" fill="#4b5563" font-size="6">{{ v }}</text>
            <!-- Area fill -->
            <path v-if="cellSparklineArea" :d="cellSparklineArea" fill="rgba(56,189,248,0.08)" />
            <!-- Line -->
            <polyline v-if="cellSparklinePoints" :points="cellSparklinePoints" fill="none" stroke="#38bdf8" stroke-width="1.2" opacity="0.7" />
            <!-- No data placeholder -->
            <text v-if="cellSparklineNoData" x="100" y="40" text-anchor="middle" fill="#4b5563" font-size="8">No signal data</text>
          </svg>
          <div class="flex items-center justify-between text-[8px] text-gray-600 mt-0.5">
            <span class="flex items-center gap-1">
              <span class="w-1.5 h-1.5 rounded-full bg-sky-400 inline-block"></span> Signal bars
            </span>
            <span v-if="cellSparklineNoData" class="text-gray-700 italic">waiting for signal data</span>
            <span v-else>{{ cellSignalHistory.length }} samples</span>
          </div>
        </div>

        <div class="space-y-1.5 text-[11px]">
          <div class="flex justify-between">
            <span class="text-gray-500">Status</span>
            <span class="text-gray-300">{{ cellStatus.text }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Operator</span>
            <span class="text-gray-300 font-mono">{{ store.cellularStatus?.operator || 'N/A' }}</span>
          </div>
          <div v-if="store.cellularStatus?.phone_number" class="flex justify-between">
            <span class="text-gray-500">Phone</span>
            <span class="text-gray-300 font-mono">{{ store.cellularStatus.phone_number }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Network</span>
            <span class="text-gray-300 font-mono">{{ store.cellularStatus?.network_type || store.cellInfo?.latest?.network_type || 'N/A' }}</span>
          </div>
          <div v-if="store.cellularStatus?.registration" class="flex justify-between">
            <span class="text-gray-500">Registration</span>
            <span class="font-mono text-[10px]"
              :class="store.cellularStatus.registration === 'registered_roaming' ? 'text-amber-400' : store.cellularStatus.registration === 'registered_home' ? 'text-emerald-400' : 'text-gray-500'">
              {{ store.cellularStatus.registration === 'registered_home' ? 'Home' : store.cellularStatus.registration === 'registered_roaming' ? 'Roaming' : store.cellularStatus.registration === 'searching' ? 'Searching...' : store.cellularStatus.registration }}
            </span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">IMEI</span>
            <span class="text-gray-400 font-mono text-[10px]">{{ store.cellularStatus?.imei || 'N/A' }}</span>
          </div>
          <div v-if="store.cellInfo?.latest" class="flex justify-between">
            <span class="text-gray-500">Cell</span>
            <span class="text-gray-400 font-mono text-[10px]">MCC{{ store.cellInfo.latest.mcc }}/MNC{{ store.cellInfo.latest.mnc }}
              <span v-if="store.cellInfo.latest.lac">LAC:{{ store.cellInfo.latest.lac }}</span>
              CID:{{ store.cellInfo.latest.cell_id }}</span>
          </div>
          <div v-if="store.cellInfo?.latest?.rsrp != null || store.cellInfo?.latest?.rsrq != null" class="flex justify-between">
            <span class="text-gray-500">RSRP/RSRQ</span>
            <span class="font-mono text-[10px]">
              <span :class="cellRsrpClass(store.cellInfo.latest.rsrp)">{{ store.cellInfo.latest.rsrp }} dBm</span>
              <span class="text-gray-600 mx-0.5">/</span>
              <span :class="cellRsrqClass(store.cellInfo.latest.rsrq)">{{ store.cellInfo.latest.rsrq }} dB</span>
            </span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">SMS Sent</span>
            <span class="text-gray-300 font-mono">{{ cellularGw?.messages_out ?? smsTxCount }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">SMS Received</span>
            <span class="text-gray-300 font-mono">{{ cellularGw?.messages_in ?? smsRxCount }}</span>
          </div>
          <div v-if="store.cellularDataStatus" class="flex justify-between">
            <span class="text-gray-500">Data</span>
            <span class="font-mono text-[10px]" :class="(store.cellularDataStatus.active || store.cellularDataStatus.connected) ? 'text-emerald-400' : 'text-gray-500'">
              {{ (store.cellularDataStatus.active || store.cellularDataStatus.connected) ? 'Connected' : 'Disconnected' }}
              <span v-if="store.cellularDataStatus.ip_address || store.cellularDataStatus.ip" class="text-gray-500 ml-1">{{ store.cellularDataStatus.ip_address || store.cellularDataStatus.ip }}</span>
            </span>
          </div>
          <div v-if="store.cellularDataStatus?.tx_bytes || store.cellularDataStatus?.rx_bytes" class="flex justify-between">
            <span class="text-gray-500">Usage</span>
            <span class="text-gray-400 font-mono text-[10px]">
              TX {{ formatBytes(store.cellularDataStatus.tx_bytes) }} / RX {{ formatBytes(store.cellularDataStatus.rx_bytes) }}
            </span>
          </div>
          <div v-if="store.dyndnsStatus" class="flex justify-between">
            <span class="text-gray-500">DynDNS</span>
            <span class="text-gray-400 font-mono text-[10px]">{{ store.dyndnsStatus.domain || 'N/A' }}
              <span :class="store.dyndnsStatus.last_update_ok ? 'text-emerald-400' : 'text-amber-400'">
                {{ store.dyndnsStatus.last_update_ok ? 'OK' : 'pending' }}
              </span>
            </span>
          </div>
        </div>

        <!-- SIM PIN Unlock (when PIN is required) -->
        <div v-if="store.cellularStatus?.sim_state === 'PIN_REQUIRED'" class="mt-3 pt-2 border-t border-tactical-border">
          <div class="flex items-center gap-2">
            <input type="password" v-model="pinInput" maxlength="8" placeholder="SIM PIN"
              class="flex-1 bg-tactical-bg border border-tactical-border rounded px-2 py-1 text-[11px] text-gray-200 font-mono" />
            <button @click="unlockPIN" :disabled="pinUnlocking"
              class="text-[10px] px-2 py-1 rounded bg-sky-500/20 text-sky-400 hover:bg-sky-500/30 disabled:opacity-50">
              {{ pinUnlocking ? 'Unlocking...' : 'Unlock' }}
            </button>
          </div>
          <div v-if="pinError" class="text-[10px] text-red-400 mt-1">{{ pinError }}</div>
        </div>

      </div>

      <!-- ═══ Emergency SOS (compact) ═══ -->
      <div v-if="wid === 'sos'"
        :class="['bg-tactical-surface rounded-lg border border-tactical-border p-4', widgetGridClass(wid), dragOver === wid ? 'ring-1 ring-tactical-iridium/40' : '']"
        draggable="true" @dragstart="onDragStart($event, wid)" @dragover="onDragOver($event, wid)" @dragleave="onDragLeave" @drop="onDrop($event, wid)" @dragend="onDragEnd">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <svg class="w-3.5 h-3.5 text-gray-600 cursor-grab active:cursor-grabbing" viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/><circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/><circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/></svg>
            <h2 class="font-display font-semibold text-sm text-tactical-sos tracking-wide cursor-pointer hover:underline" @click="openWidgetStats('sos')">EMERGENCY SOS</h2>
          </div>
          <span class="text-[10px] font-mono"
            :class="sosActive ? 'text-tactical-sos' : 'text-gray-600'">
            {{ sosActive ? 'ARMED' : 'STANDBY' }}
          </span>
        </div>

        <div class="flex items-center gap-3 mb-3">
          <svg viewBox="0 0 120 120" class="w-16 h-16 shrink-0">
            <circle cx="60" cy="60" r="54" fill="none" stroke-width="3"
              :stroke="sosActive ? '#ef4444' : '#1a2230'" />
            <circle v-if="sosActive" cx="60" cy="60" r="54" fill="none" stroke-width="3"
              stroke="#ef4444" stroke-dasharray="12 6" class="animate-spin"
              style="animation-duration: 8s;" />
            <circle cx="60" cy="60" r="44" :fill="sosActive ? '#ef444420' : '#111820'" />
            <text x="60" y="56" text-anchor="middle" font-size="22" font-weight="700"
              :fill="sosActive ? '#ef4444' : '#4b5563'" font-family="Oxanium, sans-serif">SOS</text>
            <text x="60" y="74" text-anchor="middle" font-size="9"
              :fill="sosActive ? '#ef444480' : '#374151'" font-family="JetBrains Mono, monospace">
              {{ sosActive ? 'ACTIVE' : 'READY' }}
            </text>
          </svg>
          <div class="flex-1 space-y-1.5">
            <button @click="toggleSOS" :disabled="sosArming"
              class="w-full py-2 rounded-lg text-xs font-semibold transition-all"
              :class="sosActive
                ? 'bg-tactical-sos/20 text-tactical-sos border border-tactical-sos/30 hover:bg-tactical-sos/30'
                : 'bg-gray-800 text-gray-400 border border-gray-700 hover:text-gray-200 hover:border-gray-600'">
              {{ sosArming ? '...' : sosActive ? 'CANCEL SOS' : 'ARM SOS' }}
            </button>
            <button @click="testSOS"
              class="w-full py-1 rounded text-[10px] text-gray-500 hover:text-gray-300 bg-gray-800/50 hover:bg-gray-800 transition-colors">
              Send Test
            </button>
          </div>
        </div>

        <div class="space-y-1.5 text-[11px]">
          <div class="flex justify-between">
            <span class="text-gray-500">GPS Fix</span>
            <span :class="gpsFix ? 'text-emerald-400' : 'text-red-400'">{{ gpsFix ? 'Acquired' : 'No Fix' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Position</span>
            <span class="text-gray-400 font-mono text-[10px]">
              {{ gpsFix ? `${gpsLat}, ${gpsLon}` : 'N/A' }}
            </span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Altitude</span>
            <span class="text-gray-400 font-mono">{{ gpsAlt }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Last Activation</span>
            <span class="text-gray-400 font-mono">{{ store.sosStatus?.last_activated ? formatRelativeTime(store.sosStatus.last_activated) : 'Never' }}</span>
          </div>
        </div>

        <!-- Cell Broadcast Alerts -->
        <div class="mt-3 pt-2 border-t border-tactical-border">
          <div class="flex items-center gap-1.5 mb-1">
            <span class="text-[10px] text-gray-500 uppercase tracking-wider">Cell Broadcast Alerts</span>
            <span v-if="unackedAlerts.length" class="text-[9px] font-mono px-1.5 py-px rounded bg-red-900/30 text-red-400">{{ unackedAlerts.length }}</span>
          </div>
          <div v-if="(store.cellBroadcasts || []).length" class="space-y-1">
            <div v-for="alert in (store.cellBroadcasts || []).slice(0, 5)" :key="alert.id"
              class="flex items-start gap-1.5 py-1 px-2 rounded text-[11px]"
              :class="alert.acknowledged ? 'bg-tactical-bg/30' : cbsAlertBg(alert.severity)">
              <span class="font-mono text-[9px] font-bold mt-0.5"
                :class="cbsAlertColor(alert.severity)">
                {{ alert.severity.toUpperCase() }}
              </span>
              <div class="flex-1 min-w-0">
                <div class="text-gray-200 text-[10px]">{{ alert.text || '(no text)' }}</div>
                <div class="text-[9px] text-gray-600">{{ formatRelativeTime(alert.created_at) }}</div>
              </div>
              <button v-if="!alert.acknowledged" @click="store.ackCellBroadcast(alert.id)"
                class="text-[9px] px-1.5 py-0.5 rounded bg-gray-700/50 text-gray-400 hover:text-gray-200">
                ACK
              </button>
            </div>
          </div>
          <div v-else class="text-[10px] text-gray-600 italic">
            Government emergency alerts (EU-Alert, WEA, CMAS) will appear here when received.
          </div>
        </div>
      </div>

      <!-- ═══ Unified Location ═══ -->
      <div v-if="wid === 'location'"
        :class="['bg-tactical-surface rounded-lg border border-tactical-border p-4', widgetGridClass(wid), dragOver === wid ? 'ring-1 ring-tactical-iridium/40' : '']"
        draggable="true" @dragstart="onDragStart($event, wid)" @dragover="onDragOver($event, wid)" @dragleave="onDragLeave" @drop="onDrop($event, wid)" @dragend="onDragEnd">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <svg class="w-3.5 h-3.5 text-gray-600 cursor-grab active:cursor-grabbing" viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/><circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/><circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/></svg>
            <h2 class="font-display font-semibold text-sm text-tactical-gps tracking-wide cursor-pointer hover:underline" @click="openWidgetStats('location')">LOCATION</h2>
          </div>
          <span v-if="locationResolved"
            class="text-[9px] font-mono px-1.5 py-0.5 rounded"
            :class="locationResolved.source === 'gps' ? 'bg-emerald-400/10 text-emerald-400' : 'bg-amber-400/10 text-amber-400'">
            {{ locationResolved.source.toUpperCase() }}
          </span>
          <span v-else class="text-[9px] font-mono px-1.5 py-0.5 rounded bg-gray-700/50 text-gray-500">NO FIX</span>
        </div>

        <div v-if="locationResolved" class="mb-3">
          <div class="font-mono text-sm text-gray-200">
            {{ locationResolved.lat.toFixed(6) }}, {{ locationResolved.lon.toFixed(6) }}
          </div>
          <span v-if="locationResolved.accuracy_km" class="text-[10px] text-gray-500">
            ~{{ formatAccuracy(locationResolved.accuracy_km) }} accuracy
          </span>
        </div>
        <div v-else class="mb-3 text-[11px] text-gray-600">
          No location fix from any source
        </div>

        <div class="space-y-1.5 text-[11px]">
          <div class="flex justify-between items-center">
            <div class="flex items-center gap-1.5">
              <span class="w-1.5 h-1.5 rounded-full" :class="gpsFix ? 'bg-emerald-400' : 'bg-gray-600'" />
              <span class="text-gray-500">GPS</span>
            </div>
            <span v-if="gpsFix && locationGps" class="text-gray-300 font-mono text-[10px]">
              {{ locationGps.lat.toFixed(4) }}, {{ locationGps.lon.toFixed(4) }}
              <span class="text-gray-600 ml-1">~{{ formatAccuracy(locationGps.accuracy_km) }}</span>
            </span>
            <span v-else class="text-gray-600 font-mono">No fix</span>
          </div>
          <div class="flex justify-between items-center">
            <div class="flex items-center gap-1.5">
              <span class="w-1.5 h-1.5 rounded-full bg-amber-400" />
              <span class="text-gray-500">Custom</span>
            </div>
            <span class="text-gray-400 font-mono text-[10px]">{{ (store.locations || []).length }} entries</span>
          </div>
          <div class="flex justify-between items-center">
            <div class="flex items-center gap-1.5">
              <span class="w-1.5 h-1.5 rounded-full" :class="iridiumPasses.length ? 'bg-orange-400' : 'bg-gray-600'" />
              <span class="text-gray-500">Iridium</span>
            </div>
            <span v-if="iridiumCentroid" class="text-gray-300 font-mono text-[10px]">
              {{ iridiumCentroid.lat.toFixed(4) }}, {{ iridiumCentroid.lon.toFixed(4) }}
              <span class="text-gray-600 ml-1">~{{ formatAccuracy(iridiumCentroid.accuracy_km) }}</span>
            </span>
            <span v-else-if="iridiumPasses.length" class="text-orange-400/60 font-mono text-[10px]">
              {{ iridiumPasses.length }} pass{{ iridiumPasses.length !== 1 ? 'es' : '' }} (need 3+)
            </span>
            <span v-else class="text-gray-600 font-mono text-[10px]">No passes</span>
          </div>
          <div class="flex justify-between items-center">
            <div class="flex items-center gap-1.5">
              <span class="w-1.5 h-1.5 rounded-full" :class="dashCellInfo?.cell_id ? 'bg-sky-400' : 'bg-gray-600'" />
              <span class="text-gray-500">Cellular</span>
            </div>
            <span v-if="dashCellInfo?.cell_id" class="text-gray-300 font-mono text-[10px]">
              {{ dashCellInfo.mcc }}/{{ dashCellInfo.mnc }} CID={{ dashCellInfo.cell_id }}
              <span v-if="dashCellInfo.network_type" class="text-sky-400/60 ml-1">{{ dashCellInfo.network_type }}</span>
            </span>
            <span v-else class="text-gray-600 font-mono text-[10px]">No data</span>
          </div>
        </div>

        <div class="mt-3 pt-2 border-t border-tactical-border space-y-1.5 text-[11px]">
          <div class="flex justify-between">
            <span class="text-gray-500">Satellites</span>
            <span class="text-gray-300 font-mono">{{ gpsSats }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Altitude</span>
            <span class="text-gray-300 font-mono">{{ gpsAlt }}</span>
          </div>
        </div>

        <div class="mt-2 pt-2 border-t border-tactical-border">
          <span class="text-[9px] text-gray-600">Priority: GPS (5m) > Iridium (centroid) > Cellular > Custom</span>
        </div>

        <div class="flex gap-3 mt-2">
          <router-link to="/map" class="text-[10px] text-tactical-gps/60 hover:text-tactical-gps transition-colors">
            Open Map
          </router-link>
          <router-link to="/passes" class="text-[10px] text-teal-400/60 hover:text-teal-400 transition-colors">
            Pass Predictor
          </router-link>
        </div>
      </div>

      <!-- ═══ SBD Queue ═══ -->
      <div v-if="wid === 'queue'"
        :class="['bg-tactical-surface rounded-lg border border-tactical-border p-4 flex flex-col', widgetGridClass(wid), dragOver === wid ? 'ring-1 ring-tactical-iridium/40' : '']"
        draggable="true" @dragstart="onDragStart($event, wid)" @dragover="onDragOver($event, wid)" @dragleave="onDragLeave" @drop="onDrop($event, wid)" @dragend="onDragEnd">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <svg class="w-3.5 h-3.5 text-gray-600 cursor-grab active:cursor-grabbing" viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/><circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/><circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/></svg>
            <h2 class="font-display font-semibold text-sm text-tactical-iridium tracking-wide cursor-pointer hover:underline" @click="openWidgetStats('queue')">MESSAGE QUEUE</h2>
          </div>
          <span v-if="dlqPending > 0"
            class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-amber-400/10 text-amber-400">
            {{ dlqPending }} pending
          </span>
        </div>

        <div class="space-y-1 tactical-scroll max-h-[220px] overflow-y-auto">
          <div v-for="item in unifiedQueue" :key="item._key"
            class="flex items-center gap-2 py-1.5 px-2 rounded bg-tactical-bg/50 cursor-pointer hover:bg-white/[0.04] transition-colors"
            :class="item._opacity"
            @click="openQueueItemDetail(item)">
            <span class="text-[9px] font-mono shrink-0" :class="item._dirClass">
              {{ item._label }}
            </span>
            <span v-if="item._status" class="text-[10px] font-mono px-1.5 py-px rounded"
              :class="item._statusClass">
              {{ item._status }}
            </span>
            <span class="text-[11px] text-gray-300 truncate flex-1">{{ item._text }}</span>
            <span class="text-[9px] text-gray-600 font-mono shrink-0">{{ formatRelativeTime(item._time) }}</span>
          </div>
          <div v-if="!unifiedQueue.length" class="text-[11px] text-gray-600 text-center py-3">Queue empty</div>
        </div>
      </div>

      <!-- ═══ Activity Log (col-span-2) ═══ -->
      <div v-if="wid === 'activity'"
        :class="['bg-tactical-surface rounded-lg border border-tactical-border p-4', widgetGridClass(wid), dragOver === wid ? 'ring-1 ring-tactical-iridium/40' : '']"
        @mouseenter="logPaused = true" @mouseleave="logPaused = false"
        draggable="true" @dragstart="onDragStart($event, wid)" @dragover="onDragOver($event, wid)" @dragleave="onDragLeave" @drop="onDrop($event, wid)" @dragend="onDragEnd">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <svg class="w-3.5 h-3.5 text-gray-600 cursor-grab active:cursor-grabbing" viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/><circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/><circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/></svg>
            <h2 class="font-display font-semibold text-sm text-gray-400 tracking-wide cursor-pointer hover:underline" @click="openWidgetStats('activity')">ACTIVITY LOG</h2>
          </div>
          <div class="flex items-center gap-2">
            <span v-if="logPaused" class="text-[9px] text-gray-600">PAUSED</span>
            <span class="text-[10px] text-gray-600 font-mono">{{ activityLog.length }} events</span>
          </div>
        </div>

        <div class="tactical-scroll max-h-[220px] overflow-y-auto space-y-0.5">
          <div v-for="(entry, idx) in activityLog" :key="idx"
            class="flex items-center gap-2 py-1 px-2 rounded hover:bg-white/[0.02] transition-colors">
            <span class="text-[9px] font-mono text-gray-600 shrink-0 w-14">{{ formatLogTime(entry.time) }}</span>
            <span class="text-[9px] font-medium px-1.5 py-px rounded shrink-0"
              :class="eventTag(entry.type).color">
              {{ eventTag(entry.type).label }}
            </span>
            <span class="text-[11px] text-gray-400 truncate">{{ entry.description }}</span>
          </div>
          <div v-if="!activityLog.length" class="text-center text-gray-600 text-[11px] py-6">
            Waiting for events... SSE stream active.
          </div>
        </div>
      </div>

      </template>
    </div>

    <!-- ═══ Stats Modal (Widget Diagnostics) ═══ -->
    <Teleport to="body">
      <div v-if="statsModal" class="fixed inset-0 z-[100] flex items-center justify-center bg-black/70 backdrop-blur-sm" @click.self="statsModal = false">
        <div class="bg-tactical-surface border border-tactical-border rounded-lg w-full max-w-2xl max-h-[85vh] overflow-y-auto m-4">
          <div class="sticky top-0 bg-tactical-surface border-b border-tactical-border px-4 py-3 flex items-center justify-between">
            <h3 class="font-display font-semibold text-sm text-tactical-iridium tracking-wide">{{ statsTitle }} — DIAGNOSTICS</h3>
            <button @click="statsModal = false" class="text-gray-500 hover:text-gray-300 text-lg leading-none">&times;</button>
          </div>
          <div class="p-4">
            <pre class="text-[11px] font-mono text-gray-300 whitespace-pre-wrap break-all bg-tactical-bg rounded p-3 max-h-[60vh] overflow-y-auto">{{ JSON.stringify(statsData, null, 2) }}</pre>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- ═══ Queue Item Detail Modal ═══ -->
    <Teleport to="body">
      <div v-if="queueDetailModal && queueDetailItem" class="fixed inset-0 z-[100] flex items-center justify-center bg-black/70 backdrop-blur-sm" @click.self="queueDetailModal = false">
        <div class="bg-tactical-surface border border-tactical-border rounded-lg w-full max-w-2xl max-h-[85vh] overflow-y-auto m-4">
          <div class="sticky top-0 bg-tactical-surface border-b border-tactical-border px-4 py-3 flex items-center justify-between">
            <h3 class="font-display font-semibold text-sm tracking-wide"
              :class="queueDetailItem._type === 'sms' ? 'text-sky-400' : 'text-tactical-iridium'">
              {{ queueDetailItem._type === 'sms' ? 'SMS' : queueDetailItem._type === 'sbd' ? 'SBD' : 'MESSAGE' }}
              #{{ queueDetailItem._raw?.id || '' }}
            </h3>
            <button @click="queueDetailModal = false" class="text-gray-500 hover:text-gray-300 text-lg leading-none">&times;</button>
          </div>
          <div class="p-4 space-y-4">

            <!-- ── SMS detail ── -->
            <template v-if="queueDetailItem._type === 'sms'">
              <div>
                <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">SMS Details</h4>
                <div class="grid grid-cols-2 gap-1.5 text-[11px]">
                  <div class="flex justify-between"><span class="text-gray-500">ID</span><span class="text-gray-300 font-mono">{{ queueDetailItem._raw.id }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Direction</span>
                    <span class="font-mono px-1.5 py-px rounded" :class="queueDetailItem._raw.direction === 'tx' ? 'bg-sky-400/10 text-sky-400' : 'bg-emerald-400/10 text-emerald-400'">
                      {{ queueDetailItem._raw.direction === 'tx' ? 'Outbound' : 'Inbound' }}
                    </span>
                  </div>
                  <div class="flex justify-between"><span class="text-gray-500">Phone</span><span class="text-gray-300 font-mono">{{ queueDetailItem._raw.phone || 'N/A' }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Contact</span><span class="text-gray-300">{{ queueDetailItem._raw.contact_name || 'Unknown' }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Status</span>
                    <span class="font-mono px-1.5 py-px rounded" :class="queueDetailItem._statusClass">{{ queueDetailItem._raw.status || 'queued' }}</span>
                  </div>
                  <div class="flex justify-between"><span class="text-gray-500">Timestamp</span><span class="text-gray-400 font-mono text-[10px]">{{ formatTimestamp(queueDetailItem._raw.created_at) }}</span></div>
                </div>
              </div>
              <div>
                <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Message</h4>
                <div class="bg-tactical-bg rounded p-3 text-[12px] text-gray-200 font-mono whitespace-pre-wrap break-words">{{ queueDetailItem._raw.text || '(empty)' }}</div>
              </div>
            </template>

            <!-- ── SBD / generic detail ── -->
            <template v-else>
              <div>
                <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Message Metadata</h4>
                <div class="grid grid-cols-2 gap-1.5 text-[11px]">
                  <div class="flex justify-between"><span class="text-gray-500">ID</span><span class="text-gray-300 font-mono">{{ queueDetailItem._raw.id }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Packet ID</span><span class="text-gray-300 font-mono">{{ queueDetailItem._raw.packet_id || 'N/A' }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Direction</span><span class="text-gray-300 font-mono">{{ queueDetailItem._raw.direction || 'outbound' }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Status</span><span class="font-mono px-1.5 py-px rounded" :class="dlqStatusColor(queueDetailItem._raw.status)">{{ queueDetailItem._raw.status || 'queued' }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Priority</span><span class="text-gray-300 font-mono">{{ queueDetailItem._raw.priority ?? 'N/A' }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Created</span><span class="text-gray-400 font-mono text-[10px]">{{ formatTimestamp(queueDetailItem._raw.created_at) }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Updated</span><span class="text-gray-400 font-mono text-[10px]">{{ formatTimestamp(queueDetailItem._raw.updated_at) }}</span></div>
                </div>
              </div>

              <div>
                <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Payload</h4>
                <div class="text-[11px] space-y-1">
                  <div><span class="text-gray-500">Preview: </span><span class="text-gray-300">{{ queueDetailItem._raw.text_preview || queueDetailItem._raw.decoded_text || '(none)' }}</span></div>
                  <div v-if="queueDetailItem._raw.payload"><span class="text-gray-500">Hex: </span><span class="text-gray-400 font-mono text-[10px] break-all">{{ toHex(queueDetailItem._raw.payload) }}</span></div>
                </div>
              </div>

              <div v-if="queueDetailItem._type === 'sbd'">
                <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Retry State</h4>
                <div class="grid grid-cols-2 gap-1.5 text-[11px]">
                  <div class="flex justify-between"><span class="text-gray-500">Retries</span><span class="text-gray-300 font-mono">{{ queueDetailItem._raw.retries ?? 0 }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Max Retries</span><span class="text-gray-300 font-mono">{{ queueDetailItem._raw.max_retries ?? 'N/A' }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Next Retry</span><span class="text-gray-400 font-mono text-[10px]">{{ queueDetailItem._raw.next_retry ? formatTimestamp(queueDetailItem._raw.next_retry) : 'N/A' }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Last Error</span><span class="text-gray-400 font-mono text-[10px] truncate">{{ queueDetailItem._raw.last_error || 'None' }}</span></div>
                </div>
              </div>

              <div v-if="queueDetailItem._type === 'sbd'">
                <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Routing</h4>
                <div class="grid grid-cols-2 gap-1.5 text-[11px]">
                  <div class="flex justify-between"><span class="text-gray-500">Dest Channel</span><span class="text-gray-300 font-mono">{{ queueDetailItem._raw.dest_channel ?? 'N/A' }}</span></div>
                  <div class="flex justify-between"><span class="text-gray-500">Dest Node</span><span class="text-gray-300 font-mono">{{ queueDetailItem._raw.dest_node || 'N/A' }}</span></div>
                </div>
              </div>

              <div>
                <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Flow Timeline</h4>
                <div class="flex items-center gap-2 flex-wrap">
                  <div v-for="(step, idx) in queueItemFlowSteps(queueDetailItem._raw)" :key="idx"
                    class="flex items-center gap-1.5">
                    <span class="w-2 h-2 rounded-full" :class="step.active ? 'bg-emerald-400' : 'bg-red-400'" />
                    <span class="text-[10px] font-mono" :class="step.active ? 'text-emerald-400' : 'text-red-400'">{{ step.label }}</span>
                    <span class="text-[9px] text-gray-600 font-mono">{{ formatTimestamp(step.time) }}</span>
                    <span v-if="idx < queueItemFlowSteps(queueDetailItem._raw).length - 1" class="text-gray-700">&#8594;</span>
                  </div>
                </div>
              </div>
            </template>

            <!-- Raw JSON (all types) -->
            <div>
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Raw JSON</h4>
              <pre class="text-[10px] font-mono text-gray-400 whitespace-pre-wrap break-all bg-tactical-bg rounded p-3 max-h-[200px] overflow-y-auto select-all">{{ JSON.stringify(queueDetailItem._raw, null, 2) }}</pre>
            </div>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- ═══ Node Detail Modal ═══ -->
    <Teleport to="body">
      <div v-if="nodeDetailModal && nodeDetailData" class="fixed inset-0 z-[100] flex items-center justify-center bg-black/70 backdrop-blur-sm" @click.self="nodeDetailModal = false">
        <div class="bg-tactical-surface border border-tactical-border rounded-lg w-full max-w-2xl max-h-[85vh] overflow-y-auto m-4">
          <div class="sticky top-0 bg-tactical-surface border-b border-tactical-border px-4 py-3 flex items-center justify-between">
            <div class="flex items-center gap-3">
              <div class="w-9 h-9 rounded-full bg-gray-700/50 flex items-center justify-center text-xs font-bold text-gray-400">
                {{ (nodeDetailData.short_name || '??').slice(0, 2).toUpperCase() }}
              </div>
              <div>
                <h3 class="font-display font-semibold text-sm text-tactical-lora tracking-wide">
                  {{ nodeDetailData.long_name || nodeDetailData.user_id || 'Unknown' }}
                </h3>
                <span class="text-[10px] text-gray-500 font-mono">{{ shortId(nodeDetailData.user_id) }}</span>
              </div>
            </div>
            <button @click="nodeDetailModal = false" class="text-gray-500 hover:text-gray-300 text-lg leading-none">&times;</button>
          </div>
          <div class="p-4 space-y-4">
            <!-- Identity -->
            <div>
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Identity</h4>
              <div class="grid grid-cols-2 gap-1.5 text-[11px]">
                <div class="flex justify-between"><span class="text-gray-500">Node Num</span><span class="text-gray-300 font-mono">{{ nodeDetailData.num }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">User ID</span><span class="text-gray-300 font-mono">{{ nodeDetailData.user_id || 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Short Name</span><span class="text-gray-300">{{ nodeDetailData.short_name || 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Long Name</span><span class="text-gray-300">{{ nodeDetailData.long_name || 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Hardware</span><span class="text-gray-300">{{ nodeDetailData.hw_model_name || 'Unknown' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Role</span><span class="text-gray-300">{{ nodeDetailData.role || 'N/A' }}</span></div>
              </div>
            </div>

            <!-- Radio -->
            <div>
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Radio</h4>
              <div class="grid grid-cols-2 gap-1.5 text-[11px]">
                <div class="flex justify-between"><span class="text-gray-500">SNR</span>
                  <span :class="(nodeDetailData.snr ?? -999) >= 0 ? 'text-emerald-400' : (nodeDetailData.snr ?? -999) >= -10 ? 'text-amber-400' : 'text-red-400'" class="font-mono">
                    {{ nodeDetailData.snr != null ? `${Number(nodeDetailData.snr).toFixed(1)} dB` : 'N/A' }}
                  </span>
                </div>
                <div class="flex justify-between"><span class="text-gray-500">RSSI</span><span class="text-gray-300 font-mono">{{ nodeDetailData.rssi ? `${nodeDetailData.rssi} dBm` : 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Signal Quality</span>
                  <span v-if="nodeDetailData.signal_quality" class="font-mono">{{ nodeDetailData.signal_quality }}</span>
                  <span v-else class="text-gray-600">N/A</span>
                </div>
                <div class="flex justify-between"><span class="text-gray-500">Hops Away</span><span class="text-gray-300 font-mono">{{ nodeDetailData.hops_away ?? 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Last Heard</span><span class="text-gray-400 font-mono">{{ formatLastHeard(nodeDetailData.last_heard) }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Last Message</span><span class="text-gray-400 font-mono">{{ nodeDetailData.last_message_time ? formatLastHeard(nodeDetailData.last_message_time) : 'Never' }}</span></div>
              </div>
            </div>

            <!-- Position -->
            <div v-if="nodeDetailData.latitude || nodeDetailData.longitude">
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Position</h4>
              <div class="grid grid-cols-2 gap-1.5 text-[11px]">
                <div class="flex justify-between"><span class="text-gray-500">Latitude</span><span class="text-gray-300 font-mono">{{ nodeDetailData.latitude?.toFixed(6) || 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Longitude</span><span class="text-gray-300 font-mono">{{ nodeDetailData.longitude?.toFixed(6) || 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Altitude</span><span class="text-gray-300 font-mono">{{ nodeDetailData.altitude ? `${nodeDetailData.altitude}m` : 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Satellites</span><span class="text-gray-300 font-mono">{{ nodeDetailData.sats ?? 'N/A' }}</span></div>
              </div>
            </div>

            <!-- Power -->
            <div v-if="nodeDetailData.battery_level > 0 || nodeDetailData.voltage > 0">
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Power</h4>
              <div class="grid grid-cols-2 gap-1.5 text-[11px]">
                <div class="flex justify-between"><span class="text-gray-500">Battery</span>
                  <span class="font-mono" :class="nodeDetailData.battery_level > 50 ? 'text-emerald-400' : nodeDetailData.battery_level > 20 ? 'text-amber-400' : 'text-red-400'">
                    {{ nodeDetailData.battery_level ? `${Math.round(nodeDetailData.battery_level)}%` : 'N/A' }}
                  </span>
                </div>
                <div class="flex justify-between"><span class="text-gray-500">Voltage</span><span class="text-gray-300 font-mono">{{ nodeDetailData.voltage ? `${nodeDetailData.voltage.toFixed(2)}V` : 'N/A' }}</span></div>
              </div>
            </div>

            <!-- Telemetry History -->
            <div v-if="nodeDetailLoading" class="text-xs text-gray-500">Loading telemetry...</div>
            <div v-else-if="nodeDetailTelemetry.length > 0">
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Telemetry History ({{ nodeDetailTelemetry.length }})</h4>
              <div class="overflow-x-auto">
                <table class="w-full text-[10px] text-gray-400">
                  <thead>
                    <tr class="text-gray-600">
                      <th class="text-left pr-2 py-1">Time</th>
                      <th class="text-right pr-2 py-1">Battery</th>
                      <th class="text-right pr-2 py-1">Voltage</th>
                      <th class="text-right pr-2 py-1">Ch Util</th>
                      <th class="text-right pr-2 py-1">Air Util</th>
                      <th class="text-right py-1">Uptime</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="t in nodeDetailTelemetry.slice(0, 15)" :key="t.id" class="border-t border-gray-800/50">
                      <td class="pr-2 py-0.5 text-gray-600">{{ new Date(t.created_at).toLocaleTimeString() }}</td>
                      <td class="text-right pr-2 py-0.5">{{ t.battery_level }}%</td>
                      <td class="text-right pr-2 py-0.5">{{ t.voltage?.toFixed(2) }}V</td>
                      <td class="text-right pr-2 py-0.5">{{ t.channel_util?.toFixed(1) }}%</td>
                      <td class="text-right pr-2 py-0.5">{{ t.air_util_tx?.toFixed(1) }}%</td>
                      <td class="text-right py-0.5">{{ t.uptime_seconds ? `${Math.floor(t.uptime_seconds / 3600)}h` : '-' }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>

            <!-- Neighbors -->
            <div v-if="nodeDetailNeighbors.length > 0">
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Neighbors</h4>
              <div class="flex flex-wrap gap-2">
                <div v-for="ni in nodeDetailNeighbors" :key="ni.node_id" class="bg-gray-900/50 rounded px-2 py-1 text-[10px]">
                  <div v-for="n in ni.neighbors" :key="n.node_id" class="flex items-center gap-2 py-0.5">
                    <span class="text-gray-400 font-mono">!{{ n.node_id.toString(16).padStart(8, '0') }}</span>
                    <span :class="n.snr >= 0 ? 'text-emerald-400/70' : 'text-amber-400/70'">SNR {{ n.snr?.toFixed(1) }}</span>
                  </div>
                </div>
              </div>
            </div>

            <!-- Raw JSON -->
            <div>
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Raw Data</h4>
              <pre class="text-[10px] font-mono text-gray-400 whitespace-pre-wrap break-all bg-tactical-bg rounded p-3 max-h-[200px] overflow-y-auto select-all">{{ JSON.stringify(nodeDetailData, null, 2) }}</pre>
            </div>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>
