<script setup>
import { computed, onMounted, ref } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const filter = ref('all') // 'all', 'active', 'stale'
const sortBy = ref('last_heard') // 'last_heard', 'name', 'signal'
const removing = ref(null)

const radioConnected = computed(() => store.status?.connected === true)
const now = computed(() => Date.now() / 1000)

function isActive(node) {
  if (!node.last_heard) return false
  return (now.value - node.last_heard) < 7200 // 2 hours
}

function isRecent(node) {
  if (!node.last_heard) return false
  return (now.value - node.last_heard) < 86400 // 24 hours
}

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

function signalClass(q) {
  if (!q) return 'text-gray-600'
  const u = q.toUpperCase()
  if (u === 'GOOD') return 'text-emerald-400'
  if (u === 'FAIR') return 'text-amber-400'
  return 'text-red-400'
}

function signalDot(node) {
  if (isActive(node)) return 'bg-emerald-400'
  if (isRecent(node)) return 'bg-amber-400'
  return 'bg-gray-600'
}

function shortId(id) {
  if (!id) return ''
  if (id.startsWith('!') && id.length > 6) return id.slice(0, 3) + '..' + id.slice(-4)
  return id
}

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

onMounted(() => {
  Promise.all([store.fetchNodes(), store.fetchStatus()])
})
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
      <button @click="store.fetchNodes()"
        class="px-3 py-1.5 text-xs rounded bg-gray-800 text-gray-400 hover:text-gray-200 transition-colors">
        Refresh
      </button>
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
