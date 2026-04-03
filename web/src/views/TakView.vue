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
            <tr v-if="events.length === 0">
              <td colspan="6" class="px-3 py-8 text-center text-gray-500">No CoT events yet. Events will appear when the TAK gateway is active.</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- ═══ Certificates Tab ═══ -->
    <template v-if="activeTab === 'certificates'">
      <div class="space-y-4">
        <!-- Expiry Alerts -->
        <div v-if="certData?.alerts?.length" class="bg-amber-900/20 border border-amber-700/50 rounded-lg p-4">
          <h3 class="text-sm font-medium text-amber-400 mb-2">Expiry Alerts</h3>
          <ul class="space-y-1">
            <li v-for="(alert, i) in certData.alerts" :key="i" class="text-sm text-amber-300 flex items-start gap-2">
              <span class="mt-0.5 shrink-0">!</span>
              <span>{{ alert }}</span>
            </li>
          </ul>
        </div>

        <!-- Enrollment Status -->
        <div class="bg-gray-800 rounded-lg border border-gray-700 p-4">
          <div class="flex items-center justify-between mb-3">
            <h3 class="text-sm font-medium text-gray-300">Enrollment Status</h3>
            <span v-if="certData?.enrolled"
              class="px-2 py-0.5 text-xs rounded border bg-emerald-900/50 text-emerald-400 border-emerald-700">
              Enrolled
            </span>
            <span v-else class="px-2 py-0.5 text-xs rounded border bg-gray-900/50 text-gray-400 border-gray-700">
              Not Enrolled
            </span>
          </div>
          <p v-if="certLoading" class="text-sm text-gray-500">Loading...</p>
          <p v-else-if="!certData?.certificates?.length" class="text-sm text-gray-500">
            No TAK certificates found. Use the enrollment form below or upload certificates in Settings &gt; Credentials.
          </p>
        </div>

        <!-- Certificate List -->
        <div v-if="certData?.certificates?.length" class="bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
          <table class="w-full text-sm">
            <thead>
              <tr class="text-xs text-gray-500 border-b border-gray-700">
                <th class="px-3 py-2 text-left">Subject</th>
                <th class="px-3 py-2 text-left">Type</th>
                <th class="px-3 py-2 text-left">Expires</th>
                <th class="px-3 py-2 text-left">Status</th>
                <th class="px-3 py-2 text-left">Source</th>
                <th class="px-3 py-2 text-left">Fingerprint</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="cert in certData.certificates" :key="cert.id" class="border-b border-gray-700/50">
                <td class="px-3 py-2 text-gray-200 font-mono text-xs">{{ cert.subject || cert.id }}</td>
                <td class="px-3 py-2 text-gray-400 text-xs">{{ cert.cred_type }}</td>
                <td class="px-3 py-2 text-gray-400 text-xs font-mono">
                  {{ cert.expires || '—' }}
                  <span v-if="cert.days_left >= 0" class="ml-1" :class="statusBadgeClass(cert.status).split(' ')[1]">
                    ({{ daysLeftLabel(cert.days_left) }})
                  </span>
                  <span v-else-if="cert.status === 'expired'" class="ml-1 text-red-400">(Expired)</span>
                </td>
                <td class="px-3 py-2">
                  <span class="px-1.5 py-0.5 text-xs rounded border" :class="statusBadgeClass(cert.status)">
                    {{ cert.status }}
                  </span>
                  <span v-if="cert.applied" class="ml-1 px-1.5 py-0.5 text-xs rounded border bg-teal-900/50 text-teal-400 border-teal-700">
                    active
                  </span>
                </td>
                <td class="px-3 py-2 text-gray-500 text-xs">{{ cert.source }}</td>
                <td class="px-3 py-2 text-gray-500 text-xs font-mono truncate max-w-[120px]">{{ cert.fingerprint?.slice(0, 16) || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Enrollment Form -->
        <div class="bg-gray-800 rounded-lg border border-gray-700 p-4">
          <h3 class="text-sm font-medium text-gray-300 mb-3">Certificate Enrollment</h3>
          <p class="text-xs text-gray-500 mb-3">Enroll with a TAK Server to obtain a signed client certificate via port 8446.</p>
          <div class="grid grid-cols-1 md:grid-cols-3 gap-3 mb-3">
            <input v-model="enrollForm.server_url" type="text" placeholder="https://tak-server:8446"
              class="px-3 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <input v-model="enrollForm.username" type="text" placeholder="Username"
              class="px-3 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <input v-model="enrollForm.password" type="password" placeholder="Password"
              class="px-3 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div class="flex items-center gap-3">
            <button @click="enrollCert" :disabled="enrolling || !enrollForm.server_url || !enrollForm.username || !enrollForm.password"
              class="px-4 py-1.5 rounded text-sm font-medium transition-colors"
              :class="enrolling ? 'bg-gray-700 text-gray-500 cursor-not-allowed' : 'bg-teal-600 hover:bg-teal-500 text-white'">
              {{ enrolling ? 'Enrolling...' : 'Enroll' }}
            </button>
            <button @click="loadCertificates" class="px-3 py-1.5 rounded text-sm text-gray-400 hover:text-gray-200 border border-gray-700 hover:border-gray-600 transition-colors">
              Refresh
            </button>
          </div>

          <!-- Enrollment result -->
          <div v-if="enrollResult" class="mt-3 p-3 rounded text-sm" :class="enrollResult.success ? 'bg-emerald-900/30 text-emerald-300' : 'bg-red-900/30 text-red-300'">
            <template v-if="enrollResult.success">
              Enrolled successfully. Subject: {{ enrollResult.subject }}, Expires: {{ enrollResult.expires }}
            </template>
            <template v-else>
              {{ enrollResult.error }}
            </template>
          </div>
        </div>
      </div>
    </template>

    <!-- ═══ Missions Tab ═══ -->
    <template v-if="activeTab === 'missions'">
      <div class="flex items-center justify-between mb-2">
        <p class="text-xs text-gray-500">Missions from connected TAK Server (Marti API)</p>
        <button @click="fetchMissions" :disabled="missionsLoading"
          class="px-3 py-1.5 rounded bg-blue-600 text-white text-xs font-medium hover:bg-blue-500 disabled:opacity-50">
          {{ missionsLoading ? 'Loading...' : 'Refresh' }}
        </button>
      </div>

      <div v-if="missionsError" class="bg-red-900/20 border border-red-700/40 rounded-lg p-3 text-sm text-red-400">
        {{ missionsError }}
      </div>

      <div v-if="missions.length === 0 && !missionsLoading && !missionsError"
        class="bg-gray-800 rounded-lg border border-gray-700 p-8 text-center text-gray-500">
        No missions found. The TAK gateway must be connected with Marti API access (port 8443).
      </div>

      <div v-else class="space-y-2">
        <div v-for="m in missions" :key="m.name"
          class="bg-gray-800 rounded-lg border border-gray-700 p-4 flex items-start justify-between gap-4">
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium text-gray-200">{{ m.name }}</span>
              <span v-if="m.tool" class="px-1.5 py-0.5 rounded text-[10px] bg-gray-700 text-gray-400">{{ m.tool }}</span>
            </div>
            <p v-if="m.description" class="text-xs text-gray-500 mt-1">{{ m.description }}</p>
            <div class="flex gap-3 mt-2 text-xs text-gray-600">
              <span v-if="m.createTime">Created: {{ formatDateTime(m.createTime) }}</span>
              <span v-if="m.groups && m.groups.length > 0">Groups: {{ m.groups.map(g => g.name).join(', ') }}</span>
            </div>
          </div>
          <button @click="subscribeMission(m.name)" :disabled="subscribingMission === m.name"
            class="px-3 py-1.5 rounded bg-emerald-600 text-white text-xs font-medium hover:bg-emerald-500 disabled:opacity-50 whitespace-nowrap">
            {{ subscribingMission === m.name ? 'Subscribing...' : 'Subscribe' }}
          </button>
        </div>
      </div>
    </template>

    <!-- ═══ Data Packages Tab ═══ -->
    <template v-if="activeTab === 'packages'">
      <div class="space-y-4">
        <!-- Upload -->
        <div class="bg-gray-800 rounded-lg border border-gray-700 p-4">
          <h3 class="text-sm font-medium text-gray-300 mb-3">Upload Data Package</h3>
          <div class="flex gap-3 items-center">
            <input type="file" @change="uploadFile = $event.target.files[0]"
              class="text-sm text-gray-400 file:mr-3 file:py-1.5 file:px-3 file:rounded file:border-0 file:text-xs file:font-medium file:bg-gray-700 file:text-gray-300 hover:file:bg-gray-600">
            <button @click="uploadPackage" :disabled="!uploadFile || uploading"
              class="px-3 py-1.5 rounded bg-blue-600 text-white text-xs font-medium hover:bg-blue-500 disabled:opacity-50">
              {{ uploading ? 'Uploading...' : 'Upload' }}
            </button>
          </div>
          <div v-if="uploadError" class="mt-2 text-xs text-red-400">{{ uploadError }}</div>
          <div v-if="uploadResult" class="mt-2 text-xs text-emerald-400">
            Uploaded: {{ uploadResult.filename }} (hash: <span class="font-mono">{{ uploadResult.hash }}</span>)
          </div>
        </div>

        <!-- Download -->
        <div class="bg-gray-800 rounded-lg border border-gray-700 p-4">
          <h3 class="text-sm font-medium text-gray-300 mb-3">Download by Hash</h3>
          <div class="flex gap-3 items-center">
            <input v-model="downloadHash" placeholder="Content hash"
              class="px-3 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 flex-1 font-mono">
            <button @click="downloadPackage" :disabled="!downloadHash || downloading"
              class="px-3 py-1.5 rounded bg-blue-600 text-white text-xs font-medium hover:bg-blue-500 disabled:opacity-50">
              {{ downloading ? 'Downloading...' : 'Download' }}
            </button>
          </div>
        </div>
      </div>
    </template>

    <!-- ═══ SA Snapshot Tab ═══ -->
    <template v-if="activeTab === 'sa'">
      <div class="flex items-center justify-between mb-2">
        <p class="text-xs text-gray-500">Current Situational Awareness snapshot from TAK Server</p>
        <button @click="fetchSASnapshot" :disabled="saLoading"
          class="px-3 py-1.5 rounded bg-blue-600 text-white text-xs font-medium hover:bg-blue-500 disabled:opacity-50">
          {{ saLoading ? 'Loading...' : 'Refresh' }}
        </button>
      </div>

      <div v-if="saError" class="bg-red-900/20 border border-red-700/40 rounded-lg p-3 text-sm text-red-400">
        {{ saError }}
      </div>

      <div v-if="!saData && !saLoading && !saError"
        class="bg-gray-800 rounded-lg border border-gray-700 p-8 text-center text-gray-500">
        No SA data. Click Refresh to fetch the current snapshot from the TAK Server.
      </div>

      <div v-if="saData" class="bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
        <pre class="p-4 text-xs text-gray-300 font-mono overflow-x-auto whitespace-pre-wrap max-h-[600px] overflow-y-auto">{{ saData }}</pre>
      </div>
    </template>
  </div>
</template>
