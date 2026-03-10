<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import ConfigSection from '@/components/ConfigSection.vue'

const store = useMeshsatStore()
const activeTab = ref('radio')
const tabs = [
  { id: 'radio', label: 'Radio' },
  { id: 'channels', label: 'Channels' },
  { id: 'position', label: 'Position' },
  { id: 'canned', label: 'Canned Msg' },
  { id: 'mqtt', label: 'MQTT' },
  { id: 'device_mqtt', label: 'Device MQTT' },
  { id: 'iridium', label: 'Iridium' },
  { id: 'astrocast', label: 'Astrocast' },
  { id: 'cellular', label: 'Cellular' },
  { id: 'zigbee', label: 'ZigBee' },
  { id: 'store_forward', label: 'S&F' },
  { id: 'range_test', label: 'Range Test' },
  { id: 'about', label: 'About' }
]

// Radio config
const radioSection = ref('lora')
const radioConfig = ref({})
const radioEditing = ref(false)
const radioJSON = ref('')

const radioRefreshing = ref(false)

async function refreshRadioSection() {
  radioRefreshing.value = true
  try {
    await store.fetchConfigSection(radioSection.value)
    setTimeout(() => store.fetchConfig(), 1500) // wait for device response
  } catch {}
  radioRefreshing.value = false
}

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

// Position sharing
const posForm = ref({ latitude: 0, longitude: 0, altitude: 0 })
const positionSending = ref(false)

async function doSendPosition() {
  positionSending.value = true
  try { await store.sendPosition(posForm.value) } catch {}
  positionSending.value = false
}

async function doSetFixedPosition() {
  try { await store.setFixedPosition(posForm.value) } catch {}
}

async function doRemoveFixedPosition() {
  try { await store.removeFixedPosition() } catch {}
}

// Canned messages
const cannedText = ref('')
const cannedLoading = ref(false)

async function loadCannedMessages() {
  cannedLoading.value = true
  try {
    const data = await store.getCannedMessages()
    if (data && data.messages) cannedText.value = data.messages
  } catch {}
  cannedLoading.value = false
}

async function saveCannedMessages() {
  try { await store.setCannedMessages(cannedText.value) } catch {}
}

// Device MQTT module
const deviceMqttForm = ref({})
const deviceMqttJSON = ref('')
const deviceMqttEditing = ref(false)

async function saveDeviceMqtt() {
  try {
    const data = JSON.parse(deviceMqttJSON.value)
    await store.configModule({ section: 'mqtt', config: data })
    deviceMqttEditing.value = false
  } catch (e) {
    store.error = e.message
  }
}

async function refreshDeviceMqtt() {
  try {
    await store.fetchModuleConfigSection('mqtt')
    setTimeout(() => store.fetchConfig(), 1500)
  } catch {}
}

// Store & Forward
const sfForm = ref({ node_id: 0, window: 3600 })

async function doRequestSF() {
  try { await store.requestStoreForward(sfForm.value) } catch {}
}

// Range Test
const rtForm = ref({ text: '', to: 0 })
const rtSending = ref(false)

async function doSendRangeTest() {
  rtSending.value = true
  try {
    await store.sendRangeTest(rtForm.value)
    await store.fetchRangeTests()
  } catch {}
  rtSending.value = false
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
  mailbox_mode: 'ring_alert_only', poll_interval: 1800, max_text_length: 320, include_position: true,
  dlq_max_retries: 0, dlq_retry_base_secs: 120, min_signal_bars: 1,
  daily_budget: 0, monthly_budget: 0, critical_reserve: 20,
  min_elev_deg: 5,
  expiry_policy: { critical_max_retries: 0, normal_max_retries: 0, low_max_retries: 0 }
})
const checkingMailbox = ref(false)

async function doCheckMailbox() {
  checkingMailbox.value = true
  try { await store.manualMailboxCheck() } catch {}
  checkingMailbox.value = false
}
const iridiumEnabled = ref(false)

const iridiumGw = computed(() => (store.gateways || []).find(g => g.type === 'iridium'))

