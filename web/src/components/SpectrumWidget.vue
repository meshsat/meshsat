<!--
  SpectrumWidget.vue
  ------------------
  Compact single-canvas waterfall for the dashboard. All 5 monitored
  bands rendered as horizontal strips inside ONE canvas, stacked
  vertically. Height is fixed (~140 px) so the widget doesn't push
  the dashboard around.

  Rendered once per scan event; click routes to /spectrum for the
  full detail view with per-band panels and annotations.

  Hides itself entirely when the RTL-SDR isn't present — avoids
  confusing the operator with a dead widget.
-->
<script setup>
import { ref, computed, onMounted, watch, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { useSpectrumStore } from '@/stores/spectrum'

const store = useSpectrumStore()
const router = useRouter()

const canvas = ref(null)

const BAND_ORDER = ['lora_868', 'aprs_144', 'gps_l1', 'lte_b20_dl', 'lte_b8_dl']
const orderedBands = computed(() => {
  const keys = Object.keys(store.bands)
  return BAND_ORDER.filter(b => keys.includes(b)).concat(
    keys.filter(k => !BAND_ORDER.includes(k)).sort()
  )
})

// Worst-state is the widget's border colour. If any band is jamming,
// the whole widget is red. This is the at-a-glance signal from across
// the room.
const worstState = computed(() => {
  let worst = 'clear'
  for (const n of orderedBands.value) {
    const s = store.bands[n]?.state
    if (s === 'jamming') return 'jamming'
    if (s === 'interference' && worst !== 'jamming') worst = 'interference'
    if (s === 'calibrating' && worst === 'clear') worst = 'calibrating'
  }
  return worst
})

// Badge counts live unacked alerts — may differ from visible modal
// alerts when the popup is globally disabled or bands are muted.
const unackedCount = computed(() => store.alerts.filter(a => !a.acked).length)

function colourFor(power, baselineMean, baselineStd, threshJam) {
  if (!isFinite(power)) return [0, 0, 0]
  const delta = power - baselineMean
  if (delta < -baselineStd) return [8, 12, 40]
  if (delta < baselineStd) {
    const t = (delta + baselineStd) / (2 * baselineStd)
    return [Math.round(8 + t * 30), Math.round(12 + t * 80), Math.round(40 + t * 120)]
  }
  if (power < threshJam) {
    const span = threshJam - (baselineMean + baselineStd)
    const t = span > 0 ? (power - (baselineMean + baselineStd)) / span : 0
    return [Math.round(t * 220), Math.round(180 + t * 60), Math.round(60 - t * 60)]
  }
  const over = Math.min(1, (power - threshJam) / 10)
  return [220 + Math.round(over * 35), Math.round(40 - over * 30), Math.round(40 - over * 30)]
}

// Per-band strip height in canvas pixels. Keep thin enough that 5
// strips fit in a compact widget.
const STRIP_H = 22
const STRIP_GAP = 2
// How many history columns to render inside each strip. Newest on the
// right so the visual scroll direction matches a conventional strip
// chart.
const HIST_COLS = 80

function redraw() {
  const c = canvas.value
  if (!c) return
  const names = orderedBands.value
  const H = names.length * STRIP_H + Math.max(0, names.length - 1) * STRIP_GAP
  const W = HIST_COLS
  if (c.width !== W) c.width = W
  if (c.height !== H) c.height = H

  const ctx = c.getContext('2d')
  if (!ctx) return
  const img = ctx.createImageData(W, H)

  // Fill default dark background
  for (let i = 0; i < img.data.length; i += 4) {
    img.data[i] = 12
    img.data[i + 1] = 16
    img.data[i + 2] = 28
    img.data[i + 3] = 255
  }

  for (let bi = 0; bi < names.length; bi++) {
    const name = names[bi]
    const b = store.bands[name]
    const yTop = bi * (STRIP_H + STRIP_GAP)
    if (!b) continue
    const base = b.baselineMean
    const std = b.baselineStd || 1
    const threshJam = b.threshJamming || (base + 3 * std)

    // Draw newest on the right: iterate rows (newest first) right-to-left.
    for (let col = 0; col < HIST_COLS; col++) {
      const row = b.rows[col]
      // For each row we collapse all bins to the peak power (what
      // matters for jamming detection anyway — a single jammed bin
      // should light up the strip). This keeps the widget compact
      // without burying a narrowband jammer in an averaged column.
      let power = null
      if (row && row.powers && row.powers.length > 0) {
        power = row.max != null ? row.max : Math.max(...row.powers)
      }
      const [r, g, bl] = power == null ? [20, 20, 20] : colourFor(power, base, std, threshJam)
      // Column index in the image: right-most is col=0
      const imgCol = HIST_COLS - 1 - col
      for (let y = yTop; y < yTop + STRIP_H && y < H; y++) {
        const off = (y * W + imgCol) * 4
        img.data[off] = r
        img.data[off + 1] = g
        img.data[off + 2] = bl
        img.data[off + 3] = 255
      }
    }
  }
  ctx.putImageData(img, 0, 0)
}

watch(
  () => orderedBands.value.map(n => ({
    n,
    len: store.bands[n]?.rows?.length || 0,
    ts: store.bands[n]?.rows?.[0]?.ts || '',
    st: store.bands[n]?.state || '',
  })),
  async () => { await nextTick(); redraw() },
  { deep: true }
)

onMounted(() => {
  store.connect()
  nextTick(redraw)
})

function go() {
  router.push('/spectrum')
}

const borderClass = computed(() => {
  switch (worstState.value) {
    case 'jamming': return 'border-red-500 shadow-[0_0_12px_rgba(220,38,38,0.5)]'
    case 'interference': return 'border-amber-500'
    case 'calibrating': return 'border-indigo-500'
    default: return 'border-tactical-border'
  }
})
</script>

<template>
  <div v-if="store.enabled"
       :class="['bg-tactical-surface rounded-lg border p-3 cursor-pointer hover:bg-tactical-surface/80 transition-colors', borderClass]"
       role="button" tabindex="0"
       @click="go" @keyup.enter="go" @keyup.space="go">
    <div class="flex items-center justify-between mb-2">
      <div class="flex items-center gap-2">
        <span class="font-display font-semibold text-sm tracking-wide"
              :class="{
                'text-red-400': worstState === 'jamming',
                'text-amber-400': worstState === 'interference',
                'text-indigo-400': worstState === 'calibrating',
                'text-emerald-400': worstState === 'clear',
              }">SPECTRUM</span>
        <span class="text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded"
              :class="{
                'bg-red-900/40 text-red-300': worstState === 'jamming',
                'bg-amber-900/40 text-amber-300': worstState === 'interference',
                'bg-indigo-900/40 text-indigo-300': worstState === 'calibrating',
                'bg-emerald-900/40 text-emerald-300': worstState === 'clear',
              }">{{ worstState }}</span>
        <span v-if="unackedCount > 0"
              class="text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded bg-red-900/60 text-red-200">
          {{ unackedCount }} unacked
        </span>
      </div>
      <div class="flex items-center gap-2 text-[10px] text-gray-500">
        <span>{{ store.connected ? 'LIVE' : 'IDLE' }}</span>
        <span>· click for detail</span>
      </div>
    </div>
    <!-- Single canvas, thin strips per band. image-rendering:pixelated
         keeps the per-column blocks crisp when the browser scales
         the canvas to fit the widget width. -->
    <canvas ref="canvas" class="w-full block rounded" style="image-rendering: pixelated; height: 130px;" />
    <div class="flex items-center justify-between mt-2 text-[10px] text-gray-500">
      <span>LoRa · APRS · GPS · LTE20 · LTE8</span>
      <span>RTL-SDR · baseline-relative</span>
    </div>
  </div>
</template>
