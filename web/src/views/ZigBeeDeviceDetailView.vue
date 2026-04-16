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

async function sendCommand(cmd) {
  try {
    commandSending.value = true
    commandResult.value = ''
    await store.sendZigBeeDeviceCommand(props.addr, cmd)
    commandResult.value = `Sent "${cmd}"`
    setTimeout(() => commandResult.value = '', 3000)
  } catch (e) {
    commandResult.value = `Error: ${e.message}`
  } finally {
    commandSending.value = false
  }
}

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

      <!-- Command panel -->
      <div class="bg-tactical-surface rounded-lg border border-tactical-border p-5 mb-4">
        <h2 class="text-sm font-semibold text-gray-300 mb-3">Send command (OnOff cluster)</h2>
        <div class="text-[10px] text-gray-500 mb-3">
          Only effective on devices that implement ZCL cluster 0x0006 (switches, lights, smart plugs).
          Sensor-only devices will silently ignore.
        </div>
        <div class="flex items-center gap-2 flex-wrap">
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
          <span v-if="commandResult" class="text-[11px]"
            :class="commandResult.startsWith('Error') ? 'text-red-400' : 'text-emerald-400'">
            {{ commandResult }}
          </span>
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
