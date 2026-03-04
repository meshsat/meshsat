<script setup>
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()

const editing = ref(null) // 'mqtt', 'iridium', or 'cellular'
const testing = ref(null)
const saving = ref(false)
const testResult = ref(null)
const showMqttHelp = ref(false)
const showIridiumHelp = ref(false)
const showCellularHelp = ref(false)
const signalRefreshing = ref(false)

// Signal polling — 10s fast (cached AT+CSQF)
const SIGNAL_POLL_MS = 10000
let signalTimer = null

function startSignalPolling() {
  store.fetchIridiumSignalFast()
  store.fetchCellularSignal()
  signalTimer = setInterval(() => {
    if (!signalRefreshing.value) store.fetchIridiumSignalFast()
    store.fetchCellularSignal()
  }, SIGNAL_POLL_MS)
}

function stopSignalPolling() {
  if (signalTimer) {
    clearInterval(signalTimer)
    signalTimer = null
  }
}

async function refreshSignalFull() {
  if (signalRefreshing.value) return
  signalRefreshing.value = true
  try {
    await store.fetchIridiumSignal()
  } finally {
    signalRefreshing.value = false
  }
}

function signalBarColor(bars) {
  if (bars >= 4) return '#10b981' // emerald
  if (bars >= 2) return '#f59e0b' // amber
  return '#ef4444'               // red
}

// MQTT form
const mqttForm = ref({
  broker_url: 'tcp://localhost:1883',
  username: '',
  password: '',
  client_id: 'meshsat',
  topic_prefix: 'msh/cubeos',
  channel_name: 'LongFast',
  qos: 1,
  tls: false,
  keep_alive: 60
})

// Iridium form
const iridiumForm = ref({
  forward_portnums: [1],
  forward_all: false,
  compression: 'compact',
  auto_receive: true,
  poll_interval: 0,
  max_text_length: 320,
  include_position: true
})

// Portnum checkboxes for Iridium
const PORTNUMS = [
  { value: 1, label: 'Text Messages', portnum: 'TEXT_MESSAGE' },
  { value: 3, label: 'Position', portnum: 'POSITION' },
  { value: 4, label: 'NodeInfo', portnum: 'NODEINFO' },
  { value: 67, label: 'Telemetry', portnum: 'TELEMETRY' }
]

function hasPortnum(val) {
  return iridiumForm.value.forward_portnums.includes(val)
}

function togglePortnum(val) {
  const arr = iridiumForm.value.forward_portnums
  const idx = arr.indexOf(val)
  if (idx >= 0) {
    arr.splice(idx, 1)
  } else {
    arr.push(val)
    arr.sort((a, b) => a - b)
  }
}

// Cellular form
const cellularForm = ref({
  destination_numbers: '',
  allowed_senders: '',
  sms_prefix: '[MeshSat]',
  max_sms_segments: 1,
  inbound_channel: 0,
  inbound_dest_node: '',
  apn: '',
  auto_connect_data: false,
  webhook_out_url: '',
  webhook_in_enabled: false,
  webhook_in_secret: '',
  dyndns: {
    enabled: false,
    provider: 'duckdns',
    domain: '',
    token: '',
    username: '',
    password: '',
    custom_url: '',
    interval: 300
  }
})

const mqttGateway = computed(() => store.gateways.find(g => g.type === 'mqtt'))
const iridiumGateway = computed(() => store.gateways.find(g => g.type === 'iridium'))
const cellularGateway = computed(() => store.gateways.find(g => g.type === 'cellular'))

onMounted(() => {
  store.fetchGateways()
  startSignalPolling()
})

onUnmounted(() => {
  stopSignalPolling()
})

