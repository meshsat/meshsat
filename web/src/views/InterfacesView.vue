<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const activeTab = ref('interfaces')
const showCreateIface = ref(false)
const showCreateRule = ref(false)
const editingRule = ref(null)
const expandedIface = ref(null)
const generatingKey = ref(false)

const tabs = [
  { id: 'interfaces', label: 'Interfaces' },
  { id: 'rules', label: 'Access Rules' },
  { id: 'devices', label: 'Devices' },
  { id: 'groups', label: 'Object Groups' },
  { id: 'failover', label: 'Failover' }
]

// Interface form
const ifaceForm = ref({ id: '', channel_type: 'mesh', label: '', enabled: true })
const channelTypes = ['mesh', 'iridium', 'astrocast', 'cellular', 'webhook', 'mqtt']

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

async function toggleEncryption(iface) {
  const transforms = getTransforms(iface, 'egress')
  if (hasEncryption(iface)) {
    // Remove encryption transform
    const filtered = transforms.filter(t => t.type !== 'encrypt')
    const ingressFiltered = getTransforms(iface, 'ingress').filter(t => t.type !== 'encrypt')
    await store.updateInterface(iface.id, {
      ...iface,
      egress_transforms: JSON.stringify(filtered),
      ingress_transforms: JSON.stringify(ingressFiltered)
    })
  } else {
    // Generate key and add encryption transform
    generatingKey.value = true
    try {
      const res = await store.generateEncryptionKey()
      const key = res.key
      transforms.push({ type: 'encrypt', params: { key } })
      const ingressTransforms = getTransforms(iface, 'ingress')
      ingressTransforms.push({ type: 'encrypt', params: { key } })
      await store.updateInterface(iface.id, {
        ...iface,
        egress_transforms: JSON.stringify(transforms),
        ingress_transforms: JSON.stringify(ingressTransforms)
      })
    } finally {
      generatingKey.value = false
    }
  }
}

// Access rule form
const ruleForm = ref({
  interface_id: '', direction: 'ingress', priority: 10, name: '', enabled: true,
  action: 'forward', forward_to: '', filters: '{}', qos_level: 0
})

function openNewRule() {
  editingRule.value = null
  ruleForm.value = {
    interface_id: '', direction: 'ingress', priority: 10, name: '', enabled: true,
    action: 'forward', forward_to: '', filters: '{}', qos_level: 0
  }
  showCreateRule.value = true
}

function openEditRule(rule) {
  editingRule.value = rule.id
  ruleForm.value = { ...rule, filters: typeof rule.filters === 'string' ? rule.filters : JSON.stringify(rule.filters) }
  showCreateRule.value = true
}

async function saveRule() {
  if (!ruleForm.value.interface_id || !ruleForm.value.name) return
  try {
    if (editingRule.value) {
      await store.updateAccessRule(editingRule.value, ruleForm.value)
    } else {
      await store.createAccessRule(ruleForm.value)
    }
    showCreateRule.value = false
  } catch { /* store error */ }
}

async function removeRule(id) {
  if (!confirm('Delete this access rule?')) return
  try { await store.deleteAccessRule(id) } catch { /* store error */ }
}

// State badge
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

