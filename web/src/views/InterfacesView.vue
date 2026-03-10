<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const activeTab = ref('interfaces')
const showCreateIface = ref(false)
const showCreateRule = ref(false)
const showCreateGroup = ref(false)
const showCreateFailover = ref(false)
const editingRule = ref(null)
const editingGroup = ref(null)
const expandedIface = ref(null)
const generatingKey = ref(false)
const showTransforms = ref(null)

const tabs = [
  { id: 'interfaces', label: 'Interfaces' },
  { id: 'rules', label: 'Access Rules' },
  { id: 'devices', label: 'Devices' },
  { id: 'groups', label: 'Object Groups' },
  { id: 'failover', label: 'Failover' }
]

// Interface form
const ifaceForm = ref({ id: '', channel_type: 'mesh', label: '', enabled: true })
const channelTypes = ['mesh', 'iridium', 'astrocast', 'cellular', 'zigbee', 'webhook', 'mqtt']

function resetIfaceForm() {
  ifaceForm.value = { id: '', channel_type: 'mesh', label: '', enabled: true }
}

async function saveInterface() {
  if (!ifaceForm.value.id) return
  try {
    await store.createInterface(ifaceForm.value)
    showCreateIface.value = false
    resetIfaceForm()
  } catch { /* store error */ }
}

async function toggleInterface(iface) {
  try {
    await store.updateInterface(iface.id, { ...iface, enabled: !iface.enabled })
  } catch { /* store error */ }
}

async function removeInterface(id) {
  if (!confirm(`Delete interface ${id}?`)) return
  try { await store.deleteInterface(id) } catch { /* store error */ }
}

async function doBindDevice(ifaceId, deviceId) {
  try { await store.bindDevice(ifaceId, deviceId) } catch { /* store error */ }
}

async function doUnbindDevice(ifaceId) {
  try { await store.unbindDevice(ifaceId) } catch { /* store error */ }
}

// --- Transform Pipeline ---
const transformTypes = ['zstd', 'smaz2', 'base64', 'encrypt']

function getTransforms(iface, direction) {
  const json = direction === 'egress' ? iface.egress_transforms : iface.ingress_transforms
  if (!json || json === '[]') return []
  try { return JSON.parse(json) } catch { return [] }
}

function hasEncryption(iface) {
  return getTransforms(iface, 'egress').some(t => t.type === 'encrypt')
}

function getEncryptionKey(iface) {
  const enc = getTransforms(iface, 'egress').find(t => t.type === 'encrypt')
  return enc?.params?.key || ''
}

function hasTransform(iface, type) {
  return getTransforms(iface, 'egress').some(t => t.type === type)
}

const textOnlyChannels = ['cellular', 'mqtt', 'webhook']

function isTextOnly(iface) {
  return textOnlyChannels.includes(iface.channel_type)
}

async function toggleTransform(iface, transformType) {
  let egress = getTransforms(iface, 'egress')
  let ingress = getTransforms(iface, 'ingress')

  if (hasTransform(iface, transformType)) {
    egress = egress.filter(t => t.type !== transformType)
    ingress = ingress.filter(t => t.type !== transformType)
    // Remove auto-added base64 if removing encryption from text channel
    if (transformType === 'encrypt' && isTextOnly(iface)) {
      egress = egress.filter(t => t.type !== 'base64')
      ingress = ingress.filter(t => t.type !== 'base64')
    }
  } else {
    if (transformType === 'encrypt') {
      generatingKey.value = true
      try {
        const res = await store.generateEncryptionKey()
        egress.push({ type: 'encrypt', params: { key: res.key } })
        ingress.push({ type: 'encrypt', params: { key: res.key } })
        if (isTextOnly(iface) && !egress.some(t => t.type === 'base64')) {
          egress.push({ type: 'base64' })
          ingress.push({ type: 'base64' })
        }
      } finally {
        generatingKey.value = false
      }
    } else {
      egress.push({ type: transformType })
      ingress.push({ type: transformType })
    }
  }

  await store.updateInterface(iface.id, {
    ...iface,
    egress_transforms: JSON.stringify(egress),
    ingress_transforms: JSON.stringify(ingress)
  })
}

// --- Access Rule Form (structured) ---
const ruleForm = ref({
  interface_id: '', direction: 'ingress', priority: 10, name: '', enabled: true,
  action: 'forward', forward_to: '', qos_level: 0,
  node_group: '', sender_group: '', portnum_group: '',
  rate_per_min: 0, rate_window_sec: 60,
  schedule_type: 'none', schedule_value: ''
})

