<script setup>
// The public warnings this kit holds (MESHSAT-1734, docs/warning-relay.md):
// what the Hub handed it as a signed digest, what it went out on, and
// whether it is still in force. The list is the warnings store's, read on
// every `warning` event of the Bridge's event stream and on a slow poll;
// in force first, then newest first. Times are the operator's local time
// with the UTC form on hover; the words (headline, label, source, state)
// are the relay's own, so the page says what the mesh said.
import { computed, onMounted, onUnmounted } from 'vue'
import { useWarningsStore, bearers, expiry, headline, isInForce, label, sourceLabel } from '@/stores/warnings'

const store = useWarningsStore()

const BEARER_WORDS = {
  'mesh-alert': 'mesh alert',
  'mesh-text': 'mesh text',
  aprs: 'APRS bulletin',
  cell: '2G cell',
  'cell-cancel': '2G cell (cancel)',
  lxmf: 'LXMF'
}

// state is the one word for the row: in force, cancelled (a Cancel named
// it), replaced (an Update named it), a cancellation (the Cancel itself),
// or expired. A row marked cancelled with no later message in the list
// reads as cancelled: the Cancel may be older than what the list holds.
function state(w, all) {
  if (w.msg_type === 'Cancel') return 'cancellation'
  if (w.cancelled) {
    const later = all.find(o => o.ref === w.id && o.msg_type === 'Update')
    return later ? 'replaced' : 'cancelled'
  }
  if (isInForce(w, store.now)) return 'in force'
  return 'expired'
}

const rows = computed(() => {
  const all = store.rows
  const live = []
  const rest = []
  for (const w of all) (isInForce(w, store.now) ? live : rest).push(w)
  live.sort((a, b) => new Date(b.sent) - new Date(a.sent))
  return [...live, ...rest].map(w => ({ ...w, _state: state(w, all), _bearers: bearers(w) }))
})

function bearerWords(w) {
  const list = w._bearers.map(b => BEARER_WORDS[b] || b)
  return list.length ? list.join(', ') : 'none'
}

function viaWord(w) {
  return w.via === 'hub' ? 'Hub' : 'satellite'
}

function viaTitle(w) {
  return w.via === 'hub' ? 'over the Hub link (MQTT)' : `as a satellite MT on ${w.via || 'the satellite link'}`
}