function editGateway(type) {
  testResult.value = null
  if (type === 'mqtt' && mqttGateway.value?.config) {
    const cfg = mqttGateway.value.config
    mqttForm.value = {
      broker_url: cfg.broker_url || 'tcp://localhost:1883',
      username: cfg.username || '',
      password: cfg.password === '****' ? '' : (cfg.password || ''),
      client_id: cfg.client_id || 'meshsat',
      topic_prefix: cfg.topic_prefix || 'msh/cubeos',
      channel_name: cfg.channel_name || 'LongFast',
      qos: cfg.qos ?? 1,
      tls: cfg.tls ?? false,
      keep_alive: cfg.keep_alive ?? 60
    }
  }
  if (type === 'iridium' && iridiumGateway.value?.config) {
    const cfg = iridiumGateway.value.config
    iridiumForm.value = {
      forward_portnums: cfg.forward_portnums || [1],
      forward_all: cfg.forward_all ?? false,
      compression: cfg.compression || 'compact',
      auto_receive: cfg.auto_receive ?? true,
      poll_interval: cfg.poll_interval ?? 0,
      max_text_length: cfg.max_text_length ?? 320,
      include_position: cfg.include_position ?? true
    }
  }
  if (type === 'cellular' && cellularGateway.value?.config) {
    const cfg = cellularGateway.value.config
    cellularForm.value = {
      destination_numbers: (cfg.destination_numbers || []).join(', '),
      allowed_senders: (cfg.allowed_senders || []).join(', '),
      sms_prefix: cfg.sms_prefix || '[MeshSat]',
      max_sms_segments: cfg.max_sms_segments ?? 1,
      inbound_channel: cfg.inbound_channel ?? 0,
      inbound_dest_node: cfg.inbound_dest_node || '',
      apn: cfg.apn || '',
      auto_connect_data: cfg.auto_connect_data ?? false,
      webhook_out_url: cfg.webhook_out_url || '',
      webhook_in_enabled: cfg.webhook_in_enabled ?? false,
      webhook_in_secret: cfg.webhook_in_secret === '****' ? '' : (cfg.webhook_in_secret || ''),
      dyndns: {
        enabled: cfg.dyndns?.enabled ?? false,
        provider: cfg.dyndns?.provider || 'duckdns',
        domain: cfg.dyndns?.domain || '',
        token: cfg.dyndns?.token === '****' ? '' : (cfg.dyndns?.token || ''),
        username: cfg.dyndns?.username || '',
        password: cfg.dyndns?.password === '****' ? '' : (cfg.dyndns?.password || ''),
        custom_url: cfg.dyndns?.custom_url || '',
        interval: cfg.dyndns?.interval ?? 300
      }
    }
  }
  editing.value = type
}

function buildCellularConfig() {
  const f = cellularForm.value
  return {
    destination_numbers: f.destination_numbers.split(',').map(s => s.trim()).filter(Boolean),
    allowed_senders: f.allowed_senders.split(',').map(s => s.trim()).filter(Boolean),
    sms_prefix: f.sms_prefix,
    max_sms_segments: f.max_sms_segments,
    inbound_channel: f.inbound_channel,
    inbound_dest_node: f.inbound_dest_node || undefined,
    apn: f.apn || undefined,
    auto_connect_data: f.auto_connect_data,
    webhook_out_url: f.webhook_out_url || undefined,
    webhook_in_enabled: f.webhook_in_enabled,
    webhook_in_secret: f.webhook_in_secret || undefined,
    dyndns: f.dyndns.enabled ? { ...f.dyndns } : { enabled: false }
  }
}

async function saveGateway(type) {
  saving.value = true
  try {
    let config
    if (type === 'mqtt') config = { ...mqttForm.value }
    else if (type === 'iridium') config = { ...iridiumForm.value }
    else config = buildCellularConfig()
    const gw = type === 'mqtt' ? mqttGateway.value : type === 'iridium' ? iridiumGateway.value : cellularGateway.value
    const enabled = gw?.enabled ?? true
    await store.configureGateway(type, enabled, config)
    editing.value = null
  } catch {
    // error is set in store
  } finally {
    saving.value = false
  }
}

async function toggleGateway(type) {
  const gw = type === 'mqtt' ? mqttGateway.value : type === 'iridium' ? iridiumGateway.value : cellularGateway.value
  if (!gw) return
  try {
    if (gw.connected || gw.enabled) {
      await store.stopGateway(type)
    } else {
      await store.startGateway(type)
    }
  } catch {
    // error is set in store
  }
}

async function testGatewayConnection(type) {
  testing.value = type
  testResult.value = null
  try {
    await store.testGateway(type)
    testResult.value = { type, success: true }
  } catch (e) {
    testResult.value = { type, success: false, error: e.message }
  } finally {
    testing.value = null
  }
}

async function removeGateway(type) {
  if (!confirm(`Remove ${type} gateway configuration?`)) return
  try {
    await store.deleteGateway(type)
  } catch {
    // error is set in store
  }
}

function formatTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  if (isNaN(d.getTime())) return '-'
  return d.toLocaleTimeString()
}
</script>

