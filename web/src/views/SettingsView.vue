<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import ConfigSection from '@/components/ConfigSection.vue'

const store = useMeshsatStore()
const activeTab = ref('radio')
const tabs = [
  { id: 'radio', label: 'Radio' },
  { id: 'channels', label: 'Channels' },
  { id: 'mqtt', label: 'MQTT' },
  { id: 'iridium', label: 'Iridium' },
  { id: 'presets', label: 'Presets' },
  { id: 'about', label: 'About' }
]

// Radio config
const radioSection = ref('lora')
const radioConfig = ref({})
const radioEditing = ref(false)
const radioJSON = ref('')

async function saveRadioConfig() {
  try {
    const data = JSON.parse(radioJSON.value)
    await store.configRadio({ section: radioSection.value, ...data })
    radioEditing.value = false
    store.fetchConfig()
  } catch (e) {
    store.error = e.message
  }
}

// Channels
const editingChannel = ref(null)
const channelForm = ref({})

const channels = computed(() => {
  if (!store.config?.channels) return []
  return Object.entries(store.config.channels).map(([idx, ch]) => ({ index: parseInt(idx), ...ch }))
})

function editChannel(ch) {
  editingChannel.value = ch.index
  channelForm.value = { index: ch.index, name: ch.name || '', psk: ch.psk || '', role: ch.role || 'SECONDARY', uplink_enabled: ch.uplink_enabled || false, downlink_enabled: ch.downlink_enabled || false }
}

async function saveChannel() {
  await store.setChannel(channelForm.value)
  editingChannel.value = null
  store.fetchConfig()
}

// MQTT gateway
const mqttForm = ref({ broker_url: '', username: '', password: '', client_id: 'meshsat', topic_prefix: 'msh/US', channel_name: 'LongFast', qos: 0, use_tls: false, keep_alive: 60 })
const mqttEnabled = ref(false)

const mqttGw = computed(() => (store.gateways || []).find(g => g.type === 'mqtt'))

function loadMQTT() {
  if (mqttGw.value?.config) {
    try {
      const c = typeof mqttGw.value.config === 'string' ? JSON.parse(mqttGw.value.config) : mqttGw.value.config
      Object.assign(mqttForm.value, c)
      mqttEnabled.value = mqttGw.value.enabled
    } catch {}
  }
}

async function saveMQTT() {
  await store.configureGateway('mqtt', mqttEnabled.value, mqttForm.value)
}

// Iridium gateway
const iridiumForm = ref({
  forward_all: false, forward_portnums: [1], compression: 'compact', auto_receive: true,
  poll_interval: 0, max_text_length: 320, include_position: true,
  dlq_max_retries: 3, dlq_retry_base_secs: 120, min_signal_bars: 1,
  daily_budget: 0, monthly_budget: 0, critical_reserve: 20
})
const iridiumEnabled = ref(false)

const iridiumGw = computed(() => (store.gateways || []).find(g => g.type === 'iridium'))

function loadIridium() {
  if (iridiumGw.value?.config) {
    try {
      const c = typeof iridiumGw.value.config === 'string' ? JSON.parse(iridiumGw.value.config) : iridiumGw.value.config
      Object.assign(iridiumForm.value, c)
      iridiumEnabled.value = iridiumGw.value.enabled
    } catch {}
  }
}

async function saveIridium() {
  await store.configureGateway('iridium', iridiumEnabled.value, iridiumForm.value)
}

// Signal polling
let signalTimer = null

// Presets
const presetForm = ref({ name: '', text: '', destination: 'broadcast' })
const editingPreset = ref(null)

async function savePreset() {
  if (editingPreset.value) {
    await store.updatePreset(editingPreset.value, presetForm.value)
  } else {
    await store.createPreset(presetForm.value)
  }
  editingPreset.value = null
  presetForm.value = { name: '', text: '', destination: 'broadcast' }
}

function editPreset(p) {
  editingPreset.value = p.id
  presetForm.value = { name: p.name, text: p.text, destination: p.destination }
}

async function removePreset(p) {
  if (confirm(`Delete preset "${p.name}"?`)) {
    await store.deletePreset(p.id)
  }
}

onMounted(() => {
  store.fetchConfig()
  store.fetchGateways()
  store.fetchPresets()
  store.fetchIridiumSignalFast()
  signalTimer = setInterval(() => store.fetchIridiumSignalFast(), 10000)
  setTimeout(() => { loadMQTT(); loadIridium() }, 500)
})

onUnmounted(() => { if (signalTimer) clearInterval(signalTimer) })
</script>

