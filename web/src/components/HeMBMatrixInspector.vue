<script setup>
import { ref, computed, watch } from 'vue'
import api from '../api/client'

const streamId = ref(0)
const genId = ref(0)
const inspection = ref(null)
const loading = ref(false)
const error = ref('')

// Gauss animation state
const gaussStep = ref(-1) // -1 = original matrix
const gaussPlaying = ref(false)
let gaussTimer = null

async function fetchInspection() {
  loading.value = true
  error.value = ''
  gaussStep.value = -1
  gaussPlaying.value = false
  clearInterval(gaussTimer)
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
    case 'decoded': return `DECODED (rank ${i.rank}/${i.k})`
    case 'rank_deficient': return `RANK DEFICIENT (${i.rank}/${i.k})`
    default: return `PENDING (${i.rank}/${i.k})`
  }
})

// The matrix to display — either original or current Gauss step snapshot.
const displayMatrix = computed(() => {
  if (!inspection.value) return []
  const steps = inspection.value.gauss_steps
  if (gaussStep.value < 0 || !steps || steps.length === 0) {
    return inspection.value.coefficient_matrix || []
  }
  const idx = Math.min(gaussStep.value, steps.length - 1)
  return steps[idx].matrix || []
})

const currentStepInfo = computed(() => {
  if (!inspection.value || gaussStep.value < 0) return null
  const steps = inspection.value.gauss_steps
  if (!steps || steps.length === 0) return null
  const idx = Math.min(gaussStep.value, steps.length - 1)
  const s = steps[idx]
  switch (s.op) {
    case 'swap': return `Swap R${s.row} ↔ R${s.src_row} (pivot col ${s.col})`
    case 'scale': return `Scale R${s.row} × 0x${s.factor.toString(16).padStart(2, '0')} (col ${s.col})`
    case 'eliminate': return `R${s.row} -= 0x${s.factor.toString(16).padStart(2, '0')} × R${s.src_row} (col ${s.col})`
    default: return s.op
  }
})

const totalSteps = computed(() => inspection.value?.gauss_steps?.length ?? 0)

function gaussPlay() {
  if (totalSteps.value === 0) return
  gaussPlaying.value = true
  if (gaussStep.value >= totalSteps.value - 1) gaussStep.value = -1
  gaussTimer = setInterval(() => {
    gaussStep.value++
    if (gaussStep.value >= totalSteps.value - 1) {
      gaussPlaying.value = false
      clearInterval(gaussTimer)
    }
  }, 400)
}

function gaussPause() {
  gaussPlaying.value = false
  clearInterval(gaussTimer)
}

function gaussStepForward() {
  gaussPause()
  if (gaussStep.value < totalSteps.value - 1) gaussStep.value++
}

function gaussStepBack() {
  gaussPause()
  if (gaussStep.value > -1) gaussStep.value--
}

function gaussReset() {
  gaussPause()
  gaussStep.value = -1
}

// Cell color: 0 = white, 255 = black (grayscale).
function cellStyle(val) {
  const brightness = 255 - val
  return { backgroundColor: `rgb(${brightness},${brightness},${brightness})` }
}

// Bearer colors for timeline dots.
const bearerPalette = [
  '#06b6d4', '#f97316', '#8b5cf6', '#3b82f6',
  '#22c55e', '#ef4444', '#f59e0b', '#ec4899'
]
function bearerColor(idx) {
  return bearerPalette[idx % bearerPalette.length]
}

// Timeline: compute max offset for scaling.
const timelineMaxMs = computed(() => {
  if (!inspection.value?.bearer_timeline) return 1
  const max = Math.max(...inspection.value.bearer_timeline.map(t => t.offset_ms))
  return max > 0 ? max : 1
})

// Unique bearers in the timeline.
const timelineBearers = computed(() => {
  if (!inspection.value?.bearer_timeline) return []
  const seen = new Set()
  return inspection.value.bearer_timeline
    .filter(t => { if (seen.has(t.bearer_idx)) return false; seen.add(t.bearer_idx); return true })
    .map(t => t.bearer_idx)
})

watch(() => inspection.value, () => {
  gaussStep.value = -1
  gaussPlaying.value = false
  clearInterval(gaussTimer)
})
</script>

