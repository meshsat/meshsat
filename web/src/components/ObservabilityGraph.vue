<script setup>
import { computed, ref, onMounted, onUnmounted, watch } from 'vue'
import { useObservabilityStore } from '../composables/useObservabilityStore'

const store = useObservabilityStore()

const svgRef = ref(null)
const viewBox = ref({ x: -400, y: -300, w: 800, h: 600 })
const dragging = ref(false)
const dragStart = ref({ x: 0, y: 0 })
const scale = ref(1)

// ── Colors by channel type ──
const typeColors = {
  mesh: '#06B6D4', iridium: '#A855F7', iridium_sbd: '#A855F7',
  iridium_imt: '#C084FC', cellular: '#F97316', sms: '#22C55E',
  astrocast: '#EAB308', zigbee: '#34D399', aprs: '#06B6D4',
  tcp: '#64748B', mqtt: '#64748B', webhook: '#94A3B8',
}
function typeColor(t) { return typeColors[t] || '#64748B' }

function snrColor(snr) {
  if (snr > 10) return '#4ade80'
  if (snr > 0) return '#facc15'
  return '#f87171'
}

function stateColor(state) {
  if (state === 'online') return '#4ade80'
  if (state === 'offline') return '#6b7280'
  if (state === 'error') return '#ef4444'
  return '#94a3b8'
}

// ── Layout: position nodes radially around center ──
const IFACE_RADIUS = 160
const MESH_RADIUS = 300
const PEER_RADIUS = 280

const ifacePositions = computed(() => {
  const nodes = store.interfaceNodes.value || []
  const count = nodes.length || 1
  return nodes.map((n, i) => {
    const angle = (i / count) * Math.PI * 2 - Math.PI / 2
    return { ...n, x: Math.cos(angle) * IFACE_RADIUS, y: Math.sin(angle) * IFACE_RADIUS }
  })
})

const meshPositions = computed(() => {
  // Place mesh nodes around mesh_0 interface
  const meshIface = ifacePositions.value.find(n => n.id === 'mesh_0')
  if (!meshIface) return []
  const nodes = store.meshNodeList.value || []
  const count = nodes.length || 1
  const baseAngle = Math.atan2(meshIface.y, meshIface.x)
  return nodes.map((n, i) => {
    const spread = Math.PI * 0.6
    const angle = baseAngle - spread / 2 + (i / (count - 1 || 1)) * spread
    const r = MESH_RADIUS
    return { ...n, x: Math.cos(angle) * r, y: Math.sin(angle) * r }
  })
})

const peerPositions = computed(() => {
  // Place RNS peers around tcp_0 interface
  const tcpIface = ifacePositions.value.find(n => n.id?.startsWith('tcp'))
  const cx = tcpIface?.x || 150
  const cy = tcpIface?.y || -100
  const peers = store.peerNodes.value || []
  return peers.map((n, i) => {
    const offset = (i - (peers.length - 1) / 2) * 50
    return { ...n, x: cx + 120, y: cy + offset }
  })
})

// ── Edges ──
const ifaceEdges = computed(() => {
  return (ifacePositions.value || []).map(n => ({
    id: `bridge-${n.id}`, x1: 0, y1: 0, x2: n.x, y2: n.y,
    color: typeColor(n.channelType), label: '', type: 'bridge_iface',
  }))
})

const meshEdges = computed(() => {
  const meshIface = ifacePositions.value.find(n => n.id === 'mesh_0')
  if (!meshIface) return []
  return meshPositions.value.map(n => ({
    id: `mesh-${n.id}`, x1: meshIface.x, y1: meshIface.y, x2: n.x, y2: n.y,
    color: snrColor(n.snr), label: `${n.snr}dB`, type: 'mesh_link',
  }))
})

const peerEdges = computed(() => {
  const tcpIface = ifacePositions.value.find(n => n.id?.startsWith('tcp'))
  if (!tcpIface) return []
  return peerPositions.value.map(n => ({
    id: `peer-${n.id}`, x1: tcpIface.x, y1: tcpIface.y, x2: n.x, y2: n.y,
    color: n.connected ? '#64748B' : '#374151', label: '', type: 'peer_link',
  }))
})

// Access rule edges (interface → interface)
const ruleEdgePositions = computed(() => {
  return (store.ruleEdges.value || []).map(r => {
    const src = ifacePositions.value.find(n => n.id === r.source)
    const dst = ifacePositions.value.find(n => n.id === r.target)
    if (!src || !dst) return null
    // Offset to avoid overlapping the bridge↔interface edges
    const mx = (src.x + dst.x) / 2
    const my = (src.y + dst.y) / 2
    const nx = -(dst.y - src.y) * 0.15
    const ny = (dst.x - src.x) * 0.15
    return {
      ...r, x1: src.x, y1: src.y, x2: dst.x, y2: dst.y,
      cx: mx + nx, cy: my + ny, color: '#f97316',
    }
  }).filter(Boolean)
})

