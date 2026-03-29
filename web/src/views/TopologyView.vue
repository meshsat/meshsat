<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { useObservabilityStore } from '../composables/useObservabilityStore'
import ObservabilityGraph from '../components/ObservabilityGraph.vue'
import HeMBFlowTable from '../components/HeMBFlowTable.vue'
import HeMBMatrixInspector from '../components/HeMBMatrixInspector.vue'

const store = useObservabilityStore()
const showDebug = ref(false)
const graphHeight = ref(55) // percentage

let pollFast = null
let pollSlow = null

onMounted(async () => {
  await store.fetchAll()
  store.connectSSE()
  // Fast poll: interfaces, gateways, HeMB stats (10s)
  pollFast = setInterval(() => {
    store.fetchInterfaces()
    store.fetchGateways()
    store.fetchHembStats()
  }, 10000)
  // Slow poll: everything else (30s)
  pollSlow = setInterval(() => {
    store.fetchAll()
  }, 30000)
})

onUnmounted(() => {
  store.closeSSE()
  if (pollFast) clearInterval(pollFast)
  if (pollSlow) clearInterval(pollSlow)
})
</script>

<template>
  <div class="flex flex-col h-full">
    <!-- Stats bar -->
    <div class="flex items-center gap-6 px-4 py-2 border-b border-gray-800 text-xs">
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Interfaces</span>
        <span class="font-mono text-gray-200">
          {{ store.onlineCount.value }}/{{ store.interfaceNodes.value.length }}
        </span>
      </div>
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Nodes</span>
        <span class="font-mono text-gray-200">{{ store.meshNodeList.value.length }}</span>
      </div>
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Peers</span>
        <span class="font-mono text-gray-200">{{ store.peerNodes.value.length }}</span>
      </div>
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">Rules</span>
        <span class="font-mono text-gray-200">{{ store.ruleEdges.value.length }}</span>
      </div>
      <div class="flex items-center gap-1.5">
        <span class="text-gray-500 uppercase tracking-wider">HeMB</span>
        <span class="font-mono text-emerald-400">{{ store.hembStats.value?.generations_decoded ?? 0 }}</span>
        <span class="text-gray-600">/</span>
        <span class="font-mono text-red-400">{{ store.hembStats.value?.generations_failed ?? 0 }}</span>
      </div>
      <div class="ml-auto flex items-center gap-2">
        <div class="w-2 h-2 rounded-full" :class="store.connected.value ? 'bg-emerald-400' : 'bg-red-400'"></div>
        <span class="text-gray-500">{{ store.connected.value ? 'SSE' : 'Offline' }}</span>
        <span class="text-gray-600">{{ store.events.value.length }} events</span>
        <button @click="store.selectNode(null)"
          v-if="store.selectedNodeId.value"
          class="px-2 py-0.5 bg-teal-900/30 text-teal-400 text-[10px] rounded">
          Clear filter
        </button>
        <button @click="showDebug = !showDebug"
          class="px-2 py-0.5 rounded text-[10px]"
          :class="showDebug ? 'bg-purple-900/30 text-purple-400' : 'bg-gray-800 text-gray-500 hover:text-gray-300'">
          Debug
        </button>
      </div>
    </div>

    <!-- Graph -->
    <div class="border-b border-gray-800" :style="{ height: graphHeight + '%', minHeight: '250px' }">
      <ObservabilityGraph />
    </div>

    <!-- Flow table -->
    <div class="flex-1 min-h-0 overflow-hidden">
      <HeMBFlowTable />
    </div>

    <!-- Debug panel -->
    <HeMBMatrixInspector v-if="showDebug" />
  </div>
</template>
