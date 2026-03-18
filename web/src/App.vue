<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { useMeshsatStore } from '@/stores/meshsat'
import { useAuthStore } from '@/stores/auth'
import { formatTimeHHMM } from '@/utils/format'

const route = useRoute()
const store = useMeshsatStore()
const auth = useAuthStore()
const utcTime = ref('')
const userMenuOpen = ref(false)

function toggleUserMenu() {
  userMenuOpen.value = !userMenuOpen.value
}

function closeUserMenu() {
  userMenuOpen.value = false
}

const allTabs = [
  { name: 'dashboard', label: 'Dashboard', path: '/' },
  { name: 'comms', label: 'Comms', path: '/messages' },
  { name: 'nodes', label: 'Peers', path: '/nodes' },
  { name: 'bridge', label: 'Bridge', path: '/bridge' },
  { name: 'interfaces', label: 'Interfaces', path: '/interfaces' },
  { name: 'passes', label: 'Passes', path: '/passes' },
  { name: 'topology', label: 'Topology', path: '/topology' },
  { name: 'map', label: 'Map', path: '/map' },
  { name: 'settings', label: 'Settings', path: '/settings', minRole: 'operator' },
  { name: 'audit', label: 'Audit', path: '/audit', minRole: 'operator' },
  { name: 'help', label: 'Help', path: '/help' },
  { name: 'about', label: 'About', path: '/about' }
]

// Filter tabs based on user role
const tabs = computed(() => allTabs.filter(t => !t.minRole || auth.hasRole(t.minRole)))

function isActive(tab) {
  if (tab.path === '/') return route.path === '/'
  return route.path.startsWith(tab.path)
}

// SOS indicator (persistent across all views)
const sosActive = computed(() => store.sosStatus?.active === true)

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
  store.fetchSOSStatus()
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

          <!-- Auth: user menu dropdown (only when auth is enabled) -->
          <div v-if="auth.authEnabled && auth.user" class="hidden md:flex items-center relative">
            <button @click="toggleUserMenu" class="flex items-center gap-1.5 px-1.5 py-0.5 rounded hover:bg-white/5 transition-colors">
              <div class="w-5 h-5 rounded-full bg-tactical-iridium/20 border border-tactical-iridium/30 flex items-center justify-center">
                <span class="text-[9px] font-bold text-tactical-iridium">{{ (auth.displayName || '?')[0].toUpperCase() }}</span>
              </div>
              <span class="text-[9px] text-gray-400 truncate max-w-[80px]">{{ auth.displayName }}</span>
              <svg class="w-2.5 h-2.5 text-gray-500" viewBox="0 0 20 20" fill="currentColor">
                <path fill-rule="evenodd" d="M5.293 7.293a1 1 0 011.414 0L10 10.586l3.293-3.293a1 1 0 111.414 1.414l-4 4a1 1 0 01-1.414 0l-4-4a1 1 0 010-1.414z" clip-rule="evenodd" />
              </svg>
            </button>

            <!-- Dropdown -->
            <div v-if="userMenuOpen" class="absolute right-0 top-full mt-1 w-56 bg-tactical-surface border border-tactical-border rounded-lg shadow-xl z-50" @mouseleave="closeUserMenu">
              <!-- User info -->
              <div class="px-3 py-2.5 border-b border-tactical-border">
                <p class="text-xs font-medium text-gray-200 truncate">{{ auth.user.name || auth.user.preferred_username || 'User' }}</p>
                <p v-if="auth.user.email" class="text-[10px] text-gray-500 truncate">{{ auth.user.email }}</p>
                <div class="flex items-center gap-2 mt-1.5">
                  <span v-if="auth.role" class="px-1.5 py-0.5 text-[9px] font-medium rounded border"
                    :class="auth.role === 'owner' ? 'bg-red-500/20 text-red-400 border-red-500/30' : auth.role === 'operator' ? 'bg-amber-500/20 text-amber-400 border-amber-500/30' : 'bg-sky-500/20 text-sky-400 border-sky-500/30'">
                    {{ auth.role }}
                  </span>
                  <span v-if="auth.user.tenant_id && auth.user.tenant_id !== 'default'" class="text-[9px] text-gray-600 font-mono">{{ auth.user.tenant_id }}</span>
                </div>
              </div>

              <!-- Links -->
              <div class="py-1">
                <router-link v-if="auth.role === 'owner'" to="/keys" @click="closeUserMenu"
                  class="flex items-center gap-2 px-3 py-1.5 text-xs text-gray-400 hover:text-gray-200 hover:bg-white/5 transition-colors">
                  <svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                    <path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 11-7.778 7.778 5.5 5.5 0 017.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4" />
                  </svg>
                  API Keys
                </router-link>
                <button @click="auth.logout(); closeUserMenu()"
                  class="w-full flex items-center gap-2 px-3 py-1.5 text-xs text-gray-400 hover:text-red-400 hover:bg-white/5 transition-colors">
                  <svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                    <path d="M9 21H5a2 2 0 01-2-2V5a2 2 0 012-2h4M16 17l5-5-5-5M21 12H9" />
                  </svg>
                  Sign out
                </button>
              </div>
            </div>
          </div>

          <!-- Divider -->
          <span v-if="auth.authEnabled && auth.user" class="hidden md:block w-px h-4 bg-gray-700/50" />

          <!-- UTC Clock -->
          <span class="text-[10px] font-mono text-gray-500 tabular-nums">{{ utcTime }}</span>
        </div>
      </div>
    </header>

    <!-- SOS Active Banner (visible on all views) -->
    <div v-if="sosActive" class="bg-red-900/80 border-b border-red-600 px-4 py-2 flex items-center justify-center gap-3 animate-pulse">
      <svg class="w-5 h-5 text-red-400" viewBox="0 0 24 24" fill="currentColor"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-2 15l-5-5 1.41-1.41L10 14.17l7.59-7.59L19 8l-9 9z"/></svg>
      <span class="text-sm font-bold text-red-200 tracking-wider">SOS ACTIVE</span>
      <span class="text-xs text-red-300/70">Emergency beacon transmitting on all channels</span>
    </div>

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