<template>
  <div class="border-t border-gray-800 p-4 max-h-[50vh] overflow-y-auto">
    <!-- Header with inputs -->
    <div class="flex items-center gap-4 mb-4">
      <h3 class="text-xs font-semibold text-gray-400 uppercase tracking-wider">RLNC Matrix Inspector</h3>
      <div class="flex items-center gap-2">
        <label class="text-[10px] text-gray-500">Stream</label>
        <input v-model.number="streamId" type="number" min="0" max="255"
          class="w-14 bg-gray-800 border border-gray-700 rounded px-1.5 py-0.5 text-xs font-mono text-gray-300"
          @keydown.enter="fetchInspection" />
        <label class="text-[10px] text-gray-500">Gen</label>
        <input v-model.number="genId" type="number" min="0" max="65535"
          class="w-16 bg-gray-800 border border-gray-700 rounded px-1.5 py-0.5 text-xs font-mono text-gray-300"
          @keydown.enter="fetchInspection" />
        <button @click="fetchInspection" :disabled="loading"
          class="px-2 py-0.5 bg-gray-700 hover:bg-gray-600 text-gray-300 text-xs rounded disabled:opacity-50">
          {{ loading ? 'Loading...' : 'Inspect' }}
        </button>
      </div>
    </div>

    <div v-if="error" class="text-xs text-red-400 mb-3">{{ error }}</div>

    <div v-if="inspection" class="space-y-4">
      <!-- Status bar -->
      <div class="flex items-center gap-6 text-xs flex-wrap">
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
        <div v-if="inspection.cost > 0" class="flex items-center gap-1.5">
          <span class="text-gray-500">Cost</span>
          <span class="font-mono text-orange-400">${{ inspection.cost.toFixed(4) }}</span>
        </div>
      </div>

      <!-- Coefficient matrix grid -->
      <div v-if="displayMatrix.length > 0">
        <div class="flex items-center gap-3 mb-1">
          <div class="text-[10px] text-gray-500 uppercase tracking-wider">
            Coefficient Matrix (N={{ inspection.n }} x K={{ inspection.k }})
          </div>
          <!-- Gauss animation controls -->
          <div v-if="totalSteps > 0" class="flex items-center gap-1">
            <button @click="gaussReset" class="px-1.5 py-0.5 bg-gray-800 hover:bg-gray-700 text-gray-400 text-[10px] rounded"
              title="Reset to original">|&lt;</button>
            <button @click="gaussStepBack" class="px-1.5 py-0.5 bg-gray-800 hover:bg-gray-700 text-gray-400 text-[10px] rounded"
              title="Step back">&lt;</button>
            <button v-if="!gaussPlaying" @click="gaussPlay"
              class="px-1.5 py-0.5 bg-purple-900/40 hover:bg-purple-900/60 text-purple-300 text-[10px] rounded"
              title="Play">Play</button>
            <button v-else @click="gaussPause"
              class="px-1.5 py-0.5 bg-purple-900/40 hover:bg-purple-900/60 text-purple-300 text-[10px] rounded"
              title="Pause">Pause</button>
            <button @click="gaussStepForward" class="px-1.5 py-0.5 bg-gray-800 hover:bg-gray-700 text-gray-400 text-[10px] rounded"
              title="Step forward">&gt;</button>
            <span class="text-[10px] font-mono text-gray-600 ml-1">
              {{ gaussStep < 0 ? 'original' : `${gaussStep + 1}/${totalSteps}` }}
            </span>
          </div>
        </div>

        <!-- Step description -->
        <div v-if="currentStepInfo" class="text-[10px] font-mono text-purple-400 mb-1">{{ currentStepInfo }}</div>

        <!-- Matrix grid -->
        <div class="inline-block border border-gray-700 rounded overflow-hidden">
          <div v-for="(row, ri) in displayMatrix" :key="ri"
            class="flex"
            :class="inspection.symbols?.[ri]?.is_independent ? 'border-l-2 border-l-blue-500' : 'border-l-2 border-l-red-500'">
            <div v-for="(val, ci) in row" :key="ci"
              class="w-5 h-5 flex items-center justify-center text-[8px] font-mono"
              :style="cellStyle(val)"
              :title="`[${ri},${ci}] = ${val} (0x${val.toString(16).padStart(2, '0')})`">
            </div>
            <div class="w-8 flex items-center justify-center text-[9px] font-mono text-gray-600 bg-gray-900/50">
              B{{ inspection.symbols?.[ri]?.bearer_idx ?? '?' }}
            </div>
          </div>
        </div>

        <!-- Legend -->
        <div class="flex gap-3 mt-1.5 text-[10px] text-gray-500">
          <span class="flex items-center gap-1">
            <span class="w-2 h-2 border-l-2 border-l-blue-500 inline-block"></span> Independent
          </span>
          <span class="flex items-center gap-1">
            <span class="w-2 h-2 border-l-2 border-l-red-500 inline-block"></span> Dependent
          </span>
          <span class="flex items-center gap-1">
            <span class="w-3 h-3 inline-block rounded border border-gray-700" :style="cellStyle(0)"></span> 0
          </span>
          <span class="flex items-center gap-1">
            <span class="w-3 h-3 inline-block rounded border border-gray-700" :style="cellStyle(128)"></span> 128
          </span>
          <span class="flex items-center gap-1">
            <span class="w-3 h-3 inline-block rounded border border-gray-700" :style="cellStyle(255)"></span> 255
          </span>
        </div>
      </div>

      <!-- Bearer timeline -->
      <div v-if="inspection.bearer_timeline?.length > 0">
        <div class="text-[10px] text-gray-500 uppercase tracking-wider mb-1">Bearer Timeline</div>
        <div class="bg-gray-900/50 rounded p-2">
          <!-- Timeline axis per bearer -->
          <div v-for="bidx in timelineBearers" :key="bidx" class="flex items-center gap-2 mb-1 last:mb-0">
            <span class="text-[9px] font-mono w-6 text-right" :style="{ color: bearerColor(bidx) }">B{{ bidx }}</span>
            <div class="flex-1 h-4 relative bg-gray-800/50 rounded">
              <!-- Time axis line -->
              <div class="absolute inset-y-0 left-0 right-0 flex items-center">
                <div class="w-full h-px bg-gray-700"></div>
              </div>
              <!-- Symbol dots -->
              <div v-for="(t, ti) in inspection.bearer_timeline.filter(e => e.bearer_idx === bidx)" :key="ti"
                class="absolute top-1/2 -translate-y-1/2 w-2.5 h-2.5 rounded-full border border-gray-900"
                :style="{
                  left: (t.offset_ms / timelineMaxMs * 100) + '%',
                  backgroundColor: bearerColor(bidx),
                  marginLeft: '-5px'
                }"
                :title="`Sym #${t.sym_idx} @ +${t.offset_ms}ms`">
              </div>
            </div>
          </div>
          <!-- Time labels -->
          <div class="flex justify-between mt-1 text-[9px] font-mono text-gray-600 pl-8">
            <span>0ms</span>
            <span>+{{ timelineMaxMs }}ms</span>
          </div>
        </div>
      </div>

      <!-- Symbol details table -->
      <div v-if="inspection.symbols?.length > 0">
        <div class="text-[10px] text-gray-500 uppercase tracking-wider mb-1">Symbols</div>
        <table class="text-xs w-full max-w-lg">
          <thead>
            <tr class="text-[10px] text-gray-500 uppercase">
              <th class="px-2 py-1 text-left">#</th>
              <th class="px-2 py-1 text-left">Bearer</th>
              <th class="px-2 py-1 text-left">Independent</th>
              <th class="px-2 py-1 text-left">Offset</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(sym, i) in inspection.symbols" :key="i"
              class="border-t border-gray-800/50">
              <td class="px-2 py-1 font-mono text-gray-400">{{ sym.index }}</td>
              <td class="px-2 py-1 font-mono" :style="{ color: bearerColor(sym.bearer_idx) }">B{{ sym.bearer_idx }}</td>
              <td class="px-2 py-1">
                <span v-if="sym.is_independent" class="text-blue-400">Yes</span>
                <span v-else class="text-red-400">No</span>
              </td>
              <td class="px-2 py-1 font-mono text-gray-500">{{ sym.offset_ms > 0 ? '+' + sym.offset_ms + 'ms' : '0ms' }}</td>
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
