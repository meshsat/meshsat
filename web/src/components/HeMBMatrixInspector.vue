<script setup>
import { ref, computed } from 'vue'
import api from '../api/client'

const streamId = ref(0)
const genId = ref(0)
const inspection = ref(null)
const loading = ref(false)
const error = ref('')

async function fetchInspection() {
  loading.value = true
  error.value = ''
  try {
    inspection.value = await api.get(`/hemb/generations/${streamId.value}/${genId.value}`)
  } catch (e) {
    error.value = e.message || 'Generation not found'
    inspection.value = null
  }
  loading.value = false
}

const statusColor = computed(() => {
  if (!inspection.value) return 'text-gray-500'
  switch (inspection.value.decode_status) {
    case 'decoded': return 'text-emerald-400'
    case 'rank_deficient': return 'text-red-400'
    default: return 'text-amber-400'
  }
})

const statusLabel = computed(() => {
  if (!inspection.value) return ''
  const i = inspection.value
  switch (i.decode_status) {
    case 'decoded': return `FULL RANK (${i.rank}/${i.k})`
    case 'rank_deficient': return `RANK DEFICIENT (${i.rank}/${i.k})`
    default: return `PENDING (${i.rank}/${i.k})`
  }
})

function cellColor(val) {
  if (val === 0) return 'bg-gray-900'
  const intensity = Math.round((val / 255) * 100)
  return `background-color: hsl(210, 80%, ${10 + intensity * 0.5}%)`
}

function cellStyle(val) {
  if (val === 0) return { backgroundColor: '#111827' }
  const pct = val / 255
  const l = 10 + pct * 50
  return { backgroundColor: `hsl(210, 80%, ${l}%)` }
}
</script>

<template>
  <div class="border-t border-gray-800 p-4">
    <div class="flex items-center gap-4 mb-4">
      <h3 class="text-xs font-semibold text-gray-400 uppercase tracking-wider">RLNC Matrix Inspector</h3>
      <div class="flex items-center gap-2">
        <label class="text-[10px] text-gray-500">Stream</label>
        <input v-model.number="streamId" type="number" min="0" max="255"
          class="w-14 bg-gray-800 border border-gray-700 rounded px-1.5 py-0.5 text-xs font-mono text-gray-300" />
        <label class="text-[10px] text-gray-500">Gen</label>
        <input v-model.number="genId" type="number" min="0" max="65535"
          class="w-16 bg-gray-800 border border-gray-700 rounded px-1.5 py-0.5 text-xs font-mono text-gray-300" />
        <button @click="fetchInspection" :disabled="loading"
          class="px-2 py-0.5 bg-gray-700 hover:bg-gray-600 text-gray-300 text-xs rounded disabled:opacity-50">
          {{ loading ? 'Loading...' : 'Inspect' }}
        </button>
      </div>
    </div>

    <div v-if="error" class="text-xs text-red-400 mb-3">{{ error }}</div>

    <div v-if="inspection" class="space-y-4">
      <!-- Status bar -->
      <div class="flex items-center gap-6 text-xs">
        <div class="flex items-center gap-1.5">
          <span class="text-gray-500">Status</span>
          <span class="font-mono font-semibold" :class="statusColor">{{ statusLabel }}</span>
        </div>
        <div class="flex items-center gap-1.5">
          <span class="text-gray-500">K</span>
          <span class="font-mono text-gray-300">{{ inspection.k }}</span>
        </div>
        <div class="flex items-center gap-1.5">
          <span class="text-gray-500">N</span>
          <span class="font-mono text-gray-300">{{ inspection.n }}</span>
        </div>
        <div class="flex items-center gap-1.5">
          <span class="text-gray-500">Rank</span>
          <span class="font-mono" :class="inspection.rank >= inspection.k ? 'text-emerald-400' : 'text-red-400'">
            {{ inspection.rank }}
          </span>
        </div>
      </div>

      <!-- Coefficient matrix grid -->
      <div v-if="inspection.coefficient_matrix?.length > 0">
        <div class="text-[10px] text-gray-500 uppercase tracking-wider mb-1">Coefficient Matrix (N={{ inspection.n }} x K={{ inspection.k }})</div>
        <div class="inline-block border border-gray-700 rounded overflow-hidden">
          <div v-for="(row, ri) in inspection.coefficient_matrix" :key="ri"
            class="flex"
            :class="inspection.symbols?.[ri]?.is_independent ? 'border-l-2 border-l-blue-500' : 'border-l-2 border-l-red-500'">
            <div v-for="(val, ci) in row" :key="ci"
              class="w-5 h-5 flex items-center justify-center text-[8px] font-mono"
              :style="cellStyle(val)"
              :title="`[${ri},${ci}] = ${val} (0x${val.toString(16).padStart(2, '0')})`">
              {{ val > 0 ? '' : '' }}
            </div>
            <div class="w-8 flex items-center justify-center text-[9px] font-mono text-gray-600 bg-gray-900/50">
              B{{ inspection.symbols?.[ri]?.bearer_idx ?? '?' }}
            </div>
          </div>
        </div>
        <div class="flex gap-3 mt-1.5 text-[10px] text-gray-500">
          <span class="flex items-center gap-1">
            <span class="w-2 h-2 border-l-2 border-l-blue-500 inline-block"></span> Independent
          </span>
          <span class="flex items-center gap-1">
            <span class="w-2 h-2 border-l-2 border-l-red-500 inline-block"></span> Dependent
          </span>
          <span class="flex items-center gap-1">
            <span class="w-3 h-3 inline-block rounded" :style="cellStyle(0)"></span> 0
          </span>
          <span class="flex items-center gap-1">
            <span class="w-3 h-3 inline-block rounded" :style="cellStyle(128)"></span> 128
          </span>
          <span class="flex items-center gap-1">
            <span class="w-3 h-3 inline-block rounded" :style="cellStyle(255)"></span> 255
          </span>
        </div>
      </div>

      <!-- Symbol details table -->
      <div v-if="inspection.symbols?.length > 0">
        <div class="text-[10px] text-gray-500 uppercase tracking-wider mb-1">Symbols</div>
        <table class="text-xs w-full max-w-md">
          <thead>
            <tr class="text-[10px] text-gray-500 uppercase">
              <th class="px-2 py-1 text-left">#</th>
              <th class="px-2 py-1 text-left">Bearer</th>
              <th class="px-2 py-1 text-left">Independent</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(sym, i) in inspection.symbols" :key="i"
              class="border-t border-gray-800/50">
              <td class="px-2 py-1 font-mono text-gray-400">{{ sym.index }}</td>
              <td class="px-2 py-1 font-mono text-gray-300">B{{ sym.bearer_idx }}</td>
              <td class="px-2 py-1">
                <span v-if="sym.is_independent" class="text-blue-400">Yes</span>
                <span v-else class="text-red-400">No</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-else-if="!error" class="text-xs text-gray-600 py-4 text-center">
      Enter a stream/generation ID and click Inspect to view the RLNC coefficient matrix.
    </div>
  </div>
</template>
