<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import { formatLastHeard, signalQualityClass, nodeStatusDot, shortId, isNodeActive, isNodeRecent } from '@/utils/format'

const store = useMeshsatStore()
const activeTab = ref('mesh') // 'mesh' or 'sms'
const filter = ref('all') // 'all', 'active', 'stale'

// SMS contacts state
const showContactForm = ref(false)
const editingContact = ref(null)
const contactForm = ref({ name: '', phone: '', notes: '', auto_fwd: false })
const sendingTo = ref(null)
const smsText = ref('')

function openNewContact() {
  editingContact.value = null
  contactForm.value = { name: '', phone: '', notes: '', auto_fwd: false }
  showContactForm.value = true
}

function openEditContact(c) {
  editingContact.value = c.id
  contactForm.value = { name: c.name, phone: c.phone, notes: c.notes, auto_fwd: !!c.auto_fwd }
  showContactForm.value = true
}

async function saveContact() {
  if (!contactForm.value.name || !contactForm.value.phone) return
  try {
    if (editingContact.value) {
      await store.updateSMSContact(editingContact.value, contactForm.value)
    } else {
      await store.createSMSContact(contactForm.value)
    }
    showContactForm.value = false
  } catch { /* store error */ }
}

async function deleteContact(id) {
  if (!confirm('Delete this contact?')) return
  try { await store.deleteSMSContact(id) } catch { /* store error */ }
}

async function handleSendSMS(phone) {
  if (!smsText.value.trim()) return
  try {
    await store.sendSMS(phone, smsText.value.trim())
    smsText.value = ''
    sendingTo.value = null
  } catch { /* store error */ }
}
const sortBy = ref('last_heard') // 'last_heard', 'name', 'signal'
const removing = ref(null)
const removingStale = ref(false)
const expandedNode = ref(null)
const nodeTelemetry = ref([])
const nodeNeighbors = ref([])
const telemetryLoading = ref(false)

const radioConnected = computed(() => store.status?.connected === true)
const now = ref(Date.now() / 1000)

// Template-friendly wrappers (now.value not accessible in template expressions)
const isActive = (node) => isNodeActive(node, now.value)
const isRecent = (node) => isNodeRecent(node, now.value)
const signalClass = (q) => signalQualityClass(q)
const signalDot = (node) => nodeStatusDot(node, now.value)

const filteredNodes = computed(() => {
  let list = [...(store.nodes || [])]
  if (filter.value === 'active') list = list.filter(n => isActive(n))
  else if (filter.value === 'stale') list = list.filter(n => !isActive(n))

  list.sort((a, b) => {
    if (sortBy.value === 'last_heard') return (b.last_heard || 0) - (a.last_heard || 0)
    if (sortBy.value === 'name') return (a.long_name || '').localeCompare(b.long_name || '')
    if (sortBy.value === 'signal') return (b.snr || -999) - (a.snr || -999)
    return 0
  })
  return list
})

const activeCount = computed(() => (store.nodes || []).filter(n => isActive(n)).length)
const staleCount = computed(() => (store.nodes || []).filter(n => !isActive(n)).length)

async function handleRemove(node) {
  const name = node.long_name || node.user_id || node.num
  if (!confirm(`Remove "${name}" from NodeDB?\n\nThis will forget this node from the radio.`)) return
  removing.value = node.num
  try {
    await store.removeNode(node.num)
  } catch { /* store error */ }
  removing.value = null
}

async function handleReboot(node) {
  if (!confirm(`Reboot node ${node.long_name || node.user_id}?`)) return
  try {
    await store.adminReboot({ node_id: node.num, delay_secs: 5 })
  } catch { /* store error */ }
}

async function handleTraceroute(node) {
  try {
    await store.adminTraceroute({ node_id: node.num })
  } catch { /* store error */ }
}

