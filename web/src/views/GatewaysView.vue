<script setup>
import { onMounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()

onMounted(() => store.fetchGateways())
</script>

<template>
  <div class="max-w-4xl mx-auto space-y-6">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-bold">Gateways</h1>
      <button
        @click="store.fetchGateways()"
        class="px-3 py-1.5 text-sm rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors"
      >
        Refresh
      </button>
    </div>

    <div v-if="!store.gateways.length" class="bg-gray-900 rounded-xl p-8 border border-gray-800 text-center">
      <p class="text-gray-400">No gateways configured</p>
      <p class="text-sm text-gray-500 mt-2">
        Gateway management will be available in Phase 4.
        Configure MQTT or satellite gateways to bridge mesh networks.
      </p>
    </div>

    <div v-else class="space-y-4">
      <div
        v-for="gw in store.gateways"
        :key="gw.id ?? gw.name"
        class="bg-gray-900 rounded-xl p-5 border border-gray-800"
      >
        <div class="flex items-center justify-between">
          <div>
            <h3 class="font-medium text-gray-200">{{ gw.name ?? gw.id ?? 'Unknown' }}</h3>
            <p class="text-sm text-gray-500 mt-0.5">{{ gw.type ?? 'Unknown type' }}</p>
          </div>
          <span
            class="text-xs px-2 py-1 rounded-full"
            :class="gw.status === 'connected' ? 'bg-emerald-900/30 text-emerald-400' : 'bg-gray-800 text-gray-500'"
          >
            {{ gw.status ?? 'unknown' }}
          </span>
        </div>
      </div>
    </div>
  </div>
</template>
