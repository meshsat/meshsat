<script setup>
import { ref, onMounted, onUnmounted, computed, nextTick, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import DeliveryStatus from '@/components/DeliveryStatus.vue'
import { transportBadge, portnumLabel, shortId, formatRelativeTime, formatTimestamp } from '@/utils/format'

const store = useMeshsatStore()

// Tab: 'mesh', 'sbd', 'sms', or 'webhooks'
const activeTab = ref('mesh')

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
    const node = (store.nodes || []).find(n => n.user_id === id || String(n.num) === id || n.id === id)
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
            </div>
          </div>
        </div>
      </Teleport>
    </div>

    <!-- ═══ SMS Tab ═══ -->
    <div v-if="activeTab === 'sms'" class="flex-1 overflow-y-auto min-h-0">
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

      <div class="text-center text-[11px] text-gray-600 mt-8">
        Received SMS messages appear in the Mesh Messages tab when forwarded via bridge rules.
        <br />Manage contacts in the <span class="text-teal-400/70">Peers</span> tab.
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
  </div>
</template>