async function toggleNodeDetail(node) {
  if (expandedNode.value === node.num) {
    expandedNode.value = null
    return
  }
  expandedNode.value = node.num
  telemetryLoading.value = true
  const nodeId = node.user_id || `!${node.num.toString(16).padStart(8, '0')}`
  try {
    const data = await store.fetchTelemetry({ node: nodeId, limit: 50 })
    nodeTelemetry.value = data || []
  } catch { nodeTelemetry.value = [] }
  try {
    await store.fetchNeighborInfo()
    nodeNeighbors.value = (store.neighborInfo || []).filter(n => n.node_id === node.num)
  } catch { nodeNeighbors.value = [] }
  telemetryLoading.value = false
}

async function handleRemoveAllStale() {
  const staleNodes = (store.nodes || []).filter(n => !isActive(n))
  if (!staleNodes.length) return
  if (!confirm(`Remove ${staleNodes.length} stale node${staleNodes.length > 1 ? 's' : ''} from the radio's NodeDB?\n\nThis cannot be undone.`)) return
  removingStale.value = true
  for (const node of staleNodes) {
    try { await store.removeNode(node.num) } catch { /* continue */ }
  }
  removingStale.value = false
}

let nowTimer = null
onMounted(() => {
  Promise.all([store.fetchNodes(), store.fetchStatus(), store.fetchSMSContacts()])
  nowTimer = setInterval(() => { now.value = Date.now() / 1000 }, 15000)
})
onUnmounted(() => { if (nowTimer) clearInterval(nowTimer) })
</script>

