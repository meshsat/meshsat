<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import api from '@/api/client'

const store = useMeshsatStore()
const events = ref([])
const expandedIdx = ref(null)
const typeFilter = ref('')
const callsignFilter = ref('')
let sseConn = null

// Connected clients: unique callsigns seen in last 5 minutes
const connectedClients = computed(() => {
  const cutoff = Date.now() - 300000
  const seen = {}
  for (const e of events.value) {
    if (new Date(e.timestamp).getTime() < cutoff) continue
    if (!e.callsign) continue
    if (!seen[e.callsign] || new Date(e.timestamp) > new Date(seen[e.callsign].timestamp)) {
      seen[e.callsign] = e
    }
  }
  return Object.values(seen).sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp))
})

// Rate metrics
const rateIn = computed(() => {
  const cutoff = Date.now() - 60000
  return events.value.filter(e => e.direction === 'inbound' && new Date(e.timestamp).getTime() > cutoff).length
})
const rateOut = computed(() => {
  const cutoff = Date.now() - 60000
  return events.value.filter(e => e.direction === 'outbound' && new Date(e.timestamp).getTime() > cutoff).length
})

// TAK gateway status from store
const takGw = computed(() => (store.gateways || []).find(g => g.type === 'tak'))

// Event type color
function typeColor(t) {
  if (!t) return 'text-gray-400'
  if (t.startsWith('a-f-')) return 'text-blue-400'
  if (t.startsWith('a-h-')) return 'text-red-400'
  if (t === 'b-a') return 'text-red-500'
  if (t === 'b-t-f') return 'text-cyan-400'
  if (t.startsWith('b-m-p')) return 'text-orange-400'
  if (t === 't-x-d-d') return 'text-purple-400'
  return 'text-gray-300'
}

function typeBadge(t) {
  if (!t) return 'Unknown'
  if (t.startsWith('a-f-G-U-C')) return 'PLI'
  if (t === 'b-t-f') return 'Chat'
  if (t === 'b-a') return 'SOS'
  if (t.startsWith('b-m-p')) return 'Waypoint'
  if (t === 't-x-d-d') return 'Sensor'
  if (t.startsWith('a-h-')) return 'Hostile'
  return t.split('-').slice(0, 3).join('-')
}

function dirIcon(d) { return d === 'inbound' ? '\u2B07' : '\u2B06' }
function dirColor(d) { return d === 'inbound' ? 'text-emerald-400' : 'text-amber-400' }

function formatTime(ts) {
  if (!ts) return ''
  const d = new Date(ts)
  return d.toLocaleTimeString('en-GB', { hour12: false }) + '.' + String(d.getMilliseconds()).padStart(3, '0')
}

function connectSSE() {
  let url = '/api/tak/events'
  const params = []
  if (typeFilter.value) params.push('type=' + encodeURIComponent(typeFilter.value))
  if (callsignFilter.value) params.push('callsign=' + encodeURIComponent(callsignFilter.value))
  if (params.length) url += '?' + params.join('&')

  if (sseConn) sseConn.close()
  sseConn = api.sse(url, (evt) => {
    events.value.unshift(evt)
    if (events.value.length > 500) events.value.length = 500
  })
}

onMounted(() => {
  store.fetchGateways()
  connectSSE()
})

onUnmounted(() => {
  if (sseConn) sseConn.close()
})
</script>

