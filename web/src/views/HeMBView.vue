<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { useHeMBStore } from '../composables/useHeMBStore'
import { useHeMBSelection } from '../composables/useHeMBSelection'
import HeMBBearerGraph from '../components/HeMBBearerGraph.vue'
import HeMBFlowTable from '../components/HeMBFlowTable.vue'

const store = useHeMBStore()
const selection = useHeMBSelection()
const graphHeight = ref(40) // percentage

let pollInterval = null

onMounted(() => {
  store.fetchTopology()
  store.fetchStats()
  store.fetchHistory(100)
  store.connectSSE()
  pollInterval = setInterval(() => {
    store.fetchTopology()
    store.fetchStats()
  }, 10000)
})

onUnmounted(() => {
  store.closeSSE()
  if (pollInterval) clearInterval(pollInterval)
})
</script>

<template>
  <div class="flex flex-col h-full">
    <!-- Stats bar -->
    <div class="flex items-center gap-6 px-4 py-2 border-b border-gray-800 text-xs">
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Bearers</span>
        <span class="font-mono text-gray-200">{{ store.topology?.groups?.length ?? 0 }}</span>
      </div>
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Streams</span>
        <span class="font-mono text-gray-200">{{ store.stats?.active_streams ?? 0 }}</span>
      </div>
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Decoded</span>
        <span class="font-mono text-emerald-400">{{ store.stats?.generations_decoded ?? 0 }}</span>
      </div>
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Failed</span>
        <span class="font-mono text-red-400">{{ store.stats?.generations_failed ?? 0 }}</span>
      </div>
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Cost</span>
        <span class="font-mono text-gray-200">${{ (store.stats?.cost_incurred ?? 0).toFixed(3) }}</span>
      </div>
      <div class="ml-auto flex items-center gap-2">
        <div class="w-2 h-2 rounded-full" :class="store.connected ? 'bg-emerald-400' : 'bg-red-400'"></div>
        <span class="text-gray-500">{{ store.connected ? 'SSE Connected' : 'Disconnected' }}</span>
        <span class="text-gray-600">{{ store.events.length }} events</span>
      </div>
    </div>

    <!-- Bearer graph -->
    <div class="border-b border-gray-800" :style="{ height: graphHeight + '%', minHeight: '200px' }">
      <HeMBBearerGraph />
    </div>

    <!-- Flow table -->
    <div class="flex-1 min-h-0 overflow-hidden">
      <HeMBFlowTable />
    </div>
  </div>
</template>
