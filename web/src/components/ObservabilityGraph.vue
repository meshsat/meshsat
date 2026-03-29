<script setup>
import { computed, ref, watch } from 'vue'
import { useObservabilityStore } from '../composables/useObservabilityStore'

const store = useObservabilityStore()

const svgRef = ref(null)
const dragging = ref(false)
const dragStart = ref({ x: 0, y: 0 })

// ── Colors ──
const typeColors = {
  mesh: '#06B6D4', iridium: '#A855F7', iridium_sbd: '#A855F7',
  iridium_imt: '#C084FC', cellular: '#F97316', sms: '#22C55E',
  astrocast: '#EAB308', zigbee: '#34D399', aprs: '#06B6D4',
  tcp: '#64748B', mqtt: '#64748B', webhook: '#94A3B8',
}
function typeColor(t) { return typeColors[t] || '#64748B' }
function snrColor(snr) { return snr > 10 ? '#4ade80' : snr > 0 ? '#facc15' : '#f87171' }
function stateColor(s) { return s === 'online' ? '#4ade80' : s === 'error' ? '#ef4444' : '#6b7280' }

// ── Layout constants (larger, more spaced) ──
const IFACE_RADIUS = 220
const MESH_RADIUS = 400
const PEER_OFFSET = 160
const CARD_W = 140
const CARD_H = 64
const CARD_R = 10
const MESH_R = 22
const PEER_W = 130
const PEER_H = 28

// ── Node positions ──
const ifacePositions = computed(() => {
  const nodes = store.interfaceNodes.value || []
  const count = nodes.length || 1
  return nodes.map((n, i) => {
    const angle = (i / count) * Math.PI * 2 - Math.PI / 2
    return { ...n, x: Math.cos(angle) * IFACE_RADIUS, y: Math.sin(angle) * IFACE_RADIUS }
  })
})

const meshPositions = computed(() => {
  const meshIface = (ifacePositions.value || []).find(n => n.id === 'mesh_0')
  if (!meshIface) return []
  const nodes = store.meshNodeList.value || []
  const count = nodes.length || 1
  const baseAngle = Math.atan2(meshIface.y, meshIface.x)
  return nodes.map((n, i) => {
    const spread = Math.PI * 0.8
    const angle = baseAngle - spread / 2 + (i / (count - 1 || 1)) * spread
    return { ...n, x: Math.cos(angle) * MESH_RADIUS, y: Math.sin(angle) * MESH_RADIUS }
  })
})

const peerPositions = computed(() => {
  const tcpIface = (ifacePositions.value || []).find(n => n.id?.startsWith('tcp'))
  const cx = tcpIface?.x || 180
  const cy = tcpIface?.y || -120
  const peers = store.peerNodes.value || []
  return peers.map((n, i) => {
    const offset = (i - (peers.length - 1) / 2) * 60
    return { ...n, x: cx + PEER_OFFSET, y: cy + offset }
  })
})

// ── Edge path helper (smooth curve) ──
function edgePath(x1, y1, x2, y2) {
  const mx = (x1 + x2) / 2
  const my = (y1 + y2) / 2
  return `M${x1},${y1} L${x2},${y2}`
}

// ── Edges ──
const ifaceEdges = computed(() => (ifacePositions.value || []).map(n => ({
  id: `bridge-${n.id}`, x1: 0, y1: 0, x2: n.x, y2: n.y,
  color: typeColor(n.channelType), type: 'bridge_iface',
})))

const meshEdges = computed(() => {
  const mi = (ifacePositions.value || []).find(n => n.id === 'mesh_0')
  if (!mi) return []
  return (meshPositions.value || []).map(n => ({
    id: `mesh-${n.id}`, x1: mi.x, y1: mi.y, x2: n.x, y2: n.y,
    color: snrColor(n.snr), label: n.snr ? `${n.snr}dB` : '', type: 'mesh_link',
  }))
})

const peerEdges = computed(() => {
  const ti = (ifacePositions.value || []).find(n => n.id?.startsWith('tcp'))
  if (!ti) return []
  return (peerPositions.value || []).map(n => ({
    id: `peer-${n.id}`, x1: ti.x, y1: ti.y, x2: n.x, y2: n.y,
    color: n.connected ? '#64748B' : '#374151', type: 'peer_link',
  }))
})

