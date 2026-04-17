<!--
  SpectrumView.vue
  ----------------
  Dedicated RF spectrum page. Hosts the full per-band waterfall plus
  an alert-management panel so the operator can toggle the global
  popup, manage per-band mutes, and review recent transitions from a
  single place without digging through the modal.
-->
<script setup>
import { computed } from 'vue'
import { useSpectrumStore } from '@/stores/spectrum'
import SpectrumWaterfall from '@/components/SpectrumWaterfall.vue'

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
  </div>
</template>
