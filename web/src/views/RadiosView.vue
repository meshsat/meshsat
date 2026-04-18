<script setup>
import { computed, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

// Radios — Operator-friendly view over the interface/health layer.
// One row per bearer family with a plain-English label, health dot,
// signal bars where available, and "Details" that drops into the
// Engineer Interfaces view. [MESHSAT-554]
//
// The acceptance criteria say "Satellite" not "Iridium IMT 0".
// We pick one representative interface per family, prefer the
// online/active one, and route the Details link to the Interfaces
// view so Engineer can still drill down by instance.

const store = useMeshsatStore()

onMounted(() => {
  store.fetchInterfaces()
  store.fetchHealthScores()
  store.fetchIridiumSignalFast()
  store.fetchCellularSignal()
  pollTimer = setInterval(() => {
    store.fetchInterfaces()
    store.fetchHealthScores()
    store.fetchIridiumSignalFast()
    store.fetchCellularSignal()
  }, 10_000)
})
let pollTimer = null
onUnmounted(() => { if (pollTimer) clearInterval(pollTimer) })

// Family definitions — how each bearer family gets labelled, what
// interface-id prefixes/kinds it matches, and what extra datum to
// render (bars / NOW-pass / SNR / "---").
const families = [
  { id: 'mesh',      label: 'Mesh',           match: (i) => i.channel_type === 'mesh' || i.id.startsWith('mesh'),                     extra: 'snr'  },
  { id: 'sat',       label: 'Satellite',      match: (i) => ['iridium','iridium_imt','iridium_sbd','imt','sbd'].includes(i.channel_type) || i.id.startsWith('iridium'),
                                                                                                                                      extra: 'satBars' },
  { id: 'cell',      label: 'Cell',           match: (i) => ['sms','cellular'].includes(i.channel_type) || i.id.startsWith('sms') || i.id.startsWith('cellular'),
                                                                                                                                      extra: 'cellBars' },
  { id: 'aprs',      label: 'APRS',           match: (i) => ['aprs','ax25'].includes(i.channel_type) || i.id.startsWith('ax25') || i.id.startsWith('aprs'),
                                                                                                                                      extra: 'none' },
  { id: 'tak',       label: 'TAK',            match: (i) => i.channel_type === 'tak' || i.id.startsWith('tak'),                       extra: 'none' },
  { id: 'reticulum', label: 'Reticulum (TCP)', match: (i) => ['tcp','mqtt_rns','reticulum'].includes(i.channel_type) || i.id.startsWith('tcp') || i.id.startsWith('mqtt_rns'),
                                                                                                                                      extra: 'none' },
  { id: 'zigbee',    label: 'ZigBee',         match: (i) => i.channel_type === 'zigbee' || i.id.startsWith('zigbee'),                 extra: 'none' },
  { id: 'ble',       label: 'Bluetooth LE',   match: (i) => i.channel_type === 'ble' || i.id.startsWith('ble'),                       extra: 'none' },
  { id: 'webhook',   label: 'Webhooks',       match: (i) => i.channel_type === 'webhook' || i.id.startsWith('webhook'),               extra: 'none' }
]

// Pick the best representative interface per family — prefer
// online/enabled, then any, else null.
function pickRepresentative(match) {
  const ifaces = store.interfaces || []
  const matched = ifaces.filter(match)
  if (!matched.length) return null
  const online = matched.find(i => i.state === 'online' || i.status === 'online' || i.enabled)
  return online || matched[0]
}

function healthScoreFor(ifaceId) {
  const score = (store.healthScores || []).find(s => s.interface_id === ifaceId)
  if (!score) return null
  return score.score ?? null
}

function healthDot(score, iface) {
  if (!iface || !iface.enabled) return 'bg-gray-600'
  if (score === null) return 'bg-gray-400'
  if (score <= 0)    return 'bg-red-400'
  if (score < 50)    return 'bg-amber-400'
  return 'bg-emerald-400'
}

function bearerColour(id) {
  switch (id) {
    case 'mesh':      return 'text-blue-400'
    case 'sat':       return 'text-amber-400'
    case 'cell':      return 'text-sky-400'
    case 'aprs':      return 'text-teal-400'
    case 'tak':       return 'text-violet-400'
    case 'reticulum': return 'text-violet-400'
    case 'zigbee':    return 'text-fuchsia-400'
    case 'ble':       return 'text-indigo-400'
    default:          return 'text-gray-400'
  }
}

const rows = computed(() => families.map(f => {
  const iface = pickRepresentative(f.match)
  const score = iface ? healthScoreFor(iface.id) : null
  return {
    family: f,
    iface,
    score,
    satBars: f.id === 'sat' ? (store.iridiumSignal?.bars ?? -1) : null,
    cellBars: f.id === 'cell' ? (store.cellularSignal?.bars ?? -1) : null,
    meshSnr: f.id === 'mesh' ? meshAvgSNR.value : null
  }
}))

const meshAvgSNR = computed(() => {
  const cutoff = Date.now() / 1000 - 7200
  const active = (store.nodes || []).filter(n => n.last_heard > cutoff && n.snr != null && Math.abs(n.snr) < 100)
  if (!active.length) return null
  return active.reduce((sum, n) => sum + n.snr, 0) / active.length
})
</script>

<template>
  <div class="max-w-3xl mx-auto space-y-3">
    <header class="flex items-center justify-between">
      <h1 class="text-lg font-display font-semibold text-gray-200 tracking-wide">Radios</h1>
      <router-link v-show="store.isEngineer" to="/interfaces"
        class="px-3 py-1.5 rounded border border-tactical-border text-gray-400 text-xs min-h-[36px]">
        Engineer view →
      </router-link>
    </header>

    <div class="bg-tactical-surface border border-tactical-border rounded divide-y divide-tactical-border overflow-hidden">
      <div v-for="row in rows" :key="row.family.id"
        class="flex items-center gap-3 px-3 py-3 min-h-[56px]">
        <!-- Health dot -->
        <span class="w-2.5 h-2.5 rounded-full shrink-0" :class="healthDot(row.score, row.iface)" />

        <!-- Family label -->
        <div class="min-w-0 flex-1">
          <div class="text-sm font-semibold" :class="row.iface ? bearerColour(row.family.id) : 'text-gray-600'">
            {{ row.family.label }}
          </div>
          <div class="text-[10px] text-gray-500 truncate">
            <template v-if="!row.iface">Not configured</template>
            <template v-else>
              {{ row.iface.label || row.iface.id }}
              <span v-if="row.iface.state || row.iface.status">· {{ row.iface.state || row.iface.status }}</span>
              <span v-if="row.score !== null"> · health {{ row.score }}/100</span>
            </template>
          </div>
        </div>

        <!-- Signal/Extra -->
        <div class="shrink-0 text-right">
          <div v-if="row.family.id === 'sat'" class="flex items-end gap-px h-3">
            <span v-for="i in 5" :key="i" class="w-[3px] rounded-[1px]"
              :class="row.satBars >= i ? 'bg-amber-400' : 'bg-gray-700/50'"
              :style="{ height: `${3 + i * 2}px` }" />
          </div>
          <div v-else-if="row.family.id === 'cell'" class="flex items-end gap-px h-3">
            <span v-for="i in 5" :key="'c'+i" class="w-[3px] rounded-[1px]"
              :class="row.cellBars >= i ? 'bg-sky-400' : 'bg-gray-700/50'"
              :style="{ height: `${3 + i * 2}px` }" />
          </div>
          <div v-else-if="row.family.id === 'mesh' && row.meshSnr !== null" class="text-[10px] font-mono"
            :class="row.meshSnr >= 0 ? 'text-emerald-400/80' : row.meshSnr >= -10 ? 'text-amber-400/80' : 'text-red-400/80'">
            {{ row.meshSnr.toFixed(0) }} dB
          </div>
          <div v-else class="text-[10px] text-gray-600">—</div>
        </div>

        <!-- Details affordance — Engineer always, Operator for actionable rows -->
        <router-link v-if="row.iface" to="/interfaces"
          class="shrink-0 text-[10px] text-gray-500 hover:text-gray-300 px-2 py-1 rounded border border-tactical-border min-h-[32px] flex items-center"
          :aria-label="'Details for ' + row.family.label">
          Details
        </router-link>
      </div>
    </div>

    <!-- Footnote -->
    <div class="text-[10px] text-gray-500 px-1">
      Rows group physical bearers into operator-friendly families. Engineer
      view (<router-link to="/interfaces" class="underline">Interfaces</router-link>)
      shows every instance, transforms, and access rules.
    </div>
  </div>
</template>
