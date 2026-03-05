<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { useMeshsatStore } from '@/stores/meshsat'
import { formatTimeHHMM } from '@/utils/format'

const route = useRoute()
const store = useMeshsatStore()
const utcTime = ref('')

const tabs = [
  { name: 'dashboard', label: 'Dashboard', path: '/' },
  { name: 'comms', label: 'Comms', path: '/messages' },
  { name: 'nodes', label: 'Peers', path: '/nodes' },
  { name: 'bridge', label: 'Bridge', path: '/bridge' },
  { name: 'passes', label: 'Passes', path: '/passes' },
  { name: 'map', label: 'Map', path: '/map' },
  { name: 'settings', label: 'Settings', path: '/settings' },
  { name: 'help', label: 'Help', path: '/help' },
  { name: 'about', label: 'About', path: '/about' }
]

function isActive(tab) {
  if (tab.path === '/') return route.path === '/'
  return route.path.startsWith(tab.path)
}

const deviceId = computed(() => {
  const id = store.status?.node_id
  if (!id) return '----'
  const hex = id.toString(16).toUpperCase()
  return '!' + hex.slice(-8)
})

// Mesh status
const meshConnected = computed(() => store.status?.connected ?? false)
const nodeCount = computed(() => {
  const total = (store.nodes || []).length
  const cutoff = Date.now() / 1000 - 7200
  const active = (store.nodes || []).filter(n => n.last_heard > cutoff).length
  return { active, total }
})
// Average mesh SNR from all active nodes
const meshAvgSNR = computed(() => {
  const cutoff = Date.now() / 1000 - 7200
  const activeNodes = (store.nodes || []).filter(n => n.last_heard > cutoff && n.snr != null && Math.abs(n.snr) < 100)
  if (!activeNodes.length) return null
  const avg = activeNodes.reduce((sum, n) => sum + n.snr, 0) / activeNodes.length
  return avg
})

// Iridium
const satBars = computed(() => store.iridiumSignal?.bars ?? -1)

// Next pass countdown
const nextPassInfo = computed(() => {
  const now = Date.now() / 1000
  const passes = store.passes || []
  // Find the first upcoming or active pass
  const active = passes.find(p => p.is_active)
  if (active) return { label: 'NOW', color: 'text-tactical-iridium' }
  const next = passes.find(p => p.aos > now)
  if (!next) return null
  const diffSec = next.aos - now
  if (diffSec < 60) return { label: `${Math.round(diffSec)}s`, color: 'text-tactical-iridium' }
  if (diffSec < 3600) return { label: `${Math.round(diffSec / 60)}m`, color: 'text-gray-400' }
  if (diffSec < 86400) return { label: `${Math.round(diffSec / 3600)}h`, color: 'text-gray-500' }
  return { label: formatTimeHHMM(next.aos), color: 'text-gray-600' }
})

// Cellular
const cellBars = computed(() => store.cellularSignal?.bars ?? -1)

// GPS fix
const gpsFix = computed(() => {
  const sources = store.locationSources?.sources || []
  const gps = sources.find(s => s.source === 'gps')
  return gps && gps.lat !== 0
})

function updateClock() {
  const now = new Date()
  utcTime.value = now.toISOString().slice(11, 19) + 'Z'
}

let pollTimer = null
let clockTimer = null

