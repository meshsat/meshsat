<script setup>
import { ref, reactive, onMounted, onUnmounted, watch, computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const mapEl = ref(null)
const mapReady = ref(false)
const mapError = ref(false)
const showMessages = ref(true)
const showTracks = ref(true)

// Per-node visibility toggles (reactive map: nodeId → boolean)
const nodeVisibility = reactive({})

let L = null
let map = null
let markerLayer = null
let trackLayer = null
let messageLayer = null
let locationLayer = null
let initialBoundsFit = false

// Distinct colors for nodes
const nodeColors = ['#06b6d4', '#8b5cf6', '#f97316', '#ec4899', '#10b981', '#f59e0b', '#3b82f6', '#ef4444']

function signalColor(q) {
  if (!q) return '#6b7280'
  const u = q.toUpperCase()
  if (u === 'GOOD') return '#10b981'
  if (u === 'FAIR') return '#f59e0b'
  return '#ef4444'
}

// Build a lookup: nodeId → {lat, lon, name, color}
const nodePositionMap = computed(() => {
  const m = {}
  for (const node of store.nodes) {
    if (node.latitude && node.longitude && node.latitude !== 0 && node.longitude !== 0) {
      m[node.id] = {
        lat: node.latitude,
        lon: node.longitude,
        name: node.name || node.long_name || node.id,
        signal_quality: node.signal_quality
      }
    }
  }
  return m
})

// Unique nodes with positions (for checkboxes)
const nodesWithPositions = computed(() => {
  return store.nodes.filter(n =>
    n.latitude && n.longitude && n.latitude !== 0 && n.longitude !== 0
  )
})

// Messages that have a locatable sender
const locatableMessages = computed(() => {
  const posMap = nodePositionMap.value
  return store.messages.filter(msg => {
    const nodeId = msg.from_node
    return nodeId && posMap[nodeId]
  })
})

async function initMap() {
  try {
    const leaflet = await import('leaflet')
    await import('leaflet/dist/leaflet.css')
    L = leaflet.default || leaflet

    if (!mapEl.value) return

    map = L.map(mapEl.value).setView([0, 0], 2)
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
      attribution: '&copy; OSM',
      maxZoom: 19
    }).addTo(map)

    markerLayer = L.layerGroup().addTo(map)
    trackLayer = L.layerGroup().addTo(map)
    messageLayer = L.layerGroup().addTo(map)
    locationLayer = L.layerGroup().addTo(map)
    mapReady.value = true
    updateMap()
  } catch {
    mapError.value = true
  }
}

