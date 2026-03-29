<script setup>
import { computed, ref } from 'vue'
import { useHeMBStore } from '../composables/useHeMBStore'
import { useHeMBSelection } from '../composables/useHeMBSelection'

const store = useHeMBStore()
const selection = useHeMBSelection()

const width = 600
const height = 300
const cx = width / 2
const cy = height / 2

const bearerColors = {
  mesh: '#06B6D4',
  iridium_sbd: '#A855F7',
  iridium: '#A855F7',
  iridium_imt: '#A855F7',
  cellular: '#F97316',
  sms: '#22C55E',
  astrocast: '#EAB308',
  aprs: '#06B6D4',
  zigbee: '#06B6D4',
  ipougrs: '#94A3B8',
  tcp: '#64748B',
  mqtt: '#64748B',
}

function bearerColor(type) {
  return bearerColors[type] || '#64748B'
}

const groups = computed(() => store.topology.value?.groups || [])

const hasData = computed(() => groups.value.length > 0)

// Build edges from bond group members.
const edges = computed(() => {
  const result = []
  for (const group of groups.value) {
    const members = group.members || []
    for (let i = 0; i < members.length; i++) {
      const angle = (i / members.length) * Math.PI * 2 - Math.PI / 2
      const r = Math.min(width, height) * 0.3
      const ex = cx + Math.cos(angle) * r
      const ey = cy + Math.sin(angle) * r
      result.push({
        id: `${group.id}-${members[i]}`,
        bearerId: members[i],
        channelType: members[i].replace(/_\d+$/, ''),
        x1: cx, y1: cy,
        x2: ex, y2: ey,
        label: members[i],
      })
    }
  }
  return result
})

// Bearer nodes positioned radially around center.
const bearerNodes = computed(() => {
  const allMembers = new Set()
  for (const group of groups.value) {
    for (const m of (group.members || [])) {
      allMembers.add(m)
    }
  }
  const members = [...allMembers]
  return members.map((m, i) => {
    const angle = (i / members.length) * Math.PI * 2 - Math.PI / 2
    const r = Math.min(width, height) * 0.3
    return {
      id: m,
      label: m,
      type: m.replace(/_\d+$/, ''),
      x: cx + Math.cos(angle) * r,
      y: cy + Math.sin(angle) * r,
    }
  })
})
</script>

<template>
  <div class="w-full h-full flex items-center justify-center">
    <svg :viewBox="`0 0 ${width} ${height}`" class="w-full h-full max-h-full">
      <!-- Grid background -->
      <defs>
        <pattern id="hemb-grid" width="40" height="40" patternUnits="userSpaceOnUse">
          <path d="M 40 0 L 0 0 0 40" fill="none" stroke="rgba(255,255,255,0.03)" stroke-width="1"/>
        </pattern>
      </defs>
      <rect width="100%" height="100%" fill="url(#hemb-grid)"/>

      <!-- Edges -->
      <line v-for="edge in edges" :key="edge.id"
        :x1="edge.x1" :y1="edge.y1" :x2="edge.x2" :y2="edge.y2"
        :stroke="bearerColor(edge.channelType)"
        :stroke-width="3"
        :stroke-opacity="selection.edgeOpacity(edge.id)"
        stroke-linecap="round"
        class="cursor-pointer"
        @click="selection.selectEdge(edge)"
      />

      <!-- Bearer nodes -->
      <g v-for="node in bearerNodes" :key="node.id"
        :transform="`translate(${node.x},${node.y})`"
        class="cursor-pointer"
        @click="selection.selectNode(node)">
        <circle r="22" :fill="bearerColor(node.type)" fill-opacity="0.1"
          :stroke="bearerColor(node.type)" stroke-width="1.5"
          :stroke-opacity="selection.isNodeSelected(node.id) ? 1 : 0.6"/>
        <text dy="1" text-anchor="middle" fill="#ccc" font-size="8"
          font-family="JetBrains Mono, monospace">
          {{ node.label.replace(/_0$/, '') }}
        </text>
      </g>

      <!-- Center node (local bridge) -->
      <g :transform="`translate(${cx},${cy})`">
        <circle r="28" fill="#0D9488" fill-opacity="0.15" stroke="#0D9488" stroke-width="2"/>
        <text dy="1" text-anchor="middle" fill="#14B8A6" font-size="10"
          font-weight="600" font-family="JetBrains Mono, monospace">
          {{ store.topology?.local?.id || 'bridge' }}
        </text>
        <text dy="14" text-anchor="middle" fill="#666" font-size="7"
          font-family="JetBrains Mono, monospace">
          HeMB
        </text>
      </g>

      <!-- Empty state -->
      <text v-if="!hasData" :x="cx" :y="cy + 50" text-anchor="middle"
        fill="#555" font-size="12" font-family="JetBrains Mono, monospace">
        No bond groups configured
      </text>
    </svg>
  </div>
</template>
