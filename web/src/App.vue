<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import { useSpectrumStore } from '@/stores/spectrum'
import JammingAlertModal from '@/components/JammingAlertModal.vue'
import NavBar from '@/components/NavBar.vue'
import StatusStrip from '@/components/StatusStrip.vue'
import MeshSatOSK from '@/components/MeshSatOSK.vue'
import { useShortcuts } from '@/composables/useShortcuts'

// Spectrum store is mounted at App level so the sticky jamming alert
// modal surfaces on any route, and so the SSE connection persists
// across route changes (rather than reconnecting every time the user
// navigates to the dashboard). [MESHSAT-509]
const spectrumStore = useSpectrumStore()
spectrumStore.connect()

// Engineer-only keyboard shortcuts (g c / g i / g m / g p / g r /
// n / / / Esc). No-op in Operator mode. [MESHSAT-558]
useShortcuts()

const store = useMeshsatStore()
const utcTime = ref('')

// Nav moved into NavBar component so it can branch on shellMode and
// own the mobile bottom-tab bar cleanly. [MESHSAT-550]
//
// Status indicators moved into StatusStrip component so every view
// sees the same persistent mesh/sat/cell/Hub/GPS/sync strip.
// [MESHSAT-554]

// SOS indicator (persistent across all views)
const sosActive = computed(() => store.sosStatus?.active === true)

// Apply the NVIS theme as a body class so the rules in style.css
// take effect. Watching the store so the toggle is reactive.
// [MESHSAT-556]
function applyTheme(mode) {
  const body = typeof document !== 'undefined' ? document.body : null
  if (!body) return
  if (mode === 'nvis') body.classList.add('theme-nvis')
  else body.classList.remove('theme-nvis')
}
watch(() => store.themeMode, applyTheme, { immediate: true })

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
  store.fetchAPRSStatus()
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
    store.fetchAPRSStatus()
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

        <!-- Center: Nav — 5 items + More in Operator, full list in Engineer -->
        <NavBar />

        <!-- Right: Status indicators -->
        <div class="flex items-center gap-3 shrink-0">

          <!-- Shell mode toggle: Operator (field kit) vs Engineer (admin) [MESHSAT-549] -->
          <button type="button" @click="store.toggleShellMode()"
            class="op-eng-toggle flex items-center h-8 rounded border border-tactical-border bg-tactical-surface overflow-hidden"
            :title="store.isOperator ? 'Switch to Engineer Mode' : 'Switch to Operator Mode'">
            <span class="px-3 py-1 text-xs font-medium tracking-wide transition-colors"
              :class="store.isOperator ? 'bg-tactical-iridium/20 text-tactical-iridium' : 'text-gray-500'">
              OP
            </span>
            <span class="px-3 py-1 text-xs font-medium tracking-wide transition-colors"
              :class="store.isEngineer ? 'bg-tactical-iridium/20 text-tactical-iridium' : 'text-gray-500'">
              ENG
            </span>
          </button>

          <!-- Divider -->
          <span class="hidden md:block w-px h-4 bg-gray-700/50" />

          <!-- Persistent status strip [MESHSAT-554] -->
          <StatusStrip />

          <!-- Divider -->
          <span class="hidden md:block w-px h-4 bg-gray-700/50" />

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

    <!-- Main content. Bottom-tab bar (NavBar.vue) shows when Operator
         mode + viewport ≤820 px wide OR ≤520 px tall — catches the
         Pi 7" touchscreen 800×480 that was falling through `md:` on
         width alone. Padding mirrors that rule. [fix: Pi 7" compat] -->
    <main class="flex-1" :class="{ 'pb-16': store.isOperator }">
      <div class="p-3 sm:p-4 lg:p-5">
        <router-view />
      </div>
    </main>

    <!-- Global sticky RF jamming alert — teleported to body, visible on any route -->
    <JammingAlertModal />

    <!-- In-SPA on-screen keyboard for kiosk Chromium [MESHSAT-582] -->
    <MeshSatOSK />
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
