<script setup>
import { onMounted, onUnmounted, ref, computed } from 'vue'
import { useObservabilityStore } from '../composables/useObservabilityStore'
import ObservabilityGraph from '../components/ObservabilityGraph.vue'
import HeMBMatrixInspector from '../components/HeMBMatrixInspector.vue'

const store = useObservabilityStore()
const showDebug = ref(false)
const filterType = ref('')

let pollFast = null
let pollSlow = null

onMounted(async () => {
  await store.fetchAll()
  store.connectSSE()
  pollFast = setInterval(() => {
    store.fetchInterfaces()
    store.fetchGateways()
    store.fetchHembStats()
  }, 10000)
  pollSlow = setInterval(() => { store.fetchAll() }, 30000)
})

onUnmounted(() => {
  store.closeSSE()
  if (pollFast) clearInterval(pollFast)
  if (pollSlow) clearInterval(pollSlow)
})

const eventStyles = {
  forward: { icon: '→', color: 'text-emerald-400', label: 'forwarded' },
  forward_error: { icon: '✕', color: 'text-red-400', label: 'dropped' },
  signal: { icon: '◆', color: 'text-purple-400', label: 'signal' },
  message: { icon: '◈', color: 'text-cyan-400', label: 'message' },
  inbound: { icon: '↓', color: 'text-teal-400', label: 'inbound' },
  relay: { icon: '⇌', color: 'text-amber-400', label: 'relay' },
  connected: { icon: '●', color: 'text-emerald-400', label: 'connected' },
  disconnected: { icon: '○', color: 'text-gray-500', label: 'disconnected' },
  HEMB_SYMBOL_SENT: { icon: '↑', color: 'text-cyan-400', label: 'forwarded' },
  HEMB_SYMBOL_RECEIVED: { icon: '↓', color: 'text-teal-400', label: 'forwarded' },
  HEMB_GENERATION_DECODED: { icon: '✓', color: 'text-emerald-400', label: 'forwarded' },
  HEMB_GENERATION_FAILED: { icon: '✕', color: 'text-red-400', label: 'dropped' },
}
function evStyle(type) { return eventStyles[type] || { icon: '·', color: 'text-gray-500', label: type?.substring(0, 12) || '?' } }

function fmtTime(ts) {
  if (!ts) return ''
  try {
    const d = new Date(ts)
    if (isNaN(d.getTime())) return ''
    const hh = String(d.getHours()).padStart(2, '0')
    const mm = String(d.getMinutes()).padStart(2, '0')
    const ss = String(d.getSeconds()).padStart(2, '0')
    return `${hh}:${mm}:${ss}`
  } catch { return '' }
}

function evSource(ev) {
  const p = ev.payload
  if (typeof p === 'object' && p) {
    return p.bearer_type || p.interface_id || p.source_iface || ev.source || '—'
  }
  return ev.source || '—'
}

function evDest(ev) {
  const p = ev.payload
  if (typeof p === 'object' && p) {
    return p.forward_to || p.dest_iface || p.target || '—'
  }
  return '—'
}

function evPort(ev) {
  const p = ev.payload
  if (typeof p === 'object' && p) {
    return p.payload_bytes ? `${p.payload_bytes}B` : p.port || ''
  }
  return ''
}

function evVerdict(ev) {
  return evStyle(ev.type).label
}

function evVerdictColor(ev) {
  const s = evStyle(ev.type)
  return s.color
}

const filteredEvents = computed(() => {
  let evts = store.events.value || []
  if (filterType.value) evts = evts.filter(e => e.type === filterType.value)
  if (store.selectedNodeId.value) evts = store.filteredEvents.value || evts
  return evts
})
</script>

