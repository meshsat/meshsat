<script setup>
import { computed } from 'vue'

const props = defineProps({
  rule: { type: Object, required: true }
})
const emit = defineEmits(['toggle', 'edit', 'delete'])

const sourceLabel = computed(() => {
  const r = props.rule
  switch (r.source_type) {
    case 'any': return 'Any Message'
    case 'channel': {
      try { const ch = JSON.parse(r.source_channels || '[]'); return `Ch ${ch.join(', ')}` } catch { return 'Channel' }
    }
    case 'node': {
      try {
        const n = JSON.parse(r.source_nodes || '[]')
        return n.length > 2 ? `${n.length} nodes` : n.join(', ')
      } catch { return 'Node' }
    }
    case 'portnum': {
      const names = { 1: 'Text', 3: 'Position', 4: 'NodeInfo', 67: 'Telemetry', 70: 'Traceroute' }
      try {
        const pn = JSON.parse(r.source_portnums || '[]')
        return pn.map(p => names[p] || `#${p}`).join(', ')
      } catch { return 'Portnum' }
    }
    default: return r.source_type
  }
})

const destLabel = computed(() => {
  const d = props.rule.dest_type
  if (d === 'both') return 'Iridium + MQTT'
  if (d === 'iridium') return 'Iridium SBD'
  if (d === 'mqtt') return 'MQTT'
  return d
})

const destAccent = computed(() => {
  const d = props.rule.dest_type
  if (d === 'iridium') return 'border-tactical-iridium/30 text-tactical-iridium'
  if (d === 'mqtt') return 'border-amber-400/30 text-amber-400'
  return 'border-blue-400/30 text-blue-400' // both
})

const destBg = computed(() => {
  const d = props.rule.dest_type
  if (d === 'iridium') return 'bg-tactical-iridium/5'
  if (d === 'mqtt') return 'bg-amber-400/5'
  return 'bg-blue-400/5'
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
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.floor(hrs / 24)}d ago`
}
</script>

<template>
  <div class="bg-tactical-surface rounded-lg border border-tactical-border overflow-hidden"
    :class="{ 'opacity-50': !rule.enabled }">
    <!-- Pipeline flow header -->
    <div class="flex items-center gap-0 p-3" :class="destBg">
      <!-- Source -->
      <div class="flex items-center gap-2 px-3 py-1.5 rounded-l-lg bg-tactical-lora/10 border border-tactical-lora/20">
        <svg class="w-3.5 h-3.5 text-tactical-lora" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <circle cx="12" cy="12" r="3"/><path d="M5.5 5.5a10 10 0 0114 0M8 8a6 6 0 018.5 0"/>
        </svg>
        <span class="text-[11px] font-medium text-tactical-lora">{{ sourceLabel }}</span>
      </div>

      <!-- Arrow -->
      <div class="flex items-center px-2">
        <svg class="w-5 h-5 text-gray-600" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <path stroke-linecap="round" stroke-linejoin="round" d="M17 8l4 4m0 0l-4 4m4-4H3" />
        </svg>
      </div>

      <!-- Destination -->
      <div class="flex items-center gap-2 px-3 py-1.5 rounded-r-lg border" :class="destAccent">
        <svg v-if="rule.dest_type === 'iridium' || rule.dest_type === 'both'" class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="currentColor">
          <rect x="10" y="10" width="4" height="4" rx="0.5"/><rect x="2" y="11" width="6" height="2" rx="0.5" opacity="0.7"/>
          <rect x="16" y="11" width="6" height="2" rx="0.5" opacity="0.7"/>
        </svg>
        <svg v-if="rule.dest_type === 'mqtt'" class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <path d="M4 12h16M12 4v16M7 7l10 10M17 7L7 17"/>
        </svg>
        <span class="text-[11px] font-medium">{{ destLabel }}</span>
      </div>

      <span class="flex-1" />

      <!-- Toggle -->
      <button @click="emit('toggle')"
        class="relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full transition-colors"
        :class="rule.enabled ? 'bg-teal-500' : 'bg-gray-600'">
        <span class="inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform mt-0.5"
          :class="rule.enabled ? 'translate-x-4 ml-0.5' : 'translate-x-0.5'" />
      </button>
    </div>

    <!-- Details -->
    <div class="px-4 py-2.5">
      <div class="flex items-center gap-2 mb-1.5">
        <span class="text-sm font-medium text-gray-200">{{ rule.name }}</span>
        <span v-if="rule.source_keyword" class="text-[9px] text-gray-500 px-1.5 py-px rounded bg-gray-800">
          keyword: {{ rule.source_keyword }}
        </span>
        <span v-if="rule.dest_type !== 'mqtt'" class="text-[9px] px-1.5 py-px rounded bg-gray-800"
          :class="rule.sat_priority === 0 ? 'text-red-400' : rule.sat_priority === 2 ? 'text-gray-500' : 'text-amber-400'">
          {{ priorityLabel }}
        </span>
      </div>

      <div class="flex items-center justify-between text-[10px]">
        <span class="text-gray-600">
          Matched: {{ rule.match_count || 0 }} | Last: {{ timeAgo(rule.last_match_at) }}
        </span>
        <div class="flex gap-2">
          <button @click="emit('edit')" class="text-gray-500 hover:text-teal-400 transition-colors">Edit</button>
          <button @click="emit('delete')" class="text-gray-500 hover:text-red-400 transition-colors">Delete</button>
        </div>
      </div>
    </div>
  </div>
</template>
