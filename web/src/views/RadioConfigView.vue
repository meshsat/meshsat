<script setup>
import { ref, reactive, onMounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()

// --- Collapsible sections ---
const openSections = ref({ identity: true })

function toggleSection(name) {
  if (openSections.value[name]) {
    delete openSections.value[name]
  } else {
    openSections.value[name] = true
  }
  openSections.value = { ...openSections.value }
}

// --- Save feedback ---
const saving = reactive({})
const saveResult = ref({})
let saveTimer = null

function showResult(section, ok, msg) {
  saveResult.value = { section, ok, msg }
  clearTimeout(saveTimer)
  saveTimer = setTimeout(() => { saveResult.value = {} }, 3000)
}

// --- Form state ---
const identity = reactive({ long_name: '', short_name: '' })
const lora = reactive({ region: 0, modem_preset: 0, hop_limit: 3, tx_power: 0, tx_enabled: true, override_duty_cycle: false, frequency_offset: 0 })
const channels = ref([])
const position = reactive({ gps_enabled: true, fixed_position: false, latitude: 0, longitude: 0, altitude: 0, broadcast_interval: 0, smart_position_enabled: false })
const power = reactive({ is_power_saving: false, on_battery_shutdown_after_secs: 0, adc_multiplier_override: 0, wait_bluetooth_secs: 0, sds_secs: 0, ls_secs: 0 })
const display = reactive({ screen_on_secs: 0, auto_screen_carousel_secs: 0, compass_north_top: false, flip_screen: false, units: 0 })
const bluetooth = reactive({ enabled: true, mode: 0, fixed_pin: 0 })
const network = reactive({ wifi_enabled: false, wifi_ssid: '', wifi_psk: '', ntp_server: '' })
const modules = reactive({
  telemetry: { device_update_interval: 0, environment_update_interval: 0, environment_measurement_enabled: false },
  store_forward: { enabled: false, heartbeat: false, records: 0, history_return_max: 0, history_return_window: 0 },
  range_test: { enabled: false, sender: 0, save: false },
  neighbor_info: { enabled: false, update_interval: 0 },
  external_notification: { enabled: false, output: 0, active: false }
})
const admin = reactive({ delay_secs: 5, factory_reset_confirm: false, remove_node_num: '' })

// --- Dropdown options ---
const regionOptions = [
  { value: 0, label: 'Unset' }, { value: 1, label: 'US' }, { value: 2, label: 'EU_433' },
  { value: 3, label: 'EU_868' }, { value: 4, label: 'CN' }, { value: 5, label: 'JP' },
  { value: 6, label: 'ANZ' }, { value: 7, label: 'KR' }, { value: 8, label: 'TW' },
  { value: 9, label: 'RU' }, { value: 10, label: 'IN' }, { value: 11, label: 'NZ_865' },
  { value: 12, label: 'TH' }, { value: 13, label: 'LORA_24' }, { value: 14, label: 'UA_433' },
  { value: 15, label: 'UA_868' }, { value: 16, label: 'MY_433' }, { value: 17, label: 'MY_919' },
  { value: 18, label: 'SG_923' }
]

const modemPresetOptions = [
  { value: 0, label: 'LONG_FAST' }, { value: 1, label: 'LONG_SLOW' },
  { value: 2, label: 'VERY_LONG_SLOW' }, { value: 3, label: 'MEDIUM_SLOW' },
  { value: 4, label: 'MEDIUM_FAST' }, { value: 5, label: 'SHORT_SLOW' },
  { value: 6, label: 'SHORT_FAST' }, { value: 7, label: 'LONG_MODERATE' }
]

const channelRoleOptions = [
  { value: 'DISABLED', label: 'Disabled' },
  { value: 'PRIMARY', label: 'Primary' },
  { value: 'SECONDARY', label: 'Secondary' }
]

const unitOptions = [
  { value: 0, label: 'Metric' }, { value: 1, label: 'Imperial' }
]

const btModeOptions = [
  { value: 0, label: 'Random PIN' }, { value: 1, label: 'Fixed PIN' }, { value: 2, label: 'No PIN' }
]

// --- Module sub-section collapsibles ---
const openModules = ref({ telemetry: true })

function toggleModule(name) {
  if (openModules.value[name]) {
    delete openModules.value[name]
  } else {
    openModules.value[name] = true
  }
  openModules.value = { ...openModules.value }
}

// --- Populate forms from store config ---
function populateFormsFromConfig() {
  const c = store.config
  if (!c) return

  if (c.device) {
    identity.long_name = c.device.long_name || ''
    identity.short_name = c.device.short_name || ''
  }

  if (c.lora) {
    Object.assign(lora, {
      region: c.lora.region ?? 0,
      modem_preset: c.lora.modem_preset ?? 0,
      hop_limit: c.lora.hop_limit ?? 3,
      tx_power: c.lora.tx_power ?? 0,
      tx_enabled: c.lora.tx_enabled ?? true,
      override_duty_cycle: c.lora.override_duty_cycle ?? false,
      frequency_offset: c.lora.frequency_offset ?? 0
    })
  }

  if (c.channels) {
    channels.value = c.channels.map((ch, i) => ({
      index: ch.index ?? i,
      name: ch.name || '',
      role: ch.role || 'DISABLED',
      psk: ch.psk || '',
      uplink_enabled: ch.uplink_enabled ?? false,
      downlink_enabled: ch.downlink_enabled ?? false
    }))
  }
  // Pad to 8 channels
  while (channels.value.length < 8) {
    channels.value.push({ index: channels.value.length, name: '', role: 'DISABLED', psk: '', uplink_enabled: false, downlink_enabled: false })
  }

  if (c.position) {
    Object.assign(position, {
      gps_enabled: c.position.gps_enabled ?? true,
      fixed_position: c.position.fixed_position ?? false,
      latitude: c.position.latitude ?? 0,
      longitude: c.position.longitude ?? 0,
      altitude: c.position.altitude ?? 0,
      broadcast_interval: c.position.broadcast_interval ?? 0,
      smart_position_enabled: c.position.smart_position_enabled ?? false
    })
  }

  if (c.power) {
    Object.assign(power, {
      is_power_saving: c.power.is_power_saving ?? false,
      on_battery_shutdown_after_secs: c.power.on_battery_shutdown_after_secs ?? 0,
      adc_multiplier_override: c.power.adc_multiplier_override ?? 0,
      wait_bluetooth_secs: c.power.wait_bluetooth_secs ?? 0,
      sds_secs: c.power.sds_secs ?? 0,
      ls_secs: c.power.ls_secs ?? 0
    })
  }

  if (c.display) {
    Object.assign(display, {
      screen_on_secs: c.display.screen_on_secs ?? 0,
      auto_screen_carousel_secs: c.display.auto_screen_carousel_secs ?? 0,
      compass_north_top: c.display.compass_north_top ?? false,
      flip_screen: c.display.flip_screen ?? false,
      units: c.display.units ?? 0
    })
  }

  if (c.bluetooth) {
    Object.assign(bluetooth, {
      enabled: c.bluetooth.enabled ?? true,
      mode: c.bluetooth.mode ?? 0,
      fixed_pin: c.bluetooth.fixed_pin ?? 0
    })
  }

  if (c.network) {
    Object.assign(network, {
      wifi_enabled: c.network.wifi_enabled ?? false,
      wifi_ssid: c.network.wifi_ssid || '',
      wifi_psk: c.network.wifi_psk || '',
      ntp_server: c.network.ntp_server || ''
    })
  }

  if (c.modules) {
    if (c.modules.telemetry) {
      Object.assign(modules.telemetry, {
        device_update_interval: c.modules.telemetry.device_update_interval ?? 0,
        environment_update_interval: c.modules.telemetry.environment_update_interval ?? 0,
        environment_measurement_enabled: c.modules.telemetry.environment_measurement_enabled ?? false
      })
    }
    if (c.modules.store_forward) {
      Object.assign(modules.store_forward, {
        enabled: c.modules.store_forward.enabled ?? false,
        heartbeat: c.modules.store_forward.heartbeat ?? false,
        records: c.modules.store_forward.records ?? 0,
        history_return_max: c.modules.store_forward.history_return_max ?? 0,
        history_return_window: c.modules.store_forward.history_return_window ?? 0
      })
    }
    if (c.modules.range_test) {
      Object.assign(modules.range_test, {
        enabled: c.modules.range_test.enabled ?? false,
        sender: c.modules.range_test.sender ?? 0,
        save: c.modules.range_test.save ?? false
      })
    }
    if (c.modules.neighbor_info) {
      Object.assign(modules.neighbor_info, {
        enabled: c.modules.neighbor_info.enabled ?? false,
        update_interval: c.modules.neighbor_info.update_interval ?? 0
      })
    }
    if (c.modules.external_notification) {
      Object.assign(modules.external_notification, {
        enabled: c.modules.external_notification.enabled ?? false,
        output: c.modules.external_notification.output ?? 0,
        active: c.modules.external_notification.active ?? false
      })
    }
  }
}

// --- Hex format helper ---
function nodeIdHex(id) {
  if (!id) return '—'
  return '!' + id.toString(16).padStart(8, '0')
}

// --- Generate random PSK ---
function generatePsk(channelIndex) {
  const bytes = new Uint8Array(32)
  crypto.getRandomValues(bytes)
  const binStr = Array.from(bytes).map(b => String.fromCharCode(b)).join('')
  channels.value[channelIndex].psk = btoa(binStr)
}

// --- Save handlers ---
async function saveIdentity() {
  saving.identity = true
  try {
    await store.setOwner({ long_name: identity.long_name, short_name: identity.short_name })
    showResult('identity', true, 'Saved')
  } catch (e) {
    showResult('identity', false, e.message || 'Failed')
  }
  saving.identity = false
}

async function saveLora() {
  saving.lora = true
  try {
    await store.configRadio({ section: 'lora', config: { ...lora } })
    showResult('lora', true, 'Saved')
  } catch (e) {
    showResult('lora', false, e.message || 'Failed')
  }
  saving.lora = false
}

async function saveChannel(ch) {
  const key = `channel_${ch.index}`
  saving[key] = true
  try {
    await store.setChannel({
      index: ch.index,
      name: ch.name,
      role: ch.role,
      psk: ch.psk,
      uplink_enabled: ch.uplink_enabled,
      downlink_enabled: ch.downlink_enabled
    })
    showResult(key, true, 'Saved')
  } catch (e) {
    showResult(key, false, e.message || 'Failed')
  }
  saving[key] = false
}

async function savePosition() {
  saving.position = true
  try {
    await store.configRadio({
      section: 'position',
      config: {
        gps_enabled: position.gps_enabled,
        fixed_position: position.fixed_position,
        broadcast_interval: position.broadcast_interval,
        smart_position_enabled: position.smart_position_enabled
      }
    })
    showResult('position', true, 'Saved')
  } catch (e) {
    showResult('position', false, e.message || 'Failed')
  }
  saving.position = false
}

async function setFixed() {
  saving.fixed = true
  try {
    await store.setFixedPosition({ latitude: position.latitude, longitude: position.longitude, altitude: position.altitude })
    showResult('fixed', true, 'Fixed position set')
  } catch (e) {
    showResult('fixed', false, e.message || 'Failed')
  }
  saving.fixed = false
}

async function removeFixed() {
  saving.fixed = true
  try {
    await store.removeFixedPosition()
    position.fixed_position = false
    showResult('fixed', true, 'Fixed position removed')
  } catch (e) {
    showResult('fixed', false, e.message || 'Failed')
  }
  saving.fixed = false
}

async function savePower() {
  saving.power = true
  try {
    await store.configRadio({ section: 'power', config: { ...power } })
    showResult('power', true, 'Saved')
  } catch (e) {
    showResult('power', false, e.message || 'Failed')
  }
  saving.power = false
}

async function saveDisplay() {
  saving.display = true
  try {
    await store.configRadio({ section: 'display', config: { ...display } })
    showResult('display', true, 'Saved')
  } catch (e) {
    showResult('display', false, e.message || 'Failed')
  }
  saving.display = false
}

async function saveBluetooth() {
  saving.bluetooth = true
  try {
    await store.configRadio({ section: 'bluetooth', config: { ...bluetooth } })
    showResult('bluetooth', true, 'Saved')
  } catch (e) {
    showResult('bluetooth', false, e.message || 'Failed')
  }
  saving.bluetooth = false
}

async function saveNetwork() {
  saving.network = true
  try {
    await store.configRadio({ section: 'network', config: { ...network } })
    showResult('network', true, 'Saved')
  } catch (e) {
    showResult('network', false, e.message || 'Failed')
  }
  saving.network = false
}

async function saveModule(section) {
  const key = `mod_${section}`
  saving[key] = true
  try {
    await store.configModule({ section, config: { ...modules[section] } })
    showResult(key, true, 'Saved')
  } catch (e) {
    showResult(key, false, e.message || 'Failed')
  }
  saving[key] = false
}

async function doReboot() {
  saving.reboot = true
  try {
    const nodeId = store.status?.node_id || 0
    await store.adminReboot({ node_id: nodeId, delay_secs: admin.delay_secs })
    showResult('reboot', true, 'Reboot command sent')
  } catch (e) {
    showResult('reboot', false, e.message || 'Failed')
  }
  saving.reboot = false
}

async function doFactoryReset() {
  if (!admin.factory_reset_confirm) return
  saving.factory_reset = true
  try {
    const nodeId = store.status?.node_id || 0
    await store.adminFactoryReset({ node_id: nodeId })
    showResult('factory_reset', true, 'Factory reset command sent')
    admin.factory_reset_confirm = false
  } catch (e) {
    showResult('factory_reset', false, e.message || 'Failed')
  }
  saving.factory_reset = false
}

async function doRemoveNode() {
  const num = parseInt(admin.remove_node_num)
  if (!num) return
  saving.remove_node = true
  try {
    await store.removeNode(num)
    showResult('remove_node', true, 'Node removed')
    admin.remove_node_num = ''
  } catch (e) {
    showResult('remove_node', false, e.message || 'Failed')
  }
  saving.remove_node = false
}

// --- Lifecycle ---
onMounted(async () => {
  await store.fetchStatus()
  await store.fetchConfig()
  if (store.config) {
    populateFormsFromConfig()
  }
})
</script>

<template>
  <div class="min-h-screen bg-gray-900 p-4 sm:p-6 space-y-3">
    <h1 class="text-lg font-semibold text-gray-100 mb-4">Radio Configuration</h1>

    <!-- Save result toast -->
    <Transition name="fade">
      <div v-if="saveResult.section"
        class="fixed top-4 right-4 z-50 px-4 py-2 rounded-lg text-sm font-medium shadow-lg"
        :class="saveResult.ok ? 'bg-teal-600 text-white' : 'bg-red-600 text-white'">
        {{ saveResult.msg }}
      </div>
    </Transition>

    <!-- Section 1: Device Identity -->
    <div class="bg-gray-800 rounded-xl border border-gray-700 overflow-hidden">
      <button @click="toggleSection('identity')" class="w-full flex items-center justify-between p-4 text-left hover:bg-gray-700/50 transition-colors">
        <span class="text-sm font-medium text-gray-200">Device Identity</span>
        <svg class="w-4 h-4 text-gray-400 transition-transform" :class="{ 'rotate-180': openSections.identity }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
      </button>
      <div v-if="openSections.identity" class="border-t border-gray-700 p-4 space-y-4">
        <div>
          <label class="block text-xs text-gray-400 mb-1">Long Name</label>
          <input v-model="identity.long_name" type="text" maxlength="39"
            class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
        </div>
        <div>
          <label class="block text-xs text-gray-400 mb-1">Short Name</label>
          <input v-model="identity.short_name" type="text" maxlength="4"
            class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
        </div>
        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="block text-xs text-gray-400 mb-1">HW Model</label>
            <div class="text-sm text-gray-300">{{ store.status?.hw_model_name || store.status?.hw_model || '—' }}</div>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Node ID</label>
            <div class="text-sm text-teal-400 font-mono">{{ nodeIdHex(store.status?.node_id) }}</div>
          </div>
        </div>
        <button @click="saveIdentity" :disabled="saving.identity"
          class="px-4 py-1.5 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-sm font-medium text-white">
          {{ saving.identity ? 'Saving...' : 'Save' }}
        </button>
      </div>
    </div>

    <!-- Section 2: LoRa Radio -->
    <div class="bg-gray-800 rounded-xl border border-gray-700 overflow-hidden">
      <button @click="toggleSection('lora')" class="w-full flex items-center justify-between p-4 text-left hover:bg-gray-700/50 transition-colors">
        <span class="text-sm font-medium text-gray-200">LoRa Radio</span>
        <svg class="w-4 h-4 text-gray-400 transition-transform" :class="{ 'rotate-180': openSections.lora }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
      </button>
      <div v-if="openSections.lora" class="border-t border-gray-700 p-4 space-y-4">
        <div>
          <label class="block text-xs text-gray-400 mb-1">Region</label>
          <select v-model="lora.region" class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none">
            <option v-for="opt in regionOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
        </div>
        <div>
          <label class="block text-xs text-gray-400 mb-1">Modem Preset</label>
          <select v-model="lora.modem_preset" class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none">
            <option v-for="opt in modemPresetOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
        </div>
        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="block text-xs text-gray-400 mb-1">Hop Limit (1-7)</label>
            <input v-model.number="lora.hop_limit" type="number" min="1" max="7"
              class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">TX Power (0=default)</label>
            <input v-model.number="lora.tx_power" type="number" min="0" max="30"
              class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
        </div>
        <div>
          <label class="block text-xs text-gray-400 mb-1">Frequency Offset</label>
          <input v-model.number="lora.frequency_offset" type="number"
            class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
        </div>
        <div class="flex flex-wrap gap-4">
          <label class="flex items-center gap-2">
            <input v-model="lora.tx_enabled" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
            <span class="text-sm text-gray-300">TX Enabled</span>
          </label>
          <label class="flex items-center gap-2">
            <input v-model="lora.override_duty_cycle" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
            <span class="text-sm text-gray-300">Override Duty Cycle</span>
          </label>
        </div>
        <button @click="saveLora" :disabled="saving.lora"
          class="px-4 py-1.5 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-sm font-medium text-white">
          {{ saving.lora ? 'Saving...' : 'Save' }}
        </button>
      </div>
    </div>

    <!-- Section 3: Channels -->
    <div class="bg-gray-800 rounded-xl border border-gray-700 overflow-hidden">
      <button @click="toggleSection('channels')" class="w-full flex items-center justify-between p-4 text-left hover:bg-gray-700/50 transition-colors">
        <span class="text-sm font-medium text-gray-200">Channels</span>
        <svg class="w-4 h-4 text-gray-400 transition-transform" :class="{ 'rotate-180': openSections.channels }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
      </button>
      <div v-if="openSections.channels" class="border-t border-gray-700 p-4 space-y-4">
        <div v-for="ch in channels" :key="ch.index" class="bg-gray-900 rounded-lg border border-gray-700 p-3 space-y-3">
          <div class="flex items-center gap-3">
            <span class="text-xs font-mono text-gray-500 w-6">{{ ch.index }}</span>
            <div class="flex-1">
              <input v-model="ch.name" type="text" placeholder="Channel name"
                class="w-full bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
            <select v-model="ch.role" class="bg-gray-800 border border-gray-600 rounded px-2 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none">
              <option v-for="opt in channelRoleOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
            </select>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">PSK (Base64)</label>
            <div class="flex gap-2">
              <input v-model="ch.psk" type="text" placeholder="Pre-shared key"
                class="flex-1 bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 font-mono focus:border-teal-500 focus:outline-none" />
              <button @click="generatePsk(ch.index)" class="px-3 py-1.5 bg-gray-700 hover:bg-gray-600 rounded text-xs text-gray-300" title="Generate random PSK">
                Random
              </button>
            </div>
          </div>
          <div class="flex flex-wrap gap-4">
            <label class="flex items-center gap-2">
              <input v-model="ch.uplink_enabled" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
              <span class="text-xs text-gray-300">Uplink</span>
            </label>
            <label class="flex items-center gap-2">
              <input v-model="ch.downlink_enabled" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
              <span class="text-xs text-gray-300">Downlink</span>
            </label>
          </div>
          <button @click="saveChannel(ch)" :disabled="saving[`channel_${ch.index}`]"
            class="px-3 py-1 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-xs font-medium text-white">
            {{ saving[`channel_${ch.index}`] ? 'Saving...' : 'Save Channel' }}
          </button>
        </div>
      </div>
    </div>

    <!-- Section 4: Position -->
    <div class="bg-gray-800 rounded-xl border border-gray-700 overflow-hidden">
      <button @click="toggleSection('position')" class="w-full flex items-center justify-between p-4 text-left hover:bg-gray-700/50 transition-colors">
        <span class="text-sm font-medium text-gray-200">Position</span>
        <svg class="w-4 h-4 text-gray-400 transition-transform" :class="{ 'rotate-180': openSections.position }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
      </button>
      <div v-if="openSections.position" class="border-t border-gray-700 p-4 space-y-4">
        <div class="flex flex-wrap gap-4">
          <label class="flex items-center gap-2">
            <input v-model="position.gps_enabled" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
            <span class="text-sm text-gray-300">GPS Enabled</span>
          </label>
          <label class="flex items-center gap-2">
            <input v-model="position.fixed_position" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
            <span class="text-sm text-gray-300">Fixed Position</span>
          </label>
          <label class="flex items-center gap-2">
            <input v-model="position.smart_position_enabled" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
            <span class="text-sm text-gray-300">Smart Position</span>
          </label>
        </div>
        <div v-if="position.fixed_position" class="grid grid-cols-3 gap-3">
          <div>
            <label class="block text-xs text-gray-400 mb-1">Latitude</label>
            <input v-model.number="position.latitude" type="number" step="0.000001"
              class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Longitude</label>
            <input v-model.number="position.longitude" type="number" step="0.000001"
              class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Altitude (m)</label>
            <input v-model.number="position.altitude" type="number"
              class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
        </div>
        <div>
          <label class="block text-xs text-gray-400 mb-1">Broadcast Interval (seconds)</label>
          <input v-model.number="position.broadcast_interval" type="number" min="0"
            class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
        </div>
        <div class="flex flex-wrap gap-2">
          <button @click="savePosition" :disabled="saving.position"
            class="px-4 py-1.5 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-sm font-medium text-white">
            {{ saving.position ? 'Saving...' : 'Save' }}
          </button>
          <button v-if="position.fixed_position" @click="setFixed" :disabled="saving.fixed"
            class="px-4 py-1.5 bg-gray-700 hover:bg-gray-600 disabled:opacity-50 rounded text-sm font-medium text-gray-200">
            {{ saving.fixed ? 'Setting...' : 'Set Fixed Position' }}
          </button>
          <button v-if="position.fixed_position" @click="removeFixed" :disabled="saving.fixed"
            class="px-4 py-1.5 bg-gray-700 hover:bg-gray-600 disabled:opacity-50 rounded text-sm font-medium text-gray-200">
            Remove Fixed Position
          </button>
        </div>
      </div>
    </div>

    <!-- Section 5: Power -->
    <div class="bg-gray-800 rounded-xl border border-gray-700 overflow-hidden">
      <button @click="toggleSection('power')" class="w-full flex items-center justify-between p-4 text-left hover:bg-gray-700/50 transition-colors">
        <span class="text-sm font-medium text-gray-200">Power</span>
        <svg class="w-4 h-4 text-gray-400 transition-transform" :class="{ 'rotate-180': openSections.power }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
      </button>
      <div v-if="openSections.power" class="border-t border-gray-700 p-4 space-y-4">
        <label class="flex items-center gap-2">
          <input v-model="power.is_power_saving" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
          <span class="text-sm text-gray-300">Power Saving Mode</span>
        </label>
        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="block text-xs text-gray-400 mb-1">Shutdown After (secs, on battery)</label>
            <input v-model.number="power.on_battery_shutdown_after_secs" type="number" min="0"
              class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">ADC Multiplier Override</label>
            <input v-model.number="power.adc_multiplier_override" type="number" step="0.01" min="0"
              class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
        </div>
        <div class="grid grid-cols-3 gap-4">
          <div>
            <label class="block text-xs text-gray-400 mb-1">Wait Bluetooth (secs)</label>
            <input v-model.number="power.wait_bluetooth_secs" type="number" min="0"
              class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Light Sleep (secs)</label>
            <input v-model.number="power.sds_secs" type="number" min="0"
              class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Deep Sleep (secs)</label>
            <input v-model.number="power.ls_secs" type="number" min="0"
              class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
        </div>
        <button @click="savePower" :disabled="saving.power"
          class="px-4 py-1.5 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-sm font-medium text-white">
          {{ saving.power ? 'Saving...' : 'Save' }}
        </button>
      </div>
    </div>

    <!-- Section 6: Display -->
    <div class="bg-gray-800 rounded-xl border border-gray-700 overflow-hidden">
      <button @click="toggleSection('display')" class="w-full flex items-center justify-between p-4 text-left hover:bg-gray-700/50 transition-colors">
        <span class="text-sm font-medium text-gray-200">Display</span>
        <svg class="w-4 h-4 text-gray-400 transition-transform" :class="{ 'rotate-180': openSections.display }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
      </button>
      <div v-if="openSections.display" class="border-t border-gray-700 p-4 space-y-4">
        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="block text-xs text-gray-400 mb-1">Screen On (secs)</label>
            <input v-model.number="display.screen_on_secs" type="number" min="0"
              class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Carousel Interval (secs)</label>
            <input v-model.number="display.auto_screen_carousel_secs" type="number" min="0"
              class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
          </div>
        </div>
        <div>
          <label class="block text-xs text-gray-400 mb-1">Units</label>
          <select v-model="display.units" class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none">
            <option v-for="opt in unitOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
        </div>
        <div class="flex flex-wrap gap-4">
          <label class="flex items-center gap-2">
            <input v-model="display.compass_north_top" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
            <span class="text-sm text-gray-300">Compass North Top</span>
          </label>
          <label class="flex items-center gap-2">
            <input v-model="display.flip_screen" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
            <span class="text-sm text-gray-300">Flip Screen</span>
          </label>
        </div>
        <button @click="saveDisplay" :disabled="saving.display"
          class="px-4 py-1.5 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-sm font-medium text-white">
          {{ saving.display ? 'Saving...' : 'Save' }}
        </button>
      </div>
    </div>

    <!-- Section 7: Bluetooth -->
    <div class="bg-gray-800 rounded-xl border border-gray-700 overflow-hidden">
      <button @click="toggleSection('bluetooth')" class="w-full flex items-center justify-between p-4 text-left hover:bg-gray-700/50 transition-colors">
        <span class="text-sm font-medium text-gray-200">Bluetooth</span>
        <svg class="w-4 h-4 text-gray-400 transition-transform" :class="{ 'rotate-180': openSections.bluetooth }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
      </button>
      <div v-if="openSections.bluetooth" class="border-t border-gray-700 p-4 space-y-4">
        <label class="flex items-center gap-2">
          <input v-model="bluetooth.enabled" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
          <span class="text-sm text-gray-300">Enabled</span>
        </label>
        <div>
          <label class="block text-xs text-gray-400 mb-1">Mode</label>
          <select v-model="bluetooth.mode" class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none">
            <option v-for="opt in btModeOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
        </div>
        <div v-if="bluetooth.mode === 1">
          <label class="block text-xs text-gray-400 mb-1">Fixed PIN</label>
          <input v-model.number="bluetooth.fixed_pin" type="number" min="0" max="999999"
            class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
        </div>
        <button @click="saveBluetooth" :disabled="saving.bluetooth"
          class="px-4 py-1.5 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-sm font-medium text-white">
          {{ saving.bluetooth ? 'Saving...' : 'Save' }}
        </button>
      </div>
    </div>

    <!-- Section 8: Network -->
    <div class="bg-gray-800 rounded-xl border border-gray-700 overflow-hidden">
      <button @click="toggleSection('network')" class="w-full flex items-center justify-between p-4 text-left hover:bg-gray-700/50 transition-colors">
        <span class="text-sm font-medium text-gray-200">Network</span>
        <svg class="w-4 h-4 text-gray-400 transition-transform" :class="{ 'rotate-180': openSections.network }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
      </button>
      <div v-if="openSections.network" class="border-t border-gray-700 p-4 space-y-4">
        <label class="flex items-center gap-2">
          <input v-model="network.wifi_enabled" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
          <span class="text-sm text-gray-300">WiFi Enabled</span>
        </label>
        <div>
          <label class="block text-xs text-gray-400 mb-1">WiFi SSID</label>
          <input v-model="network.wifi_ssid" type="text"
            class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
        </div>
        <div>
          <label class="block text-xs text-gray-400 mb-1">WiFi Password</label>
          <input v-model="network.wifi_psk" type="password"
            class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
        </div>
        <div>
          <label class="block text-xs text-gray-400 mb-1">NTP Server</label>
          <input v-model="network.ntp_server" type="text" placeholder="pool.ntp.org"
            class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
        </div>
        <button @click="saveNetwork" :disabled="saving.network"
          class="px-4 py-1.5 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-sm font-medium text-white">
          {{ saving.network ? 'Saving...' : 'Save' }}
        </button>
      </div>
    </div>

    <!-- Section 9: Modules -->
    <div class="bg-gray-800 rounded-xl border border-gray-700 overflow-hidden">
      <button @click="toggleSection('modules')" class="w-full flex items-center justify-between p-4 text-left hover:bg-gray-700/50 transition-colors">
        <span class="text-sm font-medium text-gray-200">Modules</span>
        <svg class="w-4 h-4 text-gray-400 transition-transform" :class="{ 'rotate-180': openSections.modules }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
      </button>
      <div v-if="openSections.modules" class="border-t border-gray-700 p-4 space-y-3">

        <!-- Telemetry -->
        <div class="bg-gray-900 rounded-lg border border-gray-700 overflow-hidden">
          <button @click="toggleModule('telemetry')" class="w-full flex items-center justify-between p-3 text-left hover:bg-gray-800/50 transition-colors">
            <span class="text-xs font-medium text-gray-300">Telemetry</span>
            <svg class="w-3 h-3 text-gray-500 transition-transform" :class="{ 'rotate-180': openModules.telemetry }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
          </button>
          <div v-if="openModules.telemetry" class="border-t border-gray-700 p-3 space-y-3">
            <div class="grid grid-cols-2 gap-3">
              <div>
                <label class="block text-xs text-gray-400 mb-1">Device Update Interval (s)</label>
                <input v-model.number="modules.telemetry.device_update_interval" type="number" min="0"
                  class="w-full bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
              </div>
              <div>
                <label class="block text-xs text-gray-400 mb-1">Environment Update Interval (s)</label>
                <input v-model.number="modules.telemetry.environment_update_interval" type="number" min="0"
                  class="w-full bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
              </div>
            </div>
            <label class="flex items-center gap-2">
              <input v-model="modules.telemetry.environment_measurement_enabled" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
              <span class="text-xs text-gray-300">Environment Measurement Enabled</span>
            </label>
            <button @click="saveModule('telemetry')" :disabled="saving.mod_telemetry"
              class="px-3 py-1 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-xs font-medium text-white">
              {{ saving.mod_telemetry ? 'Saving...' : 'Save' }}
            </button>
          </div>
        </div>

        <!-- Store & Forward -->
        <div class="bg-gray-900 rounded-lg border border-gray-700 overflow-hidden">
          <button @click="toggleModule('store_forward')" class="w-full flex items-center justify-between p-3 text-left hover:bg-gray-800/50 transition-colors">
            <span class="text-xs font-medium text-gray-300">Store & Forward</span>
            <svg class="w-3 h-3 text-gray-500 transition-transform" :class="{ 'rotate-180': openModules.store_forward }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
          </button>
          <div v-if="openModules.store_forward" class="border-t border-gray-700 p-3 space-y-3">
            <label class="flex items-center gap-2">
              <input v-model="modules.store_forward.enabled" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
              <span class="text-xs text-gray-300">Enabled</span>
            </label>
            <label class="flex items-center gap-2">
              <input v-model="modules.store_forward.heartbeat" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
              <span class="text-xs text-gray-300">Heartbeat</span>
            </label>
            <div class="grid grid-cols-3 gap-3">
              <div>
                <label class="block text-xs text-gray-400 mb-1">Records</label>
                <input v-model.number="modules.store_forward.records" type="number" min="0"
                  class="w-full bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
              </div>
              <div>
                <label class="block text-xs text-gray-400 mb-1">History Return Max</label>
                <input v-model.number="modules.store_forward.history_return_max" type="number" min="0"
                  class="w-full bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
              </div>
              <div>
                <label class="block text-xs text-gray-400 mb-1">History Window (s)</label>
                <input v-model.number="modules.store_forward.history_return_window" type="number" min="0"
                  class="w-full bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
              </div>
            </div>
            <button @click="saveModule('store_forward')" :disabled="saving.mod_store_forward"
              class="px-3 py-1 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-xs font-medium text-white">
              {{ saving.mod_store_forward ? 'Saving...' : 'Save' }}
            </button>
          </div>
        </div>

        <!-- Range Test -->
        <div class="bg-gray-900 rounded-lg border border-gray-700 overflow-hidden">
          <button @click="toggleModule('range_test')" class="w-full flex items-center justify-between p-3 text-left hover:bg-gray-800/50 transition-colors">
            <span class="text-xs font-medium text-gray-300">Range Test</span>
            <svg class="w-3 h-3 text-gray-500 transition-transform" :class="{ 'rotate-180': openModules.range_test }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
          </button>
          <div v-if="openModules.range_test" class="border-t border-gray-700 p-3 space-y-3">
            <label class="flex items-center gap-2">
              <input v-model="modules.range_test.enabled" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
              <span class="text-xs text-gray-300">Enabled</span>
            </label>
            <div>
              <label class="block text-xs text-gray-400 mb-1">Sender (interval secs, 0=receiver)</label>
              <input v-model.number="modules.range_test.sender" type="number" min="0"
                class="w-full bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
            <label class="flex items-center gap-2">
              <input v-model="modules.range_test.save" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
              <span class="text-xs text-gray-300">Save to CSV</span>
            </label>
            <button @click="saveModule('range_test')" :disabled="saving.mod_range_test"
              class="px-3 py-1 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-xs font-medium text-white">
              {{ saving.mod_range_test ? 'Saving...' : 'Save' }}
            </button>
          </div>
        </div>

        <!-- Neighbor Info -->
        <div class="bg-gray-900 rounded-lg border border-gray-700 overflow-hidden">
          <button @click="toggleModule('neighbor_info')" class="w-full flex items-center justify-between p-3 text-left hover:bg-gray-800/50 transition-colors">
            <span class="text-xs font-medium text-gray-300">Neighbor Info</span>
            <svg class="w-3 h-3 text-gray-500 transition-transform" :class="{ 'rotate-180': openModules.neighbor_info }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
          </button>
          <div v-if="openModules.neighbor_info" class="border-t border-gray-700 p-3 space-y-3">
            <label class="flex items-center gap-2">
              <input v-model="modules.neighbor_info.enabled" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
              <span class="text-xs text-gray-300">Enabled</span>
            </label>
            <div>
              <label class="block text-xs text-gray-400 mb-1">Update Interval (s)</label>
              <input v-model.number="modules.neighbor_info.update_interval" type="number" min="0"
                class="w-full bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
            <button @click="saveModule('neighbor_info')" :disabled="saving.mod_neighbor_info"
              class="px-3 py-1 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-xs font-medium text-white">
              {{ saving.mod_neighbor_info ? 'Saving...' : 'Save' }}
            </button>
          </div>
        </div>

        <!-- External Notification -->
        <div class="bg-gray-900 rounded-lg border border-gray-700 overflow-hidden">
          <button @click="toggleModule('external_notification')" class="w-full flex items-center justify-between p-3 text-left hover:bg-gray-800/50 transition-colors">
            <span class="text-xs font-medium text-gray-300">External Notification</span>
            <svg class="w-3 h-3 text-gray-500 transition-transform" :class="{ 'rotate-180': openModules.external_notification }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
          </button>
          <div v-if="openModules.external_notification" class="border-t border-gray-700 p-3 space-y-3">
            <label class="flex items-center gap-2">
              <input v-model="modules.external_notification.enabled" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
              <span class="text-xs text-gray-300">Enabled</span>
            </label>
            <div>
              <label class="block text-xs text-gray-400 mb-1">Output GPIO Pin</label>
              <input v-model.number="modules.external_notification.output" type="number" min="0"
                class="w-full bg-gray-800 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
            <label class="flex items-center gap-2">
              <input v-model="modules.external_notification.active" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-teal-500 focus:ring-teal-500" />
              <span class="text-xs text-gray-300">Active (buzzer/LED on)</span>
            </label>
            <button @click="saveModule('external_notification')" :disabled="saving.mod_external_notification"
              class="px-3 py-1 bg-teal-600 hover:bg-teal-500 disabled:opacity-50 rounded text-xs font-medium text-white">
              {{ saving.mod_external_notification ? 'Saving...' : 'Save' }}
            </button>
          </div>
        </div>

      </div>
    </div>

    <!-- Section 10: Admin -->
    <div class="bg-gray-800 rounded-xl border border-gray-700 overflow-hidden">
      <button @click="toggleSection('admin')" class="w-full flex items-center justify-between p-4 text-left hover:bg-gray-700/50 transition-colors">
        <span class="text-sm font-medium text-gray-200">Admin</span>
        <svg class="w-4 h-4 text-gray-400 transition-transform" :class="{ 'rotate-180': openSections.admin }" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
      </button>
      <div v-if="openSections.admin" class="border-t border-gray-700 p-4 space-y-6">

        <!-- Reboot -->
        <div class="space-y-3">
          <h4 class="text-xs font-semibold text-gray-400 uppercase tracking-wider">Reboot</h4>
          <div class="flex items-end gap-3">
            <div class="flex-1">
              <label class="block text-xs text-gray-400 mb-1">Delay (seconds)</label>
              <input v-model.number="admin.delay_secs" type="number" min="0" max="300"
                class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
            <button @click="doReboot" :disabled="saving.reboot"
              class="px-4 py-1.5 bg-amber-600 hover:bg-amber-500 disabled:opacity-50 rounded text-sm font-medium text-white">
              {{ saving.reboot ? 'Rebooting...' : 'Reboot' }}
            </button>
          </div>
        </div>

        <!-- Factory Reset -->
        <div class="space-y-3 border-t border-gray-700 pt-4">
          <h4 class="text-xs font-semibold text-red-400 uppercase tracking-wider">Factory Reset</h4>
          <p class="text-xs text-gray-500">This will erase all settings on the radio and restore factory defaults.</p>
          <label class="flex items-center gap-2">
            <input v-model="admin.factory_reset_confirm" type="checkbox" class="rounded border-gray-600 bg-gray-900 text-red-500 focus:ring-red-500" />
            <span class="text-sm text-gray-300">I understand this will erase all radio settings</span>
          </label>
          <button @click="doFactoryReset" :disabled="!admin.factory_reset_confirm || saving.factory_reset"
            class="px-4 py-1.5 bg-red-600 hover:bg-red-500 disabled:opacity-50 disabled:cursor-not-allowed rounded text-sm font-medium text-white">
            {{ saving.factory_reset ? 'Resetting...' : 'Factory Reset' }}
          </button>
        </div>

        <!-- Remove Node -->
        <div class="space-y-3 border-t border-gray-700 pt-4">
          <h4 class="text-xs font-semibold text-gray-400 uppercase tracking-wider">Remove Node</h4>
          <div class="flex items-end gap-3">
            <div class="flex-1">
              <label class="block text-xs text-gray-400 mb-1">Node Number</label>
              <input v-model="admin.remove_node_num" type="text" placeholder="Node number (decimal)"
                class="w-full bg-gray-900 border border-gray-600 rounded px-3 py-1.5 text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
            <button @click="doRemoveNode" :disabled="!admin.remove_node_num || saving.remove_node"
              class="px-4 py-1.5 bg-red-600 hover:bg-red-500 disabled:opacity-50 disabled:cursor-not-allowed rounded text-sm font-medium text-white">
              {{ saving.remove_node ? 'Removing...' : 'Remove' }}
            </button>
          </div>
        </div>

      </div>
    </div>

  </div>
</template>

<style scoped>
.fade-enter-active, .fade-leave-active {
  transition: opacity 0.3s ease;
}
.fade-enter-from, .fade-leave-to {
  opacity: 0;
}
</style>
