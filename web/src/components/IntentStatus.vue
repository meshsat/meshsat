<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

// One delivery intent's status (MESHSAT_DELIVERY_INTENTS): to whom, the
// outcome in the bridge's own words (r3 D11: "accepted, not confirmed" is
// never shown as delivered), what is known per route, the money, and
// Cancel while it runs. Fetched when shown, even from a snapshot, then
// polled while the intent is active (every 2 s for 3 minutes, then 10 s)
// and, once closed, every 10 s for up to 15 minutes while a transmission
// is still with a radio or modem: a closed intent's outcome and cost can
// still change when that settles (review of B10). Refresh fetches it on
// demand. [MESHSAT-1584]

const props = defineProps({
  intentId: { type: String, required: true },
  initial: { type: Object, default: null }
})
const emit = defineEmits(['closed'])

const store = useMeshsatStore()
const view = ref(props.initial)
const loadError = ref('')
const cancelling = ref(false)
const checkedAt = ref(null)
let timer = null
let startedAt = Date.now()
let closedAt = 0
let stopped = false
let closedEmitted = false
// Each read and each cancel takes a generation; an answer applies only
// when it is still the newest for this intent, so a slow Refresh, a poll
// or a read from before Cancel never overwrites a newer status (review
// B10-R5).
let gen = 0

const active = computed(() => view.value?.state === 'active')
// A transmission authorized or handed off and not yet done: its outcome
// and charge are still to come.
const unsettled = computed(() => (view.value?.routes || []).some(r =>
  (r.attempts || []).some(a => (a.transmissions || []).some(t => t.state === 'authorized' || t.state === 'handed_off'))))

const recipient = computed(() => {
  const id = view.value?.contact_id
  if (!id) return ''
  const list = Array.isArray(store.directoryContacts) ? store.directoryContacts : []
  const c = list.find(x => x.id === id)
  return c ? c.display_name : id.slice(0, 8)
})

async function load() {
  if (timer) { clearTimeout(timer); timer = null }
  const my = ++gen
  const id = props.intentId
  let next = null
  let err = ''
  try {
    next = await store.fetchIntent(id)
  } catch (e) {
    err = e.message || 'Status not available'
  }
  if (my !== gen || id !== props.intentId || stopped) return
  if (next) {
    view.value = next
    loadError.value = ''
    checkedAt.value = new Date()
  } else {
    loadError.value = err
  }
  schedule()
}

function schedule() {
  if (stopped || timer) return
  if (view.value && !active.value) {
    if (!closedAt) closedAt = Date.now()
    if (!closedEmitted) { closedEmitted = true; emit('closed', view.value) }
    if (unsettled.value && Date.now() - closedAt < 900000) timer = setTimeout(load, 10000)
    return
  }
  const every = Date.now() - startedAt < 180000 ? 2000 : 10000
  timer = setTimeout(load, loadError.value ? 10000 : every)
}

onMounted(load)
onUnmounted(() => { stopped = true; if (timer) clearTimeout(timer) })
watch(() => props.intentId, () => {
  if (timer) clearTimeout(timer)
  timer = null
  gen++
  startedAt = Date.now()
  closedAt = 0
  closedEmitted = false
  view.value = null
  load()
})

async function onCancel() {
  if (!confirm('Stop this message? Nothing more is sent; an attempt already with a radio or modem may still go out.')) return
  cancelling.value = true
  // Reads started before this cancel are now stale (gen); a poll that
  // fires after it reads the newer state and is kept.
  const my = ++gen
  const id = props.intentId
  try {
    const next = await store.cancelIntent(id, 'cancelled from the compose screen')
    if (my === gen && id === props.intentId) {
      view.value = next
      checkedAt.value = new Date()
    }
  } catch (e) {
    if (my === gen) loadError.value = e.message || 'Cancel failed'
  } finally {
    cancelling.value = false
  }
  if (my === gen) schedule()
}

const tone = computed(() => {
  switch (view.value?.outcome) {
    case 'pending': return 'text-sky-400'
    case 'delivered_handset':
    case 'delivered_device':
    case 'accepted_destination_node':
    case 'acked_station': return 'text-emerald-400'
    case 'accepted_unconfirmed': return 'text-gray-200'
    case 'may_have_been_sent': return 'text-amber-400'
    case 'failed':
    case 'expired': return 'text-red-400'
    case 'cancelled': return 'text-gray-500'
  }
  return 'text-gray-300'
})

function money(minor) {
  const cur = view.value?.currency || 'EUR'
  try {
    return new Intl.NumberFormat('en-IE', { style: 'currency', currency: cur }).format((minor || 0) / 100)
  } catch {
    return `${cur} ${((minor || 0) / 100).toFixed(2)}`
  }
}

