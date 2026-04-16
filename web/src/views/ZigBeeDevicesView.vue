<script setup>
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const devices = ref([])
const loading = ref(true)
const error = ref('')
const permitJoin = ref({ active: false, remaining_sec: 0 })
const status = ref(null)
let pollTimer = null

async function refresh() {
  try {
    const [list, st, pj] = await Promise.all([
      store.fetchZigBeeDevicesEnriched(),
      store.fetchZigBeeStatus().then(() => store.zigbeeStatus),
      store.fetchZigBeePermitJoin().then(() => store.zigbeePermitJoin),
    ])
    devices.value = list?.devices || []
    status.value = st
    permitJoin.value = pj || { active: false, remaining_sec: 0 }
    loading.value = false
  } catch (e) {
    error.value = e.message
    loading.value = false
  }
}

async function startPair() {
  try {
    error.value = ''
    await store.startZigBeePermitJoin(120)
    await refresh()
  } catch (e) { error.value = e.message }
}
async function stopPair() {
  await store.stopZigBeePermitJoin()
  await refresh()
}

const sortedDevices = computed(() => {
  return [...devices.value].sort((a, b) => {
    if (a.online !== b.online) return a.online ? -1 : 1
    return (b.last_seen || '').localeCompare(a.last_seen || '')
  })
})

function lqiBars(lqi) {
  if (lqi >= 200) return 4
  if (lqi >= 150) return 3
  if (lqi >= 100) return 2
  if (lqi >= 50) return 1
  return 0
}

function formatRelative(iso) {
  if (!iso) return '—'
  const t = Date.parse(iso.replace(' ', 'T') + (iso.includes('Z') || iso.includes('+') ? '' : 'Z'))
  if (isNaN(t)) return iso
  const diff = (Date.now() - t) / 1000
  if (diff < 60) return `${Math.round(diff)}s ago`
  if (diff < 3600) return `${Math.round(diff / 60)}m ago`
  if (diff < 86400) return `${Math.round(diff / 3600)}h ago`
  return `${Math.round(diff / 86400)}d ago`
}

onMounted(() => {
  refresh()
  pollTimer = setInterval(refresh, 5000)
})
onUnmounted(() => { if (pollTimer) clearInterval(pollTimer) })
</script>

