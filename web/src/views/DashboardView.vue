<script setup>
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import { buildPolyline, buildAreaPath } from '@/composables/useSVGChart'
import { formatRelativeTime, formatTimestamp, formatLastHeard, formatUptime, formatAccuracy, shortId, isNodeActive, isNodeRecent, nodeStatusDot } from '@/utils/format'

const store = useMeshsatStore()

// ── Manual mailbox check ──
const dashCheckingMailbox = ref(false)
async function dashCheckMailbox() {
  dashCheckingMailbox.value = true
  try { await store.manualMailboxCheck() } catch {}
  dashCheckingMailbox.value = false
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
const dlqPending = computed(() => (store.dlq || []).filter(d => d.status === 'pending' || !d.status).length)
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
const locationIridium = computed(() => (store.locationSources?.sources || []).find(s => s.source === 'iridium'))


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
const topNodes = computed(() => {
  const sorted = [...(store.nodes || [])].sort((a, b) => (b.last_heard || 0) - (a.last_heard || 0))
  return sorted.slice(0, 6)
})

// ── Computed: Cellular 4G/LTE panel ──
const cellularGw = computed(() => (store.gateways || []).find(g => g.type === 'cellular'))
const cellBars = computed(() => store.cellularSignal?.bars ?? -1)
const cellStatus = computed(() => {
  if (!cellularGw.value) return { dot: 'bg-gray-600', text: 'Not Configured' }
  if (cellularGw.value.connected) return { dot: 'bg-sky-400', text: 'Connected' }
  return { dot: 'bg-red-400', text: 'Disconnected' }
})

// ── Computed: GPS Position panel ──
const localNode = computed(() => {
  const myId = store.status?.node_id
  if (!myId) return null
  return (store.nodes || []).find(n => n.num === myId)
})
const gpsLat = computed(() => localNode.value?.latitude?.toFixed(6) ?? 'N/A')
const gpsLon = computed(() => localNode.value?.longitude?.toFixed(6) ?? 'N/A')
const gpsAlt = computed(() => localNode.value?.altitude != null ? `${localNode.value.altitude}m` : 'N/A')
const gpsSats = computed(() => localNode.value?.sats ?? 'N/A')
const gpsFix = computed(() => {
  if (!localNode.value?.latitude && !localNode.value?.longitude) return false
  return true
})

// ── Computed: SBD Queue panel (filter out expired) ──
const dlqItems = computed(() => {
  return (store.dlq || []).filter(d => d.status !== 'expired').slice(0, 8)
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
  if (type === 'message' || type === 'text') return { label: 'LORA', color: 'bg-tactical-lora/20 text-tactical-lora' }
  if (type === 'position') return { label: 'GPS', color: 'bg-tactical-gps/20 text-tactical-gps' }
  if (type === 'sos') return { label: 'SOS', color: 'bg-tactical-sos/20 text-tactical-sos' }
  if (type === 'node_update') return { label: 'LORA', color: 'bg-tactical-lora/20 text-tactical-lora' }
  if (type === 'rule_match') return { label: 'RULE', color: 'bg-purple-400/20 text-purple-400' }
  if (type === 'forward') return { label: 'FWD', color: 'bg-tactical-iridium/20 text-tactical-iridium' }
  if (type === 'forward_error') return { label: 'ERR', color: 'bg-red-400/20 text-red-400' }
  if (type === 'relay') return { label: 'RELAY', color: 'bg-amber-400/20 text-amber-400' }
  if (type === 'inbound') return { label: 'IN', color: 'bg-blue-400/20 text-blue-400' }
  if (type === 'cellular') return { label: 'CELL', color: 'bg-sky-400/20 text-sky-400' }
  return { label: 'SYS', color: 'bg-gray-700/50 text-gray-400' }
}

function eventDescription(event) {
  const type = event?.type ?? ''
  const msg = event?.message ?? ''
  if (type === 'message' || type === 'text') return msg || 'New message received'
  if (type === 'node_update') return msg || 'Node data updated'
  if (type === 'position') return msg || 'Position update received'
  if (type === 'connected') return 'Radio connected'
  if (type === 'disconnected') return 'Radio disconnected'
  if (type === 'signal') return msg || 'Iridium signal update'
  if (type === 'config_complete') return 'Config sync complete'
  if (type === 'rule_match') return msg || 'Forwarding rule matched'
  if (type === 'forward') return msg || 'Message forwarded to gateway'
  if (type === 'forward_error') return msg || 'Forward failed'
  if (type === 'relay') return msg || 'Cross-gateway relay'
  if (type === 'inbound') return msg || 'Inbound gateway message'
  if (type === 'cellular') return msg || 'Cellular event'
  return msg || type || 'Event'
}

let saveLogTimer = null
function handleSSEEvent(event) {
  const type = event?.type ?? ''
  activityLog.value.unshift({
    time: new Date().toISOString(),
    type,
    description: eventDescription(event)
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
        'Gateway': cellularGw.value || 'Not configured',
        'Signal': store.cellularSignal || 'No data',
        'Status': store.cellularStatus || 'No data',
        'Messages Out': cellularGw.value?.messages_out ?? 0,
        'Messages In': cellularGw.value?.messages_in ?? 0
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
        'SSE Connected': !!store._sseHandle,
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
    store.fetchCredits(),
    store.fetchSchedulerStatus(),
    store.fetchLocationSources(),
    store.fetchCellularSignal(),
    store.fetchCellularStatus()
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

  fetchAll()
  store.connectSSE(handleSSEEvent)
  pollTimer = setInterval(() => {
    nowSec.value = Date.now() / 1000
    store.fetchIridiumSignalFast()
    store.fetchNodes()
    store.fetchDLQ()
    store.fetchSchedulerStatus()
    store.fetchLocationSources()
    store.fetchCellularSignal()
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
function widgetGridClass(id) {
  if (id === 'sos') return 'md:col-span-2 lg:col-span-1 lg:row-span-2'
  if (id === 'activity') return 'md:col-span-2'
  return ''
}
</script>

<template>
  <div class="max-w-[1400px] mx-auto">
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
            <h2 class="font-display font-semibold text-sm text-tactical-iridium tracking-wide cursor-pointer hover:underline" @click="openWidgetStats('iridium')">IRIDIUM SBD</h2>
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

        <!-- Signal sparkline (6h history) -->
        <div v-if="sparklinePoints" class="mb-3">
          <svg viewBox="0 0 200 40" class="w-full h-8" preserveAspectRatio="none">
            <path :d="sparklineArea" fill="rgba(45,212,191,0.1)" />
            <polyline :points="sparklinePoints" fill="none" stroke="rgb(45,212,191)" stroke-width="1.5" />
          </svg>
          <div class="text-[9px] text-gray-600 text-right">6h signal history</div>
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
      </div>

      <!-- ═══ Meshtastic Mesh ═══ -->
      <div v-if="wid === 'mesh'"
        :class="['bg-tactical-surface rounded-lg border border-tactical-border p-4', widgetGridClass(wid), dragOver === wid ? 'ring-1 ring-tactical-iridium/40' : '']"
        draggable="true" @dragstart="onDragStart($event, wid)" @dragover="onDragOver($event, wid)" @dragleave="onDragLeave" @drop="onDrop($event, wid)" @dragend="onDragEnd">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <svg class="w-3.5 h-3.5 text-gray-600 cursor-grab active:cursor-grabbing" viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/><circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/><circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/></svg>
            <h2 class="font-display font-semibold text-sm text-tactical-lora tracking-wide cursor-pointer hover:underline" @click="openWidgetStats('mesh')">MESHTASTIC MESH</h2>
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
        </div>

        <div class="space-y-1">
          <div v-for="node in topNodes" :key="node.num"
            class="flex items-center gap-2 py-1 px-2 rounded hover:bg-white/[0.02] transition-colors">
            <span class="w-1.5 h-1.5 rounded-full shrink-0" :class="signalDot(node)" />
            <span class="text-[11px] text-gray-300 truncate flex-1">{{ node.long_name || 'Unknown' }}</span>
            <span class="text-[9px] font-mono text-gray-600 shrink-0">{{ shortId(node.user_id) }}</span>
            <span v-if="node.snr != null" class="text-[9px] font-mono shrink-0"
              :class="node.snr >= 0 ? 'text-emerald-400/60' : node.snr >= -10 ? 'text-amber-400/60' : 'text-red-400/60'">
              {{ Number(node.snr).toFixed(0) }}dB
            </span>
            <span class="text-[9px] text-gray-600 shrink-0">{{ formatLastHeard(node.last_heard) }}</span>
          </div>
          <div v-if="!topNodes.length" class="text-[11px] text-gray-600 text-center py-2">No nodes discovered</div>
        </div>

        <router-link to="/nodes" class="block text-center text-[10px] text-tactical-lora/60 hover:text-tactical-lora mt-2 transition-colors">
          View All Nodes
        </router-link>
      </div>

      <!-- ═══ Cellular 4G/LTE ═══ -->
      <div v-if="wid === 'cellular'"
        :class="['bg-tactical-surface rounded-lg border border-tactical-border p-4', widgetGridClass(wid), dragOver === wid ? 'ring-1 ring-tactical-iridium/40' : '']"
        draggable="true" @dragstart="onDragStart($event, wid)" @dragover="onDragOver($event, wid)" @dragleave="onDragLeave" @drop="onDrop($event, wid)" @dragend="onDragEnd">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <svg class="w-3.5 h-3.5 text-gray-600 cursor-grab active:cursor-grabbing" viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/><circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/><circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/></svg>
            <h2 class="font-display font-semibold text-sm text-sky-400 tracking-wide cursor-pointer hover:underline" @click="openWidgetStats('cellular')">CELLULAR 4G/LTE</h2>
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
          <span v-if="store.cellularStatus?.network_type" class="text-[10px] text-gray-500 uppercase">
            {{ store.cellularStatus.network_type }}
          </span>
        </div>

        <div class="space-y-1.5 text-[11px]">
          <div class="flex justify-between">
            <span class="text-gray-500">Gateway</span>
            <span class="text-gray-300">{{ cellStatus.text }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Operator</span>
            <span class="text-gray-300 font-mono">{{ store.cellularStatus?.operator || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">IMEI</span>
            <span class="text-gray-400 font-mono text-[10px]">{{ store.cellularStatus?.imei || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">SMS Sent</span>
            <span class="text-gray-300 font-mono">{{ cellularGw?.messages_out ?? 0 }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">SMS Received</span>
            <span class="text-gray-300 font-mono">{{ cellularGw?.messages_in ?? 0 }}</span>
          </div>
        </div>
      </div>

      <!-- ═══ Emergency SOS (row-span-2) ═══ -->
      <div v-if="wid === 'sos'"
        :class="['bg-tactical-surface rounded-lg border border-tactical-border p-4', widgetGridClass(wid), dragOver === wid ? 'ring-1 ring-tactical-iridium/40' : '']"
        draggable="true" @dragstart="onDragStart($event, wid)" @dragover="onDragOver($event, wid)" @dragleave="onDragLeave" @drop="onDrop($event, wid)" @dragend="onDragEnd">
        <div class="flex items-center justify-between mb-4">
          <div class="flex items-center gap-2">
            <svg class="w-3.5 h-3.5 text-gray-600 cursor-grab active:cursor-grabbing" viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/><circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/><circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/></svg>
            <h2 class="font-display font-semibold text-sm text-tactical-sos tracking-wide cursor-pointer hover:underline" @click="openWidgetStats('sos')">EMERGENCY SOS</h2>
          </div>
          <span class="text-[10px] font-mono"
            :class="sosActive ? 'text-tactical-sos' : 'text-gray-600'">
            {{ sosActive ? 'ARMED' : 'STANDBY' }}
          </span>
        </div>

        <div class="flex justify-center mb-4">
          <div class="relative">
            <svg viewBox="0 0 120 120" class="w-28 h-28 lg:w-36 lg:h-36">
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
          </div>
        </div>

        <button @click="toggleSOS" :disabled="sosArming"
          class="w-full py-2.5 rounded-lg text-xs font-semibold transition-all mb-4"
          :class="sosActive
            ? 'bg-tactical-sos/20 text-tactical-sos border border-tactical-sos/30 hover:bg-tactical-sos/30'
            : 'bg-gray-800 text-gray-400 border border-gray-700 hover:text-gray-200 hover:border-gray-600'">
          {{ sosArming ? '...' : sosActive ? 'CANCEL SOS' : 'ARM SOS' }}
        </button>

        <button @click="testSOS"
          class="w-full py-1.5 rounded text-[10px] text-gray-500 hover:text-gray-300 bg-gray-800/50 hover:bg-gray-800 transition-colors mb-4">
          Send Test
        </button>

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
            :class="locationResolved.source === 'gps' ? 'bg-emerald-400/10 text-emerald-400' : locationResolved.source === 'iridium' ? 'bg-teal-400/10 text-teal-400' : 'bg-amber-400/10 text-amber-400'">
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
              <span class="w-1.5 h-1.5 rounded-full" :class="locationGps ? 'bg-emerald-400' : 'bg-gray-600'" />
              <span class="text-gray-500">GPS</span>
            </div>
            <span v-if="locationGps" class="text-gray-300 font-mono text-[10px]">
              {{ locationGps.lat.toFixed(4) }}, {{ locationGps.lon.toFixed(4) }}
              <span class="text-gray-600 ml-1">~{{ formatAccuracy(locationGps.accuracy_km) }}</span>
            </span>
            <span v-else class="text-gray-600 font-mono">No fix</span>
          </div>
          <div class="flex justify-between items-center">
            <div class="flex items-center gap-1.5">
              <span class="w-1.5 h-1.5 rounded-full" :class="locationIridium ? 'bg-teal-400' : 'bg-gray-600'" />
              <span class="text-gray-500">Iridium</span>
            </div>
            <span v-if="locationIridium" class="text-gray-300 font-mono text-[10px]">
              {{ locationIridium.lat.toFixed(4) }}, {{ locationIridium.lon.toFixed(4) }}
              <span class="text-gray-600 ml-1">~{{ formatAccuracy(locationIridium.accuracy_km) }}</span>
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
          <span class="text-[9px] text-gray-600">Priority: GPS (5m) > Iridium (1-100km) > Custom</span>
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
        :class="['bg-tactical-surface rounded-lg border border-tactical-border p-4', widgetGridClass(wid), dragOver === wid ? 'ring-1 ring-tactical-iridium/40' : '']"
        draggable="true" @dragstart="onDragStart($event, wid)" @dragover="onDragOver($event, wid)" @dragleave="onDragLeave" @drop="onDrop($event, wid)" @dragend="onDragEnd">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <svg class="w-3.5 h-3.5 text-gray-600 cursor-grab active:cursor-grabbing" viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/><circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/><circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/></svg>
            <h2 class="font-display font-semibold text-sm text-tactical-iridium tracking-wide cursor-pointer hover:underline" @click="openWidgetStats('queue')">SBD QUEUE</h2>
          </div>
          <span v-if="dlqPending > 0"
            class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-amber-400/10 text-amber-400">
            {{ dlqPending }} pending
          </span>
        </div>

        <div class="space-y-1 tactical-scroll max-h-[200px] overflow-y-auto">
          <div v-for="item in dlqItems" :key="item.id"
            class="flex items-center gap-2 py-1.5 px-2 rounded bg-tactical-bg/50 cursor-pointer hover:bg-white/[0.04] transition-colors"
            :class="item.status === 'sent' || item.status === 'received' ? 'opacity-60' : ''"
            @click="openQueueItemDetail(item)">
            <span class="text-[9px] font-mono shrink-0"
              :class="item.direction === 'inbound' ? 'text-blue-400' : 'text-tactical-iridium'">
              {{ item.direction === 'inbound' ? 'SBD\u2192Mesh' : 'Mesh\u2192SBD' }}
            </span>
            <span class="text-[10px] font-mono px-1.5 py-px rounded"
              :class="dlqStatusColor(item.status)">
              {{ item.status === 'sent' ? 'delivered' : item.status === 'received' ? 'received' : item.status || 'queued' }}
            </span>
            <span class="text-[11px] text-gray-300 truncate flex-1">{{ item.text_preview || '(binary)' }}</span>
            <span class="text-[9px] text-gray-600 font-mono shrink-0">{{ formatRelativeTime(item.created_at) }}</span>
          </div>
          <div v-if="!dlqItems.length" class="text-[11px] text-gray-600 text-center py-3">Queue empty</div>
        </div>

        <div v-if="satMessages.length" class="mt-3 pt-3 border-t border-tactical-border">
          <span class="text-[10px] text-gray-500 block mb-1.5">Recent Satellite</span>
          <div class="space-y-1">
            <div v-for="msg in satMessages" :key="msg.id"
              class="flex items-center gap-2 text-[11px]">
              <span class="text-gray-500 font-mono text-[9px] shrink-0">{{ formatRelativeTime(msg.created_at || msg.timestamp) }}</span>
              <span class="text-gray-400 truncate">{{ msg.text || msg.payload || '(data)' }}</span>
            </div>
          </div>
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
            <h3 class="font-display font-semibold text-sm text-tactical-iridium tracking-wide">QUEUE ITEM #{{ queueDetailItem.id }}</h3>
            <button @click="queueDetailModal = false" class="text-gray-500 hover:text-gray-300 text-lg leading-none">&times;</button>
          </div>
          <div class="p-4 space-y-4">
            <!-- Message metadata -->
            <div>
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Message Metadata</h4>
              <div class="grid grid-cols-2 gap-1.5 text-[11px]">
                <div class="flex justify-between"><span class="text-gray-500">ID</span><span class="text-gray-300 font-mono">{{ queueDetailItem.id }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Packet ID</span><span class="text-gray-300 font-mono">{{ queueDetailItem.packet_id || 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Direction</span><span class="text-gray-300 font-mono">{{ queueDetailItem.direction || 'outbound' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Status</span><span class="font-mono px-1.5 py-px rounded" :class="dlqStatusColor(queueDetailItem.status)">{{ queueDetailItem.status || 'queued' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Priority</span><span class="text-gray-300 font-mono">{{ queueDetailItem.priority ?? 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Created</span><span class="text-gray-400 font-mono text-[10px]">{{ formatTimestamp(queueDetailItem.created_at) }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Updated</span><span class="text-gray-400 font-mono text-[10px]">{{ formatTimestamp(queueDetailItem.updated_at) }}</span></div>
              </div>
            </div>

            <!-- Payload -->
            <div>
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Payload</h4>
              <div class="text-[11px] space-y-1">
                <div><span class="text-gray-500">Preview: </span><span class="text-gray-300">{{ queueDetailItem.text_preview || '(none)' }}</span></div>
                <div v-if="queueDetailItem.payload"><span class="text-gray-500">Hex: </span><span class="text-gray-400 font-mono text-[10px] break-all">{{ toHex(queueDetailItem.payload) }}</span></div>
              </div>
            </div>

            <!-- Retry state -->
            <div>
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Retry State</h4>
              <div class="grid grid-cols-2 gap-1.5 text-[11px]">
                <div class="flex justify-between"><span class="text-gray-500">Retries</span><span class="text-gray-300 font-mono">{{ queueDetailItem.retries ?? 0 }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Max Retries</span><span class="text-gray-300 font-mono">{{ queueDetailItem.max_retries ?? 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Next Retry</span><span class="text-gray-400 font-mono text-[10px]">{{ queueDetailItem.next_retry ? formatTimestamp(queueDetailItem.next_retry) : 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Last Error</span><span class="text-gray-400 font-mono text-[10px] truncate">{{ queueDetailItem.last_error || 'None' }}</span></div>
              </div>
            </div>

            <!-- Routing -->
            <div>
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Routing</h4>
              <div class="grid grid-cols-2 gap-1.5 text-[11px]">
                <div class="flex justify-between"><span class="text-gray-500">Dest Channel</span><span class="text-gray-300 font-mono">{{ queueDetailItem.dest_channel ?? 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Dest Node</span><span class="text-gray-300 font-mono">{{ queueDetailItem.dest_node || 'N/A' }}</span></div>
              </div>
            </div>

            <!-- Flow timeline -->
            <div>
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Flow Timeline</h4>
              <div class="flex items-center gap-2 flex-wrap">
                <div v-for="(step, idx) in queueItemFlowSteps(queueDetailItem)" :key="idx"
                  class="flex items-center gap-1.5">
                  <span class="w-2 h-2 rounded-full" :class="step.active ? 'bg-emerald-400' : 'bg-red-400'" />
                  <span class="text-[10px] font-mono" :class="step.active ? 'text-emerald-400' : 'text-red-400'">{{ step.label }}</span>
                  <span class="text-[9px] text-gray-600 font-mono">{{ formatTimestamp(step.time) }}</span>
                  <span v-if="idx < queueItemFlowSteps(queueDetailItem).length - 1" class="text-gray-700">&#8594;</span>
                </div>
              </div>
            </div>

            <!-- Raw JSON -->
            <div>
              <h4 class="text-[10px] text-gray-500 uppercase tracking-wide mb-2">Raw JSON</h4>
              <pre class="text-[10px] font-mono text-gray-400 whitespace-pre-wrap break-all bg-tactical-bg rounded p-3 max-h-[200px] overflow-y-auto select-all">{{ JSON.stringify(queueDetailItem, null, 2) }}</pre>
            </div>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>
