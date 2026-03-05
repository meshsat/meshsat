<script setup>
</script>

<template>
  <div class="max-w-3xl mx-auto">
    <h1 class="text-lg font-semibold text-gray-200 mb-4">Help</h1>

    <div class="space-y-4">
      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Getting Started</h2>
        <div class="space-y-2 text-[12px] text-gray-400 leading-relaxed">
          <p>MeshSat bridges your Meshtastic mesh radio network with satellite (Iridium SBD), cellular (4G/LTE), and internet (MQTT/Webhooks) gateways.</p>
          <p>Connect a Meshtastic radio via USB to start receiving mesh messages. Configure gateways in the <strong class="text-gray-300">Settings</strong> page to enable forwarding.</p>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Pages Overview</h2>
        <div class="space-y-2 text-[12px] text-gray-400">
          <div><strong class="text-gray-300">Dashboard</strong> — Real-time overview of all transports, signal quality, message queue, and activity log.</div>
          <div><strong class="text-gray-300">Comms</strong> — Read and compose mesh messages, view SBD queue history, SMS, and webhook activity.</div>
          <div><strong class="text-gray-300">Peers</strong> — View mesh nodes and manage SMS contacts.</div>
          <div><strong class="text-gray-300">Bridge</strong> — Configure forwarding rules between mesh, satellite, cellular, and webhook transports.</div>
          <div><strong class="text-gray-300">Passes</strong> — Iridium satellite pass predictions with TLE-based scheduling.</div>
          <div><strong class="text-gray-300">Map</strong> — Geographic view of mesh nodes with dark/light themes.</div>
          <div><strong class="text-gray-300">Settings</strong> — Radio configuration, gateway setup, and system admin.</div>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Bridge Rules</h2>
        <div class="space-y-2 text-[12px] text-gray-400 leading-relaxed">
          <p>Bridge rules control how messages are forwarded between transports. Rules are evaluated in priority order — the first matching rule wins.</p>
          <p><strong class="text-gray-300">Outbound</strong> rules forward mesh messages to satellite, cellular, MQTT, or webhooks.</p>
          <p><strong class="text-gray-300">Inbound</strong> rules forward satellite/cellular/webhook messages back to mesh channels or specific nodes.</p>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Iridium SBD Tips</h2>
        <div class="space-y-2 text-[12px] text-gray-400 leading-relaxed">
          <p>Iridium satellite messages are limited to 340 bytes. MeshSat uses a compact binary codec to fit mesh messages within this limit.</p>
          <p>The pass scheduler automatically optimizes send timing based on satellite visibility predictions. Enable it in Settings for best results.</p>
          <p>Failed sends are queued in the Dead Letter Queue (DLQ) and retried automatically with exponential backoff.</p>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Keyboard Shortcuts</h2>
        <div class="grid grid-cols-2 gap-1 text-[12px] text-gray-400">
          <div><kbd class="px-1.5 py-0.5 rounded bg-gray-700 text-gray-300 text-[10px] font-mono">1-9</kbd> Navigate to pages</div>
          <div><kbd class="px-1.5 py-0.5 rounded bg-gray-700 text-gray-300 text-[10px] font-mono">r</kbd> Refresh data</div>
        </div>
      </div>
    </div>
  </div>
</template>
