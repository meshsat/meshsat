<!--
  SpectrumView.vue
  ----------------
  Dedicated RF spectrum page. Hosts the full per-band waterfall plus
  an alert-management panel so the operator can toggle the global
  popup, manage per-band mutes, and review recent transitions from a
  single place without digging through the modal.
-->
<script setup>
import { computed, ref, onMounted, onBeforeUnmount } from 'vue'
import { useSpectrumStore } from '@/stores/spectrum'
import SpectrumWaterfall from '@/components/SpectrumWaterfall.vue'
import { quickReferenceRows, miji9Footer } from '@/composables/useEccm'

const store = useSpectrumStore()

const mutedEntries = computed(() => {
  const now = Date.now()
  return Object.entries(store.mutedUntil)
    .filter(([, until]) => typeof until === 'number' && until > now)
    .map(([band, until]) => ({
      band,
      until,
      label: store.bands[band]?.meta?.label || band,
      remainingSec: Math.max(0, Math.floor((until - now) / 1000)),
    }))
    .sort((a, b) => a.until - b.until)
})

function fmtRemaining(sec) {
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`
  return `${Math.floor(sec / 3600)}h${Math.floor((sec % 3600) / 60)}m`
}

const historyRows = computed(() => {
  return store.alerts.slice(0, 30).map(a => ({
    ...a,
    durationSec: a.clearedAt
      ? Math.max(0, Math.floor((new Date(a.clearedAt) - new Date(a.startedAt)) / 1000))
      : null,
  }))
})

function fmtTs(iso) {
  if (!iso) return '—'
  try { return new Date(iso).toLocaleString() } catch { return iso }
}

// Quick-reference rows + MIJI-9 footer come from the shared locale
// module. Single source with SpectrumWaterfall's per-band banner.
const ECCM_TABLE = quickReferenceRows()

// 1 Hz tick powers the "last scan Ns ago" readout. We don't extend
// this to a general clock — other timestamp displays in this view
// (transition log) are static.
const nowMs = ref(Date.now())
let hwTicker = null
onMounted(() => {
  store.connect()
  hwTicker = setInterval(() => { nowMs.value = Date.now() }, 1000)
})
onBeforeUnmount(() => {
  if (hwTicker) { clearInterval(hwTicker); hwTicker = null }
})

function lastScanAgo() {
  const t = store.hardware?.last_scan_at
  if (!t || t.startsWith('0001-01-01')) return '—'
  const ms = Date.parse(t)
  if (!isFinite(ms)) return '—'
  const dt = Math.max(0, Math.floor((nowMs.value - ms) / 1000))
  if (dt < 60) return `${dt}s ago`
  if (dt < 3600) return `${Math.floor(dt / 60)}m ago`
  return `${Math.floor(dt / 3600)}h ago`
}
function lastErrorAgo() {
  const t = store.hardware?.last_scan_error_at
  if (!t || t.startsWith('0001-01-01')) return ''
  const ms = Date.parse(t)
  if (!isFinite(ms)) return ''
  const dt = Math.max(0, Math.floor((nowMs.value - ms) / 1000))
  if (dt < 60) return `${dt}s ago`
  if (dt < 3600) return `${Math.floor(dt / 60)}m ago`
  return `${Math.floor(dt / 3600)}h ago`
}
const scanStale = computed(() => {
  const t = store.hardware?.last_scan_at
  if (!t || t.startsWith('0001-01-01')) return false
  return (nowMs.value - Date.parse(t)) > 15000
})

// Relay status rows derived from store.relayStatus. Two rows expected
// (tak_cot + hub) but any destination registered server-side shows up
// automatically — future-proofs adding a webhook sink etc.
const RELAY_LABELS = {
  tak_cot: 'TAK CoT',
  hub: 'Hub MQTT',
}
const relayRows = computed(() => {
  const out = []
  const s = store.relayStatus || {}
  for (const key of Object.keys(s)) {
    const v = s[key]
    out.push({
      key,
      label: RELAY_LABELS[key] || key,
      successCount: v.success_count || 0,
      errorCount: v.error_count || 0,
      lastSuccessAt: v.last_success_at || '',
      lastErrorAt: v.last_error_at || '',
      lastError: v.last_error || '',
      lastAttemptAt: v.last_attempt_at || '',
    })
  }
  return out
})
function ago(iso) {
  if (!iso || iso.startsWith('0001-01-01')) return null
  const ms = Date.parse(iso)
  if (!isFinite(ms)) return null
  const dt = Math.max(0, Math.floor((nowMs.value - ms) / 1000))
  if (dt < 60) return `${dt}s ago`
  if (dt < 3600) return `${Math.floor(dt / 60)}m ago`
  return `${Math.floor(dt / 3600)}h ago`
}
</script>

<template>
  <div class="max-w-[1400px] mx-auto space-y-4">
    <div class="flex items-center justify-between">
      <div>
        <h1 class="text-lg font-semibold tracking-wide">RF Spectrum Monitor</h1>
        <p class="text-xs text-gray-500 mt-1">
          RTL-SDR jamming detection across 5 bands. State transitions relay via TAK/CoT to all
          connected parties and to the hub; the dashboard waterfall visualises the live baseline
          + per-bin power.
        </p>
      </div>
      <div class="flex items-center gap-3 text-[11px]">
        <span :class="store.connected ? 'text-emerald-400' : 'text-amber-400'">
          {{ store.connected ? 'SSE streaming' : (store.enabled ? 'reconnecting' : 'RTL-SDR not present') }}
        </span>
      </div>
    </div>

    <!-- RTL-SDR hardware status panel. Real values (not just the word
         "RTL-SDR") so the operator can answer "is the dongle alive
         and scanning?" without ssh'ing to the kit. -->
    <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4">
      <div class="flex items-center justify-between mb-2">
        <div class="text-sm font-semibold tracking-wide">
          RTL-SDR Hardware
          <span class="text-[10px] text-gray-500 ml-2 uppercase tracking-wider">scan loop health</span>
        </div>
        <span class="text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded"
              :class="store.hardware.available
                       ? 'bg-emerald-900/40 text-emerald-300'
                       : 'bg-red-900/40 text-red-300'">
          {{ store.hardware.available ? 'connected' : 'disconnected' }}
        </span>
      </div>
      <div class="grid grid-cols-2 md:grid-cols-4 gap-x-6 gap-y-2 text-[11px]">
        <div>
          <div class="text-gray-500 uppercase tracking-wider text-[9px]">Dongle</div>
          <div class="font-mono text-gray-200">
            {{ store.hardware.scanner?.dongle_vid || '—' }}:{{ store.hardware.scanner?.dongle_pid || '—' }}
            <span v-if="store.hardware.scanner?.product_name" class="text-gray-500">
              · {{ store.hardware.scanner.product_name }}
            </span>
          </div>
        </div>
        <div>
          <div class="text-gray-500 uppercase tracking-wider text-[9px]">USB path</div>
          <div class="font-mono text-gray-200">{{ store.hardware.scanner?.usb_path || '—' }}</div>
        </div>
        <div>
          <div class="text-gray-500 uppercase tracking-wider text-[9px]">Scanner binary</div>
          <div class="font-mono text-gray-200 truncate" :title="store.hardware.scanner?.binary_path">
            {{ store.hardware.scanner?.binary_path || '—' }}
          </div>
        </div>
        <div>
          <div class="text-gray-500 uppercase tracking-wider text-[9px]">Scan cadence</div>
          <div class="font-mono text-gray-200">
            every {{ store.hardware.scan_interval_sec || '—' }}s · last {{ store.hardware.last_scan_ms || 0 }}ms
          </div>
        </div>
        <div>
          <div class="text-gray-500 uppercase tracking-wider text-[9px]">Last scan</div>
          <div :class="scanStale ? 'text-amber-300 font-mono' : 'text-gray-200 font-mono'">
            {{ lastScanAgo() }}
          </div>
        </div>
        <div>
          <div class="text-gray-500 uppercase tracking-wider text-[9px]">Scan errors</div>
          <div :class="store.hardware.scan_error_count > 0 ? 'text-amber-300 font-mono' : 'text-gray-200 font-mono'">
            {{ store.hardware.scan_error_count || 0 }}
            <span v-if="store.hardware.scan_error_count > 0" class="text-gray-500">· {{ lastErrorAgo() }}</span>
          </div>
        </div>
        <div class="col-span-2">
          <div class="text-gray-500 uppercase tracking-wider text-[9px]">Last error</div>
          <div class="font-mono text-red-300 truncate" :title="store.hardware.last_scan_error">
            {{ store.hardware.last_scan_error || '—' }}
          </div>
        </div>
      </div>
    </div>

    <!-- MIJI/CoT + hub relay status. Per-destination success/failure
         counters plus last-attempt freshness. This is the operator's
         answer to "are my alerts making it out?" — separate from the
         hardware panel above (scanner health) because a healthy
         scanner with a wedged TAK server still means no alerts land. -->
    <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4">
      <div class="flex items-center justify-between mb-2">
        <div class="text-sm font-semibold tracking-wide">
          Alert Relay Status
          <span class="text-[10px] text-gray-500 ml-2 uppercase tracking-wider">MIJI / CoT + hub</span>
        </div>
      </div>
      <div v-if="relayRows.length === 0" class="text-[11px] text-gray-500 italic py-2">
        No relay attempts yet this session. The tracker populates on the first state transition.
      </div>
      <table v-else class="w-full text-[11px]">
        <thead>
          <tr class="text-left text-gray-500 uppercase tracking-wider">
            <th class="py-1.5">Destination</th>
            <th>Last success</th>
            <th>Last error</th>
            <th>Success</th>
            <th>Errors</th>
            <th>Detail</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in relayRows" :key="r.key" class="border-t border-tactical-border">
            <td class="py-1.5 font-mono text-gray-200">{{ r.label }}</td>
            <td class="font-mono">
              <span v-if="ago(r.lastSuccessAt)" class="text-emerald-300">{{ ago(r.lastSuccessAt) }}</span>
              <span v-else class="text-gray-500">—</span>
            </td>
            <td class="font-mono">
              <span v-if="ago(r.lastErrorAt)" class="text-red-300">{{ ago(r.lastErrorAt) }}</span>
              <span v-else class="text-gray-500">—</span>
            </td>
            <td class="font-mono text-emerald-300">{{ r.successCount }}</td>
            <td class="font-mono" :class="r.errorCount > 0 ? 'text-red-300' : 'text-gray-300'">
              {{ r.errorCount }}
            </td>
            <td class="text-gray-400 truncate max-w-[320px]" :title="r.lastError">
              {{ r.lastError || '—' }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Alert controls card: global popup toggle + muted bands -->
    <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4">
      <div class="flex items-center justify-between flex-wrap gap-3">
        <div class="flex items-center gap-3">
          <label class="inline-flex items-center gap-2 cursor-pointer select-none">
            <input type="checkbox"
              :checked="store.popupEnabled"
              @change="store.setPopupEnabled($event.target.checked)"
              class="w-4 h-4 accent-emerald-500" />
            <span class="text-sm font-semibold tracking-wide">
              Popup alerts
              <span :class="store.popupEnabled ? 'text-emerald-400' : 'text-red-400'">
                {{ store.popupEnabled ? 'ENABLED' : 'DISABLED' }}
              </span>
            </span>
          </label>
          <span class="text-[11px] text-gray-500">
            TAK/CoT and hub relays always fire regardless of this toggle.
          </span>
        </div>
        <button v-if="mutedEntries.length"
                type="button"
                class="text-[11px] px-3 py-1 rounded border border-gray-600 text-gray-300 hover:bg-white/5"
                @click="store.unmuteAll()">
          Unmute all {{ mutedEntries.length }} bands
        </button>
      </div>

      <div v-if="mutedEntries.length" class="mt-3 border-t border-tactical-border pt-3">
        <div class="text-[11px] uppercase tracking-wider text-gray-500 mb-2">Muted bands</div>
        <div class="flex flex-wrap gap-2">
          <div v-for="m in mutedEntries" :key="m.band"
               class="flex items-center gap-2 text-[11px] px-2 py-1 rounded bg-tactical-bg border border-tactical-border">
            <span class="font-mono">{{ m.band }}</span>
            <span class="text-gray-500">{{ m.label }}</span>
            <span class="text-amber-400">{{ fmtRemaining(m.remainingSec) }} left</span>
            <button type="button" class="text-gray-400 hover:text-white"
                    @click="store.unmuteBand(m.band)"
                    title="Unmute this band now">
              ×
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Full waterfall, per-band panels with baseline + thresholds -->
    <SpectrumWaterfall />

    <!-- Recent transitions history (includes acked ones so the operator
         can audit what happened while they were away from the screen). -->
    <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4">
      <div class="flex items-center justify-between mb-2">
        <div class="text-sm font-semibold tracking-wide">Recent transitions</div>
        <div class="text-[11px] text-gray-500">most recent 30 events</div>
      </div>
      <div v-if="historyRows.length === 0" class="text-[11px] text-gray-500 italic py-2">
        No transitions recorded in this session. Jamming or interference events will appear here.
      </div>
      <table v-else class="w-full text-[11px]">
        <thead>
          <tr class="text-left text-gray-500 uppercase tracking-wider">
            <th class="py-1.5">Band</th>
            <th>State</th>
            <th>Started</th>
            <th>Cleared</th>
            <th>Duration</th>
            <th>Peak dB</th>
            <th>Δ base</th>
            <th>ACK</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="a in historyRows" :key="a.band + a.startedAt"
              class="border-t border-tactical-border">
            <td class="py-1.5 font-mono">{{ a.band }}</td>
            <td>
              <span :class="{
                'text-red-400': a.state === 'jamming',
                'text-amber-400': a.state === 'interference',
              }">{{ a.state }}</span>
            </td>
            <td class="text-gray-400">{{ fmtTs(a.startedAt) }}</td>
            <td class="text-gray-400">{{ a.clearedAt ? fmtTs(a.clearedAt) : '—' }}</td>
            <td class="text-gray-400">{{ a.durationSec != null ? a.durationSec + 's' : 'ongoing' }}</td>
            <td class="font-mono">{{ a.peakDB?.toFixed?.(1) }}</td>
            <td class="font-mono">{{ (a.powerDB - a.baselineDB)?.toFixed?.(1) }}</td>
            <td :class="a.acked ? 'text-emerald-400' : 'text-red-400'">
              {{ a.acked ? 'acked' : 'open' }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- ECCM quick-reference (FM 3-12). Always visible — operators must
         know MIJI reactions in advance, not be reading them for the
         first time during an event. -->
    <div class="bg-tactical-surface rounded-lg border border-tactical-border p-4">
      <div class="flex items-center justify-between mb-2">
        <div class="text-sm font-semibold tracking-wide">
          ECCM Quick Reference
          <span class="text-[10px] text-gray-500 ml-2 uppercase tracking-wider">FM 3-12 anti-jam recommended actions</span>
        </div>
      </div>
      <table class="w-full text-[11px]">
        <thead>
          <tr class="text-left text-gray-500 uppercase tracking-wider">
            <th class="py-1.5 w-[140px]">Band</th>
            <th>On interference</th>
            <th>On jamming</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in ECCM_TABLE" :key="row.band"
              class="border-t border-tactical-border">
            <td class="py-1.5 font-mono text-gray-300">{{ row.label }}</td>
            <td class="text-amber-300">{{ row.interference }}</td>
            <td class="text-red-300">{{ row.jamming }}</td>
          </tr>
        </tbody>
      </table>
      <div class="text-[10px] text-gray-500 mt-3 leading-relaxed">
        {{ miji9Footer }}
      </div>
    </div>
  </div>
</template>