<template>
  <div class="max-w-3xl mx-auto">
    <h2 class="text-lg font-semibold text-gray-200 mb-4">Settings</h2>

    <!-- Tab bar -->
    <div class="flex gap-1 mb-6 overflow-x-auto pb-1">
      <button v-for="tab in tabs" :key="tab.id" @click="activeTab = tab.id"
        class="px-4 py-2 rounded-lg text-xs font-medium whitespace-nowrap transition-colors"
        :class="activeTab === tab.id ? 'bg-teal-600 text-white' : 'bg-gray-800 text-gray-400 hover:text-gray-200'">
        {{ tab.label }}
      </button>
    </div>

    <!-- Radio Config -->
    <div v-if="activeTab === 'radio'">
      <div v-if="!store.config" class="text-sm text-gray-500">Loading radio config...</div>
      <div v-else>
        <div class="flex gap-2 mb-4">
          <select v-model="radioSection" class="px-3 py-2 rounded bg-gray-800 border border-gray-700 text-sm text-gray-200">
            <option value="lora">LoRa Radio</option>
            <option value="device">Device</option>
            <option value="position">Position</option>
            <option value="power">Power</option>
            <option value="bluetooth">Bluetooth</option>
          </select>
          <button @click="radioEditing = !radioEditing" class="px-3 py-2 rounded bg-gray-800 border border-gray-700 text-xs text-gray-400 hover:text-teal-400">
            {{ radioEditing ? 'Cancel' : 'Edit JSON' }}
          </button>
        </div>
        <div v-if="radioEditing">
          <textarea v-model="radioJSON" rows="8" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" placeholder="{ ... }"></textarea>
          <button @click="saveRadioConfig" class="mt-2 px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Apply</button>
        </div>
        <pre v-else class="bg-gray-900 rounded-lg p-4 text-xs text-gray-400 overflow-x-auto">{{ JSON.stringify(store.config, null, 2) }}</pre>
      </div>
    </div>

    <!-- Channels -->
    <div v-if="activeTab === 'channels'">
      <div v-for="ch in channels" :key="ch.index" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-3">
        <div class="flex items-center justify-between mb-2">
          <div class="flex items-center gap-2">
            <span class="w-6 h-6 rounded bg-gray-700 flex items-center justify-center text-xs font-bold text-gray-400">{{ ch.index }}</span>
            <span class="text-sm text-gray-200">{{ ch.name || 'Unnamed' }}</span>
            <span class="px-1.5 py-0.5 rounded text-[10px] font-medium" :class="ch.role === 'PRIMARY' ? 'bg-teal-500/20 text-teal-400' : ch.role === 'DISABLED' ? 'bg-gray-600 text-gray-500' : 'bg-gray-700 text-gray-400'">
              {{ ch.role || 'SECONDARY' }}
            </span>
          </div>
          <button @click="editChannel(ch)" class="text-xs text-gray-400 hover:text-teal-400">Edit</button>
        </div>
        <div v-if="editingChannel === ch.index" class="mt-3 space-y-3">
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="block text-xs text-gray-500 mb-1">Name</label>
              <input v-model="channelForm.name" maxlength="11" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            </div>
            <div>
              <label class="block text-xs text-gray-500 mb-1">Role</label>
              <select v-model="channelForm.role" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
                <option>PRIMARY</option><option>SECONDARY</option><option>DISABLED</option>
              </select>
            </div>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">PSK (base64)</label>
            <input v-model="channelForm.psk" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono">
          </div>
          <div class="flex gap-4">
            <label class="flex items-center gap-1 text-xs text-gray-400"><input type="checkbox" v-model="channelForm.uplink_enabled" class="rounded bg-gray-900 border-gray-700"> Uplink</label>
            <label class="flex items-center gap-1 text-xs text-gray-400"><input type="checkbox" v-model="channelForm.downlink_enabled" class="rounded bg-gray-900 border-gray-700"> Downlink</label>
          </div>
          <div class="flex gap-2">
            <button @click="editingChannel = null" class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs">Cancel</button>
            <button @click="saveChannel" class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs">Save</button>
          </div>
        </div>
      </div>
    </div>

    <!-- MQTT -->
    <div v-if="activeTab === 'mqtt'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-gray-200">MQTT Gateway</span>
          <span v-if="mqttGw" class="text-xs" :class="mqttGw.connected ? 'text-emerald-400' : 'text-gray-500'">
            {{ mqttGw.connected ? 'Connected' : 'Disconnected' }}
          </span>
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Broker URL</label>
          <input v-model="mqttForm.broker_url" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="tcp://mosquitto:1883">
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Username</label>
            <input v-model="mqttForm.username" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Password</label>
            <input v-model="mqttForm.password" type="password" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Topic Prefix</label>
            <input v-model="mqttForm.topic_prefix" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Channel Name</label>
            <input v-model="mqttForm.channel_name" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="flex items-center gap-2">
          <input type="checkbox" v-model="mqttEnabled" id="mqtt_en" class="rounded bg-gray-900 border-gray-700">
          <label for="mqtt_en" class="text-xs text-gray-400">Enable MQTT gateway</label>
        </div>
        <button @click="saveMQTT" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save MQTT Config</button>
      </div>
    </div>

    <!-- Iridium -->
    <div v-if="activeTab === 'iridium'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-gray-200">Iridium Satellite</span>
          <div class="flex items-center gap-2">
            <div class="flex gap-0.5">
              <span v-for="i in 5" :key="i" class="w-1 rounded-full" :class="store.iridiumSignal?.bars >= i ? 'bg-emerald-400' : 'bg-gray-700'"
                :style="{ height: (8 + i * 3) + 'px' }"></span>
            </div>
            <span class="text-xs text-gray-400">{{ store.iridiumSignal?.bars || 0 }} bars</span>
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Max Text Length</label>
            <input v-model.number="iridiumForm.max_text_length" type="number" min="1" max="340" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Poll Interval (sec, 0=off)</label>
            <input v-model.number="iridiumForm.poll_interval" type="number" min="0" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Daily Budget (0=unlimited)</label>
            <input v-model.number="iridiumForm.daily_budget" type="number" min="0" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Monthly Budget (0=unlimited)</label>
            <input v-model.number="iridiumForm.monthly_budget" type="number" min="0" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="flex flex-wrap gap-4">
          <label class="flex items-center gap-1 text-xs text-gray-400"><input type="checkbox" v-model="iridiumForm.auto_receive" class="rounded bg-gray-900 border-gray-700"> Auto-receive</label>
          <label class="flex items-center gap-1 text-xs text-gray-400"><input type="checkbox" v-model="iridiumForm.include_position" class="rounded bg-gray-900 border-gray-700"> Include position</label>
          <label class="flex items-center gap-1 text-xs text-gray-400"><input type="checkbox" v-model="iridiumForm.forward_all" class="rounded bg-gray-900 border-gray-700"> Forward all</label>
        </div>
        <div class="flex items-center gap-2">
          <input type="checkbox" v-model="iridiumEnabled" id="iridium_en" class="rounded bg-gray-900 border-gray-700">
          <label for="iridium_en" class="text-xs text-gray-400">Enable Iridium gateway</label>
        </div>
        <button @click="saveIridium" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save Iridium Config</button>
      </div>
    </div>

    <!-- Presets -->
    <div v-if="activeTab === 'presets'">
      <div class="space-y-3 mb-4">
        <div v-for="p in store.presets" :key="p.id" class="bg-gray-800 rounded-lg p-3 border border-gray-700 flex items-center justify-between">
          <div>
            <div class="text-sm text-gray-200">{{ p.name }}</div>
            <div class="text-xs text-gray-500 mt-0.5">{{ p.text }}</div>
          </div>
          <div class="flex gap-2">
            <button @click="editPreset(p)" class="text-xs text-gray-400 hover:text-teal-400">Edit</button>
            <button @click="removePreset(p)" class="text-xs text-gray-400 hover:text-red-400">Delete</button>
          </div>
        </div>
      </div>
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <h4 class="text-sm text-gray-300">{{ editingPreset ? 'Edit Preset' : 'New Preset' }}</h4>
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
        <div class="flex gap-2">
          <button v-if="editingPreset" @click="editingPreset = null; presetForm = { name: '', text: '', destination: 'broadcast' }" class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs">Cancel</button>
          <button @click="savePreset" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">{{ editingPreset ? 'Update' : 'Create' }}</button>
        </div>
      </div>
    </div>

    <!-- About -->
    <div v-if="activeTab === 'about'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between">
          <span class="text-xs text-gray-500">Version</span>
          <span class="text-sm text-gray-300">0.2.0</span>
        </div>
        <div class="flex items-center justify-between">
          <span class="text-xs text-gray-500">Mode</span>
          <span class="text-sm text-gray-300">{{ store.status?.transport || 'unknown' }}</span>
        </div>
        <div class="flex items-center justify-between">
          <span class="text-xs text-gray-500">Radio Connected</span>
          <span class="text-sm" :class="store.status?.connected ? 'text-emerald-400' : 'text-red-400'">{{ store.status?.connected ? 'Yes' : 'No' }}</span>
        </div>
        <div class="flex items-center justify-between">
          <span class="text-xs text-gray-500">Nodes</span>
          <span class="text-sm text-gray-300">{{ store.status?.num_nodes || 0 }}</span>
        </div>
      </div>
    </div>
  </div>
</template>