<template>
  <div class="max-w-5xl mx-auto">
    <!-- Header -->
    <div class="flex items-center justify-between mb-4">
      <div>
        <h1 class="text-lg font-semibold text-gray-200">Peers</h1>
        <div class="text-xs text-gray-500 mt-0.5">
          {{ store.nodes?.length || 0 }} total
          <span class="text-emerald-400/80">{{ activeCount }} active</span>
          <span v-if="staleCount > 0" class="text-gray-600">{{ staleCount }} stale</span>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <button v-if="staleCount > 0" @click="handleRemoveAllStale" :disabled="removingStale"
          class="px-3 py-1.5 text-xs rounded bg-gray-800 text-red-400/70 hover:text-red-300 border border-red-900/30 hover:border-red-800/50 transition-colors disabled:opacity-50">
          {{ removingStale ? 'Removing...' : `Forget ${staleCount} stale` }}
        </button>
        <button @click="store.fetchNodes()"
          class="px-3 py-1.5 text-xs rounded bg-gray-800 text-gray-400 hover:text-gray-200 transition-colors">
          Refresh
        </button>
      </div>
    </div>

    <!-- Tab selector -->
    <div class="flex gap-1 mb-4">
      <button @click="activeTab = 'mesh'"
        class="px-3 py-1.5 rounded text-xs font-medium transition-colors"
        :class="activeTab === 'mesh' ? 'bg-teal-600/20 text-teal-400 border border-teal-600/30' : 'bg-gray-800/40 text-gray-500 hover:text-gray-300 border border-transparent'">
        Mesh Nodes
        <span class="text-[9px] ml-1 opacity-60">({{ store.nodes?.length || 0 }})</span>
      </button>
      <button @click="activeTab = 'sms'"
        class="px-3 py-1.5 rounded text-xs font-medium transition-colors"
        :class="activeTab === 'sms' ? 'bg-teal-600/20 text-teal-400 border border-teal-600/30' : 'bg-gray-800/40 text-gray-500 hover:text-gray-300 border border-transparent'">
        SMS Contacts
        <span class="text-[9px] ml-1 opacity-60">({{ store.smsContacts?.length || 0 }})</span>
      </button>
    </div>

    <!-- ═══ SMS Contacts Tab ═══ -->
    <div v-if="activeTab === 'sms'">
      <!-- Add contact button -->
      <div class="flex justify-end mb-3">
        <button @click="openNewContact"
          class="px-3 py-1.5 text-xs rounded bg-teal-600/20 text-teal-400 border border-teal-600/30 hover:bg-teal-600/30 transition-colors">
          + Add Contact
        </button>
      </div>

      <!-- Contact form modal -->
      <div v-if="showContactForm" class="bg-gray-800/60 rounded-lg border border-gray-700/50 p-4 mb-4">
        <h3 class="text-sm font-medium text-gray-300 mb-3">{{ editingContact ? 'Edit Contact' : 'New Contact' }}</h3>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="text-[11px] text-gray-500 mb-1 block">Name</label>
            <input v-model="contactForm.name" type="text" placeholder="John Doe"
              class="w-full px-2.5 py-1.5 rounded bg-gray-900/60 border border-gray-700 text-sm text-gray-200 focus:outline-none focus:border-teal-600" />
          </div>
          <div>
            <label class="text-[11px] text-gray-500 mb-1 block">Phone</label>
            <input v-model="contactForm.phone" type="text" placeholder="+31612345678"
              class="w-full px-2.5 py-1.5 rounded bg-gray-900/60 border border-gray-700 text-sm text-gray-200 focus:outline-none focus:border-teal-600" />
          </div>
          <div class="col-span-2">
            <label class="text-[11px] text-gray-500 mb-1 block">Notes</label>
            <input v-model="contactForm.notes" type="text" placeholder="Optional notes"
              class="w-full px-2.5 py-1.5 rounded bg-gray-900/60 border border-gray-700 text-sm text-gray-200 focus:outline-none focus:border-teal-600" />
          </div>
          <div class="col-span-2 flex items-center gap-2">
            <input v-model="contactForm.auto_fwd" type="checkbox" id="auto-fwd"
              class="rounded bg-gray-900 border-gray-700" />
            <label for="auto-fwd" class="text-xs text-gray-400">Auto-forward mesh messages to this contact</label>
          </div>
        </div>
        <div class="flex justify-end gap-2 mt-3">
          <button @click="showContactForm = false"
            class="px-3 py-1.5 text-xs rounded bg-gray-700/50 text-gray-400 hover:text-gray-200 transition-colors">Cancel</button>
          <button @click="saveContact"
            class="px-3 py-1.5 text-xs rounded bg-teal-600/20 text-teal-400 border border-teal-600/30 hover:bg-teal-600/30 transition-colors">
            {{ editingContact ? 'Update' : 'Create' }}
          </button>
        </div>
      </div>

      <!-- Empty state -->
      <div v-if="!store.smsContacts?.length && !showContactForm"
        class="flex flex-col items-center justify-center py-16 text-gray-500">
        <svg class="w-10 h-10 mb-3 text-gray-700" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1">
          <path stroke-linecap="round" stroke-linejoin="round" d="M15 19.128a9.38 9.38 0 002.625.372 9.337 9.337 0 004.121-.952 4.125 4.125 0 00-7.533-2.493M15 19.128v-.003c0-1.113-.285-2.16-.786-3.07M15 19.128H5.25A2.25 2.25 0 013 16.878V8.25A2.25 2.25 0 015.25 6h13.5A2.25 2.25 0 0121 8.25v2.875" />
        </svg>
        <span class="text-sm font-medium mb-1">No SMS Contacts</span>
        <span class="text-[12px] text-gray-600">Add phone numbers for cellular messaging and auto-forwarding.</span>
      </div>

      <!-- Contact list -->
      <div v-else class="space-y-1.5">
        <div v-for="c in store.smsContacts" :key="c.id"
          class="bg-gray-800/40 rounded-lg border border-gray-700/50 px-4 py-3 group hover:bg-gray-800/60 transition-colors">
          <div class="flex items-start gap-3">
            <!-- Avatar -->
            <div class="w-9 h-9 rounded-full bg-sky-900/40 flex items-center justify-center text-xs font-bold text-sky-400 flex-shrink-0">
              {{ (c.name || '?').slice(0, 2).toUpperCase() }}
            </div>
            <!-- Info -->
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-2">
                <span class="text-sm font-medium text-gray-200">{{ c.name }}</span>
                <span v-if="c.auto_fwd" class="text-[9px] px-1.5 py-px rounded bg-teal-900/30 text-teal-400">auto-fwd</span>
              </div>
              <div class="text-[11px] text-gray-500 font-mono mt-0.5">{{ c.phone }}</div>
              <div v-if="c.notes" class="text-[11px] text-gray-600 mt-0.5">{{ c.notes }}</div>
            </div>
            <!-- Actions -->
            <div class="flex items-center gap-1 flex-shrink-0 opacity-0 group-hover:opacity-100 transition-opacity">
              <button @click="sendingTo = sendingTo === c.id ? null : c.id" title="Send SMS"
                class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-teal-400 transition-colors">
                SMS
              </button>
              <button @click="openEditContact(c)" title="Edit"
                class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-amber-400 transition-colors">
                Edit
              </button>
              <button @click="deleteContact(c.id)" title="Delete"
                class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-red-400 transition-colors">
                Del
              </button>
            </div>
          </div>
          <!-- Inline SMS send -->
          <div v-if="sendingTo === c.id" class="mt-2 pl-12 flex gap-2">
            <input v-model="smsText" type="text" placeholder="Type message..."
              class="flex-1 px-2.5 py-1.5 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200 focus:outline-none focus:border-teal-600"
              @keydown.enter="handleSendSMS(c.phone)" />
            <button @click="handleSendSMS(c.phone)"
              class="px-3 py-1.5 text-xs rounded bg-teal-600/20 text-teal-400 border border-teal-600/30 hover:bg-teal-600/30 transition-colors">
              Send
            </button>
          </div>
        </div>
      </div>
    </div>

    <template v-if="activeTab === 'mesh'">

    <!-- Connection banner -->
    <div v-if="!radioConnected" class="bg-amber-900/20 border border-amber-800/50 rounded-lg p-3 text-amber-300/80 text-sm mb-4">
      Radio not connected. Connect a Meshtastic device to see nodes.
    </div>

    <!-- Filter + sort toolbar -->
    <div v-if="store.nodes?.length" class="flex items-center justify-between mb-3">
      <div class="flex gap-1">
        <button v-for="f in [
          { key: 'all', label: `All (${store.nodes?.length || 0})` },
          { key: 'active', label: `Active (${activeCount})` },
          { key: 'stale', label: `Stale (${staleCount})` },
        ]" :key="f.key"
          @click="filter = f.key"
          class="px-2.5 py-1 rounded text-xs font-medium transition-colors"
          :class="filter === f.key ? 'bg-gray-700 text-gray-200' : 'text-gray-500 hover:text-gray-300'">
          {{ f.label }}
        </button>
      </div>
      <select v-model="sortBy" class="px-2 py-1 rounded bg-gray-800 border border-gray-700 text-xs text-gray-400 focus:outline-none">
        <option value="last_heard">Sort: Last heard</option>
        <option value="name">Sort: Name</option>
        <option value="signal">Sort: Signal</option>
      </select>
    </div>

    <!-- Empty state -->
    <div v-if="!store.nodes?.length" class="bg-gray-800/30 rounded-lg p-8 border border-gray-800 text-center text-gray-500 text-sm">
      {{ radioConnected ? 'No nodes discovered yet. Nodes appear when packets are received.' : 'No nodes discovered' }}
    </div>

    <!-- Node cards -->
    <div v-else class="space-y-1.5">
      <div v-for="node in filteredNodes" :key="node.num"
        class="bg-gray-800/40 rounded-lg border px-4 py-3 group transition-colors hover:bg-gray-800/60"
        :class="isActive(node) ? 'border-gray-700/50' : 'border-gray-800/50 opacity-60 hover:opacity-80'">
        <div class="flex items-start gap-3">
          <!-- Status dot + avatar -->
          <div class="flex-shrink-0 relative mt-0.5">
            <div class="w-9 h-9 rounded-full bg-gray-700/50 flex items-center justify-center text-xs font-bold text-gray-400">
              {{ (node.short_name || '??').slice(0, 2).toUpperCase() }}
            </div>
            <span class="absolute -bottom-0.5 -right-0.5 w-2.5 h-2.5 rounded-full border-2 border-gray-950"
              :class="signalDot(node)"></span>
          </div>

          <!-- Info -->
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium text-gray-200 truncate">{{ node.long_name || node.user_id || `!${node.num.toString(16).padStart(8, '0')}` }}</span>
              <span v-if="node.user_id" class="text-[10px] text-gray-600 font-mono">{{ shortId(node.user_id) }}</span>
            </div>
            <div class="flex items-center gap-3 mt-1 text-[11px]">
              <span v-if="node.hw_model_name" class="text-gray-500">{{ node.hw_model_name }}</span>
              <span v-if="node.snr != null && Math.abs(node.snr) < 100" :class="node.snr >= 0 ? 'text-emerald-400/70' : node.snr >= -10 ? 'text-amber-400/70' : 'text-red-400/70'">
                SNR {{ Number(node.snr).toFixed(1) }}
              </span>
              <span v-if="node.rssi" class="text-gray-500">{{ node.rssi }} dBm</span>
              <span v-if="node.signal_quality" class="px-1.5 py-px rounded text-[10px] font-medium"
                :class="signalClass(node.signal_quality)">
                {{ node.signal_quality }}
              </span>
              <span v-if="node.battery_level > 0 && node.battery_level <= 100" class="text-gray-500">
                {{ Math.round(node.battery_level) }}%
                <span v-if="node.voltage > 0 && node.voltage < 10" class="text-gray-600">{{ node.voltage.toFixed(1) }}V</span>
              </span>
            </div>
          </div>

          <!-- Last heard + actions -->
          <div class="flex-shrink-0 text-right">
            <div class="text-xs" :class="isActive(node) ? 'text-gray-400' : 'text-gray-600'">
              {{ formatLastHeard(node.last_heard) }}
            </div>
            <div v-if="node.last_message_time" class="text-[10px] text-gray-600 mt-0.5" title="Last text message">
              msg {{ formatLastHeard(node.last_message_time) }}
            </div>
            <div class="flex items-center gap-1 mt-1.5 opacity-0 group-hover:opacity-100 transition-opacity">
              <button @click="handleTraceroute(node)" title="Traceroute"
                class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-teal-400 transition-colors">
                Trace
              </button>
              <button @click="handleReboot(node)" title="Reboot"
                class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-amber-400 transition-colors">
                Reboot
              </button>
              <button @click="handleRemove(node)" :disabled="removing === node.num" title="Remove from NodeDB"
                class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-red-400 transition-colors disabled:opacity-50">
                {{ removing === node.num ? '...' : 'Forget' }}
              </button>
            </div>
          </div>
        </div>

        <!-- GPS row (if position available) -->
        <div v-if="node.latitude || node.longitude" class="flex items-center gap-3 mt-1.5 pl-12 text-[10px] text-gray-600">
          <span>{{ node.latitude?.toFixed(5) }}, {{ node.longitude?.toFixed(5) }}</span>
          <span v-if="node.altitude">{{ node.altitude }}m</span>
          <span v-if="node.sats">{{ node.sats }} sats</span>
        </div>

        <!-- Expand toggle -->
        <div class="mt-1.5 pl-12">
          <button @click="toggleNodeDetail(node)" class="text-[10px] text-gray-600 hover:text-teal-400 transition-colors">
            {{ expandedNode === node.num ? 'Hide details' : 'Show details' }}
          </button>
        </div>

        <!-- Expanded detail (telemetry + neighbors) -->
        <div v-if="expandedNode === node.num" class="mt-3 pl-12 space-y-3">
          <div v-if="telemetryLoading" class="text-xs text-gray-500">Loading...</div>

          <!-- Telemetry history -->
          <div v-if="nodeTelemetry.length > 0">
            <h4 class="text-xs font-medium text-gray-400 mb-1.5">Telemetry History ({{ nodeTelemetry.length }} records)</h4>
            <div class="overflow-x-auto">
              <table class="w-full text-[10px] text-gray-400">
                <thead>
                  <tr class="text-gray-600">
                    <th class="text-left pr-2 py-1">Time</th>
                    <th class="text-right pr-2 py-1">Battery</th>
                    <th class="text-right pr-2 py-1">Voltage</th>
                    <th class="text-right pr-2 py-1">Ch Util</th>
                    <th class="text-right pr-2 py-1">Air Util</th>
                    <th class="text-right pr-2 py-1">Temp</th>
                    <th class="text-right py-1">Uptime</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="t in nodeTelemetry.slice(0, 20)" :key="t.id" class="border-t border-gray-800/50">
                    <td class="pr-2 py-0.5 text-gray-600">{{ new Date(t.created_at).toLocaleTimeString() }}</td>
                    <td class="text-right pr-2 py-0.5">{{ t.battery_level }}%</td>
                    <td class="text-right pr-2 py-0.5">{{ t.voltage?.toFixed(2) }}V</td>
                    <td class="text-right pr-2 py-0.5">{{ t.channel_util?.toFixed(1) }}%</td>
                    <td class="text-right pr-2 py-0.5">{{ t.air_util_tx?.toFixed(1) }}%</td>
                    <td class="text-right pr-2 py-0.5">{{ t.temperature != null ? `${t.temperature.toFixed(1)}°` : '-' }}</td>
                    <td class="text-right py-0.5">{{ t.uptime_seconds ? `${Math.floor(t.uptime_seconds / 3600)}h` : '-' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <!-- Mini sparkline: battery over time -->
            <div class="mt-2 flex items-end gap-px h-8">
              <div v-for="(t, i) in nodeTelemetry.slice(0, 30).reverse()" :key="i"
                class="flex-1 rounded-t"
                :class="t.battery_level > 50 ? 'bg-emerald-500/40' : t.battery_level > 20 ? 'bg-amber-500/40' : 'bg-red-500/40'"
                :style="{ height: `${Math.max(2, t.battery_level * 0.3)}px` }"
                :title="`${t.battery_level}% at ${new Date(t.created_at).toLocaleTimeString()}`">
              </div>
            </div>
            <div class="text-[9px] text-gray-600 mt-0.5">Battery level over time</div>
          </div>

          <!-- Neighbor info -->
          <div v-if="nodeNeighbors.length > 0">
            <h4 class="text-xs font-medium text-gray-400 mb-1.5">Neighbors</h4>
            <div class="flex flex-wrap gap-2">
              <div v-for="ni in nodeNeighbors" :key="ni.node_id" class="bg-gray-900/50 rounded px-2 py-1 text-[10px]">
                <div v-for="n in ni.neighbors" :key="n.node_id" class="flex items-center gap-2 py-0.5">
                  <span class="text-gray-400 font-mono">!{{ n.node_id.toString(16).padStart(8, '0') }}</span>
                  <span :class="n.snr >= 0 ? 'text-emerald-400/70' : 'text-amber-400/70'">SNR {{ n.snr?.toFixed(1) }}</span>
                </div>
              </div>
            </div>
          </div>

          <div v-if="!telemetryLoading && nodeTelemetry.length === 0 && nodeNeighbors.length === 0" class="text-xs text-gray-600">
            No telemetry or neighbor data for this node.
          </div>
        </div>
      </div>
    </div>

    <!-- Stale nodes hint -->
    <div v-if="staleCount > 0 && filter !== 'stale'" class="mt-4 text-center">
      <button @click="filter = 'stale'" class="text-[11px] text-gray-600 hover:text-gray-400 transition-colors">
        {{ staleCount }} stale node{{ staleCount > 1 ? 's' : '' }} (not heard in 2+ hours) — click to manage
      </button>
    </div>

    </template>
  </div>
</template>
