<script setup>
import { ref, onMounted, onUnmounted, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useMeshsatStore } from '@/stores/meshsat'

const props = defineProps({ addr: { type: String, required: true } })
const router = useRouter()
const store = useMeshsatStore()

const device = ref(null)
const history = ref([])
const routing = ref(null)
const error = ref('')
const editing = ref(false)
const aliasInput = ref('')
const commandSending = ref(false)
const commandResult = ref('')
const historyHours = ref(24)
let pollTimer = null

async function refresh() {
  try {
    const [dev, hist, route] = await Promise.all([
      store.fetchZigBeeDevice(props.addr),
      store.fetchZigBeeDeviceHistory(props.addr, historyHours.value).catch(() => ({ readings: [] })),
      store.fetchZigBeeDeviceRouting(props.addr).catch(() => null),
    ])
    device.value = dev
    history.value = hist?.readings || []
    routing.value = route
  } catch (e) {
    error.value = e.message
  }
}

watch(historyHours, () => refresh())

async function saveAlias() {
  try {
    error.value = ''
    const updated = await store.patchZigBeeDevice(props.addr, { alias: aliasInput.value })
    device.value = updated
    editing.value = false
  } catch (e) { error.value = e.message }
}

async function sendCommand(cmd, extra = {}) {
  try {
    commandSending.value = true
    commandResult.value = ''
    await store.sendZigBeeDeviceCommand(props.addr, cmd, extra)
    commandResult.value = `Sent "${cmd}"`
    setTimeout(() => commandResult.value = '', 3000)
  } catch (e) {
    commandResult.value = `Error: ${e.message}`
  } finally {
    commandSending.value = false
  }
}

const levelInput = ref(128)
const colorTempK = ref(3000)
async function sendLevel() {
  await sendCommand('level', { level: Math.max(0, Math.min(254, parseInt(levelInput.value, 10))) })
}
async function sendColorTemp() {
  await sendCommand('color_temp', { kelvin: Math.max(1500, Math.min(7000, parseInt(colorTempK.value, 10))) })
}

// IAS Zone status helpers — decode the 16-bit bitmask the API returns into
// human-readable badges. -1 means the device has never sent a zone-status
// frame (most sensors), so the panel hides itself entirely.
const zoneStatus = computed(() => {
  if (!device.value || device.value.last_zone_status < 0) return null
  const raw = device.value.last_zone_status
  const flags = []
  if (raw & 0x0001) flags.push({ key: 'alarm1', label: 'ALARM 1', tone: 'red' })
  if (raw & 0x0002) flags.push({ key: 'alarm2', label: 'ALARM 2', tone: 'red' })
  if (raw & 0x0004) flags.push({ key: 'tamper', label: 'TAMPER', tone: 'orange' })
  if (raw & 0x0008) flags.push({ key: 'battery_low', label: 'LOW BATTERY', tone: 'amber' })
  if (raw & 0x0040) flags.push({ key: 'trouble', label: 'TROUBLE', tone: 'orange' })
  if (raw & 0x0080) flags.push({ key: 'ac_fault', label: 'AC FAULT', tone: 'orange' })
  if (raw & 0x0100) flags.push({ key: 'test', label: 'TEST MODE', tone: 'gray' })
  if (raw & 0x0200) flags.push({ key: 'battery_defect', label: 'BAT DEFECT', tone: 'red' })
  return { raw, flags, triggered: (raw & 0x0007) !== 0 }
})

async function saveRouting() {
  try {
    routing.value = await store.putZigBeeDeviceRouting(props.addr, routing.value)
  } catch (e) { error.value = e.message }
}

async function refreshSensors() {
  try {
    error.value = ''
    commandResult.value = 'Polling device…'
    await store.refreshZigBeeDevice(props.addr)
    // Give the device 5s to respond on its next mac poll, then re-pull.
    setTimeout(async () => { await refresh(); commandResult.value = 'Done — values may take ~5s for sleepy devices' }, 1000)
    setTimeout(() => commandResult.value = '', 8000)
  } catch (e) { error.value = e.message }
}

async function unpair() {
  if (!confirm(`Forget device ${device.value?.display_name}? Sensor history will be deleted. The device may rejoin on its next announce.`)) return
  try {
    await store.deleteZigBeeDevice(props.addr)
    router.push('/zigbee')
  } catch (e) { error.value = e.message }
}

