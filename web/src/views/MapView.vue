<script setup>
import { ref, reactive, onMounted, onUnmounted, watch, computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import 'leaflet/dist/leaflet.css'

const store = useMeshsatStore()
const mapEl = ref(null)
const mapReady = ref(false)
const mapError = ref(false)

// Layer toggles
const showMessages = ref(true)
const showTracks = ref(true)
const showGps = ref(true)
const showCustom = ref(true)
const showIridium = ref(true)
const showCellular = ref(true)

// Per-node visibility toggles (reactive map: nodeId → boolean)
const nodeVisibility = reactive({})

// Map theme
function loadMapTheme() {
  return localStorage.getItem('meshsat-map-theme') || 'dark'
}
const mapTheme = ref(loadMapTheme())

const tileUrls = {
  dark: 'https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png',
  light: 'https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png'
}
const tileAttrs = {
  dark: '&copy; <a href="https://carto.com/">CARTO</a> &copy; OSM',
  light: '&copy; OSM'
}

let L = null
let map = null
let tileLayer = null
let markerLayer = null
let trackLayer = null
let messageLayer = null
let locationLayer = null
let iridiumLayer = null
let cellularLayer = null
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
      const nid = node.user_id || String(node.num)
      m[nid] = {
        lat: node.latitude,
        lon: node.longitude,
        name: node.long_name || node.short_name || nid,
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
  ).map(n => ({ ...n, _id: n.user_id || String(n.num) }))
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
    L = leaflet.default || leaflet

    if (!mapEl.value) return

    map = L.map(mapEl.value).setView([0, 0], 2)
    tileLayer = L.tileLayer(tileUrls[mapTheme.value], {
      attribution: tileAttrs[mapTheme.value],
      maxZoom: 19
    }).addTo(map)

    markerLayer = L.layerGroup().addTo(map)
    trackLayer = L.layerGroup().addTo(map)
    messageLayer = L.layerGroup().addTo(map)
    locationLayer = L.layerGroup().addTo(map)
    iridiumLayer = L.layerGroup().addTo(map)
    cellularLayer = L.layerGroup().addTo(map)
    mapReady.value = true
    updateMap()
  } catch (err) {
    console.error('Map init failed:', err)
    mapError.value = true
  }
}

