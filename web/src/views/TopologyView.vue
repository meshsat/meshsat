<script setup>
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const svgRef = ref(null)
const width = ref(800)
const height = ref(600)

// Force simulation state
const positions = ref({}) // nodeId -> { x, y, vx, vy }
let animFrame = null
let settled = false
let pollTimer = null

const graph = computed(() => store.topology || { nodes: [], links: [], stats: {} })

const viewBox = computed(() => `0 0 ${width.value} ${height.value}`)

// Initialize positions for nodes that don't have one yet
function initPositions() {
  const g = graph.value
  if (!g.nodes) return
  const cx = width.value / 2
  const cy = height.value / 2
  const existing = { ...positions.value }
  const newPos = {}
  g.nodes.forEach((node, i) => {
    if (existing[node.id]) {
      newPos[node.id] = existing[node.id]
    } else {
      // Place in a circle initially
      const angle = (2 * Math.PI * i) / Math.max(g.nodes.length, 1)
      const radius = Math.min(width.value, height.value) * 0.3
      newPos[node.id] = {
        x: cx + radius * Math.cos(angle),
        y: cy + radius * Math.sin(angle),
        vx: 0,
        vy: 0
      }
    }
  })
  positions.value = newPos
  settled = false
}

// Force simulation step
function simulate() {
  const g = graph.value
  if (!g.nodes || g.nodes.length === 0) return

  const pos = { ...positions.value }
  const cx = width.value / 2
  const cy = height.value / 2
  const nodeIds = g.nodes.map(n => n.id)
  const damping = 0.85
  const repulsion = 5000
  const attraction = 0.005
  const centerGravity = 0.01

  let totalMovement = 0

  // Repulsion between all node pairs (Coulomb's law)
  for (let i = 0; i < nodeIds.length; i++) {
    for (let j = i + 1; j < nodeIds.length; j++) {
      const a = pos[nodeIds[i]]
      const b = pos[nodeIds[j]]
      if (!a || !b) continue
      const dx = a.x - b.x
      const dy = a.y - b.y
      const dist = Math.sqrt(dx * dx + dy * dy) || 1
      const force = repulsion / (dist * dist)
      const fx = (dx / dist) * force
      const fy = (dy / dist) * force
      a.vx += fx
      a.vy += fy
      b.vx -= fx
      b.vy -= fy
    }
  }

  // Attraction along links (Hooke's law)
  const idealLength = 120
  if (g.links) {
    for (const link of g.links) {
      const a = pos[link.source]
      const b = pos[link.target]
      if (!a || !b) continue
      const dx = b.x - a.x
      const dy = b.y - a.y
      const dist = Math.sqrt(dx * dx + dy * dy) || 1
      const force = (dist - idealLength) * attraction
      const fx = (dx / dist) * force
      const fy = (dy / dist) * force
      a.vx += fx
      a.vy += fy
      b.vx -= fx
      b.vy -= fy
    }
  }

  // Center gravity
  for (const id of nodeIds) {
    const p = pos[id]
    if (!p) continue
    p.vx += (cx - p.x) * centerGravity
    p.vy += (cy - p.y) * centerGravity
  }

  // Apply velocity and damping
  for (const id of nodeIds) {
    const p = pos[id]
    if (!p) continue
    p.vx *= damping
    p.vy *= damping
    p.x += p.vx
    p.y += p.vy
    // Constrain to bounds
    p.x = Math.max(30, Math.min(width.value - 30, p.x))
    p.y = Math.max(30, Math.min(height.value - 30, p.y))
    totalMovement += Math.abs(p.vx) + Math.abs(p.vy)
  }

  positions.value = pos

  // Stop simulation when settled
  if (totalMovement < 0.5) {
    settled = true
  }
}

function runSimulation() {
  if (!settled) {
    simulate()
  }
  animFrame = requestAnimationFrame(runSimulation)
}

function nodePos(id) {
  const p = positions.value[id]
  return p ? { x: p.x, y: p.y } : { x: width.value / 2, y: height.value / 2 }
}

function nodeColor(node) {
  return node.online ? '#4ade80' : '#666666'
}

