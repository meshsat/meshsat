<script setup>
// Power widget for the header: the pack from the X1202 monitor (mains,
// charging, level, voltage) and a menu with restart and power off.
// Both actions are hold-to-confirm (1.5 s) so a visitor's tap on a booth
// panel cannot take a kit down. The facts about the X1202 are stated in
// the menu: a halt leaves the box powered until the UPS button is
// long-pressed, a warm reboot may bring the panel back black. [MESHSAT-831]
import { ref, computed, onMounted, onUnmounted } from 'vue'
import api from '@/api/client'

const props = defineProps({ kit: { type: String, default: '' }, compact: { type: Boolean, default: false } })

const bat = ref(null)     // /api/system/battery
const missing = ref(false)
let timer = null
async function poll() {
  try { bat.value = await api.get('/system/battery'); missing.value = false } catch { missing.value = true }
}

const pct = computed(() => bat.value ? Math.max(0, Math.min(100, Math.round(bat.value.soc_percent))) : null)
const onMains = computed(() => !!(bat.value && bat.value.ac_present))
const full = computed(() => pct.value !== null && pct.value >= 99)
const stale = computed(() => !!(bat.value && bat.value.stale))
const state = computed(() => {
  if (missing.value || !bat.value) return 'no UPS reading'
  if (stale.value) return 'UPS reading stale'
  if (onMains.value) return full.value ? 'mains, full' : 'mains, charging'
  return 'battery'
})
const tone = computed(() => {
  if (missing.value || stale.value) return 'text-gray-500 border-gray-700'
  if (!onMains.value && pct.value !== null && pct.value < 20) return 'text-red-300 border-red-500/50'
  if (onMains.value) return 'text-emerald-300 border-emerald-500/40'
  return 'text-amber-300 border-amber-500/50'
})

// menu
const open = ref(false)
const busy = ref('')
const note = ref('')
const hold = ref('')        // which action is being held
const holdPct = ref(0)
let holdTimer = null, holdStart = 0
const HOLD_MS = 1500
function holdStartFor(action) {
  hold.value = action; holdStart = performance.now(); holdPct.value = 0
  const tick = () => {
    if (hold.value !== action) return
    const k = Math.min(1, (performance.now() - holdStart) / HOLD_MS)
    holdPct.value = Math.round(k * 100)
    if (k >= 1) { hold.value = ''; fire(action); return }
    holdTimer = requestAnimationFrame(tick)
  }
  holdTimer = requestAnimationFrame(tick)
}
function holdEnd() { if (holdTimer) cancelAnimationFrame(holdTimer); hold.value = ''; holdPct.value = 0 }
async function fire(action) {
  busy.value = action; note.value = ''
  try {
    const r = await api.post('/system/power', { action, delay_secs: 5, confirm: true })
    note.value = action === 'poweroff'
      ? `Powering off in ${r.delay_secs} s. The UPS keeps the box powered: long-press its button to cut power.`
      : `Restarting in ${r.delay_secs} s. If the panel comes back black, cycle the UPS button.`
  } catch (e) {
    note.value = (e && e.message) || 'the kit did not accept the request'
    busy.value = ''
  }
}
function toggle() { open.value = !open.value; if (!open.value) { note.value = ''; busy.value = '' } }
function onKey(e) { if (e.key === 'Escape') open.value = false }

onMounted(() => { poll(); timer = setInterval(poll, 10000); window.addEventListener('keydown', onKey) })
onUnmounted(() => { clearInterval(timer); window.removeEventListener('keydown', onKey); holdEnd() })
</script>

