<script setup>
import { ref, reactive, onMounted, onUnmounted, watch, computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import 'leaflet/dist/leaflet.css'
import ms from 'milsymbol'

const store = useMeshsatStore()
const mapEl = ref(null)
const mapContainer = ref(null)
const mapReady = ref(false)
const mapError = ref(false)

// Auto-refresh
const autoRefresh = ref(false)
let autoRefreshTimer = null

// Click-to-copy coordinates
const clickedCoords = ref(null)
const coordsCopied = ref(false)

// Bearing/distance measurement tool
const measureMode = ref(false)
const measurePointA = ref(null)
const measurePointB = ref(null)
const measureResult = ref(null)
let measureMarkerA = null
let measureMarkerB = null
let measureLine = null

function toRad(deg) { return deg * Math.PI / 180 }
function toDeg(rad) { return rad * 180 / Math.PI }

function haversineDistance(lat1, lon1, lat2, lon2) {
  const R = 6371 // km
  const dLat = toRad(lat2 - lat1)
  const dLon = toRad(lon2 - lon1)
  const a = Math.sin(dLat / 2) ** 2 +
    Math.cos(toRad(lat1)) * Math.cos(toRad(lat2)) * Math.sin(dLon / 2) ** 2
  return R * 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a))
}

function bearing(lat1, lon1, lat2, lon2) {
  const dLon = toRad(lon2 - lon1)
  const y = Math.sin(dLon) * Math.cos(toRad(lat2))
  const x = Math.cos(toRad(lat1)) * Math.sin(toRad(lat2)) -
    Math.sin(toRad(lat1)) * Math.cos(toRad(lat2)) * Math.cos(dLon)
  return (toDeg(Math.atan2(y, x)) + 360) % 360
}

function toggleMeasureMode() {
  measureMode.value = !measureMode.value
  if (!measureMode.value) clearMeasure()
}

function clearMeasure() {
  measurePointA.value = null
  measurePointB.value = null
  measureResult.value = null
  if (measureMarkerA) { map.removeLayer(measureMarkerA); measureMarkerA = null }
  if (measureMarkerB) { map.removeLayer(measureMarkerB); measureMarkerB = null }
  if (measureLine) { map.removeLayer(measureLine); measureLine = null }
}

function handleMeasureClick(e) {
  if (!measureMode.value || !L || !map) return
  const { lat, lng } = e.latlng

  if (!measurePointA.value) {
    measurePointA.value = { lat, lon: lng }
    measurePointB.value = null
    measureResult.value = null
    if (measureMarkerB) { map.removeLayer(measureMarkerB); measureMarkerB = null }
    if (measureLine) { map.removeLayer(measureLine); measureLine = null }
    if (measureMarkerA) map.removeLayer(measureMarkerA)
    measureMarkerA = L.circleMarker([lat, lng], {
      radius: 6, fillColor: '#f43f5e', fillOpacity: 0.9, color: '#fff', weight: 2
    }).addTo(map).bindPopup('Point A').openPopup()
  } else {
    measurePointB.value = { lat, lon: lng }
    if (measureMarkerB) map.removeLayer(measureMarkerB)
    measureMarkerB = L.circleMarker([lat, lng], {
      radius: 6, fillColor: '#3b82f6', fillOpacity: 0.9, color: '#fff', weight: 2
    }).addTo(map).bindPopup('Point B').openPopup()

    if (measureLine) map.removeLayer(measureLine)
    const a = measurePointA.value
    measureLine = L.polyline([[a.lat, a.lon], [lat, lng]], {
      color: '#f43f5e', weight: 2, dashArray: '6 4'
    }).addTo(map)

    const dist = haversineDistance(a.lat, a.lon, lat, lng)
    const brg = bearing(a.lat, a.lon, lat, lng)
    measureResult.value = {
      distance_km: dist,
      bearing_deg: brg,
      distance_display: dist < 1 ? `${(dist * 1000).toFixed(0)} m` : `${dist.toFixed(2)} km`,
      bearing_display: `${brg.toFixed(1)}°`
    }
    measureLine.bindPopup(
      `<strong>${measureResult.value.distance_display}</strong> @ ${measureResult.value.bearing_display}`
    ).openPopup()
  }
}

