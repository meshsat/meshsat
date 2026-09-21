<script setup>
// Bridge-to-bridge time sync: the other bridges that answered, and how often
// this bridge asks on each free interface. These are the 0x14 request and
// 0x15 reply packets an operator sees on a KISS modem or the mesh; the
// schedule is read-only, set by MESHSAT_TIMESYNC_DISCOVERY_MIN and the
// interface's bit rate (MESHSAT_AX25_BITRATE for AX.25). [MESHSAT-778]
import { ref, computed, onMounted, onUnmounted } from 'vue'
import api from '@/api/client'

const data = ref(null)
const error = ref('')
let timer = null

async function load() {
  try {
    data.value = await api.get('/timesync/peers')
    error.value = ''
  } catch (e) {
    error.value = e.message || String(e)
  }
}

onMounted(() => {
  load()
  timer = setInterval(load, 30000)
})
onUnmounted(() => clearInterval(timer))

const peers = computed(() => data.value?.peers || [])
const ifaces = computed(() => data.value?.interfaces || [])

function ago(ts) {
  if (!ts) return 'never'
  const s = Math.max(0, Math.round((Date.now() - new Date(ts).getTime()) / 1000))
  if (s < 90) return `${s} s ago`
  if (s < 5400) return `${Math.round(s / 60)} min ago`
  return `${Math.round(s / 3600)} h ago`
}

function period(sec) {
  if (!sec) return '-'
  return sec < 120 ? `${sec} s` : `${Math.round(sec / 60)} min`
}

function rate(bps) {
  if (!bps) return 'no limit'
  return bps >= 10000 ? `${Math.round(bps / 1000)} kbit/s` : `${bps} bit/s`
}
</script>

<template>
  <div class="bg-gray-800 rounded-lg p-4 border border-gray-700">
    <div class="flex items-center justify-between mb-1">
      <h3 class="text-sm font-medium text-gray-200">Time sync between bridges</h3>
      <button @click="load" class="text-[10px] px-2 py-0.5 rounded bg-gray-700 text-gray-300 hover:bg-gray-600">Refresh</button>
    </div>
    <p class="text-[10px] text-gray-500 mb-3">
      Bridges compare clocks with a 26-byte request (packet type 0x14) and a reply (0x15) on the free
      interfaces, never on paid ones. Where another bridge answered in the last 10 minutes this bridge
      asks every {{ data?.request_interval_sec || 30 }} s; elsewhere once every
      {{ period(data?.discovery_interval_sec) }}. A slow link asks less often so the request stays
      within 2 % of its airtime.
    </p>

    <p v-if="error" class="text-[10px] text-amber-400 mb-2">Could not load: {{ error }}</p>
    <p v-else-if="data && !data.enabled" class="text-xs text-gray-500">Time sync is not running on this bridge.</p>

    <template v-else-if="data">
      <div class="text-[10px] text-gray-500 uppercase tracking-wide mb-1">Other bridges ({{ peers.length }})</div>
      <div v-if="peers.length === 0" class="text-xs text-gray-500 mb-3">
        No other bridge has answered yet.
      </div>
      <div v-else class="overflow-x-auto mb-3">
        <table class="w-full text-xs">
          <thead>
            <tr class="text-[10px] text-gray-500 text-left">
              <th class="font-normal pr-3 py-1">Bridge</th>
              <th class="font-normal pr-3 py-1">Via</th>
              <th class="font-normal pr-3 py-1">Stratum</th>
              <th class="font-normal pr-3 py-1">Offset</th>
              <th class="font-normal pr-3 py-1">Round trip</th>
              <th class="font-normal py-1">Heard</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in peers" :key="p.dest_hash" class="border-t border-gray-700/60" :class="p.stale ? 'text-gray-500' : 'text-gray-200'">
              <td class="pr-3 py-1 font-mono" :title="p.dest_hash">{{ p.dest_hash.slice(0, 12) }}</td>
              <td class="pr-3 py-1 font-mono">{{ p.iface || '-' }}</td>
              <td class="pr-3 py-1">{{ p.stratum }}</td>
              <td class="pr-3 py-1">{{ p.offset_ms.toFixed(0) }} ms</td>
              <td class="pr-3 py-1">{{ p.last_rtt_ms.toFixed(0) }} ms</td>
              <td class="py-1">{{ ago(p.last_seen) }}<span v-if="p.stale" class="ml-1 text-[10px]">(stale)</span></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="text-[10px] text-gray-500 uppercase tracking-wide mb-1">Requests per interface</div>
      <div v-if="ifaces.length === 0" class="text-xs text-gray-500">No free interface to ask on.</div>
      <div v-else class="overflow-x-auto">
        <table class="w-full text-xs">
          <thead>
            <tr class="text-[10px] text-gray-500 text-left">
              <th class="font-normal pr-3 py-1">Interface</th>
              <th class="font-normal pr-3 py-1">Mode</th>
              <th class="font-normal pr-3 py-1">Every</th>
              <th class="font-normal pr-3 py-1">Link</th>
              <th class="font-normal py-1">Last request</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="i in ifaces" :key="i.iface" class="border-t border-gray-700/60 text-gray-200">
              <td class="pr-3 py-1 font-mono">{{ i.iface }}</td>
              <td class="pr-3 py-1">
                <span class="text-[10px] px-1.5 py-0.5 rounded"
                  :class="i.mode === 'peer' ? 'bg-gray-600 text-gray-100' : 'bg-gray-900 text-gray-400 border border-gray-700'">
                  {{ i.mode === 'peer' ? 'peer present' : 'discovery' }}
                </span>
              </td>
              <td class="pr-3 py-1">{{ period(i.period_sec) }}</td>
              <td class="pr-3 py-1 text-gray-400">{{ rate(i.bitrate_bps) }}</td>
              <td class="py-1 text-gray-400">{{ ago(i.last_request_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
    <p v-else class="text-xs text-gray-500">Loading…</p>
  </div>
</template>
