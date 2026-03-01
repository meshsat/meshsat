<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()

const messageText = ref('')
const messageTo = ref('')
const messageChannel = ref('')
const sending = ref(false)
const sent = ref(false)
const loadingMore = ref(false)
const offset = ref(0)
const limit = 50
const hasMore = ref(true)
let sentTimeout = null

function formatTime(val) {
  if (!val) return '—'
  const ts = typeof val === 'number' && val < 1e12 ? val * 1000 : val
  const d = new Date(ts)
  if (isNaN(d.getTime())) return String(val)
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

async function load() {
  await Promise.all([
    store.fetchMessages({ limit, offset: 0 }),
    store.fetchMessageStats()
  ])
  offset.value = store.messages.length
  hasMore.value = store.messages.length >= limit
}

async function loadMore() {
  if (loadingMore.value || !hasMore.value) return
  loadingMore.value = true
  try {
    const data = await store.fetchMessages({ limit, offset: offset.value })
    offset.value += data.length
    hasMore.value = data.length >= limit
  } finally {
    loadingMore.value = false
  }
}

async function handleSend() {
  if (!messageText.value.trim()) return
  sending.value = true
  sent.value = false
  try {
    const payload = { text: messageText.value.trim() }
    if (messageTo.value.trim()) payload.to = Number(messageTo.value) || 0
    if (messageChannel.value !== '') payload.channel = Number(messageChannel.value)
    await store.sendMessage(payload)
    messageText.value = ''
    sent.value = true
    if (sentTimeout) clearTimeout(sentTimeout)
    sentTimeout = setTimeout(() => { sent.value = false }, 3000)
  } catch { /* store sets error */ } finally {
    sending.value = false
  }
}

onMounted(() => {
  load()
  store.connectSSE((event) => {
    const type = event?.type ?? ''
    if (type === 'message' || type === 'text') {
      store.fetchMessages({ limit, offset: 0 })
    }
  })
})

onUnmounted(() => {
  store.closeSSE()
  if (sentTimeout) clearTimeout(sentTimeout)
})
</script>

<template>
  <div class="max-w-4xl mx-auto space-y-6">
    <h1 class="text-2xl font-bold">Messages</h1>

    <!-- Stats -->
    <div v-if="store.messageStats" class="grid grid-cols-2 sm:grid-cols-4 gap-4">
      <div class="bg-gray-900 rounded-xl p-4 border border-gray-800">
        <p class="text-xs text-gray-500">Total</p>
        <p class="text-2xl font-bold tabular-nums">{{ store.messageStats.total ?? 0 }}</p>
      </div>
      <div v-if="store.messageStats.by_transport" class="bg-gray-900 rounded-xl p-4 border border-gray-800">
        <p class="text-xs text-gray-500">By Transport</p>
        <div class="flex flex-wrap gap-1 mt-2">
          <span v-for="(c, t) in store.messageStats.by_transport" :key="t"
                class="text-xs px-2 py-0.5 rounded bg-gray-800 text-gray-300">
            {{ t }}: {{ c }}
          </span>
        </div>
      </div>
    </div>

    <!-- Send form -->
    <div class="bg-gray-900 rounded-xl p-5 border border-gray-800">
      <h2 class="text-sm font-semibold text-gray-300 mb-3">Send Message</h2>
      <div class="flex flex-col sm:flex-row gap-3">
        <input
          v-model="messageText"
          type="text"
          placeholder="Type a message..."
          maxlength="237"
          @keyup.enter="handleSend"
          class="flex-1 px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-teal-500"
        />
        <button
          @click="handleSend"
          :disabled="!messageText.trim() || sending"
          class="px-4 py-2 text-sm font-medium rounded-lg bg-teal-600 text-white hover:bg-teal-500 disabled:opacity-50 transition-colors"
        >
          {{ sending ? 'Sending...' : 'Send' }}
        </button>
      </div>
      <div class="flex gap-3 mt-2">
        <input
          v-model="messageTo"
          type="text"
          placeholder="To (empty = broadcast)"
          class="flex-1 px-3 py-1.5 text-xs rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-teal-500 font-mono"
        />
        <input
          v-model="messageChannel"
          type="number"
          min="0" max="7"
          placeholder="Ch"
          class="w-20 px-3 py-1.5 text-xs rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-teal-500"
        />
      </div>
      <p v-if="sent" class="text-xs text-teal-400 mt-2">Message sent</p>
    </div>

    <!-- Message list -->
    <div class="bg-gray-900 rounded-xl border border-gray-800 overflow-hidden">
      <div v-if="!store.messages.length" class="p-8 text-center text-gray-500">
        No messages yet
      </div>
      <div v-else class="divide-y divide-gray-800 max-h-[500px] overflow-y-auto">
        <div v-for="(msg, idx) in store.messages" :key="msg.id ?? idx"
             class="px-5 py-3 hover:bg-gray-800/50 transition-colors">
          <div class="flex items-center justify-between mb-1">
            <div class="flex items-center gap-2 text-xs text-gray-500">
              <span class="font-medium">{{ msg.from ?? 'Unknown' }}</span>
              <span v-if="msg.to">→ {{ msg.to === 0 ? 'Broadcast' : msg.to }}</span>
              <span v-if="msg.channel != null" class="px-1.5 py-0.5 rounded bg-gray-800">Ch {{ msg.channel }}</span>
              <span v-if="msg.transport" class="px-1.5 py-0.5 rounded bg-teal-900/30 text-teal-400">{{ msg.transport }}</span>
            </div>
            <span class="text-xs text-gray-500">{{ formatTime(msg.timestamp ?? msg.time ?? msg.rx_time) }}</span>
          </div>
          <p class="text-sm text-gray-200">{{ msg.text ?? msg.decoded_text ?? msg.message ?? '—' }}</p>
        </div>
      </div>
      <div v-if="hasMore && store.messages.length" class="px-5 py-3 border-t border-gray-800 text-center">
        <button
          @click="loadMore"
          :disabled="loadingMore"
          class="text-sm text-teal-400 hover:text-teal-300 disabled:opacity-50"
        >
          {{ loadingMore ? 'Loading...' : 'Load More' }}
        </button>
      </div>
    </div>
  </div>
</template>