function linkColor(snr) {
  if (snr > 10) return '#4ade80'
  if (snr > 5) return '#facc15'
  return '#ef4444'
}

function linkOpacity(snr) {
  if (snr > 10) return 0.7
  if (snr > 5) return 0.5
  return 0.4
}

function formatLastSeen(ts) {
  if (!ts) return 'never'
  const diff = Date.now() / 1000 - ts
  if (diff < 60) return 'now'
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

// Resize observer
function updateSize() {
  if (svgRef.value && svgRef.value.parentElement) {
    const rect = svgRef.value.parentElement.getBoundingClientRect()
    width.value = Math.max(400, rect.width)
    height.value = Math.max(400, rect.height - 60) // leave space for stats bar
  }
}

watch(graph, () => {
  initPositions()
}, { deep: true })

onMounted(async () => {
  await store.fetchTopology()
  await nextTick()
  updateSize()
  window.addEventListener('resize', updateSize)
  initPositions()
  runSimulation()
  // Poll every 30 seconds
  pollTimer = setInterval(() => {
    store.fetchTopology()
  }, 30000)
})

onUnmounted(() => {
  if (animFrame) cancelAnimationFrame(animFrame)
  if (pollTimer) clearInterval(pollTimer)
  window.removeEventListener('resize', updateSize)
})
</script>

<template>
  <div class="topology-view">
    <!-- Stats bar -->
    <div class="flex items-center gap-6 mb-3 px-1">
      <div class="flex items-center gap-2">
        <span class="text-xs text-gray-500 uppercase tracking-wider">Nodes</span>
        <span class="text-sm font-mono text-gray-200">
          {{ graph.stats?.online_nodes || 0 }}
          <span class="text-gray-500">/</span>
          {{ graph.stats?.total_nodes || 0 }}
        </span>
      </div>
      <div class="flex items-center gap-2">
        <span class="text-xs text-gray-500 uppercase tracking-wider">Links</span>
        <span class="text-sm font-mono text-gray-200">{{ graph.stats?.total_links || 0 }}</span>
      </div>
      <div class="flex items-center gap-2">
        <span class="text-xs text-gray-500 uppercase tracking-wider">Avg SNR</span>
        <span class="text-sm font-mono"
          :class="(graph.stats?.avg_snr || 0) >= 0 ? 'text-emerald-400' : (graph.stats?.avg_snr || 0) >= -10 ? 'text-amber-400' : 'text-red-400'">
          {{ (graph.stats?.avg_snr || 0).toFixed(1) }} dB
        </span>
      </div>
      <div class="ml-auto flex items-center gap-4 text-[10px] text-gray-600">
        <span class="flex items-center gap-1"><span class="w-2 h-2 rounded-full bg-emerald-400 inline-block"></span> Online</span>
        <span class="flex items-center gap-1"><span class="w-2 h-2 rounded-full bg-gray-500 inline-block"></span> Offline</span>
        <span class="flex items-center gap-1"><span class="w-3 h-0.5 bg-emerald-400 inline-block"></span> Good SNR</span>
        <span class="flex items-center gap-1"><span class="w-3 h-0.5 bg-yellow-400 inline-block"></span> Medium</span>
        <span class="flex items-center gap-1"><span class="w-3 h-0.5 bg-red-400 inline-block"></span> Poor</span>
      </div>
    </div>

    <!-- SVG Graph -->
    <div class="bg-tactical-surface/50 rounded border border-tactical-border relative" style="min-height: 500px;">
      <svg ref="svgRef" class="w-full" :viewBox="viewBox" style="min-height: 500px;">
        <!-- Grid pattern for background -->
        <defs>
          <pattern id="grid" width="40" height="40" patternUnits="userSpaceOnUse">
            <path d="M 40 0 L 0 0 0 40" fill="none" stroke="rgba(255,255,255,0.03)" stroke-width="1" />
          </pattern>
        </defs>
        <rect width="100%" height="100%" fill="url(#grid)" />

        <!-- Links -->
        <line v-for="link in (graph.links || [])" :key="link.source + '-' + link.target"
          :x1="nodePos(link.source).x" :y1="nodePos(link.source).y"
          :x2="nodePos(link.target).x" :y2="nodePos(link.target).y"
          :stroke="linkColor(link.snr)" stroke-width="1.5" :opacity="linkOpacity(link.snr)"
          stroke-dasharray="4,2" />

        <!-- Link SNR labels -->
        <text v-for="link in (graph.links || [])" :key="'lbl-' + link.source + '-' + link.target"
          :x="(nodePos(link.source).x + nodePos(link.target).x) / 2"
          :y="(nodePos(link.source).y + nodePos(link.target).y) / 2 - 6"
          text-anchor="middle" :fill="linkColor(link.snr)" font-size="8" opacity="0.7">
          {{ link.snr.toFixed(1) }}dB
          <template v-if="link.hops > 0"> ({{ link.hops }}h)</template>
        </text>

        <!-- Nodes -->
        <g v-for="node in (graph.nodes || [])" :key="node.id"
          :transform="`translate(${nodePos(node.id).x},${nodePos(node.id).y})`">
          <!-- Glow for online nodes -->
          <circle v-if="node.online" r="18" :fill="nodeColor(node)" opacity="0.1" />
          <!-- Node circle -->
          <circle r="12" :fill="nodeColor(node)" stroke="#1a1a2e" stroke-width="2" opacity="0.9" />
          <!-- Label below -->
          <text dy="28" text-anchor="middle" fill="#ccc" font-size="10" font-weight="500">
            {{ node.label || node.id }}
          </text>
          <!-- Battery above -->
          <text dy="-20" text-anchor="middle" fill="#888" font-size="8">
            <template v-if="node.battery > 0">{{ node.battery }}%</template>
          </text>
          <!-- SNR to the right -->
          <text dx="18" dy="4" fill="#666" font-size="8" font-family="monospace">
            {{ node.snr.toFixed(0) }}dB
          </text>
        </g>

        <!-- Empty state -->
        <text v-if="!graph.nodes || graph.nodes.length === 0"
          :x="width / 2" :y="height / 2" text-anchor="middle" fill="#555" font-size="14">
          No mesh nodes detected
        </text>
      </svg>
    </div>

    <!-- Node detail table -->
    <div v-if="graph.nodes && graph.nodes.length > 0" class="mt-4">
      <h3 class="text-xs text-gray-500 uppercase tracking-wider mb-2">Node Details</h3>
      <div class="overflow-x-auto">
        <table class="w-full text-xs">
          <thead>
            <tr class="text-gray-500 border-b border-tactical-border">
              <th class="text-left py-1.5 px-2 font-medium">Status</th>
              <th class="text-left py-1.5 px-2 font-medium">ID</th>
              <th class="text-left py-1.5 px-2 font-medium">Name</th>
              <th class="text-right py-1.5 px-2 font-medium">SNR</th>
              <th class="text-right py-1.5 px-2 font-medium">Battery</th>
              <th class="text-right py-1.5 px-2 font-medium">Last Seen</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="node in graph.nodes" :key="'tbl-' + node.id"
              class="border-b border-tactical-border/30 hover:bg-white/[0.02]">
              <td class="py-1.5 px-2">
                <span class="w-2 h-2 rounded-full inline-block" :class="node.online ? 'bg-emerald-400' : 'bg-gray-600'"></span>
              </td>
              <td class="py-1.5 px-2 font-mono text-gray-400">{{ node.id }}</td>
              <td class="py-1.5 px-2 text-gray-200">{{ node.label || '-' }}</td>
              <td class="py-1.5 px-2 text-right font-mono"
                :class="node.snr >= 0 ? 'text-emerald-400' : node.snr >= -10 ? 'text-amber-400' : 'text-red-400'">
                {{ node.snr.toFixed(1) }}
              </td>
              <td class="py-1.5 px-2 text-right font-mono"
                :class="node.battery > 50 ? 'text-emerald-400' : node.battery > 20 ? 'text-amber-400' : 'text-red-400'">
                {{ node.battery > 0 ? node.battery + '%' : '-' }}
              </td>
              <td class="py-1.5 px-2 text-right text-gray-500">{{ formatLastSeen(node.last_seen) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.topology-view {
  max-width: 1200px;
}
</style>