function loadIridium() {
  if (iridiumGw.value?.config) {
    try {
      const c = typeof iridiumGw.value.config === 'string' ? JSON.parse(iridiumGw.value.config) : iridiumGw.value.config
      Object.assign(iridiumForm.value, c)
      // Ensure expiry_policy exists with defaults (backward compat with configs saved before this feature)
      if (!iridiumForm.value.expiry_policy || typeof iridiumForm.value.expiry_policy !== 'object') {
        iridiumForm.value.expiry_policy = { critical_max_retries: 0, normal_max_retries: 0, low_max_retries: 0 }
      }
      iridiumEnabled.value = iridiumGw.value.enabled
    } catch {}
  }
}

async function saveIridium() {
  await store.configureGateway('iridium', iridiumEnabled.value, iridiumForm.value)
}

// Credit budget
const budgetForm = ref({ daily: 0, monthly: 0 })

async function loadBudget() {
  await store.fetchCredits()
  if (store.creditSummary) {
    budgetForm.value.daily = store.creditSummary.daily_budget || 0
    budgetForm.value.monthly = store.creditSummary.monthly_budget || 0
  }
}

async function saveBudget() {
  await store.setCreditBudget(budgetForm.value.daily, budgetForm.value.monthly)
}

// Astrocast gateway
const astrocastForm = ref({
  max_uplink_bytes: 160, poll_interval_sec: 300, fragment_enabled: true,
  geoloc_enabled: false, power_mode: 'balanced'
})
const astrocastEnabled = ref(false)

const astrocastGw = computed(() => (store.gateways || []).find(g => g.type === 'astrocast'))

function loadAstrocast() {
  if (astrocastGw.value?.config) {
    try {
      const c = typeof astrocastGw.value.config === 'string' ? JSON.parse(astrocastGw.value.config) : astrocastGw.value.config
      Object.assign(astrocastForm.value, c)
      astrocastEnabled.value = astrocastGw.value.enabled
    } catch {}
  }
}

async function saveAstrocast() {
  await store.configureGateway('astrocast', astrocastEnabled.value, astrocastForm.value)
}

// Cellular gateway
const cellularForm = ref({
  sms_destinations: '', allowed_senders: '', sms_prefix: 'MESHSAT', max_segments: 3,
  apn: '', auto_connect: false,
  webhook_url: '', webhook_headers: '', inbound_webhook_enabled: false, inbound_webhook_secret: '',
  dyndns_provider: 'none', dyndns_domain: '', dyndns_token: '', dyndns_interval: 300
})
const cellularEnabled = ref(false)

// SIM PIN unlock
const settingsPinInput = ref('')
const settingsPinUnlocking = ref(false)
const settingsPinError = ref('')
async function unlockSettingsPIN() {
  settingsPinUnlocking.value = true
  settingsPinError.value = ''
  try {
    await store.submitCellularPIN(settingsPinInput.value)
    settingsPinInput.value = ''
    await store.fetchCellularStatus()
  } catch (e) {
    settingsPinError.value = e.message || 'PIN unlock failed'
  }
  settingsPinUnlocking.value = false
}

const cellularGw = computed(() => (store.gateways || []).find(g => g.type === 'cellular'))

function loadCellular() {
  if (cellularGw.value?.config) {
    try {
      const c = typeof cellularGw.value.config === 'string' ? JSON.parse(cellularGw.value.config) : cellularGw.value.config
      Object.assign(cellularForm.value, c)
      cellularEnabled.value = cellularGw.value.enabled
    } catch {}
  }
}

async function saveCellular() {
  await store.configureGateway('cellular', cellularEnabled.value, cellularForm.value)
}

// ZigBee gateway
const zigbeeForm = ref({
  serial_port: 'auto', inbound_channel: 0, inbound_dest: '',
  forward_all: false, default_dst_addr: 65535, default_dst_ep: 1, default_cluster: 6
})
const zigbeeEnabled = ref(false)
const zigbeeStatus = ref(null)
const zigbeeDevices = ref([])

const zigbeeGw = computed(() => (store.gateways || []).find(g => g.type === 'zigbee'))