const ruleEdgePositions = computed(() => {
  return (store.ruleEdges.value || []).map(r => {
    const src = (ifacePositions.value || []).find(n => n.id === r.source)
    const dst = (ifacePositions.value || []).find(n => n.id === r.target)
    if (!src || !dst) return null
    const mx = (src.x + dst.x) / 2
    const my = (src.y + dst.y) / 2
    const nx = -(dst.y - src.y) * 0.2
    const ny = (dst.x - src.x) * 0.2
    return { ...r, x1: src.x, y1: src.y, x2: dst.x, y2: dst.y, cx: mx + nx, cy: my + ny }
  }).filter(Boolean)
})

const bondBrackets = computed(() => {
  return (store.bondGroups.value || []).map(g => {
    const members = (g.members || []).map(m => {
      const mid = typeof m === 'string' ? m : m.interface_id
      return (ifacePositions.value || []).find(n => n.id === mid)
    }).filter(Boolean)
    if (members.length < 2) return null
    const xs = members.map(m => m.x)
    const ys = members.map(m => m.y)
    return {
      id: g.id, label: g.label || g.id,
      x: Math.min(...xs) - 50, y: Math.min(...ys) - 50,
      w: Math.max(...xs) - Math.min(...xs) + 100, h: Math.max(...ys) - Math.min(...ys) + 100,
    }
  }).filter(Boolean)
})

// ── Auto-fit viewport ──
const autoViewBox = computed(() => {
  const all = [
    ...(ifacePositions.value || []),
    ...(meshPositions.value || []),
    ...(peerPositions.value || []),
    { x: 0, y: 0 }, // center bridge
  ]
  if (all.length <= 1) return { x: -500, y: -400, w: 1000, h: 800 }
  const xs = all.map(n => n.x)
  const ys = all.map(n => n.y)
  const pad = 120
  const minX = Math.min(...xs) - pad - CARD_W / 2
  const minY = Math.min(...ys) - pad - CARD_H / 2
  const maxX = Math.max(...xs) + pad + CARD_W / 2 + PEER_OFFSET
  const maxY = Math.max(...ys) + pad + CARD_H / 2
  return { x: minX, y: minY, w: maxX - minX, h: maxY - minY }
})

const viewBox = ref(null)
watch(autoViewBox, (v) => { if (!dragging.value) viewBox.value = { ...v } }, { immediate: true })

const vb = computed(() => viewBox.value || autoViewBox.value)

// ── Zoom/pan ──
function onWheel(e) {
  e.preventDefault()
  const v = viewBox.value || { ...autoViewBox.value }
  const factor = e.deltaY > 0 ? 1.1 : 0.9
  const nw = v.w * factor, nh = v.h * factor
  viewBox.value = { x: v.x - (nw - v.w) / 2, y: v.y - (nh - v.h) / 2, w: nw, h: nh }
}
function onMouseDown(e) { dragging.value = true; dragStart.value = { x: e.clientX, y: e.clientY } }
function onMouseMove(e) {
  if (!dragging.value) return
  const svg = svgRef.value
  if (!svg) return
  const r = svg.getBoundingClientRect()
  const v = viewBox.value || { ...autoViewBox.value }
  v.x -= (e.clientX - dragStart.value.x) * (v.w / r.width)
  v.y -= (e.clientY - dragStart.value.y) * (v.h / r.height)
  viewBox.value = { ...v }
  dragStart.value = { x: e.clientX, y: e.clientY }
}
function onMouseUp() { dragging.value = false }

const isSelected = (id) => store.selectedNodeId.value === id
const nodeOpacity = (id) => store.selectedNodeId.value ? (isSelected(id) ? 1 : 0.2) : 1
</script>

