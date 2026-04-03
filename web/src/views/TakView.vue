<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import api from '@/api/client'

const store = useMeshsatStore()
const activeTab = ref('events')
const tabs = [
  { id: 'events', label: 'Events' },
  { id: 'certificates', label: 'Certificates' },
  { id: 'missions', label: 'Missions' },
  { id: 'packages', label: 'Data Packages' },
  { id: 'sa', label: 'SA Snapshot' },
  { id: 'chat', label: 'GeoChat' },
  { id: 'nineline', label: '9-Line' },
]

// ═══ Events tab state ═══
const events = ref([])
const expandedIdx = ref(null)
const typeFilter = ref('')
const callsignFilter = ref('')
let sseConn = null

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

const rateIn = computed(() => {
  const cutoff = Date.now() - 60000
  return events.value.filter(e => e.direction === 'inbound' && new Date(e.timestamp).getTime() > cutoff).length
})
const rateOut = computed(() => {
  const cutoff = Date.now() - 60000
  return events.value.filter(e => e.direction === 'outbound' && new Date(e.timestamp).getTime() > cutoff).length
})

const takGw = computed(() => (store.gateways || []).find(g => g.type === 'tak'))

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
  let url = '/tak/events'
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

// ═══ Missions tab state ═══
const missions = ref([])
const missionsLoading = ref(false)
const missionsError = ref('')
const subscribingMission = ref('')

async function fetchMissions() {
  missionsLoading.value = true
  missionsError.value = ''
  try {
    const data = await api.get('/tak/missions')
    missions.value = data || []
  } catch (e) {
    missionsError.value = e.message
    missions.value = []
  } finally {
    missionsLoading.value = false
  }
}

async function subscribeMission(name) {
  subscribingMission.value = name
  try {
    await api.post(`/tak/missions/${encodeURIComponent(name)}/subscribe`)
  } catch (e) {
    missionsError.value = e.message
  } finally {
    subscribingMission.value = ''
  }
}

// ═══ Data Packages tab state ═══
const uploadFile = ref(null)
const uploading = ref(false)
const uploadResult = ref(null)
const uploadError = ref('')
const downloadHash = ref('')
const downloading = ref(false)

async function uploadPackage() {
  if (!uploadFile.value) return
  uploading.value = true
  uploadError.value = ''
  uploadResult.value = null
  try {
    const result = await api.upload('/tak/upload', uploadFile.value)
    uploadResult.value = result
    uploadFile.value = null
  } catch (e) {
    uploadError.value = e.message
  } finally {
    uploading.value = false
  }
}

function downloadPackage() {
  if (!downloadHash.value) return
  downloading.value = true
  window.open(`/api/tak/download?hash=${encodeURIComponent(downloadHash.value)}`, '_blank')
  setTimeout(() => { downloading.value = false }, 1000)
}

// ═══ SA Snapshot tab state ═══
const saData = ref('')
const saLoading = ref(false)
const saError = ref('')

async function fetchSASnapshot() {
  saLoading.value = true
  saError.value = ''
  try {
    const resp = await fetch('/api/tak/sa')
    if (!resp.ok) {
      const text = await resp.text()
      throw new Error(text || `HTTP ${resp.status}`)
    }
    saData.value = await resp.text()
  } catch (e) {
    saError.value = e.message
    saData.value = ''
  } finally {
    saLoading.value = false
  }
}

function formatDateTime(iso) {
  if (!iso) return ''
  try {
    return new Date(iso).toLocaleString('en-GB', { hour12: false })
  } catch { return iso }
}

// ═══ Certificates tab state ═══
const certData = ref(null)
const certLoading = ref(false)
const enrolling = ref(false)
const enrollForm = ref({ server_url: '', username: '', password: '' })
const enrollResult = ref(null)

async function loadCertificates() {
  certLoading.value = true
  try {
    certData.value = await api.get('/tak/certificates')
  } catch {
    certData.value = { enrolled: false, certificates: [], alerts: [] }
  } finally {
    certLoading.value = false
  }
}