// Build sparkline points for temperature + humidity from history.
const tempSeries = computed(() => history.value.filter(r => r.cluster === 0x0402))
const humiditySeries = computed(() => history.value.filter(r => r.cluster === 0x0405))
const batterySeries = computed(() => history.value.filter(r => r.cluster === 0x0001))

function buildPath(series, viewW, viewH) {
  if (!series.length) return ''
  const vals = series.map(r => r.value_num).filter(v => v != null)
  if (!vals.length) return ''
  const min = Math.min(...vals)
  const max = Math.max(...vals)
  const span = max - min || 1
  const stepX = viewW / Math.max(1, series.length - 1)
  const points = series.map((r, i) => {
    const x = i * stepX
    const y = viewH - ((r.value_num - min) / span) * viewH
    return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
  })
  return points.join(' ')
}

function seriesRange(series) {
  const vals = series.map(r => r.value_num).filter(v => v != null)
  if (!vals.length) return { min: 0, max: 0 }
  return { min: Math.min(...vals), max: Math.max(...vals) }
}

onMounted(() => {
  refresh()
  pollTimer = setInterval(refresh, 10000)
})
onUnmounted(() => { if (pollTimer) clearInterval(pollTimer) })
</script>

<template>
  <div class="p-4 lg:p-6 max-w-5xl mx-auto space-y-4">
    <!-- Back link -->
    <router-link to="/zigbee" class="text-xs text-gray-500 hover:text-gray-300">← All ZigBee devices</router-link>

    <div v-if="error" class="px-3 py-2 rounded bg-red-500/10 text-red-400 text-xs">{{ error }}</div>

    <div v-if="!device" class="text-gray-500 text-sm py-8 text-center">Loading…</div>

    <div v-else>
      <!-- Header card -->
      <div class="bg-tactical-surface rounded-lg border border-tactical-border p-5 mb-4">
        <div class="flex items-start justify-between gap-3 flex-wrap">
          <div class="min-w-0 flex-1">
            <div v-if="editing" class="flex items-center gap-2 mb-2">
              <input v-model="aliasInput" type="text" placeholder="Set device name"
                class="bg-tactical-bg border border-tactical-border rounded px-3 py-1.5 text-sm flex-1 max-w-xs"
                @keyup.enter="saveAlias" @keyup.escape="editing = false" />
              <button @click="saveAlias" class="px-3 py-1.5 rounded text-xs bg-yellow-400/10 text-yellow-400 border border-yellow-400/30 hover:bg-yellow-400/20">
                Save
              </button>
              <button @click="editing = false" class="px-3 py-1.5 rounded text-xs text-gray-500 hover:text-gray-300">
                Cancel
              </button>
            </div>
            <div v-else class="flex items-center gap-2 mb-2">
              <h1 class="text-xl font-semibold text-gray-100">
                {{ device.display_name || device.alias || ('ZB-' + device.short_addr) }}
              </h1>
              <button @click="editing = true; aliasInput = device.alias"
                class="text-xs text-gray-500 hover:text-yellow-400" title="Rename">
                ✎
              </button>
            </div>
            <div class="font-mono text-[11px] text-gray-500 truncate">IEEE: {{ device.ieee_addr || '—' }}</div>
            <div class="font-mono text-[11px] text-gray-600">
              Short: 0x{{ device.short_addr.toString(16).padStart(4, '0') }} (decimal {{ device.short_addr }})
              · EP{{ device.endpoint || 1 }}
            </div>
            <div v-if="device.manufacturer || device.model" class="text-[11px] text-gray-500 mt-1">
              {{ [device.manufacturer, device.model].filter(Boolean).join(' · ') }}
            </div>
          </div>
          <div class="flex flex-col items-end gap-1">
            <div class="flex items-center gap-1 text-[11px]">
              <span :class="device.online ? 'bg-emerald-400' : 'bg-gray-600'" class="w-2 h-2 rounded-full"></span>
              <span :class="device.online ? 'text-emerald-400' : 'text-gray-500'">
                {{ device.online ? 'Online' : 'Offline' }}
              </span>
            </div>
            <div class="text-[10px] text-gray-500">First seen: {{ device.first_seen?.split(' ')[0] || '—' }}</div>
            <div class="text-[10px] text-gray-500">{{ device.message_count || 0 }} messages</div>
            <button @click="refreshSensors"
              class="mt-1 px-2 py-1 rounded text-[10px] bg-yellow-400/10 text-yellow-400 border border-yellow-400/30 hover:bg-yellow-400/20"
              title="Send ZCL Read Attributes for temp/humidity/battery — sleepy devices may take ~5s to respond">
              Refresh now
            </button>
          </div>
        </div>

        <!-- Live readings strip -->
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-2 mt-4">
          <div class="bg-emerald-500/5 rounded p-3 text-center">
            <div class="text-[10px] text-gray-500 uppercase">Temperature</div>
            <div class="font-mono text-lg text-emerald-400">{{ device.last_temp != null ? device.last_temp.toFixed(1) + '°C' : '—' }}</div>
          </div>
          <div class="bg-sky-500/5 rounded p-3 text-center">
            <div class="text-[10px] text-gray-500 uppercase">Humidity</div>
            <div class="font-mono text-lg text-sky-400">{{ device.last_humidity != null ? device.last_humidity.toFixed(0) + '%' : '—' }}</div>
          </div>
          <div class="bg-amber-500/5 rounded p-3 text-center">
            <div class="text-[10px] text-gray-500 uppercase">Battery</div>
            <div class="font-mono text-lg" :class="device.battery_pct < 20 && device.battery_pct >= 0 ? 'text-red-400' : 'text-amber-400'">
              {{ device.battery_pct >= 0 ? device.battery_pct + '%' : '—' }}
            </div>
          </div>
          <div class="bg-yellow-400/5 rounded p-3 text-center">
            <div class="text-[10px] text-gray-500 uppercase">Signal LQI</div>
            <div class="font-mono text-lg text-yellow-400">{{ device.lqi }}</div>
          </div>
        </div>
      </div>

      <!-- Sensor history chart -->
      <div class="bg-tactical-surface rounded-lg border border-tactical-border p-5 mb-4">
        <div class="flex items-center justify-between mb-3">
          <h2 class="text-sm font-semibold text-gray-300">Sensor history</h2>
          <select v-model.number="historyHours" class="bg-tactical-bg border border-tactical-border rounded px-2 py-1 text-xs">
            <option :value="1">Last 1 hour</option>
            <option :value="6">Last 6 hours</option>
            <option :value="24">Last 24 hours</option>
            <option :value="168">Last 7 days</option>
            <option :value="720">Last 30 days</option>
          </select>
        </div>

        <div v-if="!history.length" class="text-xs text-gray-500 py-6 text-center">
          No readings in the selected window.
        </div>

        <div v-else class="space-y-4">
          <!-- Temperature chart -->
          <div v-if="tempSeries.length">
            <div class="flex items-center justify-between text-[10px] text-gray-500 mb-1">
              <span>Temperature ({{ tempSeries.length }} points)</span>
              <span class="font-mono text-emerald-400">
                {{ seriesRange(tempSeries).min.toFixed(1) }}°C — {{ seriesRange(tempSeries).max.toFixed(1) }}°C
              </span>
            </div>
            <svg viewBox="0 0 600 80" class="w-full h-20 bg-tactical-bg rounded">
              <path :d="buildPath(tempSeries, 600, 76)" fill="none" stroke="#34d399" stroke-width="1.5" />
            </svg>
          </div>

          <!-- Humidity chart -->
          <div v-if="humiditySeries.length">
            <div class="flex items-center justify-between text-[10px] text-gray-500 mb-1">
              <span>Humidity ({{ humiditySeries.length }} points)</span>
              <span class="font-mono text-sky-400">
                {{ seriesRange(humiditySeries).min.toFixed(0) }}% — {{ seriesRange(humiditySeries).max.toFixed(0) }}%
              </span>
            </div>
            <svg viewBox="0 0 600 80" class="w-full h-20 bg-tactical-bg rounded">
              <path :d="buildPath(humiditySeries, 600, 76)" fill="none" stroke="#38bdf8" stroke-width="1.5" />
            </svg>
          </div>

          <!-- Battery chart -->
          <div v-if="batterySeries.length">
            <div class="flex items-center justify-between text-[10px] text-gray-500 mb-1">
              <span>Battery ({{ batterySeries.length }} points)</span>
              <span class="font-mono text-amber-400">
                {{ seriesRange(batterySeries).min.toFixed(0) }}% — {{ seriesRange(batterySeries).max.toFixed(0) }}%
              </span>
            </div>
            <svg viewBox="0 0 600 80" class="w-full h-20 bg-tactical-bg rounded">
              <path :d="buildPath(batterySeries, 600, 76)" fill="none" stroke="#f59e0b" stroke-width="1.5" />
            </svg>
          </div>
        </div>
      </div>

      <!-- Routing config -->
      <div v-if="routing" class="bg-tactical-surface rounded-lg border border-tactical-border p-5 mb-4">
        <h2 class="text-sm font-semibold text-gray-300 mb-3">Routing — where do this device's readings go?</h2>
        <div class="space-y-2">
          <label class="flex items-center gap-2 text-xs text-gray-300 cursor-pointer">
            <input type="checkbox" v-model="routing.to_tak" class="rounded" />
            <span>Forward to TAK as CoT marker (sensor PoI)</span>
          </label>
          <label class="flex items-center gap-2 text-xs text-gray-300 cursor-pointer">
            <input type="checkbox" v-model="routing.to_hub" class="rounded" />
            <span>Publish to Hub telemetry stream</span>
          </label>
          <label class="flex items-center gap-2 text-xs text-gray-300 cursor-pointer">
            <input type="checkbox" v-model="routing.to_log" class="rounded" />
            <span>Log locally (always recommended)</span>
          </label>
          <label class="flex items-center gap-2 text-xs text-gray-300 cursor-pointer">
            <input type="checkbox" v-model="routing.to_mesh" class="rounded" />
            <span>Broadcast on Meshtastic primary channel</span>
          </label>
        </div>
        <div class="mt-3 grid grid-cols-2 gap-3">
          <div>
            <label class="text-[10px] text-gray-500 uppercase">CoT type override (optional)</label>
            <input v-model="routing.cot_type" type="text" placeholder="b-m-p-s-p-i"
              class="bg-tactical-bg border border-tactical-border rounded px-2 py-1 text-xs w-full mt-1 font-mono" />
          </div>
          <div>
            <label class="text-[10px] text-gray-500 uppercase">Min interval (seconds)</label>
            <input v-model.number="routing.min_interval_sec" type="number" min="0"
              class="bg-tactical-bg border border-tactical-border rounded px-2 py-1 text-xs w-full mt-1 font-mono" />
          </div>
        </div>
        <button @click="saveRouting"
          class="mt-3 px-4 py-1.5 rounded text-xs bg-yellow-400/10 text-yellow-400 border border-yellow-400/30 hover:bg-yellow-400/20">
          Save routing
        </button>
      </div>

      <!-- IAS Zone alarm panel — only renders for devices that have ever
           sent a zone-status frame (motion sensors, contacts, leak detectors,
           tamper switches). [MESHSAT-509] -->
      <div v-if="zoneStatus" class="bg-tactical-surface rounded-lg border p-5 mb-4"
        :class="zoneStatus.triggered ? 'border-red-500/50' : 'border-tactical-border'">
        <h2 class="text-sm font-semibold mb-3 flex items-center gap-2"
          :class="zoneStatus.triggered ? 'text-red-400' : 'text-gray-300'">
          <span :class="zoneStatus.triggered ? 'animate-pulse' : ''">●</span>
          IAS Zone status
          <span class="font-mono text-[10px] text-gray-600">0x{{ zoneStatus.raw.toString(16).padStart(4, '0') }}</span>
        </h2>
        <div v-if="zoneStatus.flags.length === 0" class="text-emerald-400 text-xs">All clear</div>
        <div v-else class="flex flex-wrap gap-2">
          <span v-for="f in zoneStatus.flags" :key="f.key"
            class="px-2 py-1 rounded text-[11px] font-medium border"
            :class="{
              'bg-red-500/10 text-red-400 border-red-500/30 animate-pulse': f.tone === 'red',
              'bg-orange-500/10 text-orange-400 border-orange-500/30': f.tone === 'orange',
              'bg-amber-500/10 text-amber-400 border-amber-500/30': f.tone === 'amber',
              'bg-gray-500/10 text-gray-400 border-gray-500/30': f.tone === 'gray',
            }">
            {{ f.label }}
          </span>
        </div>
      </div>

      <!-- Command panel -->
      <div class="bg-tactical-surface rounded-lg border border-tactical-border p-5 mb-4">
        <h2 class="text-sm font-semibold text-gray-300 mb-3">Send commands</h2>
        <div class="text-[10px] text-gray-500 mb-3">
          OnOff (cluster 0x0006), Level (0x0008), Color Temperature (0x0300 cmd 0x0a).
          Sensor-only devices will silently ignore. Tested on Sengled / IKEA / Hue / Tuya bulbs.
        </div>

        <!-- OnOff buttons -->
        <div class="flex items-center gap-2 flex-wrap mb-4">
          <button @click="sendCommand('on')" :disabled="commandSending"
            class="px-4 py-1.5 rounded text-xs bg-emerald-500/10 text-emerald-400 border border-emerald-500/30 hover:bg-emerald-500/20 disabled:opacity-40">
            Turn ON
          </button>
          <button @click="sendCommand('off')" :disabled="commandSending"
            class="px-4 py-1.5 rounded text-xs bg-gray-500/10 text-gray-400 border border-gray-500/30 hover:bg-gray-500/20 disabled:opacity-40">
            Turn OFF
          </button>
          <button @click="sendCommand('toggle')" :disabled="commandSending"
            class="px-4 py-1.5 rounded text-xs bg-violet-500/10 text-violet-400 border border-violet-500/30 hover:bg-violet-500/20 disabled:opacity-40">
            Toggle
          </button>
        </div>

        <!-- Level slider -->
        <div class="border-t border-tactical-border pt-3 mb-4">
          <div class="flex items-center justify-between mb-1">
            <label class="text-[11px] text-gray-400">Brightness (0-254)</label>
            <span class="font-mono text-[11px] text-gray-300">{{ levelInput }} ({{ Math.round(levelInput / 254 * 100) }}%)</span>
          </div>
          <div class="flex items-center gap-3">
            <input type="range" v-model="levelInput" min="0" max="254" step="1"
              class="flex-1 accent-yellow-400" />
            <button @click="sendLevel" :disabled="commandSending"
              class="px-3 py-1.5 rounded text-xs bg-yellow-400/10 text-yellow-400 border border-yellow-400/30 hover:bg-yellow-400/20 disabled:opacity-40">
              Set level
            </button>
          </div>
        </div>

        <!-- Color temperature slider (tunable white) -->
        <div class="border-t border-tactical-border pt-3">
          <div class="flex items-center justify-between mb-1">
            <label class="text-[11px] text-gray-400">Color temperature (Kelvin)</label>
            <span class="font-mono text-[11px] text-gray-300">{{ colorTempK }}K</span>
          </div>
          <div class="flex items-center gap-3">
            <input type="range" v-model="colorTempK" min="2000" max="6500" step="100"
              class="flex-1"
              :style="{
                background: 'linear-gradient(to right, #ffaa55 0%, #ffd9a8 30%, #fff8e7 60%, #d1e3ff 100%)'
              }" />
            <button @click="sendColorTemp" :disabled="commandSending"
              class="px-3 py-1.5 rounded text-xs bg-yellow-400/10 text-yellow-400 border border-yellow-400/30 hover:bg-yellow-400/20 disabled:opacity-40">
              Set temp
            </button>
          </div>
        </div>

        <div v-if="commandResult" class="mt-3 text-[11px]"
          :class="commandResult.startsWith('Error') ? 'text-red-400' : 'text-emerald-400'">
          {{ commandResult }}
        </div>
      </div>

      <!-- Danger zone -->
      <div class="bg-tactical-surface rounded-lg border border-red-500/20 p-5">
        <h2 class="text-sm font-semibold text-red-400 mb-2">Danger zone</h2>
        <div class="text-[11px] text-gray-500 mb-3">
          Forget this device — clears the database row, sensor history, and routing config. The device may rejoin automatically on its next announce.
        </div>
        <button @click="unpair"
          class="px-4 py-1.5 rounded text-xs bg-red-500/10 text-red-400 border border-red-500/30 hover:bg-red-500/20">
          Forget device
        </button>
      </div>
    </div>
  </div>
</template>
