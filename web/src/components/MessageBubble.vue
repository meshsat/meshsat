<script setup>
import { computed } from 'vue'
import DeliveryTicks from '@/components/DeliveryTicks.vue'
import { formatRelativeTime } from '@/utils/format'

// iMessage-style message bubble with per-bearer colouring.
// [MESHSAT-552]
//
// Transport → bubble tone mapping:
//   mesh / meshtastic                    → blue   (emerald accent)
//   sms / cellular                       → green
//   iridium / iridium_imt / sbd / imt    → amber  (tactical-iridium)
//   aprs / ax25                          → teal
//   reticulum / rns / tcp_0 / mqtt_rns_0 → violet
//   anything else                        → slate

const props = defineProps({
  // Unified message record: { id, text, transport, direction,
  // created_at, delivery_status, per_bearer, read }
  message: { type: Object, required: true }
})

const palette = computed(() => {
  const t = (props.message.transport || '').toLowerCase()
  if (t === 'mesh' || t.startsWith('mesh'))                return 'bg-blue-500/15 border-blue-500/40 text-blue-100'
  if (t === 'sms' || t.startsWith('cellular'))             return 'bg-emerald-500/15 border-emerald-500/40 text-emerald-100'
  if (t === 'iridium' || t.startsWith('iridium') ||
      t === 'sbd' || t === 'imt')                          return 'bg-amber-500/15 border-amber-500/40 text-amber-100'
  if (t === 'aprs' || t === 'ax25' || t.startsWith('ax25'))return 'bg-teal-500/15 border-teal-500/40 text-teal-100'
  if (t.startsWith('reticulum') || t.startsWith('rns') ||
      t === 'tcp' || t === 'mqtt_rns')                     return 'bg-violet-500/15 border-violet-500/40 text-violet-100'
  return 'bg-slate-500/15 border-slate-500/40 text-slate-100'
})

const outgoing = computed(() => (props.message.direction || '').toLowerCase() === 'out' ||
                                 (props.message.direction || '').toLowerCase() === 'sent')

const time = computed(() => formatRelativeTime(props.message.created_at || props.message.rx_time))
</script>

<template>
  <div class="flex w-full" :class="outgoing ? 'justify-end' : 'justify-start'">
    <div class="max-w-[75%] rounded-lg border px-3 py-2 text-sm"
      :class="palette">
      <div class="flex items-center gap-2 mb-1 text-[9px] uppercase tracking-wide opacity-70">
        <span>{{ message.transport || 'unknown' }}</span>
        <span v-if="message.from_node" class="font-mono">{{ message.from_node }}</span>
        <span class="ml-auto">{{ time }}</span>
      </div>
      <div class="whitespace-pre-wrap break-words">{{ message.decoded_text || message.text || '(empty)' }}</div>
      <div v-if="outgoing" class="flex justify-end mt-1">
        <DeliveryTicks :status="message.delivery_status" :read="!!message.read" :per-bearer="message.per_bearer || []" />
      </div>
    </div>
  </div>
</template>