async function enrollCert() {
  if (!enrollForm.value.server_url || !enrollForm.value.username || !enrollForm.value.password) return
  enrolling.value = true
  enrollResult.value = null
  try {
    const res = await api.post('/tak/enroll', enrollForm.value)
    enrollResult.value = res
    if (res.success) {
      enrollForm.value.password = ''
      await loadCertificates()
    }
  } catch (e) {
    enrollResult.value = { success: false, error: e.message || 'Enrollment failed' }
  } finally {
    enrolling.value = false
  }
}

function statusBadgeClass(status) {
  switch (status) {
    case 'valid': return 'bg-emerald-900/50 text-emerald-400 border-emerald-700'
    case 'expiring': return 'bg-amber-900/50 text-amber-400 border-amber-700'
    case 'expired': return 'bg-red-900/50 text-red-400 border-red-700'
    default: return 'bg-gray-900/50 text-gray-400 border-gray-700'
  }
}

function daysLeftLabel(d) {
  if (d < 0) return 'Expired'
  if (d === 0) return 'Expires today'
  if (d === 1) return '1 day left'
  return d + ' days left'
}

const certAlertCount = computed(() => certData.value?.alerts?.length || 0)

onMounted(() => {
  store.fetchGateways()
  connectSSE()
  loadCertificates()
})

// ── GeoChat ──
const chatMessages = computed(() => events.value.filter(e => e.type === "b-t-f"))
const chatInput = ref("")
async function sendChat() {
  if (!chatInput.value.trim()) return
  try {
    await api.post("/api/tak/chat", { text: chatInput.value })
    chatInput.value = ""
  } catch (e) { alert(e.response?.data?.error || e.message) }
}

// ── 9-Line MEDEVAC ──
const nineLineForm = ref({ location: "", freq: "", patients: "1", urgency: "Urgent", security: "No Enemy", marking: "Panels", nationality: "US", terrain: "Open", equipment: "None" })
const nineLineSubmitting = ref(false)
async function submitNineLine() {
  nineLineSubmitting.value = true
  const lines = [
    "1. Location: " + nineLineForm.value.location,
    "2. Frequency: " + nineLineForm.value.freq,
    "3. Patients: " + nineLineForm.value.patients,
    "4. Equipment: " + nineLineForm.value.equipment,
    "5. Patients Type: Litter",
    "6. Security: " + nineLineForm.value.security,
    "7. Marking: " + nineLineForm.value.marking,
    "8. Nationality: " + nineLineForm.value.nationality,
    "9. Terrain: " + nineLineForm.value.terrain
  ].join("\n")
  try {
    await api.post("/api/tak/nineline", { text: lines, urgency: nineLineForm.value.urgency })
    alert("9-Line MEDEVAC submitted")
  } catch (e) { alert(e.response?.data?.error || e.message) }
  nineLineSubmitting.value = false
}
onUnmounted(() => {
  if (sseConn) sseConn.close()
})
</script>