<template>
  <div class="p-4 lg:p-6 max-w-7xl mx-auto space-y-4">
    <!-- Header -->
    <div class="flex items-start justify-between flex-wrap gap-3">
      <div>
        <h1 class="font-display text-xl font-semibold text-yellow-400 tracking-wide">ZigBee Devices</h1>
        <p class="text-xs text-gray-500 mt-0.5">
          {{ status?.firmware || 'Coordinator not connected' }}
          <span v-if="status?.coord_state" class="ml-2 px-1.5 py-0.5 rounded text-[10px]"
            :class="status.coord_ready ? 'bg-emerald-500/10 text-emerald-400' : 'bg-amber-500/10 text-amber-400'">
            {{ status.coord_state }}
          </span>
        </p>
      </div>
      <div class="flex items-center gap-2">
        <button v-if="!permitJoin.active" @click="startPair"
          :disabled="!status?.coord_ready"
          class="px-4 py-2 rounded text-xs font-medium bg-yellow-400/10 text-yellow-400 border border-yellow-400/30 hover:bg-yellow-400/20 disabled:opacity-40 disabled:cursor-not-allowed">
          Pair new device (120s)
        </button>
        <button v-else @click="stopPair"
          class="px-4 py-2 rounded text-xs font-medium bg-red-400/10 text-red-400 border border-red-400/30 hover:bg-red-400/20 animate-pulse">
          Pairing open ({{ permitJoin.remaining_sec }}s) — Stop
        </button>
      </div>
    </div>

    <div v-if="error" class="px-3 py-2 rounded bg-red-500/10 text-red-400 text-xs">
      {{ error }}
    </div>

    <!-- Empty state -->
    <div v-if="!loading && sortedDevices.length === 0"
      class="bg-tactical-surface rounded-lg border border-tactical-border p-8 text-center">
      <div class="text-gray-500 text-sm mb-2">No paired ZigBee devices</div>
      <div class="text-gray-600 text-xs">
        Press "Pair new device" above and put your device into pairing mode (consult the device manual — usually a long press on the reset button).
      </div>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="text-gray-500 text-sm py-8 text-center">Loading…</div>

    <!-- Device list -->
    <div v-if="!loading && sortedDevices.length > 0" class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      <router-link v-for="dev in sortedDevices" :key="dev.ieee_addr || dev.short_addr"
        :to="`/zigbee/${dev.ieee_addr || dev.short_addr}`"
        class="bg-tactical-surface rounded-lg border border-tactical-border p-4 hover:border-yellow-400/40 transition-colors block">
        <!-- Top row: name + online status -->
        <div class="flex items-start justify-between gap-2 mb-3">
          <div class="min-w-0 flex-1">
            <div class="font-medium text-gray-100 truncate" :title="dev.display_name">
              {{ dev.display_name || dev.alias || ('ZB-' + dev.short_addr) }}
            </div>
            <div class="font-mono text-[10px] text-gray-500 truncate" :title="dev.ieee_addr">
              {{ dev.ieee_addr || ('short:' + dev.short_addr) }}
            </div>
            <div v-if="dev.manufacturer || dev.model" class="text-[10px] text-gray-600 truncate">
              {{ [dev.manufacturer, dev.model].filter(Boolean).join(' · ') }}
            </div>
          </div>
          <div class="flex items-center gap-1 shrink-0">
            <span :class="dev.online ? 'bg-emerald-400' : 'bg-gray-600'"
              class="w-2 h-2 rounded-full"></span>
            <span class="text-[10px]" :class="dev.online ? 'text-emerald-400' : 'text-gray-600'">
              {{ dev.online ? 'live' : 'offline' }}
            </span>
          </div>
        </div>

        <!-- Sensor readings -->
        <div class="grid grid-cols-2 gap-2 mb-3">
          <div v-if="dev.last_temp != null" class="text-center bg-emerald-500/5 rounded p-2">
            <div class="text-[9px] text-gray-500 uppercase tracking-wide">Temp</div>
            <div class="font-mono text-sm text-emerald-400">{{ dev.last_temp.toFixed(1) }}°C</div>
          </div>
          <div v-if="dev.last_humidity != null" class="text-center bg-sky-500/5 rounded p-2">
            <div class="text-[9px] text-gray-500 uppercase tracking-wide">Humidity</div>
            <div class="font-mono text-sm text-sky-400">{{ dev.last_humidity.toFixed(0) }}%</div>
          </div>
          <div v-if="dev.battery_pct >= 0" class="text-center bg-amber-500/5 rounded p-2">
            <div class="text-[9px] text-gray-500 uppercase tracking-wide">Battery</div>
            <div class="font-mono text-sm" :class="dev.battery_pct < 20 ? 'text-red-400' : 'text-amber-400'">
              {{ dev.battery_pct }}%
            </div>
          </div>
          <div v-if="dev.last_onoff >= 0" class="text-center bg-violet-500/5 rounded p-2">
            <div class="text-[9px] text-gray-500 uppercase tracking-wide">Switch</div>
            <div class="font-mono text-sm text-violet-400">{{ dev.last_onoff ? 'ON' : 'OFF' }}</div>
          </div>
        </div>

        <!-- Footer: signal + last seen -->
        <div class="flex items-center justify-between text-[10px] text-gray-500">
          <div class="flex items-center gap-1" :title="`LQI ${dev.lqi}`">
            <div class="flex items-end gap-0.5 h-3">
              <div v-for="bar in 4" :key="bar"
                class="w-0.5 rounded-sm"
                :style="{ height: `${bar * 25}%` }"
                :class="bar <= lqiBars(dev.lqi) ? 'bg-yellow-400' : 'bg-gray-700'"></div>
            </div>
            <span>{{ dev.lqi }}</span>
          </div>
          <span>{{ formatRelative(dev.last_seen) }}</span>
        </div>
      </router-link>
    </div>
  </div>
</template>