// Unassigned devices (not bound to any interface)
const unassignedDevices = computed(() =>
  (store.devices || []).filter(d => !d.bound_to)
)

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
      </button>
    </div>

    <!-- Error banner -->
    <div v-if="store.error" class="mb-4 p-3 rounded-lg bg-red-900/30 border border-red-700 text-sm text-red-300">
      {{ store.error }}
    </div>

    <!-- Interfaces Tab -->
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
              — Bind:
              <button v-for="d in unassignedDevices" :key="d.device_id" @click="doBindDevice(iface.id, d.device_id)"
                class="ml-1 px-1.5 py-0.5 rounded bg-gray-700 text-gray-300 hover:bg-teal-700 hover:text-teal-300">
                {{ d.device_type }} ({{ d.port }})
              </button>
            </span>
          </span>
        </div>

        <!-- Encryption status -->
        <div class="mt-2 flex items-center gap-2 text-xs">
          <button @click="toggleEncryption(iface)" :disabled="generatingKey"
            class="px-2 py-1 rounded text-xs"
            :class="hasEncryption(iface) ? 'bg-amber-500/20 text-amber-400' : 'bg-gray-700 text-gray-500 hover:bg-gray-600'">
            {{ hasEncryption(iface) ? 'AES-256-GCM' : 'No encryption' }}
          </button>
          <button v-if="hasEncryption(iface)" @click="expandedIface = expandedIface === iface.id ? null : iface.id"
            class="px-2 py-1 rounded bg-gray-700 text-gray-400 hover:bg-gray-600">
            {{ expandedIface === iface.id ? 'Hide key' : 'Show key' }}
          </button>
        </div>

        <!-- Expanded encryption key -->
        <div v-if="expandedIface === iface.id && hasEncryption(iface)" class="mt-2 p-2 rounded bg-gray-900 border border-gray-700">
          <div class="text-[10px] text-gray-500 mb-1">PSK (share with receiving end)</div>
          <code class="text-xs text-amber-400 break-all select-all">{{ getEncryptionKey(iface) }}</code>
        </div>
      </div>

      <div v-if="!(store.interfaces || []).length" class="text-sm text-gray-500 text-center py-8">
        No interfaces configured. Create one to get started.
      </div>
    </div>

    <!-- Access Rules Tab -->
    <div v-if="activeTab === 'rules'">
      <div class="flex items-center justify-between mb-4">
        <span class="text-sm text-gray-400">{{ (store.accessRules || []).length }} access rules</span>
        <button @click="openNewRule"
          class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs hover:bg-teal-500">
          + New Rule
        </button>
      </div>

      <!-- Rule editor -->
      <div v-if="showCreateRule" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-4">
        <div class="grid grid-cols-2 gap-3 mb-3">
          <input v-model="ruleForm.name" placeholder="Rule name" class="px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" />
          <select v-model="ruleForm.interface_id" class="px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <option value="">Select interface</option>
            <option v-for="i in store.interfaces" :key="i.id" :value="i.id">{{ i.id }}</option>
          </select>
          <select v-model="ruleForm.direction" class="px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <option value="ingress">Ingress</option>
            <option value="egress">Egress</option>
          </select>
          <select v-model="ruleForm.action" class="px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <option value="forward">Forward</option>
            <option value="drop">Drop</option>
            <option value="log">Log</option>
          </select>
          <select v-if="ruleForm.action === 'forward'" v-model="ruleForm.forward_to" class="px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <option value="">Forward to...</option>
            <option v-for="i in store.interfaces" :key="i.id" :value="i.id">{{ i.id }}</option>
          </select>
          <input v-model.number="ruleForm.priority" type="number" placeholder="Priority" class="px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" />
        </div>
        <div class="mb-3">
          <label class="text-xs text-gray-500 mb-1 block">Filters JSON</label>
          <textarea v-model="ruleForm.filters" rows="2" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-xs text-gray-200 font-mono" placeholder='{"keyword":"","channels":"","nodes":"","portnums":""}'></textarea>
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
      </div>

      <div v-if="!(store.accessRules || []).length" class="text-sm text-gray-500 text-center py-8">
        No access rules. All traffic is implicitly denied.
      </div>
    </div>

    <!-- Devices Tab -->
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
      </div>
      <div v-if="!(store.devices || []).length" class="text-sm text-gray-500 text-center py-8">
        No USB serial devices detected.
      </div>
    </div>

    <!-- Object Groups Tab -->
    <div v-if="activeTab === 'groups'">
      <div class="mb-4 text-sm text-gray-400">
        {{ (store.objectGroups || []).length }} object groups
      </div>
      <div v-for="g in store.objectGroups" :key="g.id" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-3">
        <div class="flex items-center justify-between">
          <div>
            <span class="text-sm font-medium text-gray-200">{{ g.id }}</span>
            <span class="text-xs text-gray-500 ml-2">{{ g.type }} - {{ g.label || 'Unnamed' }}</span>
          </div>
          <button @click="store.deleteObjectGroup(g.id)" class="px-2 py-1 rounded bg-red-900/30 text-red-400 text-xs hover:bg-red-900/50">Delete</button>
        </div>
        <div class="text-xs text-gray-600 mt-1">Members: {{ g.members }}</div>
      </div>
      <div v-if="!(store.objectGroups || []).length" class="text-sm text-gray-500 text-center py-8">
        No object groups defined.
      </div>
    </div>

    <!-- Failover Groups Tab -->
    <div v-if="activeTab === 'failover'">
      <div class="mb-4 text-sm text-gray-400">
        {{ (store.failoverGroups || []).length }} failover groups
      </div>
      <div v-for="g in store.failoverGroups" :key="g.id" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-3">
        <div class="flex items-center justify-between">
          <div>
            <span class="text-sm font-medium text-gray-200">{{ g.id }}</span>
            <span class="text-xs text-gray-500 ml-2">{{ g.label }} ({{ g.mode }})</span>
          </div>
          <button @click="store.deleteFailoverGroup(g.id)" class="px-2 py-1 rounded bg-red-900/30 text-red-400 text-xs hover:bg-red-900/50">Delete</button>
        </div>
        <div v-if="g.members && g.members.length" class="mt-2">
          <div v-for="m in g.members" :key="m.id" class="text-xs text-gray-500">
            P{{ m.priority }}: {{ m.interface_id }}
          </div>
        </div>
      </div>
      <div v-if="!(store.failoverGroups || []).length" class="text-sm text-gray-500 text-center py-8">
        No failover groups configured.
      </div>
    </div>
  </div>
</template>