function updateMap() {
  if (!map || !L || !markerLayer) return
  markerLayer.clearLayers()
  trackLayer.clearLayers()
  if (messageLayer) messageLayer.clearLayers()

  const visibleNodes = nodesWithPositions.value.filter(n => nodeVisibility[n._id] !== false)

  // Node markers
  for (const node of visibleNodes) {
    const color = signalColor(node.signal_quality)
    const m = L.circleMarker([node.latitude, node.longitude], {
      radius: 8, fillColor: color, fillOpacity: 0.8, color, weight: 2
    })
    let html = `<strong>${node.long_name || node.short_name || node._id}</strong>`
    if (node.battery != null) html += `<br>Battery: ${Math.round(node.battery)}%`
    if (node.snr != null) html += `<br>SNR: ${Number(node.snr).toFixed(1)} dB`
    m.bindPopup(html)
    markerLayer.addLayer(m)
  }

  // Track lines
  if (showTracks.value) {
    const visibleIds = new Set(visibleNodes.map(n => n._id))
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
    const visibleIds = new Set(visibleNodes.map(n => n._id))

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
      const color = isText ? '#38bdf8' : '#94a3b8'
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

  // Location source markers (GPS, Custom)
  if (locationLayer) {
    locationLayer.clearLayers()
    const ls = store.locationSources
    if (ls && ls.sources) {
      for (const src of ls.sources) {
        if (!src.lat || !src.lon) continue
        const isGps = src.source === 'gps'

        // Skip based on layer toggles
        if (isGps && !showGps.value) continue
        if (!isGps && !showCustom.value) continue

        const color = isGps ? '#10b981' : '#f59e0b'
        const label = isGps ? 'GPS' : 'Custom'
        const accKm = src.accuracy_km || 0
        const accTxt = accKm > 0
          ? (accKm < 1 ? `${(accKm * 1000).toFixed(0)}m` : `${accKm.toFixed(0)}km`)
          : ''

        const m = L.circleMarker([src.lat, src.lon], {
          radius: 7,
          fillColor: color,
          fillOpacity: 0.25,
          color,
          weight: 2
        })
        let popup = `<strong>${label} Position</strong><br>`
        popup += `<span style="font-family:monospace;font-size:11px">${src.lat.toFixed(5)}, ${src.lon.toFixed(5)}</span>`
        if (accTxt) popup += `<br>Accuracy: ~${accTxt}`
        m.bindPopup(popup)
        locationLayer.addLayer(m)
      }
    }

    // Custom locations from the locations list
    if (showCustom.value) {
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
  }

  // Iridium satellite sub-point markers (multi-pass visualization)
  if (iridiumLayer) {
    iridiumLayer.clearLayers()
    if (showIridium.value) {
      const ls2 = store.locationSources
      const passes = ls2?.iridium_passes || []
      const centroid = ls2?.iridium_centroid

      // Individual satellite sub-points (orange pins)
      for (let i = 0; i < passes.length; i++) {
        const p = passes[i]
        if (!p.lat || !p.lon) continue
        const age = Date.now() / 1000 - p.timestamp
        const opacity = Math.max(0.3, 1.0 - age / (6 * 3600)) // fade over 6h

        const m = L.circleMarker([p.lat, p.lon], {
          radius: 5,
          fillColor: '#f97316',
          fillOpacity: opacity,
          color: '#ea580c',
          weight: 1.5
        })
        const timeStr = new Date(p.timestamp * 1000).toLocaleTimeString()
        let popup = `<strong>Iridium Pass #${passes.length - i}</strong><br>`
        popup += `<span style="font-family:monospace;font-size:11px">${p.lat.toFixed(3)}, ${p.lon.toFixed(3)}</span>`
        popup += `<br><span style="font-size:11px;color:#888">${timeStr}</span>`
        popup += `<br>Accuracy: ~200 km (satellite sub-point)`
        if (p.satellite_id) popup += `<br>Sat: ${p.satellite_id}`
        m.bindPopup(popup)
        iridiumLayer.addLayer(m)
      }

      // Connect passes with dashed line (shows satellite track)
      if (passes.length >= 2) {
        const coords = passes.filter(p => p.lat && p.lon).map(p => [p.lat, p.lon])
        if (coords.length >= 2) {
          L.polyline(coords, {
            color: '#f97316',
            weight: 1.5,
            opacity: 0.4,
            dashArray: '4 6'
          }).addTo(iridiumLayer)
        }
      }

      // Polygon (triangle/quad) for 3+ points
      if (passes.length >= 3) {
        const coords = passes.filter(p => p.lat && p.lon).map(p => [p.lat, p.lon])
        if (coords.length >= 3) {
          L.polygon(coords, {
            color: '#f97316',
            weight: 1,
            opacity: 0.3,
            fillColor: '#f97316',
            fillOpacity: 0.08
          }).addTo(iridiumLayer)
        }
      }

      // Centroid marker (estimated position)
      if (centroid && centroid.lat && centroid.lon) {
        const accTxt = centroid.accuracy_km < 1
          ? `${(centroid.accuracy_km * 1000).toFixed(0)}m`
          : `${centroid.accuracy_km.toFixed(0)} km`
        const m = L.circleMarker([centroid.lat, centroid.lon], {
          radius: 9,
          fillColor: '#f97316',
          fillOpacity: 0.6,
          color: '#c2410c',
          weight: 3
        })
        let popup = `<strong>Iridium Estimated Position</strong><br>`
        popup += `<span style="font-family:monospace;font-size:11px">${centroid.lat.toFixed(4)}, ${centroid.lon.toFixed(4)}</span>`
        popup += `<br>Accuracy: ~${accTxt}`
        popup += `<br>Based on ${centroid.points} satellite passes`
        m.bindPopup(popup)
        iridiumLayer.addLayer(m)
      }
    }
  }

  // --- Cellular cell tower info ---
  if (cellularLayer) {
    cellularLayer.clearLayers()
    if (showCellular.value) {
      const ci = store.cellInfo?.latest || store.cellInfo?.live
      if (ci && ci.cell_id) {
        // Show cell info as a marker if we have location data, otherwise skip
        // For now, show cell info popup at resolved/GPS location as context overlay
        const cellLabel = `Cell: ${ci.mcc}/${ci.mnc} LAC=${ci.lac} CID=${ci.cell_id}`
        const netType = ci.network_type || ''
        const rsrp = ci.rsrp != null ? `${ci.rsrp} dBm` : 'N/A'

        // If we have a resolved location, place the cell info marker there
        const resolved = store.locationSources?.resolved
        if (resolved && resolved.lat && resolved.lon) {
          const m = L.circleMarker([resolved.lat, resolved.lon], {
            radius: 14,
            color: '#38bdf8',
            fillColor: '#38bdf8',
            fillOpacity: 0.08,
            weight: 1,
            dashArray: '4,4'
          })
          m.bindPopup(`<div style="font-size:11px">
            <b>Cell Tower Info</b><br/>
            MCC/MNC: ${ci.mcc}/${ci.mnc}<br/>
            LAC: ${ci.lac}<br/>
            Cell ID: ${ci.cell_id}<br/>
            Network: ${netType}<br/>
            RSRP: ${rsrp}
          </div>`)
          cellularLayer.addLayer(m)
        }
      }
    }
  }

  // Fit bounds (include all sources regardless of toggle for initial view)
  const allVisible = visibleNodes.map(n => [n.latitude, n.longitude])
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

function toggleMapTheme() {
  mapTheme.value = mapTheme.value === 'dark' ? 'light' : 'dark'
  localStorage.setItem('meshsat-map-theme', mapTheme.value)
  if (map && tileLayer && L) {
    map.removeLayer(tileLayer)
    tileLayer = L.tileLayer(tileUrls[mapTheme.value], {
      attribution: tileAttrs[mapTheme.value],
      maxZoom: 19
    }).addTo(map)
  }
}

function toggleNode(nodeId) {
  nodeVisibility[nodeId] = nodeVisibility[nodeId] === false ? true : false
  updateMap()
}

function toggleAll(show) {
  for (const n of nodesWithPositions.value) {
    nodeVisibility[n._id] = show
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
watch(showGps, updateMap)
watch(showCustom, updateMap)
watch(showIridium, updateMap)
watch(showCellular, updateMap)

onMounted(async () => {
  const since = new Date(Date.now() - 24 * 3600 * 1000).toISOString()
  // Initialize all node visibility to true
  for (const n of store.nodes) {
    const nid = n.user_id || String(n.num)
    if (nodeVisibility[nid] === undefined) nodeVisibility[nid] = true
  }
  await Promise.all([
    store.fetchNodes(),
    store.fetchPositions({ since, limit: 500 }),
    store.fetchMessages({ limit: 200 }),
    store.fetchLocations(),
    store.fetchLocationSources(),
    store.fetchCellInfo()
  ])
  // Set visibility for any newly loaded nodes
  for (const n of store.nodes) {
    const nid = n.user_id || String(n.num)
    if (nodeVisibility[nid] === undefined) nodeVisibility[nid] = true
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
      <div class="flex items-center gap-2">
        <button @click="toggleMapTheme"
          class="px-3 py-1.5 text-xs rounded bg-gray-800 text-gray-300 hover:text-white transition-colors"
          :title="mapTheme === 'dark' ? 'Switch to light map' : 'Switch to dark map'">
          {{ mapTheme === 'dark' ? 'Light' : 'Dark' }}
        </button>
        <button
          @click="store.fetchNodes().then(() => store.fetchMessages({ limit: 200 })).then(updateMap)"
          class="px-3 py-1.5 text-xs rounded bg-gray-800 text-gray-300 hover:text-white transition-colors"
        >
          Refresh
        </button>
      </div>
    </div>

    <!-- Map container -->
    <div class="bg-gray-900 rounded-lg border border-gray-800 overflow-hidden">
      <div v-if="!mapError" ref="mapEl" class="w-full h-[450px] sm:h-[500px] bg-gray-800" />
      <div v-else class="p-8 text-center text-gray-500">
        Map unavailable. Nodes with GPS will display coordinates below.
      </div>
    </div>

    <!-- Layer controls -->
    <div class="bg-tactical-surface rounded-lg border border-tactical-border p-3">
      <div class="flex items-center justify-between mb-2">
        <span class="text-[10px] text-gray-500 uppercase tracking-wider">Layers</span>
      </div>
      <div class="flex flex-wrap items-center gap-4 text-xs">
        <!-- Location sources -->
        <label class="flex items-center gap-1.5 cursor-pointer text-gray-400 hover:text-gray-200">
          <input type="checkbox" v-model="showGps" class="rounded bg-gray-800 border-gray-600 text-emerald-500 focus:ring-0 w-3 h-3" />
          <span class="w-2 h-2 rounded-full bg-emerald-400"></span>
          GPS
        </label>
        <label class="flex items-center gap-1.5 cursor-pointer text-gray-400 hover:text-gray-200">
          <input type="checkbox" v-model="showCustom" class="rounded bg-gray-800 border-gray-600 text-amber-500 focus:ring-0 w-3 h-3" />
          <span class="w-2 h-2 rounded-full bg-amber-400"></span>
          Custom
        </label>
        <label class="flex items-center gap-1.5 cursor-pointer text-gray-400 hover:text-gray-200">
          <input type="checkbox" v-model="showIridium" class="rounded bg-gray-800 border-gray-600 text-orange-500 focus:ring-0 w-3 h-3" />
          <span class="w-2 h-2 rounded-full bg-orange-400"></span>
          Iridium
        </label>
        <label class="flex items-center gap-1.5 cursor-pointer text-gray-400 hover:text-gray-200">
          <input type="checkbox" v-model="showCellular" class="rounded bg-gray-800 border-gray-600 text-sky-500 focus:ring-0 w-3 h-3" />
          <span class="w-2 h-2 rounded-full bg-sky-400"></span>
          Cellular
        </label>

        <span class="w-px h-4 bg-gray-700" />

        <!-- Mesh data layers -->
        <label class="flex items-center gap-1.5 cursor-pointer text-gray-400 hover:text-gray-200">
          <input type="checkbox" v-model="showMessages" class="rounded bg-gray-800 border-gray-600 text-sky-500 focus:ring-0 w-3 h-3" />
          <span class="w-2 h-2 rounded-full bg-sky-400"></span>
          Messages
        </label>
        <label class="flex items-center gap-1.5 cursor-pointer text-gray-400 hover:text-gray-200">
          <input type="checkbox" v-model="showTracks" class="rounded bg-gray-800 border-gray-600 text-cyan-500 focus:ring-0 w-3 h-3" />
          <span class="w-2 h-2 rounded-full bg-cyan-400"></span>
          Tracks
        </label>

        <span class="flex-1" />

        <!-- Signal legend (not toggleable, just reference) -->
        <div class="flex items-center gap-3 text-gray-600">
          <span class="flex items-center gap-1"><span class="w-2 h-2 rounded-full bg-emerald-500"></span> Good</span>
          <span class="flex items-center gap-1"><span class="w-2 h-2 rounded-full bg-amber-500"></span> Fair</span>
          <span class="flex items-center gap-1"><span class="w-2 h-2 rounded-full bg-red-500"></span> Bad</span>
        </div>
      </div>
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
        <label v-for="(node, idx) in nodesWithPositions" :key="node._id"
          class="flex items-center gap-1.5 px-2 py-1 rounded cursor-pointer text-xs transition-colors"
          :class="nodeVisibility[node._id] !== false
            ? 'bg-gray-700/50 text-gray-200'
            : 'bg-gray-800/30 text-gray-600'">
          <input type="checkbox"
            :checked="nodeVisibility[node._id] !== false"
            @change="toggleNode(node._id)"
            class="rounded bg-gray-800 border-gray-600 text-teal-500 focus:ring-0 w-3 h-3" />
          <span class="w-2 h-2 rounded-full" :style="{ backgroundColor: nodeColor(idx) }"></span>
          {{ node.long_name || node.short_name || node._id }}
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
