<script setup>
import { useMeshsatStore } from '@/stores/meshsat'
const store = useMeshsatStore()
</script>

<template>
  <div class="max-w-3xl mx-auto">
    <h1 class="text-lg font-semibold text-gray-200 mb-4">About MeshSat</h1>

    <div class="space-y-4">
      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4 text-center">
        <div class="font-display font-bold text-2xl text-teal-400 mb-1">MeshSat</div>
        <div class="text-[12px] text-gray-500">Meshtastic + Satellite Gateway</div>
        <div class="text-[11px] text-gray-600 mt-2 font-mono">Port 6050</div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">System Info</h2>
        <div class="space-y-1.5 text-[12px]">
          <div class="flex justify-between">
            <span class="text-gray-500">Radio Connected</span>
            <span :class="store.status?.connected ? 'text-emerald-400' : 'text-red-400'">
              {{ store.status?.connected ? 'Yes' : 'No' }}
            </span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Node ID</span>
            <span class="text-gray-300 font-mono">{{ store.status?.node_id ? `!${store.status.node_id.toString(16).toUpperCase().slice(-8)}` : 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Node Name</span>
            <span class="text-gray-300">{{ store.status?.node_name || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Firmware</span>
            <span class="text-gray-300 font-mono">{{ store.status?.firmware_version || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Hardware</span>
            <span class="text-gray-300">{{ store.status?.hw_model || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Total Nodes</span>
            <span class="text-gray-300 font-mono">{{ store.nodes?.length || 0 }}</span>
          </div>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Supported Transports</h2>
        <div class="grid grid-cols-2 gap-2 text-[12px]">
          <div class="flex items-center gap-2">
            <span class="w-2 h-2 rounded-full bg-tactical-lora"></span>
            <span class="text-gray-400">Meshtastic LoRa (mesh radio)</span>
          </div>
          <div class="flex items-center gap-2">
            <span class="w-2 h-2 rounded-full bg-tactical-iridium"></span>
            <span class="text-gray-400">Iridium SBD (9603N satellite)</span>
          </div>
          <div class="flex items-center gap-2">
            <span class="w-2 h-2 rounded-full bg-sky-400"></span>
            <span class="text-gray-400">Cellular 4G/LTE (USB modem)</span>
          </div>
          <div class="flex items-center gap-2">
            <span class="w-2 h-2 rounded-full bg-purple-400"></span>
            <span class="text-gray-400">MQTT (internet bridge)</span>
          </div>
        </div>
      </div>

      <div class="text-center text-[11px] text-gray-600 mt-6">
        Part of the CubeOS ecosystem
      </div>
    </div>
  </div>
</template>
