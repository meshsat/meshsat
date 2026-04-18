<script setup>
import { computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import { formatTimeHHMM } from '@/utils/format'

// Persistent status strip — mesh / sat / cell / Hub / GPS / UTC,
// with battery and sync slots reserved for when the bridge REST
// layer exposes them (MESHSAT-523 UPS monitor; MESHSAT-540 hub
// directory sync). [MESHSAT-554]
//
// Lives in the App.vue header, visible on every route.

const store = useMeshsatStore()

// Iridium
const satBars = computed(() => store.iridiumSignal?.bars ?? -1)
const nextPassInfo = computed(() => {
  const now = Date.now() / 1000
  const passes = store.passes || []
  const active = passes.find(p => p.is_active)
  if (active) return { label: 'NOW', color: 'text-tactical-iridium' }
  const next = passes.find(p => p.aos > now)
  if (!next) return null
  const diffSec = next.aos - now
  if (diffSec < 60)    return { label: `${Math.round(diffSec)}s`, color: 'text-tactical-iridium' }
  if (diffSec < 3600)  return { label: `${Math.round(diffSec / 60)}m`, color: 'text-gray-400' }
  if (diffSec < 86400) return { label: `${Math.round(diffSec / 3600)}h`, color: 'text-gray-500' }
  return { label: formatTimeHHMM(next.aos), color: 'text-gray-600' }
})

// Mesh
const meshConnected = computed(() => store.status?.connected ?? false)
const nodeCount = computed(() => {
  const total = (store.nodes || []).length
  const cutoff = Date.now() / 1000 - 7200
  const active = (store.nodes || []).filter(n => n.last_heard > cutoff).length
  return { active, total }
})
const meshAvgSNR = computed(() => {
  const cutoff = Date.now() / 1000 - 7200
  const activeNodes = (store.nodes || []).filter(n => n.last_heard > cutoff && n.snr != null && Math.abs(n.snr) < 100)
  if (!activeNodes.length) return null
  return activeNodes.reduce((sum, n) => sum + n.snr, 0) / activeNodes.length
})
const deviceId = computed(() => {
  const id = store.status?.node_id
  if (!id) return '----'
  return '!' + id.toString(16).toUpperCase().slice(-8)
})

// Cellular
const cellBars = computed(() => store.cellularSignal?.bars ?? -1)

// GPS
const gpsFix = computed(() => {
  const sources = store.locationSources?.sources || []
  const gps = sources.find(s => s.source === 'gps')
  return gps && gps.lat !== 0
})

// Hub (via /api/status hub block populated by hubreporter)
const hubConnected = computed(() => !!store.status?.hub?.connected)

// Battery — the X1202 UPS monitor runs as a separate systemd unit
// today; its readings aren't on /api yet. Placeholder until the
// bridge endpoint lands.
const battery = computed(() => store.status?.battery?.percent ?? null)

// Directory sync timestamp — arrives with MESHSAT-540.
const directorySyncedAt = computed(() => store.status?.directory?.last_sync_at || null)
</script>

<template>
  <div class="flex items-center gap-3 shrink-0 text-[9px]">
    <!-- Iridium -->
    <div class="flex items-center gap-1 md:gap-1.5" aria-label="Satellite signal">
      <span class="hidden md:inline font-medium text-tactical-iridium/70">SAT</span>
      <div class="flex items-end gap-px h-3">
        <span v-for="i in 5" :key="i" class="w-[3px] rounded-[1px]"
          :class="satBars >= i ? (satBars <= 2 ? 'bg-amber-400' : 'bg-tactical-iridium') : 'bg-gray-700/50'"
          :style="{ height: `${3 + i * 2}px` }" />
      </div>
      <span v-if="nextPassInfo" class="hidden md:inline font-mono" :class="nextPassInfo.color">
        {{ nextPassInfo.label }}
      </span>
    </div>

    <span class="hidden md:block w-px h-4 bg-gray-700/50" />

    <!-- Mesh -->
    <div class="flex items-center gap-1 md:gap-1.5" aria-label="Mesh status">
      <span class="hidden md:inline font-medium text-tactical-lora/70">MESH</span>
      <span class="w-1.5 h-1.5 rounded-full" :class="meshConnected ? 'bg-emerald-400' : 'bg-red-400'" />
      <span v-if="meshAvgSNR !== null" class="hidden md:inline font-mono"
        :class="meshAvgSNR >= 0 ? 'text-emerald-400/70' : meshAvgSNR >= -10 ? 'text-amber-400/70' : 'text-red-400/70'">
        {{ meshAvgSNR.toFixed(0) }}dB
      </span>
      <span class="hidden md:inline font-mono text-gray-500">{{ deviceId }}</span>
      <span class="hidden lg:inline font-mono text-gray-600">{{ nodeCount.active }}/{{ nodeCount.total }}</span>
    </div>

    <span class="hidden md:block w-px h-4 bg-gray-700/50" />

    <!-- Cellular -->
    <div class="flex items-center gap-1" aria-label="Cellular signal">
      <span class="hidden md:inline font-medium text-sky-400/70">CELL</span>
      <template v-if="cellBars >= 0">
        <div class="flex items-end gap-px h-3">
          <span v-for="i in 5" :key="'cell'+i" class="w-[3px] rounded-[1px]"
            :class="cellBars >= i ? 'bg-sky-400' : 'bg-gray-700/50'"
            :style="{ height: `${3 + i * 2}px` }" />
        </div>
        <span class="hidden md:inline text-sky-400/60 font-mono">{{ store.cellularStatus?.network_type || 'LTE' }}</span>
      </template>
      <span v-else class="text-gray-600 font-mono">--</span>
    </div>

    <!-- Hub connection -->
    <div class="flex items-center gap-1" aria-label="Hub status">
      <span class="w-1.5 h-1.5 rounded-full" :class="hubConnected ? 'bg-violet-400' : 'bg-gray-600'" />
      <span class="hidden md:inline" :class="hubConnected ? 'text-violet-400/70' : 'text-gray-600'">HUB</span>
    </div>

    <!-- Battery (X1202 UPS — placeholder until endpoint lands) -->
    <div v-if="battery !== null" class="flex items-center gap-1" aria-label="Battery">
      <svg class="w-3 h-3" viewBox="0 0 24 14" fill="none" stroke="currentColor" stroke-width="2">
        <rect x="1" y="1" width="18" height="12" rx="1" />
        <path d="M20 4v6" />
      </svg>
      <span class="font-mono" :class="battery < 20 ? 'text-red-400' : battery < 50 ? 'text-amber-400' : 'text-emerald-400'">
        {{ battery }}%
      </span>
    </div>

    <!-- GPS -->
    <div class="flex items-center gap-1" aria-label="GPS fix">
      <span class="w-1.5 h-1.5 rounded-full" :class="gpsFix ? 'bg-tactical-gps' : 'bg-gray-600'" />
      <span class="hidden md:inline" :class="gpsFix ? 'text-tactical-gps/70' : 'text-gray-600'">GPS</span>
    </div>

    <!-- Directory sync (placeholder until MESHSAT-540) -->
    <div v-if="directorySyncedAt" class="hidden lg:flex items-center gap-1" :title="'Directory synced ' + directorySyncedAt">
      <span class="w-1.5 h-1.5 rounded-full bg-emerald-400" />
      <span class="text-emerald-400/70">SYNC</span>
    </div>

    <!-- NVIS night theme toggle (MIL-STD-3009 Green A) [MESHSAT-556] -->
    <button type="button" @click="store.toggleNVIS()"
      class="flex items-center gap-1 px-1.5 py-0.5 rounded border border-tactical-border"
      :class="store.isNVIS
        ? 'bg-[#00FF41]/10 text-[#00FF41] border-[#00FF41]/60'
        : 'text-gray-500 hover:text-gray-300'"
      :title="store.isNVIS ? 'Disable NVIS night theme' : 'Enable NVIS night theme (MIL-STD-3009)'"
      aria-label="Toggle NVIS night theme">
      <svg class="w-3 h-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
        stroke-linecap="round" stroke-linejoin="round">
        <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
      </svg>
      <span class="hidden md:inline font-medium tracking-wide">NVIS</span>
    </button>
  </div>
</template>