// Team/group filter (TAK team colors)
const takTeamColors = {
  Cyan: '#00FFFF',
  Green: '#00FF00',
  Yellow: '#FFFF00',
  White: '#FFFFFF',
  Orange: '#FF8C00',
  Magenta: '#FF00FF',
  Maroon: '#800000',
  Red: '#FF0000'
}
const selectedTeam = ref('all') // 'all' or team name

// Derive team from cot_type: a-f = friendly (Cyan), a-h = hostile (Red), a-n = neutral (Green), a-u = unknown (Yellow)
function nodeTeam(cotType) {
  if (!cotType) return 'Cyan'
  if (cotType.startsWith('a-h-')) return 'Red'
  if (cotType.startsWith('a-n-')) return 'Green'
  if (cotType.startsWith('a-u-')) return 'Yellow'
  return 'Cyan' // friendly default
}

// Available teams based on current nodes
const availableTeams = computed(() => {
  const teams = new Set()
  for (const node of nodesWithPositions.value) {
    teams.add(nodeTeam(node.cot_type))
  }
  return [...teams].sort()
})

// Fullscreen
const isFullscreen = ref(false)

// Geofence management
const showGeofencePanel = ref(false)
const showNewZoneForm = ref(false)
const newZone = ref({ id: '', name: '', alert_on: 'both', polygon: '' })
const geofenceCreating = ref(false)
let geofenceLayer = null

function resetNewZone() {
  newZone.value = { id: '', name: '', alert_on: 'both', polygon: '' }
}

async function createGeofenceZone() {
  const lines = newZone.value.polygon.trim().split('\n').filter(l => l.trim())
  if (lines.length < 3) { store.error = 'Polygon needs at least 3 vertices'; return }
  const polygon = lines.map(l => {
    const parts = l.trim().split(/[,\s]+/)
    return { lat: parseFloat(parts[0]), lon: parseFloat(parts[1]) }
  })
  if (polygon.some(p => isNaN(p.lat) || isNaN(p.lon))) { store.error = 'Invalid coordinate format'; return }

  geofenceCreating.value = true
  try {
    await store.createGeofence({
      id: newZone.value.id || newZone.value.name.toLowerCase().replace(/\s+/g, '-'),
      name: newZone.value.name,
      alert_on: newZone.value.alert_on,
      polygon
    })
    showNewZoneForm.value = false
    resetNewZone()
    renderGeofences()
  } catch {}
  geofenceCreating.value = false
}

async function removeGeofence(id) {
  if (!confirm(`Delete geofence zone "${id}"?`)) return
  try {
    await store.deleteGeofence(id)
    renderGeofences()
  } catch {}
}

function renderGeofences() {
  if (!map || !L || !geofenceLayer) return
  geofenceLayer.clearLayers()
  for (const zone of store.geofences) {
    if (!zone.polygon || zone.polygon.length < 3) continue
    const coords = zone.polygon.map(p => [p.lat, p.lon])
    const color = zone.alert_on === 'enter' ? '#f59e0b' : zone.alert_on === 'exit' ? '#ef4444' : '#8b5cf6'
    const poly = L.polygon(coords, {
      color, weight: 2, opacity: 0.6,
      fillColor: color, fillOpacity: 0.1,
      dashArray: '6 4'
    })
    poly.bindPopup(`<strong>${zone.name || zone.id}</strong><br><span style="font-size:11px">Alert on: ${zone.alert_on}</span>`)
    geofenceLayer.addLayer(poly)
  }
}

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

