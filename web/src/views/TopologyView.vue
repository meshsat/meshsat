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

// Event type icons for flow table
const eventStyles = {
  forward: { icon: '→', color: 'text-emerald-400 bg-emerald-400/10', label: 'FWD' },
  forward_error: { icon: '✕', color: 'text-red-400 bg-red-400/10', label: 'ERR' },
  signal: { icon: '◆', color: 'text-purple-400 bg-purple-400/10', label: 'SIG' },
  message: { icon: '◈', color: 'text-cyan-400 bg-cyan-400/10', label: 'MSG' },
  inbound: { icon: '↓', color: 'text-teal-400 bg-teal-400/10', label: 'IN' },
  relay: { icon: '⇌', color: 'text-amber-400 bg-amber-400/10', label: 'RLY' },
  connected: { icon: '●', color: 'text-emerald-400 bg-emerald-400/10', label: 'ON' },
  disconnected: { icon: '○', color: 'text-gray-400 bg-gray-400/10', label: 'OFF' },
  scheduler: { icon: '◷', color: 'text-blue-400 bg-blue-400/10', label: 'SCH' },
  dlq: { icon: '◫', color: 'text-orange-400 bg-orange-400/10', label: 'DLQ' },
  HEMB_SYMBOL_SENT: { icon: '↑', color: 'text-cyan-400 bg-cyan-400/10', label: 'SENT' },
  HEMB_SYMBOL_RECEIVED: { icon: '↓', color: 'text-teal-400 bg-teal-400/10', label: 'RECV' },
  HEMB_GENERATION_DECODED: { icon: '✓', color: 'text-emerald-400 bg-emerald-400/10', label: 'DEC' },
  HEMB_GENERATION_FAILED: { icon: '✕', color: 'text-red-400 bg-red-400/10', label: 'FAIL' },
}
function evStyle(type) { return eventStyles[type] || { icon: '·', color: 'text-gray-500 bg-gray-500/10', label: type?.substring(0, 6) || '?' } }

function formatTime(ts) {
  if (!ts) return ''
  try { return new Date(ts).toLocaleTimeString('en-GB', { hour12: false, fractionalSecondDigits: 1 }) } catch { return '' }
}

function eventDetail(e) {
  const p = e.payload
  if (!p || typeof p !== 'object') return typeof p === 'string' ? p.substring(0, 60) : ''
  return p.message || p.msg || p.text || p.error || ''
}

const filteredEvents = computed(() => {
  let evts = store.events.value || []
  if (filterType.value) evts = evts.filter(e => e.type === filterType.value)
  if (store.selectedNodeId.value) evts = store.filteredEvents.value || evts
  return evts
})
</script>

<template>
  <div class="flex flex-col h-full overflow-hidden">
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
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">HeMB</span>
        <span class="font-mono text-emerald-400">{{ store.hembStats.value?.generations_decoded ?? 0 }}</span>
        <span class="text-gray-600">/</span>
        <span class="font-mono text-red-400">{{ store.hembStats.value?.generations_failed ?? 0 }}</span>
      </div>
      <div class="ml-auto flex items-center gap-2">
        <div class="w-2 h-2 rounded-full" :class="store.connected.value ? 'bg-emerald-400' : 'bg-red-400'"></div>
        <span class="text-gray-500">{{ store.connected.value ? 'SSE' : 'Offline' }}</span>
        <span class="text-gray-600">{{ (store.events.value || []).length }} events</span>
        <button v-if="store.selectedNodeId.value" @click="store.selectNode(null)"
          class="px-2 py-0.5 bg-teal-900/30 text-teal-400 text-[10px] rounded">Clear</button>
        <button @click="showDebug = !showDebug"
          class="px-2 py-0.5 rounded text-[10px]"
          :class="showDebug ? 'bg-purple-900/30 text-purple-400' : 'bg-gray-800 text-gray-500 hover:text-gray-300'">Debug</button>
      </div>
    </div>

    <!-- Graph: fixed 55% of remaining space -->
    <div class="flex-none border-b border-gray-800" style="height: calc(55vh - 40px); min-height: 200px">
      <ObservabilityGraph />
    </div>

    <!-- Flow table: fills remaining space -->
    <div class="flex-1 min-h-0 flex flex-col overflow-hidden">
      <!-- Table header bar -->
      <div class="flex-none flex items-center gap-3 px-4 py-1.5 border-b border-gray-800 text-xs">
        <span class="text-gray-500 uppercase tracking-wider text-[10px]">Columns</span>
        <select v-model="filterType"
          class="bg-gray-800 text-gray-300 border border-gray-700 rounded px-2 py-0.5 text-xs">
          <option value="">All</option>
          <option value="forward">Forward</option>
          <option value="signal">Signal</option>
          <option value="message">Message</option>
          <option value="inbound">Inbound</option>
          <option value="HEMB_SYMBOL_SENT">HeMB Sent</option>
          <option value="HEMB_GENERATION_DECODED">HeMB Decoded</option>
        </select>
        <span class="text-gray-600 ml-auto">{{ filteredEvents.length }} flows</span>
      </div>

      <!-- Column headers -->
      <div class="flex-none grid grid-cols-[80px_70px_1fr_1fr_80px_1fr] gap-0 px-4 py-1 text-[10px] text-gray-500 uppercase tracking-wider border-b border-gray-800/50">
        <span>Time</span>
        <span>Event</span>
        <span>Source</span>
        <span>Destination</span>
        <span>Verdict</span>
        <span>Detail</span>
      </div>

      <!-- Flow rows (scrollable) -->
      <div class="flex-1 overflow-y-auto">
        <div v-for="(ev, i) in filteredEvents.slice(0, 200)" :key="ev.ts + '-' + i"
          class="grid grid-cols-[80px_70px_1fr_1fr_80px_1fr] gap-0 px-4 py-1 text-xs border-b border-gray-800/30 hover:bg-white/[0.02]">
          <span class="font-mono text-gray-500 truncate">{{ formatTime(ev.ts) }}</span>
          <span>
            <span class="inline-flex items-center gap-0.5 px-1 py-px rounded text-[10px] font-mono"
              :class="evStyle(ev.type).color">
              {{ evStyle(ev.type).icon }} {{ evStyle(ev.type).label }}
            </span>
          </span>
          <span class="font-mono text-gray-400 truncate">{{ ev.source || '—' }}</span>
          <span class="font-mono text-gray-400 truncate">{{ ev.payload?.forward_to || ev.payload?.interface_id || '—' }}</span>
          <span class="font-mono" :class="ev.type?.includes('error') || ev.type?.includes('FAIL') ? 'text-red-400' : 'text-emerald-400'">
            {{ ev.type?.includes('error') || ev.type?.includes('FAIL') ? 'dropped' : 'forwarded' }}
          </span>
          <span class="text-gray-500 truncate">{{ eventDetail(ev) }}</span>
        </div>
        <div v-if="filteredEvents.length === 0" class="px-4 py-6 text-center text-gray-600 text-sm">
          Waiting for events...
        </div>
      </div>
    </div>

    <!-- Debug panel -->
    <HeMBMatrixInspector v-if="showDebug" />
  </div>
</template>
