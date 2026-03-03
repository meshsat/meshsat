<script setup>
import { ref, reactive, computed, onMounted, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import ConfigSection from '@/components/ConfigSection.vue'

const store = useMeshsatStore()
const loading = ref(true)
const showRaw = ref(false)

// Editable local state per section — initialized from store.config
const editState = reactive({
  lora: {},
  device: {},
  position: {},
  power: {},
  display: {},
  bluetooth: {}
})

const saving = reactive({})
const saved = reactive({})

// Initialize edit state from store config
function syncFromStore() {
  const cfg = store.config || {}
  editState.lora = { ...(cfg.lora || {}) }
  editState.device = { ...(cfg.device || {}) }
  editState.position = { ...(cfg.position || {}) }
  editState.power = { ...(cfg.power || {}) }
  editState.display = { ...(cfg.display || {}) }
  editState.bluetooth = { ...(cfg.bluetooth || {}) }
}

watch(() => store.config, syncFromStore, { deep: true })

// Raw edit state (advanced)
const radioSection = ref('lora')
const radioJSON = ref('{}')
const radioLoading = ref(false)
const radioSaved = ref(false)
const moduleSection = ref('mqtt')
const moduleJSON = ref('{}')
const moduleLoading = ref(false)
const moduleSaved = ref(false)

// Waypoint
const wpName = ref('')
const wpDesc = ref('')
const wpLat = ref('')
const wpLon = ref('')
const wpIcon = ref(0)
const wpExpire = ref('')
const wpLoading = ref(false)
const wpSent = ref(false)

const RADIO_SECTIONS = ['lora', 'bluetooth', 'device', 'display', 'network', 'position', 'power']
const MODULE_SECTIONS = ['mqtt', 'serial', 'external_notification', 'store_forward', 'range_test', 'telemetry', 'canned_message']

// Friendly config field schemas
const loraFields = [
  { key: 'region', label: 'Region', type: 'select', tip: 'Frequency band for your region. Must match local regulations.',
    options: [
      { value: 'US', label: 'US (902-928 MHz)' }, { value: 'EU_868', label: 'EU 868 MHz' },
      { value: 'CN', label: 'CN (470-510 MHz)' }, { value: 'JP', label: 'JP (920 MHz)' },
      { value: 'ANZ', label: 'ANZ (915-928 MHz)' }, { value: 'KR', label: 'KR (920-923 MHz)' },
      { value: 'TW', label: 'TW (920-925 MHz)' }, { value: 'RU', label: 'RU (868 MHz)' },
      { value: 'IN', label: 'IN (865-867 MHz)' }, { value: 'NZ_865', label: 'NZ 865 MHz' },
      { value: 'TH', label: 'TH (920-925 MHz)' }, { value: 'LORA_24', label: '2.4 GHz' },
      { value: 'UA_433', label: 'UA 433 MHz' }, { value: 'UA_868', label: 'UA 868 MHz' },
      { value: 'UNSET', label: 'Unset' }
    ] },
  { key: 'modem_preset', label: 'Modem Preset', type: 'select', tip: 'Higher spreading factor = more range but slower. LONG_FAST is a good default.',
    options: [
      { value: 'LONG_FAST', label: 'Long Fast' }, { value: 'LONG_MODERATE', label: 'Long Moderate' },
      { value: 'LONG_SLOW', label: 'Long Slow' }, { value: 'VERY_LONG_SLOW', label: 'Very Long Slow' },
      { value: 'MEDIUM_SLOW', label: 'Medium Slow' }, { value: 'MEDIUM_FAST', label: 'Medium Fast' },
      { value: 'SHORT_SLOW', label: 'Short Slow' }, { value: 'SHORT_FAST', label: 'Short Fast' },
      { value: 'SHORT_TURBO', label: 'Short Turbo' }
    ] },
  { key: 'tx_power', label: 'TX Power (dBm)', type: 'number', min: 0, max: 30, tip: 'Higher = more range, more battery drain. 0 = use radio default.' },
  { key: 'hop_limit', label: 'Hop Limit', type: 'number', min: 1, max: 7, tip: 'Max number of hops a packet can take. 3 is typical for most meshes.' }
]

const deviceFields = [
  { key: 'long_name', label: 'Node Name', type: 'text', tip: 'Friendly name shown to other nodes in the mesh.' },
  { key: 'role', label: 'Role', type: 'select', tip: 'CLIENT: normal. ROUTER: rebroadcasts packets for other nodes. REPEATER: relay-only, minimal processing.',
    options: [
      { value: 'CLIENT', label: 'Client' }, { value: 'CLIENT_MUTE', label: 'Client Mute' },
      { value: 'ROUTER', label: 'Router' }, { value: 'ROUTER_CLIENT', label: 'Router Client' },
      { value: 'REPEATER', label: 'Repeater' }, { value: 'TRACKER', label: 'Tracker' },
      { value: 'SENSOR', label: 'Sensor' }, { value: 'TAK', label: 'TAK' },
      { value: 'TAK_TRACKER', label: 'TAK Tracker' }, { value: 'CLIENT_HIDDEN', label: 'Client Hidden' },
      { value: 'LOST_AND_FOUND', label: 'Lost and Found' }
    ] }
]

const positionFields = [
  { key: 'gps_enabled', label: 'GPS Enabled', type: 'checkbox', tip: 'Enable the built-in GPS module to broadcast position.' },
  { key: 'position_broadcast_secs', label: 'Update Interval (seconds)', type: 'number', min: 0, max: 86400, tip: 'How often to broadcast position. 0 = use default (15 min).' },
  { key: 'position_broadcast_smart_enabled', label: 'Smart Broadcast', type: 'checkbox', tip: 'Only broadcast when position changes significantly. Saves battery.' }
]

const powerFields = [
  { key: 'mesh_sds_timeout_secs', label: 'Mesh SDS Timeout (seconds)', type: 'number', min: 0, max: 604800, tip: 'Time before entering super deep sleep when no mesh activity. 0 = disabled.' },
  { key: 'ls_secs', label: 'Light Sleep (seconds)', type: 'number', min: 0, max: 604800, tip: 'Light sleep interval. Saves battery when no activity.' },
  { key: 'wait_bluetooth_secs', label: 'Wait Bluetooth (seconds)', type: 'number', min: 0, max: 3600, tip: 'How long to keep Bluetooth on after last connection. 0 = always on.' }
]

const displayFields = [
  { key: 'screen_on_secs', label: 'Screen On (seconds)', type: 'number', min: 0, max: 3600, tip: '0 = always on. How long the screen stays on after interaction.' },
  { key: 'auto_screen_carousel_secs', label: 'Auto Carousel (seconds)', type: 'number', min: 0, max: 600, tip: '0 = disabled. Automatically cycles through screen pages.' }
]

const bluetoothFields = [
  { key: 'enabled', label: 'Bluetooth Enabled', type: 'checkbox', tip: 'Disable if not pairing devices to save battery.' },
  { key: 'mode', label: 'Mode', type: 'select', tip: 'FIXED_PIN is more secure than RANDOM_PIN.',
    options: [
      { value: 'RANDOM_PIN', label: 'Random PIN' }, { value: 'FIXED_PIN', label: 'Fixed PIN' },
      { value: 'NO_PIN', label: 'No PIN' }
    ] },
  { key: 'fixed_pin', label: 'Fixed PIN', type: 'number', min: 100000, max: 999999, tip: 'Six-digit PIN for Bluetooth pairing when using Fixed PIN mode.' }
]

const configSections = [
  { title: 'LoRa Radio', key: 'lora', fields: loraFields },
  { title: 'Device', key: 'device', fields: deviceFields },
  { title: 'Position', key: 'position', fields: positionFields },
  { title: 'Power', key: 'power', fields: powerFields },
  { title: 'Display', key: 'display', fields: displayFields },
  { title: 'Bluetooth', key: 'bluetooth', fields: bluetoothFields }
]

function onSectionUpdate(sectionKey, newData) {
  editState[sectionKey] = newData
}

async function saveSection(sectionKey) {
  saving[sectionKey] = true
  saved[sectionKey] = false
  try {
    await store.configRadio({ section: sectionKey, config: editState[sectionKey] })
    saved[sectionKey] = true
    setTimeout(() => { saved[sectionKey] = false }, 3000)
    await store.fetchConfig()
  } catch { /* store error */ } finally {
    saving[sectionKey] = false
  }
}

function isValidJSON(s) {
  try { JSON.parse(s); return true } catch { return false }
}

async function handleRadio() {
  if (!isValidJSON(radioJSON.value)) return
  radioLoading.value = true
  try {
    await store.configRadio({ section: radioSection.value, config: JSON.parse(radioJSON.value) })
    radioSaved.value = true
    setTimeout(() => { radioSaved.value = false }, 3000)
  } catch { /* store error */ } finally {
    radioLoading.value = false
  }
}

async function handleModule() {
  if (!isValidJSON(moduleJSON.value)) return
  moduleLoading.value = true
  try {
    await store.configModule({ section: moduleSection.value, config: JSON.parse(moduleJSON.value) })
    moduleSaved.value = true
    setTimeout(() => { moduleSaved.value = false }, 3000)
  } catch { /* store error */ } finally {
    moduleLoading.value = false
  }
}

async function handleWaypoint() {
  if (!wpName.value.trim() || !wpLat.value || !wpLon.value) return
  wpLoading.value = true
  wpSent.value = false
  try {
    const payload = {
      name: wpName.value.trim(),
      description: wpDesc.value.trim() || undefined,
      latitude: Number(wpLat.value),
      longitude: Number(wpLon.value),
      icon: Number(wpIcon.value) || 0
    }
    if (wpExpire.value) payload.expire = Math.floor(new Date(wpExpire.value).getTime() / 1000)
    await store.sendWaypoint(payload)
    wpName.value = ''; wpDesc.value = ''; wpLat.value = ''; wpLon.value = ''
    wpSent.value = true
    setTimeout(() => { wpSent.value = false }, 3000)
  } catch { /* store error */ } finally {
    wpLoading.value = false
  }
}

onMounted(async () => {
  loading.value = true
  await store.fetchConfig()
  syncFromStore()
  loading.value = false
})
</script>

<template>
  <div class="max-w-3xl mx-auto space-y-6">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-bold">Configuration</h1>
      <div class="flex gap-2">
        <button
          @click="showRaw = !showRaw"
          class="px-3 py-1.5 text-xs rounded-lg transition-colors"
          :class="showRaw ? 'bg-teal-500/15 text-teal-400' : 'bg-gray-800 text-gray-400 hover:text-white'"
        >
          {{ showRaw ? 'Form View' : 'Advanced (JSON)' }}
        </button>
        <button
          @click="store.fetchConfig().then(syncFromStore)"
          class="px-3 py-1.5 text-sm rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors"
        >
          Refresh
        </button>
      </div>
    </div>

    <!-- Friendly form view (editable) -->
    <template v-if="!showRaw">
      <div v-if="loading" class="bg-gray-900 rounded-xl p-8 border border-gray-800 text-center text-gray-500">
        Loading configuration...
      </div>

      <div v-else-if="!store.config" class="bg-gray-900 rounded-xl p-8 border border-gray-800 text-center text-gray-500">
        No configuration data available. Connect a Meshtastic device to view settings.
      </div>

      <template v-else>
        <p class="text-xs text-gray-500">
          Edit settings and click Save to apply changes to the radio.
        </p>
        <div v-for="section in configSections" :key="section.key" class="space-y-2">
          <ConfigSection
            :title="section.title"
            :fields="section.fields"
            :model-value="editState[section.key]"
            @update:model-value="onSectionUpdate(section.key, $event)"
            :collapsed="true"
          />
          <div class="flex items-center gap-3 px-1">
            <button @click="saveSection(section.key)" :disabled="saving[section.key]"
              class="px-4 py-1.5 text-xs font-medium rounded-lg bg-teal-600 text-white hover:bg-teal-500 disabled:opacity-50 transition-colors">
              {{ saving[section.key] ? 'Saving...' : `Save ${section.title}` }}
            </button>
            <span v-if="saved[section.key]" class="text-xs text-teal-400">Applied</span>
          </div>
        </div>
      </template>
    </template>

    <!-- Raw edit view (advanced) -->
    <template v-else>
      <!-- Radio Config -->
      <div class="bg-gray-900 rounded-xl p-5 border border-gray-800">
        <h2 class="text-lg font-semibold mb-4">Radio Configuration</h2>
        <div class="space-y-4 max-w-lg">
          <div>
            <label class="block text-sm text-gray-400 mb-1">Section</label>
            <select v-model="radioSection"
                    class="w-full px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 focus:outline-none focus:ring-2 focus:ring-teal-500">
              <option v-for="s in RADIO_SECTIONS" :key="s" :value="s">{{ s }}</option>
            </select>
          </div>
          <div>
            <label class="block text-sm text-gray-400 mb-1">Config (JSON)</label>
            <textarea v-model="radioJSON" rows="4"
                      class="w-full px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 font-mono focus:outline-none focus:ring-2 focus:ring-teal-500"
                      :class="{ 'border-red-500': radioJSON && !isValidJSON(radioJSON) }" />
            <p v-if="radioJSON && !isValidJSON(radioJSON)" class="text-xs text-red-400 mt-1">Invalid JSON</p>
          </div>
          <button @click="handleRadio" :disabled="!isValidJSON(radioJSON) || radioLoading"
                  class="px-4 py-2 text-sm font-medium rounded-lg bg-teal-600 text-white hover:bg-teal-500 disabled:opacity-50 transition-colors">
            {{ radioLoading ? 'Applying...' : 'Apply' }}
          </button>
          <p v-if="radioSaved" class="text-xs text-teal-400">Radio config applied</p>
        </div>
      </div>

      <!-- Module Config -->
      <div class="bg-gray-900 rounded-xl p-5 border border-gray-800">
        <h2 class="text-lg font-semibold mb-4">Module Configuration</h2>
        <div class="space-y-4 max-w-lg">
          <div>
            <label class="block text-sm text-gray-400 mb-1">Module</label>
            <select v-model="moduleSection"
                    class="w-full px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 focus:outline-none focus:ring-2 focus:ring-teal-500">
              <option v-for="s in MODULE_SECTIONS" :key="s" :value="s">{{ s }}</option>
            </select>
          </div>
          <div>
            <label class="block text-sm text-gray-400 mb-1">Config (JSON)</label>
            <textarea v-model="moduleJSON" rows="4"
                      class="w-full px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 font-mono focus:outline-none focus:ring-2 focus:ring-teal-500"
                      :class="{ 'border-red-500': moduleJSON && !isValidJSON(moduleJSON) }" />
            <p v-if="moduleJSON && !isValidJSON(moduleJSON)" class="text-xs text-red-400 mt-1">Invalid JSON</p>
          </div>
          <button @click="handleModule" :disabled="!isValidJSON(moduleJSON) || moduleLoading"
                  class="px-4 py-2 text-sm font-medium rounded-lg bg-teal-600 text-white hover:bg-teal-500 disabled:opacity-50 transition-colors">
            {{ moduleLoading ? 'Applying...' : 'Apply' }}
          </button>
          <p v-if="moduleSaved" class="text-xs text-teal-400">Module config applied</p>
        </div>
      </div>
    </template>

    <!-- Waypoints (always visible) -->
    <div class="bg-gray-900 rounded-xl p-5 border border-gray-800">
      <h2 class="text-lg font-semibold mb-4">Send Waypoint</h2>
      <div class="space-y-4 max-w-lg">
        <input v-model="wpName" type="text" placeholder="Name"
               class="w-full px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-teal-500" />
        <input v-model="wpDesc" type="text" placeholder="Description (optional)"
               class="w-full px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-teal-500" />
        <div class="grid grid-cols-2 gap-4">
          <input v-model="wpLat" type="number" step="0.000001" placeholder="Latitude"
                 class="px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 font-mono focus:outline-none focus:ring-2 focus:ring-teal-500" />
          <input v-model="wpLon" type="number" step="0.000001" placeholder="Longitude"
                 class="px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 font-mono focus:outline-none focus:ring-2 focus:ring-teal-500" />
        </div>
        <div class="grid grid-cols-2 gap-4">
          <input v-model.number="wpIcon" type="number" min="0" max="255" placeholder="Icon (0-255)"
                 class="px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-teal-500" />
          <input v-model="wpExpire" type="datetime-local"
                 class="px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 focus:outline-none focus:ring-2 focus:ring-teal-500" />
        </div>
        <button @click="handleWaypoint" :disabled="!wpName.trim() || !wpLat || !wpLon || wpLoading"
                class="px-4 py-2 text-sm font-medium rounded-lg bg-teal-600 text-white hover:bg-teal-500 disabled:opacity-50 transition-colors">
          {{ wpLoading ? 'Sending...' : 'Send Waypoint' }}
        </button>
        <p v-if="wpSent" class="text-xs text-teal-400">Waypoint sent</p>
      </div>
    </div>
  </div>
</template>