// TAK/CoT marker icon system — MIL-STD-2525-inspired SVG icons
function cotColor(cotType) {
  if (!cotType) return '#4A90D9'
  if (cotType.startsWith('a-h-')) return '#FF3030' // hostile
  if (cotType.startsWith('a-n-')) return '#00A000' // neutral
  if (cotType.startsWith('a-u-')) return '#FFFF00' // unknown
  if (cotType === 'b-a') return '#FF0000'          // emergency
  if (cotType.startsWith('b-m-p')) return '#FF8C00' // waypoint
  if (cotType === 't-x-d-d') return '#9B59B6'      // sensor
  if (cotType === 'b-t-f') return '#22D3EE'         // chat
  return '#4A90D9' // friendly (default)
}

function cotShape(cotType) {
  if (!cotType) return 'diamond'
  if (cotType.includes('-U-C-I') || cotType.includes('-E-')) return 'square'
  if (cotType.startsWith('b-m-p')) return 'pushpin'
  if (cotType === 'b-a') return 'emergency'
  if (cotType === 't-x-d-d') return 'circle'
  return 'diamond'
}

function cotMarkerIcon(cotType, callsign, stale) {
  const color = cotColor(cotType)
  const shape = cotShape(cotType)
  const opacity = stale ? 0.45 : 1.0
  const label = callsign ? `<text x="16" y="39" text-anchor="middle" fill="#fff" font-size="9" font-family="sans-serif" style="text-shadow:0 0 3px #000">${callsign.length > 8 ? callsign.slice(-6) : callsign}</text>` : ''

  let svg = ''
  if (shape === 'diamond') {
    svg = `<svg width="32" height="42" viewBox="0 0 32 42" opacity="${opacity}"><polygon points="16,2 30,16 16,30 2,16" fill="${color}" stroke="#fff" stroke-width="2"/>${label}</svg>`
  } else if (shape === 'square') {
    svg = `<svg width="32" height="42" viewBox="0 0 32 42" opacity="${opacity}"><rect x="3" y="3" width="26" height="26" fill="${color}" stroke="#fff" stroke-width="2" rx="2"/>${label}</svg>`
  } else if (shape === 'pushpin') {
    svg = `<svg width="24" height="38" viewBox="0 0 24 38" opacity="${opacity}"><circle cx="12" cy="10" r="8" fill="${color}" stroke="#fff" stroke-width="2"/><polygon points="12,36 6,18 18,18" fill="${color}"/><circle cx="12" cy="10" r="3" fill="#fff"/></svg>`
  } else if (shape === 'emergency') {
    svg = `<svg width="32" height="42" viewBox="0 0 32 42" opacity="${opacity}"><circle cx="16" cy="16" r="14" fill="${color}" stroke="#fff" stroke-width="2"/><line x1="8" y1="8" x2="24" y2="24" stroke="#fff" stroke-width="3"/><line x1="24" y1="8" x2="8" y2="24" stroke="#fff" stroke-width="3"/><text x="16" y="39" text-anchor="middle" fill="#FF0000" font-size="9" font-weight="bold">SOS</text></svg>`
  } else {
    svg = `<svg width="28" height="42" viewBox="0 0 28 42" opacity="${opacity}"><circle cx="14" cy="14" r="12" fill="${color}" stroke="#fff" stroke-width="2"/>${label}</svg>`
  }

  return L.divIcon({
    html: svg,
    className: '',
    iconSize: [32, 42],
    iconAnchor: [16, 30],
    popupAnchor: [0, -30]
  })
}

// MIL-STD-2525D / APP-6D symbol icon rendered via milsymbol.
// [MESHSAT-559]
function milsymbolIcon(sidc, callsign, stale) {
  try {
    const symbol = new ms.Symbol(sidc, {
      size: 32,
      uniqueDesignation: callsign,
      monoColor: stale ? '#888' : undefined
    })
    const size = symbol.getSize()
    const anchor = symbol.getAnchor()
    return L.divIcon({
      html: symbol.asSVG(),
      className: '',
      iconSize:   [size.width, size.height],
      iconAnchor: [anchor.x,   anchor.y],
      popupAnchor: [0, -anchor.y]
    })
  } catch {
    // Malformed SIDC — fall back to the caller's default.
    return null
  }
}

