<script setup>
import { computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()

const meshConnected = computed(() => store.status?.connected ?? false)
const nodeCount = computed(() => store.status?.num_nodes || 0)

const satBars = computed(() => store.iridiumSignal?.bars ?? -1)
const satColor = computed(() => {
  if (satBars.value < 0) return 'text-gray-600'
  if (satBars.value === 0) return 'text-red-400'
  if (satBars.value <= 2) return 'text-amber-400'
  return 'text-emerald-400'
})

const dlqCount = computed(() => {
  const iridium = (store.gateways || []).find(g => g.type === 'iridium')
  return iridium?.dlq_pending || 0
})
</script>

<template>
  <div class="bg-gray-900/90 border-b border-gray-800/80 px-4 py-1 flex items-center gap-4 text-[11px] z-50">
    <div class="flex items-center gap-1.5">
      <span class="w-1.5 h-1.5 rounded-full" :class="meshConnected ? 'bg-emerald-400' : 'bg-red-400'"></span>
      <span class="text-gray-500">Mesh</span>
      <span :class="meshConnected ? 'text-gray-300' : 'text-red-400'">{{ meshConnected ? `${nodeCount} nodes` : 'Offline' }}</span>
    </div>
    <div class="flex items-center gap-1.5">
      <div class="flex items-end gap-px h-3">
        <span v-for="i in 5" :key="i" class="w-[3px] rounded-[1px]"
          :class="satBars >= i ? (satBars <= 2 ? 'bg-amber-400' : 'bg-emerald-400') : 'bg-gray-700'"
          :style="{ height: `${3 + i * 2}px` }"></span>
      </div>
      <span class="text-gray-500">Sat</span>
      <span :class="satColor">{{ satBars >= 0 ? satBars + '/5' : '--' }}</span>
      <span v-if="dlqCount > 0" class="text-amber-400/80">{{ dlqCount }}q</span>
    </div>
  </div>
</template>
