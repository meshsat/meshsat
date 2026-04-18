<script setup>
import { ref, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

// SOS panic button. [MESHSAT-562]
//
// Acceptance: ≤2 taps from any screen. Confirmation requires either
// a 3-second hold OR a second tap within 2 seconds. The underlying
// /api/sos/activate call fans out FLASH-precedence messages over
// mesh + every online satellite gateway (existing handler).
//
// This component is designed to sit in both the persistent status
// strip (compact "SOS" pill) and any page that wants the full
// confirmation panel (`compact` prop flips the layout).

const props = defineProps({
  compact: { type: Boolean, default: false }
})

const store = useMeshsatStore()

const holding = ref(false)
const holdStart = ref(0)
const holdProgress = ref(0)
let holdTimer = null
let tapExpiry = null
const HOLD_MS = 3000
const DOUBLE_TAP_WINDOW = 2000

function startHold(ev) {
  if (store.sosStatus?.active) return
  ev.preventDefault()
  holding.value = true
  holdStart.value = Date.now()
  holdProgress.value = 0
  holdTimer = setInterval(() => {
    holdProgress.value = Math.min(100, (Date.now() - holdStart.value) / HOLD_MS * 100)
    if (holdProgress.value >= 100) {
      fire('hold-3s')
      cancelHold()
    }
  }, 50)
}
function cancelHold() {
  holding.value = false
  holdProgress.value = 0
  if (holdTimer) { clearInterval(holdTimer); holdTimer = null }
}

// Double-tap path: first tap arms, second tap within 2 s fires.
function onTap() {
  if (store.sosStatus?.active) return
  if (tapExpiry && Date.now() < tapExpiry) {
    fire('double-tap')
    tapExpiry = null
    return
  }
  tapExpiry = Date.now() + DOUBLE_TAP_WINDOW
  setTimeout(() => { if (tapExpiry && Date.now() >= tapExpiry) tapExpiry = null }, DOUBLE_TAP_WINDOW + 50)
}

async function fire(trigger) {
  try {
    await store.activateSOS({ trigger })
  } catch (_) { /* surfaced via store.error */ }
}

async function onCancel() { await store.cancelSOS() }

onUnmounted(() => { if (holdTimer) clearInterval(holdTimer) })
</script>

<template>
  <!-- Active SOS -->
  <div v-if="store.sosStatus?.active"
    class="flex items-center gap-2 px-3 py-1.5 rounded border border-red-500 bg-red-500/20 animate-pulse">
    <span class="text-[10px] font-bold text-red-300 tracking-wider">SOS ACTIVE</span>
    <span class="text-[10px] text-red-300/80">{{ store.sosStatus.sends || 0 }}/3</span>
    <button type="button" @click="onCancel"
      class="px-2 py-0.5 rounded bg-gray-700 text-gray-200 text-[10px] font-medium min-h-[28px]">
      Cancel
    </button>
  </div>

  <!-- Idle SOS (compact header pill) -->
  <button v-else-if="compact" type="button"
    @pointerdown="startHold" @pointerup="cancelHold" @pointerleave="cancelHold"
    @click="onTap"
    class="relative flex items-center gap-1 px-2 py-1 rounded bg-red-600 hover:bg-red-500 text-white text-[10px] font-bold tracking-wider min-h-[28px]"
    :aria-label="'SOS — hold 3 seconds or double-tap'">
    <svg class="w-3 h-3" viewBox="0 0 24 24" fill="currentColor"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z"/></svg>
    <span>SOS</span>
    <span v-if="holding" class="absolute inset-x-0 bottom-0 h-0.5 bg-white/80 rounded-b"
      :style="{ width: holdProgress + '%' }" />
  </button>

  <!-- Full panel (dashboard use) -->
  <button v-else type="button"
    @pointerdown="startHold" @pointerup="cancelHold" @pointerleave="cancelHold"
    @click="onTap"
    class="relative w-full py-4 rounded-lg bg-red-600 hover:bg-red-500 text-white font-bold text-base tracking-wider min-h-[56px]">
    <span>SOS — hold 3 seconds or double-tap</span>
    <span v-if="holding" class="absolute inset-x-0 bottom-0 h-1 bg-white/80 rounded-b"
      :style="{ width: holdProgress + '%' }" />
  </button>
</template>
