<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import { formatLastHeard, signalQualityClass, nodeStatusDot, shortId, isNodeActive, isNodeRecent } from '@/utils/format'

const store = useMeshsatStore()
const activeTab = ref('contacts') // 'contacts', 'mesh', or 'sms'
const filter = ref('all') // 'all', 'active', 'stale'

// SMS contacts state (legacy tab)
const showContactForm = ref(false)
const editingContact = ref(null)
const contactForm = ref({ name: '', phone: '', notes: '', auto_fwd: false })
const sendingTo = ref(null)
const smsText = ref('')

// Unified contacts state
const showUnifiedForm = ref(false)
const editingUnified = ref(null)
const unifiedForm = ref({ display_name: '', notes: '' })
const activeContactId = ref(null) // selected contact for conversation view
const conversationMessages = ref([])
const convLoading = ref(false)
const showAddAddress = ref(false)
const addressForm = ref({ type: 'sms', address: '', label: '', encryption_key: '', is_primary: false, auto_fwd: false })
const editingAddress = ref(null)
const convCompose = ref({ text: '', transport: '' })

const activeContact = computed(() => (store.contacts || []).find(c => c.id === activeContactId.value))

const transportBadgeClass = {
  sms: 'bg-sky-900/40 text-sky-400',
  mesh: 'bg-emerald-900/40 text-emerald-400',
  iridium: 'bg-amber-900/40 text-amber-400',
  webhook: 'bg-purple-900/40 text-purple-400',
  mqtt: 'bg-indigo-900/40 text-indigo-400',
  astrocast: 'bg-orange-900/40 text-orange-400',
  zigbee: 'bg-lime-900/40 text-lime-400',
}

function transportBadge(type) {
  return transportBadgeClass[type] || 'bg-gray-700/40 text-gray-400'
}

function openNewUnifiedContact() {
  editingUnified.value = null
  unifiedForm.value = { display_name: '', notes: '' }
  showUnifiedForm.value = true
}

function openEditUnifiedContact(c) {
  editingUnified.value = c.id
  unifiedForm.value = { display_name: c.display_name, notes: c.notes || '' }
  showUnifiedForm.value = true
}

async function saveUnifiedContact() {
  if (!unifiedForm.value.display_name) return
  try {
    if (editingUnified.value) {
      await store.updateContact(editingUnified.value, unifiedForm.value)
    } else {
      await store.createContact(unifiedForm.value)
    }
    showUnifiedForm.value = false
  } catch { /* store error */ }
}

async function deleteUnifiedContact(id) {
  if (!confirm('Delete this contact and all addresses?')) return
  try {
    await store.deleteContact(id)
    if (activeContactId.value === id) activeContactId.value = null
  } catch { /* store error */ }
}

function openAddAddress(contactId) {
  editingAddress.value = null
  addressForm.value = { type: 'sms', address: '', label: '', encryption_key: '', is_primary: false, auto_fwd: false }
  showAddAddress.value = contactId
}

function openEditAddress(addr) {
  editingAddress.value = addr.id
  addressForm.value = { type: addr.type, address: addr.address, label: addr.label, encryption_key: addr.encryption_key || '', is_primary: !!addr.is_primary, auto_fwd: !!addr.auto_fwd }
  showAddAddress.value = addr.contact_id
}

async function saveAddress() {
  if (!addressForm.value.type || !addressForm.value.address) return
  try {
    if (editingAddress.value) {
      await store.updateContactAddress(showAddAddress.value, editingAddress.value, addressForm.value)
    } else {
      await store.addContactAddress(showAddAddress.value, addressForm.value)
    }
    showAddAddress.value = false
    editingAddress.value = null
  } catch { /* store error */ }
}

async function removeAddress(contactId, addrId) {
  if (!confirm('Remove this address?')) return
  try { await store.deleteContactAddress(contactId, addrId) } catch { /* store error */ }
}

async function openConversation(contactId) {
  activeContactId.value = contactId
  convLoading.value = true
  try {
    await store.fetchConversation(contactId, 200)
    conversationMessages.value = store.activeConversation || []
  } catch { conversationMessages.value = [] }
  convLoading.value = false
}

