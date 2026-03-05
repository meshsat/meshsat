<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { useMeshsatStore } from '@/stores/meshsat'

const route = useRoute()
const store = useMeshsatStore()
const utcTime = ref('')

const tabs = [
  { name: 'dashboard', label: 'Dashboard', path: '/' },
  { name: 'comms', label: 'Comms', path: '/messages' },
  { name: 'nodes', label: 'Nodes', path: '/nodes' },
  { name: 'map', label: 'Map', path: '/map' },
  { name: 'bridge', label: 'Bridge', path: '/bridge' },
  { name: 'passes', label: 'Passes', path: '/passes' },
  { name: 'settings', label: 'Settings', path: '/settings' }
]

function isActive(tab) {
  if (tab.path === '/') return route.path === '/'
  return route.path.startsWith(tab.path)
}

// Bridge status from gateways
const bridgeActive = computed(() => {
  return (store.gateways || []).some(g => g.connected)
})

const deviceId = computed(() => {
  const id = store.status?.node_id
  if (!id) return '----'
  const hex = id.toString(16).toUpperCase()
  return '!' + hex.slice(-8)
})

// Mesh + Sat status for header
const meshConnected = computed(() => store.status?.connected ?? false)
const satBars = computed(() => store.iridiumSignal?.bars ?? -1)
const cellBars = computed(() => store.cellularSignal?.bars ?? -1)

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
  store.fetchGateways()
  store.fetchIridiumSignalFast()
  store.fetchCellularSignal()
  pollTimer = setInterval(() => {
    store.fetchStatus()
    store.fetchGateways()
    store.fetchIridiumSignalFast()
    store.fetchCellularSignal()
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
      <img src="/logo-bg.png" alt="" class="w-[60vmin] h-[60vmin] object-contain opacity-[0.04]" />
    </div>

    <!-- Sticky horizontal header -->
    <header class="sticky top-0 z-50 bg-tactical-surface/95 backdrop-blur border-b border-tactical-border">
      <div class="flex items-center h-12 px-3 lg:px-5 gap-3">
        <!-- Left: Brand text -->
        <router-link to="/" class="flex items-center shrink-0">
          <span class="font-display font-semibold text-sm text-gray-200 tracking-wide">MESHSAT</span>
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

        <!-- Right: Status + clock -->
        <div class="flex items-center gap-3 shrink-0">
          <!-- Mesh indicator -->
          <div class="hidden md:flex items-center gap-1.5">
            <span class="w-1.5 h-1.5 rounded-full"
              :class="meshConnected ? 'bg-emerald-400 animate-pulse-dot' : 'bg-red-400'" />
            <span class="text-[10px] text-gray-500">MESH</span>
          </div>

          <!-- Sat signal bars -->
          <div class="hidden md:flex items-end gap-px h-3">
            <span v-for="i in 5" :key="i" class="w-[3px] rounded-[1px]"
              :class="satBars >= i ? (satBars <= 2 ? 'bg-amber-400' : 'bg-tactical-iridium') : 'bg-gray-700/50'"
              :style="{ height: `${3 + i * 2}px` }" />
          </div>

          <!-- Cellular signal bars -->
          <div v-if="cellBars >= 0" class="hidden md:flex items-center gap-1">
            <div class="flex items-end gap-px h-3">
              <span v-for="i in 5" :key="'cell'+i" class="w-[3px] rounded-[1px]"
                :class="cellBars >= i ? 'bg-sky-400' : 'bg-gray-700/50'"
                :style="{ height: `${3 + i * 2}px` }" />
            </div>
            <span class="text-[9px] text-sky-400/60 font-mono">4G</span>
          </div>

          <!-- Bridge status badge -->
          <div v-if="bridgeActive"
            class="hidden sm:flex items-center gap-1.5 px-2 py-0.5 rounded bg-tactical-iridium/10 border border-tactical-iridium/20">
            <span class="w-1.5 h-1.5 rounded-full bg-tactical-iridium animate-pulse-dot" />
            <span class="text-[10px] font-medium text-tactical-iridium">BRIDGE</span>
          </div>

          <!-- Device ID -->
          <span class="text-[10px] font-mono text-gray-600 hidden lg:block">{{ deviceId }}</span>

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
