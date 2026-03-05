<script setup>
import { onMounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
const store = useMeshsatStore()

onMounted(() => {
  store.fetchStatus()
  store.fetchNodes()
  store.fetchGateways()
})
</script>

<template>
  <div class="max-w-3xl mx-auto">
    <h1 class="text-lg font-semibold text-gray-200 mb-4">About MeshSat</h1>

    <div class="space-y-4">
      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4 text-center">
        <div class="font-display font-bold text-2xl text-teal-400 mb-1">MeshSat</div>
        <div class="text-[12px] text-gray-500">Any-to-Any Message Routing Gateway</div>
        <div class="text-[11px] text-gray-600 mt-2 font-mono">v0.2.0</div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">System Info</h2>
        <div class="space-y-1.5 text-[12px]">
          <div class="flex justify-between">
            <span class="text-gray-500">Radio</span>
            <span :class="store.status?.connected ? 'text-emerald-400' : 'text-red-400'">
              {{ store.status?.connected ? 'Connected' : 'Disconnected' }}
            </span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Transport</span>
            <span class="text-gray-300">{{ store.status?.transport || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Serial Port</span>
            <span class="text-gray-300 font-mono">{{ store.status?.address || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Node ID</span>
            <span class="text-gray-300 font-mono">{{ store.status?.node_id || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Node Name</span>
            <span class="text-gray-300">{{ store.status?.node_name || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Hardware</span>
            <span class="text-gray-300">{{ store.status?.hw_model_name || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Mesh Nodes</span>
            <span class="text-gray-300 font-mono">{{ store.status?.num_nodes || 0 }}</span>
          </div>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Transport Channels</h2>
        <div class="grid grid-cols-2 gap-2 text-[12px]">
          <div class="flex items-center gap-2">
            <span class="w-2 h-2 rounded-full bg-tactical-lora"></span>
            <span class="text-gray-400">Meshtastic LoRa</span>
          </div>
          <div class="flex items-center gap-2">
            <span class="w-2 h-2 rounded-full bg-tactical-iridium"></span>
            <span class="text-gray-400">Iridium SBD</span>
          </div>
          <div class="flex items-center gap-2">
            <span class="w-2 h-2 rounded-full bg-teal-400"></span>
            <span class="text-gray-400">Astrocast LEO</span>
          </div>
          <div class="flex items-center gap-2">
            <span class="w-2 h-2 rounded-full bg-sky-400"></span>
            <span class="text-gray-400">Cellular SMS</span>
          </div>
          <div class="flex items-center gap-2">
            <span class="w-2 h-2 rounded-full bg-amber-400"></span>
            <span class="text-gray-400">Webhook HTTP</span>
          </div>
          <div class="flex items-center gap-2">
            <span class="w-2 h-2 rounded-full bg-purple-400"></span>
            <span class="text-gray-400">MQTT Broker</span>
          </div>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Architecture</h2>
        <div class="space-y-2 text-[12px] text-gray-400 leading-relaxed">
          <p>MeshSat is an any-to-any message routing fabric. Bridge rules define directional routes between transport channels. Messages are tracked through a delivery ledger with per-channel status.</p>
          <p>Runs standalone on any Linux device with USB radios, or as a managed service inside CubeOS on Raspberry Pi and ARM64 SBCs.</p>
        </div>
      </div>

      <div class="text-center text-[11px] text-gray-600 mt-6">
        MeshSat &mdash; part of the <span class="text-gray-500">CubeOS</span> project
      </div>
    </div>
  </div>
</template>
