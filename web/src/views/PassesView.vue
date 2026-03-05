<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const selectedLocationId = ref(null)
const locationMode = ref('auto') // 'auto', 'gps', 'iridium', or 'custom'
const windowHours = ref(24)
const cacheAgeSec = ref(-1)
const loadingPasses = ref(false)
const refreshing = ref(false)
const showOverlay = ref(true)

// Add location form
const showAddForm = ref(false)
const newLoc = ref({ name: '', lat: '', lon: '', alt_m: 0 })

// Chart hover state
const hoverInfo = ref(null)

// Collapsible pass list
const showPassList = ref(false)

const windowOptions = [12, 24, 48, 72]

// Location source options for dropdown
const locationModes = [
  { value: 'auto', label: 'AUTO', desc: 'GPS > Iridium > Custom' },
  { value: 'gps', label: 'GPS', desc: 'Meshtastic GPS' },
  { value: 'iridium', label: 'Iridium', desc: 'AT-MSGEO ~1-100km' },
  { value: 'custom', label: 'Custom', desc: 'User locations' }
]

const selectedLocation = computed(() =>
  store.locations.find(l => l.id === selectedLocationId.value) || null
)

// Resolved location based on current mode
const activeLocation = computed(() => {
  const sources = store.locationSources
  if (!sources) return selectedLocation

  if (locationMode.value === 'auto') {
    return sources.resolved ? {
      name: `AUTO (${sources.resolved.source.toUpperCase()})`,
      lat: sources.resolved.lat,
      lon: sources.resolved.lon,
      alt_m: (sources.resolved.alt_km || 0) * 1000,
      _source: sources.resolved.source,
      _accuracy: sources.resolved.accuracy_km
    } : selectedLocation
  }

  if (locationMode.value === 'gps') {
    const gps = (sources.sources || []).find(s => s.source === 'gps')
    if (gps && gps.lat !== 0) return {
      name: 'GPS',
      lat: gps.lat,
      lon: gps.lon,
      alt_m: (gps.alt_km || 0) * 1000,
      _source: 'gps',
      _accuracy: gps.accuracy_km
    }
    return null
  }

  if (locationMode.value === 'iridium') {
    const irid = (sources.sources || []).find(s => s.source === 'iridium')
    if (irid && irid.lat !== 0) return {
      name: 'Iridium',
      lat: irid.lat,
      lon: irid.lon,
      alt_m: (irid.alt_km || 0) * 1000,
      _source: 'iridium',
      _accuracy: irid.accuracy_km
    }
    return null
  }

  // custom mode — use the dropdown selection
  return selectedLocation
})

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
  const loc = activeLocation.value || selectedLocation
  if (!loc) return
  loadingPasses.value = true
  // Start pass prediction from the lookback time so passes overlap with signal history
  const windowSec = windowHours.value * 3600
  const startUnix = Math.floor(Date.now() / 1000) - Math.floor(windowSec * 0.5)
  const data = await store.fetchPasses({
    lat: loc.lat,
    lon: loc.lon,
    alt_m: loc.alt_m || 0,
    hours: windowHours.value,
    min_elev: 5,
    start: startUnix
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
  if (selectedLocationId.value === loc.id) {
    selectedLocationId.value = store.locations[0]?.id || null
    fetchPasses()
  }
}

// Chart dimensions
const chartWidth = 900
const chartHeight = 240
const plotTop = 15
const plotBottom = 210
const plotHeight = 195
const padL = 45
const padR = 45
const plotWidth = chartWidth - padL - padR

// Elevation Y-axis ticks
const elevTicks = [0, 15, 30, 45, 60, 75, 90]

const chartData = computed(() => {
  const now = Date.now() / 1000
  const windowSec = windowHours.value * 3600
  const startTs = now - windowSec * 0.5  // 50% past
  const endTs = now + windowSec * 0.5    // 50% future

  function xPos(ts) {
    return padL + ((ts - startTs) / (endTs - startTs)) * plotWidth
  }

  function signalY(val) {
    return plotBottom - (val / 5) * plotHeight
  }

  function elevY(deg) {
    return plotBottom - (deg / 90) * plotHeight
  }

  // Pass triangles: each pass becomes a triangle where height = peak elevation
  const passes = (store.passes || []).map(p => {
    const x1 = Math.max(padL, xPos(p.aos))
    const x2 = Math.min(chartWidth - padR, xPos(p.los))
    const xMid = (x1 + x2) / 2
    const peakY = elevY(p.peak_elev_deg)
    const baseline = plotBottom
    return {
      path: `M ${x1},${baseline} L ${xMid},${peakY} L ${x2},${baseline} Z`,
      x1, x2, xMid, peakY, baseline,
      elev: p.peak_elev_deg,
      sat: p.satellite,
      active: p.is_active,
      aos: p.aos,
      los: p.los,
      time: formatTime(p.aos)
    }
  }).filter(p => p.x2 > padL && p.x1 < chartWidth - padR)

  // Signal points
  const signals = (store.signalHistory || []).map(s => {
    const val = s.value || s.avg || 0
    return {
      x: xPos(s.timestamp || s.bucket),
      val,
      y: signalY(val),
      ts: s.timestamp || s.bucket
    }
  }).filter(s => s.x >= padL && s.x <= chartWidth - padR)

  // Signal area path (closed polygon under the signal line)
  let signalAreaPath = ''
  let signalLinePts = ''
  if (signals.length > 1) {
    const pts = signals.map(s => `${s.x},${s.y}`)
    signalLinePts = pts.join(' ')
    signalAreaPath = `M ${signals[0].x},${plotBottom} L ${pts.join(' L ')} L ${signals[signals.length - 1].x},${plotBottom} Z`
  }

  // Time axis labels (every 3h or 6h depending on window)
  const step = windowHours.value <= 24 ? 3 * 3600 : 6 * 3600
  const labels = []
  let t = Math.ceil(startTs / step) * step
  while (t < endTs) {
    labels.push({ x: xPos(t), label: formatTime(t) })
    t += step
  }

  // Now line
  const nowX = xPos(now)

  // Signal Y-axis ticks (0-5 bars)
  const signalTicks = [0, 1, 2, 3, 4, 5].map(v => ({
    val: v,
    y: signalY(v)
  }))

  // Elevation Y-axis ticks
  const elevTickData = elevTicks.map(d => ({
    deg: d,
    y: elevY(d)
  }))

  // Grid lines (at signal bar positions)
  const gridLines = signalTicks.map(t => t.y)

  return { passes, signals, signalAreaPath, signalLinePts, labels, nowX, startTs, endTs, signalTicks, elevTickData, gridLines, xPos }
})

function onChartHover(event) {
  const svg = event.currentTarget
  const rect = svg.getBoundingClientRect()
  const mouseX = ((event.clientX - rect.left) / rect.width) * chartWidth

  if (mouseX < padL || mouseX > chartWidth - padR) {
    hoverInfo.value = null
    return
  }

  const data = chartData.value
  // Convert mouseX to timestamp
  const frac = (mouseX - padL) / plotWidth
  const ts = data.startTs + frac * (data.endTs - data.startTs)

  // Find active pass at this timestamp
  let passLabel = null
  let passElev = null
  for (const p of data.passes) {
    if (ts >= p.aos && ts <= p.los) {
      passLabel = p.sat
      passElev = p.elev
      break
    }
  }

  // Find nearest signal point
  let signalVal = null
  if (data.signals.length > 0) {
    let closest = data.signals[0]
    let minDist = Math.abs(data.signals[0].x - mouseX)
    for (const s of data.signals) {
      const d = Math.abs(s.x - mouseX)
      if (d < minDist) {
        minDist = d
        closest = s
      }
    }
    if (minDist < 30) {
      signalVal = closest.val
    }
  }

  hoverInfo.value = {
    x: mouseX,
    time: formatTime(ts),
    passLabel,
    passElev,
    signalVal
  }
}

function clearHover() {
  hoverInfo.value = null
}

async function fetchSignalHistory() {
  const now = Math.floor(Date.now() / 1000)
  const windowSec = windowHours.value * 3600
  const from = now - Math.floor(windowSec * 0.5)
  const to = now + Math.floor(windowSec * 0.5)
  await store.fetchSignalHistory({ source: 'iridium', from, to, mode: 'raw', limit: 2000 })
}

watch(windowHours, fetchSignalHistory)
watch(locationMode, () => { store.fetchLocationSources().then(fetchPasses) })

onMounted(async () => {
  await Promise.all([
    store.fetchLocations(),
    store.fetchLocationSources()
  ])
  if (store.locations.length > 0) {
    selectedLocationId.value = store.locations[0].id
  }
  fetchPasses()
  fetchSignalHistory()
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
      <!-- Location source mode -->
      <div class="flex items-center gap-2">
        <label class="text-xs text-gray-500">Source</label>
        <select v-model="locationMode"
          class="px-3 py-1.5 rounded bg-gray-800 border border-gray-700 text-sm text-gray-200">
          <option v-for="m in locationModes" :key="m.value" :value="m.value">{{ m.label }}</option>
        </select>
      </div>

      <!-- Custom location selector (only shown in custom mode) -->
      <div v-if="locationMode === 'custom'" class="flex items-center gap-2">
        <select v-model="selectedLocationId" @change="fetchPasses"
          class="px-3 py-1.5 rounded bg-gray-800 border border-gray-700 text-sm text-gray-200">
          <option v-for="loc in store.locations" :key="loc.id" :value="loc.id">{{ loc.name }}</option>
        </select>
        <button @click="showAddForm = !showAddForm"
          class="px-2 py-1 rounded bg-gray-800 border border-gray-700 text-xs text-gray-400 hover:text-teal-400">
          {{ showAddForm ? 'Cancel' : '+ Add' }}
        </button>
      </div>

      <!-- Active location indicator (for auto/gps/iridium modes) -->
      <div v-if="locationMode !== 'custom' && activeLocation" class="flex items-center gap-1.5 text-[11px]">
        <span class="w-2 h-2 rounded-full"
          :class="activeLocation._source === 'gps' ? 'bg-emerald-400' : activeLocation._source === 'iridium' ? 'bg-teal-400' : 'bg-amber-400'" />
        <span class="text-gray-400">{{ activeLocation.lat?.toFixed(4) }}, {{ activeLocation.lon?.toFixed(4) }}</span>
        <span v-if="activeLocation._accuracy" class="text-gray-600">(~{{ activeLocation._accuracy < 1 ? (activeLocation._accuracy * 1000).toFixed(0) + 'm' : activeLocation._accuracy.toFixed(0) + 'km' }})</span>
      </div>
      <div v-else-if="locationMode !== 'custom' && !activeLocation" class="text-[11px] text-red-400">
        No {{ locationMode }} fix available
      </div>

      <!-- Time window -->
      <div class="flex items-center gap-1 ml-auto">
        <button v-for="h in windowOptions" :key="h" @click="windowHours = h; fetchPasses()"
          class="px-2.5 py-1 rounded text-xs font-medium transition-colors"
          :class="windowHours === h ? 'bg-tactical-iridium/20 text-tactical-iridium' : 'bg-gray-800 text-gray-500 hover:text-gray-300'">
          {{ h }}h
        </button>
      </div>
    </div>

    <!-- Add location form (custom mode) -->
    <div v-if="showAddForm && locationMode === 'custom'" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-4">
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

    <!-- Location management (custom mode) -->
    <div v-if="locationMode === 'custom' && store.locations.length > 0" class="flex flex-wrap gap-2 mb-4">
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

    <!-- Signal vs Passes overlay chart -->
    <div v-if="showOverlay && (sortedPasses.length > 0 || store.signalHistory.length > 0)"
      class="bg-tactical-surface rounded-lg border border-tactical-border p-3 mb-4">
      <div class="flex items-center justify-between mb-2">
        <span class="text-[10px] text-gray-500 uppercase tracking-wider">Signal vs Passes</span>
        <div class="flex items-center gap-3 text-[10px] text-gray-500">
          <span class="flex items-center gap-1">
            <svg width="12" height="8" class="inline-block"><polygon points="0,8 6,1 12,8" fill="rgba(129,140,248,0.3)" stroke="rgba(129,140,248,0.5)" stroke-width="0.5"/></svg>
            Pass
          </span>
          <span class="flex items-center gap-1"><span class="w-2 h-2 rounded-full bg-emerald-400 inline-block"></span> Signal</span>
        </div>
      </div>
      <svg :viewBox="`0 0 ${chartWidth} ${chartHeight}`" class="w-full h-auto" preserveAspectRatio="xMidYMid meet"
        @mousemove="onChartHover" @mouseleave="clearHover" style="cursor: crosshair;">
        <defs>
          <!-- Pass triangle gradient (indigo/blue) -->
          <linearGradient id="passGrad" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="rgb(129,140,248)" stop-opacity="0.30" />
            <stop offset="100%" stop-color="rgb(129,140,248)" stop-opacity="0.03" />
          </linearGradient>
          <!-- Active pass gradient (brighter indigo) -->
          <linearGradient id="passGradActive" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="rgb(165,180,252)" stop-opacity="0.50" />
            <stop offset="100%" stop-color="rgb(165,180,252)" stop-opacity="0.08" />
          </linearGradient>
          <!-- Signal area gradient -->
          <linearGradient id="signalAreaGrad" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="#10b981" stop-opacity="0.15" />
            <stop offset="100%" stop-color="#10b981" stop-opacity="0.02" />
          </linearGradient>
          <!-- Clip to plot area -->
          <clipPath id="plotClip">
            <rect :x="padL" :y="plotTop" :width="plotWidth" :height="plotBottom - plotTop" />
          </clipPath>
        </defs>

        <!-- Horizontal grid lines -->
        <line v-for="y in chartData.gridLines" :key="'grid'+y"
          :x1="padL" :x2="chartWidth - padR" :y1="y" :y2="y"
          stroke="#374151" stroke-width="0.5" stroke-dasharray="2 3" />

        <!-- Left Y-axis labels (signal bars 0-5) -->
        <text v-for="t in chartData.signalTicks" :key="'sl'+t.val"
          :x="padL - 6" :y="t.y" text-anchor="end" fill="#6b7280" font-size="8" dominant-baseline="middle">
          {{ t.val }}
        </text>
        <text :x="padL - 6" :y="plotTop - 5" text-anchor="end" fill="#6b7280" font-size="7">bars</text>

        <!-- Right Y-axis labels (elevation degrees) -->
        <text v-for="t in chartData.elevTickData" :key="'el'+t.deg"
          :x="chartWidth - padR + 6" :y="t.y" text-anchor="start" fill="#818cf8" font-size="8" dominant-baseline="middle" opacity="0.5">
          {{ t.deg }}
        </text>
        <text :x="chartWidth - padR + 6" :y="plotTop - 5" text-anchor="start" fill="#818cf8" font-size="7" opacity="0.5">deg</text>

        <!-- Plot area (clipped) -->
        <g clip-path="url(#plotClip)">
          <!-- Pass triangles (background layer) -->
          <path v-for="(p, idx) in chartData.passes" :key="'pt'+idx"
            :d="p.path" :fill="p.active ? 'url(#passGradActive)' : 'url(#passGrad)'"
            :stroke="p.active ? 'rgba(165,180,252,0.5)' : 'rgba(129,140,248,0.2)'" stroke-width="1" />

          <!-- Pass peak labels (for wider triangles) -->
          <text v-for="(p, idx) in chartData.passes.filter(pp => (pp.x2 - pp.x1) > 20)" :key="'plbl'+idx"
            :x="p.xMid" :y="p.peakY - 4" text-anchor="middle" fill="#a5b4fc" font-size="7" opacity="0.6">
            {{ p.elev.toFixed(0) }}
          </text>

          <!-- Signal area fill -->
          <path v-if="chartData.signalAreaPath" :d="chartData.signalAreaPath" fill="url(#signalAreaGrad)" />

          <!-- Signal line -->
          <polyline v-if="chartData.signalLinePts"
            :points="chartData.signalLinePts"
            fill="none" stroke="#10b981" stroke-width="1.5" opacity="0.7" />

          <!-- Signal dots -->
          <circle v-for="(s, idx) in chartData.signals" :key="'sd'+idx"
            :cx="s.x" :cy="s.y" r="2.5"
            :fill="s.val >= 3 ? '#10b981' : s.val >= 1 ? '#f59e0b' : '#ef4444'"
            opacity="0.85" />

          <!-- Now line -->
          <line :x1="chartData.nowX" :x2="chartData.nowX" :y1="plotTop" :y2="plotBottom"
            stroke="#f59e0b" stroke-width="1" stroke-dasharray="3 2" opacity="0.6" />
        </g>

        <!-- Now label (outside clip) -->
        <text :x="chartData.nowX" :y="plotBottom + 18" text-anchor="middle" fill="#f59e0b" font-size="7">now</text>

        <!-- X axis time labels -->
        <text v-for="(l, idx) in chartData.labels" :key="'tl'+idx"
          :x="l.x" :y="plotBottom + 18" text-anchor="middle" fill="#6b7280" font-size="7">
          {{ l.label }}
        </text>

        <!-- Hover crosshair + tooltip -->
        <g v-if="hoverInfo">
          <!-- Vertical crosshair -->
          <line :x1="hoverInfo.x" :x2="hoverInfo.x" :y1="plotTop" :y2="plotBottom"
            stroke="#9ca3af" stroke-width="0.5" stroke-dasharray="2 2" opacity="0.5" />

          <!-- Tooltip background -->
          <rect :x="hoverInfo.x + (hoverInfo.x > chartWidth / 2 ? -120 : 8)" :y="plotTop + 2"
            width="112" :height="hoverInfo.passLabel ? 42 : 28" rx="3"
            fill="#1f2937" stroke="#374151" stroke-width="0.5" opacity="0.95" />

          <!-- Tooltip text -->
          <text :x="hoverInfo.x + (hoverInfo.x > chartWidth / 2 ? -114 : 14)" :y="plotTop + 14"
            fill="#d1d5db" font-size="8">
            {{ hoverInfo.time }} UTC
          </text>
          <text v-if="hoverInfo.passLabel"
            :x="hoverInfo.x + (hoverInfo.x > chartWidth / 2 ? -114 : 14)" :y="plotTop + 25"
            fill="#a5b4fc" font-size="8">
            {{ hoverInfo.passLabel }} {{ hoverInfo.passElev?.toFixed(0) }}deg
          </text>
          <text v-if="hoverInfo.signalVal !== null"
            :x="hoverInfo.x + (hoverInfo.x > chartWidth / 2 ? -114 : 14)"
            :y="plotTop + (hoverInfo.passLabel ? 36 : 25)"
            :fill="hoverInfo.signalVal >= 3 ? '#10b981' : hoverInfo.signalVal >= 1 ? '#f59e0b' : '#ef4444'" font-size="8">
            Signal: {{ hoverInfo.signalVal }} bars
          </text>
        </g>
      </svg>
    </div>

    <!-- Collapsible pass list -->
    <div class="mb-4">
      <button v-if="!loadingPasses && sortedPasses.length > 0"
        @click="showPassList = !showPassList"
        class="flex items-center gap-2 w-full px-3 py-2 rounded-lg bg-gray-800/50 hover:bg-gray-800 border border-gray-700/50 transition-colors text-left">
        <svg class="w-3 h-3 text-gray-500 transition-transform" :class="showPassList ? 'rotate-90' : ''"
          viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="2">
          <path d="M4 2 L8 6 L4 10" />
        </svg>
        <span class="text-[11px] text-gray-400">
          {{ sortedPasses.length }} passes
          <span v-if="nextPass" class="text-gray-500 ml-2">Next: {{ nextPass.satellite }} at {{ formatTime(nextPass.aos) }}</span>
        </span>
      </button>

      <div v-if="loadingPasses" class="text-center text-gray-500 py-8 text-sm">Calculating passes...</div>
      <div v-else-if="sortedPasses.length === 0" class="text-center text-gray-500 py-8 text-sm bg-gray-800/50 rounded-lg border border-gray-700">
        No passes found for the selected location and time window.
      </div>
      <div v-else-if="showPassList" class="space-y-1 mt-2">
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
  </div>
</template>
