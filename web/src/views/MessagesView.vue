<script setup>
import { ref, reactive, onMounted, onUnmounted, computed, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import DeliveryStatus from '@/components/DeliveryStatus.vue'
import { transportBadge, portnumLabel, shortId, formatRelativeTime, formatTimestamp } from '@/utils/format'

const store = useMeshsatStore()

// Tab: 'mesh', 'sbd', 'sms', or 'webhooks'
const activeTab = ref('mesh')

// ── SMS Conversation State ──
const smsActivePhone = ref(null)
const smsShowKeyMgmt = ref(false)
const smsKeyInput = ref('')
const smsKeyErr = ref('')

// Per-conversation encryption keys stored in localStorage
const SMS_KEYS_STORAGE = 'meshsat_sms_conv_keys'
const smsConvKeys = reactive(JSON.parse(localStorage.getItem(SMS_KEYS_STORAGE) || '{}'))

function smsSetConvKey() {
  const key = smsKeyInput.value.trim()
  if (!/^[0-9a-fA-F]{64}$/.test(key)) {
    smsKeyErr.value = 'Key must be 64 hex characters (32 bytes)'
    return
  }
  smsConvKeys[smsActivePhone.value] = key.toLowerCase()
  localStorage.setItem(SMS_KEYS_STORAGE, JSON.stringify(smsConvKeys))
  smsKeyInput.value = ''
  smsKeyErr.value = ''
}

function smsDeleteConvKey() {
  delete smsConvKeys[smsActivePhone.value]
  localStorage.setItem(SMS_KEYS_STORAGE, JSON.stringify(smsConvKeys))
}

// AES-256-GCM decryption using Web Crypto API
// Encrypted format: base64( 12-byte nonce || ciphertext || 16-byte tag )
async function tryDecryptSMS(text, hexKey) {
  if (!hexKey || !text) return null
  try {
    // Try base64 decode
    const raw = Uint8Array.from(atob(text), c => c.charCodeAt(0))
    if (raw.length < 28) return null // 12 nonce + 16 tag minimum
    const nonce = raw.slice(0, 12)
    const ciphertextAndTag = raw.slice(12)
    const keyBytes = new Uint8Array(hexKey.match(/.{2}/g).map(b => parseInt(b, 16)))
    const cryptoKey = await crypto.subtle.importKey('raw', keyBytes, 'AES-GCM', false, ['decrypt'])
    const plainBuf = await crypto.subtle.decrypt({ name: 'AES-GCM', iv: nonce }, cryptoKey, ciphertextAndTag)
    return new TextDecoder().decode(plainBuf)
  } catch {
    return null
  }
}

// Detect if text looks like base64-encoded ciphertext
function looksEncrypted(text) {
  if (!text || text.length < 40) return false
  return /^[A-Za-z0-9+/=]{40,}$/.test(text.trim())
}

// SMS conversations grouped by phone number
const smsConversations = computed(() => {
  const msgs = store.smsMessages || []
  const byPhone = {}
  for (const sms of msgs) {
    const phone = sms.phone || '(unknown)'
    if (!byPhone[phone]) byPhone[phone] = { phone, messages: [], lastTime: sms.created_at, lastText: '', count: 0 }
    byPhone[phone].messages.push(sms)
    byPhone[phone].count++
    // Track most recent message
    const t = new Date(sms.created_at || 0).getTime()
    if (t >= new Date(byPhone[phone].lastTime || 0).getTime()) {
      byPhone[phone].lastTime = sms.created_at
      byPhone[phone].lastText = sms.text || '(empty)'
    }
  }
  // Add contact names and sort by most recent
  const list = Object.values(byPhone).map(conv => {
    conv.contactName = smsContactName(conv.phone)
    return conv
  })
  list.sort((a, b) => new Date(b.lastTime || 0) - new Date(a.lastTime || 0))
  return list
})

// Active conversation messages with on-the-fly decrypt
const smsActiveConversation = computed(() => {
  if (!smsActivePhone.value) return []
  const msgs = (store.smsMessages || []).filter(s => s.phone === smsActivePhone.value)
  const key = smsConvKeys[smsActivePhone.value]
  // Sort oldest first (chat style)
  const sorted = [...msgs].sort((a, b) => new Date(a.created_at || 0) - new Date(b.created_at || 0))
  return sorted.map(sms => {
    const encrypted = looksEncrypted(sms.text)
    return { ...sms, _encrypted: encrypted && !key, _decrypted: false, _displayText: sms.text || '(empty)' }
  })
})

// Async decrypt — runs when conversation or key changes
const smsDecryptedTexts = ref({})
watch([smsActivePhone, () => smsConvKeys[smsActivePhone.value], () => store.smsMessages], async () => {
  const phone = smsActivePhone.value
  if (!phone) return
  const key = smsConvKeys[phone]
  const msgs = (store.smsMessages || []).filter(s => s.phone === phone)
  const results = {}
  for (const sms of msgs) {
    if (key && looksEncrypted(sms.text)) {
      const plain = await tryDecryptSMS(sms.text, key)
      if (plain) results[sms.id] = plain
    }
  }
  smsDecryptedTexts.value = results
}, { immediate: true, deep: true })

// SBD queue detail modal
const sbdDetailModal = ref(false)
const sbdDetailItem = ref(null)

function openSbdDetail(item) {
  sbdDetailItem.value = item
  sbdDetailModal.value = true
}

function dlqStatusColor(status) {
  if (status === 'sent' || status === 'delivered') return 'text-emerald-400 bg-emerald-400/10'
  if (status === 'received') return 'text-blue-400 bg-blue-400/10'
  if (status === 'pending') return 'text-amber-400 bg-amber-400/10'
  if (status === 'queued' || !status) return 'text-gray-400 bg-gray-400/10'
  if (status === 'failed') return 'text-red-400 bg-red-400/10'
  if (status === 'expired') return 'text-orange-400 bg-orange-400/10'
  if (status === 'cancelled') return 'text-gray-500 bg-gray-500/10'
  return 'text-gray-400 bg-gray-400/10'
}

// Compose state
const sendText = ref('')
const sendTo = ref('')
const sendChannel = ref(0)
const replyTo = ref(null)
const showPresets = ref(false)
const showCompose = ref(false)
const showPresetEditor = ref(false)
const editingPreset = ref(null)
const presetForm = ref({ name: '', text: '', destination: 'broadcast' })

// SMS quick-send state
const smsTo = ref('')
const smsMsgText = ref('')
const smsSent = ref(false)
const smsErr = ref('')

// Cell broadcast helpers
const cbsFilter = ref('all')
const filteredBroadcasts = computed(() => {
  const all = store.cellBroadcasts || []
  if (cbsFilter.value === 'all') return all
  if (cbsFilter.value === 'unacked') return all.filter(a => !a.acknowledged)
  return all.filter(a => a.severity === cbsFilter.value)
})

function cbsSeverityClass(severity) {
  switch (severity) {
    case 'extreme': return 'bg-red-900/40 text-red-400'
    case 'severe': return 'bg-orange-900/40 text-orange-400'
    case 'amber': return 'bg-amber-900/40 text-amber-400'
    case 'test': return 'bg-blue-900/40 text-blue-400'
    default: return 'bg-gray-700/40 text-gray-400'
  }
}

function cbsCardClass(severity) {
  switch (severity) {
    case 'extreme': return 'bg-red-950/30 border-red-900/40'
    case 'severe': return 'bg-orange-950/30 border-orange-900/40'
    case 'amber': return 'bg-amber-950/30 border-amber-900/40'
    case 'test': return 'bg-blue-950/30 border-blue-900/40'
    default: return 'bg-gray-800/40 border-gray-700/40'
  }
}

function smsContactName(phone) {
  const c = (store.smsContacts || []).find(c => c.phone === phone)
  return c ? c.name : ''
}

async function doSendSMS() {
  if (!smsTo.value || !smsMsgText.value) return
  smsSent.value = false
  smsErr.value = ''
  try {
    await store.sendSMS(smsTo.value, smsMsgText.value)
    smsSent.value = true
    smsMsgText.value = ''
    setTimeout(() => { smsSent.value = false }, 3000)
  } catch (e) {
    smsErr.value = e.message || 'Send failed'
  }
}

// Per-message delivery chain
const deliveryModal = ref(false)
const deliveryItems = ref([])
const deliveryMsgRef = ref('')

async function showDeliveries(msg) {
  const ref = msg.msg_ref || String(msg.id)
  deliveryMsgRef.value = ref
  deliveryItems.value = []
  deliveryModal.value = true
  try {
    const data = await store.fetchMessageDeliveries(ref)
    deliveryItems.value = Array.isArray(data) ? data : []
  } catch { /* store error */ }
}

// Filter
const filter = ref('all') // 'all', 'text', 'system'
const selectedNode = ref(null) // null = all nodes, string = specific node mailbox

const byteCount = computed(() => new TextEncoder().encode(sendText.value).length)
const feedEl = ref(null)

// Per-node mailbox list: unique nodes with message counts
const nodeMailboxes = computed(() => {
  const msgs = store.messages || []
  const counts = {}
  for (const m of msgs) {
    // Count both from_node and to_node
    if (m.from_node) {
      if (!counts[m.from_node]) counts[m.from_node] = { sent: 0, recv: 0 }
      if (m.direction === 'tx') counts[m.from_node].sent++
      else counts[m.from_node].recv++
    }
    if (m.to_node && m.to_node !== m.from_node) {
      if (!counts[m.to_node]) counts[m.to_node] = { sent: 0, recv: 0 }
      if (m.direction === 'tx') counts[m.to_node].recv++
      else counts[m.to_node].sent++
    }
  }
  // Build sorted list
  const list = Object.entries(counts).map(([id, c]) => {
    const node = (store.nodes || []).find(n => n.user_id === id || String(n.num) === id)
    return {
      id,
      name: node?.long_name || node?.short_name || id,
      shortName: node?.short_name || id.slice(-2).toUpperCase(),
      total: c.sent + c.recv,
      sent: c.sent,
      recv: c.recv
    }
  })
  list.sort((a, b) => b.total - a.total)
  return list
})

const filteredMessages = computed(() => {
  let msgs = store.messages || []
  // Node mailbox filter
  if (selectedNode.value) {
    msgs = msgs.filter(m => m.from_node === selectedNode.value || m.to_node === selectedNode.value)
  }
  if (filter.value === 'text') return msgs.filter(m => m.portnum === 1 || m.portnum_name === 'TEXT_MESSAGE_APP')
  if (filter.value === 'system') return msgs.filter(m => m.portnum !== 1 && m.portnum_name !== 'TEXT_MESSAGE_APP')
  return msgs
})

function selectNode(nodeId) {
  selectedNode.value = selectedNode.value === nodeId ? null : nodeId
}

// Group messages by date
const groupedMessages = computed(() => {
  const groups = []
  let currentDate = null
  for (const msg of filteredMessages.value) {
    const d = new Date(msg.created_at)
    const dateKey = d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' })
    if (dateKey !== currentDate) {
      currentDate = dateKey
      groups.push({ date: dateKey, messages: [] })
    }
    groups[groups.length - 1].messages.push(msg)
  }
  return groups
})

const onlineNodes = computed(() => {
  if (!store.nodes?.length) return 0
  const cutoff = Date.now() / 1000 - 7200 // 2h
  return store.nodes.filter(n => n.last_heard > cutoff).length
})

const textMsgCount = computed(() => {
  return (store.messages || []).filter(m => m.portnum === 1 || m.portnum_name === 'TEXT_MESSAGE_APP').length
})


function timeLabel(msg) {
  const d = new Date(msg.created_at)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function relativeTime(msg) {
  const now = Date.now()
  const t = new Date(msg.created_at).getTime()
  const diff = Math.floor((now - t) / 1000)
  if (diff < 60) return 'just now'
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

function isTextMessage(msg) {
  return msg.portnum === 1 || msg.portnum_name === 'TEXT_MESSAGE_APP'
}

async function send() {
  if (!sendText.value.trim()) return
  const payload = { text: sendText.value.trim() }
  if (sendTo.value) payload.to = sendTo.value
  if (sendChannel.value) payload.channel = sendChannel.value
  try {
    await store.sendMessage(payload)
    sendText.value = ''
    replyTo.value = null
    showCompose.value = false
    store.fetchMessages()
  } catch {
    // Error is set in store — input preserved for retry
  }
}

function reply(msg) {
  replyTo.value = msg
  sendTo.value = msg.from_node
  sendChannel.value = msg.channel
  showCompose.value = true
}

function cancelReply() {
  replyTo.value = null
  sendTo.value = ''
  sendChannel.value = 0
}

async function sendPreset(preset) {
  await store.sendPreset(preset.id)
  showPresets.value = false
  store.fetchMessages()
}

function editPreset(p) {
  editingPreset.value = p.id
  presetForm.value = { name: p.name, text: p.text, destination: p.destination }
  showPresetEditor.value = true
}

function newPreset() {
  editingPreset.value = null
  presetForm.value = { name: '', text: '', destination: 'broadcast' }
  showPresetEditor.value = true
}

async function savePresetForm() {
  if (editingPreset.value) {
    await store.updatePreset(editingPreset.value, presetForm.value)
  } else {
    await store.createPreset(presetForm.value)
  }
  editingPreset.value = null
  presetForm.value = { name: '', text: '', destination: 'broadcast' }
  showPresetEditor.value = false
}

async function removePreset(p) {
  if (confirm(`Delete preset "${p.name}"?`)) {
    await store.deletePreset(p.id)
  }
}

const purgeConfirming = ref(false)
const purgeDays = ref(30)

async function purgeOld() {
  if (!purgeConfirming.value) {
    purgeConfirming.value = true
    return
  }
  const d = new Date()
  d.setDate(d.getDate() - purgeDays.value)
  await store.purgeMessages(d.toISOString())
  purgeConfirming.value = false
}

let pollTimer = null
onMounted(() => {
  store.fetchMessages()
  store.fetchNodes()
  store.fetchPresets()
  store.fetchMessageStats()
  store.fetchDLQ()
  store.fetchSMSContacts()
  store.fetchSMSMessages()
  store.fetchCellBroadcasts()
  store.fetchWebhookLog()
  store.connectSSE((event) => {
    if (event.type === 'message') store.fetchMessages()
  })
  pollTimer = setInterval(() => {
    store.fetchMessages()
    store.fetchNodes()
  }, 15000)
})

onUnmounted(() => {
  store.closeSSE()
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<template>
  <div class="max-w-5xl mx-auto flex flex-col h-[calc(100vh-5rem)] lg:h-[calc(100vh-3rem)]">

    <!-- Tab selector -->
    <div class="flex gap-1 mb-3 flex-shrink-0 overflow-x-auto no-scrollbar">
      <button v-for="tab in [
        { key: 'mesh', label: 'Mesh Messages' },
        { key: 'sbd', label: 'SBD Queue' },
        { key: 'sms', label: 'SMS' },
        { key: 'broadcasts', label: 'Broadcasts' },
        { key: 'webhooks', label: 'Webhooks' }
      ]" :key="tab.key"
        @click="activeTab = tab.key"
        class="px-3 py-1.5 rounded text-xs font-medium transition-colors whitespace-nowrap"
        :class="activeTab === tab.key ? 'bg-teal-600/20 text-teal-400 border border-teal-600/30' : 'bg-gray-800/40 text-gray-500 hover:text-gray-300 border border-transparent'">
        {{ tab.label }}
        <span v-if="tab.key === 'sbd' && (store.dlq || []).filter(d => d.status === 'pending' || !d.status).length > 0"
          class="ml-1 text-[9px] px-1 py-px rounded bg-amber-400/20 text-amber-400">
          {{ (store.dlq || []).filter(d => d.status === 'pending' || !d.status).length }}
        </span>
        <span v-if="tab.key === 'webhooks' && (store.webhookLog || []).length > 0"
          class="ml-1 text-[9px] px-1 py-px rounded bg-purple-400/20 text-purple-400">
          {{ store.webhookLog.length }}
        </span>
      </button>
    </div>

    <!-- ═══ SBD Queue Tab ═══ -->
    <div v-if="activeTab === 'sbd'" class="flex-1 overflow-y-auto min-h-0">
      <div v-if="!(store.dlq || []).length" class="flex flex-col items-center justify-center h-full text-gray-500">
        <svg class="w-10 h-10 mb-3 text-gray-700" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1">
          <path stroke-linecap="round" stroke-linejoin="round" d="M20 13V6a2 2 0 00-2-2H6a2 2 0 00-2 2v7m16 0v5a2 2 0 01-2 2H6a2 2 0 01-2-2v-5m16 0h-2.586a1 1 0 00-.707.293l-2.414 2.414a1 1 0 01-.707.293h-3.172a1 1 0 01-.707-.293l-2.414-2.414A1 1 0 006.586 13H4" />
        </svg>
        <span class="text-sm">No SBD messages in queue</span>
      </div>
      <div v-else class="space-y-1">
        <div v-for="item in (store.dlq || [])" :key="item.id"
          class="flex items-center gap-2 py-2 px-3 rounded-lg bg-gray-800/40 hover:bg-gray-800/60 cursor-pointer transition-colors"
          @click="openSbdDetail(item)">
          <span class="text-[9px] font-mono shrink-0"
            :class="item.direction === 'inbound' ? 'text-blue-400' : 'text-teal-400'">
            {{ item.direction === 'inbound' ? 'SBD\u2192Mesh' : 'Mesh\u2192SBD' }}
          </span>
          <span class="text-[10px] font-mono px-1.5 py-px rounded" :class="dlqStatusColor(item.status)">
            {{ item.status || 'queued' }}
          </span>
          <span class="text-[11px] text-gray-300 truncate flex-1">{{ item.text_preview || '(binary)' }}</span>
          <span v-if="item.retries > 0" class="text-[9px] text-amber-400/60 font-mono shrink-0">{{ item.retries }}x</span>
          <span class="text-[10px] text-gray-600 font-mono shrink-0">{{ formatRelativeTime(item.created_at) }}</span>
        </div>
      </div>

      <!-- SBD Detail Modal -->
      <Teleport to="body">
        <div v-if="sbdDetailModal && sbdDetailItem" class="fixed inset-0 z-[100] flex items-center justify-center bg-black/70 backdrop-blur-sm" @click.self="sbdDetailModal = false">
          <div class="bg-gray-900 border border-gray-700 rounded-lg w-full max-w-2xl max-h-[85vh] overflow-y-auto m-4">
            <div class="sticky top-0 bg-gray-900 border-b border-gray-700 px-4 py-3 flex items-center justify-between">
              <h3 class="font-semibold text-sm text-teal-400">SBD ITEM #{{ sbdDetailItem.id }}</h3>
              <button @click="sbdDetailModal = false" class="text-gray-500 hover:text-gray-300 text-lg">&times;</button>
            </div>
            <div class="p-4 space-y-4">
              <div class="grid grid-cols-2 gap-1.5 text-[11px]">
                <div class="flex justify-between"><span class="text-gray-500">Direction</span><span class="text-gray-300 font-mono">{{ sbdDetailItem.direction || 'outbound' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Status</span><span class="font-mono px-1.5 py-px rounded" :class="dlqStatusColor(sbdDetailItem.status)">{{ sbdDetailItem.status || 'queued' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Priority</span><span class="text-gray-300 font-mono">{{ sbdDetailItem.priority ?? 'N/A' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Retries</span><span class="text-gray-300 font-mono">{{ sbdDetailItem.retries ?? 0 }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Created</span><span class="text-gray-400 font-mono text-[10px]">{{ formatTimestamp(sbdDetailItem.created_at) }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Updated</span><span class="text-gray-400 font-mono text-[10px]">{{ formatTimestamp(sbdDetailItem.updated_at) }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Last Error</span><span class="text-gray-400 font-mono text-[10px] truncate">{{ sbdDetailItem.last_error || 'None' }}</span></div>
                <div class="flex justify-between"><span class="text-gray-500">Packet ID</span><span class="text-gray-300 font-mono">{{ sbdDetailItem.packet_id || 'N/A' }}</span></div>
              </div>
              <div>
                <h4 class="text-[10px] text-gray-500 uppercase mb-1">Payload</h4>
                <div class="text-[11px] text-gray-300">{{ sbdDetailItem.text_preview || '(none)' }}</div>
              </div>
              <div>
                <h4 class="text-[10px] text-gray-500 uppercase mb-1">Raw</h4>
                <pre class="text-[10px] font-mono text-gray-400 whitespace-pre-wrap break-all bg-gray-800 rounded p-3 max-h-[200px] overflow-y-auto select-all">{{ JSON.stringify(sbdDetailItem, null, 2) }}</pre>
              </div>
              <!-- Actions -->
              <div v-if="sbdDetailItem.status === 'pending' || !sbdDetailItem.status" class="flex items-center gap-2 pt-2 border-t border-gray-700">
                <span class="text-[10px] text-gray-500">Priority:</span>
                <button @click="store.setQueuePriority(sbdDetailItem.id, 0); sbdDetailItem.priority = 0"
                  class="px-2 py-1 rounded text-[10px]" :class="sbdDetailItem.priority === 0 ? 'bg-red-400/20 text-red-400' : 'bg-gray-700 text-gray-400 hover:text-red-400'">Critical</button>
                <button @click="store.setQueuePriority(sbdDetailItem.id, 1); sbdDetailItem.priority = 1"
                  class="px-2 py-1 rounded text-[10px]" :class="sbdDetailItem.priority === 1 ? 'bg-amber-400/20 text-amber-400' : 'bg-gray-700 text-gray-400 hover:text-amber-400'">Normal</button>
                <button @click="store.setQueuePriority(sbdDetailItem.id, 2); sbdDetailItem.priority = 2"
                  class="px-2 py-1 rounded text-[10px]" :class="sbdDetailItem.priority === 2 ? 'bg-gray-600 text-gray-400' : 'bg-gray-700 text-gray-400'">Low</button>
                <span class="flex-1" />
                <button @click="store.cancelQueueItem(sbdDetailItem.id); sbdDetailModal = false"
                  class="px-3 py-1 rounded text-[10px] bg-red-900/30 text-red-400 hover:bg-red-900/50">Cancel Item</button>
              </div>
            </div>
          </div>
        </div>
      </Teleport>
    </div>

    <!-- ═══ SMS Tab ═══ -->
    <div v-if="activeTab === 'sms'" class="flex-1 overflow-y-auto min-h-0">
      <!-- Conversation list (no phone selected) -->
      <div v-if="!smsActivePhone">
        <!-- Quick send -->
        <div class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-3 mb-3">
          <div class="text-[11px] text-gray-500 mb-2">Quick Send SMS</div>
          <div class="flex gap-2">
            <input v-model="smsTo" type="text" placeholder="+31612345678"
              class="w-40 px-2.5 py-1.5 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200 focus:outline-none focus:border-teal-600" />
            <input v-model="smsMsgText" type="text" placeholder="Message text..."
              class="flex-1 px-2.5 py-1.5 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200 focus:outline-none focus:border-teal-600"
              @keydown.enter="doSendSMS" />
            <button @click="doSendSMS" :disabled="!smsTo || !smsMsgText"
              class="px-3 py-1.5 text-xs rounded bg-sky-600/20 text-sky-400 border border-sky-600/30 hover:bg-sky-600/30 transition-colors disabled:opacity-40">
              Send
            </button>
          </div>
          <div v-if="smsSent" class="text-[10px] text-emerald-400 mt-1.5">SMS sent successfully</div>
          <div v-if="smsErr" class="text-[10px] text-red-400 mt-1.5">{{ smsErr }}</div>
        </div>

        <!-- Contacts quick-dial -->
        <div v-if="(store.smsContacts || []).length > 0" class="mb-3">
          <div class="text-[11px] text-gray-500 mb-1.5">Contacts</div>
          <div class="flex flex-wrap gap-1.5">
            <button v-for="c in store.smsContacts" :key="c.id" @click="smsTo = c.phone"
              class="px-2 py-1 text-[10px] rounded bg-gray-800/60 text-gray-400 hover:text-sky-400 border border-gray-700/50 hover:border-sky-600/30 transition-colors">
              {{ c.name }} <span class="text-gray-600 font-mono">{{ c.phone }}</span>
            </button>
          </div>
        </div>

        <!-- Conversations -->
        <div class="mt-3">
          <div class="flex items-center justify-between mb-2">
            <div class="text-[11px] text-gray-500">Conversations</div>
            <button @click="store.fetchSMSMessages()" class="px-2.5 py-1 text-[10px] rounded bg-gray-800 text-gray-400 hover:text-gray-200 transition-colors">Refresh</button>
          </div>
          <div v-if="!smsConversations.length" class="text-center text-[11px] text-gray-600 py-8">No SMS messages yet.</div>
          <div v-else class="space-y-1">
            <div v-for="conv in smsConversations" :key="conv.phone"
              @click="smsActivePhone = conv.phone; smsTo = conv.phone"
              class="flex items-center gap-3 py-2.5 px-3 rounded-lg bg-gray-800/40 border border-gray-700/30 cursor-pointer hover:bg-gray-700/40 transition-colors">
              <div class="w-8 h-8 rounded-full bg-sky-900/40 flex items-center justify-center text-sky-400 text-xs font-bold shrink-0">
                {{ (conv.contactName || conv.phone).slice(0, 2).toUpperCase() }}
              </div>
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2">
                  <span v-if="conv.contactName" class="text-xs text-gray-200">{{ conv.contactName }}</span>
                  <span class="text-xs font-mono" :class="conv.contactName ? 'text-gray-500 text-[10px]' : 'text-gray-200'">{{ conv.phone }}</span>
                  <span v-if="smsConvKeys[conv.phone]" class="text-[9px] text-amber-400/60" title="Encryption key set">ENC</span>
                </div>
                <div class="text-[11px] text-gray-400 mt-0.5 truncate">{{ conv.lastText }}</div>
              </div>
              <div class="text-right shrink-0">
                <div class="text-[9px] text-gray-600 font-mono">{{ formatRelativeTime(conv.lastTime) }}</div>
                <div class="text-[10px] text-gray-500 mt-0.5">{{ conv.count }} msg{{ conv.count !== 1 ? 's' : '' }}</div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Conversation chat view (phone selected) -->
      <div v-else class="flex flex-col h-full">
        <!-- Header -->
        <div class="flex items-center gap-2 pb-3 border-b border-gray-700/50 mb-3 shrink-0">
          <button @click="smsActivePhone = null" class="px-2 py-1 text-[10px] rounded bg-gray-800 text-gray-400 hover:text-gray-200 transition-colors">Back</button>
          <div class="flex-1 min-w-0">
            <span v-if="smsContactName(smsActivePhone)" class="text-xs text-gray-200">{{ smsContactName(smsActivePhone) }}</span>
            <span class="text-xs font-mono" :class="smsContactName(smsActivePhone) ? 'text-gray-500 text-[10px] ml-2' : 'text-gray-200'">{{ smsActivePhone }}</span>
          </div>
          <button @click="smsShowKeyMgmt = !smsShowKeyMgmt"
            class="px-2 py-1 text-[10px] rounded transition-colors"
            :class="smsConvKeys[smsActivePhone] ? 'bg-amber-900/30 text-amber-400 hover:bg-amber-900/50' : 'bg-gray-800 text-gray-400 hover:text-gray-200'">
            {{ smsConvKeys[smsActivePhone] ? 'ENC' : 'Key' }}
          </button>
        </div>

        <!-- Key management panel -->
        <div v-if="smsShowKeyMgmt" class="bg-gray-800/60 rounded-lg border border-gray-700/50 p-3 mb-3 shrink-0">
          <div class="text-[11px] text-gray-500 mb-2">Per-Conversation Encryption Key (AES-256-GCM)</div>
          <div class="flex gap-2">
            <input v-model="smsKeyInput" type="text" :placeholder="smsConvKeys[smsActivePhone] ? '(key set — enter new to replace)' : '64-char hex key'"
              class="flex-1 px-2.5 py-1.5 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200 font-mono focus:outline-none focus:border-amber-600" />
            <button @click="smsSetConvKey" :disabled="!smsKeyInput"
              class="px-3 py-1.5 text-xs rounded bg-amber-600/20 text-amber-400 border border-amber-600/30 hover:bg-amber-600/30 transition-colors disabled:opacity-40">
              Set
            </button>
            <button v-if="smsConvKeys[smsActivePhone]" @click="smsDeleteConvKey"
              class="px-3 py-1.5 text-xs rounded bg-red-900/20 text-red-400 border border-red-600/30 hover:bg-red-900/30 transition-colors">
              Delete
            </button>
          </div>
          <div v-if="smsKeyErr" class="text-[10px] text-red-400 mt-1.5">{{ smsKeyErr }}</div>
          <div v-if="smsConvKeys[smsActivePhone]" class="text-[10px] text-amber-400/60 mt-1.5">Key active — messages will be decrypted on the fly</div>
        </div>

        <!-- Chat messages -->
        <div class="flex-1 overflow-y-auto min-h-0 space-y-1 mb-3">
          <div v-for="sms in smsActiveConversation" :key="sms.id"
            class="flex" :class="sms.direction === 'tx' ? 'justify-end' : 'justify-start'">
            <div class="max-w-[80%] py-1.5 px-3 rounded-lg text-[11px]"
              :class="sms.direction === 'tx' ? 'bg-sky-900/30 border border-sky-800/40 text-gray-200' : 'bg-gray-800/60 border border-gray-700/40 text-gray-300'">
              <div class="break-words whitespace-pre-wrap"
                :class="looksEncrypted(sms.text) && !smsDecryptedTexts[sms.id] ? 'font-mono text-[10px] text-gray-500' : ''">
                {{ smsDecryptedTexts[sms.id] || sms.text || '(empty)' }}
              </div>
              <div class="text-[9px] mt-1 flex items-center gap-2"
                :class="sms.direction === 'tx' ? 'text-sky-400/40' : 'text-gray-600'">
                <span>{{ formatTimestamp(sms.created_at) }}</span>
                <span v-if="looksEncrypted(sms.text) && !smsDecryptedTexts[sms.id]" class="text-amber-400/40" title="Encrypted — add key to decrypt">locked</span>
                <span v-if="smsDecryptedTexts[sms.id]" class="text-emerald-400/40" title="Decrypted with conversation key">decrypted</span>
                <span class="font-mono">{{ sms.status }}</span>
              </div>
            </div>
          </div>
          <div v-if="!smsActiveConversation.length" class="text-center text-[11px] text-gray-600 py-8">No messages in this conversation.</div>
        </div>

        <!-- Compose bar -->
        <div class="flex gap-2 pt-2 border-t border-gray-700/50 shrink-0">
          <input v-model="smsMsgText" type="text" placeholder="Type a message..."
            class="flex-1 px-2.5 py-1.5 rounded bg-gray-900/60 border border-gray-700 text-xs text-gray-200 focus:outline-none focus:border-sky-600"
            @keydown.enter="doSendSMS" />
          <button @click="doSendSMS" :disabled="!smsMsgText"
            class="px-3 py-1.5 text-xs rounded bg-sky-600/20 text-sky-400 border border-sky-600/30 hover:bg-sky-600/30 transition-colors disabled:opacity-40">
            Send
          </button>
        </div>
        <div v-if="smsSent" class="text-[10px] text-emerald-400 mt-1">Sent</div>
        <div v-if="smsErr" class="text-[10px] text-red-400 mt-1">{{ smsErr }}</div>
      </div>
    </div>

    <!-- ═══ Broadcasts Tab ═══ -->
    <div v-if="activeTab === 'broadcasts'" class="flex-1 overflow-y-auto min-h-0">
      <div class="flex items-center justify-between mb-3">
        <div class="flex items-center gap-2">
          <div class="text-[11px] text-gray-500">Cell Broadcast Alerts</div>
          <select v-model="cbsFilter" class="px-2 py-1 text-[10px] rounded bg-gray-800 border border-gray-700 text-gray-300">
            <option value="all">All</option>
            <option value="extreme">Extreme</option>
            <option value="severe">Severe</option>
            <option value="amber">AMBER</option>
            <option value="test">Test</option>
            <option value="unacked">Unacknowledged</option>
          </select>
        </div>
        <button @click="store.fetchCellBroadcasts()"
          class="px-2.5 py-1 text-[10px] rounded bg-gray-800 text-gray-400 hover:text-gray-200 transition-colors">
          Refresh
        </button>
      </div>

      <div v-if="!filteredBroadcasts.length" class="flex flex-col items-center justify-center py-16 text-gray-500">
        <svg class="w-10 h-10 mb-3 text-gray-700" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1">
          <path stroke-linecap="round" stroke-linejoin="round" d="M10.34 15.84c-.688-.06-1.386-.09-2.09-.09H7.5a4.5 4.5 0 110-9h.75c.704 0 1.402-.03 2.09-.09m0 9.18c.253.962.584 1.892.985 2.783.247.55.06 1.21-.463 1.511l-.657.38c-.551.318-1.26.117-1.527-.461a20.845 20.845 0 01-1.44-4.282m3.102.069a18.03 18.03 0 01-.59-4.59c0-1.586.205-3.124.59-4.59m0 9.18a23.848 23.848 0 018.835 2.535M10.34 6.66a23.847 23.847 0 008.835-2.535m0 0A23.74 23.74 0 0018.795 3m.38 1.125a23.91 23.91 0 011.014 5.395m-1.014 8.855c-.118.38-.245.754-.38 1.125m.38-1.125a23.91 23.91 0 001.014-5.395m0-3.46c.495.413.811 1.035.811 1.73 0 .695-.316 1.317-.811 1.73m0-3.46a24.347 24.347 0 010 3.46" />
        </svg>
        <div class="text-[11px]">No cell broadcasts received</div>
        <div class="text-[10px] text-gray-600 mt-1">Government emergency alerts (EU-Alert, WEA, CMAS) appear here</div>
      </div>
      <div v-else class="space-y-1.5">
        <div v-for="alert in filteredBroadcasts" :key="alert.id"
          class="flex items-start gap-2 py-2 px-3 rounded-lg border"
          :class="alert.acknowledged ? 'bg-gray-800/30 border-gray-700/30' : cbsCardClass(alert.severity)">
          <span class="mt-0.5 text-[9px] font-mono font-bold px-1.5 py-0.5 rounded"
            :class="cbsSeverityClass(alert.severity)">
            {{ (alert.severity || 'info').toUpperCase() }}
          </span>
          <div class="flex-1 min-w-0">
            <div class="text-[11px] text-gray-200">{{ alert.text || '(no text)' }}</div>
            <div class="text-[9px] text-gray-600 mt-0.5">
              {{ formatTimestamp(alert.created_at) }}
              <span v-if="alert.message_id" class="ml-2 text-gray-700">ID:{{ alert.message_id }} Ch:{{ alert.channel }}</span>
            </div>
          </div>
          <button v-if="!alert.acknowledged" @click="store.ackCellBroadcast(alert.id)"
            class="text-[10px] px-2 py-1 rounded bg-gray-700/50 text-gray-400 hover:text-gray-200 shrink-0">
            ACK
          </button>
          <span v-else class="text-[9px] text-gray-600">acked</span>
        </div>
      </div>
    </div>

    <!-- ═══ Webhooks Tab ═══ -->
    <div v-if="activeTab === 'webhooks'" class="flex-1 overflow-y-auto min-h-0">
      <div class="flex items-center justify-between mb-3">
        <div class="text-[11px] text-gray-500">Recent webhook activity</div>
        <button @click="store.fetchWebhookLog()"
          class="px-2.5 py-1 text-[10px] rounded bg-gray-800 text-gray-400 hover:text-gray-200 transition-colors">
          Refresh
        </button>
      </div>

      <div v-if="!(store.webhookLog || []).length" class="flex flex-col items-center justify-center py-16 text-gray-500">
        <svg class="w-10 h-10 mb-3 text-gray-700" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1">
          <path stroke-linecap="round" stroke-linejoin="round" d="M13.19 8.688a4.5 4.5 0 011.242 7.244l-4.5 4.5a4.5 4.5 0 01-6.364-6.364l1.757-1.757m9.556-9.556a4.5 4.5 0 00-6.364 0l-4.5 4.5a4.5 4.5 0 001.242 7.244" />
        </svg>
        <span class="text-sm font-medium mb-1">No Webhook Activity</span>
        <span class="text-[12px] text-gray-600 text-center max-w-xs">
          Configure outbound webhook URLs in the cellular gateway settings, or send inbound webhooks to <span class="font-mono text-gray-500">/api/webhooks/cellular/inbound</span>.
        </span>
      </div>

      <div v-else class="space-y-1">
        <div v-for="entry in store.webhookLog" :key="entry.id"
          class="flex items-center gap-2 py-2 px-3 rounded-lg bg-gray-800/40 hover:bg-gray-800/60 transition-colors">
          <span class="text-[9px] font-mono shrink-0 w-14"
            :class="entry.direction === 'inbound' ? 'text-blue-400' : 'text-purple-400'">
            {{ entry.direction === 'inbound' ? 'IN' : 'OUT' }}
          </span>
          <span class="text-[10px] font-mono px-1.5 py-px rounded shrink-0"
            :class="entry.status >= 200 && entry.status < 300 ? 'text-emerald-400 bg-emerald-400/10' : entry.status >= 400 ? 'text-red-400 bg-red-400/10' : 'text-gray-400 bg-gray-400/10'">
            {{ entry.status || '-' }}
          </span>
          <span class="text-[10px] text-gray-500 font-mono shrink-0">{{ entry.method }}</span>
          <span class="text-[11px] text-gray-400 truncate flex-1" :title="entry.url">{{ entry.url }}</span>
          <span v-if="entry.error" class="text-[10px] text-red-400/70 truncate max-w-[120px]" :title="entry.error">{{ entry.error }}</span>
          <span class="text-[10px] text-gray-600 font-mono shrink-0">{{ formatRelativeTime(entry.created_at) }}</span>
        </div>
      </div>
    </div>

    <!-- ═══ Mesh Messages Tab ═══ -->
    <template v-if="activeTab === 'mesh'">

    <!-- Status cards row -->
    <div class="grid grid-cols-3 gap-2 mb-3 flex-shrink-0">
      <div class="bg-gray-800/60 rounded-lg px-3 py-2 border border-gray-700/50">
        <div class="text-[10px] uppercase tracking-wider text-gray-500 mb-0.5">Nodes</div>
        <div class="flex items-baseline gap-1.5">
          <span class="text-lg font-semibold text-gray-100">{{ store.status?.num_nodes || 0 }}</span>
          <span class="text-[10px] text-emerald-400/70">{{ onlineNodes }} active</span>
        </div>
      </div>
      <div class="bg-gray-800/60 rounded-lg px-3 py-2 border border-gray-700/50">
        <div class="text-[10px] uppercase tracking-wider text-gray-500 mb-0.5">Messages Today</div>
        <div class="flex items-baseline gap-1.5">
          <span class="text-lg font-semibold text-gray-100">{{ store.messageStats?.today_text || 0 }}</span>
          <span class="text-[10px] text-gray-500">text</span>
          <span class="text-[10px] text-gray-600">/ {{ store.messageStats?.today || 0 }} total</span>
        </div>
        <div class="flex items-center gap-1.5 mt-0.5">
          <span class="text-[10px] text-emerald-400/60">{{ store.messageStats?.by_transport?.radio || 0 }} radio</span>
          <span v-if="store.messageStats?.by_transport?.iridium" class="text-[10px] text-blue-400/60">{{ store.messageStats.by_transport.iridium }} sat</span>
          <span v-if="store.messageStats?.by_transport?.mqtt" class="text-[10px] text-purple-400/60">{{ store.messageStats.by_transport.mqtt }} mqtt</span>
        </div>
      </div>
      <div class="bg-gray-800/60 rounded-lg px-3 py-2 border border-gray-700/50">
        <div class="text-[10px] uppercase tracking-wider text-gray-500 mb-0.5">Total stored</div>
        <div class="flex items-baseline gap-1.5">
          <span class="text-lg font-semibold text-gray-100">{{ store.messageStats?.total || 0 }}</span>
          <button v-if="store.messageStats?.total > 100 && !purgeConfirming" @click="purgeOld"
            class="text-[10px] text-gray-600 hover:text-red-400 transition-colors">clear old</button>
        </div>
        <div v-if="store.messageStats?.by_portnum" class="flex items-center gap-1.5 mt-0.5 flex-wrap">
          <span v-if="store.messageStats.by_portnum['TEXT_MESSAGE_APP']" class="text-[10px] text-emerald-400/60">{{ store.messageStats.by_portnum['TEXT_MESSAGE_APP'] }} text</span>
          <span v-if="store.messageStats.by_portnum['TELEMETRY_APP']" class="text-[10px] text-amber-400/60">{{ store.messageStats.by_portnum['TELEMETRY_APP'] }} telem</span>
          <span v-if="store.messageStats.by_portnum['POSITION_APP']" class="text-[10px] text-cyan-400/60">{{ store.messageStats.by_portnum['POSITION_APP'] }} pos</span>
          <span v-if="store.messageStats.by_portnum['NODEINFO_APP']" class="text-[10px] text-gray-500">{{ store.messageStats.by_portnum['NODEINFO_APP'] }} node</span>
        </div>
      </div>
    </div>

    <!-- Toolbar: filter tabs + actions -->
    <div class="flex items-center justify-between mb-2 flex-shrink-0">
      <div class="flex gap-1">
        <button v-for="f in [
          { key: 'text', label: 'Text' },
          { key: 'all', label: 'All' },
          { key: 'system', label: 'System' }
        ]" :key="f.key"
          @click="filter = f.key"
          class="px-2.5 py-1 rounded text-xs font-medium transition-colors"
          :class="filter === f.key ? 'bg-gray-700 text-gray-200' : 'text-gray-500 hover:text-gray-300'">
          {{ f.label }}
        </button>
      </div>
      <div class="flex items-center gap-2">
        <!-- Presets trigger -->
        <button @click="showPresets = !showPresets"
          class="px-2.5 py-1 rounded text-xs text-gray-400 hover:text-gray-200 hover:bg-gray-800 transition-colors"
          title="Quick presets">
          <svg class="w-4 h-4 inline-block" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1.5">
            <path stroke-linecap="round" stroke-linejoin="round" d="M3.75 6A2.25 2.25 0 016 3.75h2.25A2.25 2.25 0 0110.5 6v2.25a2.25 2.25 0 01-2.25 2.25H6a2.25 2.25 0 01-2.25-2.25V6zM3.75 15.75A2.25 2.25 0 016 13.5h2.25a2.25 2.25 0 012.25 2.25V18a2.25 2.25 0 01-2.25 2.25H6A2.25 2.25 0 013.75 18v-2.25zM13.5 6a2.25 2.25 0 012.25-2.25H18A2.25 2.25 0 0120.25 6v2.25A2.25 2.25 0 0118 10.5h-2.25a2.25 2.25 0 01-2.25-2.25V6zM13.5 15.75a2.25 2.25 0 012.25-2.25H18a2.25 2.25 0 012.25 2.25V18A2.25 2.25 0 0118 20.25h-2.25A2.25 2.25 0 0113.5 18v-2.25z" />
          </svg>
          Presets
        </button>
        <!-- Compose toggle -->
        <button @click="showCompose = !showCompose"
          class="px-3 py-1 rounded text-xs font-medium bg-teal-600 text-white hover:bg-teal-500 transition-colors">
          + Message
        </button>
      </div>
    </div>

    <!-- Node mailbox selector -->
    <div v-if="nodeMailboxes.length > 1" class="flex items-center gap-1.5 mb-2 flex-shrink-0 overflow-x-auto no-scrollbar">
      <button @click="selectedNode = null"
        class="px-2.5 py-1 rounded text-[11px] font-medium whitespace-nowrap transition-colors shrink-0"
        :class="!selectedNode ? 'bg-teal-600/20 text-teal-400 border border-teal-600/30' : 'bg-gray-800/40 text-gray-500 hover:text-gray-300 border border-transparent'">
        All
      </button>
      <button v-for="mb in nodeMailboxes" :key="mb.id" @click="selectNode(mb.id)"
        class="flex items-center gap-1.5 px-2.5 py-1 rounded text-[11px] font-medium whitespace-nowrap transition-colors shrink-0"
        :class="selectedNode === mb.id ? 'bg-teal-600/20 text-teal-400 border border-teal-600/30' : 'bg-gray-800/40 text-gray-500 hover:text-gray-300 border border-transparent'">
        <span class="w-4 h-4 rounded-full bg-gray-700 text-[8px] font-bold flex items-center justify-center text-gray-400">
          {{ mb.shortName }}
        </span>
        <span class="truncate max-w-[100px]">{{ mb.name }}</span>
        <span class="text-[9px] opacity-60">{{ mb.total }}</span>
      </button>
    </div>

    <!-- Selected node info bar -->
    <div v-if="selectedNode" class="flex items-center gap-2 mb-2 flex-shrink-0 px-3 py-1.5 bg-teal-900/20 border border-teal-800/30 rounded-lg">
      <span class="text-[11px] text-teal-400">Mailbox:</span>
      <span class="text-xs text-gray-200 font-medium">{{ nodeMailboxes.find(m => m.id === selectedNode)?.name || selectedNode }}</span>
      <span class="text-[10px] text-gray-500">{{ filteredMessages.length }} messages</span>
      <span class="flex-1" />
      <button @click="selectedNode = null" class="text-[10px] text-gray-500 hover:text-gray-300">Clear</button>
    </div>

    <!-- Presets dropdown -->
    <div v-if="showPresets" class="mb-2 flex-shrink-0">
      <div class="bg-gray-800/60 rounded-lg p-2 border border-gray-700/50">
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-1.5">
          <div v-for="preset in store.presets" :key="preset.id"
            class="px-3 py-2 rounded bg-gray-700/50 hover:bg-gray-600/50 border border-gray-600/30 text-left transition-colors group relative">
            <button @click="sendPreset(preset)" class="w-full text-left">
              <div class="text-xs font-medium text-gray-200 truncate">{{ preset.name }}</div>
              <div class="text-[10px] text-gray-500 truncate mt-0.5">{{ preset.text }}</div>
            </button>
            <div class="absolute top-1 right-1 hidden group-hover:flex gap-1">
              <button @click.stop="editPreset(preset)" class="text-[9px] text-gray-500 hover:text-teal-400 px-1">Edit</button>
              <button @click.stop="removePreset(preset)" class="text-[9px] text-gray-500 hover:text-red-400 px-1">Del</button>
            </div>
          </div>
        </div>
        <div v-if="!store.presets?.length" class="text-center text-xs text-gray-500 py-2">
          No presets yet
        </div>
        <div class="flex justify-end mt-2">
          <button @click="newPreset" class="text-[10px] text-teal-400 hover:text-teal-300">+ New Preset</button>
        </div>
      </div>
    </div>

    <!-- Preset editor modal -->
    <div v-if="showPresetEditor" class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" @click.self="showPresetEditor = false">
      <div class="bg-gray-800 rounded-xl border border-gray-700 w-full max-w-md p-5">
        <h3 class="text-sm font-medium text-gray-200 mb-4">{{ editingPreset ? 'Edit Preset' : 'New Preset' }}</h3>
        <div class="space-y-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Name</label>
            <input v-model="presetForm.name" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="I'm OK">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Text</label>
            <textarea v-model="presetForm.text" rows="2" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="All good, checking in on schedule."></textarea>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Destination</label>
            <input v-model="presetForm.destination" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="broadcast">
          </div>
          <div class="flex gap-2 mt-4">
            <button @click="showPresetEditor = false" class="flex-1 py-2 rounded bg-gray-700 text-gray-300 text-sm hover:bg-gray-600">Cancel</button>
            <button @click="savePresetForm" class="flex-1 py-2 rounded bg-teal-600 text-white text-sm font-medium hover:bg-teal-500">
              {{ editingPreset ? 'Update' : 'Create' }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Compose panel (expandable) -->
    <div v-if="showCompose" class="mb-2 flex-shrink-0 bg-gray-800/60 rounded-lg border border-gray-700/50 p-3">
      <div v-if="replyTo" class="flex items-center gap-2 mb-2 px-2 py-1 bg-blue-900/20 rounded text-xs text-blue-400 border border-blue-800/30">
        <svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="2">
          <path stroke-linecap="round" stroke-linejoin="round" d="M3 10h10a8 8 0 018 8v2M3 10l6 6m-6-6l6-6" />
        </svg>
        <span>{{ shortId(replyTo.from_node) }}</span>
        <button @click="cancelReply" class="ml-auto text-blue-500 hover:text-blue-300">
          <svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="2">
            <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
          </svg>
        </button>
      </div>
      <div class="flex gap-2">
        <input v-model="sendText" @keyup.enter="send" placeholder="Type a message..."
          class="flex-1 px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 placeholder-gray-600 focus:border-teal-600 focus:outline-none">
        <button @click="send" :disabled="!sendText.trim()"
          class="px-4 py-2 rounded bg-teal-600 text-white text-sm font-medium hover:bg-teal-500 disabled:opacity-30 disabled:cursor-not-allowed transition-colors">
          Send
        </button>
      </div>
      <div class="flex items-center justify-between mt-2 px-0.5">
        <div class="flex gap-2">
          <input v-model="sendTo" placeholder="To (broadcast)"
            class="w-24 px-2 py-1 rounded bg-gray-900 border border-gray-700 text-[11px] text-gray-400 placeholder-gray-600 focus:border-teal-600 focus:outline-none">
          <select v-model.number="sendChannel"
            class="px-2 py-1 rounded bg-gray-900 border border-gray-700 text-[11px] text-gray-400 focus:outline-none">
            <option :value="0">Ch 0</option>
            <option :value="1">Ch 1</option>
            <option :value="2">Ch 2</option>
            <option :value="3">Ch 3</option>
          </select>
        </div>
        <span class="text-[11px] font-mono" :class="byteCount > 320 ? 'text-red-400' : byteCount > 200 ? 'text-amber-400' : 'text-gray-500'">
          {{ byteCount }}/340 B
        </span>
      </div>
    </div>

    <!-- Message feed -->
    <div ref="feedEl" class="flex-1 overflow-y-auto min-h-0 space-y-0.5 pr-1">
      <div v-if="filteredMessages.length === 0" class="flex flex-col items-center justify-center h-full text-gray-500">
        <svg class="w-10 h-10 mb-3 text-gray-700" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1">
          <path stroke-linecap="round" stroke-linejoin="round" d="M8 12h.01M12 12h.01M16 12h.01M21 12c0 4.418-4.03 8-9 8a9.863 9.863 0 01-4.255-.949L3 20l1.395-3.72C3.512 15.042 3 13.574 3 12c0-4.418 4.03-8 9-8s9 3.582 9 8z" />
        </svg>
        <span class="text-sm">{{ filter === 'text' ? 'No text messages yet' : 'No messages yet' }}</span>
      </div>

      <template v-for="group in groupedMessages" :key="group.date">
        <!-- Date separator -->
        <div class="flex items-center gap-3 py-2">
          <div class="flex-1 h-px bg-gray-800"></div>
          <span class="text-[10px] text-gray-600 font-medium uppercase tracking-wider">{{ group.date }}</span>
          <div class="flex-1 h-px bg-gray-800"></div>
        </div>

        <template v-for="msg in group.messages" :key="msg.id">
          <!-- Text message: chat bubble style -->
          <div v-if="isTextMessage(msg)"
            class="flex gap-2 px-1 py-1 group" :class="msg.direction === 'tx' ? 'flex-row-reverse' : ''">
            <!-- Avatar circle -->
            <div class="flex-shrink-0 w-7 h-7 rounded-full flex items-center justify-center text-[10px] font-bold mt-0.5"
              :class="msg.direction === 'tx' ? 'bg-teal-900/50 text-teal-400' : 'bg-gray-700 text-gray-400'">
              {{ (msg.from_node || '?').slice(-2).toUpperCase() }}
            </div>
            <!-- Bubble -->
            <div class="max-w-[75%] min-w-[120px]">
              <div class="flex items-center gap-1.5 mb-0.5" :class="msg.direction === 'tx' ? 'flex-row-reverse' : ''">
                <span class="text-[11px] font-medium" :class="msg.direction === 'tx' ? 'text-teal-400' : 'text-gray-400'">
                  {{ shortId(msg.from_node) }}
                </span>
                <span class="px-1 py-px rounded text-[9px] font-medium border"
                  :class="transportBadge(msg.transport).cls">
                  {{ transportBadge(msg.transport).label }}
                </span>
                <span class="text-[10px] text-gray-600">Ch {{ msg.channel }}</span>
              </div>
              <div class="rounded-lg px-3 py-2"
                :class="msg.direction === 'tx'
                  ? 'bg-teal-900/30 border border-teal-800/40 rounded-tr-sm'
                  : 'bg-gray-800 border border-gray-700/50 rounded-tl-sm'">
                <p class="text-sm text-gray-200 break-words whitespace-pre-wrap leading-relaxed">{{ msg.decoded_text || '(empty)' }}</p>
              </div>
              <div class="flex items-center gap-2 mt-0.5 px-1" :class="msg.direction === 'tx' ? 'flex-row-reverse' : ''">
                <span class="text-[10px] text-gray-600">{{ timeLabel(msg) }}</span>
                <DeliveryStatus v-if="msg.delivery_status && msg.delivery_status !== 'received'" :status="msg.delivery_status" />
                <button v-if="msg.msg_ref || msg.id" @click="showDeliveries(msg)"
                  class="text-[10px] text-gray-600 hover:text-blue-400 opacity-0 group-hover:opacity-100 transition-opacity">
                  Deliveries
                </button>
                <button v-if="msg.direction === 'rx'" @click="reply(msg)"
                  class="text-[10px] text-gray-600 hover:text-teal-400 opacity-0 group-hover:opacity-100 transition-opacity">
                  Reply
                </button>
              </div>
            </div>
          </div>

          <!-- System message: compact inline style -->
          <div v-else class="flex items-center gap-2 px-3 py-1 mx-1 group">
            <span class="w-1.5 h-1.5 rounded-full flex-shrink-0"
              :class="msg.portnum === 3 ? 'bg-cyan-500/60' : msg.portnum === 67 ? 'bg-amber-500/60' : 'bg-gray-600'"></span>
            <span class="text-[11px] text-gray-500 font-medium">{{ shortId(msg.from_node) }}</span>
            <span class="text-[11px] text-gray-600">{{ portnumLabel(msg.portnum, msg.portnum_name) }}</span>
            <span v-if="msg.decoded_text" class="text-[11px] text-gray-500 truncate max-w-[200px]">{{ msg.decoded_text }}</span>
            <span class="px-1 py-px rounded text-[9px] border" :class="transportBadge(msg.transport).cls">
              {{ transportBadge(msg.transport).label }}
            </span>
            <span class="text-[10px] text-gray-700 ml-auto flex-shrink-0">{{ relativeTime(msg) }}</span>
          </div>
        </template>
      </template>

      <!-- Load more -->
      <div v-if="filteredMessages.length > 0" class="text-center py-3">
        <button @click="store.fetchMessages({ offset: store.messages.length, limit: 50 })"
          class="text-[11px] text-gray-600 hover:text-gray-400 transition-colors">
          Load older messages
        </button>
      </div>
    </div>

    </template>

    <!-- Per-message delivery chain modal -->
    <Teleport to="body">
      <div v-if="deliveryModal" class="fixed inset-0 z-[100] flex items-center justify-center bg-black/70 backdrop-blur-sm" @click.self="deliveryModal = false">
        <div class="bg-gray-900 border border-gray-700 rounded-lg w-full max-w-lg max-h-[70vh] overflow-y-auto m-4">
          <div class="sticky top-0 bg-gray-900 border-b border-gray-700 px-4 py-3 flex items-center justify-between">
            <h3 class="font-semibold text-sm text-blue-400">Deliveries for {{ deliveryMsgRef }}</h3>
            <button @click="deliveryModal = false" class="text-gray-500 hover:text-gray-300 text-lg">&times;</button>
          </div>
          <div class="p-4">
            <div v-if="!deliveryItems.length" class="text-sm text-gray-500 text-center py-6">No deliveries found for this message.</div>
            <div v-else class="space-y-2">
              <div v-for="del in deliveryItems" :key="del.id" class="bg-gray-800 rounded-lg p-3 border border-gray-700">
                <div class="flex items-center gap-2 mb-1 text-[11px]">
                  <span class="font-mono px-1.5 py-px rounded bg-gray-700 text-gray-400">{{ del.channel }}</span>
                  <span class="font-mono px-1.5 py-px rounded"
                    :class="del.status === 'sent' ? 'bg-emerald-400/10 text-emerald-400' : del.status === 'failed' || del.status === 'dead' ? 'bg-red-400/10 text-red-400' : 'bg-gray-600 text-gray-400'">
                    {{ del.status }}
                  </span>
                  <span v-if="del.rule_id" class="text-gray-600 font-mono">rule:{{ del.rule_id }}</span>
                  <span class="flex-1" />
                  <span class="text-gray-600 font-mono text-[10px]">{{ formatRelativeTime(del.created_at) }}</span>
                </div>
                <div v-if="del.last_error" class="text-[10px] text-red-400/70 mt-1">{{ del.last_error }}</div>
                <div class="flex items-center gap-3 text-[10px] text-gray-600 mt-1">
                  <span>Retries: {{ del.retries }}/{{ del.max_retries || '~' }}</span>
                  <button v-if="del.status === 'failed' || del.status === 'dead'"
                    @click="store.retryDelivery(del.id)" class="text-teal-400 hover:text-teal-300">Retry</button>
                  <button v-if="del.status === 'queued' || del.status === 'retry'"
                    @click="store.cancelDelivery(del.id)" class="text-red-400 hover:text-red-300">Cancel</button>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>