function updateMap() {
  if (!map || !L || !markerLayer) return
  markerLayer.clearLayers()
  trackLayer.clearLayers()
  if (messageLayer) messageLayer.clearLayers()

  const visibleNodes = nodesWithPositions.value.filter(n => nodeVisibility[n.id] !== false)

  // Node markers
  for (const node of visibleNodes) {
    const color = signalColor(node.signal_quality)
    const m = L.circleMarker([node.latitude, node.longitude], {
      radius: 8, fillColor: color, fillOpacity: 0.8, color, weight: 2
    })
    let html = `<strong>${node.name ?? node.long_name ?? node.id}</strong>`
    if (node.battery != null) html += `<br>Battery: ${Math.round(node.battery)}%`
    if (node.snr != null) html += `<br>SNR: ${Number(node.snr).toFixed(1)} dB`
    m.bindPopup(html)
    markerLayer.addLayer(m)
  }

  // Track lines
  if (showTracks.value) {
    const visibleIds = new Set(visibleNodes.map(n => String(n.id)))
    const groups = {}
    for (const p of store.positions) {
      const nid = String(p.node_id ?? p.from)
      if (!nid || !p.latitude || !p.longitude) continue
      if (!visibleIds.has(nid)) continue
      if (!groups[nid]) groups[nid] = []
      groups[nid].push([Number(p.latitude), Number(p.longitude)])
    }
    let ci = 0
    for (const nid in groups) {
      if (groups[nid].length < 2) continue
      L.polyline(groups[nid], { color: nodeColors[ci % nodeColors.length], weight: 2, opacity: 0.6, dashArray: '4 4' }).addTo(trackLayer)
      ci++
    }
  }

  // Message markers
  if (showMessages.value && messageLayer) {
    const posMap = nodePositionMap.value
    const visibleIds = new Set(visibleNodes.map(n => String(n.id)))

    for (const msg of store.messages) {
      const nid = String(msg.from_node)
      if (!visibleIds.has(nid)) continue
      const pos = posMap[nid]
      if (!pos) continue

      // Small offset for multiple messages at same position
      const numId = typeof msg.id === 'number' ? msg.id : parseInt(msg.id, 10) || 0
      const jitter = (numId % 100) * 0.00005
      const lat = pos.lat + jitter
      const lon = pos.lon + jitter

      const isText = msg.portnum === 1
      const color = isText ? '#2dd4bf' : '#94a3b8'
      const m = L.circleMarker([lat, lon], {
        radius: 4, fillColor: color, fillOpacity: 0.9, color: '#1e293b', weight: 1
      })

      const time = msg.created_at ? new Date(msg.created_at).toLocaleString() : ''
      let popup = `<div style="max-width:200px"><strong>${pos.name}</strong><br>`
      popup += `<span style="font-size:11px;color:#888">${time}</span><br>`
      if (msg.decoded_text) {
        popup += `<span style="font-family:monospace;font-size:12px">${msg.decoded_text.slice(0, 120)}</span>`
      } else {
        popup += `<em style="color:#888">${msg.portnum_name || 'portnum ' + msg.portnum}</em>`
      }
      popup += '</div>'
      m.bindPopup(popup)
      messageLayer.addLayer(m)
    }
  }

  // Location source markers (GPS, Iridium, Custom)
  if (locationLayer) {
    locationLayer.clearLayers()
    const ls = store.locationSources
    if (ls && ls.sources) {
      for (const src of ls.sources) {
        if (!src.lat || !src.lon) continue
        const isGps = src.source === 'gps'
        const isIridium = src.source === 'iridium'
        const color = isGps ? '#10b981' : isIridium ? '#14b8a6' : '#f59e0b'
        const label = isGps ? 'GPS' : isIridium ? 'Iridium' : 'Custom'
        const accTxt = src.accuracy_km != null
          ? (src.accuracy_km < 1 ? `${(src.accuracy_km * 1000).toFixed(0)}m` : `${src.accuracy_km.toFixed(0)}km`)
          : ''

        const m = L.circleMarker([src.lat, src.lon], {
          radius: isGps ? 7 : 10,
          fillColor: color,
          fillOpacity: 0.25,
          color,
          weight: 2,
          dashArray: isIridium ? '4 3' : null
        })
        let popup = `<strong>${label} Position</strong><br>`
        popup += `<span style="font-family:monospace;font-size:11px">${src.lat.toFixed(5)}, ${src.lon.toFixed(5)}</span>`
        if (accTxt) popup += `<br>Accuracy: ~${accTxt}`
        m.bindPopup(popup)
        locationLayer.addLayer(m)

        // For Iridium, draw accuracy circle
        if (isIridium && src.accuracy_km > 0) {
          const circle = L.circle([src.lat, src.lon], {
            radius: src.accuracy_km * 1000,
            fillColor: color,
            fillOpacity: 0.05,
            color,
            weight: 1,
            dashArray: '4 3'
          })
          locationLayer.addLayer(circle)
        }
      }
    }

    // Custom locations from the locations list
    for (const loc of (store.locations || [])) {
      const m = L.circleMarker([loc.lat, loc.lon], {
        radius: 6,
        fillColor: '#f59e0b',
        fillOpacity: 0.3,
        color: '#f59e0b',
        weight: 1.5
      })
      m.bindPopup(`<strong>${loc.name}</strong><br><span style="font-size:11px;color:#888">${loc.builtin ? 'Built-in' : 'Custom'}</span>`)
      locationLayer.addLayer(m)
    }
  }

  // Fit bounds
  const allVisible = visibleNodes.map(n => [n.latitude, n.longitude])
  // Include location sources in bounds
  const ls = store.locationSources
  if (ls && ls.sources) {
    for (const src of ls.sources) {
      if (src.lat && src.lon) allVisible.push([src.lat, src.lon])
    }
  }
  if (allVisible.length && !initialBoundsFit) {
    const bounds = L.latLngBounds(allVisible)
    map.fitBounds(bounds.pad(0.2), { maxZoom: 15 })
    initialBoundsFit = true
  }
}

function toggleNode(nodeId) {
  nodeVisibility[nodeId] = nodeVisibility[nodeId] === false ? true : false
  updateMap()
}

function toggleAll(show) {
  for (const n of nodesWithPositions.value) {
    nodeVisibility[n.id] = show
  }
  updateMap()
}

function nodeColor(idx) {
  return nodeColors[idx % nodeColors.length]
}

let updateTimer = null
function debouncedUpdateMap() {
  if (updateTimer) clearTimeout(updateTimer)
  updateTimer = setTimeout(updateMap, 100)
}

watch(() => store.nodes, debouncedUpdateMap, { deep: true })
watch(() => store.positions, debouncedUpdateMap, { deep: true })
watch(() => store.messages, debouncedUpdateMap, { deep: true })
watch(showMessages, updateMap)
watch(showTracks, updateMap)

onMounted(async () => {
  const since = new Date(Date.now() - 24 * 3600 * 1000).toISOString()
  // Initialize all node visibility to true
  for (const n of store.nodes) {
    if (nodeVisibility[n.id] === undefined) nodeVisibility[n.id] = true
  }
  await Promise.all([
    store.fetchNodes(),
    store.fetchPositions({ since, limit: 500 }),
    store.fetchMessages({ limit: 200 }),
    store.fetchLocations(),
    store.fetchLocationSources()
  ])
  // Set visibility for any newly loaded nodes
  for (const n of store.nodes) {
    if (nodeVisibility[n.id] === undefined) nodeVisibility[n.id] = true
  }
  await initMap()
})

