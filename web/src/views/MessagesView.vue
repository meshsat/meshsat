<script setup>
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import DeliveryStatus from '@/components/DeliveryStatus.vue'
import PresetGrid from '@/components/PresetGrid.vue'
import SOSButton from '@/components/SOSButton.vue'

const store = useMeshsatStore()
const sendText = ref('')
const sendTo = ref('')
const sendChannel = ref(0)
const replyTo = ref(null)
const showPresets = ref(false)

const byteCount = computed(() => new TextEncoder().encode(sendText.value).length)

function transportBadge(t) {
  if (t === 'radio') return { label: 'Mesh', color: 'bg-emerald-500/20 text-emerald-400' }
  if (t === 'iridium') return { label: 'Sat', color: 'bg-blue-500/20 text-blue-400' }
  if (t === 'mqtt') return { label: 'MQTT', color: 'bg-purple-500/20 text-purple-400' }
  return { label: t, color: 'bg-gray-500/20 text-gray-400' }
}

function timeLabel(msg) {
  const d = new Date(msg.created_at)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

async function send() {
  if (!sendText.value.trim()) return
  const payload = { text: sendText.value.trim() }
  if (sendTo.value) payload.to = sendTo.value
  if (sendChannel.value) payload.channel = sendChannel.value
  await store.sendMessage(payload)
  sendText.value = ''
  replyTo.value = null
  store.fetchMessages()
}

function reply(msg) {
  replyTo.value = msg
  sendTo.value = msg.from_node
  sendChannel.value = msg.channel
}

function cancelReply() {
  replyTo.value = null
  sendTo.value = ''
  sendChannel.value = 0
}

onMounted(() => {
  store.fetchMessages()
  store.fetchPresets()
  store.fetchSOSStatus()
  store.connectSSE((event) => {
    if (event.type === 'message') store.fetchMessages()
  })
})

onUnmounted(() => { store.closeSSE() })
</script>

<template>
  <div class="max-w-2xl mx-auto">
    <h2 class="text-lg font-semibold text-gray-200 mb-4">Messages</h2>

    <!-- SOS -->
    <div class="mb-4">
      <SOSButton />
    </div>

    <!-- Presets toggle -->
    <div class="mb-3">
      <button @click="showPresets = !showPresets" class="text-xs text-teal-400 hover:text-teal-300">
        {{ showPresets ? 'Hide presets' : 'Quick send presets' }}
      </button>
      <div v-if="showPresets" class="mt-2">
        <PresetGrid />
      </div>
    </div>

    <!-- Compose -->
    <div class="bg-gray-800 rounded-lg p-3 mb-4 border border-gray-700">
      <div v-if="replyTo" class="flex items-center gap-2 mb-2 px-2 py-1 bg-blue-900/30 rounded text-xs text-blue-400">
        <span>Replying to {{ replyTo.from_node }}</span>
        <button @click="cancelReply" class="text-blue-500 hover:text-blue-300 ml-auto">x</button>
      </div>
      <div class="flex gap-2">
        <input v-model="sendText" @keyup.enter="send" placeholder="Type a message..."
          class="flex-1 px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 placeholder-gray-500">
        <button @click="send" :disabled="!sendText.trim()"
          class="px-4 py-2 rounded bg-teal-600 text-white text-sm font-medium hover:bg-teal-500 disabled:opacity-50 disabled:cursor-not-allowed">
          Send
        </button>
      </div>
      <div class="flex items-center justify-between mt-2 px-1">
        <div class="flex gap-3">
          <input v-model="sendTo" placeholder="To (optional)" class="w-28 px-2 py-1 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300 placeholder-gray-600">
          <select v-model.number="sendChannel" class="px-2 py-1 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300">
            <option :value="0">Ch 0</option>
            <option :value="1">Ch 1</option>
            <option :value="2">Ch 2</option>
            <option :value="3">Ch 3</option>
          </select>
        </div>
        <span class="text-xs" :class="byteCount > 320 ? 'text-red-400' : 'text-gray-500'">{{ byteCount }}/340 bytes</span>
      </div>
    </div>

    <!-- Message list (chat bubbles) -->
    <div class="space-y-2">
      <div v-if="store.messages.length === 0" class="text-center text-gray-500 py-8 text-sm">
        No messages yet
      </div>
      <div v-for="msg in store.messages" :key="msg.id"
        class="flex" :class="msg.direction === 'tx' ? 'justify-end' : 'justify-start'">
        <div class="max-w-[80%] rounded-lg px-3 py-2"
          :class="msg.direction === 'tx' ? 'bg-teal-900/40 border border-teal-800' : 'bg-gray-800 border border-gray-700'">
          <div class="flex items-center gap-2 mb-1">
            <span class="text-xs font-medium" :class="msg.direction === 'tx' ? 'text-teal-400' : 'text-gray-400'">
              {{ msg.from_node }}
            </span>
            <span class="px-1.5 py-0.5 rounded text-[10px] font-medium" :class="transportBadge(msg.transport).color">
              {{ transportBadge(msg.transport).label }}
            </span>
            <DeliveryStatus v-if="msg.delivery_status && msg.delivery_status !== 'received'" :status="msg.delivery_status" />
          </div>
          <p class="text-sm text-gray-200 break-words">{{ msg.decoded_text }}</p>
          <div class="flex items-center justify-between mt-1 gap-3">
            <span class="text-[10px] text-gray-500">{{ timeLabel(msg) }} | Ch {{ msg.channel }}</span>
            <button v-if="msg.direction === 'rx'" @click="reply(msg)" class="text-[10px] text-gray-500 hover:text-teal-400">Reply</button>
          </div>
        </div>
      </div>
    </div>

    <div class="text-center mt-4">
      <button @click="store.fetchMessages({ offset: store.messages.length })"
        class="text-xs text-gray-500 hover:text-gray-300">Load more</button>
    </div>
  </div>
</template>
