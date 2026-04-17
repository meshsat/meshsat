<!--
  SpectrumWidget.vue
  ------------------
  Compact at-a-glance spectrum strip for the dashboard. Each of the
  5 bands is rendered as a thin row: a mini FFT trace (sparkline
  reading current scan) overlaid on a tiny Turbo-colormapped history
  heatmap. Click anywhere → /spectrum for full detail.

  Kept deliberately small (~170 px total) so it doesn't shove the
  other dashboard widgets around, but the trace + heatmap together
  look more like a real SDR analyzer than a blocky tile grid.
-->
<script setup>
import { ref, computed, onMounted, watch, nextTick, reactive } from 'vue'
import { useRouter } from 'vue-router'
import { useSpectrumStore } from '@/stores/spectrum'

const store = useSpectrumStore()
const router = useRouter()

const BAND_ORDER = ['lora_868', 'aprs_144', 'gps_l1', 'lte_b20_dl', 'lte_b8_dl']
const orderedBands = computed(() => {
  const keys = Object.keys(store.bands)
  return BAND_ORDER.filter(b => keys.includes(b)).concat(
    keys.filter(k => !BAND_ORDER.includes(k)).sort()
  )
})

// Turbo colormap (shared identically with SpectrumWaterfall.vue — duplicated
// here to avoid coupling the widget to the waterfall component's scope).
function turbo(t) {
  t = Math.max(0, Math.min(1, t))
  const r = 34.61 + t * (1172.33 - t * (10793.56 - t * (33300.12 - t * (38394.49 - t * 14825.05))))
  const g = 23.31 + t * (557.33 + t * (1225.33 - t * (3574.96 - t * (1073.77 + t * 707.56))))
  const b = 27.2 + t * (3211.1 - t * (15327.97 - t * (27814 - t * (22569.18 - t * 6838.66))))
  return [Math.max(0, Math.min(255, r)) | 0,
          Math.max(0, Math.min(255, g)) | 0,
          Math.max(0, Math.min(255, b)) | 0]
}
function normPower(power, baseMean, baseStd) {
  const floor = baseMean - 2 * (baseStd || 1)
  const ceil  = baseMean + 10 * (baseStd || 1)
  if (!isFinite(power)) return 0
  return (power - floor) / (ceil - floor)
}

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

const unackedCount = computed(() => store.alerts.filter(a => !a.acked).length)

// Per-band thin canvas — one for the FFT trace, one for the waterfall
// mini-strip. We use two canvases so we can interpolate the waterfall
// with CSS scaling (smooth) while keeping the trace crisp.
const traceCanvases = reactive({})
const heatmapCanvases = reactive({})

const STRIP_HEIGHT_CSS = 28
const HISTORY_COLS = 90

function drawBand(name) {
  const b = store.bands[name]
  const trace = traceCanvases[name]
  const heat  = heatmapCanvases[name]
  if (!b) return
  const top = b.rows?.[0]
  const nBins = top?.powers?.length || 1
  const dpr = Math.min(2, window.devicePixelRatio || 1)

  // Heatmap: newest column on the right
  if (heat) {
    if (heat.width !== HISTORY_COLS) heat.width = HISTORY_COLS
    if (heat.height !== 1) heat.height = 1
    const ctx = heat.getContext('2d')
    const img = ctx.createImageData(HISTORY_COLS, 1)
    const base = b.baselineMean, std = b.baselineStd || 1
    for (let col = 0; col < HISTORY_COLS; col++) {
      const row = b.rows[col]
      let power = null
      if (row && row.powers && row.powers.length > 0) {
        power = row.max != null ? row.max : Math.max(...row.powers)
      }
      const [r, g, bl] = power == null
        ? [15, 15, 25]
        : turbo(normPower(power, base, std))
      const imgCol = HISTORY_COLS - 1 - col
      const off = imgCol * 4
      img.data[off] = r
      img.data[off + 1] = g
      img.data[off + 2] = bl
      img.data[off + 3] = 255
    }
    ctx.putImageData(img, 0, 0)
  }

  // Trace: crisp sparkline, scaled to DPR. Spans the strip width.
  if (trace && top?.powers?.length) {
    const cssW = trace.clientWidth || 300
    const cssH = STRIP_HEIGHT_CSS
    const W = Math.min(1200, Math.floor(cssW * dpr))
    const H = Math.floor(cssH * dpr)
    if (trace.width !== W) trace.width = W
    if (trace.height !== H) trace.height = H

    const ctx = trace.getContext('2d')
    ctx.clearRect(0, 0, W, H)

    const yTop = b.baselineMean + 10 * (b.baselineStd || 1)
    const yBot = b.baselineMean - 3 * (b.baselineStd || 1)
    const yRange = yTop - yBot
    const yAt = (dB) => ((yTop - dB) / yRange) * H

    // Threshold lines (thin, semi-transparent)
    ctx.strokeStyle = 'rgba(220, 38, 38, 0.7)'
    ctx.setLineDash([3 * dpr, 3 * dpr])
    ctx.lineWidth = 1
    ctx.beginPath()
    const yJam = yAt(b.threshJamming || (b.baselineMean + 3 * (b.baselineStd || 1)))
    ctx.moveTo(0, yJam); ctx.lineTo(W, yJam)
    ctx.stroke()
    ctx.setLineDash([])

    // Trace
    const powers = top.powers
    const n = powers.length
    ctx.strokeStyle = '#7dd3fc'
    ctx.lineWidth = 1.1 * dpr
    ctx.lineJoin = 'round'
    ctx.beginPath()
    for (let i = 0; i < n; i++) {
      const x = (i + 0.5) * (W / n)
      const y = yAt(powers[i])
      if (i === 0) ctx.moveTo(x, y)
      else ctx.lineTo(x, y)
    }
    ctx.stroke()
  }
}

