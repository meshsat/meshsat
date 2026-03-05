<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import { formatLastHeard, signalQualityClass, nodeStatusDot, shortId, isNodeActive, isNodeRecent } from '@/utils/format'

const store = useMeshsatStore()
const filter = ref('all') // 'all', 'active', 'stale'
const sortBy = ref('last_heard') // 'last_heard', 'name', 'signal'
const removing = ref(null)
const removingStale = ref(false)
const expandedNode = ref(null)
const nodeTelemetry = ref([])
const nodeNeighbors = ref([])
const telemetryLoading = ref(false)

const radioConnected = computed(() => store.status?.connected === true)
const now = ref(Date.now() / 1000)

// Template-friendly wrappers (now.value not accessible in template expressions)
const isActive = (node) => isNodeActive(node, now.value)
const isRecent = (node) => isNodeRecent(node, now.value)
const signalClass = (q) => signalQualityClass(q)
const signalDot = (node) => nodeStatusDot(node, now.value)

const filteredNodes = computed(() => {
  let list = [...(store.nodes || [])]
  if (filter.value === 'active') list = list.filter(n => isActive(n))
  else if (filter.value === 'stale') list = list.filter(n => !isActive(n))

  list.sort((a, b) => {
    if (sortBy.value === 'last_heard') return (b.last_heard || 0) - (a.last_heard || 0)
    if (sortBy.value === 'name') return (a.long_name || '').localeCompare(b.long_name || '')
    if (sortBy.value === 'signal') return (b.snr || -999) - (a.snr || -999)
    return 0
  })
  return list
})

const activeCount = computed(() => (store.nodes || []).filter(n => isActive(n)).length)
const staleCount = computed(() => (store.nodes || []).filter(n => !isActive(n)).length)

async function handleRemove(node) {
  const name = node.long_name || node.user_id || node.num
  if (!confirm(`Remove "${name}" from NodeDB?\n\nThis will forget this node from the radio.`)) return
  removing.value = node.num
  try {
    await store.removeNode(node.num)
  } catch { /* store error */ }
  removing.value = null
}

async function handleReboot(node) {
  if (!confirm(`Reboot node ${node.long_name || node.user_id}?`)) return
  try {
    await store.adminReboot({ node_id: node.num, delay_secs: 5 })
  } catch { /* store error */ }
}

async function handleTraceroute(node) {
  try {
    await store.adminTraceroute({ node_id: node.num })
  } catch { /* store error */ }
}

async function toggleNodeDetail(node) {
  if (expandedNode.value === node.num) {
    expandedNode.value = null
    return
  }
  expandedNode.value = node.num
  telemetryLoading.value = true
  const nodeId = node.user_id || `!${node.num.toString(16).padStart(8, '0')}`
  try {
    const data = await store.fetchTelemetry({ node: nodeId, limit: 50 })
    nodeTelemetry.value = data || []
  } catch { nodeTelemetry.value = [] }
  try {
    await store.fetchNeighborInfo()
    nodeNeighbors.value = (store.neighborInfo || []).filter(n => n.node_id === node.num)
  } catch { nodeNeighbors.value = [] }
  telemetryLoading.value = false
}

async function handleRemoveAllStale() {
  const staleNodes = (store.nodes || []).filter(n => !isActive(n))
  if (!staleNodes.length) return
  if (!confirm(`Remove ${staleNodes.length} stale node${staleNodes.length > 1 ? 's' : ''} from the radio's NodeDB?\n\nThis cannot be undone.`)) return
  removingStale.value = true
  for (const node of staleNodes) {
    try { await store.removeNode(node.num) } catch { /* continue */ }
  }
  removingStale.value = false
}

let nowTimer = null
onMounted(() => {
  Promise.all([store.fetchNodes(), store.fetchStatus()])
  nowTimer = setInterval(() => { now.value = Date.now() / 1000 }, 15000)
})
onUnmounted(() => { if (nowTimer) clearInterval(nowTimer) })
</script>

