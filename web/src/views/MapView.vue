<script setup>
import { ref, onMounted, onUnmounted, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const mapEl = ref(null)
const mapReady = ref(false)
const mapError = ref(false)

let L = null
let map = null
let markerLayer = null
let trackLayer = null

function signalColor(q) {
  if (!q) return '#6b7280'
  const u = q.toUpperCase()
  if (u === 'GOOD') return '#10b981'
  if (u === 'FAIR') return '#f59e0b'
  return '#ef4444'
}

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

  const nodesWithPos = store.nodes.filter(n =>
    n.latitude && n.longitude && n.latitude !== 0 && n.longitude !== 0
  )

  for (const node of nodesWithPos) {
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

  // Track lines from position history
  const groups = {}
  for (const p of store.positions) {
    const nid = p.node_id ?? p.from
    if (!nid || !p.latitude || !p.longitude) continue
    if (!groups[nid]) groups[nid] = []
    groups[nid].push([Number(p.latitude), Number(p.longitude)])
  }
  const colors = ['#06b6d4', '#8b5cf6', '#f97316', '#ec4899']
  let ci = 0
  for (const nid in groups) {
    if (groups[nid].length < 2) continue
    L.polyline(groups[nid], { color: colors[ci % colors.length], weight: 2, opacity: 0.6, dashArray: '4 4' }).addTo(trackLayer)
    ci++
  }

  if (nodesWithPos.length) {
    const bounds = L.latLngBounds(nodesWithPos.map(n => [n.latitude, n.longitude]))
    map.fitBounds(bounds.pad(0.2), { maxZoom: 15 })
  }
}

watch(() => store.nodes, updateMap, { deep: true })
watch(() => store.positions, updateMap, { deep: true })

onMounted(async () => {
  const since = new Date(Date.now() - 24 * 3600 * 1000).toISOString()
  await Promise.all([
    store.fetchNodes(),
    store.fetchPositions({ since, limit: 500 })
  ])
  await initMap()
})

onUnmounted(() => {
  if (map) { map.remove(); map = null }
})
</script>

<template>
  <div class="max-w-5xl mx-auto space-y-6">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-bold">Map</h1>
      <button
        @click="store.fetchNodes().then(updateMap)"
        class="px-3 py-1.5 text-sm rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors"
      >
        Refresh
      </button>
    </div>

    <div class="bg-gray-900 rounded-xl border border-gray-800 overflow-hidden">
      <div v-if="!mapError" ref="mapEl" class="w-full h-[500px] bg-gray-800" />
      <div v-else class="p-8 text-center text-gray-500">
        Map unavailable. Nodes with GPS will display coordinates below.
      </div>
    </div>

    <!-- Legend -->
    <div class="flex items-center gap-4 text-xs text-gray-500">
      <div class="flex items-center gap-1.5">
        <span class="w-3 h-3 rounded-full bg-emerald-500"></span> Good
      </div>
      <div class="flex items-center gap-1.5">
        <span class="w-3 h-3 rounded-full bg-amber-500"></span> Fair
      </div>
      <div class="flex items-center gap-1.5">
        <span class="w-3 h-3 rounded-full bg-red-500"></span> Bad
      </div>
    </div>
  </div>
</template>