function openNewRule() {
  editingRule.value = null
  ruleForm.value = {
    interface_id: '', direction: 'ingress', priority: 10, name: '', enabled: true,
    action: 'forward', forward_to: '', qos_level: 0,
    node_group: '', sender_group: '', portnum_group: '',
    rate_per_min: 0, rate_window_sec: 60,
    schedule_type: 'none', schedule_value: ''
  }
  showCreateRule.value = true
}

function openEditRule(rule) {
  editingRule.value = rule.id
  const filters = typeof rule.filters === 'string' ? JSON.parse(rule.filters || '{}') : (rule.filters || {})
  ruleForm.value = {
    interface_id: rule.interface_id || '',
    direction: rule.direction || 'ingress',
    priority: rule.priority || 10,
    name: rule.name || '',
    enabled: rule.enabled !== false,
    action: rule.action || 'forward',
    forward_to: rule.forward_to || '',
    qos_level: rule.qos_level || 0,
    node_group: filters.node_group || '',
    sender_group: filters.sender_group || '',
    portnum_group: filters.portnum_group || '',
    rate_per_min: rule.rate_per_min || 0,
    rate_window_sec: rule.rate_window_sec || 60,
    schedule_type: rule.schedule_type || 'none',
    schedule_value: rule.schedule_value || ''
  }
  showCreateRule.value = true
}

async function saveRule() {
  if (!ruleForm.value.interface_id || !ruleForm.value.name) return
  const filters = {}
  if (ruleForm.value.node_group) filters.node_group = ruleForm.value.node_group
  if (ruleForm.value.sender_group) filters.sender_group = ruleForm.value.sender_group
  if (ruleForm.value.portnum_group) filters.portnum_group = ruleForm.value.portnum_group

  const payload = {
    interface_id: ruleForm.value.interface_id,
    direction: ruleForm.value.direction,
    priority: ruleForm.value.priority,
    name: ruleForm.value.name,
    enabled: ruleForm.value.enabled,
    action: ruleForm.value.action,
    forward_to: ruleForm.value.forward_to,
    qos_level: ruleForm.value.qos_level,
    filters: JSON.stringify(filters),
    rate_per_min: ruleForm.value.rate_per_min || 0,
    rate_window_sec: ruleForm.value.rate_window_sec || 60,
    schedule_type: ruleForm.value.schedule_type,
    schedule_value: ruleForm.value.schedule_value
  }

  try {
    if (editingRule.value) {
      await store.updateAccessRule(editingRule.value, payload)
    } else {
      await store.createAccessRule(payload)
    }
    showCreateRule.value = false
  } catch { /* store error */ }
}

async function removeRule(id) {
  if (!confirm('Delete this access rule?')) return
  try { await store.deleteAccessRule(id) } catch { /* store error */ }
}

// Forward-to options: interfaces + failover groups
const forwardTargets = computed(() => {
  const targets = []
  for (const i of (store.interfaces || [])) {
    targets.push({ value: i.id, label: `${i.id} (${i.channel_type})`, type: 'interface' })
  }
  for (const g of (store.failoverGroups || [])) {
    targets.push({ value: `failover:${g.id}`, label: `${g.id} (failover)`, type: 'failover' })
  }
  return targets
})

// --- Object Group Form ---
const groupTypes = ['node_group', 'sender_group', 'portnum_group']
const groupForm = ref({ id: '', type: 'node_group', label: '', members: '' })

function openNewGroup() {
  editingGroup.value = null
  groupForm.value = { id: '', type: 'node_group', label: '', members: '' }
  showCreateGroup.value = true
}

function openEditGroup(group) {
  editingGroup.value = group.id
  groupForm.value = {
    id: group.id,
    type: group.type || 'node_group',
    label: group.label || '',
    members: Array.isArray(group.members) ? group.members.join(', ') : (group.members || '')
  }
  showCreateGroup.value = true
}

async function saveGroup() {
  if (!groupForm.value.id) return
  const payload = {
    id: groupForm.value.id,
    type: groupForm.value.type,
    label: groupForm.value.label,
    members: groupForm.value.members
  }
  try {
    if (editingGroup.value) {
      await store.updateObjectGroup(editingGroup.value, payload)
    } else {
      await store.createObjectGroup(payload)
    }
    showCreateGroup.value = false
  } catch { /* store error */ }
}

async function removeGroup(id) {
  if (!confirm(`Delete object group ${id}?`)) return
  try { await store.deleteObjectGroup(id) } catch { /* store error */ }
}

// --- Failover Group Form ---
const failoverForm = ref({ id: '', label: '', mode: 'priority', members: [{ interface_id: '', priority: 1 }] })

