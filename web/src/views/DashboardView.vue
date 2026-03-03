<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import { buildPolyline, buildAreaPath } from '@/composables/useSVGChart'

const store = useMeshsatStore()

// ── Activity log ──
const activityLog = ref([])
const MAX_LOG = 50
const logPaused = ref(false)

// ── SOS state ──
const sosArming = ref(false)

// ── Helpers from NodesView ──
const nowSec = computed(() => Date.now() / 1000)

function isNodeActive(node) {
  if (!node.last_heard) return false
  return (nowSec.value - node.last_heard) < 7200
}

function isNodeRecent(node) {
  if (!node.last_heard) return false
  return (nowSec.value - node.last_heard) < 86400
}

function signalDot(node) {
  if (isNodeActive(node)) return 'bg-emerald-400'
  if (isNodeRecent(node)) return 'bg-amber-400'
  return 'bg-gray-600'
}

function shortId(id) {
  if (!id) return ''
  if (id.startsWith('!') && id.length > 6) return id.slice(0, 3) + '..' + id.slice(-4)
  return id
}

function formatLastHeard(val) {
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

function formatUptime(secs) {
  if (!secs || secs <= 0) return 'N/A'
  const d = Math.floor(secs / 86400)
  const h = Math.floor((secs % 86400) / 3600)
  const m = Math.floor((secs % 3600) / 60)
  if (d > 0) return `${d}d ${h}h ${m}m`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
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
  // Last successful outbound SBD send — from the queue (dead_letters with status=sent, direction=outbound)
  const sent = (store.dlq || []).filter(d => d.status === 'sent' && d.direction === 'outbound')
  if (sent.length) {
    // Queue is sorted by id desc, first entry is most recent
    return formatRelativeTime(sent[0].updated_at || sent[0].created_at)
  }
  return 'N/A'
})
const lastSatRx = computed(() => {
  // Last inbound SBD receive — from the queue (dead_letters with direction=inbound)
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

function formatAccuracy(km) {
  if (km == null) return ''
  if (km < 1) return `${(km * 1000).toFixed(0)}m`
  return `${km.toFixed(0)}km`
}

function locationSourceColor(source) {
  if (source === 'gps') return 'text-emerald-400'
  if (source === 'iridium') return 'text-teal-400'
  return 'text-amber-400'
}

function locationSourceDot(source) {
  if (source === 'gps') return 'bg-emerald-400'
  if (source === 'iridium') return 'bg-teal-400'
  return 'bg-amber-400'
}

// Credits from store
const creditsToday = computed(() => store.creditSummary?.today ?? 0)
const creditsMonth = computed(() => store.creditSummary?.month ?? 0)
const dailyBudget = computed(() => store.creditSummary?.daily_budget || 0)
const monthlyBudget = computed(() => store.creditSummary?.monthly_budget || 0)

function formatRelativeTime(val) {
  if (!val) return 'N/A'
  const ts = typeof val === 'string' ? new Date(val).getTime() : (val < 1e12 ? val * 1000 : val)
  const diff = Math.floor((Date.now() - ts) / 1000)
  if (diff < 0) return 'just now'
  if (diff < 60) return `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

// ── Computed: Meshtastic Mesh panel ──
const radioConnected = computed(() => store.status?.connected === true)
const nodeName = computed(() => store.status?.node_name || 'Unknown')
const activeNodes = computed(() => (store.nodes || []).filter(n => isNodeActive(n)))
const totalNodes = computed(() => (store.nodes || []).length)
const topNodes = computed(() => {
  const sorted = [...(store.nodes || [])].sort((a, b) => (b.last_heard || 0) - (a.last_heard || 0))
  return sorted.slice(0, 6)
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

// ── Computed: SBD Queue panel ──
const dlqItems = computed(() => {
  return (store.dlq || []).slice(0, 8)
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

// ── Computed: Power System panel ──
const batteryLevel = computed(() => localNode.value?.battery_level ?? null)
const voltage = computed(() => localNode.value?.voltage ?? null)
const uptimeSeconds = computed(() => localNode.value?.uptime_seconds ?? store.status?.uptime_seconds ?? null)
const temperature = computed(() => localNode.value?.temperature ?? null)

function batteryColor(level) {
  if (level == null) return 'bg-gray-600'
  if (level > 60) return 'bg-tactical-power'
  if (level > 25) return 'bg-amber-400'
  return 'bg-red-400'
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
  return msg || type || 'Event'
}

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
}

function formatLogTime(t) {
  if (!t) return ''
  return new Date(t).toISOString().slice(11, 19)
}

// ── DLQ cancel ──
async function cancelDLQ(id) {
  try {
    await store.fetchDLQ() // refresh after cancel — API doesn't have a cancel endpoint yet
  } catch { /* ignore */ }
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
    store.fetchLocationSources()
  ])
}

onMounted(() => {
  fetchAll()
  store.connectSSE(handleSSEEvent)
  pollTimer = setInterval(() => {
    store.fetchIridiumSignalFast()
    store.fetchNodes()
    store.fetchDLQ()
    store.fetchSchedulerStatus()
    store.fetchLocationSources()
  }, 15000)
})

onUnmounted(() => {
  store.closeSSE()
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<template>
  <div class="max-w-[1400px] mx-auto">
    <!-- 7-Panel Grid -->
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">

      <!-- ═══ Panel 1: Iridium SBD ═══ -->
      <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4">
        <div class="flex items-center justify-between mb-3">
          <h2 class="font-display font-semibold text-sm text-tactical-iridium tracking-wide">IRIDIUM SBD</h2>
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
          <!-- Scheduler mode badge -->
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
      </div>

      <!-- ═══ Panel 2: Meshtastic Mesh ═══ -->
      <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4">
        <div class="flex items-center justify-between mb-3">
          <h2 class="font-display font-semibold text-sm text-tactical-lora tracking-wide">MESHTASTIC MESH</h2>
          <span class="w-2 h-2 rounded-full" :class="radioConnected ? 'bg-emerald-400' : 'bg-red-400'" />
        </div>

        <!-- Connection info -->
        <div class="flex items-center gap-2 mb-3">
          <span class="text-xs" :class="radioConnected ? 'text-emerald-400' : 'text-red-400'">
            {{ radioConnected ? 'Connected' : 'Disconnected' }}
          </span>
          <span class="text-[10px] text-gray-500 font-mono">{{ nodeName }}</span>
        </div>

        <!-- Node count -->
        <div class="flex items-center gap-2 mb-3">
          <span class="font-mono text-lg font-bold text-tactical-lora">{{ activeNodes.length }}</span>
          <span class="text-[10px] text-gray-500">/ {{ totalNodes }} nodes</span>
        </div>

        <!-- Top nodes list -->
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

      <!-- ═══ Panel 3: Emergency SOS (row-span-2) ═══ -->
      <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4 md:col-span-2 lg:col-span-1 lg:row-span-2">
        <div class="flex items-center justify-between mb-4">
          <h2 class="font-display font-semibold text-sm text-tactical-sos tracking-wide">EMERGENCY SOS</h2>
          <span class="text-[10px] font-mono"
            :class="sosActive ? 'text-tactical-sos' : 'text-gray-600'">
            {{ sosActive ? 'ARMED' : 'STANDBY' }}
          </span>
        </div>

        <!-- SOS Ring -->
        <div class="flex justify-center mb-4">
          <div class="relative">
            <svg viewBox="0 0 120 120" class="w-28 h-28 lg:w-36 lg:h-36">
              <!-- Outer ring -->
              <circle cx="60" cy="60" r="54" fill="none" stroke-width="3"
                :stroke="sosActive ? '#ef4444' : '#1a2230'" />
              <!-- Animated ring when active -->
              <circle v-if="sosActive" cx="60" cy="60" r="54" fill="none" stroke-width="3"
                stroke="#ef4444" stroke-dasharray="12 6" class="animate-spin"
                style="animation-duration: 8s;" />
              <!-- Inner fill -->
              <circle cx="60" cy="60" r="44" :fill="sosActive ? '#ef444420' : '#111820'" />
              <!-- SOS text -->
              <text x="60" y="56" text-anchor="middle" font-size="22" font-weight="700"
                :fill="sosActive ? '#ef4444' : '#4b5563'" font-family="Oxanium, sans-serif">SOS</text>
              <text x="60" y="74" text-anchor="middle" font-size="9"
                :fill="sosActive ? '#ef444480' : '#374151'" font-family="JetBrains Mono, monospace">
                {{ sosActive ? 'ACTIVE' : 'READY' }}
              </text>
            </svg>
          </div>
        </div>

        <!-- Arm/Disarm button -->
        <button @click="toggleSOS" :disabled="sosArming"
          class="w-full py-2.5 rounded-lg text-xs font-semibold transition-all mb-4"
          :class="sosActive
            ? 'bg-tactical-sos/20 text-tactical-sos border border-tactical-sos/30 hover:bg-tactical-sos/30'
            : 'bg-gray-800 text-gray-400 border border-gray-700 hover:text-gray-200 hover:border-gray-600'">
          {{ sosArming ? '...' : sosActive ? 'CANCEL SOS' : 'ARM SOS' }}
        </button>

        <!-- Test button -->
        <button @click="testSOS"
          class="w-full py-1.5 rounded text-[10px] text-gray-500 hover:text-gray-300 bg-gray-800/50 hover:bg-gray-800 transition-colors mb-4">
          Send Test
        </button>

        <!-- Status rows -->
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

      <!-- ═══ Panel 4: GPS Position ═══ -->
      <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4">
        <div class="flex items-center justify-between mb-3">
          <h2 class="font-display font-semibold text-sm text-tactical-gps tracking-wide">GPS POSITION</h2>
          <span class="w-2 h-2 rounded-full" :class="gpsFix ? 'bg-tactical-gps' : 'bg-gray-600'" />
        </div>

        <!-- Coordinates -->
        <div class="space-y-2 mb-3">
          <div>
            <span class="text-[10px] text-gray-500 block">LAT</span>
            <span class="font-mono text-sm text-gray-200">{{ gpsLat }}</span>
          </div>
          <div>
            <span class="text-[10px] text-gray-500 block">LON</span>
            <span class="font-mono text-sm text-gray-200">{{ gpsLon }}</span>
          </div>
        </div>

        <!-- Details -->
        <div class="space-y-1.5 text-[11px]">
          <div class="flex justify-between">
            <span class="text-gray-500">Satellites</span>
            <div class="flex items-center gap-1">
              <svg class="w-3 h-3 text-tactical-gps/60" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <circle cx="12" cy="12" r="10"/><path d="M12 2a15.3 15.3 0 014 10 15.3 15.3 0 01-4 10 15.3 15.3 0 01-4-10 15.3 15.3 0 014-10z"/><path d="M2 12h20"/>
              </svg>
              <span class="text-gray-300 font-mono">{{ gpsSats }}</span>
            </div>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Altitude</span>
            <span class="text-gray-300 font-mono">{{ gpsAlt }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Speed</span>
            <span class="text-gray-400 font-mono">N/A</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">HDOP</span>
            <span class="text-gray-400 font-mono">N/A</span>
          </div>
        </div>

        <router-link to="/map" class="block text-center text-[10px] text-tactical-gps/60 hover:text-tactical-gps mt-3 transition-colors">
          Open Map
        </router-link>
      </div>

      <!-- ═══ Panel 5: Iridium Location ═══ -->
      <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4">
        <div class="flex items-center justify-between mb-3">
          <h2 class="font-display font-semibold text-sm text-teal-400 tracking-wide">IRIDIUM LOCATION</h2>
          <span v-if="locationResolved"
            class="text-[9px] font-mono px-1.5 py-0.5 rounded"
            :class="locationResolved.source === 'gps' ? 'bg-emerald-400/10 text-emerald-400' : locationResolved.source === 'iridium' ? 'bg-teal-400/10 text-teal-400' : 'bg-amber-400/10 text-amber-400'">
            {{ locationResolved.source.toUpperCase() }}
          </span>
          <span v-else class="text-[9px] font-mono px-1.5 py-0.5 rounded bg-gray-700/50 text-gray-500">NO FIX</span>
        </div>

        <!-- Active (resolved) location -->
        <div v-if="locationResolved" class="mb-3">
          <span class="text-[10px] text-gray-500 block">AUTO Resolved</span>
          <div class="font-mono text-sm text-gray-200">
            {{ locationResolved.lat.toFixed(5) }}, {{ locationResolved.lon.toFixed(5) }}
          </div>
          <span v-if="locationResolved.accuracy_km" class="text-[10px] text-gray-500">
            ~{{ formatAccuracy(locationResolved.accuracy_km) }} accuracy
          </span>
        </div>
        <div v-else class="mb-3 text-[11px] text-gray-600">
          No location fix from any source
        </div>

        <!-- Source breakdown -->
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

        <!-- Priority legend -->
        <div class="mt-3 pt-2 border-t border-tactical-border">
          <span class="text-[9px] text-gray-600">Priority: GPS (5m) > Iridium (1-100km) > Custom</span>
        </div>

        <router-link to="/passes" class="block text-center text-[10px] text-teal-400/60 hover:text-teal-400 mt-2 transition-colors">
          Pass Predictor
        </router-link>
      </div>

      <!-- ═══ Panel 6: SBD Message Queue ═══ -->
      <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4">
        <div class="flex items-center justify-between mb-3">
          <h2 class="font-display font-semibold text-sm text-tactical-iridium tracking-wide">SBD QUEUE</h2>
          <span v-if="dlqPending > 0"
            class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-amber-400/10 text-amber-400">
            {{ dlqPending }} pending
          </span>
        </div>

        <!-- Queue items -->
        <div class="space-y-1 tactical-scroll max-h-[200px] overflow-y-auto">
          <div v-for="item in dlqItems" :key="item.id"
            class="flex items-center gap-2 py-1.5 px-2 rounded bg-tactical-bg/50"
            :class="item.status === 'sent' || item.status === 'received' ? 'opacity-60' : ''">
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

        <!-- Recent satellite messages -->
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

      <!-- ═══ Panel 7: Activity Log (col-span-2) ═══ -->
      <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4 md:col-span-2"
        @mouseenter="logPaused = true" @mouseleave="logPaused = false">
        <div class="flex items-center justify-between mb-3">
          <h2 class="font-display font-semibold text-sm text-gray-400 tracking-wide">ACTIVITY LOG</h2>
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

      <!-- ═══ Panel 8: Power System ═══ -->
      <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4">
        <div class="flex items-center justify-between mb-3">
          <h2 class="font-display font-semibold text-sm text-tactical-power tracking-wide">POWER SYSTEM</h2>
          <svg class="w-4 h-4 text-tactical-power/60" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <rect x="6" y="7" width="12" height="14" rx="1"/><line x1="10" y1="7" x2="10" y2="4"/><line x1="14" y1="7" x2="14" y2="4"/>
            <line x1="10" y1="12" x2="10" y2="17"/><line x1="14" y1="12" x2="14" y2="17"/>
          </svg>
        </div>

        <!-- Battery gauge -->
        <div class="mb-4">
          <div class="flex items-baseline gap-2 mb-1.5">
            <span class="font-mono text-2xl font-bold"
              :class="batteryLevel != null ? (batteryLevel > 60 ? 'text-tactical-power' : batteryLevel > 25 ? 'text-amber-400' : 'text-red-400') : 'text-gray-600'">
              {{ batteryLevel != null ? `${Math.round(batteryLevel)}%` : 'N/A' }}
            </span>
          </div>
          <div class="h-2 rounded-full bg-gray-800 overflow-hidden">
            <div class="h-full rounded-full transition-all duration-500"
              :class="batteryColor(batteryLevel)"
              :style="{ width: batteryLevel != null ? `${Math.max(2, batteryLevel)}%` : '0%' }" />
          </div>
        </div>

        <!-- Details -->
        <div class="space-y-1.5 text-[11px]">
          <div class="flex justify-between">
            <span class="text-gray-500">Voltage</span>
            <span class="text-gray-300 font-mono">{{ voltage != null ? `${voltage.toFixed(2)}V` : 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Uptime</span>
            <span class="text-gray-300 font-mono">{{ formatUptime(uptimeSeconds) }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Temperature</span>
            <span class="text-gray-300 font-mono">{{ temperature != null ? `${temperature.toFixed(1)}C` : 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">UPS</span>
            <span class="text-gray-400 font-mono">N/A</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Solar</span>
            <span class="text-gray-400 font-mono">N/A</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Current</span>
            <span class="text-gray-400 font-mono">N/A</span>
          </div>
        </div>
      </div>

    </div>
  </div>
</template>
