<script setup>
import { computed, ref, watch } from 'vue'
import { useObservabilityStore } from '../composables/useObservabilityStore'

const store = useObservabilityStore()

const svgRef = ref(null)
const dragging = ref(false)
const dragStart = ref({ x: 0, y: 0 })

// ── Design tokens (Hubble-inspired) ──
const typeColors = {
  mesh: '#06B6D4', iridium: '#A855F7', iridium_sbd: '#A855F7',
  iridium_imt: '#C084FC', cellular: '#F97316', sms: '#22C55E',
  astrocast: '#EAB308', zigbee: '#34D399', aprs: '#06B6D4',
  tcp: '#64748B', mqtt: '#64748B', webhook: '#94A3B8',
}
const mtuByType = {
  mesh: 237, iridium: 340, iridium_sbd: 340, iridium_imt: 102400,
  cellular: 160, sms: 160, astrocast: 160, zigbee: 100, aprs: 256,
  tcp: 65535, mqtt: 65535, webhook: 65535,
}
function typeColor(t) { return typeColors[t] || '#64748B' }
function mtuLabel(t) { const m = mtuByType[t]; return m ? (m >= 1024 ? `${(m/1024).toFixed(0)}KB` : `${m}B`) : '' }
function snrColor(snr) { return snr > 10 ? '#4ade80' : snr > 0 ? '#facc15' : '#f87171' }
function stateColor(s) { return s === 'online' ? '#34d399' : s === 'error' ? '#ef4444' : '#6b7280' }
function stateBg(s) { return s === 'online' ? '#059669' : s === 'error' ? '#7f1d1d' : '#374151' }

// ── Layout ──
const IFACE_RADIUS = 240
const MESH_RADIUS = 430
const PEER_OFFSET = 200
const CW = 190   // card width
const CH = 80    // card height
const CR = 10    // card radius
const MR = 24    // mesh node radius
const PW = 150   // peer card width
const PH = 32    // peer card height
const BW = 180   // bridge card width
const BH = 88    // bridge card height

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
  const mi = (ifacePositions.value || []).find(n => n.id === 'mesh_0')
  if (!mi) return []
  const nodes = store.meshNodeList.value || []
  const count = nodes.length || 1
  const baseAngle = Math.atan2(mi.y, mi.x)
  return nodes.map((n, i) => {
    const spread = Math.PI * 0.9
    const angle = baseAngle - spread / 2 + (i / (count - 1 || 1)) * spread
    return { ...n, x: Math.cos(angle) * MESH_RADIUS, y: Math.sin(angle) * MESH_RADIUS }
  })
})

const peerPositions = computed(() => {
  const ti = (ifacePositions.value || []).find(n => n.id?.startsWith('tcp'))
  const cx = ti?.x || 200
  const cy = ti?.y || -140
  const peers = store.peerNodes.value || []
  return peers.map((n, i) => ({
    ...n, x: cx + PEER_OFFSET, y: cy + (i - (peers.length - 1) / 2) * 65,
  }))
})

// ── Edges ──
const ifaceEdges = computed(() => (ifacePositions.value || []).map(n => ({
  id: `br-${n.id}`, x1: 0, y1: 0, x2: n.x, y2: n.y,
  color: typeColor(n.channelType),
})))

const meshEdges = computed(() => {
  const mi = (ifacePositions.value || []).find(n => n.id === 'mesh_0')
  if (!mi) return []
  return (meshPositions.value || []).map(n => ({
    id: `m-${n.id}`, x1: mi.x, y1: mi.y, x2: n.x, y2: n.y,
    color: snrColor(n.snr), label: n.snr ? `${n.snr}dB` : '',
  }))
})

const peerEdges = computed(() => {
  const ti = (ifacePositions.value || []).find(n => n.id?.startsWith('tcp'))
  if (!ti) return []
  return (peerPositions.value || []).map(n => ({
    id: `p-${n.id}`, x1: ti.x, y1: ti.y, x2: n.x, y2: n.y,
    color: n.connected ? '#94a3b8' : '#4b5563',
  }))
})

const ruleEdges = computed(() => {
  return (store.ruleEdges.value || []).map(r => {
    const s = (ifacePositions.value || []).find(n => n.id === r.source)
    const d = (ifacePositions.value || []).find(n => n.id === r.target)
    if (!s || !d) return null
    const mx = (s.x + d.x) / 2, my = (s.y + d.y) / 2
    const nx = -(d.y - s.y) * 0.25, ny = (d.x - s.x) * 0.25
    return { ...r, x1: s.x, y1: s.y, x2: d.x, y2: d.y, cx: mx + nx, cy: my + ny }
  }).filter(Boolean)
})

