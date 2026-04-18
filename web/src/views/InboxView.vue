<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import MessageBubble from '@/components/MessageBubble.vue'

// Unified inbox — bearer-coloured bubbles, WhatsApp-style ticks.
// [MESHSAT-552]
//
// Source of truth today: the mesh `messages` table. SMS, iridium,
// APRS, reticulum etc. all go through the delivery ledger and will
// be stitched into this view by MESHSAT-565 (unified inbound
// receive hook). For now we pull what's available:
//   - store.messages        (mesh)
//   - store.smsMessages     (cellular SMS)
//   - store.dlq             (iridium MT queue + DLQ)
// and normalise them to a single shape.

const store = useMeshsatStore()
const refreshing = ref(false)
const filterBearer = ref('')

onMounted(() => {
  refresh()
  store.fetchMessageStats()
  pollTimer = setInterval(refresh, 10_000)
})

let pollTimer = null
onUnmounted(() => { if (pollTimer) clearInterval(pollTimer) })

async function refresh() {
  refreshing.value = true
  try {
    await Promise.all([
      store.fetchMessages({ limit: 200 }),
      store.fetchSMSMessages({ limit: 200 }),
      store.fetchDLQ()
    ])
  } finally {
    refreshing.value = false
  }
}

// Normalise a mesh message row.
function fromMesh(m) {
  return {
    id: 'mesh-' + m.id,
    text: m.decoded_text || m.text,
    transport: m.transport || 'mesh',
    direction: m.direction,
    from_node: m.from_node,
    to_node: m.to_node,
    created_at: m.created_at || (m.rx_time ? new Date(m.rx_time * 1000).toISOString() : null),
    delivery_status: m.delivery_status,
    read: false
  }
}

// Normalise an SMS row. Our SMS records don't carry per-bearer
// breakdown (there's only one bearer); map the status directly.
function fromSMS(s) {
  return {
    id: 'sms-' + (s.id || s.phone + '-' + s.created_at),
    text: s.text,
    transport: 'sms',
    direction: s.direction || (s.is_outgoing ? 'out' : 'in'),
    from_node: s.phone,
    created_at: s.created_at,
    delivery_status: s.status || 'sent',
    read: false
  }
}

// Normalise an iridium queue entry. Only outbound exists in the DLQ.
function fromIridium(q) {
  return {
    id: 'sat-' + q.id,
    text: q.message || q.text,
    transport: 'iridium',
    direction: 'out',
    created_at: q.created_at || q.queued_at,
    delivery_status: q.status || 'queued',
    read: false
  }
}

const unified = computed(() => {
  const list = []
  for (const m of (store.messages || [])) list.push(fromMesh(m))
  for (const s of (store.smsMessages || [])) list.push(fromSMS(s))
  for (const q of (store.dlq || [])) list.push(fromIridium(q))
  // Newest first.
  list.sort((a, b) => {
    const ta = new Date(a.created_at || 0).getTime()
    const tb = new Date(b.created_at || 0).getTime()
    return tb - ta
  })
  return list
})

const filtered = computed(() => {
  if (!filterBearer.value) return unified.value
  return unified.value.filter(m => (m.transport || '').toLowerCase().includes(filterBearer.value))
})

// Bearer filter chips — only show kinds that actually appear in the
// current list so the strip stays tight on a field kit.
const bearers = computed(() => {
  const set = new Set()
  for (const m of unified.value) set.add((m.transport || 'unknown').toLowerCase())
  return Array.from(set)
})

const bearerColour = (k) => {
  if (k.startsWith('mesh')) return 'text-blue-400 border-blue-500/40'
  if (k === 'sms' || k.startsWith('cellular')) return 'text-emerald-400 border-emerald-500/40'
  if (k.startsWith('iridium') || k === 'sbd' || k === 'imt') return 'text-amber-400 border-amber-500/40'
  if (k === 'aprs' || k.startsWith('ax25')) return 'text-teal-400 border-teal-500/40'
  if (k.startsWith('reticulum') || k.startsWith('rns') || k === 'tcp' || k === 'mqtt_rns') return 'text-violet-400 border-violet-500/40'
  return 'text-gray-400 border-gray-500/40'
}
</script>

<template>
  <div class="max-w-3xl mx-auto space-y-3">
    <header class="flex items-center justify-between">
      <h1 class="text-lg font-display font-semibold text-gray-200 tracking-wide">Inbox</h1>
      <div class="flex items-center gap-2">
        <router-link to="/compose"
          class="px-3 py-1.5 rounded bg-tactical-iridium text-tactical-bg text-xs font-semibold min-h-[36px] flex items-center gap-1">
          <svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 20h4L20 8l-4-4L4 16v4z"/><path d="M14 6l4 4"/></svg>
          Compose
        </router-link>
        <button type="button" @click="refresh" :disabled="refreshing"
          class="px-2 py-1.5 rounded border border-tactical-border text-gray-400 text-xs min-h-[36px]"
          :class="{ 'animate-spin': refreshing }"
          aria-label="Refresh">
          <svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
        </button>
      </div>
    </header>

    <!-- Bearer filter chips -->
    <div v-if="bearers.length > 1" class="flex flex-wrap gap-1.5">
      <button type="button" @click="filterBearer = ''"
        class="px-2 py-1 rounded border text-[10px] font-medium uppercase tracking-wide"
        :class="filterBearer === '' ? 'border-tactical-iridium text-tactical-iridium bg-tactical-iridium/10' : 'border-tactical-border text-gray-500 hover:text-gray-300'">
        All
      </button>
      <button v-for="k in bearers" :key="k" type="button" @click="filterBearer = k"
        class="px-2 py-1 rounded border text-[10px] font-medium uppercase tracking-wide"
        :class="filterBearer === k ? bearerColour(k) + ' bg-white/5' : 'border-tactical-border text-gray-500 hover:text-gray-300'">
        {{ k }}
      </button>
    </div>

    <!-- Messages -->
    <div v-if="filtered.length" class="space-y-2">
      <MessageBubble v-for="m in filtered" :key="m.id" :message="m" />
    </div>
    <div v-else class="text-center py-8 text-sm text-gray-500">
      <span v-if="refreshing">Loading…</span>
      <span v-else>No messages yet — <router-link to="/compose" class="text-tactical-iridium">compose the first one</router-link>.</span>
    </div>
  </div>
</template>