<template>
  <div class="flex flex-col overflow-hidden" style="height: calc(100vh - 90px)">
    <!-- Stats bar -->
    <div class="flex-none flex items-center gap-6 px-4 py-2 border-b border-gray-800 text-xs">
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Interfaces</span>
        <span class="font-mono text-gray-200">{{ store.onlineCount.value }}/{{ (store.interfaceNodes.value || []).length }}</span>
      </div>
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Nodes</span>
        <span class="font-mono text-gray-200">{{ (store.meshNodeList.value || []).length }}</span>
      </div>
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Peers</span>
        <span class="font-mono text-gray-200">{{ (store.peerNodes.value || []).length }}</span>
      </div>
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Rules</span>
        <span class="font-mono text-gray-200">{{ (store.ruleEdges.value || []).length }}</span>
      </div>
      <div class="ml-auto flex items-center gap-2">
        <div class="w-2 h-2 rounded-full" :class="store.connected.value ? 'bg-emerald-400' : 'bg-red-400'"></div>
        <span class="text-gray-500 font-mono">{{ (store.events.value || []).length }} flows</span>
        <button v-if="store.selectedNodeId.value" @click="store.selectNode(null)"
          class="px-2 py-0.5 bg-teal-900/30 text-teal-400 text-[10px] rounded">Clear</button>
        <button @click="showDebug = !showDebug"
          class="px-2 py-0.5 rounded text-[10px]"
          :class="showDebug ? 'bg-purple-900/30 text-purple-400' : 'bg-gray-800 text-gray-500 hover:text-gray-300'">Debug</button>
      </div>
    </div>

    <!-- Graph -->
    <div class="border-b border-gray-800 overflow-hidden" style="flex: 55 0 0%">
      <ObservabilityGraph />
    </div>

    <!-- Flow table (Hubble-style) -->
    <div class="flex flex-col overflow-hidden" style="flex: 45 0 0%">
      <!-- Column header + filter -->
      <div class="flex-none flex items-center border-b border-gray-800/60 px-4 py-1.5">
        <span class="text-[10px] text-gray-500 uppercase tracking-wider mr-3">Columns</span>
        <select v-model="filterType"
          class="bg-gray-800/50 text-gray-400 border border-gray-700/50 rounded px-2 py-0.5 text-xs">
          <option value="">All</option>
          <option value="HEMB_SYMBOL_SENT">HeMB Sent</option>
          <option value="HEMB_GENERATION_DECODED">HeMB Decoded</option>
          <option value="forward">Forward</option>
          <option value="signal">Signal</option>
          <option value="inbound">Inbound</option>
        </select>
        <span class="ml-auto text-gray-600 text-xs font-mono">{{ filteredEvents.length }} flows</span>
      </div>

      <!-- Table header -->
      <div class="flex-none grid grid-cols-[160px_160px_120px_60px_100px_1fr] px-4 py-1.5 text-[11px] text-gray-500 border-b border-gray-800/40">
        <span>Source Identity</span>
        <span>Destination Identity</span>
        <span>Destination Port</span>
        <span>L7 info</span>
        <span>Verdict</span>
        <span>Timestamp</span>
      </div>

      <!-- Rows (scrollable) -->
      <div class="flex-1 overflow-y-auto">
        <div v-for="(ev, i) in filteredEvents.slice(0, 200)" :key="i"
          class="grid grid-cols-[160px_160px_120px_60px_100px_1fr] px-4 py-1 text-xs border-b border-gray-800/20 hover:bg-white/[0.02] cursor-default">
          <span class="text-gray-300 truncate">{{ evSource(ev) }} <span class="text-gray-600">{{ evSource(ev) }}</span></span>
          <span class="text-gray-300 truncate">{{ evDest(ev) }} <span class="text-gray-600">{{ evDest(ev) }}</span></span>
          <span class="text-gray-400 font-mono text-right pr-4">{{ evPort(ev) }}</span>
          <span class="text-gray-600">—</span>
          <span :class="evVerdictColor(ev)">{{ evVerdict(ev) }}</span>
          <span class="text-gray-500 font-mono">{{ fmtTime(ev.ts) }}</span>
        </div>
        <div v-if="filteredEvents.length === 0" class="px-4 py-8 text-center text-gray-600 text-sm">
          Waiting for flows...
        </div>
      </div>
    </div>

    <!-- Debug panel -->
    <HeMBMatrixInspector v-if="showDebug" />
  </div>
</template>