const bondBrackets = computed(() => {
  return (store.bondGroups.value || []).map(g => {
    const ms = (g.members || []).map(m => {
      const mid = typeof m === 'string' ? m : m.interface_id
      return (ifacePositions.value || []).find(n => n.id === mid)
    }).filter(Boolean)
    if (ms.length < 2) return null
    const xs = ms.map(m => m.x), ys = ms.map(m => m.y)
    return {
      id: g.id, label: g.label || g.id,
      x: Math.min(...xs) - 60, y: Math.min(...ys) - 60,
      w: Math.max(...xs) - Math.min(...xs) + 120, h: Math.max(...ys) - Math.min(...ys) + 120,
    }
  }).filter(Boolean)
})

// ── Auto-fit viewport (zoomed out) ──
const autoViewBox = computed(() => {
  const all = [
    ...(ifacePositions.value || []),
    ...(meshPositions.value || []),
    ...(peerPositions.value || []),
    { x: 0, y: 0 },
  ]
  if (all.length <= 1) return { x: -600, y: -500, w: 1200, h: 1000 }
  const xs = all.map(n => n.x), ys = all.map(n => n.y)
  const pad = 160
  return {
    x: Math.min(...xs) - pad - CW, y: Math.min(...ys) - pad - CH,
    w: Math.max(...xs) - Math.min(...xs) + 2 * pad + 2 * CW + PEER_OFFSET,
    h: Math.max(...ys) - Math.min(...ys) + 2 * pad + 2 * CH,
  }
})

const viewBox = ref(null)
watch(autoViewBox, (v) => { if (!dragging.value) viewBox.value = { ...v } }, { immediate: true })
const vb = computed(() => viewBox.value || autoViewBox.value)

// ── Zoom/pan ──
function onWheel(e) {
  e.preventDefault()
  const v = viewBox.value || { ...autoViewBox.value }
  const f = e.deltaY > 0 ? 1.1 : 0.9
  const nw = v.w * f, nh = v.h * f
  viewBox.value = { x: v.x - (nw - v.w) / 2, y: v.y - (nh - v.h) / 2, w: nw, h: nh }
}
function onMouseDown(e) { dragging.value = true; dragStart.value = { x: e.clientX, y: e.clientY } }
function onMouseMove(e) {
  if (!dragging.value) return
  const svg = svgRef.value; if (!svg) return
  const r = svg.getBoundingClientRect()
  const v = viewBox.value || { ...autoViewBox.value }
  v.x -= (e.clientX - dragStart.value.x) * (v.w / r.width)
  v.y -= (e.clientY - dragStart.value.y) * (v.h / r.height)
  viewBox.value = { ...v }
  dragStart.value = { x: e.clientX, y: e.clientY }
}
function onMouseUp() { dragging.value = false }

const isSel = (id) => store.selectedNodeId.value === id
const nOp = (id) => store.selectedNodeId.value ? (isSel(id) ? 1 : 0.15) : 1
</script>