<template>
  <div class="h-full w-full overflow-hidden relative" style="background: linear-gradient(180deg, #0f1419 0%, #0a0e12 100%)"
    @wheel.prevent="onWheel" @mousedown="onMouseDown"
    @mousemove="onMouseMove" @mouseup="onMouseUp" @mouseleave="onMouseUp">
    <svg ref="svgRef" class="w-full h-full" :viewBox="`${vb.x} ${vb.y} ${vb.w} ${vb.h}`">
      <defs>
        <!-- Subtle dot grid -->
        <pattern id="dot-grid" width="30" height="30" patternUnits="userSpaceOnUse">
          <circle cx="15" cy="15" r="0.5" fill="rgba(255,255,255,0.06)"/>
        </pattern>
        <!-- Arrow markers -->
        <marker id="arrow-gray" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
          <polygon points="0 0, 8 3, 0 6" fill="#4b5563" opacity="0.6"/>
        </marker>
        <marker id="arrow-orange" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
          <polygon points="0 0, 8 3, 0 6" fill="#f97316" opacity="0.6"/>
        </marker>
        <!-- Card shadow filter -->
        <filter id="card-glow" x="-20%" y="-20%" width="140%" height="140%">
          <feGaussianBlur in="SourceAlpha" stdDeviation="4"/>
          <feComponentTransfer><feFuncA type="linear" slope="0.15"/></feComponentTransfer>
          <feMerge><feMergeNode/><feMergeNode in="SourceGraphic"/></feMerge>
        </filter>
      </defs>
      <rect :x="vb.x" :y="vb.y" :width="vb.w" :height="vb.h" fill="url(#dot-grid)"/>

      <!-- Bond group brackets -->
      <g v-for="b in bondBrackets" :key="b.id">
        <rect :x="b.x" :y="b.y" :width="b.w" :height="b.h" rx="12"
          fill="none" stroke="#0d9488" stroke-width="1.5" stroke-dasharray="8 4" opacity="0.3"/>
        <text :x="b.x + b.w/2" :y="b.y - 8" text-anchor="middle"
          fill="#0d9488" font-size="10" opacity="0.5">{{ b.label }}</text>
      </g>

      <!-- Access rule edges (curved, with arrow) -->
      <g v-for="r in ruleEdgePositions" :key="r.id">
        <path :d="`M${r.x1},${r.y1} Q${r.cx},${r.cy} ${r.x2},${r.y2}`"
          fill="none" stroke="#f97316" stroke-width="1.5" stroke-dasharray="6 3" opacity="0.4"
          marker-end="url(#arrow-orange)" class="cursor-pointer" @click="store.selectNode(r.source)"/>
        <text :x="r.cx" :y="r.cy - 8" text-anchor="middle"
          fill="#f97316" font-size="9" opacity="0.6">{{ r.matchCount }}x</text>
      </g>

      <!-- Bridge↔Interface edges -->
      <line v-for="e in ifaceEdges" :key="e.id"
        :x1="e.x1" :y1="e.y1" :x2="e.x2" :y2="e.y2"
        :stroke="e.color" stroke-width="2.5" :opacity="nodeOpacity(e.id.replace('bridge-','')) * 0.6"
        stroke-linecap="round" marker-end="url(#arrow-gray)"/>

      <!-- Mesh↔Node edges -->
      <g v-for="e in meshEdges" :key="e.id">
        <line :x1="e.x1" :y1="e.y1" :x2="e.x2" :y2="e.y2"
          :stroke="e.color" stroke-width="1.5" opacity="0.4" stroke-linecap="round"/>
        <text v-if="e.label" :x="(e.x1+e.x2)/2" :y="(e.y1+e.y2)/2 - 6" text-anchor="middle"
          :fill="e.color" font-size="8" opacity="0.6">{{ e.label }}</text>
      </g>

      <!-- TCP↔Peer edges -->
      <line v-for="e in peerEdges" :key="e.id"
        :x1="e.x1" :y1="e.y1" :x2="e.x2" :y2="e.y2"
        :stroke="e.color" stroke-width="1.5" stroke-dasharray="4 3" opacity="0.4"
        marker-end="url(#arrow-gray)"/>

      <!-- ═══ INTERFACE CARDS ═══ -->
      <g v-for="n in ifacePositions" :key="n.id"
        :transform="`translate(${n.x},${n.y})`"
        :opacity="nodeOpacity(n.id)"
        filter="url(#card-glow)"
        class="cursor-pointer" @click="store.selectNode(n.id)">
        <!-- Card background -->
        <rect :x="-CARD_W/2" :y="-CARD_H/2" :width="CARD_W" :height="CARD_H" :rx="CARD_R"
          fill="#1a1f2e" :stroke="typeColor(n.channelType)"
          :stroke-width="isSelected(n.id) ? 2.5 : 1.5" :stroke-opacity="isSelected(n.id) ? 1 : 0.4"/>
        <!-- Color accent bar (top) -->
        <rect :x="-CARD_W/2" :y="-CARD_H/2" :width="CARD_W" height="4" :rx="CARD_R"
          :fill="typeColor(n.channelType)" opacity="0.6"/>
        <!-- Interface name -->
        <text dy="-8" text-anchor="middle" fill="#e2e8f0" font-size="12" font-weight="600">
          {{ n.id }}
        </text>
        <!-- Channel type -->
        <text dy="8" text-anchor="middle" fill="#64748b" font-size="9">
          {{ n.channelType }}
        </text>
        <!-- Message counts -->
        <text dy="22" text-anchor="middle" fill="#475569" font-size="8">
          {{ n.messagesIn > 0 || n.messagesOut > 0 ? `↓${n.messagesIn} ↑${n.messagesOut}` : '' }}
        </text>
        <!-- State indicator (double ring) -->
        <circle :cx="CARD_W/2 - 10" :cy="-CARD_H/2 + 10" r="6" :fill="stateColor(n.state)" opacity="0.2"/>
        <circle :cx="CARD_W/2 - 10" :cy="-CARD_H/2 + 10" r="4" :fill="stateColor(n.state)"/>
      </g>

      <!-- ═══ MESH NODES ═══ -->
      <g v-for="n in meshPositions" :key="n.id"
        :transform="`translate(${n.x},${n.y})`"
        :opacity="nodeOpacity(n.id)"
        class="cursor-pointer" @click="store.selectNode(n.id)">
        <circle :r="MESH_R" :fill="n.online ? '#059669' : '#1e293b'" fill-opacity="0.2"
          :stroke="n.online ? '#34d399' : '#475569'" stroke-width="1.5"/>
        <text dy="-4" text-anchor="middle" fill="#d1d5db" font-size="9" font-weight="500">
          {{ n.label ? n.label.substring(0, 12) : n.id?.substring(0, 8) || '' }}
        </text>
        <text dy="9" text-anchor="middle" fill="#64748b" font-size="7">
          {{ n.hwModel ? n.hwModel.substring(0, 12) : '' }}
        </text>
      </g>

      <!-- ═══ RNS PEERS ═══ -->
      <g v-for="n in peerPositions" :key="n.id"
        :transform="`translate(${n.x},${n.y})`" opacity="0.8">
        <rect :x="-PEER_W/2" :y="-PEER_H/2" :width="PEER_W" :height="PEER_H" rx="6"
          fill="#1e293b" stroke="#334155" stroke-width="1"/>
        <text dy="4" text-anchor="middle" fill="#94a3b8" font-size="9">
          {{ n.label?.split(':')[0]?.split('.').pop() || '' }}:{{ n.label?.split(':')[1] || '' }}
        </text>
        <!-- Connected indicator -->
        <circle :cx="PEER_W/2 - 10" cy="0" r="4" :fill="n.connected ? '#4ade80' : '#6b7280'"/>
      </g>

      <!-- ═══ CENTER BRIDGE ═══ -->
      <g transform="translate(0,0)">
        <rect x="-70" y="-36" width="140" height="72" rx="12"
          fill="#0d9488" fill-opacity="0.08" stroke="#0d9488" stroke-width="2.5"/>
        <!-- Accent bar -->
        <rect x="-70" y="-36" width="140" height="5" rx="12"
          fill="#0d9488" opacity="0.4"/>
        <text dy="-10" text-anchor="middle" fill="#14b8a6" font-size="13" font-weight="700">
          {{ store.bridgeStatus.value?.node_name || store.bridgeStatus.value?.node_id || 'bridge' }}
        </text>
        <text dy="6" text-anchor="middle" fill="#5eead4" font-size="10">
          {{ store.bridgeStatus.value?.hw_model_name || '' }}
        </text>
        <text dy="22" text-anchor="middle" fill="#6b7280" font-size="9">
          {{ store.meshNodeList.value?.length || 0 }} nodes · {{ (store.interfaceNodes.value || []).length }} ifaces
        </text>
      </g>
    </svg>
  </div>
</template>