onMounted(() => {
  updateClock()
  clockTimer = setInterval(updateClock, 1000)
  store.fetchStatus()
  store.fetchNodes()
  store.fetchGateways()
  store.fetchIridiumSignalFast()
  store.fetchCellularSignal()
  store.fetchCellularStatus()
  store.fetchLocationSources()
  // Fetch passes for next-pass countdown (use resolved location)
  store.fetchLocations().then(() => {
    store.fetchLocationSources().then(() => {
      const resolved = store.locationSources?.resolved
      if (resolved) {
        store.fetchPasses({
          lat: resolved.lat, lon: resolved.lon,
          alt_m: (resolved.alt_km || 0) * 1000,
          hours: 12, min_elev: 5
        })
      }
    })
  })
  pollTimer = setInterval(() => {
    store.fetchStatus()
    store.fetchNodes()
    store.fetchGateways()
    store.fetchIridiumSignalFast()
    store.fetchCellularSignal()
    store.fetchCellularStatus()
    store.fetchLocationSources()
  }, 10000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
  if (clockTimer) clearInterval(clockTimer)
})
</script>

<template>
  <div class="min-h-screen bg-tactical-bg text-gray-100 flex flex-col relative">
    <!-- Fullscreen background logo -->
    <div class="fixed inset-0 z-0 flex items-center justify-center pointer-events-none">
      <img src="/logo-bg.png" alt="" class="w-[150vmin] h-[150vmin] object-contain opacity-[0.04]" />
    </div>

    <!-- Sticky horizontal header -->
    <header class="sticky top-0 z-50 bg-tactical-surface/95 backdrop-blur border-b border-tactical-border">
      <div class="flex items-center h-12 px-3 lg:px-5 gap-3">
        <!-- Left: Brand text -->
        <router-link to="/" class="flex items-center shrink-0">
          <span class="font-display font-semibold text-sm text-gray-200 tracking-wide">MeshSat</span>
        </router-link>

        <!-- Center: Nav tabs (scrollable on mobile) -->
        <nav class="flex-1 flex items-center overflow-x-auto no-scrollbar mx-2 lg:mx-6">
          <div class="flex items-center gap-0.5">
            <router-link v-for="tab in tabs" :key="tab.name" :to="tab.path"
              class="px-3 py-1.5 rounded text-xs font-medium whitespace-nowrap transition-colors"
              :class="isActive(tab)
                ? 'bg-tactical-iridium/10 text-tactical-iridium'
                : 'text-gray-500 hover:text-gray-300 hover:bg-white/5'">
              {{ tab.label }}
            </router-link>
          </div>
        </nav>

        <!-- Right: Status indicators -->
        <div class="flex items-center gap-3 shrink-0">

          <!-- Iridium: label + signal bars + next pass -->
          <div class="hidden md:flex items-center gap-1.5">
            <span class="text-[9px] font-medium text-tactical-iridium/70">IRD</span>
            <div class="flex items-end gap-px h-3">
              <span v-for="i in 5" :key="i" class="w-[3px] rounded-[1px]"
                :class="satBars >= i ? (satBars <= 2 ? 'bg-amber-400' : 'bg-tactical-iridium') : 'bg-gray-700/50'"
                :style="{ height: `${3 + i * 2}px` }" />
            </div>
            <span v-if="nextPassInfo" class="text-[9px] font-mono" :class="nextPassInfo.color">
              {{ nextPassInfo.label }}
            </span>
          </div>

          <!-- Divider -->
          <span class="hidden md:block w-px h-4 bg-gray-700/50" />

          <!-- Mesh: label + avg SNR + device ID + node count -->
          <div class="hidden md:flex items-center gap-1.5">
            <span class="text-[9px] font-medium text-tactical-lora/70">MESH</span>
            <span class="w-1.5 h-1.5 rounded-full"
              :class="meshConnected ? 'bg-emerald-400' : 'bg-red-400'" />
            <span v-if="meshAvgSNR !== null" class="text-[9px] font-mono"
              :class="meshAvgSNR >= 0 ? 'text-emerald-400/70' : meshAvgSNR >= -10 ? 'text-amber-400/70' : 'text-red-400/70'">
              {{ meshAvgSNR.toFixed(0) }}dB
            </span>
            <span class="text-[9px] font-mono text-gray-500">{{ deviceId }}</span>
            <span class="text-[9px] font-mono text-gray-600">{{ nodeCount.active }}/{{ nodeCount.total }}</span>
          </div>

          <!-- Divider -->
          <span class="hidden md:block w-px h-4 bg-gray-700/50" />

          <!-- Cellular: label + signal bars + type -->
          <div class="hidden md:flex items-center gap-1">
            <span class="text-[9px] font-medium text-sky-400/70">CELL</span>
            <template v-if="cellBars >= 0">
              <div class="flex items-end gap-px h-3">
                <span v-for="i in 5" :key="'cell'+i" class="w-[3px] rounded-[1px]"
                  :class="cellBars >= i ? 'bg-sky-400' : 'bg-gray-700/50'"
                  :style="{ height: `${3 + i * 2}px` }" />
              </div>
              <span class="text-[9px] text-sky-400/60 font-mono">{{ store.cellularStatus?.network_type || 'LTE' }}</span>
            </template>
            <span v-else class="text-[9px] text-gray-600 font-mono">--</span>
          </div>

          <!-- GPS fix indicator -->
          <div class="hidden md:flex items-center gap-1">
            <span class="w-1.5 h-1.5 rounded-full" :class="gpsFix ? 'bg-tactical-gps' : 'bg-gray-600'" />
            <span class="text-[9px]" :class="gpsFix ? 'text-tactical-gps/70' : 'text-gray-600'">GPS</span>
          </div>

          <!-- Divider -->
          <span class="hidden md:block w-px h-4 bg-gray-700/50" />

          <!-- UTC Clock -->
          <span class="text-[10px] font-mono text-gray-500 tabular-nums">{{ utcTime }}</span>
        </div>
      </div>
    </header>

    <!-- Main content -->
    <main class="flex-1">
      <div class="p-3 sm:p-4 lg:p-5">
        <router-view />
      </div>
    </main>
  </div>
</template>

<style scoped>
.no-scrollbar::-webkit-scrollbar {
  display: none;
}
.no-scrollbar {
  -ms-overflow-style: none;
  scrollbar-width: none;
}
</style>