onUnmounted(() => {
  if (map) { map.remove(); map = null }
})
</script>

<template>
  <div class="max-w-5xl mx-auto space-y-4">
    <div class="flex items-center justify-between">
      <h1 class="text-lg font-semibold text-gray-200">Map</h1>
      <button
        @click="store.fetchNodes().then(() => store.fetchMessages({ limit: 200 })).then(updateMap)"
        class="px-3 py-1.5 text-xs rounded bg-gray-800 text-gray-300 hover:text-white transition-colors"
      >
        Refresh
      </button>
    </div>

    <!-- Map container -->
    <div class="bg-gray-900 rounded-lg border border-gray-800 overflow-hidden">
      <div v-if="!mapError" ref="mapEl" class="w-full h-[450px] sm:h-[500px] bg-gray-800" />
      <div v-else class="p-8 text-center text-gray-500">
        Map unavailable. Nodes with GPS will display coordinates below.
      </div>
    </div>

    <!-- Controls bar -->
    <div class="flex flex-wrap items-center gap-4 text-xs">
      <!-- Legend -->
      <div class="flex items-center gap-3 text-gray-500">
        <div class="flex items-center gap-1.5">
          <span class="w-3 h-3 rounded-full bg-emerald-500"></span> Good
        </div>
        <div class="flex items-center gap-1.5">
          <span class="w-3 h-3 rounded-full bg-amber-500"></span> Fair
        </div>
        <div class="flex items-center gap-1.5">
          <span class="w-3 h-3 rounded-full bg-red-500"></span> Bad
        </div>
        <div class="flex items-center gap-1.5">
          <span class="w-2 h-2 rounded-full bg-teal-400"></span>
          <span>Msg</span>
        </div>
        <span class="text-gray-700">|</span>
        <div class="flex items-center gap-1.5">
          <span class="w-2 h-2 rounded-full bg-emerald-400 ring-1 ring-emerald-400/50"></span> GPS
        </div>
        <div class="flex items-center gap-1.5">
          <span class="w-2 h-2 rounded-full bg-teal-400 ring-1 ring-teal-400/50" style="border: 1px dashed"></span> Iridium
        </div>
        <div class="flex items-center gap-1.5">
          <span class="w-2 h-2 rounded-full bg-amber-400"></span> Custom
        </div>
      </div>

      <span class="flex-1" />

      <!-- Layer toggles -->
      <label class="flex items-center gap-1.5 cursor-pointer text-gray-400 hover:text-gray-200">
        <input type="checkbox" v-model="showMessages" class="rounded bg-gray-800 border-gray-600 text-teal-500 focus:ring-0 w-3 h-3" />
        Messages
      </label>
      <label class="flex items-center gap-1.5 cursor-pointer text-gray-400 hover:text-gray-200">
        <input type="checkbox" v-model="showTracks" class="rounded bg-gray-800 border-gray-600 text-teal-500 focus:ring-0 w-3 h-3" />
        Tracks
      </label>
    </div>

    <!-- Per-node filter checkboxes -->
    <div v-if="nodesWithPositions.length" class="bg-tactical-surface rounded-lg border border-tactical-border p-3">
      <div class="flex items-center justify-between mb-2">
        <span class="text-[10px] text-gray-500 uppercase tracking-wider">Node Filters</span>
        <div class="flex gap-2">
          <button @click="toggleAll(true)" class="text-[10px] text-teal-400 hover:text-teal-300">Show all</button>
          <button @click="toggleAll(false)" class="text-[10px] text-gray-500 hover:text-gray-300">Hide all</button>
        </div>
      </div>
      <div class="flex flex-wrap gap-2">
        <label v-for="(node, idx) in nodesWithPositions" :key="node.id"
          class="flex items-center gap-1.5 px-2 py-1 rounded cursor-pointer text-xs transition-colors"
          :class="nodeVisibility[node.id] !== false
            ? 'bg-gray-700/50 text-gray-200'
            : 'bg-gray-800/30 text-gray-600'">
          <input type="checkbox"
            :checked="nodeVisibility[node.id] !== false"
            @change="toggleNode(node.id)"
            class="rounded bg-gray-800 border-gray-600 text-teal-500 focus:ring-0 w-3 h-3" />
          <span class="w-2 h-2 rounded-full" :style="{ backgroundColor: nodeColor(idx) }"></span>
          {{ node.name || node.long_name || node.id }}
        </label>
      </div>
    </div>

    <!-- Stats summary -->
    <div class="text-[11px] text-gray-600">
      {{ nodesWithPositions.length }} nodes with GPS
      <span v-if="locatableMessages.length"> &middot; {{ locatableMessages.length }} locatable messages</span>
    </div>
  </div>
</template>
