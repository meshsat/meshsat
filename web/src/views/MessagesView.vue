<script setup>
import { ref, onMounted, onUnmounted, computed, nextTick, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import DeliveryStatus from '@/components/DeliveryStatus.vue'

const store = useMeshsatStore()

// Compose state
const sendText = ref('')
const sendTo = ref('')
const sendChannel = ref(0)
const replyTo = ref(null)
const showPresets = ref(false)
const showCompose = ref(false)

// Filter
const filter = ref('text') // 'all', 'text', 'system'
const selectedNode = ref(null) // null = all nodes, string = specific node mailbox

// SOS
const sosConfirming = ref(false)
const sosSending = ref(false)

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

const activeRuleCount = computed(() => {
  return (store.rules || []).filter(r => r.enabled).length
})

const satBars = computed(() => store.iridiumSignal?.bars ?? -1)

function transportBadge(t) {
  if (t === 'radio') return { label: 'Mesh', cls: 'bg-emerald-500/20 text-emerald-400 border-emerald-500/30' }
  if (t === 'iridium') return { label: 'Sat', cls: 'bg-blue-500/20 text-blue-400 border-blue-500/30' }
  if (t === 'mqtt') return { label: 'MQTT', cls: 'bg-purple-500/20 text-purple-400 border-purple-500/30' }
  return { label: t || '?', cls: 'bg-gray-500/20 text-gray-400 border-gray-500/30' }
}

function portnumLabel(pn, name) {
  if (pn === 1) return null // text messages don't need a label
  if (pn === 3 || name === 'POSITION_APP') return 'Position'
  if (pn === 4 || name === 'NODEINFO_APP') return 'Node Info'
  if (pn === 67 || name === 'TELEMETRY_APP') return 'Telemetry'
  if (pn === 8 || name === 'WAYPOINT_APP') return 'Waypoint'
  if (pn === 70 || name === 'TRACEROUTE_APP') return 'Traceroute'
  return name || `Port ${pn}`
}

function shortNode(id) {
  if (!id) return '?'
  if (id.startsWith('!') && id.length > 6) return id.slice(0, 3) + '..' + id.slice(-4)
  return id
}

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

async function sosActivate() {
  sosSending.value = true
  try { await store.activateSOS() } finally { sosSending.value = false; sosConfirming.value = false }
}

let pollTimer = null
onMounted(() => {
  store.fetchMessages()
  store.fetchNodes()
  store.fetchPresets()
  store.fetchSOSStatus()
  store.fetchRules()
  store.fetchMessageStats()
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

    <!-- Status cards row -->
    <div class="grid grid-cols-2 sm:grid-cols-4 gap-2 mb-3 flex-shrink-0">
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
        <div class="text-[10px] uppercase tracking-wider text-gray-500 mb-0.5">Satellite</div>
        <div class="flex items-center gap-2">
          <div class="flex items-end gap-0.5 h-4">
            <span v-for="i in 5" :key="i" class="w-1 rounded-sm transition-colors"
              :class="satBars >= i ? 'bg-emerald-400' : 'bg-gray-700'"
              :style="{ height: `${4 + i * 3}px` }"></span>
          </div>
          <span class="text-xs text-gray-400">{{ satBars >= 0 ? satBars + '/5' : 'N/A' }}</span>
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
        <!-- SOS - small but distinct -->
        <button v-if="!store.sosStatus?.active && !sosConfirming"
          @click="sosConfirming = true"
          class="px-2.5 py-1 rounded text-xs font-bold text-red-400 hover:bg-red-900/30 border border-red-800/50 hover:border-red-700 transition-colors"
          title="Emergency SOS">
          SOS
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
      <div class="grid grid-cols-2 sm:grid-cols-4 gap-1.5 bg-gray-800/60 rounded-lg p-2 border border-gray-700/50">
        <button v-for="preset in store.presets" :key="preset.id"
          @click="sendPreset(preset)"
          class="px-3 py-2 rounded bg-gray-700/50 hover:bg-gray-600/50 border border-gray-600/30 text-left transition-colors">
          <div class="text-xs font-medium text-gray-200 truncate">{{ preset.name }}</div>
          <div class="text-[10px] text-gray-500 truncate mt-0.5">{{ preset.text }}</div>
        </button>
        <div v-if="!store.presets?.length" class="col-span-full text-center text-xs text-gray-500 py-2">
          No presets configured
        </div>
      </div>
    </div>

    <!-- SOS confirmation -->
    <div v-if="sosConfirming && !store.sosStatus?.active" class="mb-2 flex-shrink-0 bg-red-900/30 border border-red-800/60 rounded-lg px-4 py-3">
      <div class="flex items-center justify-between">
        <div>
          <div class="text-sm font-medium text-red-300">Send emergency alert?</div>
          <div class="text-[11px] text-red-400/70 mt-0.5">GPS position will be broadcast via all transports</div>
        </div>
        <div class="flex gap-2 ml-4">
          <button @click="sosConfirming = false" class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs hover:bg-gray-600">Cancel</button>
          <button @click="sosActivate" :disabled="sosSending"
            class="px-3 py-1.5 rounded bg-red-600 text-white text-xs font-bold hover:bg-red-500 disabled:opacity-50">
            {{ sosSending ? 'Sending...' : 'Confirm SOS' }}
          </button>
        </div>
      </div>
    </div>

    <!-- Active SOS banner -->
    <div v-if="store.sosStatus?.active" class="mb-2 flex-shrink-0 bg-red-900/40 border border-red-600/60 rounded-lg px-4 py-2 animate-pulse">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <span class="w-2 h-2 rounded-full bg-red-500"></span>
          <span class="text-sm font-bold text-red-400">SOS ACTIVE</span>
          <span class="text-xs text-red-300/70">{{ store.sosStatus.sends || 0 }}/3 sent</span>
        </div>
        <button @click="store.cancelSOS()" class="px-3 py-1 rounded bg-gray-700 text-gray-300 text-xs hover:bg-gray-600">Cancel</button>
      </div>
    </div>

    <!-- Compose panel (expandable) -->
    <div v-if="showCompose" class="mb-2 flex-shrink-0 bg-gray-800/60 rounded-lg border border-gray-700/50 p-3">
      <div v-if="replyTo" class="flex items-center gap-2 mb-2 px-2 py-1 bg-blue-900/20 rounded text-xs text-blue-400 border border-blue-800/30">
        <svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="2">
          <path stroke-linecap="round" stroke-linejoin="round" d="M3 10h10a8 8 0 018 8v2M3 10l6 6m-6-6l6-6" />
        </svg>
        <span>{{ shortNode(replyTo.from_node) }}</span>
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
                  {{ shortNode(msg.from_node) }}
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
            <span class="text-[11px] text-gray-500 font-medium">{{ shortNode(msg.from_node) }}</span>
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
  </div>
</template>