function openNewFailover() {
  failoverForm.value = { id: '', label: '', mode: 'priority', members: [{ interface_id: '', priority: 1 }] }
  showCreateFailover.value = true
}

function addFailoverMember() {
  const maxP = failoverForm.value.members.reduce((m, x) => Math.max(m, x.priority), 0)
  failoverForm.value.members.push({ interface_id: '', priority: maxP + 1 })
}

function removeFailoverMember(idx) {
  failoverForm.value.members.splice(idx, 1)
}

async function saveFailover() {
  if (!failoverForm.value.id) return
  try {
    await store.createFailoverGroup(failoverForm.value)
    showCreateFailover.value = false
  } catch { /* store error */ }
}

async function removeFailoverGroup(id) {
  if (!confirm(`Delete failover group ${id}?`)) return
  try { await store.deleteFailoverGroup(id) } catch { /* store error */ }
}

// State/action badges
function stateColor(state) {
  switch (state) {
    case 'online': return 'bg-emerald-500/20 text-emerald-400'
    case 'binding': return 'bg-yellow-500/20 text-yellow-400'
    case 'offline': return 'bg-gray-600 text-gray-400'
    case 'error': return 'bg-red-500/20 text-red-400'
    case 'unbound': return 'bg-gray-700 text-gray-500'
    default: return 'bg-gray-700 text-gray-500'
  }
}

function actionColor(action) {
  switch (action) {
    case 'forward': return 'bg-teal-500/20 text-teal-400'
    case 'drop': return 'bg-red-500/20 text-red-400'
    case 'log': return 'bg-blue-500/20 text-blue-400'
    default: return 'bg-gray-700 text-gray-500'
  }
}

function transformBadge(type) {
  switch (type) {
    case 'encrypt': return 'bg-amber-500/20 text-amber-400'
    case 'zstd': return 'bg-purple-500/20 text-purple-400'
    case 'smaz2': return 'bg-indigo-500/20 text-indigo-400'
    case 'base64': return 'bg-gray-600 text-gray-400'
    default: return 'bg-gray-700 text-gray-500'
  }
}

// Unassigned devices
const unassignedDevices = computed(() =>
  (store.devices || []).filter(d => !d.bound_to)
)

// Object groups by type (for access rule dropdowns)
const nodeGroups = computed(() => (store.objectGroups || []).filter(g => g.type === 'node_group'))
const senderGroups = computed(() => (store.objectGroups || []).filter(g => g.type === 'sender_group'))
const portnumGroups = computed(() => (store.objectGroups || []).filter(g => g.type === 'portnum_group'))

// Parse filters from stored rules for display
function parseFilters(rule) {
  if (!rule.filters) return {}
  try {
    return typeof rule.filters === 'string' ? JSON.parse(rule.filters) : rule.filters
  } catch { return {} }
}

// Polling
let pollTimer = null