<template>
  <div class="max-w-5xl mx-auto space-y-4">
    <h2 class="text-lg font-semibold text-gray-200">TAK / CoT Monitor</h2>

    <!-- Top row: rate metrics + gateway status -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="bg-gray-800 rounded-lg border border-gray-700 p-3 text-center">
        <div class="text-2xl font-bold text-emerald-400">{{ rateIn }}</div>
        <div class="text-xs text-gray-500">Inbound /min</div>
      </div>
      <div class="bg-gray-800 rounded-lg border border-gray-700 p-3 text-center">
        <div class="text-2xl font-bold text-amber-400">{{ rateOut }}</div>
        <div class="text-xs text-gray-500">Outbound /min</div>
      </div>
      <div class="bg-gray-800 rounded-lg border border-gray-700 p-3 text-center">
        <div class="text-2xl font-bold text-cyan-400">{{ connectedClients.length }}</div>
        <div class="text-xs text-gray-500">Active Clients</div>
      </div>
      <div class="bg-gray-800 rounded-lg border border-gray-700 p-3 text-center">
        <div class="text-lg font-bold" :class="takGw?.connected ? 'text-emerald-400' : 'text-gray-500'">
          {{ takGw?.connected ? 'Connected' : 'Offline' }}
        </div>
        <div class="text-xs text-gray-500">TAK Gateway {{ takGw?.connection_uptime || '' }}</div>
      </div>
    </div>

    <!-- Connected clients panel -->
    <div class="bg-gray-800 rounded-lg border border-gray-700 p-4" v-if="connectedClients.length > 0">
      <h3 class="text-sm font-medium text-gray-300 mb-2">Active TAK Clients (last 5 min)</h3>
      <div class="grid grid-cols-2 md:grid-cols-4 gap-2">
        <div v-for="c in connectedClients" :key="c.callsign"
          class="flex items-center gap-2 bg-gray-900 rounded px-3 py-2">
          <span class="w-2 h-2 rounded-full bg-emerald-400"></span>
          <span class="text-sm text-gray-200 font-mono">{{ c.callsign }}</span>
          <span class="text-xs text-gray-500 ml-auto">{{ typeBadge(c.type) }}</span>
        </div>
      </div>
    </div>

    <!-- Filters -->
    <div class="flex gap-3 items-center">
      <input v-model="typeFilter" @change="connectSSE" placeholder="Filter by type (e.g. a-f)" class="px-3 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 w-40">
      <input v-model="callsignFilter" @change="connectSSE" placeholder="Filter by callsign" class="px-3 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 w-40">
      <span class="text-xs text-gray-500 ml-auto">{{ events.length }} events</span>
    </div>

    <!-- CoT event stream table -->
    <div class="bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
      <table class="w-full text-sm">
        <thead>
          <tr class="text-xs text-gray-500 border-b border-gray-700">
            <th class="px-3 py-2 text-left w-8"></th>
            <th class="px-3 py-2 text-left">Time</th>
            <th class="px-3 py-2 text-left">Type</th>
            <th class="px-3 py-2 text-left">Callsign</th>
            <th class="px-3 py-2 text-left">Position</th>
            <th class="px-3 py-2 text-left">UID</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="(evt, idx) in events.slice(0, 100)" :key="idx">
            <tr class="border-b border-gray-700/50 hover:bg-gray-700/30 cursor-pointer" @click="expandedIdx = expandedIdx === idx ? null : idx">
              <td class="px-3 py-1.5" :class="dirColor(evt.direction)">{{ dirIcon(evt.direction) }}</td>
              <td class="px-3 py-1.5 text-gray-400 font-mono text-xs">{{ formatTime(evt.timestamp) }}</td>
              <td class="px-3 py-1.5">
                <span class="px-1.5 py-0.5 rounded text-xs font-medium" :class="typeColor(evt.type)">{{ typeBadge(evt.type) }}</span>
              </td>
              <td class="px-3 py-1.5 text-gray-200 font-mono">{{ evt.callsign || '—' }}</td>
              <td class="px-3 py-1.5 text-gray-400 text-xs font-mono">
                <span v-if="evt.lat !== 0 || evt.lon !== 0">{{ evt.lat?.toFixed(4) }}, {{ evt.lon?.toFixed(4) }}</span>
                <span v-else class="text-gray-600">—</span>
              </td>
              <td class="px-3 py-1.5 text-gray-500 text-xs truncate max-w-[140px]">{{ evt.uid }}</td>
            </tr>
            <tr v-if="expandedIdx === idx" class="bg-gray-900/50">
              <td colspan="6" class="px-6 py-3 text-xs text-gray-400 font-mono whitespace-pre-wrap">
                Type: {{ evt.type }}
How: {{ evt.how }}
Direction: {{ evt.direction }}
Stale: {{ evt.stale }}
Detail: {{ evt.detail || '(none)' }}
              </td>
            </tr>
          </template>
          <tr v-if="events.length === 0">
            <td colspan="6" class="px-3 py-8 text-center text-gray-500">No CoT events yet. Events will appear when the TAK gateway is active.</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
