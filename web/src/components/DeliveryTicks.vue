<script setup>
import { computed, ref } from 'vue'

// WhatsApp-style delivery ticks. [MESHSAT-552]
//   · queued    → single dot
//   ✓ sent      → single check (grey-blue)
//   ✓✓ delivered → double check (grey)
//   ✓✓ read     → double check (accent blue)
//
// A "read" signal requires an explicit read receipt from the
// recipient. We don't have that on most bearers yet (mesh has
// ROUTING_APP acks, SBD doesn't ack at all), so the blue-double
// state is only rendered when the caller passes read=true — today
// that's wired from the Reticulum proof-of-receipt path.

const props = defineProps({
  // DeliveryStatus enum on message_deliveries row
  status: { type: String, default: '' },
  // true if we have a proof-of-receipt from the recipient
  read:   { type: Boolean, default: false },
  // Optional per-bearer breakdown {kind, status, error} — enables
  // click-to-expand popover.
  perBearer: { type: Array, default: () => [] }
})

const popover = ref(false)
function togglePopover() { if (props.perBearer.length) popover.value = !popover.value }

// Three-state derivation: dot / single-check / double-check
const shape = computed(() => {
  const s = (props.status || '').toLowerCase()
  if (['queued', 'pending', ''].includes(s))        return 'dot'
  if (['sending', 'retry'].includes(s))              return 'dot'
  if (['sent', 'mesh_sent', 'sat_sent'].includes(s)) return 'single'
  if (['delivered', 'confirmed', 'mesh_delivered', 'received'].includes(s)) return 'double'
  if (['failed', 'dead', 'cancelled'].includes(s))   return 'failed'
  return 'dot'
})

const tone = computed(() => {
  if (shape.value === 'failed') return 'text-red-400'
  if (props.read && shape.value === 'double') return 'text-tactical-iridium'
  if (shape.value === 'double') return 'text-emerald-400/70'
  if (shape.value === 'single') return 'text-emerald-400/60'
  return 'text-gray-500'
})
</script>

<template>
  <span class="relative inline-flex items-center">
    <button type="button" @click.stop="togglePopover"
      class="inline-flex items-center justify-center min-w-[20px] min-h-[20px]"
      :class="[tone, perBearer.length ? 'cursor-pointer' : 'cursor-default']"
      :title="status || 'queued'"
      :aria-label="'Delivery status: ' + (status || 'queued')">
      <!-- Failed -->
      <svg v-if="shape === 'failed'" class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M6 18L18 6M6 6l12 12"/></svg>
      <!-- Queued dot -->
      <svg v-else-if="shape === 'dot'" class="w-2 h-2" viewBox="0 0 8 8" fill="currentColor"><circle cx="4" cy="4" r="2"/></svg>
      <!-- Single check -->
      <svg v-else-if="shape === 'single'" class="w-4 h-3" viewBox="0 0 18 12" fill="none" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M1 6l5 5 11-10"/></svg>
      <!-- Double check -->
      <svg v-else class="w-5 h-3" viewBox="0 0 22 12" fill="none" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M1 7l4 4 9-9"/><path stroke-linecap="round" stroke-linejoin="round" d="M8 10l3 1 9-9"/></svg>
    </button>

    <!-- Per-bearer popover -->
    <div v-show="popover && perBearer.length"
      class="absolute bottom-full right-0 mb-1 w-56 bg-tactical-surface border border-tactical-border rounded shadow-lg z-30 p-2 space-y-1">
      <div class="text-[9px] uppercase tracking-wide text-gray-500 mb-1">Per-bearer</div>
      <div v-for="b in perBearer" :key="b.kind" class="flex items-center justify-between gap-2 text-[11px]">
        <span class="font-mono text-gray-400">{{ b.kind }}</span>
        <span v-if="b.error" class="text-red-400 truncate">{{ b.error }}</span>
        <span v-else class="text-emerald-400">{{ b.status || 'queued' }}</span>
      </div>
    </div>
  </span>
</template>