<template>
  <div class="relative">
    <button type="button" @click="toggle" :title="state"
      class="power-chip flex items-center gap-2 h-8 px-2 rounded border bg-gray-900/60 font-mono text-xs" :class="tone" aria-haspopup="dialog" :aria-expanded="open">
      <!-- battery glyph, filled to the level; plug when on mains -->
      <svg viewBox="0 0 34 16" class="w-8 h-4" aria-hidden="true">
        <rect x="1" y="2" width="28" height="12" rx="2.5" fill="none" stroke="currentColor" stroke-width="1.5" />
        <rect x="30" y="5.5" width="3" height="5" rx="1" fill="currentColor" />
        <rect v-if="pct !== null" x="3" y="4" :width="Math.max(1, 24 * pct / 100)" height="8" rx="1" fill="currentColor" opacity="0.85" />
        <path v-if="onMains" d="M14 3 L11 9 h4 l-2 5 5 -7 h-4 z" fill="#040406" stroke="#040406" stroke-width="0.6" />
      </svg>
      <span v-if="pct !== null" class="tabular-nums">{{ pct }}%</span>
      <span v-if="!compact" class="text-gray-400">{{ state }}</span>
    </button>

    <Teleport to="body">
      <div v-if="open" class="fixed inset-0 z-[10001]" @click.self="toggle">
        <section class="absolute top-16 right-4 w-[360px] max-w-[calc(100vw-2rem)] rounded-lg border border-gray-700 bg-gray-900 shadow-2xl p-4 font-sans" role="dialog" aria-label="Power">
          <div class="flex items-baseline justify-between">
            <h2 class="font-display text-lg text-gray-50">{{ kit || 'Power' }}</h2>
            <button type="button" @click="toggle" class="font-mono text-xs text-gray-400 hover:text-gray-100 px-2 py-1">close</button>
          </div>
          <dl class="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 mt-3 text-sm">
            <dt class="text-gray-400">Supply</dt><dd class="text-gray-100">{{ missing ? 'no UPS reading on this kit' : (onMains ? 'mains connected' : 'on battery') }}</dd>
            <dt class="text-gray-400">Pack</dt><dd class="text-gray-100">{{ pct !== null ? `${pct}%` : '' }}<span v-if="bat && bat.voltage" class="text-gray-400"> at {{ bat.voltage.toFixed(2) }} V</span></dd>
            <dt class="text-gray-400">Charging</dt><dd class="text-gray-100">{{ missing ? '' : (onMains ? (full ? 'full, holding' : 'yes') : 'no, discharging') }}</dd>
            <dt v-if="stale" class="text-amber-300">Reading</dt><dd v-if="stale" class="text-amber-300">stale, the UPS monitor has not written for a minute</dd>
          </dl>

          <div class="mt-4 space-y-2">
            <button type="button" class="hold w-full text-left rounded border px-3 py-2 relative overflow-hidden select-none"
              :class="busy === 'reboot' ? 'border-teal-500 text-teal-300' : 'border-gray-700 text-gray-100'"
              :disabled="!!busy"
              @pointerdown.prevent="holdStartFor('reboot')" @pointerup="holdEnd" @pointercancel="holdEnd" @pointerleave="holdEnd">
              <span class="hold-fill absolute inset-y-0 left-0 bg-teal-500/25" :style="{ width: (hold === 'reboot' ? holdPct : 0) + '%' }" />
              <span class="relative block font-display text-base">Restart the kit</span>
              <span class="relative block text-xs text-gray-400">hold for a second and a half; warm reboot, about 90 s</span>
            </button>
            <button type="button" class="hold w-full text-left rounded border px-3 py-2 relative overflow-hidden select-none"
              :class="busy === 'poweroff' ? 'border-red-500 text-red-300' : 'border-gray-700 text-gray-100'"
              :disabled="!!busy"
              @pointerdown.prevent="holdStartFor('poweroff')" @pointerup="holdEnd" @pointercancel="holdEnd" @pointerleave="holdEnd">
              <span class="hold-fill absolute inset-y-0 left-0 bg-red-500/25" :style="{ width: (hold === 'poweroff' ? holdPct : 0) + '%' }" />
              <span class="relative block font-display text-base">Power off the kit</span>
              <span class="relative block text-xs text-gray-400">hold for a second and a half; the UPS keeps the box powered until its button is long-pressed</span>
            </button>
          </div>
          <p v-if="note" class="mt-3 text-sm" :class="busy ? 'text-gray-100' : 'text-red-300'">{{ note }}</p>
        </section>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.hold-fill { transition: width 60ms linear; }
.power-chip { transition: color 0.3s, border-color 0.3s; }
</style>
