<script setup>
import { ref, onMounted, computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import RuleCard from '@/components/RuleCard.vue'
import RuleEditor from '@/components/RuleEditor.vue'

const store = useMeshsatStore()
const activeTab = ref('outbound')
const editorOpen = ref(false)
const editingRule = ref(null)
const editorDirection = ref(null)

const subTabs = [
  { id: 'outbound', label: 'Outbound' },
  { id: 'inbound', label: 'Inbound' },
  { id: 'queue', label: 'Queue' },
  { id: 'rules', label: 'Rules' }
]

const mqttGw = computed(() => (store.gateways || []).find(g => g.type === 'mqtt'))
const iridiumGw = computed(() => (store.gateways || []).find(g => g.type === 'iridium'))

// Split rules by direction: inbound rules have dest_type === 'mesh'
const outboundRules = computed(() =>
  (store.rules || []).filter(r => r.dest_type !== 'mesh')
)
const inboundRules = computed(() =>
  (store.rules || []).filter(r => r.dest_type === 'mesh')
)

// Queue items with decoded payload
const queueItems = computed(() =>
  (store.dlq || []).map(item => ({
    ...item,
    preview: decodePayload(item.payload),
    statusColor: dlqStatusColor(item.status)
  }))
)

// Compose message
const composeOpen = ref(false)
const composeMsg = ref('')
const composePriority = ref(1)

function decodePayload(payload) {
  if (!payload) return '(empty)'
  if (typeof payload === 'string') {
    // Try base64 decode
    try {
      return atob(payload)
    } catch {
      return payload.slice(0, 60)
    }
  }
  if (payload instanceof Array) {
    try {
      return new TextDecoder().decode(new Uint8Array(payload)).slice(0, 60)
    } catch {
      return `(${payload.length} bytes)`
    }
  }
  return String(payload).slice(0, 60)
}

function dlqStatusColor(status) {
  if (status === 'sent' || status === 'delivered') return 'text-emerald-400 bg-emerald-400/10'
  if (status === 'received') return 'text-blue-400 bg-blue-400/10'
  if (status === 'pending') return 'text-amber-400 bg-amber-400/10'
  if (status === 'failed' || status === 'expired') return 'text-red-400 bg-red-400/10'
  if (status === 'cancelled') return 'text-gray-500 bg-gray-500/10'
  return 'text-gray-400 bg-gray-400/10'
}

function priorityLabel(p) {
  return p === 0 ? 'Critical' : p === 2 ? 'Low' : 'Normal'
}

function priorityColor(p) {
  return p === 0 ? 'text-red-400' : p === 2 ? 'text-gray-500' : 'text-amber-400'
}

function formatTime(ts) {
  if (!ts) return ''
  const d = new Date(ts)
  if (isNaN(d.getTime())) return String(ts)
  return d.toISOString().slice(11, 19) + 'Z'
}

function formatRelative(ts) {
  if (!ts) return 'N/A'
  const d = new Date(ts)
  const diff = Math.floor((Date.now() - d.getTime()) / 1000)
  if (diff < 0) return 'soon'
  if (diff < 60) return `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  return `${Math.floor(diff / 3600)}h ago`
}

function nextRetryCountdown(ts) {
  if (!ts) return ''
  const diff = Math.floor((new Date(ts).getTime() - Date.now()) / 1000)
  if (diff <= 0) return 'now'
  if (diff < 60) return `${diff}s`
  return `${Math.floor(diff / 60)}m ${diff % 60}s`
}

function gwStatusColor(gw) {
  if (!gw) return 'bg-gray-600'
  return gw.connected ? 'bg-emerald-400' : gw.enabled ? 'bg-amber-400' : 'bg-gray-600'
}

function gwStatusLabel(gw) {
  if (!gw) return 'Not configured'
  return gw.connected ? 'Connected' : gw.enabled ? 'Disconnected' : 'Disabled'
}

function openCreate(dir = null) {
  editingRule.value = null
  editorDirection.value = dir
  editorOpen.value = true
}

function openEdit(rule) {
  editingRule.value = { ...rule }
  editorDirection.value = null
  editorOpen.value = true
}

async function saveRule(data) {
  if (editingRule.value?.id) {
    await store.updateRule(editingRule.value.id, data)
  } else {
    await store.createRule(data)
  }
  editorOpen.value = false
}

async function toggleRule(rule) {
  rule.enabled ? await store.disableRule(rule.id) : await store.enableRule(rule.id)
}

async function removeRule(rule) {
  if (confirm(`Delete rule "${rule.name}"?`)) {
    await store.deleteRule(rule.id)
  }
}

async function cancelItem(id) {
  await store.cancelQueueItem(id)
}

async function reprioritize(id, newPriority) {
  await store.setQueuePriority(id, newPriority)
}

async function sendComposed() {
  if (!composeMsg.value.trim()) return
  await store.enqueueIridiumMessage(composeMsg.value.trim(), composePriority.value)
  composeMsg.value = ''
  composeOpen.value = false
  store.fetchDLQ()
}

onMounted(() => {
  store.fetchRules()
  store.fetchGateways()
  store.fetchDLQ()
  store.fetchStatus()
})
</script>

<template>
  <div class="max-w-4xl mx-auto">
    <div class="flex items-center justify-between mb-4">
      <h2 class="text-lg font-semibold text-gray-200">Bridge</h2>
    </div>

    <!-- Status panes -->
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 mb-4">
      <div class="bg-tactical-surface rounded-lg p-3 border border-tactical-border">
        <div class="text-[10px] text-gray-500 mb-1">MESH RADIO</div>
        <div class="flex items-center gap-2">
          <span class="w-2 h-2 rounded-full" :class="store.status?.connected ? 'bg-emerald-400' : 'bg-red-400'" />
          <span class="text-xs text-gray-300">{{ store.status?.connected ? 'Connected' : 'Disconnected' }}</span>
        </div>
      </div>
      <div class="bg-tactical-surface rounded-lg p-3 border border-tactical-border">
        <div class="text-[10px] text-gray-500 mb-1">MQTT</div>
        <div class="flex items-center gap-2">
          <span class="w-2 h-2 rounded-full" :class="gwStatusColor(mqttGw)" />
          <span class="text-xs text-gray-300">{{ gwStatusLabel(mqttGw) }}</span>
        </div>
      </div>
      <div class="bg-tactical-surface rounded-lg p-3 border border-tactical-border">
        <div class="text-[10px] text-gray-500 mb-1">IRIDIUM</div>
        <div class="flex items-center gap-2">
          <span class="w-2 h-2 rounded-full" :class="gwStatusColor(iridiumGw)" />
          <span class="text-xs text-gray-300">{{ gwStatusLabel(iridiumGw) }}</span>
        </div>
      </div>
    </div>

    <!-- Sub-tab bar -->
    <div class="flex gap-1 mb-4 border-b border-tactical-border pb-2">
      <button v-for="tab in subTabs" :key="tab.id" @click="activeTab = tab.id"
        class="px-3 py-1.5 rounded text-xs font-medium transition-colors"
        :class="activeTab === tab.id ? 'bg-tactical-iridium/10 text-tactical-iridium' : 'text-gray-500 hover:text-gray-300'">
        {{ tab.label }}
        <span v-if="tab.id === 'queue' && queueItems.length > 0"
          class="ml-1 px-1 py-px rounded text-[9px] bg-amber-400/10 text-amber-400">{{ queueItems.length }}</span>
      </button>
    </div>

    <!-- ═══ Outbound Tab ═══ -->
    <div v-if="activeTab === 'outbound'">
      <div class="flex items-center justify-between mb-3">
        <p class="text-xs text-gray-500">Mesh messages forwarded to Iridium / MQTT</p>
        <button @click="openCreate('outbound')" class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs font-medium hover:bg-teal-500">
          + New Outbound Rule
        </button>
      </div>
      <div v-if="outboundRules.length === 0" class="text-center text-gray-500 py-6 text-sm bg-gray-800/50 rounded-lg border border-gray-700">
        No outbound rules. Mesh messages stay local.
      </div>
      <div class="space-y-3">
        <RuleCard v-for="rule in outboundRules" :key="rule.id" :rule="rule"
          @toggle="toggleRule(rule)" @edit="openEdit(rule)" @delete="removeRule(rule)" />
      </div>
    </div>

    <!-- ═══ Inbound Tab ═══ -->
    <div v-if="activeTab === 'inbound'">
      <div class="flex items-center justify-between mb-3">
        <p class="text-xs text-gray-500">External messages routed back to the mesh network</p>
        <button @click="openCreate('inbound')" class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs font-medium hover:bg-teal-500">
          + New Inbound Rule
        </button>
      </div>
      <div v-if="inboundRules.length === 0" class="text-center text-gray-500 py-6 text-sm bg-gray-800/50 rounded-lg border border-gray-700">
        No inbound rules configured. External messages are received but not routed to mesh.
      </div>
      <div class="space-y-3">
        <RuleCard v-for="rule in inboundRules" :key="rule.id" :rule="rule"
          @toggle="toggleRule(rule)" @edit="openEdit(rule)" @delete="removeRule(rule)" />
      </div>
    </div>

    <!-- ═══ Queue Tab ═══ -->
    <div v-if="activeTab === 'queue'">
      <div class="flex items-center justify-between mb-3">
        <p class="text-xs text-gray-500">Satellite relay log — outbound sends and inbound receives</p>
        <button @click="composeOpen = !composeOpen"
          class="px-3 py-1.5 rounded bg-tactical-iridium/20 text-tactical-iridium text-xs font-medium hover:bg-tactical-iridium/30 border border-tactical-iridium/20">
          {{ composeOpen ? 'Cancel' : 'Compose' }}
        </button>
      </div>

      <!-- Compose form -->
      <div v-if="composeOpen" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-4 space-y-3">
        <div>
          <label class="block text-xs text-gray-400 mb-1">Message (max 340 bytes)</label>
          <textarea v-model="composeMsg" rows="2" maxlength="340"
            class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono"
            placeholder="Type message to send via Iridium SBD..." />
        </div>
        <div class="flex items-center gap-3">
          <select v-model.number="composePriority" class="px-3 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <option :value="0">Critical</option>
            <option :value="1">Normal</option>
            <option :value="2">Low</option>
          </select>
          <button @click="sendComposed" :disabled="!composeMsg.trim()"
            class="px-4 py-1.5 rounded bg-teal-600 text-white text-sm hover:bg-teal-500 disabled:opacity-50">
            Enqueue
          </button>
          <span class="text-[10px] text-gray-600">{{ composeMsg.length }}/340</span>
        </div>
      </div>

      <!-- Queue items -->
      <div v-if="queueItems.length === 0" class="text-center text-gray-500 py-6 text-sm bg-gray-800/50 rounded-lg border border-gray-700">
        Queue is empty.
      </div>
      <div class="space-y-2">
        <div v-for="item in queueItems" :key="item.id"
          class="bg-tactical-surface rounded-lg p-3 border border-tactical-border"
          :class="(item.status === 'sent' || item.status === 'received') ? 'opacity-60' : ''">
          <div class="flex items-center gap-2 mb-2">
            <span class="text-[10px] font-mono px-1.5 py-px rounded"
              :class="item.direction === 'inbound' ? 'text-blue-400 bg-blue-400/10' : 'text-tactical-iridium bg-tactical-iridium/10'">
              {{ item.direction === 'inbound' ? 'SBD \u2192 Mesh' : 'Mesh \u2192 SBD' }}
            </span>
            <span class="text-[10px] font-mono px-1.5 py-px rounded" :class="item.statusColor">
              {{ item.status === 'sent' ? 'delivered' : item.status === 'received' ? 'received' : item.status || 'queued' }}
            </span>
            <span v-if="item.status === 'pending'" class="text-[10px] font-medium" :class="priorityColor(item.priority)">
              {{ priorityLabel(item.priority) }}
            </span>
            <span class="text-[9px] text-gray-600 font-mono">ID:{{ item.id }}</span>
            <span class="flex-1" />
            <span class="text-[9px] text-gray-600">{{ formatRelative(item.created_at) }}</span>
          </div>

          <!-- Message preview -->
          <div class="text-[11px] font-mono bg-gray-900/50 rounded px-2 py-1.5 mb-2 truncate"
            :class="item.status === 'sent' ? 'text-gray-500' : 'text-gray-400'">
            {{ item.preview || '(no text payload)' }}
          </div>

          <!-- Actions (only for pending items) -->
          <div v-if="item.status === 'pending'" class="flex items-center gap-2 text-[10px]">
            <span class="text-gray-600">Retries: {{ item.retries }}/{{ item.max_retries }}</span>
            <span class="text-gray-600">
              Next: {{ nextRetryCountdown(item.next_retry) }}
            </span>
            <span class="flex-1" />
            <button @click="reprioritize(item.id, 0)"
              class="px-1.5 py-0.5 rounded bg-red-400/10 text-red-400 hover:bg-red-400/20">Urgent</button>
            <button @click="reprioritize(item.id, 2)"
              class="px-1.5 py-0.5 rounded bg-gray-700 text-gray-400 hover:text-gray-200">Low</button>
            <button @click="cancelItem(item.id)"
              class="px-1.5 py-0.5 rounded bg-gray-700 text-gray-400 hover:text-red-400">Cancel</button>
          </div>
          <!-- Sent/expired status line -->
          <div v-else-if="item.status === 'expired'" class="text-[10px] text-red-400">
            Failed after {{ item.retries }}/{{ item.max_retries }} retries: {{ item.last_error }}
          </div>
        </div>
      </div>
    </div>

    <!-- ═══ Rules Tab ═══ -->
    <div v-if="activeTab === 'rules'">
      <div class="flex items-center justify-between mb-3">
        <p class="text-xs text-gray-500">All forwarding rules — full management</p>
        <button @click="openCreate" class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs font-medium hover:bg-teal-500">
          + New Rule
        </button>
      </div>
      <div v-if="store.rules.length === 0" class="text-center text-gray-500 py-6 text-sm bg-gray-800/50 rounded-lg border border-gray-700">
        No forwarding rules configured.
      </div>
      <div class="space-y-3">
        <RuleCard v-for="rule in store.rules" :key="rule.id" :rule="rule"
          @toggle="toggleRule(rule)" @edit="openEdit(rule)" @delete="removeRule(rule)" />
      </div>
    </div>

    <!-- Rule editor modal -->
    <RuleEditor :open="editorOpen" :rule="editingRule" :initial-direction="editorDirection" @save="saveRule" @close="editorOpen = false" />
  </div>
</template>