<template>
  <div class="h-full w-full overflow-hidden relative" style="background: linear-gradient(180deg, #111827 0%, #0c1017 100%)"
    @wheel.prevent="onWheel" @mousedown="onMouseDown"
    @mousemove="onMouseMove" @mouseup="onMouseUp" @mouseleave="onMouseUp">
    <svg ref="svgRef" class="w-full h-full" :viewBox="`${vb.x} ${vb.y} ${vb.w} ${vb.h}`">
      <defs>
        <pattern id="dot-grid" width="30" height="30" patternUnits="userSpaceOnUse">
          <circle cx="15" cy="15" r="0.5" fill="rgba(255,255,255,0.04)"/>
        </pattern>
        <marker id="arr" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
          <polygon points="0 0, 8 3, 0 6" fill="#6b7280" opacity="0.5"/>
        </marker>
        <marker id="arr-o" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
          <polygon points="0 0, 8 3, 0 6" fill="#f97316" opacity="0.5"/>
        </marker>
        <filter id="glow" x="-30%" y="-30%" width="160%" height="160%">
          <feGaussianBlur in="SourceAlpha" stdDeviation="6"/>
          <feComponentTransfer><feFuncA type="linear" slope="0.12"/></feComponentTransfer>
          <feMerge><feMergeNode/><feMergeNode in="SourceGraphic"/></feMerge>
        </filter>
      </defs>
      <rect :x="vb.x" :y="vb.y" :width="vb.w" :height="vb.h" fill="url(#dot-grid)"/>

      <!-- Bond brackets -->
      <g v-for="b in bondBrackets" :key="b.id">
        <rect :x="b.x" :y="b.y" :width="b.w" :height="b.h" rx="14"
          fill="none" stroke="#0d9488" stroke-width="1.5" stroke-dasharray="8 4" opacity="0.25"/>
        <text :x="b.x + b.w/2" :y="b.y - 10" text-anchor="middle"
          fill="#0d9488" font-size="11" opacity="0.4">{{ b.label }}</text>
      </g>

      <!-- Rule edges (curved, orange, with arrow) -->
      <g v-for="r in ruleEdges" :key="r.id">
        <path :d="`M${r.x1},${r.y1} Q${r.cx},${r.cy} ${r.x2},${r.y2}`"
          fill="none" stroke="#f97316" stroke-width="2" stroke-dasharray="6 3" opacity="0.35"
          marker-end="url(#arr-o)" class="cursor-pointer" @click="store.selectNode(r.source)"/>
        <circle :cx="r.cx" :cy="r.cy" r="14" fill="#1e293b" stroke="#f97316" stroke-width="1" opacity="0.5"/>
        <text :x="r.cx" :y="r.cy + 4" text-anchor="middle" fill="#f97316" font-size="9" font-weight="600">
          {{ r.matchCount }}
        </text>
      </g>

      <!-- Bridge→Interface edges -->
      <line v-for="e in ifaceEdges" :key="e.id"
        :x1="e.x1" :y1="e.y1" :x2="e.x2" :y2="e.y2"
        :stroke="e.color" stroke-width="2" :opacity="nOp(e.id.replace('br-','')) * 0.5"
        stroke-linecap="round"/>
      <!-- Connection dots at card border -->
      <circle v-for="e in ifaceEdges" :key="'cd-'+e.id"
        :cx="e.x2" :cy="e.y2" r="4" :fill="e.color" :opacity="nOp(e.id.replace('br-','')) * 0.6"/>

      <!-- Mesh edges -->
      <g v-for="e in meshEdges" :key="e.id">
        <line :x1="e.x1" :y1="e.y1" :x2="e.x2" :y2="e.y2"
          :stroke="e.color" stroke-width="1.5" opacity="0.35"/>
        <text v-if="e.label" :x="(e.x1+e.x2)/2" :y="(e.y1+e.y2)/2 - 6" text-anchor="middle"
          :fill="e.color" font-size="9" opacity="0.7">{{ e.label }}</text>
      </g>

      <!-- Peer edges -->
      <line v-for="e in peerEdges" :key="e.id"
        :x1="e.x1" :y1="e.y1" :x2="e.x2" :y2="e.y2"
        :stroke="e.color" stroke-width="1.5" stroke-dasharray="5 3" opacity="0.35"
        marker-end="url(#arr)"/>

      <!-- ═══ INTERFACE CARDS (Hubble-style: light bg, accent bar, port info) ═══ -->
      <g v-for="n in ifacePositions" :key="n.id"
        :transform="`translate(${n.x},${n.y})`"
        :opacity="nOp(n.id)" filter="url(#glow)"
        class="cursor-pointer" @click="store.selectNode(n.id)">
        <!-- Card body — light semi-transparent background -->
        <rect :x="-CW/2" :y="-CH/2" :width="CW" :height="CH" :rx="CR"
          fill="rgba(30,41,59,0.92)" stroke="rgba(148,163,184,0.25)" stroke-width="1"/>
        <!-- Color accent bar (left edge, Hubble-style) -->
        <rect :x="-CW/2" :y="-CH/2" width="4" :height="CH" :rx="CR"
          :fill="typeColor(n.channelType)"/>
        <!-- State dot -->
        <circle :cx="CW/2 - 14" :cy="-CH/2 + 14" r="5" :fill="stateBg(n.state)" opacity="0.3"/>
        <circle :cx="CW/2 - 14" :cy="-CH/2 + 14" r="3.5" :fill="stateColor(n.state)"/>
        <!-- Interface name (large, like Hubble service name) -->
        <text :x="-CW/2 + 16" dy="-14" fill="#e2e8f0" font-size="13" font-weight="600">{{ n.id }}</text>
        <!-- Channel type -->
        <text :x="-CW/2 + 16" dy="4" fill="#94a3b8" font-size="10">{{ n.channelType }}</text>
        <!-- MTU + protocol info (like Hubble port rows) -->
        <text :x="-CW/2 + 16" dy="20" fill="#64748b" font-size="9">
          MTU {{ mtuLabel(n.channelType) }}{{ n.messagesIn > 0 || n.messagesOut > 0 ? ` · ↓${n.messagesIn} ↑${n.messagesOut}` : '' }}
        </text>
        <!-- Accent bottom line -->
        <line :x1="-CW/2 + 12" :y1="CH/2 - 1" :x2="CW/2 - 12" :y2="CH/2 - 1"
          :stroke="typeColor(n.channelType)" stroke-width="1" opacity="0.15"/>
      </g>

      <!-- ═══ MESH NODES ═══ -->
      <g v-for="n in meshPositions" :key="n.id"
        :transform="`translate(${n.x},${n.y})`" :opacity="nOp(n.id)"
        class="cursor-pointer" @click="store.selectNode(n.id)">
        <circle :r="MR" fill="rgba(30,41,59,0.85)"
          :stroke="n.online ? '#34d399' : '#475569'" stroke-width="1.5"/>
        <circle :r="MR+5" fill="none" :stroke="n.online ? '#059669' : 'transparent'" stroke-width="0.5" opacity="0.3"/>
        <text dy="-4" text-anchor="middle" fill="#d1d5db" font-size="10" font-weight="500">
          {{ n.label ? n.label.substring(0, 11) : n.id?.substring(0, 8) || '' }}
        </text>
        <text dy="10" text-anchor="middle" fill="#64748b" font-size="8">
          {{ n.hwModel ? n.hwModel.substring(0, 11) : '' }}
        </text>
      </g>

      <!-- ═══ RNS PEERS ═══ -->
      <g v-for="n in peerPositions" :key="n.id"
        :transform="`translate(${n.x},${n.y})`" opacity="0.85">
        <rect :x="-PW/2" :y="-PH/2" :width="PW" :height="PH" rx="6"
          fill="rgba(30,41,59,0.9)" stroke="rgba(148,163,184,0.2)" stroke-width="1"/>
        <rect :x="-PW/2" :y="-PH/2" width="3" :height="PH" rx="6"
          fill="#64748B"/>
        <text :x="-PW/2 + 14" dy="4" fill="#94a3b8" font-size="10">
          {{ n.label?.split(':')[0]?.split('.').pop() || '' }}:{{ n.label?.split(':')[1] || '' }}
        </text>
        <circle :cx="PW/2 - 12" cy="0" r="4" :fill="n.connected ? '#34d399' : '#6b7280'"/>
      </g>

      <!-- ═══ CENTER BRIDGE ═══ -->
      <g transform="translate(0,0)" filter="url(#glow)">
        <rect :x="-BW/2" :y="-BH/2" :width="BW" :height="BH" rx="12"
          fill="rgba(13,148,136,0.06)" stroke="#0d9488" stroke-width="2"/>
        <rect :x="-BW/2" :y="-BH/2" width="5" :height="BH" rx="12"
          fill="#0d9488" opacity="0.5"/>
        <text :x="-BW/2 + 20" dy="-20" fill="#14b8a6" font-size="14" font-weight="700">
          {{ store.bridgeStatus.value?.node_name || store.bridgeStatus.value?.node_id || 'MeshSat' }}
        </text>
        <text :x="-BW/2 + 20" dy="0" fill="#5eead4" font-size="11">
          {{ store.bridgeStatus.value?.hw_model_name || '' }}
        </text>
        <text :x="-BW/2 + 20" dy="18" fill="#6b7280" font-size="9">
          {{ store.meshNodeList.value?.length || 0 }} nodes · {{ (store.interfaceNodes.value || []).length }} interfaces
        </text>
        <text :x="-BW/2 + 20" dy="32" fill="#475569" font-size="8">
          {{ store.reticulumIdentity.value?.dest_hash?.substring(0, 16) || '' }}
        </text>
      </g>
    </svg>
  </div>
</template>