<template>
  <div class="max-w-4xl mx-auto space-y-6">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-bold">Gateways</h1>
      <button
        @click="store.fetchGateways()"
        class="px-3 py-1.5 text-sm rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors"
      >
        Refresh
      </button>
    </div>

    <!-- Error banner -->
    <div v-if="store.error" class="bg-red-900/30 border border-red-800 rounded-lg p-3 text-red-300 text-sm">
      {{ store.error }}
    </div>

    <!-- MQTT Gateway Card -->
    <div class="bg-gray-900 rounded-xl border border-gray-800 overflow-hidden">
      <div class="p-5">
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-3">
            <div class="w-10 h-10 rounded-lg bg-gray-800 flex items-center justify-center">
              <svg class="w-5 h-5 text-teal-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>
              </svg>
            </div>
            <div>
              <h3 class="font-medium text-gray-200">MQTT Gateway</h3>
              <p class="text-xs text-gray-500 mt-0.5">Bridge mesh messages to MQTT broker</p>
            </div>
          </div>
          <div class="flex items-center gap-2">
            <span
              class="text-xs px-2 py-1 rounded-full"
              :class="mqttGateway?.connected ? 'bg-emerald-900/30 text-emerald-400' : 'bg-gray-800 text-gray-500'"
            >
              {{ mqttGateway?.connected ? 'Connected' : mqttGateway ? 'Disconnected' : 'Not configured' }}
            </span>
          </div>
        </div>

        <!-- Stats row -->
        <div v-if="mqttGateway" class="mt-4 grid grid-cols-4 gap-3">
          <div class="text-center">
            <p class="text-lg font-semibold text-gray-200">{{ mqttGateway.messages_in ?? 0 }}</p>
            <p class="text-xs text-gray-500">Messages In</p>
          </div>
          <div class="text-center">
            <p class="text-lg font-semibold text-gray-200">{{ mqttGateway.messages_out ?? 0 }}</p>
            <p class="text-xs text-gray-500">Messages Out</p>
          </div>
          <div class="text-center">
            <p class="text-lg font-semibold text-gray-200">{{ mqttGateway.errors ?? 0 }}</p>
            <p class="text-xs text-gray-500">Errors</p>
          </div>
          <div class="text-center">
            <p class="text-sm text-gray-300">{{ formatTime(mqttGateway.last_activity) }}</p>
            <p class="text-xs text-gray-500">Last Activity</p>
          </div>
        </div>

        <!-- Actions -->
        <div class="mt-4 flex gap-2">
          <button
            v-if="mqttGateway"
            @click="toggleGateway('mqtt')"
            class="px-3 py-1.5 text-xs rounded-lg transition-colors"
            :class="mqttGateway.connected ? 'bg-red-900/30 text-red-400 hover:bg-red-900/50' : 'bg-teal-900/30 text-teal-400 hover:bg-teal-900/50'"
          >
            {{ mqttGateway.connected ? 'Stop' : 'Start' }}
          </button>
          <button
            v-if="mqttGateway"
            @click="testGatewayConnection('mqtt')"
            :disabled="testing === 'mqtt'"
            class="px-3 py-1.5 text-xs rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors disabled:opacity-50"
          >
            {{ testing === 'mqtt' ? 'Testing...' : 'Test' }}
          </button>
          <button
            @click="editGateway('mqtt')"
            class="px-3 py-1.5 text-xs rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors"
          >
            Configure
          </button>
          <button
            v-if="mqttGateway"
            @click="removeGateway('mqtt')"
            class="px-3 py-1.5 text-xs rounded-lg bg-gray-800 text-gray-400 hover:text-red-400 transition-colors ml-auto"
          >
            Remove
          </button>
        </div>

        <!-- Test result -->
        <div v-if="testResult?.type === 'mqtt'" class="mt-3 text-xs px-3 py-2 rounded-lg"
          :class="testResult.success ? 'bg-emerald-900/20 text-emerald-400' : 'bg-red-900/20 text-red-400'"
        >
          {{ testResult.success ? 'Connection successful' : `Test failed: ${testResult.error}` }}
        </div>
      </div>

      <!-- MQTT Config Form -->
      <div v-if="editing === 'mqtt'" class="border-t border-gray-800 p-5 bg-gray-950/50">
        <h4 class="text-sm font-medium text-gray-300 mb-4">MQTT Configuration</h4>
        <div class="grid grid-cols-2 gap-4">
          <div class="col-span-2">
            <label class="block text-xs text-gray-400 mb-1">Broker URL</label>
            <input v-model="mqttForm.broker_url" type="text" placeholder="tcp://localhost:1883"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none"
              :class="mqttForm.broker_url ? 'border-gray-700' : 'border-red-700'" />
            <p class="text-[10px] text-gray-600 mt-1">Full broker address. Use tcp:// for plain, tls:// or ssl:// for encrypted connections.</p>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Username</label>
            <input v-model="mqttForm.username" type="text"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">Leave empty for anonymous connections.</p>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Password</label>
            <input v-model="mqttForm.password" type="password"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">Broker authentication password.</p>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Client ID</label>
            <input v-model="mqttForm.client_id" type="text"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">Unique identifier for this MQTT client. Default: meshsat.</p>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Topic Prefix</label>
            <input v-model="mqttForm.topic_prefix" type="text"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">MQTT topic namespace. Messages published to {prefix}/{channel}. Default: msh/cubeos.</p>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Channel Name</label>
            <input v-model="mqttForm.channel_name" type="text"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">Mesh channel to bridge. Must match your radio channel name. Default: LongFast.</p>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">QoS (0-2)</label>
            <select v-model.number="mqttForm.qos"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none">
              <option :value="0">0 - At most once</option>
              <option :value="1">1 - At least once</option>
              <option :value="2">2 - Exactly once</option>
            </select>
            <p class="text-[10px] text-gray-600 mt-1">QoS 1 recommended. QoS 2 adds latency but guarantees delivery.</p>
          </div>
          <div class="flex items-center gap-3 mt-4">
            <label class="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
              <input v-model="mqttForm.tls" type="checkbox" class="accent-teal-500" />
              TLS
            </label>
            <p class="text-[10px] text-gray-600">Enable TLS encryption for the MQTT connection.</p>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Keep Alive (seconds)</label>
            <input v-model.number="mqttForm.keep_alive" type="number" min="10" max="600"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">Ping interval to keep the connection alive. Default: 60s.</p>
          </div>
        </div>

        <!-- Connection Help -->
        <div class="mt-4">
          <button @click="showMqttHelp = !showMqttHelp"
            class="text-xs text-gray-400 hover:text-gray-200 transition-colors">
            {{ showMqttHelp ? 'Hide' : 'Show' }} Connection Help
          </button>
          <div v-if="showMqttHelp" class="mt-2 p-3 rounded-lg bg-gray-800/50 text-xs text-gray-400 space-y-2">
            <p>Common issues:</p>
            <ul class="list-disc list-inside space-y-1">
              <li>Ensure the broker is running and reachable from the MeshSat device.</li>
              <li>For Mosquitto: check /etc/mosquitto/mosquitto.conf allows external connections (listener 1883).</li>
              <li>If using TLS, the broker URL should start with tls:// or ssl://.</li>
              <li>The CubeOS Mosquitto coreapp listens on ports 6040 (MQTT) and 6041 (WebSocket).</li>
              <li>Check firewall rules if connecting to an external broker.</li>
            </ul>
          </div>
        </div>

        <div class="flex gap-2 mt-4">
          <button @click="saveGateway('mqtt')" :disabled="saving"
            class="px-4 py-2 text-sm rounded-lg bg-teal-600 text-white hover:bg-teal-500 transition-colors disabled:opacity-50">
            {{ saving ? 'Saving...' : 'Save' }}
          </button>
          <button @click="editing = null"
            class="px-4 py-2 text-sm rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors">
            Cancel
          </button>
        </div>
      </div>
    </div>

    <!-- Iridium Gateway Card -->
    <div class="bg-gray-900 rounded-xl border border-gray-800 overflow-hidden">
      <div class="p-5">
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-3">
            <div class="w-10 h-10 rounded-lg bg-gray-800 flex items-center justify-center">
              <svg class="w-5 h-5 text-teal-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <circle cx="12" cy="12" r="10"/>
                <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>
                <path d="M2 12h20"/>
              </svg>
            </div>
            <div>
              <h3 class="font-medium text-gray-200">Iridium Satellite</h3>
              <p class="text-xs text-gray-500 mt-0.5">Bridge mesh messages via Iridium SBD</p>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <!-- Signal bars -->
            <div v-if="store.iridiumSignal" class="flex items-center gap-2">
              <div
                class="flex items-end gap-0.5 h-4"
                :title="`Signal: ${store.iridiumSignal.bars}/5 (${store.iridiumSignal.assessment})`"
              >
                <div
                  v-for="bar in 5"
                  :key="bar"
                  class="w-1 rounded-sm"
                  :style="{
                    height: `${bar * 3}px`,
                    background: bar <= store.iridiumSignal.bars ? signalBarColor(store.iridiumSignal.bars) : '#374151'
                  }"
                />
              </div>
              <span class="text-xs text-gray-500">{{ store.iridiumSignal.bars }}/5</span>
              <button
                @click="refreshSignalFull"
                :disabled="signalRefreshing"
                class="p-0.5 rounded text-gray-600 hover:text-gray-300 transition-colors disabled:opacity-40"
                title="Force refresh signal (blocking scan)"
              >
                <svg class="w-3 h-3" :class="{ 'animate-spin': signalRefreshing }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/>
                  <path d="M21 3v5h-5"/>
                  <path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/>
                  <path d="M8 16H3v5"/>
                </svg>
              </button>
            </div>
            <span
              class="text-xs px-2 py-1 rounded-full"
              :class="iridiumGateway?.connected ? 'bg-emerald-900/30 text-emerald-400' : 'bg-gray-800 text-gray-500'"
            >
              {{ iridiumGateway?.connected ? 'Connected' : iridiumGateway ? 'Disconnected' : 'Not configured' }}
            </span>
          </div>
        </div>

        <!-- Stats row -->
        <div v-if="iridiumGateway" class="mt-4 grid grid-cols-4 gap-3">
          <div class="text-center">
            <p class="text-lg font-semibold text-gray-200">{{ iridiumGateway.messages_in ?? 0 }}</p>
            <p class="text-xs text-gray-500">Messages In</p>
          </div>
          <div class="text-center">
            <p class="text-lg font-semibold text-gray-200">{{ iridiumGateway.messages_out ?? 0 }}</p>
            <p class="text-xs text-gray-500">Messages Out</p>
          </div>
          <div class="text-center">
            <p class="text-lg font-semibold text-gray-200">{{ iridiumGateway.errors ?? 0 }}</p>
            <p class="text-xs text-gray-500">Errors</p>
          </div>
          <div class="text-center">
            <p class="text-sm text-gray-300">{{ formatTime(iridiumGateway.last_activity) }}</p>
            <p class="text-xs text-gray-500">Last Activity</p>
          </div>
        </div>

        <!-- Actions -->
        <div class="mt-4 flex gap-2">
          <button
            v-if="iridiumGateway"
            @click="toggleGateway('iridium')"
            class="px-3 py-1.5 text-xs rounded-lg transition-colors"
            :class="iridiumGateway.connected ? 'bg-red-900/30 text-red-400 hover:bg-red-900/50' : 'bg-teal-900/30 text-teal-400 hover:bg-teal-900/50'"
          >
            {{ iridiumGateway.connected ? 'Stop' : 'Start' }}
          </button>
          <button
            v-if="iridiumGateway"
            @click="testGatewayConnection('iridium')"
            :disabled="testing === 'iridium'"
            class="px-3 py-1.5 text-xs rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors disabled:opacity-50"
          >
            {{ testing === 'iridium' ? 'Testing...' : 'Test' }}
          </button>
          <button
            @click="editGateway('iridium')"
            class="px-3 py-1.5 text-xs rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors"
          >
            Configure
          </button>
          <button
            v-if="iridiumGateway"
            @click="removeGateway('iridium')"
            class="px-3 py-1.5 text-xs rounded-lg bg-gray-800 text-gray-400 hover:text-red-400 transition-colors ml-auto"
          >
            Remove
          </button>
        </div>

        <!-- Test result -->
        <div v-if="testResult?.type === 'iridium'" class="mt-3 text-xs px-3 py-2 rounded-lg"
          :class="testResult.success ? 'bg-emerald-900/20 text-emerald-400' : 'bg-red-900/20 text-red-400'"
        >
          {{ testResult.success ? 'Modem connected' : `Test failed: ${testResult.error}` }}
        </div>
      </div>

      <!-- Iridium Config Form -->
      <div v-if="editing === 'iridium'" class="border-t border-gray-800 p-5 bg-gray-950/50">
        <h4 class="text-sm font-medium text-gray-300 mb-4">Iridium Configuration</h4>
        <div class="space-y-4">
          <div>
            <label class="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
              <input v-model="iridiumForm.forward_all" type="checkbox" class="accent-teal-500" />
              Forward all message types
            </label>
            <p class="text-[10px] text-gray-600 mt-1">When disabled, only forward selected message types via satellite.</p>
          </div>
          <div v-if="!iridiumForm.forward_all">
            <label class="block text-xs text-gray-400 mb-2">Forward Message Types</label>
            <div class="space-y-2">
              <label v-for="pn in PORTNUMS" :key="pn.value"
                class="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
                <input
                  type="checkbox"
                  :checked="hasPortnum(pn.value)"
                  @change="togglePortnum(pn.value)"
                  class="accent-teal-500"
                />
                {{ pn.label }} <span class="text-[10px] text-gray-600">({{ pn.value }})</span>
              </label>
            </div>
            <p class="text-[10px] text-gray-600 mt-2">Select which Meshtastic portnums to forward via Iridium SBD.</p>
          </div>
          <div class="grid grid-cols-2 gap-4">
            <div>
              <label class="block text-xs text-gray-400 mb-1">Compression</label>
              <select v-model="iridiumForm.compression"
                class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none">
                <option value="compact">Compact binary</option>
                <option value="none">None (raw text)</option>
              </select>
              <p class="text-[10px] text-gray-600 mt-1">Compact binary saves SBD credits. Recommended for production use.</p>
            </div>
            <div>
              <label class="block text-xs text-gray-400 mb-1">Max Text Length</label>
              <input v-model.number="iridiumForm.max_text_length" type="number" min="1" max="340"
                class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
              <p class="text-[10px] text-gray-600 mt-1">Iridium SBD max payload is 340 bytes. Longer texts are truncated.</p>
            </div>
          </div>
          <div class="flex items-center gap-6">
            <div>
              <label class="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
                <input v-model="iridiumForm.auto_receive" type="checkbox" class="accent-teal-500" />
                Auto-receive on ring alerts
              </label>
              <p class="text-[10px] text-gray-600 mt-1">Automatically check for incoming SBD messages when the modem signals.</p>
            </div>
            <div>
              <label class="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
                <input v-model="iridiumForm.include_position" type="checkbox" class="accent-teal-500" />
                Include position
              </label>
              <p class="text-[10px] text-gray-600 mt-1">Attach GPS coordinates to outgoing SBD messages.</p>
            </div>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Poll Interval (seconds, 0 = disabled)</label>
            <input v-model.number="iridiumForm.poll_interval" type="number" min="0" max="3600"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">How often to check for incoming SBD messages. 0 = only on ring alerts. Uses SBD credits.</p>
          </div>
        </div>

        <!-- Connection Help -->
        <div class="mt-4">
          <button @click="showIridiumHelp = !showIridiumHelp"
            class="text-xs text-gray-400 hover:text-gray-200 transition-colors">
            {{ showIridiumHelp ? 'Hide' : 'Show' }} Connection Help
          </button>
          <div v-if="showIridiumHelp" class="mt-2 p-3 rounded-lg bg-gray-800/50 text-xs text-gray-400 space-y-2">
            <p>Common issues:</p>
            <ul class="list-disc list-inside space-y-1">
              <li>Ensure the RockBLOCK modem is connected via USB and the serial port is available.</li>
              <li>The modem needs a clear view of the sky for satellite communication.</li>
              <li>Check your RockBLOCK account has active line rental and SBD credits.</li>
              <li>Signal strength of 2+ bars is needed for reliable message transfer.</li>
              <li>Each SBD message costs 1 credit (~12 cents). Use compact compression to minimize usage.</li>
            </ul>
          </div>
        </div>

        <div class="flex gap-2 mt-4">
          <button @click="saveGateway('iridium')" :disabled="saving"
            class="px-4 py-2 text-sm rounded-lg bg-teal-600 text-white hover:bg-teal-500 transition-colors disabled:opacity-50">
            {{ saving ? 'Saving...' : 'Save' }}
          </button>
          <button @click="editing = null"
            class="px-4 py-2 text-sm rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors">
            Cancel
          </button>
        </div>
      </div>
    </div>

    <!-- Cellular Gateway Card -->
    <div class="bg-gray-900 rounded-xl border border-gray-800 overflow-hidden">
      <div class="p-5">
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-3">
            <div class="w-10 h-10 rounded-lg bg-gray-800 flex items-center justify-center">
              <svg class="w-5 h-5 text-teal-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <rect x="5" y="2" width="14" height="20" rx="2"/>
                <path d="M12 18h.01"/>
                <path d="M9 6h6"/>
              </svg>
            </div>
            <div>
              <h3 class="font-medium text-gray-200">Cellular (4G/LTE)</h3>
              <p class="text-xs text-gray-500 mt-0.5">Bridge mesh messages via SMS / webhooks</p>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <!-- Signal bars -->
            <div v-if="store.cellularSignal" class="flex items-center gap-2">
              <div
                class="flex items-end gap-0.5 h-4"
                :title="`Signal: ${store.cellularSignal.bars}/5 (${store.cellularSignal.technology || 'unknown'})`"
              >
                <div
                  v-for="bar in 5"
                  :key="bar"
                  class="w-1 rounded-sm"
                  :style="{
                    height: `${bar * 3}px`,
                    background: bar <= store.cellularSignal.bars ? signalBarColor(store.cellularSignal.bars) : '#374151'
                  }"
                />
              </div>
              <span class="text-xs text-gray-500">{{ store.cellularSignal.bars }}/5</span>
            </div>
            <span
              class="text-xs px-2 py-1 rounded-full"
              :class="cellularGateway?.connected ? 'bg-emerald-900/30 text-emerald-400' : 'bg-gray-800 text-gray-500'"
            >
              {{ cellularGateway?.connected ? 'Connected' : cellularGateway ? 'Disconnected' : 'Not configured' }}
            </span>
          </div>
        </div>

        <!-- Stats row -->
        <div v-if="cellularGateway" class="mt-4 grid grid-cols-4 gap-3">
          <div class="text-center">
            <p class="text-lg font-semibold text-gray-200">{{ cellularGateway.messages_in ?? 0 }}</p>
            <p class="text-xs text-gray-500">Messages In</p>
          </div>
          <div class="text-center">
            <p class="text-lg font-semibold text-gray-200">{{ cellularGateway.messages_out ?? 0 }}</p>
            <p class="text-xs text-gray-500">Messages Out</p>
          </div>
          <div class="text-center">
            <p class="text-lg font-semibold text-gray-200">{{ cellularGateway.errors ?? 0 }}</p>
            <p class="text-xs text-gray-500">Errors</p>
          </div>
          <div class="text-center">
            <p class="text-sm text-gray-300">{{ formatTime(cellularGateway.last_activity) }}</p>
            <p class="text-xs text-gray-500">Last Activity</p>
          </div>
        </div>

        <!-- Actions -->
        <div class="mt-4 flex gap-2">
          <button
            v-if="cellularGateway"
            @click="toggleGateway('cellular')"
            class="px-3 py-1.5 text-xs rounded-lg transition-colors"
            :class="cellularGateway.connected ? 'bg-red-900/30 text-red-400 hover:bg-red-900/50' : 'bg-teal-900/30 text-teal-400 hover:bg-teal-900/50'"
          >
            {{ cellularGateway.connected ? 'Stop' : 'Start' }}
          </button>
          <button
            v-if="cellularGateway"
            @click="testGatewayConnection('cellular')"
            :disabled="testing === 'cellular'"
            class="px-3 py-1.5 text-xs rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors disabled:opacity-50"
          >
            {{ testing === 'cellular' ? 'Testing...' : 'Test' }}
          </button>
          <button
            @click="editGateway('cellular')"
            class="px-3 py-1.5 text-xs rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors"
          >
            Configure
          </button>
          <button
            v-if="cellularGateway"
            @click="removeGateway('cellular')"
            class="px-3 py-1.5 text-xs rounded-lg bg-gray-800 text-gray-400 hover:text-red-400 transition-colors ml-auto"
          >
            Remove
          </button>
        </div>

        <!-- Test result -->
        <div v-if="testResult?.type === 'cellular'" class="mt-3 text-xs px-3 py-2 rounded-lg"
          :class="testResult.success ? 'bg-emerald-900/20 text-emerald-400' : 'bg-red-900/20 text-red-400'"
        >
          {{ testResult.success ? 'Modem connected, SIM ready' : `Test failed: ${testResult.error}` }}
        </div>
      </div>

      <!-- Cellular Config Form -->
      <div v-if="editing === 'cellular'" class="border-t border-gray-800 p-5 bg-gray-950/50">
        <h4 class="text-sm font-medium text-gray-300 mb-4">Cellular Configuration</h4>
        <div class="grid grid-cols-2 gap-4">
          <div class="col-span-2">
            <label class="block text-xs text-gray-400 mb-1">Destination Numbers (comma-separated)</label>
            <input v-model="cellularForm.destination_numbers" type="text" placeholder="+1234567890, +0987654321"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">Phone numbers to send SMS to. International format with +country code.</p>
          </div>
          <div class="col-span-2">
            <label class="block text-xs text-gray-400 mb-1">Allowed Senders (comma-separated, empty = all)</label>
            <input v-model="cellularForm.allowed_senders" type="text" placeholder="+1234567890"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">Only accept inbound SMS from these numbers. Empty means accept all.</p>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">SMS Prefix</label>
            <input v-model="cellularForm.sms_prefix" type="text"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">Prefix added to outgoing SMS. Default: [MeshSat].</p>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Max SMS Segments</label>
            <input v-model.number="cellularForm.max_sms_segments" type="number" min="1" max="10"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">Max 160 chars per segment. More segments = higher cost.</p>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Inbound Channel</label>
            <input v-model.number="cellularForm.inbound_channel" type="number" min="0" max="7"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">Mesh channel for incoming SMS messages (0-7).</p>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">APN</label>
            <input v-model="cellularForm.apn" type="text" placeholder="internet"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            <p class="text-[10px] text-gray-600 mt-1">Access Point Name for LTE data connection.</p>
          </div>
          <div class="col-span-2 flex items-center gap-6">
            <label class="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
              <input v-model="cellularForm.auto_connect_data" type="checkbox" class="accent-teal-500" />
              Auto-connect LTE data
            </label>
            <label class="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
              <input v-model="cellularForm.webhook_in_enabled" type="checkbox" class="accent-teal-500" />
              Enable inbound webhooks
            </label>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Webhook Out URL</label>
            <input v-model="cellularForm.webhook_out_url" type="text" placeholder="https://example.com/webhook"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Webhook In Secret</label>
            <input v-model="cellularForm.webhook_in_secret" type="password"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
        </div>

        <!-- DynDNS section -->
        <div class="mt-4 border-t border-gray-800 pt-4">
          <label class="flex items-center gap-2 text-sm text-gray-300 cursor-pointer mb-3">
            <input v-model="cellularForm.dyndns.enabled" type="checkbox" class="accent-teal-500" />
            Enable DynDNS Updater
          </label>
          <div v-if="cellularForm.dyndns.enabled" class="grid grid-cols-2 gap-4">
            <div>
              <label class="block text-xs text-gray-400 mb-1">Provider</label>
              <select v-model="cellularForm.dyndns.provider"
                class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none">
                <option value="duckdns">DuckDNS</option>
                <option value="noip">No-IP</option>
                <option value="dynu">Dynu</option>
                <option value="custom">Custom URL</option>
              </select>
            </div>
            <div>
              <label class="block text-xs text-gray-400 mb-1">Domain</label>
              <input v-model="cellularForm.dyndns.domain" type="text" placeholder="mymeshsat"
                class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
            <div v-if="cellularForm.dyndns.provider === 'duckdns'">
              <label class="block text-xs text-gray-400 mb-1">Token</label>
              <input v-model="cellularForm.dyndns.token" type="password"
                class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
            <div v-if="cellularForm.dyndns.provider === 'noip' || cellularForm.dyndns.provider === 'dynu'">
              <label class="block text-xs text-gray-400 mb-1">Username</label>
              <input v-model="cellularForm.dyndns.username" type="text"
                class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
            <div v-if="cellularForm.dyndns.provider === 'noip' || cellularForm.dyndns.provider === 'dynu'">
              <label class="block text-xs text-gray-400 mb-1">Password</label>
              <input v-model="cellularForm.dyndns.password" type="password"
                class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
            <div v-if="cellularForm.dyndns.provider === 'custom'" class="col-span-2">
              <label class="block text-xs text-gray-400 mb-1">Custom URL (use {ip} and {hostname} placeholders)</label>
              <input v-model="cellularForm.dyndns.custom_url" type="text" placeholder="https://api.example.com/update?ip={ip}&host={hostname}"
                class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
            <div>
              <label class="block text-xs text-gray-400 mb-1">Update Interval (seconds)</label>
              <input v-model.number="cellularForm.dyndns.interval" type="number" min="60" max="3600"
                class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
          </div>
        </div>

        <!-- Connection Help -->
        <div class="mt-4">
          <button @click="showCellularHelp = !showCellularHelp"
            class="text-xs text-gray-400 hover:text-gray-200 transition-colors">
            {{ showCellularHelp ? 'Hide' : 'Show' }} Connection Help
          </button>
          <div v-if="showCellularHelp" class="mt-2 p-3 rounded-lg bg-gray-800/50 text-xs text-gray-400 space-y-2">
            <p>Common issues:</p>
            <ul class="list-disc list-inside space-y-1">
              <li>Ensure the SIM7600 modem is connected via USB (VID:PID 1e0e:9001).</li>
              <li>A SIM card must be inserted and the PIN unlocked.</li>
              <li>Destination numbers must be in international format (+country code).</li>
              <li>For LTE data, configure the correct APN for your carrier.</li>
              <li>Each SMS costs per-message. Use rate limits on forwarding rules to control costs.</li>
              <li>DynDNS keeps a hostname pointed at your dynamic LTE IP for remote access.</li>
            </ul>
          </div>
        </div>

        <div class="flex gap-2 mt-4">
          <button @click="saveGateway('cellular')" :disabled="saving"
            class="px-4 py-2 text-sm rounded-lg bg-teal-600 text-white hover:bg-teal-500 transition-colors disabled:opacity-50">
            {{ saving ? 'Saving...' : 'Save' }}
          </button>
          <button @click="editing = null"
            class="px-4 py-2 text-sm rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors">
            Cancel
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