async function sendConvMessage() {
  if (!convCompose.value.text.trim() || !convCompose.value.transport) return
  const contact = activeContact.value
  if (!contact) return
  const addr = contact.addresses?.find(a => a.type === convCompose.value.transport)
  if (!addr) return

  try {
    if (convCompose.value.transport === 'sms') {
      await store.sendSMS(addr.address, convCompose.value.text.trim())
    } else if (convCompose.value.transport === 'mesh') {
      await store.sendMessage({ to: addr.address, text: convCompose.value.text.trim() })
    }
    convCompose.value.text = ''
    // Refresh conversation
    setTimeout(() => openConversation(activeContactId.value), 500)
  } catch { /* store error */ }
}

function formatConvTime(ts) {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  const now = new Date()
  const diffH = (now - d) / 3600000
  if (diffH < 24) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  if (diffH < 168) return d.toLocaleDateString([], { weekday: 'short' }) + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' }) + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

// Auto-select first transport for compose
watch(activeContact, (c) => {
  if (c?.addresses?.length) convCompose.value.transport = c.addresses[0].type
})

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
const requestingInfo = ref(null)
const expandedNode = ref(null)
const nodeTelemetry = ref([])
const nodeNeighbors = ref([])
const telemetryLoading = ref(false)

const radioConnected = computed(() => store.status?.connected === true)
const now = ref(Date.now() / 1000)

// Active Meshtastic channels from config
const activeChannels = computed(() => {
  const cfg = store.config
  if (!cfg) return []
  const channels = []
  for (let i = 0; i < 8; i++) {
    const ch = cfg['channel_' + i]
    if (!ch) continue
    const role = ch['3'] || 0
    if (role === 0) continue
    const settings = ch['2'] || {}
    const name = settings['3'] || settings['4'] || ''
    const psk = settings['2'] || settings['3'] || ''
    let pskLabel = ''
    if (typeof psk === 'string' && psk.length > 4) {
      try { const raw = atob(psk); let xor = 0; for (let j = 0; j < raw.length; j++) xor ^= raw.charCodeAt(j); pskLabel = String.fromCharCode(0x41 + (xor % 26)) } catch { pskLabel = '?' }
    }
    channels.push({ index: i, name: name || (i === 0 ? 'Default' : `Ch ${i}`), role: role === 1 ? 'PRIMARY' : 'SECONDARY', pskHash: pskLabel })
  }
  return channels
})

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

async function handleRequestInfo(node) {
  requestingInfo.value = node.num
  try {
    await store.requestNodeInfo(node.num)
    // Refresh nodes after a short delay to pick up the response
    setTimeout(() => store.fetchNodes(), 3000)
  } catch { /* store error */ }
  requestingInfo.value = null
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
  Promise.all([store.fetchNodes(), store.fetchStatus(), store.fetchSMSContacts(), store.fetchContacts(), store.fetchConfig()])
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
      <button @click="activeTab = 'contacts'"
        class="px-3 py-1.5 rounded text-xs font-medium transition-colors"
        :class="activeTab === 'contacts' ? 'bg-teal-600/20 text-teal-400 border border-teal-600/30' : 'bg-gray-800/40 text-gray-500 hover:text-gray-300 border border-transparent'">
        Contacts
        <span class="text-[9px] ml-1 opacity-60">({{ store.contacts?.length || 0 }})</span>
      </button>
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

    <!-- ═══ Unified Contacts Tab ═══ -->
    <div v-if="activeTab === 'contacts'">
      <!-- Conversation view (contact selected) -->
      <div v-if="activeContactId && activeContact" class="flex flex-col" style="height: calc(100vh - 180px)">
        <!-- Contact header -->
        <div class="flex items-center gap-3 pb-3 border-b border-gray-700/50 mb-3 shrink-0">
          <button @click="activeContactId = null" class="px-2 py-1 text-[10px] rounded bg-gray-800 text-gray-400 hover:text-gray-200 transition-colors">Back</button>
          <div class="w-10 h-10 rounded-full bg-teal-900/40 flex items-center justify-center text-sm font-bold text-teal-400">
            {{ (activeContact.display_name || '?').slice(0, 2).toUpperCase() }}
          </div>
          <div class="flex-1 min-w-0">
            <div class="text-sm font-medium text-gray-200">{{ activeContact.display_name }}</div>
            <div class="flex items-center gap-1.5 mt-0.5 flex-wrap">
              <span v-for="addr in activeContact.addresses" :key="addr.id"
                class="text-[9px] px-1.5 py-px rounded font-mono" :class="transportBadge(addr.type)">
                {{ addr.type.toUpperCase() }}: {{ addr.address }}
              </span>
            </div>
          </div>
          <button @click="openEditUnifiedContact(activeContact)" class="px-2 py-1 text-[10px] rounded bg-gray-800 text-gray-400 hover:text-gray-200 transition-colors">Edit</button>
        </div>

        <!-- Address management strip -->
        <div class="flex items-center gap-2 mb-3 shrink-0 flex-wrap">
          <div v-for="addr in activeContact.addresses" :key="addr.id"
            class="flex items-center gap-1 px-2 py-1 rounded bg-gray-800/50 border border-gray-700/40 text-[10px]">
            <span class="font-mono" :class="transportBadge(addr.type)">{{ addr.type }}</span>
            <span class="text-gray-400">{{ addr.address }}</span>
            <span v-if="addr.label" class="text-gray-600">({{ addr.label }})</span>
            <span v-if="addr.encryption_key" class="text-amber-400/60" title="Encrypted">ENC</span>
            <button @click="openEditAddress(addr)" class="text-gray-600 hover:text-gray-300 ml-1">Edit</button>
            <button @click="removeAddress(activeContact.id, addr.id)" class="text-gray-600 hover:text-red-400">x</button>
          </div>
          <button @click="openAddAddress(activeContact.id)" class="px-2 py-1 text-[10px] rounded bg-gray-800/50 text-teal-400/70 hover:text-teal-400 border border-dashed border-gray-700/40 hover:border-teal-600/30 transition-colors">
            + Address
          </button>
        </div>

        <!-- Address form (inline) -->
        <div v-if="showAddAddress === activeContact.id" class="bg-gray-800/50 rounded-lg border border-gray-700/50 p-3 mb-3 shrink-0">
          <div class="grid grid-cols-4 gap-2">
            <div>
              <label class="text-[10px] text-gray-500 mb-0.5 block">Type</label>
              <select v-model="addressForm.type" class="w-full px-2 py-1 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200">
                <option v-for="t in ['sms','mesh','iridium','webhook','mqtt','astrocast','zigbee']" :key="t" :value="t">{{ t }}</option>
              </select>
            </div>
            <div>
              <label class="text-[10px] text-gray-500 mb-0.5 block">Address</label>
              <input v-model="addressForm.address" type="text" :placeholder="addressForm.type === 'sms' ? '+31612345678' : addressForm.type === 'mesh' ? '!abcd1234' : 'address'"
                class="w-full px-2 py-1 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200 focus:outline-none focus:border-teal-600" />
            </div>
            <div>
              <label class="text-[10px] text-gray-500 mb-0.5 block">Label</label>
              <input v-model="addressForm.label" type="text" placeholder="e.g. Personal cell"
                class="w-full px-2 py-1 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200 focus:outline-none focus:border-teal-600" />
            </div>
            <div>
              <label class="text-[10px] text-gray-500 mb-0.5 block">Encryption Key (hex)</label>
              <input v-model="addressForm.encryption_key" type="text" placeholder="Optional AES key"
                class="w-full px-2 py-1 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200 focus:outline-none focus:border-teal-600" />
            </div>
          </div>
          <div class="flex items-center gap-4 mt-2">
            <label class="flex items-center gap-1 text-[10px] text-gray-400">
              <input v-model="addressForm.is_primary" type="checkbox" class="rounded bg-gray-900 border-gray-700" /> Primary
            </label>
            <label class="flex items-center gap-1 text-[10px] text-gray-400">
              <input v-model="addressForm.auto_fwd" type="checkbox" class="rounded bg-gray-900 border-gray-700" /> Auto-forward
            </label>
            <div class="flex-1"></div>
            <button @click="showAddAddress = false" class="px-2 py-1 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-gray-200">Cancel</button>
            <button @click="saveAddress" class="px-2 py-1 text-[10px] rounded bg-teal-600/20 text-teal-400 border border-teal-600/30 hover:bg-teal-600/30">Save</button>
          </div>
        </div>

        <!-- Conversation timeline (Matrix-room style) -->
        <div class="flex-1 overflow-y-auto space-y-1 mb-3 tactical-scroll">
          <div v-if="convLoading" class="text-center text-xs text-gray-500 py-8">Loading conversation...</div>
          <div v-else-if="!conversationMessages.length" class="text-center text-xs text-gray-600 py-8">No messages yet. Send the first message below.</div>
          <template v-else>
            <div v-for="msg in [...conversationMessages].reverse()" :key="msg.transport + '-' + msg.id"
              class="flex gap-2 py-1.5 px-2"
              :class="msg.direction === 'tx' ? 'flex-row-reverse' : ''">
              <!-- Transport badge -->
              <div class="flex-shrink-0 mt-0.5">
                <span class="text-[8px] px-1 py-0.5 rounded font-mono uppercase" :class="transportBadge(msg.transport)">
                  {{ msg.transport }}
                </span>
              </div>
              <!-- Message bubble -->
              <div class="max-w-[70%] rounded-lg px-3 py-2"
                :class="msg.direction === 'tx' ? 'bg-teal-900/30 border border-teal-800/30' : 'bg-gray-800/60 border border-gray-700/30'">
                <div class="text-xs text-gray-200 break-words whitespace-pre-wrap">{{ msg.text }}</div>
                <div class="flex items-center gap-2 mt-1">
                  <span class="text-[9px] text-gray-600 font-mono">{{ formatConvTime(msg.timestamp) }}</span>
                  <span class="text-[9px] text-gray-600 font-mono">{{ msg.address }}</span>
                  <span v-if="msg.status && msg.status !== 'delivered'" class="text-[9px]"
                    :class="msg.status === 'failed' ? 'text-red-400' : 'text-gray-500'">{{ msg.status }}</span>
                </div>
              </div>
            </div>
          </template>
        </div>

        <!-- Compose bar -->
        <div class="flex items-center gap-2 pt-3 border-t border-gray-700/50 shrink-0">
          <select v-model="convCompose.transport"
            class="w-24 px-2 py-1.5 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-300 focus:outline-none">
            <option v-for="addr in activeContact.addresses" :key="addr.id" :value="addr.type">
              {{ addr.type.toUpperCase() }}
            </option>
          </select>
          <input v-model="convCompose.text" type="text" placeholder="Type a message..."
            class="flex-1 px-3 py-1.5 rounded bg-gray-900/60 border border-gray-700 text-sm text-gray-200 focus:outline-none focus:border-teal-600"
            @keydown.enter="sendConvMessage" />
          <button @click="sendConvMessage" :disabled="!convCompose.text.trim() || !convCompose.transport"
            class="px-4 py-1.5 text-xs rounded bg-teal-600/20 text-teal-400 border border-teal-600/30 hover:bg-teal-600/30 transition-colors disabled:opacity-40">
            Send
          </button>
        </div>
      </div>

      <!-- Contact list (no contact selected) -->
      <div v-else>
        <div class="flex justify-end mb-3">
          <button @click="openNewUnifiedContact"
            class="px-3 py-1.5 text-xs rounded bg-teal-600/20 text-teal-400 border border-teal-600/30 hover:bg-teal-600/30 transition-colors">
            + New Contact
          </button>
        </div>

        <!-- Contact form -->
        <div v-if="showUnifiedForm" class="bg-gray-800/60 rounded-lg border border-gray-700/50 p-4 mb-4">
          <h3 class="text-sm font-medium text-gray-300 mb-3">{{ editingUnified ? 'Edit Contact' : 'New Contact' }}</h3>
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="text-[11px] text-gray-500 mb-1 block">Name</label>
              <input v-model="unifiedForm.display_name" type="text" placeholder="Kyriakos"
                class="w-full px-2.5 py-1.5 rounded bg-gray-900/60 border border-gray-700 text-sm text-gray-200 focus:outline-none focus:border-teal-600" />
            </div>
            <div>
              <label class="text-[11px] text-gray-500 mb-1 block">Notes</label>
              <input v-model="unifiedForm.notes" type="text" placeholder="Optional notes"
                class="w-full px-2.5 py-1.5 rounded bg-gray-900/60 border border-gray-700 text-sm text-gray-200 focus:outline-none focus:border-teal-600" />
            </div>
          </div>
          <div class="flex justify-end gap-2 mt-3">
            <button @click="showUnifiedForm = false"
              class="px-3 py-1.5 text-xs rounded bg-gray-700/50 text-gray-400 hover:text-gray-200 transition-colors">Cancel</button>
            <button @click="saveUnifiedContact"
              class="px-3 py-1.5 text-xs rounded bg-teal-600/20 text-teal-400 border border-teal-600/30 hover:bg-teal-600/30 transition-colors">
              {{ editingUnified ? 'Update' : 'Create' }}
            </button>
          </div>
        </div>

        <!-- Empty state -->
        <div v-if="!store.contacts?.length && !showUnifiedForm"
          class="flex flex-col items-center justify-center py-16 text-gray-500">
          <svg class="w-10 h-10 mb-3 text-gray-700" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1">
            <path stroke-linecap="round" stroke-linejoin="round" d="M18 18.72a9.094 9.094 0 003.741-.479 3 3 0 00-4.682-2.72m.94 3.198l.001.031c0 .225-.012.447-.037.666A11.944 11.944 0 0112 21c-2.17 0-4.207-.576-5.963-1.584A6.062 6.062 0 016 18.719m12 0a5.971 5.971 0 00-.941-3.197m0 0A5.995 5.995 0 0012 12.75a5.995 5.995 0 00-5.058 2.772m0 0a3 3 0 00-4.681 2.72 8.986 8.986 0 003.74.477m.94-3.197a5.971 5.971 0 00-.94 3.197M15 6.75a3 3 0 11-6 0 3 3 0 016 0zm6 3a2.25 2.25 0 11-4.5 0 2.25 2.25 0 014.5 0zm-13.5 0a2.25 2.25 0 11-4.5 0 2.25 2.25 0 014.5 0z" />
          </svg>
          <span class="text-sm font-medium mb-1">No Contacts</span>
          <span class="text-[12px] text-gray-600">Create contacts and add their transport addresses (phone, mesh node, webhook, etc.).</span>
        </div>

        <!-- Contact cards -->
        <div v-else class="space-y-2">
          <div v-for="c in store.contacts" :key="c.id"
            @click="openConversation(c.id)"
            class="bg-gray-800/40 rounded-lg border border-gray-700/50 px-4 py-3 cursor-pointer group hover:bg-gray-800/60 transition-colors">
            <div class="flex items-start gap-3">
              <!-- Avatar -->
              <div class="w-10 h-10 rounded-full bg-teal-900/40 flex items-center justify-center text-sm font-bold text-teal-400 flex-shrink-0">
                {{ (c.display_name || '?').slice(0, 2).toUpperCase() }}
              </div>
              <!-- Info -->
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2">
                  <span class="text-sm font-medium text-gray-200">{{ c.display_name }}</span>
                  <span v-if="c.notes" class="text-[10px] text-gray-600">{{ c.notes }}</span>
                </div>
                <!-- Address tags -->
                <div class="flex items-center gap-1.5 mt-1 flex-wrap">
                  <span v-for="addr in c.addresses" :key="addr.id"
                    class="text-[9px] px-1.5 py-px rounded font-mono" :class="transportBadge(addr.type)">
                    {{ addr.type.toUpperCase() }}: {{ addr.address }}
                    <span v-if="addr.encryption_key" class="text-amber-400/60 ml-0.5">ENC</span>
                  </span>
                  <span v-if="!c.addresses?.length" class="text-[9px] text-gray-600 italic">no addresses</span>
                </div>
              </div>
              <!-- Actions -->
              <div class="flex items-center gap-1 flex-shrink-0 opacity-0 group-hover:opacity-100 transition-opacity" @click.stop>
                <button @click.stop="openAddAddress(c.id)" title="Add address"
                  class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-teal-400 transition-colors">
                  +Addr
                </button>
                <button @click.stop="openEditUnifiedContact(c)" title="Edit"
                  class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-amber-400 transition-colors">
                  Edit
                </button>
                <button @click.stop="deleteUnifiedContact(c.id)" title="Delete"
                  class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-red-400 transition-colors">
                  Del
                </button>
              </div>
            </div>

            <!-- Inline add-address form (if adding to this contact) -->
            <div v-if="showAddAddress === c.id" @click.stop class="mt-3 pl-13">
              <div class="bg-gray-800/50 rounded-lg border border-gray-700/50 p-3">
                <div class="grid grid-cols-4 gap-2">
                  <div>
                    <label class="text-[10px] text-gray-500 mb-0.5 block">Type</label>
                    <select v-model="addressForm.type" class="w-full px-2 py-1 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200">
                      <option v-for="t in ['sms','mesh','iridium','webhook','mqtt','astrocast','zigbee']" :key="t" :value="t">{{ t }}</option>
                    </select>
                  </div>
                  <div>
                    <label class="text-[10px] text-gray-500 mb-0.5 block">Address</label>
                    <input v-model="addressForm.address" type="text" placeholder="address"
                      class="w-full px-2 py-1 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200 focus:outline-none focus:border-teal-600" />
                  </div>
                  <div>
                    <label class="text-[10px] text-gray-500 mb-0.5 block">Label</label>
                    <input v-model="addressForm.label" type="text" placeholder="label"
                      class="w-full px-2 py-1 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200 focus:outline-none focus:border-teal-600" />
                  </div>
                  <div>
                    <label class="text-[10px] text-gray-500 mb-0.5 block">Encryption Key</label>
                    <input v-model="addressForm.encryption_key" type="text" placeholder="hex key"
                      class="w-full px-2 py-1 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200 focus:outline-none focus:border-teal-600" />
                  </div>
                </div>
                <div class="flex justify-end gap-2 mt-2">
                  <button @click="showAddAddress = false" class="px-2 py-1 text-[10px] rounded bg-gray-700/50 text-gray-400">Cancel</button>
                  <button @click="saveAddress" class="px-2 py-1 text-[10px] rounded bg-teal-600/20 text-teal-400 border border-teal-600/30">Save</button>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- ═══ SMS Contacts Tab (legacy) ═══ -->
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

    <!-- Active Meshtastic channels -->
    <div v-if="activeChannels.length" class="flex items-center gap-2 mb-3 flex-wrap">
      <span class="text-[10px] text-gray-500 uppercase tracking-wider mr-1">Channels:</span>
      <router-link v-for="ch in activeChannels" :key="ch.index" to="/radio"
        class="flex items-center gap-1 px-2 py-0.5 rounded-full border text-[10px] hover:bg-white/[0.04] transition-colors"
        :class="ch.role === 'PRIMARY' ? 'border-tactical-lora/30 text-tactical-lora' : 'border-gray-600/30 text-gray-400'">
        <span class="font-medium">{{ ch.name }}</span><span v-if="ch.pskHash" class="text-gray-600">-{{ ch.pskHash }}</span>
        <span class="text-[8px] px-1 rounded" :class="ch.role === 'PRIMARY' ? 'bg-tactical-lora/10' : 'bg-gray-700/30'">{{ ch.role === 'PRIMARY' ? 'P' : 'S' }}</span>
      </router-link>
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
              <button @click="handleRequestInfo(node)" :disabled="requestingInfo === node.num" title="Request NodeInfo"
                class="px-1.5 py-0.5 text-[10px] rounded bg-gray-700/50 text-gray-400 hover:text-emerald-400 transition-colors disabled:opacity-50">
                {{ requestingInfo === node.num ? '...' : 'Refresh' }}
              </button>
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
