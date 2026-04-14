<script setup>
import { computed } from 'vue'

const props = defineProps({
  event: { type: Object, required: true }
})

const payload = computed(() => {
  if (!props.event.payload) return {}
  if (typeof props.event.payload === 'string') {
    try { return JSON.parse(props.event.payload) } catch { return {} }
  }
  return props.event.payload
})

const isGeneration = computed(() =>
  props.event.type === 'HEMB_GENERATION_DECODED' || props.event.type === 'HEMB_GENERATION_FAILED'
)

const bearerColors = {
  mesh: '#06b6d4',
  iridium: '#f97316',
  iridium_imt: '#f59e0b',
  cellular: '#8b5cf6',
  sms: '#a855f7',
  mqtt: '#10b981',
  tcp: '#6b7280',
  ax25: '#ef4444',
  zigbee: '#22c55e',
}

function bearerColor(type) {
  return bearerColors[type] || '#6b7280'
}

const contributions = computed(() => {
  const bearers = payload.value.bearers || []
  const total = bearers.reduce((s, b) => s + (b.symbol_count || 1), 0)
  return bearers.map(b => ({
    ...b,
    count: b.symbol_count || 1,
    pct: total > 0 ? ((b.symbol_count || 1) / total * 100).toFixed(1) : 0,
    color: bearerColor(b.bearer_type),
    width: total > 0 ? (b.symbol_count || 1) / total * 100 : 0,
  }))
})

const totalCost = computed(() => {
  const c = payload.value.cost_est || payload.value.cost_total || 0
  return c > 0 ? '$' + c.toFixed(4) : 'free'
})
</script>

<template>
  <div class="space-y-3">
    <!-- Bearer contribution bar (generation events) -->
    <div v-if="isGeneration && contributions.length > 0">
      <div class="text-[10px] text-gray-500 uppercase tracking-wider mb-1">Bearer Contributions</div>
      <div class="flex h-5 rounded overflow-hidden">
        <div v-for="(c, i) in contributions" :key="i"
          :style="{ width: c.width + '%', backgroundColor: c.color }"
          class="flex items-center justify-center text-[9px] font-mono text-white/80 min-w-[20px]"
          :title="`Bearer ${c.bearer_idx}: ${c.count} symbols (${c.pct}%)`">
          {{ c.count }}
        </div>
      </div>
      <div class="flex gap-3 mt-1 text-[10px]">
        <span v-for="(c, i) in contributions" :key="i" class="flex items-center gap-1">
          <span class="w-2 h-2 rounded-full inline-block" :style="{ backgroundColor: c.color }"></span>
          <span class="text-gray-400">B{{ c.bearer_idx }}: {{ c.pct }}%</span>
        </span>
      </div>
    </div>

    <!-- Decode stats (generation events) -->
    <div v-if="isGeneration" class="grid grid-cols-2 md:grid-cols-4 gap-2">
      <div class="bg-gray-800/50 rounded px-2 py-1">
        <div class="text-[10px] text-gray-500">K (required)</div>
        <div class="font-mono text-gray-200">{{ payload.k ?? '-' }}</div>
      </div>
      <div class="bg-gray-800/50 rounded px-2 py-1">
        <div class="text-[10px] text-gray-500">N (received)</div>
        <div class="font-mono text-gray-200">{{ payload.received ?? payload.n ?? '-' }}</div>
      </div>
      <div class="bg-gray-800/50 rounded px-2 py-1">
        <div class="text-[10px] text-gray-500">Decode time</div>
        <div class="font-mono text-gray-200">{{ payload.decode_time_us != null ? payload.decode_time_us + 'us' : '-' }}</div>
      </div>
      <div class="bg-gray-800/50 rounded px-2 py-1">
        <div class="text-[10px] text-gray-500">Cost</div>
        <div class="font-mono" :class="totalCost === 'free' ? 'text-emerald-400' : 'text-orange-400'">{{ totalCost }}</div>
      </div>
    </div>

    <!-- Raw payload (all events) -->
    <div>
      <div class="text-[10px] text-gray-500 uppercase tracking-wider mb-1">Raw Payload</div>
      <pre class="text-[10px] font-mono text-gray-400 whitespace-pre-wrap bg-gray-900/50 rounded p-2 max-h-32 overflow-auto">{{ JSON.stringify(payload, null, 2) }}</pre>
    </div>
  </div>
</template>