const paid = computed(() => (view.value?.routes || []).some(r => r.paid))
const kinds = { MESHTASTIC: 'mesh', RETICULUM: 'LXMF', IRIDIUM_IMT: 'satellite (IMT)', IRIDIUM_SBD: 'satellite (SBD)' }
function kindName(k) { return kinds[k] || (k || '').toLowerCase() }

function when(ts) {
  if (!ts) return ''
  const d = ts instanceof Date ? ts : new Date(ts.includes('T') ? ts : ts.replace(' ', 'T') + 'Z')
  return isNaN(d) ? String(ts) : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}
</script>

<template>
  <div class="border border-tactical-border rounded p-3 space-y-2 text-xs" :data-intent="intentId">
    <div v-if="!view" class="text-gray-500">
      <span v-if="loadError" class="text-red-400">{{ loadError }}</span>
      <span v-else>Loading status…</span>
    </div>
    <template v-else>
      <div class="flex items-start justify-between gap-2">
        <div class="min-w-0">
          <div class="font-semibold text-sm" :class="tone" data-role="label">
            <span v-if="active" class="inline-block w-2 h-2 rounded-full bg-sky-400 animate-pulse mr-1.5 align-middle"></span>{{ view.label }}
          </div>
          <div class="text-gray-500 truncate mt-0.5" data-role="recipient">
            <span v-if="recipient">to {{ recipient }}</span><span v-if="recipient && view.text"> · </span><span v-if="view.text" :title="view.text">“{{ view.text }}”</span>
          </div>
        </div>
        <button v-if="active" type="button" @click="onCancel" :disabled="cancelling"
          class="shrink-0 px-3 py-1.5 rounded border border-tactical-border text-gray-400 text-[11px] min-h-[36px] disabled:opacity-40">
          <span v-if="cancelling">Stopping…</span><span v-else>Cancel</span>
        </button>
      </div>

      <div v-if="view.notes?.length" class="flex flex-wrap gap-1">
        <span v-for="n in view.notes" :key="n"
          class="px-2 py-0.5 rounded border border-amber-500/40 text-amber-400 text-[10px]">{{ n }}</span>
      </div>

      <ul class="space-y-1">
        <li v-for="r in view.routes" :key="r.route_index" class="flex flex-wrap items-baseline gap-x-2">
          <span class="font-mono text-gray-400">{{ r.route_index }}. {{ kindName(r.kind) }}</span>
          <span class="font-mono text-gray-500 truncate max-w-[12rem]" :title="r.destination">{{ r.destination }}</span>
          <span v-if="!r.admitted" class="text-gray-500">not used: {{ r.reason }}</span>
          <span v-else-if="r.facts?.length" class="text-gray-300">{{ r.facts.join('; ') }}</span>
          <span v-else class="text-gray-500">waiting</span>
        </li>
      </ul>

      <div v-if="paid" class="text-[10px] text-gray-500" data-role="money">
        Cost: {{ money(view.incurred_minor) }} spent<span v-if="view.possible_minor">, {{ money(view.possible_minor) }} possibly spent</span><span v-if="view.reserved_minor">, {{ money(view.reserved_minor) }} reserved</span>
      </div>

      <details v-if="store.isEngineer" class="text-[10px] text-gray-500">
        <summary class="cursor-pointer select-none">Attempts</summary>
        <div v-for="r in view.routes" :key="'a' + r.route_index">
          <div v-for="a in r.attempts || []" :key="a.delivery_id" class="mt-1">
            <div class="font-mono text-gray-400">
              #{{ a.delivery_id }} {{ kindName(r.kind) }} round {{ a.round }}: {{ a.status }}<span v-if="a.reason"> ({{ a.reason }})</span>
            </div>
            <div v-if="a.last_error" class="pl-3">{{ a.last_error }}</div>
            <div v-for="t in a.transmissions || []" :key="t.id" class="pl-3 font-mono">
              tx {{ t.gen }}: {{ t.state }}<span v-if="t.outcome_class"> {{ t.outcome_class }}</span><span v-if="t.accepted_by"> by {{ t.accepted_by }}</span><span v-if="t.charged_minor"> {{ money(t.charged_minor) }} {{ t.charge_state }}</span><span v-if="t.detail"> · {{ t.detail }}</span>
            </div>
          </div>
        </div>
      </details>

      <div class="flex items-center justify-between gap-2 text-[10px] text-gray-600">
        <span>
          {{ when(view.created_at) }}<span v-if="checkedAt"> · checked {{ when(checkedAt) }}</span><span v-if="!active && unsettled"> · still settling</span><span v-if="loadError" class="text-red-400"> · {{ loadError }}</span>
        </span>
        <button v-if="!active" type="button" @click="load"
          class="px-2 py-1 rounded border border-tactical-border text-gray-400 min-h-[32px]" data-role="refresh">Refresh</button>
      </div>
    </template>
  </div>
</template>