onMounted(() => {
  store.fetchInterfaces()
  store.fetchDevices()
  store.fetchAccessRules()
  store.fetchObjectGroups()
  store.fetchFailoverGroups()
  pollTimer = setInterval(() => {
    store.fetchInterfaces()
    store.fetchDevices()
    store.fetchAccessRules()
    store.fetchObjectGroups()
    store.fetchFailoverGroups()
  }, 5000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<template>
  <div class="max-w-4xl mx-auto">
    <h2 class="text-lg font-semibold text-gray-200 mb-4">Interfaces</h2>

    <!-- Tab bar -->
    <div class="flex gap-1 mb-6 overflow-x-auto pb-1">
      <button v-for="tab in tabs" :key="tab.id" @click="activeTab = tab.id"
        class="px-4 py-2 rounded-lg text-xs font-medium whitespace-nowrap transition-colors"
        :class="activeTab === tab.id ? 'bg-teal-600 text-white' : 'bg-gray-800 text-gray-400 hover:text-gray-200'">
        {{ tab.label }}
        <span v-if="tab.id === 'groups'" class="ml-1 text-[9px] opacity-60">{{ (store.objectGroups || []).length }}</span>
        <span v-if="tab.id === 'failover'" class="ml-1 text-[9px] opacity-60">{{ (store.failoverGroups || []).length }}</span>
      </button>
    </div>

    <!-- Error banner -->
    <div v-if="store.error" class="mb-4 p-3 rounded-lg bg-red-900/30 border border-red-700 text-sm text-red-300">
      {{ store.error }}
    </div>

    <!-- ═══ Interfaces Tab ═══ -->
    <div v-if="activeTab === 'interfaces'">
      <div class="flex items-center justify-between mb-4">
        <span class="text-sm text-gray-400">{{ (store.interfaces || []).length }} interfaces</span>
        <button @click="showCreateIface = !showCreateIface"
          class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs hover:bg-teal-500">
          {{ showCreateIface ? 'Cancel' : '+ New Interface' }}
        </button>
      </div>

      <!-- Create form -->
      <div v-if="showCreateIface" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-4">
        <div class="grid grid-cols-2 gap-3 mb-3">
          <input v-model="ifaceForm.id" placeholder="ID (e.g. iridium_1)" class="px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" />
          <select v-model="ifaceForm.channel_type" class="px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <option v-for="t in channelTypes" :key="t" :value="t">{{ t }}</option>
          </select>
          <input v-model="ifaceForm.label" placeholder="Label" class="px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" />
          <label class="flex items-center gap-2 text-sm text-gray-400">
            <input type="checkbox" v-model="ifaceForm.enabled" class="rounded" /> Enabled
          </label>
        </div>
        <button @click="saveInterface" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Create</button>
      </div>

      <!-- Interface list -->
      <div v-for="iface in store.interfaces" :key="iface.id" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-3">
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-3">
            <span class="px-2 py-0.5 rounded text-[10px] font-bold uppercase" :class="stateColor(iface.state)">
              {{ iface.state }}
            </span>
            <div>
              <span class="text-sm font-medium text-gray-200">{{ iface.id }}</span>
              <span class="text-xs text-gray-500 ml-2">{{ iface.label || iface.channel_type }}</span>
            </div>
          </div>
          <div class="flex items-center gap-2">
            <button @click="toggleInterface(iface)" class="px-2 py-1 rounded text-xs"
              :class="iface.enabled ? 'bg-emerald-500/20 text-emerald-400' : 'bg-gray-700 text-gray-500'">
              {{ iface.enabled ? 'ON' : 'OFF' }}
            </button>
            <button @click="removeInterface(iface.id)" class="px-2 py-1 rounded bg-red-900/30 text-red-400 text-xs hover:bg-red-900/50">
              Delete
            </button>
          </div>
        </div>

        <!-- Device binding info -->
        <div class="mt-2 text-xs text-gray-500">
          <span v-if="iface.device_id">
            Device: <span class="text-gray-300">{{ iface.device_id }}</span>
            <span v-if="iface.device_port" class="ml-2">Port: {{ iface.device_port }}</span>
            <button @click="doUnbindDevice(iface.id)" class="ml-2 text-yellow-500 hover:text-yellow-400">Unbind</button>
          </span>
          <span v-else>
            No device bound
            <span v-if="unassignedDevices.length > 0" class="ml-2">
              &mdash; Bind:
              <button v-for="d in unassignedDevices" :key="d.device_id" @click="doBindDevice(iface.id, d.device_id)"
                class="ml-1 px-1.5 py-0.5 rounded bg-gray-700 text-gray-300 hover:bg-teal-700 hover:text-teal-300">
                {{ d.device_type }} ({{ d.port }})
              </button>
            </span>
          </span>
        </div>

        <!-- Transform pipeline -->
        <div class="mt-2 flex items-center gap-1.5 flex-wrap">
          <span class="text-[10px] text-gray-600 mr-1">Transforms:</span>
          <button v-for="tt in transformTypes" :key="tt"
            @click="toggleTransform(iface, tt)" :disabled="generatingKey && tt === 'encrypt'"
            class="px-2 py-0.5 rounded text-[10px] font-medium transition-colors border"
            :class="hasTransform(iface, tt) ? transformBadge(tt) + ' border-current' : 'bg-gray-800 text-gray-600 border-gray-700 hover:border-gray-500'">
            {{ tt }}
          </button>
          <span v-if="isTextOnly(iface) && hasEncryption(iface)" class="text-[9px] text-yellow-500 ml-1">+base64 auto</span>
        </div>

        <!-- Expanded key (encryption) -->
        <div class="mt-1">
          <button v-if="hasEncryption(iface)" @click="expandedIface = expandedIface === iface.id ? null : iface.id"
            class="text-[10px] text-gray-500 hover:text-amber-400">
            {{ expandedIface === iface.id ? 'Hide key' : 'Show encryption key' }}
          </button>
          <div v-if="expandedIface === iface.id && hasEncryption(iface)" class="mt-1 p-2 rounded bg-gray-900 border border-gray-700">
            <div class="text-[10px] text-gray-500 mb-1">PSK (share with receiving end)</div>
            <code class="text-xs text-amber-400 break-all select-all">{{ getEncryptionKey(iface) }}</code>
            <div v-if="isTextOnly(iface)" class="mt-1.5 text-[10px] text-yellow-500">
              Text-only transport &mdash; base64 auto-added (33% overhead).
            </div>
          </div>
        </div>
      </div>

      <div v-if="!(store.interfaces || []).length" class="text-sm text-gray-500 text-center py-8">
        No interfaces configured. Create one to get started.
      </div>
    </div>

    <!-- ═══ Access Rules Tab ═══ -->
    <div v-if="activeTab === 'rules'">
      <div class="flex items-center justify-between mb-4">
        <span class="text-sm text-gray-400">{{ (store.accessRules || []).length }} access rules</span>
        <button @click="openNewRule"
          class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs hover:bg-teal-500">
          + New Rule
        </button>
      </div>

      <!-- Structured rule editor -->
      <div v-if="showCreateRule" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-4">
        <div class="text-xs text-gray-500 mb-3">{{ editingRule ? 'Edit Access Rule' : 'New Access Rule' }}</div>

        <div class="grid grid-cols-2 gap-3 mb-3">
          <div>
            <label class="text-[10px] text-gray-500 mb-1 block">Name</label>
            <input v-model="ruleForm.name" placeholder="Rule name" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" />
          </div>
          <div>
            <label class="text-[10px] text-gray-500 mb-1 block">Interface</label>
            <select v-model="ruleForm.interface_id" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <option value="">Select interface</option>
              <option v-for="i in store.interfaces" :key="i.id" :value="i.id">{{ i.id }} ({{ i.channel_type }})</option>
            </select>
          </div>
          <div>
            <label class="text-[10px] text-gray-500 mb-1 block">Direction</label>
            <select v-model="ruleForm.direction" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <option value="ingress">Ingress (incoming)</option>
              <option value="egress">Egress (outgoing)</option>
            </select>
          </div>
          <div>
            <label class="text-[10px] text-gray-500 mb-1 block">Action</label>
            <select v-model="ruleForm.action" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <option value="forward">Forward</option>
              <option value="drop">Drop</option>
              <option value="log">Log only</option>
            </select>
          </div>
          <div v-if="ruleForm.action === 'forward'">
            <label class="text-[10px] text-gray-500 mb-1 block">Forward to</label>
            <select v-model="ruleForm.forward_to" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <option value="">Select target...</option>
              <option v-for="t in forwardTargets" :key="t.value" :value="t.value">{{ t.label }}</option>
            </select>
          </div>
          <div>
            <label class="text-[10px] text-gray-500 mb-1 block">Priority</label>
            <input v-model.number="ruleForm.priority" type="number" min="1" max="999"
              class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" />
          </div>
        </div>

        <!-- Filters: Object Group selectors -->
        <div class="border-t border-gray-700 pt-3 mb-3">
          <div class="text-[10px] text-gray-500 uppercase mb-2">Filters (Object Groups)</div>
          <div class="grid grid-cols-3 gap-3">
            <div>
              <label class="text-[10px] text-gray-600 mb-1 block">Node Group</label>
              <select v-model="ruleForm.node_group" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300">
                <option value="">Any node</option>
                <option v-for="g in nodeGroups" :key="g.id" :value="g.id">{{ g.id }} ({{ g.label || g.members }})</option>
              </select>
            </div>
            <div>
              <label class="text-[10px] text-gray-600 mb-1 block">Sender Group</label>
              <select v-model="ruleForm.sender_group" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300">
                <option value="">Any sender</option>
                <option v-for="g in senderGroups" :key="g.id" :value="g.id">{{ g.id }} ({{ g.label || g.members }})</option>
              </select>
            </div>
            <div>
              <label class="text-[10px] text-gray-600 mb-1 block">Portnum Group</label>
              <select v-model="ruleForm.portnum_group" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300">
                <option value="">Any portnum</option>
                <option v-for="g in portnumGroups" :key="g.id" :value="g.id">{{ g.id }} ({{ g.label || g.members }})</option>
              </select>
            </div>
          </div>
        </div>

        <!-- Rate limiting -->
        <div class="border-t border-gray-700 pt-3 mb-3">
          <div class="text-[10px] text-gray-500 uppercase mb-2">Rate Limiting</div>
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="text-[10px] text-gray-600 mb-1 block">Max per minute (0=unlimited)</label>
              <input v-model.number="ruleForm.rate_per_min" type="number" min="0"
                class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300" />
            </div>
            <div>
              <label class="text-[10px] text-gray-600 mb-1 block">Window (seconds)</label>
              <input v-model.number="ruleForm.rate_window_sec" type="number" min="1"
                class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300" />
            </div>
          </div>
        </div>

        <!-- Schedule -->
        <div class="border-t border-gray-700 pt-3 mb-3">
          <div class="text-[10px] text-gray-500 uppercase mb-2">Schedule</div>
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="text-[10px] text-gray-600 mb-1 block">Type</label>
              <select v-model="ruleForm.schedule_type" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300">
                <option value="none">Always active</option>
                <option value="daily">Daily window</option>
                <option value="weekly">Weekly window</option>
                <option value="monthly">Monthly window</option>
              </select>
            </div>
            <div v-if="ruleForm.schedule_type !== 'none'">
              <label class="text-[10px] text-gray-600 mb-1 block">Value (e.g. 08:00-18:00)</label>
              <input v-model="ruleForm.schedule_value" placeholder="08:00-18:00"
                class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300" />
            </div>
          </div>
        </div>

        <!-- QoS + enabled -->
        <div class="flex items-center gap-4 mb-3">
          <div>
            <label class="text-[10px] text-gray-600 mb-1 block">QoS Level</label>
            <select v-model.number="ruleForm.qos_level" class="px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300">
              <option :value="0">0 - Best effort</option>
              <option :value="1">1 - Normal</option>
              <option :value="2">2 - High</option>
              <option :value="3">3 - Critical</option>
            </select>
          </div>
          <label class="flex items-center gap-2 text-xs text-gray-400 mt-4">
            <input type="checkbox" v-model="ruleForm.enabled" class="rounded" /> Enabled
          </label>
        </div>

        <div class="flex gap-2">
          <button @click="saveRule" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">
            {{ editingRule ? 'Update' : 'Create' }}
          </button>
          <button @click="showCreateRule = false" class="px-4 py-2 rounded bg-gray-700 text-gray-300 text-sm hover:bg-gray-600">Cancel</button>
        </div>
      </div>

      <!-- Rules list -->
      <div v-for="rule in store.accessRules" :key="rule.id" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-3">
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-2">
            <span class="px-2 py-0.5 rounded text-[10px] font-bold uppercase" :class="actionColor(rule.action)">
              {{ rule.action }}
            </span>
            <span class="text-sm font-medium text-gray-200">{{ rule.name || `Rule #${rule.id}` }}</span>
            <span class="text-xs text-gray-500">{{ rule.interface_id }} / {{ rule.direction }}</span>
          </div>
          <div class="flex items-center gap-2 text-xs">
            <span class="text-gray-500">P{{ rule.priority }}</span>
            <span v-if="rule.forward_to" class="text-gray-400">--> {{ rule.forward_to }}</span>
            <span class="text-gray-600">{{ rule.match_count || 0 }} hits</span>
            <button @click="openEditRule(rule)" class="px-2 py-1 rounded bg-gray-700 text-gray-300 hover:bg-gray-600">Edit</button>
            <button @click="removeRule(rule.id)" class="px-2 py-1 rounded bg-red-900/30 text-red-400 hover:bg-red-900/50">Del</button>
          </div>
        </div>
        <!-- Show parsed filters -->
        <div class="mt-1.5 flex flex-wrap gap-1.5 text-[10px]">
          <span v-if="parseFilters(rule).node_group" class="px-1.5 py-0.5 rounded bg-gray-700 text-gray-400">nodes: {{ parseFilters(rule).node_group }}</span>
          <span v-if="parseFilters(rule).sender_group" class="px-1.5 py-0.5 rounded bg-gray-700 text-gray-400">senders: {{ parseFilters(rule).sender_group }}</span>
          <span v-if="parseFilters(rule).portnum_group" class="px-1.5 py-0.5 rounded bg-gray-700 text-gray-400">portnums: {{ parseFilters(rule).portnum_group }}</span>
          <span v-if="rule.rate_per_min > 0" class="px-1.5 py-0.5 rounded bg-amber-400/10 text-amber-400">rate: {{ rule.rate_per_min }}/min</span>
          <span v-if="rule.schedule_type && rule.schedule_type !== 'none'" class="px-1.5 py-0.5 rounded bg-blue-400/10 text-blue-400">{{ rule.schedule_type }}: {{ rule.schedule_value }}</span>
          <span v-if="rule.qos_level > 0" class="px-1.5 py-0.5 rounded bg-purple-400/10 text-purple-400">QoS {{ rule.qos_level }}</span>
        </div>
      </div>

      <div v-if="!(store.accessRules || []).length" class="text-sm text-gray-500 text-center py-8">
        No access rules. All traffic is implicitly denied.
      </div>
    </div>

    <!-- ═══ Devices Tab ═══ -->
    <div v-if="activeTab === 'devices'">
      <div class="mb-4 text-sm text-gray-400">
        {{ (store.devices || []).length }} USB devices detected
      </div>
      <div v-for="dev in store.devices" :key="dev.device_id" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-3">
        <div class="flex items-center justify-between">
          <div>
            <span class="text-sm font-medium text-gray-200">{{ dev.port }}</span>
            <span class="text-xs text-gray-500 ml-2">{{ dev.vid_pid }}</span>
            <span class="ml-2 px-2 py-0.5 rounded text-[10px] font-bold uppercase bg-gray-700 text-gray-400">{{ dev.device_type }}</span>
          </div>
          <div class="text-xs text-gray-500">
            <span v-if="dev.bound_to" class="text-teal-400">Bound to {{ dev.bound_to }}</span>
            <span v-else class="text-gray-600">Unassigned</span>
          </div>
        </div>
        <div class="text-xs text-gray-600 mt-1">ID: {{ dev.device_id }}</div>
        <div v-if="dev.usb_serial" class="text-xs text-gray-600">Serial: {{ dev.usb_serial }}</div>
      </div>
      <div v-if="!(store.devices || []).length" class="text-sm text-gray-500 text-center py-8">
        No USB serial devices detected.
      </div>
    </div>

    <!-- ═══ Object Groups Tab ═══ -->
    <div v-if="activeTab === 'groups'">
      <div class="flex items-center justify-between mb-4">
        <span class="text-sm text-gray-400">{{ (store.objectGroups || []).length }} object groups</span>
        <button @click="openNewGroup"
          class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs hover:bg-teal-500">
          + New Group
        </button>
      </div>

      <!-- Group editor -->
      <div v-if="showCreateGroup" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-4">
        <div class="text-xs text-gray-500 mb-3">{{ editingGroup ? 'Edit Object Group' : 'New Object Group' }}</div>
        <div class="grid grid-cols-2 gap-3 mb-3">
          <div>
            <label class="text-[10px] text-gray-600 mb-1 block">ID</label>
            <input v-model="groupForm.id" :disabled="!!editingGroup" placeholder="e.g. field_nodes"
              class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 disabled:opacity-50" />
          </div>
          <div>
            <label class="text-[10px] text-gray-600 mb-1 block">Type</label>
            <select v-model="groupForm.type" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <option value="node_group">Node Group</option>
              <option value="sender_group">Sender Group</option>
              <option value="portnum_group">Portnum Group</option>
            </select>
          </div>
          <div>
            <label class="text-[10px] text-gray-600 mb-1 block">Label</label>
            <input v-model="groupForm.label" placeholder="Descriptive name"
              class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" />
          </div>
        </div>
        <div class="mb-3">
          <label class="text-[10px] text-gray-600 mb-1 block">Members (comma-separated)</label>
          <textarea v-model="groupForm.members" rows="2" placeholder="!abcd1234, !efgh5678 or 1, 3, 67"
            class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-xs text-gray-200 font-mono"></textarea>
          <div class="text-[9px] text-gray-600 mt-1">
            <span v-if="groupForm.type === 'node_group'">Node IDs (hex, e.g. !abcd1234)</span>
            <span v-else-if="groupForm.type === 'sender_group'">Sender addresses</span>
            <span v-else>Portnum numbers (e.g. 1=TEXT, 3=POSITION, 67=TELEMETRY)</span>
          </div>
        </div>
        <div class="flex gap-2">
          <button @click="saveGroup" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">
            {{ editingGroup ? 'Update' : 'Create' }}
          </button>
          <button @click="showCreateGroup = false" class="px-4 py-2 rounded bg-gray-700 text-gray-300 text-sm hover:bg-gray-600">Cancel</button>
        </div>
      </div>

      <!-- Group list -->
      <div v-for="g in store.objectGroups" :key="g.id" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-3">
        <div class="flex items-center justify-between">
          <div>
            <span class="text-sm font-medium text-gray-200">{{ g.id }}</span>
            <span class="ml-2 px-2 py-0.5 rounded text-[10px] bg-gray-700 text-gray-400">{{ g.type }}</span>
            <span v-if="g.label" class="text-xs text-gray-500 ml-2">{{ g.label }}</span>
          </div>
          <div class="flex items-center gap-2">
            <button @click="openEditGroup(g)" class="px-2 py-1 rounded bg-gray-700 text-gray-300 text-xs hover:bg-gray-600">Edit</button>
            <button @click="removeGroup(g.id)" class="px-2 py-1 rounded bg-red-900/30 text-red-400 text-xs hover:bg-red-900/50">Delete</button>
          </div>
        </div>
        <div class="text-xs text-gray-500 mt-1 font-mono">{{ g.members }}</div>
      </div>

      <div v-if="!(store.objectGroups || []).length" class="text-sm text-gray-500 text-center py-8">
        No object groups defined. Create groups to use as filters in access rules.
      </div>
    </div>

    <!-- ═══ Failover Groups Tab ═══ -->
    <div v-if="activeTab === 'failover'">
      <div class="flex items-center justify-between mb-4">
        <span class="text-sm text-gray-400">{{ (store.failoverGroups || []).length }} failover groups</span>
        <button @click="openNewFailover"
          class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs hover:bg-teal-500">
          + New Failover Group
        </button>
      </div>

      <!-- Failover editor -->
      <div v-if="showCreateFailover" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-4">
        <div class="text-xs text-gray-500 mb-3">New Failover Group</div>
        <div class="grid grid-cols-3 gap-3 mb-3">
          <div>
            <label class="text-[10px] text-gray-600 mb-1 block">ID</label>
            <input v-model="failoverForm.id" placeholder="e.g. sat_failover"
              class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" />
          </div>
          <div>
            <label class="text-[10px] text-gray-600 mb-1 block">Label</label>
            <input v-model="failoverForm.label" placeholder="Satellite failover"
              class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" />
          </div>
          <div>
            <label class="text-[10px] text-gray-600 mb-1 block">Mode</label>
            <select v-model="failoverForm.mode" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <option value="priority">Priority (highest first)</option>
              <option value="round_robin">Round-robin</option>
            </select>
          </div>
        </div>

        <!-- Members -->
        <div class="text-[10px] text-gray-500 uppercase mb-2">Members (ordered by priority)</div>
        <div v-for="(m, idx) in failoverForm.members" :key="idx" class="flex items-center gap-2 mb-2">
          <span class="text-xs text-gray-600 w-8">P{{ m.priority }}</span>
          <select v-model="m.interface_id" class="flex-1 px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300">
            <option value="">Select interface</option>
            <option v-for="i in store.interfaces" :key="i.id" :value="i.id">{{ i.id }} ({{ i.channel_type }})</option>
          </select>
          <input v-model.number="m.priority" type="number" min="1" max="99"
            class="w-16 px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300" />
          <button v-if="failoverForm.members.length > 1" @click="removeFailoverMember(idx)"
            class="px-2 py-1 rounded bg-red-900/30 text-red-400 text-xs hover:bg-red-900/50">x</button>
        </div>
        <button @click="addFailoverMember" class="text-xs text-teal-400 hover:text-teal-300 mb-3">+ Add member</button>

        <div class="flex gap-2">
          <button @click="saveFailover" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Create</button>
          <button @click="showCreateFailover = false" class="px-4 py-2 rounded bg-gray-700 text-gray-300 text-sm hover:bg-gray-600">Cancel</button>
        </div>
      </div>

      <!-- Failover list -->
      <div v-for="g in store.failoverGroups" :key="g.id" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-3">
        <div class="flex items-center justify-between">
          <div>
            <span class="text-sm font-medium text-gray-200">{{ g.id }}</span>
            <span class="text-xs text-gray-500 ml-2">{{ g.label }} ({{ g.mode }})</span>
          </div>
          <button @click="removeFailoverGroup(g.id)" class="px-2 py-1 rounded bg-red-900/30 text-red-400 text-xs hover:bg-red-900/50">Delete</button>
        </div>
        <div v-if="g.members && g.members.length" class="mt-2 space-y-1">
          <div v-for="m in g.members" :key="m.id" class="flex items-center gap-2 text-xs">
            <span class="text-gray-600 w-8">P{{ m.priority }}</span>
            <span class="text-gray-300">{{ m.interface_id }}</span>
            <span v-if="(store.interfaces || []).find(i => i.id === m.interface_id)?.state === 'online'"
              class="px-1.5 py-0.5 rounded text-[9px] bg-emerald-500/20 text-emerald-400">online</span>
            <span v-else class="px-1.5 py-0.5 rounded text-[9px] bg-gray-700 text-gray-500">
              {{ (store.interfaces || []).find(i => i.id === m.interface_id)?.state || 'unknown' }}
            </span>
          </div>
        </div>
        <div v-else class="text-xs text-gray-600 mt-2">No members</div>
      </div>

      <div v-if="!(store.failoverGroups || []).length" class="text-sm text-gray-500 text-center py-8">
        No failover groups configured. Create one to enable automatic failover between interfaces.
      </div>
    </div>
  </div>
</template>