function redrawAll() {
  for (const n of orderedBands.value) drawBand(n)
}

watch(
  () => orderedBands.value.map(n => ({
    n,
    len: store.bands[n]?.rows?.length || 0,
    ts: store.bands[n]?.rows?.[0]?.ts || '',
    st: store.bands[n]?.state || '',
  })),
  async () => { await nextTick(); redrawAll() },
  { deep: true }
)

onMounted(() => {
  store.connect()
  nextTick(redrawAll)
  window.addEventListener('resize', onResize)
})
function onResize() { nextTick(redrawAll) }

function go() { router.push('/spectrum') }

function stateColour(state) {
  switch (state) {
    case 'jamming': return '#dc2626'
    case 'interference': return '#f59e0b'
    case 'clear': return '#10b981'
    case 'calibrating': return '#6366f1'
    default: return '#6b7280'
  }
}

const borderClass = computed(() => {
  switch (worstState.value) {
    case 'jamming': return 'border-red-500 shadow-[0_0_12px_rgba(220,38,38,0.5)]'
    case 'interference': return 'border-amber-500'
    case 'calibrating': return 'border-indigo-500'
    default: return 'border-tactical-border'
  }
})

function shortLabel(name) {
  const map = {
    lora_868: 'LoRa 868',
    aprs_144: 'APRS 2m',
    gps_l1: 'GPS L1',
    lte_b20_dl: 'LTE-20',
    lte_b8_dl: 'LTE-8',
  }
  return map[name] || name
}
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
              }">RF SPECTRUM</span>
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
        <span :class="store.connected ? 'text-emerald-400' : 'text-amber-400'">{{ store.connected ? 'LIVE' : 'IDLE' }}</span>
        <span>· click for detail</span>
      </div>
    </div>

    <div class="widget-grid">
      <div v-for="name in orderedBands" :key="name" class="widget-strip">
        <div class="strip-label">
          <span class="dot" :style="{ background: stateColour(store.bands[name]?.state) }" />
          <span>{{ shortLabel(name) }}</span>
        </div>
        <div class="strip-canvases">
          <!-- Heatmap fills the row; trace overlays on top -->
          <canvas :ref="el => { if (el) heatmapCanvases[name] = el }" class="strip-heat" />
          <canvas :ref="el => { if (el) traceCanvases[name] = el }" class="strip-trace" />
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.widget-grid {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.widget-strip {
  display: grid;
  grid-template-columns: 64px 1fr;
  gap: 6px;
  align-items: center;
}
.strip-label {
  display: flex;
  align-items: center;
  gap: 4px;
  font-family: monospace;
  font-size: 10px;
  color: #94a3b8;
  letter-spacing: 0.04em;
}
.strip-label .dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
}
.strip-canvases {
  position: relative;
  height: 28px;
  width: 100%;
  background: #020617;
  border-radius: 2px;
  overflow: hidden;
}
.strip-heat {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  image-rendering: auto;
}
.strip-trace {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  pointer-events: none;
}
</style>
