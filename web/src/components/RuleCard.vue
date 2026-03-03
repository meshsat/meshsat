<script setup>
import { computed } from 'vue'

const props = defineProps({
  rule: { type: Object, required: true }
})
const emit = defineEmits(['toggle', 'edit', 'delete'])

const sourceLabel = computed(() => {
  const r = props.rule
  switch (r.source_type) {
    case 'any': return 'any message'
    case 'channel': {
      try { const ch = JSON.parse(r.source_channels || '[]'); return `channel ${ch.join(', ')}` } catch { return 'channel' }
    }
    case 'node': {
      try { const n = JSON.parse(r.source_nodes || '[]'); return `node ${n.join(', ')}` } catch { return 'node' }
    }
    case 'portnum': {
      const names = { 1: 'Text', 3: 'Position', 4: 'NodeInfo', 67: 'Telemetry' }
      try {
        const pn = JSON.parse(r.source_portnums || '[]')
        return pn.map(p => names[p] || `#${p}`).join(', ')
      } catch { return 'portnum' }
    }
    default: return r.source_type
  }
})

const destLabel = computed(() => {
  const d = props.rule.dest_type
  if (d === 'both') return 'Iridium + MQTT'
  return d.charAt(0).toUpperCase() + d.slice(1)
})

const priorityLabel = computed(() => {
  const p = props.rule.sat_priority
  return p === 0 ? 'Critical' : p === 2 ? 'Low' : 'Normal'
})

function timeAgo(ts) {
  if (!ts) return 'Never'
  const diff = Date.now() - new Date(ts).getTime()
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'Just now'
  if (mins < 60) return `${mins} min ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.floor(hrs / 24)}d ago`
}
</script>

<template>
  <div class="bg-gray-800 rounded-lg p-4 border border-gray-700">
    <div class="flex items-start justify-between mb-2">
      <div class="flex items-center gap-2">
        <svg class="w-4 h-4 text-teal-400" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="2">
          <path stroke-linecap="round" stroke-linejoin="round" d="M13 10V3L4 14h7v7l9-11h-7z" />
        </svg>
        <span class="text-sm font-medium text-gray-200">{{ rule.name }}</span>
      </div>
      <button @click="emit('toggle')"
        class="relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full transition-colors"
        :class="rule.enabled ? 'bg-teal-500' : 'bg-gray-600'">
        <span class="inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform mt-0.5"
          :class="rule.enabled ? 'translate-x-4 ml-0.5' : 'translate-x-0.5'"></span>
      </button>
    </div>

    <div class="text-xs text-gray-400 space-y-1 mb-3">
      <div><span class="text-gray-500">When</span> {{ sourceLabel }}</div>
      <div class="flex items-center gap-1">
        <svg class="w-3 h-3 text-gray-600" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="2">
          <path stroke-linecap="round" stroke-linejoin="round" d="M19 14l-7 7m0 0l-7-7m7 7V3" />
        </svg>
        <span><span class="text-gray-500">Forward via</span> {{ destLabel }}</span>
      </div>
      <div v-if="rule.dest_type !== 'mqtt'">
        <span class="text-gray-500">Priority:</span> {{ priorityLabel }}
      </div>
    </div>

    <div class="flex items-center justify-between text-xs">
      <div class="text-gray-500">
        Matched: {{ rule.match_count || 0 }} | Last: {{ timeAgo(rule.last_match_at) }}
      </div>
      <div class="flex gap-2">
        <button @click="emit('edit')" class="text-gray-400 hover:text-teal-400">Edit</button>
        <button @click="emit('delete')" class="text-gray-400 hover:text-red-400">Delete</button>
      </div>
    </div>
  </div>
</template>