function local(t) {
  const d = new Date(t)
  if (!t || isNaN(d.getTime())) return ''
  return d.toLocaleString([], { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
}

function utc(t) {
  const d = new Date(t)
  if (!t || isNaN(d.getTime())) return ''
  return d.toISOString().replace('T', ' ').slice(0, 16) + ' UTC'
}

function stateClass(s) {
  if (s === 'in force') return 'border-red-500/50 bg-red-950/40 text-red-200'
  if (s === 'cancellation' || s === 'cancelled') return 'border-gray-600/60 bg-gray-800/60 text-gray-300'
  return 'border-gray-700/60 bg-gray-800/40 text-gray-500'
}

onMounted(() => store.start())
onUnmounted(() => store.stop())
</script>

<template>
  <div class="max-w-5xl mx-auto">
    <div class="flex items-start gap-3 mb-1">
      <svg class="w-5 h-5 mt-0.5 shrink-0 text-gray-300" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round" aria-hidden="true">
        <path d="M12 3 2.5 20h19L12 3z"/><path d="M12 9v5"/><circle cx="12" cy="17" r=".6" fill="currentColor"/>
      </svg>
      <div class="min-w-0">
        <h1 class="text-lg font-semibold text-gray-200">Public warnings</h1>
        <p class="text-[12px] text-gray-500 mb-3">
          Official public warnings the Hub handed this kit for its area, as signed digests. Each one went out on the mesh,
          the APRS bulletin channel, the 2G cell and to LXMF peers where that bearer was up, marked as a relay and never
          as an official alert. In force first, then newest first.
        </p>
      </div>
    </div>

    <div v-if="store.error" class="bg-red-900/30 border border-red-700/50 text-red-200 text-[12px] rounded-lg p-3 mb-4">{{ store.error }}</div>

    <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 overflow-hidden">
      <table class="w-full text-sm block md:table" data-testid="warnings-table">
        <thead class="hidden md:table-header-group">
          <tr class="text-xs text-gray-500 border-b border-gray-700">
            <th class="px-3 py-2 text-left font-medium">Warning</th>
            <th class="px-3 py-2 text-left font-medium">Severity</th>
            <th class="px-3 py-2 text-left font-medium">Sent / expires</th>
            <th class="px-3 py-2 text-left font-medium">Source</th>
            <th class="px-3 py-2 text-left font-medium">Via</th>
            <th class="px-3 py-2 text-left font-medium">Went out on</th>
          </tr>
        </thead>
        <tbody class="block md:table-row-group">
          <tr v-for="w in rows" :key="w.id + '/' + w.msg_type"
            class="block md:table-row border-b border-gray-700/50 last:border-0 px-3 py-2 md:p-0"
            :class="w._state === 'in force' ? '' : 'text-gray-400'"
            :data-state="w._state">
            <td class="block md:table-cell md:px-3 md:py-2 align-top md:min-w-[18rem]">
              <div class="flex flex-wrap items-center gap-1.5">
                <span v-if="w.test" class="px-1.5 py-0.5 rounded border border-amber-500/50 text-amber-300 text-[10px] font-mono tracking-wider">TEST</span>
                <span v-if="w.msg_type === 'Update'" class="px-1.5 py-0.5 rounded border border-gray-500/60 text-gray-300 text-[10px] font-mono tracking-wider">UPDATE</span>
                <span v-if="w.msg_type === 'Cancel'" class="px-1.5 py-0.5 rounded border border-gray-500/60 text-gray-300 text-[10px] font-mono tracking-wider">CANCELLED</span>
                <span class="font-medium" :class="w._state === 'in force' ? 'text-gray-100' : ''">{{ w.event || 'Other' }}</span>
                <span class="px-1.5 py-0.5 rounded border text-[10px] font-mono tracking-wider" :class="stateClass(w._state)">{{ w._state }}</span>
              </div>
              <div v-if="w.text" class="text-[13px] mt-1 break-words max-w-[60ch]" :class="w._state === 'in force' ? 'text-gray-200' : 'text-gray-400'">{{ w.text }}</div>
              <div class="text-[11px] text-gray-500 mt-0.5 font-mono break-all">{{ headline(w) }}<span v-if="w.ref"> &middot; refers to {{ w.ref }}</span></div>
            </td>
            <td class="block md:table-cell md:px-3 md:py-2 align-top whitespace-nowrap mt-1 md:mt-0">
              <span class="md:hidden text-[11px] text-gray-500 mr-1">Severity</span>
              <span :class="w._state === 'in force' && w.severity === 'Extreme' ? 'text-red-300' : w._state === 'in force' && w.severity === 'Severe' ? 'text-amber-300' : ''">{{ w.severity || 'Unknown' }}</span>
              <div class="text-[11px] text-gray-500 inline md:block ml-1 md:ml-0">{{ [w.urgency, w.certainty, w.category].filter(Boolean).join(', ') }}</div>
            </td>
            <td class="block md:table-cell md:px-3 md:py-2 align-top whitespace-nowrap mt-1 md:mt-0">
              <span class="md:hidden text-[11px] text-gray-500 mr-1">Sent</span>
              <span :title="utc(w.sent)">{{ local(w.sent) }}</span>
              <div class="text-[11px] text-gray-500 inline md:block ml-1 md:ml-0">
                <template v-if="expiry(w)"><span class="md:hidden">,</span> until <span :title="utc(w.expires)">{{ local(w.expires) }}</span></template>
                <template v-else><span class="md:hidden">,</span> no expiry given</template>
              </div>
            </td>
            <td class="block md:table-cell md:px-3 md:py-2 align-top mt-1 md:mt-0">
              <span class="md:hidden text-[11px] text-gray-500 mr-1">Source</span>
              <span>{{ sourceLabel(w.source) }}</span>
              <div class="text-[11px] text-gray-500 max-w-[28ch] leading-tight mt-0.5">{{ label(w) }}</div>
            </td>
            <td class="block md:table-cell md:px-3 md:py-2 align-top whitespace-nowrap mt-1 md:mt-0">
              <span class="md:hidden text-[11px] text-gray-500 mr-1">Via</span>
              <span :title="viaTitle(w)">{{ viaWord(w) }}</span>
              <div class="text-[11px] text-gray-500 inline md:block ml-1 md:ml-0" :title="utc(w.received_at)">{{ local(w.received_at) }}</div>
            </td>
            <td class="block md:table-cell md:px-3 md:py-2 align-top mt-1 md:mt-0 text-[13px]">
              <span class="md:hidden text-[11px] text-gray-500 mr-1">Went out on</span>
              <span :class="w._bearers.length ? '' : 'text-gray-500'">{{ bearerWords(w) }}</span>
            </td>
          </tr>
          <tr v-if="store.loaded && rows.length === 0" class="block md:table-row">
            <td colspan="6" class="block md:table-cell px-3 py-8 text-center text-gray-500">
              No public warning has reached this kit. One is listed here when the Hub hands it a digest for its area, over the Hub link or over satellite.
            </td>
          </tr>
          <tr v-if="!store.loaded" class="block md:table-row">
            <td colspan="6" class="block md:table-cell px-3 py-8 text-center text-gray-500">Reading the warnings...</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
