<script setup>
import { computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()

const meshStatus = computed(() => {
  if (!store.status) return { color: 'text-gray-500', label: 'Unknown' }
  return store.status.connected
    ? { color: 'text-emerald-400', label: `Connected (${store.status.num_nodes || 0} nodes)` }
    : { color: 'text-red-400', label: 'Disconnected' }
})

const satStatus = computed(() => {
  const sig = store.iridiumSignal
  if (!sig) return { color: 'text-gray-500', label: 'N/A', bars: 0 }
  const colors = ['text-red-400', 'text-red-400', 'text-amber-400', 'text-amber-400', 'text-emerald-400', 'text-emerald-400']
  return {
    color: colors[sig.bars] || 'text-gray-500',
    label: `${sig.bars} bar${sig.bars !== 1 ? 's' : ''}`,
    bars: sig.bars
  }
})

const dlqCount = computed(() => {
  const iridium = (store.gateways || []).find(g => g.type === 'iridium')
  return iridium?.dlq_pending || 0
})
</script>

<template>
  <div class="bg-gray-900/80 border-b border-gray-800 px-4 py-1.5 flex items-center gap-4 text-xs flex-wrap z-50">
    <!-- Mesh -->
    <div class="flex items-center gap-1.5">
      <span class="w-2 h-2 rounded-full" :class="meshStatus.color.replace('text-', 'bg-')"></span>
      <span class="text-gray-400">Mesh:</span>
      <span :class="meshStatus.color">{{ meshStatus.label }}</span>
    </div>

    <span class="text-gray-700">|</span>

    <!-- Satellite -->
    <div class="flex items-center gap-1.5">
      <span class="w-2 h-2 rounded-full" :class="satStatus.color.replace('text-', 'bg-')"></span>
      <span class="text-gray-400">Sat:</span>
      <span :class="satStatus.color">{{ satStatus.label }}</span>
      <span v-if="dlqCount > 0" class="text-amber-400">({{ dlqCount }} queued)</span>
    </div>
  </div>
</template>