// Lookup SIDC for a node via its matching directory contact. The
// store already loads /api/contacts; each contact's addresses are
// walked to see whether its mesh/tak/etc address matches the node.
function sidcForNode(node) {
  const candidates = [node._id, node.user_id, node.short_name, node.long_name]
  const contacts = store.contacts || []
  for (const c of contacts) {
    if (!c.sidc) continue
    for (const a of (c.addresses || [])) {
      const val = (a.address || a.value || '').toLowerCase()
      if (!val) continue
      for (const cand of candidates) {
        if (cand && String(cand).toLowerCase() === val) return c.sidc
      }
    }
  }
  return ''
}

// Build a lookup: nodeId → {lat, lon, name, color} — includes TAK positions
const nodePositionMap = computed(() => {
  const m = {}
  for (const node of nodesWithPositions.value) {
    const nid = node._id
    m[nid] = {
      lat: node.latitude,
      lon: node.longitude,
      name: node.long_name || node.short_name || nid,
      signal_quality: node.signal_quality
    }
  }
  return m
})

// Unique nodes with positions (for checkboxes) — includes TAK-relayed positions
const nodesWithPositions = computed(() => {
  // Mesh nodes from /api/nodes
  const meshNodes = store.nodes.filter(n =>
    n.latitude && n.longitude && n.latitude !== 0 && n.longitude !== 0
  ).map(n => ({ ...n, _id: n.user_id || String(n.num) }))

  // TAK/CoT positions that don't have a matching mesh node (relayed via Hub)
  const meshIds = new Set(meshNodes.map(n => n._id))
  const latestByNode = {}
  for (const p of store.positions) {
    const nid = String(p.node_id ?? p.from)
    if (!nid || meshIds.has(nid)) continue
    if (!p.latitude || !p.longitude || (p.latitude === 0 && p.longitude === 0)) continue
    if (!latestByNode[nid] || new Date(p.created_at) > new Date(latestByNode[nid].created_at)) {
      latestByNode[nid] = p
    }
  }
  const takNodes = Object.entries(latestByNode).map(([nid, p]) => ({
    _id: nid,
    user_id: nid,
    long_name: nid.replace(/^meshsat-/, '').replace(/-/g, ' '),
    short_name: nid.length > 8 ? nid.slice(-8) : nid,
    latitude: p.latitude,
    longitude: p.longitude,
    altitude: p.altitude || 0,
    cot_type: 'a-f-G-U-C',
    last_heard: Math.floor(new Date(p.created_at).getTime() / 1000),
    _source: 'tak'
  }))

  return [...meshNodes, ...takNodes]
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

    map = L.map(mapEl.value).setView([52.16, 4.49], 6)
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
    geofenceLayer = L.layerGroup().addTo(map)
    mapReady.value = true

    // Map click for coordinate display
    map.on('click', onMapClick)

    // Leaflet needs a size recalc after the container is rendered in the DOM
    setTimeout(() => { if (map) map.invalidateSize() }, 200)

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

  const visibleNodes = nodesWithPositions.value.filter(n =>
    nodeVisibility[n._id] !== false &&
    (selectedTeam.value === 'all' || nodeTeam(n.cot_type) === selectedTeam.value)
  )

  // Node markers — prefer a MIL-STD-2525D symbol when the matching
  // directory contact has a SIDC set (MESHSAT-559); fall back to
  // the CoT-derived marker otherwise.
  for (const node of visibleNodes) {
    const callsign = node.short_name || node.long_name || node._id
    const cotType = node.cot_type || 'a-f-G-U-C' // default: friendly ground unit
    const lastSeen = node.last_heard ? new Date(node.last_heard * 1000) : null
    const stale = lastSeen ? (Date.now() - lastSeen.getTime() > 300000) : false
    const sidc = sidcForNode(node)
    const icon = sidc
      ? milsymbolIcon(sidc, callsign, stale)
      : cotMarkerIcon(cotType, callsign, stale)
    const m = L.marker([node.latitude, node.longitude], { icon })
    let html = `<strong>${node.long_name || node.short_name || node._id}</strong>`
    html += `<br><span style="color:#888;font-size:11px">${cotType}</span>`
    if (node.battery != null) html += `<br>Battery: ${Math.round(node.battery)}%`
    if (node.snr != null) html += `<br>SNR: ${Number(node.snr).toFixed(1)} dB`
    if (stale && lastSeen) html += `<br><span style="color:#f59e0b">Stale (${Math.floor((Date.now() - lastSeen.getTime()) / 60000)}m ago)</span>`
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

// Auto-refresh: poll every 30s when enabled
function toggleAutoRefresh() {
  autoRefresh.value = !autoRefresh.value
  if (autoRefresh.value) {
    autoRefreshTimer = setInterval(async () => {
      const since = new Date(Date.now() - 24 * 3600 * 1000).toISOString()
      await Promise.all([
        store.fetchNodes(),
        store.fetchPositions({ since, limit: 500 }),
        store.fetchMessages({ limit: 200 }),
        store.fetchLocationSources(),
        store.fetchCellInfo()
      ])
      updateMap()
    }, 30000)
  } else {
    clearInterval(autoRefreshTimer)
    autoRefreshTimer = null
  }
}

// Click-to-copy coordinates on map click
function onMapClick(e) {
  if (measureMode.value) {
    handleMeasureClick(e)
    return
  }
  const lat = e.latlng.lat.toFixed(6)
  const lon = e.latlng.lng.toFixed(6)
  clickedCoords.value = `${lat}, ${lon}`
  coordsCopied.value = false
}

async function copyCoords() {
  if (!clickedCoords.value) return
  try {
    await navigator.clipboard.writeText(clickedCoords.value)
    coordsCopied.value = true
    setTimeout(() => { coordsCopied.value = false }, 2000)
  } catch {
    // Fallback for non-secure contexts
    const ta = document.createElement('textarea')
    ta.value = clickedCoords.value
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
    coordsCopied.value = true
    setTimeout(() => { coordsCopied.value = false }, 2000)
  }
}

// Fullscreen toggle
function toggleFullscreen() {
  const el = mapContainer.value
  if (!el) return
  if (!document.fullscreenElement) {
    el.requestFullscreen?.() || el.webkitRequestFullscreen?.()
  } else {
    document.exitFullscreen?.() || document.webkitExitFullscreen?.()
  }
}

function onFullscreenChange() {
  isFullscreen.value = !!document.fullscreenElement
  // Leaflet needs size recalc after fullscreen change
  setTimeout(() => { if (map) map.invalidateSize() }, 200)
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
watch(selectedTeam, updateMap)
watch(() => store.geofences, renderGeofences, { deep: true })

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
    store.fetchCellInfo(),
    store.fetchGeofences(),
    // Contacts feed sidcForNode() lookup for MIL-STD-2525D pins.
    // [MESHSAT-559]
    store.fetchContacts()
  ])
  // Set visibility for any newly loaded nodes (including TAK-relayed)
  for (const n of nodesWithPositions.value) {
    if (nodeVisibility[n._id] === undefined) nodeVisibility[n._id] = true
  }
  await initMap()
  renderGeofences()
  // Center on resolved location if available and no nodes have GPS
  if (map && !nodesWithPositions.value.length) {
    const resolved = store.locationSources?.resolved
    if (resolved && resolved.lat && resolved.lon) {
      map.setView([resolved.lat, resolved.lon], 10)
    }
  }
  document.addEventListener('fullscreenchange', onFullscreenChange)
})