<template>
  <div class="max-w-5xl mx-auto space-y-4">
    <h2 class="text-lg font-semibold text-gray-200">TAK / CoT Monitor</h2>

    <!-- Tab bar -->
    <div class="flex gap-1 border-b border-gray-700 pb-2">
      <button v-for="tab in tabs" :key="tab.id" @click="activeTab = tab.id; tab.id === 'missions' && fetchMissions(); tab.id === 'sa' && fetchSASnapshot(); tab.id === 'certificates' && loadCertificates()"
        class="px-3 py-1.5 rounded text-xs font-medium transition-colors"
        :class="activeTab === tab.id ? 'bg-blue-600/10 text-blue-400' : 'text-gray-500 hover:text-gray-300'">
        {{ tab.label }}
        <span v-if="tab.id === 'missions' && missions.length > 0"
          class="ml-1 px-1 py-px rounded text-[9px] bg-blue-400/10 text-blue-400">{{ missions.length }}</span>
        <span v-if="tab.id === 'certificates' && certAlertCount > 0"
          class="ml-1 px-1 py-px rounded text-[9px] bg-amber-400/10 text-amber-400">{{ certAlertCount }}</span>
      </button>
    </div>

    <!-- ═══ Events Tab ═══ -->
    <template v-if="activeTab === 'events'">
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
            <tr v-if="!events.length"><td colspan="6" class="px-3 py-8 text-center text-gray-500">No CoT events yet.</td></tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- GEOCHAT -->
    <div v-if="activeTab==='chat'" class="space-y-4">
      <div class="bg-gray-800 rounded-lg border border-gray-700 p-4">
        <h3 class="text-sm font-medium text-gray-300 mb-3">TAK GeoChat</h3>
        <div class="space-y-2 max-h-80 overflow-y-auto mb-3">
          <div v-for="(m, i) in chatMessages.slice(0, 50)" :key="i" class="flex gap-2">
            <span class="text-xs text-cyan-400 font-mono whitespace-nowrap">{{ m.callsign || '?' }}</span>
            <span class="text-xs text-gray-300">{{ m.detail || m.uid }}</span>
            <span class="text-[10px] text-gray-600 ml-auto">{{ formatTime(m.timestamp) }}</span>
          </div>
          <p v-if="!chatMessages.length" class="text-xs text-gray-500">No chat messages yet.</p>
        </div>
        <div class="flex gap-2">
          <input v-model="chatInput" @keyup.enter="sendChat" placeholder="Type a message..." class="flex-1 px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" />
          <button @click="sendChat" class="px-4 py-2 rounded text-xs bg-cyan-600 text-white hover:bg-cyan-500">Send</button>
        </div>
      </div>
    </div>

    <!-- 9-LINE MEDEVAC -->
    <div v-if="activeTab==='nineline'" class="space-y-4">
      <div class="bg-gray-800 rounded-lg border border-gray-700 p-4">
        <h3 class="text-sm font-medium text-red-400 mb-3">9-Line MEDEVAC Request</h3>
        <div class="grid grid-cols-2 gap-3">
          <div><label class="block text-xs text-gray-500 mb-1">1. Location</label><input v-model="nineLineForm.location" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="52.3676N 4.9041E"></div>
          <div><label class="block text-xs text-gray-500 mb-1">2. Radio Frequency</label><input v-model="nineLineForm.freq" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="156.800 MHz"></div>
          <div><label class="block text-xs text-gray-500 mb-1">3. Patients</label><input v-model="nineLineForm.patients" type="number" min="1" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200"></div>
          <div><label class="block text-xs text-gray-500 mb-1">4. Equipment</label><select v-model="nineLineForm.equipment" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200"><option>None</option><option>Hoist</option><option>Extraction</option><option>Ventilator</option></select></div>
          <div><label class="block text-xs text-gray-500 mb-1">6. Security</label><select v-model="nineLineForm.security" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200"><option>No Enemy</option><option>Possible Enemy</option><option>Enemy in Area</option></select></div>
          <div><label class="block text-xs text-gray-500 mb-1">7. Marking</label><select v-model="nineLineForm.marking" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200"><option>Panels</option><option>Smoke</option><option>Pyrotechnic</option><option>None</option></select></div>
          <div><label class="block text-xs text-gray-500 mb-1">8. Nationality</label><input v-model="nineLineForm.nationality" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="US"></div>
          <div><label class="block text-xs text-gray-500 mb-1">9. Terrain</label><select v-model="nineLineForm.terrain" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200"><option>Open</option><option>Wooded</option><option>Rough</option><option>Urban</option></select></div>
        </div>
        <div class="mt-3 flex items-center gap-3">
          <select v-model="nineLineForm.urgency" class="px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200"><option>Urgent</option><option>Urgent-Surgical</option><option>Priority</option><option>Routine</option></select>
          <button @click="submitNineLine" :disabled="nineLineSubmitting" class="px-4 py-2 rounded text-xs bg-red-600 text-white hover:bg-red-500 disabled:opacity-40">{{ nineLineSubmitting ? 'Submitting...' : 'Submit 9-Line' }}</button>
        </div>
        <p class="text-xs text-gray-500 mt-3">Sends MEDEVAC as CoT SOS with 9-line remarks.</p>
      </div>
    </div>

  </div>
</template>
