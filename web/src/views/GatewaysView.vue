<script setup>
import { ref, onMounted, computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()

const editing = ref(null) // 'mqtt' or 'iridium'
const testing = ref(null)
const saving = ref(false)
const testResult = ref(null)
const showMqttHelp = ref(false)
const showIridiumHelp = ref(false)

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

const mqttGateway = computed(() => store.gateways.find(g => g.type === 'mqtt'))
const iridiumGateway = computed(() => store.gateways.find(g => g.type === 'iridium'))

onMounted(() => {
  store.fetchGateways()
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
  editing.value = type
}

async function saveGateway(type) {
  saving.value = true
  try {
    const config = type === 'mqtt' ? { ...mqttForm.value } : { ...iridiumForm.value }
    const gw = type === 'mqtt' ? mqttGateway.value : iridiumGateway.value
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
  const gw = type === 'mqtt' ? mqttGateway.value : iridiumGateway.value
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
          <div class="flex items-center gap-2">
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
  </div>
</template>