// Bond group visual brackets
const bondBrackets = computed(() => {
  return (store.bondGroups.value || []).map(g => {
    const members = (g.members || []).map(m => {
      const mid = typeof m === 'string' ? m : m.interface_id
      return ifacePositions.value.find(n => n.id === mid)
    }).filter(Boolean)
    if (members.length < 2) return null
    const xs = members.map(m => m.x)
    const ys = members.map(m => m.y)
    return {
      id: g.id, label: g.label || g.id,
      x: Math.min(...xs) - 30, y: Math.min(...ys) - 30,
      w: Math.max(...xs) - Math.min(...xs) + 60,
      h: Math.max(...ys) - Math.min(...ys) + 60,
    }
  }).filter(Boolean)
})

// ── Zoom/pan via mouse wheel + drag ──
function onWheel(e) {
  e.preventDefault()
  const factor = e.deltaY > 0 ? 1.1 : 0.9
  const newW = viewBox.value.w * factor
  const newH = viewBox.value.h * factor
  const dx = (newW - viewBox.value.w) / 2
  const dy = (newH - viewBox.value.h) / 2
  viewBox.value = { x: viewBox.value.x - dx, y: viewBox.value.y - dy, w: newW, h: newH }
  scale.value *= (1 / factor)
}

function onMouseDown(e) {
  dragging.value = true
  dragStart.value = { x: e.clientX, y: e.clientY }
}
function onMouseMove(e) {
  if (!dragging.value) return
  const svg = svgRef.value
  if (!svg) return
  const rect = svg.getBoundingClientRect()
  const sx = viewBox.value.w / rect.width
  const sy = viewBox.value.h / rect.height
  viewBox.value.x -= (e.clientX - dragStart.value.x) * sx
  viewBox.value.y -= (e.clientY - dragStart.value.y) * sy
  dragStart.value = { x: e.clientX, y: e.clientY }
}
function onMouseUp() { dragging.value = false }

const isSelected = (id) => store.selectedNodeId.value === id
const nodeOpacity = (id) => {
  if (!store.selectedNodeId.value) return 1
  return isSelected(id) ? 1 : 0.25
}
</script>

