<script setup>
</script>

<template>
  <div class="max-w-3xl mx-auto">
    <h1 class="text-lg font-semibold text-gray-200 mb-4">Help</h1>

    <div class="space-y-4">
      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Getting Started</h2>
        <div class="space-y-2 text-[12px] text-gray-400 leading-relaxed">
          <p>MeshSat is an any-to-any message routing gateway. It bridges Meshtastic mesh radios with satellite (Iridium SBD), cellular (4G/LTE SMS), MQTT, and webhook transports.</p>
          <p><strong class="text-gray-300">1.</strong> Connect a Meshtastic radio via USB. MeshSat auto-detects the device and starts receiving mesh traffic.</p>
          <p><strong class="text-gray-300">2.</strong> Add gateways in <strong class="text-gray-300">Settings</strong> (Iridium modem, cellular modem, MQTT broker, or webhook URL).</p>
          <p><strong class="text-gray-300">3.</strong> Create bridge rules in <strong class="text-gray-300">Bridge</strong> to define which messages get forwarded where.</p>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Pages</h2>
        <div class="space-y-2 text-[12px] text-gray-400">
          <div><strong class="text-gray-300">Dashboard</strong> — Live overview: transport status, signal quality, outbound queue, scheduler state, and activity feed via SSE.</div>
          <div><strong class="text-gray-300">Comms</strong> — Read and send mesh messages. View SBD queue, SMS inbox, webhook log, and per-message delivery tracking.</div>
          <div><strong class="text-gray-300">Peers</strong> — Mesh node list with signal, position, telemetry, and neighbor info. SMS contact book for cellular gateway.</div>
          <div><strong class="text-gray-300">Bridge</strong> — Create and manage forwarding rules. Rules are direction-aware (outbound: mesh to gateways, inbound: gateways to mesh) and evaluated in priority order.</div>
          <div><strong class="text-gray-300">Passes</strong> — Iridium satellite pass predictions using SGP4/TLE propagation. Shows pass quality scores, elevation profiles, and signal overlay. Supports auto, GPS, and manual location modes.</div>
          <div><strong class="text-gray-300">Map</strong> — Leaflet map with mesh node positions, message markers, and per-node filtering.</div>
          <div><strong class="text-gray-300">Settings</strong> — Radio config (LoRa, channels, position), module config (canned messages, S&F, range test), gateway setup, and device admin (reboot, factory reset, traceroute).</div>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Bridge Rules</h2>
        <div class="space-y-2 text-[12px] text-gray-400 leading-relaxed">
          <p>Bridge rules are the single authority for message forwarding. No message crosses transports without a matching rule.</p>
          <p><strong class="text-gray-300">Outbound</strong> — Forward mesh messages to Iridium, cellular, MQTT, or webhooks. Filter by source node, channel, or portnum.</p>
          <p><strong class="text-gray-300">Inbound</strong> — Route incoming satellite/cellular/webhook messages to a specific mesh channel or node.</p>
          <p>Rules are evaluated in priority order. Use the drag handle to reorder. Each rule can be individually enabled or disabled without deleting it.</p>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Iridium Satellite</h2>
        <div class="space-y-2 text-[12px] text-gray-400 leading-relaxed">
          <p>Iridium SBD payloads are limited to 340 bytes. MeshSat uses a compact binary codec to fit mesh messages within this constraint. Longer messages are truncated.</p>
          <p><strong class="text-gray-300">Pass Scheduler</strong> — When enabled, MeshSat predicts satellite passes using TLE data and optimizes send timing. Four modes: Idle (low-power polling), PreWake (preparing), Active (transmitting), PostPass (grace period).</p>
          <p><strong class="text-gray-300">Dead Letter Queue</strong> — Failed sends are automatically retried with ISU-aware backoff. After a failed SBDIX (mo_status 32/36), MeshSat waits at least 3 minutes before retrying to avoid registration death spiral.</p>
          <p><strong class="text-gray-300">Mailbox Check</strong> — Checks for inbound MT messages using lightweight SBDSX first, only escalating to full SBDIX when the RA flag or MTWaiting count indicates pending mail.</p>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Cellular Gateway</h2>
        <div class="space-y-2 text-[12px] text-gray-400 leading-relaxed">
          <p>Supports USB cellular modems (e.g., SIM7600G-H) for SMS-based message forwarding. Connect via USB and configure in Settings.</p>
          <p><strong class="text-gray-300">Allowed Senders</strong> — Restrict inbound SMS processing to specific phone numbers. If empty, all SMS are forwarded.</p>
          <p><strong class="text-gray-300">Data Connection</strong> — The cellular modem can also provide LTE data connectivity. Enable/disable from the Dashboard cellular panel.</p>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Delivery Tracking</h2>
        <div class="space-y-2 text-[12px] text-gray-400 leading-relaxed">
          <p>Every forwarded message is tracked in the delivery ledger with per-channel status: pending, sent, delivered, or failed.</p>
          <p>View delivery status on individual messages in <strong class="text-gray-300">Comms</strong>, or see aggregate statistics in <strong class="text-gray-300">Bridge</strong>.</p>
        </div>
      </div>

      <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Troubleshooting</h2>
        <div class="space-y-2 text-[12px] text-gray-400 leading-relaxed">
          <p><strong class="text-gray-300">Radio not detected</strong> — Check USB connection. MeshSat scans for Meshtastic devices by USB VID:PID. Try a different USB port or cable.</p>
          <p><strong class="text-gray-300">Iridium signal 0 bars</strong> — Ensure the antenna has clear sky view. Indoor locations typically have no satellite visibility. Check antenna cable connections.</p>
          <p><strong class="text-gray-300">Messages not forwarding</strong> — Verify a bridge rule exists for the desired direction. Check that the rule is enabled and the source gateway is connected.</p>
          <p><strong class="text-gray-300">DLQ keeps retrying</strong> — Failed Iridium sends retry with exponential backoff (up to 30 min). Check signal quality on the Dashboard. You can cancel or reprioritize items in the queue.</p>
        </div>
      </div>
    </div>
  </div>
</template>