<template>
  <div class="max-w-5xl mx-auto">
    <!-- Header -->
    <div class="flex items-center justify-between mb-4">
      <div>
        <h1 class="text-lg font-semibold text-gray-200">Mesh Nodes</h1>
        <div class="text-xs text-gray-500 mt-0.5">
          {{ store.nodes?.length || 0 }} total
          <span class="text-emerald-400/80">{{ activeCount }} active</span>
          <span v-if="staleCount > 0" class="text-gray-600">{{ staleCount }} stale</span>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <button v-if="staleCount > 0" @click="handleRemoveAllStale" :disabled="removingStale"
          class="px-3 py-1.5 text-xs rounded bg-gray-800 text-red-400/70 hover:text-red-300 border border-red-900/30 hover:border-red-800/50 transition-colors disabled:opacity-50">
          {{ removingStale ? 'Removing...' : `Forget ${staleCount} stale` }}
        </button>
        <button @click="store.fetchNodes()"
          class="px-3 py-1.5 text-xs rounded bg-gray-800 text-gray-400 hover:text-gray-200 transition-colors">
          Refresh
        </button>
      </div>
    </div>

    <!-- Connection banner -->
    <div v-if="!radioConnected" class="bg-amber-900/20 border border-amber-800/50 rounded-lg p-3 text-amber-300/80 text-sm mb-4">
      Radio not connected. Connect a Meshtastic device to see nodes.
    </div>

    <!-- Filter + sort toolbar -->
    <div v-if="store.nodes?.length" class="flex items-center justify-between mb-3">
      <div class="flex gap-1">
        <button v-for="f in [
          { key: 'all', label: `All (${store.nodes?.length || 0})` },
          { key: 'active', label: `Active (${activeCount})` },
          { key: 'stale', label: `Stale (${staleCount})` },
        ]" :key="f.key"
          @click="filter = f.key"
          class="px-2.5 py-1 rounded text-xs font-medium transition-colors"
          :class="filter === f.key ? 'bg-gray-700 text-gray-200' : 'text-gray-500 hover:text-gray-300'">
          {{ f.label }}
        </button>
      </div>
      <select v-model="sortBy" class="px-2 py-1 rounded bg-gray-800 border border-gray-700 text-xs text-gray-400 focus:outline-none">
        <option value="last_heard">Sort: Last heard</option>
        <option value="name">Sort: Name</option>
        <option value="signal">Sort: Signal</option>
      </select>
    </div>

    <!-- Empty state -->
    <div v-if="!store.nodes?.length" class="bg-gray-800/30 rounded-lg p-8 border border-gray-800 text-center text-gray-500 text-sm">
      {{ radioConnected ? 'No nodes discovered yet. Nodes appear when packets are received.' : 'No nodes discovered' }}
    </div>

    <!-- Node cards -->
    <div v-else class="space-y-1.5">
      <div v-for="node in filteredNodes" :key="node.num"
        class="bg-gray-800/40 rounded-lg border px-4 py-3 group transition-colors hover:bg-gray-800/60"
        :class="isActive(node) ? 'border-gray-700/50' : 'border-gray-800/50 opacity-60 hover:opacity-80'">
        <div class="flex items-start gap-3">
          <!-- Status dot + avatar -->
          <div class="flex-shrink-0 relative mt-0.5">
            <div class="w-9 h-9 rounded-full bg-gray-700/50 flex items-center justify-center text-xs font-bold text-gray-400">
              {{ (node.short_name || '??').slice(0, 2).toUpperCase() }}
            </div>
            <span class="absolute -bottom-0.5 -right-0.5 w-2.5 h-2.5 rounded-full border-2 border-gray-950"
              :class="signalDot(node)"></span>
          </div>

          <!-- Info -->
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium text-gray-200 truncate">{{ node.long_name || 'Unknown' }}</span>
              <span class="text-[10px] text-gray-600 font-mono">{{ shortId(node.user_id) }}</span>
            </div>
            <div class="flex items-center gap-3 mt-1 text-[11px]">
              <span v-if="node.hw_model_name" class="text-gray-500">{{ node.hw_model_name }}</span>
              <span v-if="node.snr != null" :class="node.snr >= 0 ? 'text-emerald-400/70' : node.snr >= -10 ? 'text-amber-400/70' : 'text-red-400/70'">
                SNR {{ Number(node.snr).toFixed(1) }}
              </span>
              <span v-if="node.rssi" class="text-gray-500">{{ node.rssi }} dBm</span>
              <span v-if="node.signal_quality" class="px-1.5 py-px rounded text-[10px] font-medium"
                :class="signalClass(node.signal_quality)">
                {{ node.signal_quality }}
              </span>
              <span v-if="node.battery_level" class="text-gray-500">
                {{ Math.round(node.battery_level) }}%
                <span v-if="node.voltage" class="text-gray-600">{{ node.voltage.toFixed(1) }}V</span>
              </span>
            </div>
          </div>

          <!-- Last heard + actions -->
          <div class="flex-shrink-0 text-right">
            <div class="text-xs" :class="isActive(node) ? 'text-gray-400' : 'text-gray-600'">
              {{ formatLastHeard(node.last_heard) }}
            </div>
            <div class="flex items-center gap-1 mt-1.5 opacity-0 group-hover:opacity-100 transition-opacity">
              <button @click="handleTraceroute(node)" title="Traceroute"
                class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-teal-400 transition-colors">
                Trace
              </button>
              <button @click="handleReboot(node)" title="Reboot"
                class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-amber-400 transition-colors">
                Reboot
              </button>
              <button @click="handleRemove(node)" :disabled="removing === node.num" title="Remove from NodeDB"
                class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-red-400 transition-colors disabled:opacity-50">
                {{ removing === node.num ? '...' : 'Forget' }}
              </button>
            </div>
          </div>
        </div>

        <!-- GPS row (if position available) -->
        <div v-if="node.latitude || node.longitude" class="flex items-center gap-3 mt-1.5 pl-12 text-[10px] text-gray-600">
          <span>{{ node.latitude?.toFixed(5) }}, {{ node.longitude?.toFixed(5) }}</span>
          <span v-if="node.altitude">{{ node.altitude }}m</span>
          <span v-if="node.sats">{{ node.sats }} sats</span>
        </div>

        <!-- Expand toggle -->
        <div class="mt-1.5 pl-12">
          <button @click="toggleNodeDetail(node)" class="text-[10px] text-gray-600 hover:text-teal-400 transition-colors">
            {{ expandedNode === node.num ? 'Hide details' : 'Show details' }}
          </button>
        </div>

        <!-- Expanded detail (telemetry + neighbors) -->
        <div v-if="expandedNode === node.num" class="mt-3 pl-12 space-y-3">
          <div v-if="telemetryLoading" class="text-xs text-gray-500">Loading...</div>

          <!-- Telemetry history -->
          <div v-if="nodeTelemetry.length > 0">
            <h4 class="text-xs font-medium text-gray-400 mb-1.5">Telemetry History ({{ nodeTelemetry.length }} records)</h4>
            <div class="overflow-x-auto">
              <table class="w-full text-[10px] text-gray-400">
                <thead>
                  <tr class="text-gray-600">
                    <th class="text-left pr-2 py-1">Time</th>
                    <th class="text-right pr-2 py-1">Battery</th>
                    <th class="text-right pr-2 py-1">Voltage</th>
                    <th class="text-right pr-2 py-1">Ch Util</th>
                    <th class="text-right pr-2 py-1">Air Util</th>
                    <th class="text-right pr-2 py-1">Temp</th>
                    <th class="text-right py-1">Uptime</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="t in nodeTelemetry.slice(0, 20)" :key="t.id" class="border-t border-gray-800/50">
                    <td class="pr-2 py-0.5 text-gray-600">{{ new Date(t.created_at).toLocaleTimeString() }}</td>
                    <td class="text-right pr-2 py-0.5">{{ t.battery_level }}%</td>
                    <td class="text-right pr-2 py-0.5">{{ t.voltage?.toFixed(2) }}V</td>
                    <td class="text-right pr-2 py-0.5">{{ t.channel_util?.toFixed(1) }}%</td>
                    <td class="text-right pr-2 py-0.5">{{ t.air_util_tx?.toFixed(1) }}%</td>
                    <td class="text-right pr-2 py-0.5">{{ t.temperature != null ? `${t.temperature.toFixed(1)}°` : '-' }}</td>
                    <td class="text-right py-0.5">{{ t.uptime_seconds ? `${Math.floor(t.uptime_seconds / 3600)}h` : '-' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <!-- Mini sparkline: battery over time -->
            <div class="mt-2 flex items-end gap-px h-8">
              <div v-for="(t, i) in nodeTelemetry.slice(0, 30).reverse()" :key="i"
                class="flex-1 rounded-t"
                :class="t.battery_level > 50 ? 'bg-emerald-500/40' : t.battery_level > 20 ? 'bg-amber-500/40' : 'bg-red-500/40'"
                :style="{ height: `${Math.max(2, t.battery_level * 0.3)}px` }"
                :title="`${t.battery_level}% at ${new Date(t.created_at).toLocaleTimeString()}`">
              </div>
            </div>
            <div class="text-[9px] text-gray-600 mt-0.5">Battery level over time</div>
          </div>

          <!-- Neighbor info -->
          <div v-if="nodeNeighbors.length > 0">
            <h4 class="text-xs font-medium text-gray-400 mb-1.5">Neighbors</h4>
            <div class="flex flex-wrap gap-2">
              <div v-for="ni in nodeNeighbors" :key="ni.node_id" class="bg-gray-900/50 rounded px-2 py-1 text-[10px]">
                <div v-for="n in ni.neighbors" :key="n.node_id" class="flex items-center gap-2 py-0.5">
                  <span class="text-gray-400 font-mono">!{{ n.node_id.toString(16).padStart(8, '0') }}</span>
                  <span :class="n.snr >= 0 ? 'text-emerald-400/70' : 'text-amber-400/70'">SNR {{ n.snr?.toFixed(1) }}</span>
                </div>
              </div>
            </div>
          </div>

          <div v-if="!telemetryLoading && nodeTelemetry.length === 0 && nodeNeighbors.length === 0" class="text-xs text-gray-600">
            No telemetry or neighbor data for this node.
          </div>
        </div>
      </div>
    </div>

    <!-- Stale nodes hint -->
    <div v-if="staleCount > 0 && filter !== 'stale'" class="mt-4 text-center">
      <button @click="filter = 'stale'" class="text-[11px] text-gray-600 hover:text-gray-400 transition-colors">
        {{ staleCount }} stale node{{ staleCount > 1 ? 's' : '' }} (not heard in 2+ hours) — click to manage
      </button>
    </div>
  </div>
</template>