<template>
  <div class="h-full w-full overflow-hidden bg-gray-950 relative"
    @wheel.prevent="onWheel" @mousedown="onMouseDown"
    @mousemove="onMouseMove" @mouseup="onMouseUp" @mouseleave="onMouseUp">
    <svg ref="svgRef" class="w-full h-full" :viewBox="`${viewBox.x} ${viewBox.y} ${viewBox.w} ${viewBox.h}`">
      <!-- Grid -->
      <defs>
        <pattern id="obs-grid" width="50" height="50" patternUnits="userSpaceOnUse">
          <path d="M 50 0 L 0 0 0 50" fill="none" stroke="rgba(255,255,255,0.03)" stroke-width="0.5"/>
        </pattern>
      </defs>
      <rect :x="viewBox.x" :y="viewBox.y" :width="viewBox.w" :height="viewBox.h" fill="url(#obs-grid)"/>

      <!-- Bond group brackets -->
      <rect v-for="b in bondBrackets" :key="b.id"
        :x="b.x" :y="b.y" :width="b.w" :height="b.h" rx="8"
        fill="none" stroke="#0d9488" stroke-width="1" stroke-dasharray="6 3" opacity="0.4"/>
      <text v-for="b in bondBrackets" :key="'bl-'+b.id"
        :x="b.x + b.w/2" :y="b.y - 6" text-anchor="middle"
        fill="#0d9488" font-size="8" opacity="0.5" font-family="monospace">{{ b.label }}</text>

      <!-- Access rule edges (curved) -->
      <path v-for="r in ruleEdgePositions" :key="r.id"
        :d="`M${r.x1},${r.y1} Q${r.cx},${r.cy} ${r.x2},${r.y2}`"
        fill="none" :stroke="r.color" stroke-width="1.5" stroke-dasharray="4 2" opacity="0.5"
        @click="store.selectNode(r.source)"/>
      <text v-for="r in ruleEdgePositions" :key="'rl-'+r.id"
        :x="r.cx" :y="r.cy - 5" text-anchor="middle"
        fill="#f97316" font-size="7" opacity="0.6" font-family="monospace">{{ r.matchCount }}</text>

      <!-- Bridge↔Interface edges -->
      <line v-for="e in ifaceEdges" :key="e.id"
        :x1="e.x1" :y1="e.y1" :x2="e.x2" :y2="e.y2"
        :stroke="e.color" stroke-width="2" :opacity="nodeOpacity(e.id.replace('bridge-',''))" stroke-linecap="round"/>

      <!-- Mesh↔Node edges -->
      <line v-for="e in meshEdges" :key="e.id"
        :x1="e.x1" :y1="e.y1" :x2="e.x2" :y2="e.y2"
        :stroke="e.color" stroke-width="1" opacity="0.6" stroke-linecap="round"/>
      <text v-for="e in meshEdges" :key="'ml-'+e.id"
        :x="(e.x1+e.x2)/2" :y="(e.y1+e.y2)/2 - 4" text-anchor="middle"
        :fill="e.color" font-size="7" opacity="0.5" font-family="monospace">{{ e.label }}</text>

      <!-- TCP↔Peer edges -->
      <line v-for="e in peerEdges" :key="e.id"
        :x1="e.x1" :y1="e.y1" :x2="e.x2" :y2="e.y2"
        :stroke="e.color" stroke-width="1.5" stroke-dasharray="3 2" opacity="0.5"/>

      <!-- ═══ NODES ═══ -->

      <!-- Interface cards -->
      <g v-for="n in ifacePositions" :key="n.id"
        :transform="`translate(${n.x},${n.y})`"
        :opacity="nodeOpacity(n.id)"
        class="cursor-pointer" @click="store.selectNode(n.id)">
        <rect x="-40" y="-22" width="80" height="44" rx="6"
          :fill="typeColor(n.channelType)" fill-opacity="0.08"
          :stroke="typeColor(n.channelType)" stroke-width="1.5"
          :stroke-opacity="isSelected(n.id) ? 1 : 0.5"/>
        <text dy="-6" text-anchor="middle" :fill="typeColor(n.channelType)" font-size="9" font-weight="600" font-family="monospace">
          {{ n.id }}
        </text>
        <text dy="7" text-anchor="middle" fill="#9ca3af" font-size="7" font-family="monospace">
          {{ n.channelType }}
        </text>
        <!-- State dot -->
        <circle cx="30" cy="-14" r="3" :fill="stateColor(n.state)"/>
        <!-- Message counts -->
        <text dy="17" text-anchor="middle" fill="#6b7280" font-size="6" font-family="monospace">
          {{ n.messagesIn > 0 || n.messagesOut > 0 ? `↓${n.messagesIn} ↑${n.messagesOut}` : '' }}
        </text>
      </g>

      <!-- Mesh nodes -->
      <g v-for="n in meshPositions" :key="n.id"
        :transform="`translate(${n.x},${n.y})`"
        :opacity="nodeOpacity(n.id)"
        class="cursor-pointer" @click="store.selectNode(n.id)">
        <circle r="16" :fill="n.online ? '#059669' : '#374151'" fill-opacity="0.15"
          :stroke="n.online ? '#34d399' : '#4b5563'" stroke-width="1"/>
        <text dy="-2" text-anchor="middle" fill="#d1d5db" font-size="7" font-family="monospace">
          {{ n.label ? n.label.substring(0, 10) : n.id.substring(0, 8) }}
        </text>
        <text dy="8" text-anchor="middle" fill="#6b7280" font-size="6" font-family="monospace">
          {{ n.hwModel ? n.hwModel.substring(0, 10) : '' }}
        </text>
      </g>

      <!-- RNS peers -->
      <g v-for="n in peerPositions" :key="n.id"
        :transform="`translate(${n.x},${n.y})`" opacity="0.7">
        <rect x="-50" y="-12" width="100" height="24" rx="4"
          fill="#1e293b" stroke="#475569" stroke-width="1"/>
        <text dy="3" text-anchor="middle" fill="#94a3b8" font-size="7" font-family="monospace">
          {{ n.label.split(':')[0].split('.').pop() }}:{{ n.label.split(':')[1] || '' }}
        </text>
        <circle cx="40" cy="0" r="3" :fill="n.connected ? '#4ade80' : '#6b7280'"/>
      </g>

      <!-- Center bridge node -->
      <g transform="translate(0,0)">
        <rect x="-50" y="-28" width="100" height="56" rx="8"
          fill="#0d9488" fill-opacity="0.1" stroke="#0d9488" stroke-width="2"/>
        <text dy="-8" text-anchor="middle" fill="#14b8a6" font-size="11" font-weight="700" font-family="monospace">
          {{ store.bridgeStatus.value?.node_name || store.bridgeStatus.value?.node_id || 'bridge' }}
        </text>
        <text dy="6" text-anchor="middle" fill="#5eead4" font-size="8" font-family="monospace">
          {{ store.bridgeStatus.value?.hw_model_name || '' }}
        </text>
        <text dy="18" text-anchor="middle" fill="#6b7280" font-size="7" font-family="monospace">
          {{ store.meshNodeList.value?.length || 0 }} nodes
        </text>
      </g>
    </svg>
  </div>
</template>