function loadZigBee() {
  if (zigbeeGw.value?.config) {
    try {
      const c = typeof zigbeeGw.value.config === 'string' ? JSON.parse(zigbeeGw.value.config) : zigbeeGw.value.config
      Object.assign(zigbeeForm.value, c)
      zigbeeEnabled.value = zigbeeGw.value.enabled
    } catch {}
  }
}

async function saveZigBee() {
  await store.configureGateway('zigbee', zigbeeEnabled.value, zigbeeForm.value)
}

async function fetchZigBeeStatus() {
  try {
    const resp = await fetch('/api/zigbee/status')
    zigbeeStatus.value = await resp.json()
  } catch {}
}

async function fetchZigBeeDevices() {
  try {
    const resp = await fetch('/api/zigbee/devices')
    const data = await resp.json()
    zigbeeDevices.value = data.devices || []
  } catch {}
}

// Signal polling
let signalTimer = null

onMounted(async () => {
  store.fetchConfig()
  await store.fetchGateways()
  store.fetchIridiumSignalFast()
  signalTimer = setInterval(() => store.fetchIridiumSignalFast(), 10000)
  store.fetchCellularStatus()
  store.fetchCellularSignal()
  loadMQTT(); loadIridium(); loadBudget(); loadAstrocast(); loadCellular(); loadZigBee()
  fetchZigBeeStatus(); fetchZigBeeDevices()
  store.fetchRangeTests()
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
          <button @click="refreshRadioSection" :disabled="radioRefreshing" class="px-3 py-2 rounded bg-gray-800 border border-gray-700 text-xs text-gray-400 hover:text-teal-400 disabled:opacity-40">
            {{ radioRefreshing ? 'Refreshing...' : 'Refresh' }}
          </button>
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
          <span class="text-xs" :class="iridiumGw?.connected ? 'text-emerald-400' : 'text-gray-500'">
            {{ iridiumGw?.connected ? 'Connected' : 'Disconnected' }}
          </span>
        </div>

        <!-- Mailbox Polling Mode -->
        <div>
          <label class="block text-xs text-gray-500 mb-1">Mailbox Polling Mode</label>
          <select v-model="iridiumForm.mailbox_mode" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <option value="ring_alert_only">Ring Alert Only (no periodic polling, saves credits)</option>
            <option value="scheduled">Scheduled (pass-aware periodic polling)</option>
            <option value="off">Off (no mailbox checking)</option>
          </select>
          <p class="text-[10px] text-gray-600 mt-1">
            <template v-if="iridiumForm.mailbox_mode === 'ring_alert_only'">Waits for Iridium ring alerts and satellite pass events. Zero credit waste.</template>
            <template v-else-if="iridiumForm.mailbox_mode === 'scheduled'">Periodically checks mailbox using pass-aware scheduling. Costs 1 credit per empty check.</template>
            <template v-else>No mailbox checking at all. Inbound messages will not be received.</template>
          </p>
        </div>

        <!-- Poll interval (only shown for scheduled mode) -->
        <div v-if="iridiumForm.mailbox_mode === 'scheduled'" class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Idle Poll Interval (sec)</label>
            <input v-model.number="iridiumForm.idle_poll_sec" type="number" min="60" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Active Poll Interval (sec)</label>
            <input v-model.number="iridiumForm.active_poll_sec" type="number" min="10" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>

        <!-- Check Mailbox Now -->
        <div class="flex items-center gap-3">
          <button @click="doCheckMailbox" :disabled="checkingMailbox || !iridiumGw?.connected"
            class="px-3 py-1.5 rounded bg-gray-700 border border-gray-600 text-xs text-gray-300 hover:text-teal-400 hover:border-teal-600/30 transition-colors disabled:opacity-40 disabled:cursor-not-allowed">
            {{ checkingMailbox ? 'Checking...' : 'Check Mailbox Now' }}
          </button>
          <span class="text-[10px] text-gray-600">Triggers a one-shot SBDIX session (costs 1 credit if mailbox is empty)</span>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Max Text Length</label>
            <input v-model.number="iridiumForm.max_text_length" type="number" min="1" max="340" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Min Signal Bars</label>
            <input v-model.number="iridiumForm.min_signal_bars" type="number" min="0" max="5" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Min Elevation (°)</label>
            <input v-model.number="iridiumForm.min_elev_deg" type="number" min="0" max="90" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <p class="text-[10px] text-gray-600 mt-0.5">Pass scheduler min elevation (5=clear sky, 40=urban, 60=canyon)</p>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Critical Reserve (%)</label>
            <input v-model.number="iridiumForm.critical_reserve" type="number" min="0" max="100" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
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

        <!-- Per-Priority Message Expiration -->
        <div class="border-t border-gray-700 pt-3 mt-1">
          <h4 class="text-xs font-medium text-gray-300 mb-2">Message Expiration by Priority</h4>
          <p class="text-[10px] text-gray-600 mb-3">Configure how many retry attempts before a queued message expires. 0 or "Never expire" means infinite retries.</p>
          <div class="space-y-2">
            <div v-for="p in [{key: 'critical_max_retries', label: 'Critical (P0)', color: 'text-red-400'}, {key: 'normal_max_retries', label: 'Normal (P1)', color: 'text-gray-300'}, {key: 'low_max_retries', label: 'Low (P2)', color: 'text-gray-500'}]" :key="p.key"
              class="flex items-center gap-3">
              <span class="text-xs w-24" :class="p.color">{{ p.label }}</span>
              <label class="flex items-center gap-1 text-xs text-gray-400">
                <input type="checkbox" :checked="iridiumForm.expiry_policy[p.key] === 0"
                  @change="iridiumForm.expiry_policy[p.key] = $event.target.checked ? 0 : 10"
                  class="rounded bg-gray-900 border-gray-700">
                Never expire
              </label>
              <input v-if="iridiumForm.expiry_policy[p.key] > 0"
                v-model.number="iridiumForm.expiry_policy[p.key]" type="number" min="1" max="999"
                class="w-20 px-2 py-1 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <span v-if="iridiumForm.expiry_policy[p.key] > 0" class="text-[10px] text-gray-600">retries</span>
            </div>
          </div>
        </div>

        <div class="flex items-center gap-2">
          <input type="checkbox" v-model="iridiumEnabled" id="iridium_en" class="rounded bg-gray-900 border-gray-700">
          <label for="iridium_en" class="text-xs text-gray-400">Enable Iridium gateway</label>
        </div>
        <button @click="saveIridium" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save Iridium Config</button>
      </div>

      <!-- Credit Budget (dedicated API) -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mt-4">
        <h4 class="text-sm font-medium text-gray-200">Credit Budget</h4>
        <p class="text-xs text-gray-500">Set daily and monthly SBD credit limits. Zero means unlimited.</p>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Daily Limit</label>
            <input v-model.number="budgetForm.daily" type="number" min="0" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Monthly Limit</label>
            <input v-model.number="budgetForm.monthly" type="number" min="0" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div v-if="store.creditSummary" class="text-xs text-gray-500">
          Used today: {{ store.creditSummary.today }} | This month: {{ store.creditSummary.month }} | All time: {{ store.creditSummary.all_time }}
        </div>
        <button @click="saveBudget" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save Budget</button>
      </div>
    </div>

    <!-- Astrocast -->
    <div v-if="activeTab === 'astrocast'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-gray-200">Astrocast Astronode S</span>
          <span class="text-xs" :class="astrocastGw?.connected ? 'text-emerald-400' : 'text-gray-500'">
            {{ astrocastGw?.connected ? 'Connected' : 'Disconnected' }}
          </span>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Max Uplink Bytes</label>
            <input v-model.number="astrocastForm.max_uplink_bytes" type="number" min="1" max="160" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <p class="text-[10px] text-gray-600 mt-0.5">Astronode S max payload is 160 bytes</p>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Poll Interval (sec)</label>
            <input v-model.number="astrocastForm.poll_interval_sec" type="number" min="60" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <p class="text-[10px] text-gray-600 mt-0.5">How often to check for downlink messages</p>
          </div>
        </div>

        <div>
          <label class="block text-xs text-gray-500 mb-1">Power Mode</label>
          <select v-model="astrocastForm.power_mode" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <option value="low_power">Low Power (minimal polling, battery saving)</option>
            <option value="balanced">Balanced (default)</option>
            <option value="performance">Performance (aggressive polling)</option>
          </select>
        </div>

        <div class="flex flex-wrap gap-4">
          <label class="flex items-center gap-1 text-xs text-gray-400">
            <input type="checkbox" v-model="astrocastForm.fragment_enabled" class="rounded bg-gray-900 border-gray-700">
            Auto-fragment messages >160 bytes
          </label>
          <label class="flex items-center gap-1 text-xs text-gray-400">
            <input type="checkbox" v-model="astrocastForm.geoloc_enabled" class="rounded bg-gray-900 border-gray-700">
            Include geolocation in uplinks
          </label>
        </div>

        <div class="flex items-center gap-2">
          <input type="checkbox" v-model="astrocastEnabled" id="astrocast_en" class="rounded bg-gray-900 border-gray-700">
          <label for="astrocast_en" class="text-xs text-gray-400">Enable Astrocast gateway</label>
        </div>
        <button @click="saveAstrocast" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save Astrocast Config</button>
      </div>
    </div>

    <!-- Cellular -->
    <div v-if="activeTab === 'cellular'">
      <!-- Modem Status (read-only) -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mb-4">
        <h4 class="text-sm font-medium text-gray-200">Modem Status</h4>
        <div class="space-y-2 text-[11px]">
          <div class="flex justify-between">
            <span class="text-gray-500">IMEI</span>
            <span class="text-gray-300 font-mono">{{ store.cellularStatus?.imei || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Model</span>
            <span class="text-gray-300">{{ store.cellularStatus?.model || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Operator</span>
            <span class="text-gray-300">{{ store.cellularStatus?.operator || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Network Type</span>
            <span class="text-gray-300">{{ store.cellularStatus?.network_type || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">SIM State</span>
            <span class="text-gray-300">{{ store.cellularStatus?.sim_state || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Registration</span>
            <span class="text-gray-300">{{ store.cellularStatus?.registration || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Signal</span>
            <span class="text-gray-300">{{ store.cellularSignal?.bars ?? 'N/A' }}/5 bars ({{ store.cellularSignal?.dbm ?? 'N/A' }} dBm)</span>
          </div>
        </div>
      </div>

      <!-- SIM PIN Unlock -->
      <div v-if="store.cellularStatus?.sim_state === 'PIN_REQUIRED'" class="bg-amber-900/20 rounded-lg p-4 border border-amber-700/40 space-y-3 mb-4">
        <h4 class="text-sm font-medium text-amber-400">SIM PIN Required</h4>
        <p class="text-xs text-gray-400">The SIM card requires a PIN to unlock. Enter the 4-8 digit PIN below.</p>
        <div class="flex items-center gap-2">
          <input type="password" v-model="settingsPinInput" maxlength="8" placeholder="SIM PIN"
            class="flex-1 px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" />
          <button @click="unlockSettingsPIN" :disabled="settingsPinUnlocking"
            class="px-4 py-2 rounded bg-amber-600 text-white text-sm hover:bg-amber-500 disabled:opacity-50">
            {{ settingsPinUnlocking ? 'Unlocking...' : 'Unlock SIM' }}
          </button>
        </div>
        <div v-if="settingsPinError" class="text-xs text-red-400">{{ settingsPinError }}</div>
      </div>

      <!-- SMS Configuration -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mb-4">
        <h4 class="text-sm font-medium text-gray-200">SMS Configuration</h4>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Destination Numbers (comma-separated)</label>
          <input v-model="cellularForm.sms_destinations" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="+1234567890,+0987654321">
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Allowed Senders (comma-separated, empty = all)</label>
          <input v-model="cellularForm.allowed_senders" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="+1234567890">
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">SMS Prefix</label>
            <input v-model="cellularForm.sms_prefix" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Max Segments</label>
            <input v-model.number="cellularForm.max_segments" type="number" min="1" max="10" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
      </div>

      <!-- Data Connection -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mb-4">
        <h4 class="text-sm font-medium text-gray-200">Data Connection</h4>
        <div>
          <label class="block text-xs text-gray-500 mb-1">APN</label>
          <input v-model="cellularForm.apn" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="internet">
        </div>
        <label class="flex items-center gap-1 text-xs text-gray-400">
          <input type="checkbox" v-model="cellularForm.auto_connect" class="rounded bg-gray-900 border-gray-700"> Auto-connect on boot
        </label>
        <div class="flex gap-2">
          <button @click="store.connectCellularData(cellularForm.apn)" class="px-3 py-1.5 rounded bg-emerald-600 text-white text-xs hover:bg-emerald-500">Connect</button>
          <button @click="store.disconnectCellularData()" class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs hover:bg-gray-600">Disconnect</button>
        </div>
      </div>

      <!-- Webhooks -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mb-4">
        <h4 class="text-sm font-medium text-gray-200">Webhooks</h4>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Outbound URL</label>
          <input v-model="cellularForm.webhook_url" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="https://example.com/webhook">
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Outbound Headers (JSON)</label>
          <input v-model="cellularForm.webhook_headers" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" placeholder='{"Authorization": "Bearer ..."}'>
        </div>
        <div class="flex gap-4">
          <label class="flex items-center gap-1 text-xs text-gray-400"><input type="checkbox" v-model="cellularForm.inbound_webhook_enabled" class="rounded bg-gray-900 border-gray-700"> Inbound webhook</label>
        </div>
        <div v-if="cellularForm.inbound_webhook_enabled">
          <label class="block text-xs text-gray-500 mb-1">Inbound Secret</label>
          <input v-model="cellularForm.inbound_webhook_secret" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono">
        </div>
      </div>

      <!-- DynDNS -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mb-4">
        <h4 class="text-sm font-medium text-gray-200">Dynamic DNS</h4>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Provider</label>
            <select v-model="cellularForm.dyndns_provider" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <option value="none">None</option>
              <option value="duckdns">DuckDNS</option>
              <option value="noip">No-IP</option>
              <option value="cloudflare">Cloudflare</option>
              <option value="custom">Custom</option>
            </select>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Update Interval (sec)</label>
            <input v-model.number="cellularForm.dyndns_interval" type="number" min="60" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div v-if="cellularForm.dyndns_provider !== 'none'">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Domain</label>
            <input v-model="cellularForm.dyndns_domain" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="mydevice.duckdns.org">
          </div>
          <div class="mt-3">
            <label class="block text-xs text-gray-500 mb-1">Token / Credentials</label>
            <input v-model="cellularForm.dyndns_token" type="password" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
      </div>

      <!-- Enable + Save -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center gap-2">
          <input type="checkbox" v-model="cellularEnabled" id="cellular_en" class="rounded bg-gray-900 border-gray-700">
          <label for="cellular_en" class="text-xs text-gray-400">Enable Cellular gateway</label>
        </div>
        <button @click="saveCellular" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save Cellular Config</button>
      </div>
    </div>

    <!-- ZigBee -->
    <div v-if="activeTab === 'zigbee'" class="space-y-4">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-gray-200">ZigBee 3.0 Coordinator</span>
          <span class="text-xs" :class="zigbeeStatus?.connected ? 'text-emerald-400' : 'text-gray-500'">
            {{ zigbeeStatus?.connected ? 'Connected' : 'Disconnected' }}
          </span>
        </div>

        <div v-if="zigbeeStatus?.firmware" class="text-[11px] text-gray-500">
          Firmware: {{ zigbeeStatus.firmware }}
          <span v-if="zigbeeStatus?.uptime" class="ml-3">Uptime: {{ zigbeeStatus.uptime }}</span>
        </div>

        <div>
          <label class="block text-xs text-gray-500 mb-1">Serial Port</label>
          <input v-model="zigbeeForm.serial_port" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="auto">
          <p class="text-[10px] text-gray-600 mt-0.5">"auto" scans USB ports for CC2652P/CC2531 coordinator dongles</p>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Default Dest Address</label>
            <input v-model.number="zigbeeForm.default_dst_addr" type="number" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <p class="text-[10px] text-gray-600 mt-0.5">65535 = broadcast (0xFFFF)</p>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Default Endpoint</label>
            <input v-model.number="zigbeeForm.default_dst_ep" type="number" min="1" max="240" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Default Cluster ID</label>
            <input v-model.number="zigbeeForm.default_cluster" type="number" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <p class="text-[10px] text-gray-600 mt-0.5">6 = On/Off, 8 = Level Control</p>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Inbound Mesh Channel</label>
            <input v-model.number="zigbeeForm.inbound_channel" type="number" min="0" max="7" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>

        <div class="flex flex-wrap gap-4">
          <label class="flex items-center gap-1 text-xs text-gray-400">
            <input type="checkbox" v-model="zigbeeForm.forward_all" class="rounded bg-gray-900 border-gray-700">
            Forward all mesh messages to ZigBee
          </label>
        </div>

        <div class="flex items-center gap-2">
          <input type="checkbox" v-model="zigbeeEnabled" id="zigbee_en" class="rounded bg-gray-900 border-gray-700">
          <label for="zigbee_en" class="text-xs text-gray-400">Enable ZigBee gateway</label>
        </div>
        <button @click="saveZigBee" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save ZigBee Config</button>
      </div>

      <!-- Paired Devices -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium text-gray-200">Paired Devices ({{ zigbeeDevices.length }})</span>
          <button @click="fetchZigBeeDevices" class="text-xs text-teal-400 hover:text-teal-300">Refresh</button>
        </div>
        <div v-if="zigbeeDevices.length === 0" class="text-xs text-gray-500 py-2">
          No devices paired yet. Pair a ZigBee device by putting it in pairing mode.
        </div>
        <div v-else class="divide-y divide-gray-700/50">
          <div v-for="dev in zigbeeDevices" :key="dev.short_addr" class="py-2 flex items-center justify-between text-xs">
            <div>
              <span class="text-gray-200 font-mono">0x{{ dev.short_addr.toString(16).padStart(4, '0').toUpperCase() }}</span>
              <span v-if="dev.ieee_addr" class="text-gray-500 ml-2 font-mono">{{ dev.ieee_addr }}</span>
            </div>
            <div class="flex items-center gap-3">
              <span class="text-gray-500">EP {{ dev.endpoint }}</span>
              <span :class="dev.lqi > 150 ? 'text-emerald-400' : dev.lqi > 80 ? 'text-amber-400' : 'text-red-400'">
                LQI {{ dev.lqi }}
              </span>
              <span class="text-gray-600" v-if="dev.last_seen">
                {{ new Date(dev.last_seen).toLocaleTimeString() }}
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Position -->
    <div v-if="activeTab === 'position'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <h4 class="text-sm font-medium text-gray-200">Share Position</h4>
        <p class="text-xs text-gray-500">Broadcast MeshSat's location as a position packet to the mesh.</p>
        <div class="grid grid-cols-3 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Latitude</label>
            <input v-model.number="posForm.latitude" type="number" step="0.000001" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Longitude</label>
            <input v-model.number="posForm.longitude" type="number" step="0.000001" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Altitude (m)</label>
            <input v-model.number="posForm.altitude" type="number" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="flex gap-2">
          <button @click="doSendPosition" :disabled="positionSending" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500 disabled:opacity-40">
            {{ positionSending ? 'Sending...' : 'Send Position' }}
          </button>
        </div>
      </div>
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mt-4">
        <h4 class="text-sm font-medium text-gray-200">Fixed Position</h4>
        <p class="text-xs text-gray-500">Set a fixed GPS position on the device (disables GPS module, uses this position).</p>
        <div class="flex gap-2">
          <button @click="doSetFixedPosition" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Set Fixed Position</button>
          <button @click="doRemoveFixedPosition" class="px-4 py-2 rounded bg-gray-700 text-gray-300 text-sm hover:bg-gray-600">Remove Fixed</button>
        </div>
      </div>
    </div>

    <!-- Canned Messages -->
    <div v-if="activeTab === 'canned'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <h4 class="text-sm font-medium text-gray-200">Canned Messages</h4>
        <p class="text-xs text-gray-500">Configure quick-send messages on the device. Separate messages with pipe (|) characters.</p>
        <button @click="loadCannedMessages" :disabled="cannedLoading" class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs hover:text-teal-400 disabled:opacity-40">
          {{ cannedLoading ? 'Requesting...' : 'Request from Device' }}
        </button>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Messages (pipe-separated)</label>
          <textarea v-model="cannedText" rows="4" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" placeholder="OK|Help|SOS|Returning to base|Position report"></textarea>
        </div>
        <button @click="saveCannedMessages" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save to Device</button>
      </div>
    </div>

    <!-- Device MQTT Module -->
    <div v-if="activeTab === 'device_mqtt'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-gray-200">Device Built-in MQTT</span>
          <button @click="refreshDeviceMqtt" class="text-xs text-gray-400 hover:text-teal-400">Refresh from Device</button>
        </div>
        <p class="text-xs text-gray-500">Configure the Meshtastic device's built-in MQTT module. This is separate from MeshSat's MQTT gateway.</p>
        <div v-if="!deviceMqttEditing">
          <pre class="bg-gray-900 rounded-lg p-4 text-xs text-gray-400 overflow-x-auto">{{ JSON.stringify(store.config?.['module_1'] || {}, null, 2) }}</pre>
          <button @click="deviceMqttEditing = true" class="mt-2 px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs hover:text-teal-400">Edit JSON</button>
        </div>
        <div v-else>
          <textarea v-model="deviceMqttJSON" rows="8" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" placeholder='{"1": true, "4": "mqtt.meshtastic.org"}'></textarea>
          <p class="text-[10px] text-gray-600 mt-1">ModuleConfig.MQTTConfig fields: 1=enabled, 2=address, 3=username, 4=password, 5=encryption_enabled, 6=json_enabled, 7=tls_enabled, 8=root</p>
          <div class="flex gap-2 mt-2">
            <button @click="deviceMqttEditing = false" class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs">Cancel</button>
            <button @click="saveDeviceMqtt" class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs">Apply</button>
          </div>
        </div>
      </div>
    </div>

    <!-- Store & Forward -->
    <div v-if="activeTab === 'store_forward'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <h4 class="text-sm font-medium text-gray-200">Store & Forward</h4>
        <p class="text-xs text-gray-500">Request missed messages from a Store & Forward server node. The S&F node must have the store_forward module enabled.</p>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">S&F Server Node ID (decimal)</label>
            <input v-model.number="sfForm.node_id" type="number" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="e.g. 1234567890">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">History Window (seconds)</label>
            <input v-model.number="sfForm.window" type="number" min="60" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <button @click="doRequestSF" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Request History</button>
        <p class="text-[10px] text-gray-600">Responses will appear as messages in the Messages view via SSE events.</p>
      </div>
    </div>

    <!-- Range Test -->
    <div v-if="activeTab === 'range_test'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <h4 class="text-sm font-medium text-gray-200">Range Test</h4>
        <p class="text-xs text-gray-500">Send a range test packet. Receiving nodes with Range Test enabled will log it.</p>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Text (optional)</label>
            <input v-model="rtForm.text" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="auto-generated if empty">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">To Node (0 = broadcast)</label>
            <input v-model.number="rtForm.to" type="number" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <button @click="doSendRangeTest" :disabled="rtSending" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500 disabled:opacity-40">
          {{ rtSending ? 'Sending...' : 'Send Range Test' }}
        </button>
      </div>
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 mt-4">
        <h4 class="text-sm font-medium text-gray-200 mb-3">Range Test History</h4>
        <div v-if="store.rangeTests.length === 0" class="text-xs text-gray-500">No range test results yet.</div>
        <div v-else class="space-y-2">
          <div v-for="rt in store.rangeTests" :key="rt.id" class="flex items-center justify-between text-xs bg-gray-900 rounded px-3 py-2">
            <div>
              <span class="text-gray-400">{{ rt.from_node }}</span>
              <span class="text-gray-600 mx-1">&rarr;</span>
              <span class="text-gray-400">{{ rt.to_node || 'broadcast' }}</span>
            </div>
            <div class="flex items-center gap-3">
              <span class="text-gray-500">SNR {{ rt.rx_snr?.toFixed(1) }}</span>
              <span class="text-gray-500">RSSI {{ rt.rx_rssi }}</span>
              <span class="text-gray-600">{{ new Date(rt.created_at).toLocaleTimeString() }}</span>
            </div>
          </div>
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
