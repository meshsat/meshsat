<script setup>
import { ref, computed, onMounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const selectedLocation = ref(null)
const windowHours = ref(24)
const cacheAgeSec = ref(-1)
const loadingPasses = ref(false)
const refreshing = ref(false)

// Add location form
const showAddForm = ref(false)
const newLoc = ref({ name: '', lat: '', lon: '', alt_m: 0 })

const windowOptions = [12, 24, 48, 72]

const sortedPasses = computed(() => {
  const now = Date.now() / 1000
  return (store.passes || []).map(p => ({
    ...p,
    isNext: !p.is_active && p.aos > now,
    isPast: p.los < now
  }))
})

const nextPass = computed(() => {
  const now = Date.now() / 1000
  return sortedPasses.value.find(p => p.aos > now)
})

function formatTime(unix) {
  if (!unix) return ''
  return new Date(unix * 1000).toISOString().slice(11, 16)
}

function formatDate(unix) {
  if (!unix) return ''
  const d = new Date(unix * 1000)
  return d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short' })
}

function formatDuration(min) {
  if (!min) return ''
  return `${Math.round(min)}m`
}

function formatCacheAge(sec) {
  if (sec < 0) return 'No data'
  if (sec < 3600) return `${Math.round(sec / 60)}m ago`
  if (sec < 86400) return `${Math.round(sec / 3600)}h ago`
  return `${Math.round(sec / 86400)}d ago`
}

function elevColor(elev) {
  if (elev >= 60) return 'bg-tactical-iridium'
  if (elev >= 30) return 'bg-emerald-400'
  if (elev >= 15) return 'bg-amber-400'
  return 'bg-gray-500'
}

async function fetchPasses() {
  if (!selectedLocation.value) return
  loadingPasses.value = true
  const loc = selectedLocation.value
  const data = await store.fetchPasses({
    lat: loc.lat,
    lon: loc.lon,
    alt_m: loc.alt_m || 0,
    hours: windowHours.value,
    min_elev: 5
  })
  if (data?.cache_age_sec !== undefined) {
    cacheAgeSec.value = data.cache_age_sec
  }
  loadingPasses.value = false
}

async function doRefreshTLEs() {
  refreshing.value = true
  try {
    await store.refreshTLEs()
    await fetchPasses()
  } catch { /* store error */ }
  refreshing.value = false
}

async function addLocation() {
  const lat = parseFloat(newLoc.value.lat)
  const lon = parseFloat(newLoc.value.lon)
  if (!newLoc.value.name || isNaN(lat) || isNaN(lon)) return
  await store.createLocation({ name: newLoc.value.name, lat, lon, alt_m: newLoc.value.alt_m || 0 })
  newLoc.value = { name: '', lat: '', lon: '', alt_m: 0 }
  showAddForm.value = false
}

async function removeLocation(loc) {
  if (loc.builtin) return
  if (!confirm(`Delete "${loc.name}"?`)) return
  await store.deleteLocation(loc.id)
  if (selectedLocation.value?.id === loc.id) {
    selectedLocation.value = store.locations[0] || null
    fetchPasses()
  }
}

onMounted(async () => {
  await store.fetchLocations()
  if (store.locations.length > 0) {
    selectedLocation.value = store.locations[0]
    fetchPasses()
  }
})
</script>

<template>
  <div class="max-w-4xl mx-auto">
    <div class="flex items-center justify-between mb-4">
      <h2 class="text-lg font-semibold text-gray-200">Pass Predictor</h2>
      <div class="flex items-center gap-2 text-[10px] text-gray-500">
        <span>TLE: {{ formatCacheAge(cacheAgeSec) }}</span>
        <button @click="doRefreshTLEs" :disabled="refreshing"
          class="px-2 py-1 rounded bg-gray-800 border border-gray-700 text-gray-400 hover:text-tactical-iridium hover:border-tactical-iridium/30 transition-colors">
          {{ refreshing ? 'Refreshing...' : 'Refresh TLEs' }}
        </button>
      </div>
    </div>

    <!-- Controls -->
    <div class="flex flex-wrap items-center gap-3 mb-4">
      <!-- Location selector -->
      <div class="flex items-center gap-2">
        <label class="text-xs text-gray-500">Location</label>
        <select v-model="selectedLocation" @change="fetchPasses"
          class="px-3 py-1.5 rounded bg-gray-800 border border-gray-700 text-sm text-gray-200">
          <option v-for="loc in store.locations" :key="loc.id" :value="loc">{{ loc.name }}</option>
        </select>
        <button @click="showAddForm = !showAddForm"
          class="px-2 py-1 rounded bg-gray-800 border border-gray-700 text-xs text-gray-400 hover:text-teal-400">
          {{ showAddForm ? 'Cancel' : '+ Add' }}
        </button>
      </div>

      <!-- Time window -->
      <div class="flex items-center gap-1">
        <button v-for="h in windowOptions" :key="h" @click="windowHours = h; fetchPasses()"
          class="px-2.5 py-1 rounded text-xs font-medium transition-colors"
          :class="windowHours === h ? 'bg-tactical-iridium/20 text-tactical-iridium' : 'bg-gray-800 text-gray-500 hover:text-gray-300'">
          {{ h }}h
        </button>
      </div>
    </div>

    <!-- Add location form -->
    <div v-if="showAddForm" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-4">
      <div class="grid grid-cols-4 gap-3">
        <div>
          <label class="block text-xs text-gray-500 mb-1">Name</label>
          <input v-model="newLoc.name" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="My Location">
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Latitude</label>
          <input v-model="newLoc.lat" type="number" step="any" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="52.16">
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Longitude</label>
          <input v-model="newLoc.lon" type="number" step="any" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="4.49">
        </div>
        <div class="flex items-end">
          <button @click="addLocation" class="px-4 py-1.5 rounded bg-teal-600 text-white text-sm hover:bg-teal-500 w-full">Add</button>
        </div>
      </div>
    </div>

    <!-- Location management -->
    <div v-if="store.locations.length > 0" class="flex flex-wrap gap-2 mb-4">
      <div v-for="loc in store.locations" :key="loc.id"
        class="flex items-center gap-1.5 px-2 py-1 rounded text-[11px] bg-gray-800/50 border border-gray-700/50">
        <span class="text-gray-300">{{ loc.name }}</span>
        <span class="text-gray-600 font-mono">{{ loc.lat.toFixed(2) }}, {{ loc.lon.toFixed(2) }}</span>
        <button v-if="!loc.builtin" @click="removeLocation(loc)" class="text-gray-600 hover:text-red-400 ml-1">x</button>
      </div>
    </div>

    <!-- Next pass highlight -->
    <div v-if="nextPass" class="bg-tactical-iridium/5 border border-tactical-iridium/20 rounded-lg p-4 mb-4">
      <div class="flex items-center justify-between">
        <div>
          <span class="text-[10px] text-tactical-iridium/60 uppercase">Next Pass</span>
          <div class="text-sm text-tactical-iridium font-medium mt-0.5">{{ nextPass.satellite }}</div>
        </div>
        <div class="text-right">
          <div class="text-lg font-mono font-bold text-tactical-iridium">{{ formatTime(nextPass.aos) }}</div>
          <div class="text-[10px] text-gray-500">{{ formatDate(nextPass.aos) }} UTC</div>
        </div>
      </div>
      <div class="flex items-center gap-4 mt-2 text-[11px] text-gray-400">
        <span>Duration: {{ formatDuration(nextPass.duration_min) }}</span>
        <span>Peak: {{ nextPass.peak_elev_deg.toFixed(0) }}deg</span>
        <span>Az: {{ nextPass.peak_azimuth.toFixed(0) }}deg</span>
      </div>
    </div>

    <!-- Pass list -->
    <div v-if="loadingPasses" class="text-center text-gray-500 py-8 text-sm">Calculating passes...</div>
    <div v-else-if="sortedPasses.length === 0" class="text-center text-gray-500 py-8 text-sm bg-gray-800/50 rounded-lg border border-gray-700">
      No passes found for the selected location and time window.
    </div>
    <div v-else class="space-y-1">
      <div v-for="pass in sortedPasses" :key="`${pass.satellite}-${pass.aos}`"
        class="flex items-center gap-3 px-3 py-2 rounded-lg transition-colors"
        :class="pass.is_active ? 'bg-tactical-iridium/10 border border-tactical-iridium/20' : pass.isPast ? 'bg-gray-800/30 opacity-50' : 'bg-gray-800/50 hover:bg-gray-800'">

        <!-- Active indicator -->
        <span v-if="pass.is_active" class="w-2 h-2 rounded-full bg-tactical-iridium animate-pulse shrink-0" />
        <span v-else class="w-2 h-2 rounded-full bg-gray-700 shrink-0" />

        <!-- Satellite name -->
        <span class="text-[11px] text-gray-300 w-32 truncate shrink-0">{{ pass.satellite }}</span>

        <!-- Time -->
        <span class="text-[11px] font-mono text-gray-400 w-20 shrink-0">
          {{ formatTime(pass.aos) }}-{{ formatTime(pass.los) }}
        </span>

        <!-- Date -->
        <span class="text-[10px] text-gray-600 w-14 shrink-0">{{ formatDate(pass.aos) }}</span>

        <!-- Duration -->
        <span class="text-[10px] font-mono text-gray-500 w-8 shrink-0">{{ formatDuration(pass.duration_min) }}</span>

        <!-- Elevation bar -->
        <div class="flex-1 flex items-center gap-2">
          <div class="flex-1 h-1.5 rounded-full bg-gray-800 overflow-hidden">
            <div class="h-full rounded-full transition-all" :class="elevColor(pass.peak_elev_deg)"
              :style="{ width: `${Math.min(100, pass.peak_elev_deg / 90 * 100)}%` }" />
          </div>
          <span class="text-[10px] font-mono text-gray-500 w-10 text-right">{{ pass.peak_elev_deg.toFixed(0) }}deg</span>
        </div>
      </div>
    </div>
  </div>
</template>