onUnmounted(() => {
  clearMeasure()
  if (map) { map.remove(); map = null }
  if (autoRefreshTimer) clearInterval(autoRefreshTimer)
  document.removeEventListener('fullscreenchange', onFullscreenChange)
})
</script>

<template>
  <div class="max-w-5xl mx-auto space-y-4">
    <div class="flex items-center justify-between">
      <h1 class="text-lg font-semibold text-gray-200">Map</h1>
      <div class="flex items-center gap-2">
        <button @click="toggleAutoRefresh"
          class="px-3 py-1.5 text-xs rounded transition-colors"
          :class="autoRefresh ? 'bg-teal-600 text-white hover:bg-teal-500' : 'bg-gray-800 text-gray-300 hover:text-white'"
          title="Auto-refresh every 30s">
          {{ autoRefresh ? 'Auto: ON' : 'Auto: OFF' }}
        </button>
        <button @click="toggleMapTheme"
          class="px-3 py-1.5 text-xs rounded bg-gray-800 text-gray-300 hover:text-white transition-colors"
          :title="mapTheme === 'dark' ? 'Switch to light map' : 'Switch to dark map'">
          {{ mapTheme === 'dark' ? 'Light' : 'Dark' }}
        </button>
        <button @click="toggleFullscreen"
          class="px-3 py-1.5 text-xs rounded bg-gray-800 text-gray-300 hover:text-white transition-colors"
          :title="isFullscreen ? 'Exit fullscreen' : 'Fullscreen'">
          {{ isFullscreen ? 'Exit FS' : 'Fullscreen' }}
        </button>
        <button @click="toggleMeasureMode"
          class="px-3 py-1.5 text-xs rounded transition-colors"
          :class="measureMode ? 'bg-rose-600 text-white hover:bg-rose-500' : 'bg-gray-800 text-gray-300 hover:text-white'"
          title="Bearing/distance measurement — click two points on map">
          {{ measureMode ? 'Measure: ON' : 'Measure' }}
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
    <div ref="mapContainer" class="bg-gray-900 rounded-lg border border-gray-800 overflow-hidden relative">
      <div v-if="!mapError" ref="mapEl" class="w-full bg-gray-800"
        :class="isFullscreen ? 'h-screen' : 'h-[450px] sm:h-[500px]'" />
      <div v-else class="p-8 text-center text-gray-500">
        Map unavailable. Nodes with GPS will display coordinates below.
      </div>
      <!-- Clicked coordinates overlay -->
      <div v-if="clickedCoords" class="absolute bottom-2 left-2 z-[1000] flex items-center gap-2 px-2.5 py-1.5 rounded bg-gray-900/90 border border-gray-700 backdrop-blur-sm">
        <code class="text-[11px] text-gray-300 font-mono">{{ clickedCoords }}</code>
        <button @click.stop="copyCoords"
          class="text-[10px] px-1.5 py-0.5 rounded transition-colors"
          :class="coordsCopied ? 'text-emerald-400 bg-emerald-400/10' : 'text-gray-400 hover:text-white bg-gray-700'">
          {{ coordsCopied ? 'Copied' : 'Copy' }}
        </button>
        <button @click.stop="clickedCoords = null" class="text-[10px] text-gray-500 hover:text-gray-300">&times;</button>
      </div>
      <!-- Measurement result overlay -->
      <div v-if="measureMode" class="absolute top-2 left-2 z-[1000] px-2.5 py-1.5 rounded bg-gray-900/90 border border-gray-700 backdrop-blur-sm">
        <div v-if="measureResult" class="text-xs space-y-0.5">
          <div class="text-gray-200 font-medium">
            <span class="text-rose-400">{{ measureResult.distance_display }}</span>
            <span class="text-gray-500 mx-1">@</span>
            <span class="text-blue-400">{{ measureResult.bearing_display }}</span>
          </div>
          <button @click.stop="clearMeasure" class="text-[10px] text-gray-500 hover:text-gray-300">Reset</button>
        </div>
        <div v-else class="text-[11px] text-gray-500">
          {{ measurePointA ? 'Click point B' : 'Click point A' }}
        </div>
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

        <span class="w-px h-4 bg-gray-700" />

        <!-- Team/group filter -->
        <div class="flex items-center gap-1.5">
          <span class="text-gray-600 text-[10px]">Team:</span>
          <select v-model="selectedTeam"
            class="px-1.5 py-0.5 rounded bg-gray-800 border border-gray-700 text-xs text-gray-300 focus:ring-0 focus:border-gray-600">
            <option value="all">All</option>
            <option v-for="team in availableTeams" :key="team" :value="team">{{ team }}</option>
          </select>
          <span v-if="selectedTeam !== 'all'" class="w-2.5 h-2.5 rounded-full border border-gray-600"
            :style="{ backgroundColor: takTeamColors[selectedTeam] || '#888' }" />
        </div>

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

    <!-- Geofence Zones -->
    <div class="bg-tactical-surface rounded-lg border border-tactical-border p-3">
      <div class="flex items-center justify-between mb-2">
        <span class="text-[10px] text-gray-500 uppercase tracking-wider">Geofence Zones</span>
        <div class="flex gap-2">
          <button @click="showGeofencePanel = !showGeofencePanel"
            class="text-[10px] text-teal-400 hover:text-teal-300">
            {{ showGeofencePanel ? 'Hide' : 'Manage' }}
          </button>
        </div>
      </div>

      <!-- Zone count summary -->
      <div v-if="!showGeofencePanel" class="text-[11px] text-gray-500">
        {{ store.geofences.length }} zone{{ store.geofences.length !== 1 ? 's' : '' }} configured
      </div>

      <!-- Expanded panel -->
      <div v-if="showGeofencePanel" class="space-y-2">
        <!-- Zone list -->
        <div v-for="zone in store.geofences" :key="zone.id"
          class="flex items-center gap-2 py-1.5 px-2 rounded bg-tactical-bg/50">
          <span class="w-2 h-2 rounded-full"
            :class="zone.alert_on === 'enter' ? 'bg-amber-400' : zone.alert_on === 'exit' ? 'bg-red-400' : 'bg-violet-400'" />
          <span class="text-xs text-gray-300 flex-1 truncate">{{ zone.name || zone.id }}</span>
          <span class="text-[9px] font-mono text-gray-500">{{ zone.alert_on }}</span>
          <span class="text-[9px] text-gray-600">{{ zone.polygon?.length || 0 }} pts</span>
          <button @click="removeGeofence(zone.id)"
            class="text-[9px] text-red-400/60 hover:text-red-400 transition-colors">
            Delete
          </button>
        </div>
        <div v-if="!store.geofences.length" class="text-[11px] text-gray-600 text-center py-2">
          No geofence zones configured
        </div>

        <!-- New Zone button / form -->
        <button v-if="!showNewZoneForm" @click="showNewZoneForm = true"
          class="w-full px-3 py-2 rounded border border-dashed border-gray-700 text-xs text-gray-400 hover:text-teal-400 hover:border-teal-400/30 transition-colors">
          + New Zone
        </button>

        <div v-if="showNewZoneForm" class="space-y-2 bg-tactical-bg/50 rounded-lg p-3">
          <div class="flex gap-2">
            <input v-model="newZone.name" placeholder="Zone name"
              class="flex-1 px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-200" />
            <select v-model="newZone.alert_on"
              class="px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-200">
              <option value="both">Alert: Both</option>
              <option value="enter">Alert: Enter</option>
              <option value="exit">Alert: Exit</option>
            </select>
          </div>
          <textarea v-model="newZone.polygon" rows="4"
            placeholder="Polygon vertices (one per line):&#10;52.370216, 4.895168&#10;52.373086, 4.892568&#10;52.371463, 4.899351"
            class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-200 font-mono" />
          <div class="flex gap-2">
            <button @click="createGeofenceZone" :disabled="geofenceCreating || !newZone.name"
              class="flex-1 px-3 py-1.5 rounded bg-teal-600 text-white text-xs font-medium hover:bg-teal-500 disabled:opacity-40">
              {{ geofenceCreating ? 'Creating...' : 'Create Zone' }}
            </button>
            <button @click="showNewZoneForm = false; resetNewZone()"
              class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs hover:bg-gray-600">
              Cancel
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Stats summary -->
    <div class="text-[11px] text-gray-600">
      {{ nodesWithPositions.length }} nodes with GPS
      <span v-if="locatableMessages.length"> &middot; {{ locatableMessages.length }} locatable messages</span>
      <span v-if="store.geofences.length"> &middot; {{ store.geofences.length }} geofence zones</span>
    </div>
  </div>
</template>
