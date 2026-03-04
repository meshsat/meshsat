<script setup>
import { computed } from 'vue'

const props = defineProps({
  rule: { type: Object, required: true }
})
const emit = defineEmits(['toggle', 'edit', 'delete'])

const isInbound = computed(() => props.rule.dest_type === 'mesh')

const sourceLabel = computed(() => {
  const r = props.rule
  if (isInbound.value) {
    switch (r.source_type) {
      case 'iridium': return 'Iridium Satellite'
      case 'mqtt': return 'MQTT Broker'
      case 'cellular': return 'Cellular (SMS)'
      case 'external': return 'Any External'
      default: return r.source_type
    }
  }
  switch (r.source_type) {
    case 'any': return 'All Messages'
    case 'channel': {
      try { const ch = JSON.parse(r.source_channels || '[]'); return `Ch ${ch.join(', ')}` } catch { return 'Channels' }
    }
    case 'node': {
      try {
        const n = JSON.parse(r.source_nodes || '[]')
        return n.length > 2 ? `${n.length} nodes` : n.join(', ')
      } catch { return 'Nodes' }
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
  const r = props.rule
  if (isInbound.value) {
    const chLabel = `Ch ${r.dest_channel || 0}`
    if (r.dest_node) return `${chLabel} → ${r.dest_node}`
    return `${chLabel} (broadcast)`
  }
  const d = r.dest_type
  if (d === 'both') return 'Iridium + MQTT'
  if (d === 'iridium') return 'Iridium SBD'
  if (d === 'mqtt') return 'MQTT'
  if (d === 'cellular') return 'Cellular SMS'
  if (d === 'all') return 'All Gateways'
  return d
})

const destAccent = computed(() => {
  if (isInbound.value) return 'border-tactical-lora/30 text-tactical-lora'
  const d = props.rule.dest_type
  if (d === 'iridium') return 'border-tactical-iridium/30 text-tactical-iridium'
  if (d === 'mqtt') return 'border-amber-400/30 text-amber-400'
  if (d === 'cellular') return 'border-sky-400/30 text-sky-400'
  if (d === 'all') return 'border-purple-400/30 text-purple-400'
  return 'border-blue-400/30 text-blue-400' // both
})

const sourceAccent = computed(() => {
  if (isInbound.value) {
    const s = props.rule.source_type
    if (s === 'iridium') return 'bg-tactical-iridium/10 border-tactical-iridium/20'
    if (s === 'mqtt') return 'bg-amber-400/10 border-amber-400/20'
    if (s === 'cellular') return 'bg-sky-400/10 border-sky-400/20'
    return 'bg-blue-400/10 border-blue-400/20' // external
  }
  return 'bg-tactical-lora/10 border-tactical-lora/20'
})

const sourceIconColor = computed(() => {
  if (isInbound.value) {
    const s = props.rule.source_type
    if (s === 'iridium') return 'text-tactical-iridium'
    if (s === 'mqtt') return 'text-amber-400'
    if (s === 'cellular') return 'text-sky-400'
    return 'text-blue-400'
  }
  return 'text-tactical-lora'
})

const sourceLabelColor = computed(() => {
  if (isInbound.value) {
    const s = props.rule.source_type
    if (s === 'iridium') return 'text-tactical-iridium'
    if (s === 'mqtt') return 'text-amber-400'
    if (s === 'cellular') return 'text-sky-400'
    return 'text-blue-400'
  }
  return 'text-tactical-lora'
})

const destBg = computed(() => {
  if (isInbound.value) return 'bg-tactical-lora/5'
  const d = props.rule.dest_type
  if (d === 'iridium') return 'bg-tactical-iridium/5'
  if (d === 'mqtt') return 'bg-amber-400/5'
  if (d === 'cellular') return 'bg-sky-400/5'
  if (d === 'all') return 'bg-purple-400/5'
  return 'bg-blue-400/5'
})

// Cost risk badge
const riskLevel = computed(() => props.rule.risk?.level)
const riskColor = computed(() => {
  const l = riskLevel.value
  if (l === 'danger') return 'bg-red-500'
  if (l === 'warning') return 'bg-amber-500'
  return 'bg-emerald-500'
})
const riskTooltip = computed(() => {
  const risk = props.rule.risk
  if (!risk) return ''
  const reasons = (risk.reasons || []).join('; ')
  if (risk.level === 'safe') return `Low risk: ${reasons || 'Free transport'}`
  return `${risk.level === 'danger' ? 'HIGH RISK' : 'Warning'}: ${reasons}. Est. ${risk.estimated_monthly_cost}/month`
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
      <div class="flex items-center gap-2 px-3 py-1.5 rounded-l-lg border" :class="sourceAccent">
        <!-- Outbound: mesh radio icon -->
        <svg v-if="!isInbound" class="w-3.5 h-3.5 text-tactical-lora" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <circle cx="12" cy="12" r="3"/><path d="M5.5 5.5a10 10 0 0114 0M8 8a6 6 0 018.5 0"/>
        </svg>
        <!-- Inbound: satellite/mqtt icon -->
        <svg v-if="isInbound && (rule.source_type === 'iridium' || rule.source_type === 'external')" class="w-3.5 h-3.5" :class="sourceIconColor" viewBox="0 0 24 24" fill="currentColor">
          <rect x="10" y="10" width="4" height="4" rx="0.5"/><rect x="2" y="11" width="6" height="2" rx="0.5" opacity="0.7"/>
          <rect x="16" y="11" width="6" height="2" rx="0.5" opacity="0.7"/>
        </svg>
        <svg v-if="isInbound && rule.source_type === 'mqtt'" class="w-3.5 h-3.5" :class="sourceIconColor" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <path d="M4 12h16M12 4v16M7 7l10 10M17 7L7 17"/>
        </svg>
        <svg v-if="isInbound && rule.source_type === 'cellular'" class="w-3.5 h-3.5" :class="sourceIconColor" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <rect x="5" y="2" width="14" height="20" rx="2"/><path d="M12 18h.01"/>
        </svg>
        <span class="text-[11px] font-medium" :class="sourceLabelColor">{{ sourceLabel }}</span>
      </div>

      <!-- Arrow -->
      <div class="flex items-center px-2">
        <svg class="w-5 h-5 text-gray-600" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <path stroke-linecap="round" stroke-linejoin="round" d="M17 8l4 4m0 0l-4 4m4-4H3" />
        </svg>
      </div>

      <!-- Destination -->
      <div class="flex items-center gap-2 px-3 py-1.5 rounded-r-lg border" :class="destAccent">
        <!-- Outbound: satellite/mqtt icons -->
        <template v-if="!isInbound">
          <svg v-if="rule.dest_type === 'iridium' || rule.dest_type === 'both' || rule.dest_type === 'all'" class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="currentColor">
            <rect x="10" y="10" width="4" height="4" rx="0.5"/><rect x="2" y="11" width="6" height="2" rx="0.5" opacity="0.7"/>
            <rect x="16" y="11" width="6" height="2" rx="0.5" opacity="0.7"/>
          </svg>
          <svg v-if="rule.dest_type === 'mqtt'" class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M4 12h16M12 4v16M7 7l10 10M17 7L7 17"/>
          </svg>
          <svg v-if="rule.dest_type === 'cellular'" class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <rect x="5" y="2" width="14" height="20" rx="2"/><path d="M12 18h.01"/>
          </svg>
        </template>
        <!-- Inbound: mesh radio icon -->
        <svg v-if="isInbound" class="w-3.5 h-3.5 text-tactical-lora" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <circle cx="12" cy="12" r="3"/><path d="M5.5 5.5a10 10 0 0114 0M8 8a6 6 0 018.5 0"/>
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
        <span v-if="rule.risk" class="w-2 h-2 rounded-full shrink-0" :class="riskColor" :title="riskTooltip" />
        <span class="text-sm font-medium text-gray-200">{{ rule.name }}</span>
        <span v-if="rule.source_keyword" class="text-[9px] text-gray-500 px-1.5 py-px rounded bg-gray-800">
          keyword: {{ rule.source_keyword }}
        </span>
        <span v-if="!isInbound && rule.dest_type !== 'mqtt'" class="text-[9px] px-1.5 py-px rounded bg-gray-800"
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
