<!--
  SpectrumWidget.vue
  ------------------
  Compact live spectrum panel for the dashboard. 5 band rows, each
  with a real mini waterfall (tall enough to show rolling history)
  + a live FFT sparkline overlay. Play/pause button freezes the
  display without disconnecting the SSE stream. Click anywhere
  outside the controls → /spectrum for detail.
-->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount, watch, nextTick, reactive } from 'vue'
import { useRouter } from 'vue-router'
import { useSpectrumStore } from '@/stores/spectrum'

// nowTick drives the per-second countdown re-render. Ticking every 1s
// is enough — the calibration window is 30 s so sub-second precision
// would just burn CPU. Shared across all widget instances via module
// scope? No — each mount sets its own interval; Vue tears it down on
// unmount via onBeforeUnmount.
const nowMs = ref(Date.now())

const store = useSpectrumStore()
const router = useRouter()

const BAND_ORDER = ['lora_868', 'aprs_144', 'gps_l1', 'lte_b20_dl', 'lte_b8_dl']
const orderedBands = computed(() => {
  const keys = Object.keys(store.bands)
  return BAND_ORDER.filter(b => keys.includes(b)).concat(
    keys.filter(k => !BAND_ORDER.includes(k)).sort()
  )
})

// Turbo colormap — same curve as the full page so the widget reads
// identically to the detail panel.
function turbo(t) {
  t = Math.max(0, Math.min(1, t))
  const r = 34.61 + t * (1172.33 - t * (10793.56 - t * (33300.12 - t * (38394.49 - t * 14825.05))))
  const g = 23.31 + t * (557.33 + t * (1225.33 - t * (3574.96 - t * (1073.77 + t * 707.56))))
  const b = 27.2 + t * (3211.1 - t * (15327.97 - t * (27814 - t * (22569.18 - t * 6838.66))))
  return [Math.max(0, Math.min(255, r)) | 0,
          Math.max(0, Math.min(255, g)) | 0,
          Math.max(0, Math.min(255, b)) | 0]
}
function normPower(p, base, std) {
  const floor = base - 2 * (std || 1)
  const ceil  = base + 10 * (std || 1)
  if (!isFinite(p)) return 0
  return (p - floor) / (ceil - floor)
}

// How many history rows the widget shows per band. Each row = one scan
// tick (~3 s). 20 rows = ~60 s of history — enough to see an event
// rolling through without making the widget enormous.
const ROWS_PER_BAND = 20

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

// Canvases keyed by band: heat (waterfall) + trace (FFT sparkline).
const heats = reactive({})
const traces = reactive({})

// Upsample width for the widget heatmap — same rationale as the full
// page: sparse rtl_power bins (12-120) stretched via CSS look mushy;
// interpolating to 256 cols gives the mini strip a crisp SDR look.
const WIDGET_COLS = 256

function widgetRange(row, b) {
  if (b.baselineStd > 0) {
    return { floor: b.baselineMean - 2 * b.baselineStd,
             ceil:  b.baselineMean + 10 * b.baselineStd }
  }
  if (!row?.powers?.length) return { floor: -80, ceil: -10 }
  let mn = Infinity, mx = -Infinity
  for (const p of row.powers) {
    if (!isFinite(p)) continue
    if (p < mn) mn = p
    if (p > mx) mx = p
  }
  if (!isFinite(mn) || mn === mx) return { floor: -80, ceil: -10 }
  const pad = (mx - mn) * 0.3
  return { floor: mn - pad, ceil: mx + pad }
}

function drawBand(name) {
  const b = store.bands[name]
  if (!b) return
  const heat = heats[name]
  const trace = traces[name]
  const top = b.rows?.[0]
  const nBins = top?.powers?.length || 1

  if (heat) {
    if (heat.width !== WIDGET_COLS) heat.width = WIDGET_COLS
    if (heat.height !== ROWS_PER_BAND) heat.height = ROWS_PER_BAND
    const ctx = heat.getContext('2d')
    const img = ctx.createImageData(WIDGET_COLS, ROWS_PER_BAND)
    for (let y = 0; y < ROWS_PER_BAND; y++) {
      const row = b.rows[y]
      if (!row?.powers?.length) {
        for (let x = 0; x < WIDGET_COLS; x++) {
          const off = (y * WIDGET_COLS + x) * 4
          img.data[off] = 15; img.data[off + 1] = 15; img.data[off + 2] = 25; img.data[off + 3] = 255
        }
        continue
      }
      const { floor, ceil } = widgetRange(row, b)
      const span = ceil - floor || 1
      const rBins = row.powers.length
      for (let x = 0; x < WIDGET_COLS; x++) {
        const fBin = (x / (WIDGET_COLS - 1)) * (rBins - 1)
        const i0 = Math.floor(fBin)
        const i1 = Math.min(rBins - 1, i0 + 1)
        const frac = fBin - i0
        const p = row.powers[i0] * (1 - frac) + row.powers[i1] * frac
        const t = Math.max(0, Math.min(1, (p - floor) / span))
        const [r, g, bl] = turbo(t)
        const off = (y * WIDGET_COLS + x) * 4
        img.data[off] = r; img.data[off + 1] = g; img.data[off + 2] = bl; img.data[off + 3] = 255
      }
    }
    ctx.putImageData(img, 0, 0)
  }

  // Trace sparkline overlay — current scan, drawn crisply at DPR.
  if (trace && top?.powers?.length) {
    const dpr = Math.min(2, window.devicePixelRatio || 1)
    const cssW = trace.clientWidth || 300
    const cssH = trace.clientHeight || 36
    const W = Math.min(1200, Math.floor(cssW * dpr))
    const H = Math.floor(cssH * dpr)
    if (trace.width !== W) trace.width = W
    if (trace.height !== H) trace.height = H

    const ctx = trace.getContext('2d')
    ctx.clearRect(0, 0, W, H)

    let yTop, yBot
    if (b.baselineStd > 0) {
      yTop = b.baselineMean + 10 * b.baselineStd
      yBot = b.baselineMean - 3 * b.baselineStd
    } else {
      const { floor, ceil } = widgetRange(top, b)
      yTop = ceil; yBot = floor
    }
    const yRange = yTop - yBot
    const yAt = (dB) => ((yTop - dB) / yRange) * H

    // Jamming threshold line
    ctx.strokeStyle = 'rgba(220, 38, 38, 0.55)'
    ctx.setLineDash([3 * dpr, 3 * dpr])
    ctx.lineWidth = 1
    const yJam = yAt(b.threshJamming || (b.baselineMean + 3 * (b.baselineStd || 1)))
    ctx.beginPath(); ctx.moveTo(0, yJam); ctx.lineTo(W, yJam); ctx.stroke()
    ctx.setLineDash([])

    const powers = top.powers
    const n = powers.length
    ctx.strokeStyle = '#7dd3fc'
    ctx.lineWidth = 1.3 * dpr
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
function redrawAll() { for (const n of orderedBands.value) drawBand(n) }

watch(
  () => orderedBands.value.map(n => ({
    n,
    len: store.bands[n]?.rows?.length || 0,
    ts: store.bands[n]?.rows?.[0]?.ts || '',
    st: store.bands[n]?.state || '',
    pz: store.paused,  // redraw when pause toggles so the pause overlay updates
  })),
  async () => { await nextTick(); redrawAll() },
  { deep: true }
)
let tickTimer = null
onMounted(() => {
  store.connect()
  nextTick(redrawAll)
  window.addEventListener('resize', onResize)
  tickTimer = setInterval(() => { nowMs.value = Date.now() }, 1000)
})
onBeforeUnmount(() => {
  if (tickTimer) { clearInterval(tickTimer); tickTimer = null }
  window.removeEventListener('resize', onResize)
})
function onResize() { nextTick(redrawAll) }

// calibrationInfo(bandName) → { active: bool, pct: 0..100, remainingSec: int }
// active=true if this band is the band whose 30s window is live right now.
// pct=0 / remainingSec=null for bands still queued in Phase 1.
function calibrationInfo(name) {
  const b = store.bands[name]
  if (!b || b.state !== 'calibrating') return null
  const started = b.calibrationStartedAt
  const dur = b.calibrationDurationSec || 30
  if (!started) return { active: false, pct: 0, remainingSec: null }
  const elapsed = Math.max(0, (nowMs.value - started.getTime()) / 1000)
  const pct = Math.min(100, (elapsed / dur) * 100)
  const remaining = Math.max(0, Math.ceil(dur - elapsed))
  return { active: true, pct, remainingSec: remaining }
}

function go() { router.push('/spectrum') }
function onPauseClick(e) {
  e.stopPropagation()
  store.togglePause()
}

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
        <span v-if="store.paused"
              class="text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded bg-yellow-900/60 text-yellow-200">
          paused
        </span>
      </div>
      <div class="flex items-center gap-2 text-[10px]">
        <button type="button"
                class="pause-btn"
                :title="store.paused ? 'Resume waterfall' : 'Pause waterfall'"
                @click="onPauseClick"
                @keyup.enter.stop @keyup.space.stop>
          <!-- Play / Pause icon -->
          <svg v-if="!store.paused" viewBox="0 0 16 16" width="12" height="12" aria-label="Pause">
            <rect x="3" y="2" width="3" height="12" fill="currentColor" />
            <rect x="10" y="2" width="3" height="12" fill="currentColor" />
          </svg>
          <svg v-else viewBox="0 0 16 16" width="12" height="12" aria-label="Play">
            <polygon points="3,2 14,8 3,14" fill="currentColor" />
          </svg>
          {{ store.paused ? 'play' : 'pause' }}
        </button>
        <span :class="store.connected ? 'text-emerald-400' : 'text-amber-400'">{{ store.connected ? 'LIVE' : 'IDLE' }}</span>
        <span class="text-gray-500">· click for detail</span>
      </div>
    </div>

    <div class="widget-grid">
      <div v-for="name in orderedBands" :key="name" class="widget-strip">
        <div class="strip-label">
          <span class="dot" :style="{ background: stateColour(store.bands[name]?.state) }" />
          <span>{{ shortLabel(name) }}</span>
        </div>
        <div class="strip-canvases">
          <canvas :ref="el => { if (el) heats[name] = el }" class="strip-heat" />
          <canvas :ref="el => { if (el) traces[name] = el }" class="strip-trace" />
          <!-- Calibration overlay: progress bar + countdown for the
               band whose 30 s window is currently running, or "queued"
               for bands still waiting their turn. -->
          <template v-if="calibrationInfo(name)">
            <div v-if="calibrationInfo(name).active" class="cal-overlay">
              <div class="cal-bar" :style="{ width: calibrationInfo(name).pct + '%' }" />
              <div class="cal-text">
                calibrating · {{ calibrationInfo(name).remainingSec }}s
              </div>
            </div>
            <div v-else class="cal-overlay cal-queued">
              <div class="cal-text">queued</div>
            </div>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.widget-grid {
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.widget-strip {
  display: grid;
  grid-template-columns: 64px 1fr;
  gap: 6px;
  align-items: stretch;
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
.strip-label .dot { width: 6px; height: 6px; border-radius: 50%; display: inline-block; }
.strip-canvases {
  position: relative;
  height: 36px;
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
.cal-overlay {
  position: absolute;
  inset: 0;
  background: rgba(15, 23, 42, 0.78);
  display: flex;
  align-items: center;
  justify-content: flex-end;
  padding: 0 8px;
  pointer-events: none;
  z-index: 3;
  overflow: hidden;
}
.cal-bar {
  position: absolute;
  inset: 0;
  right: auto;
  background: linear-gradient(90deg,
    rgba(99, 102, 241, 0.55) 0%,
    rgba(99, 102, 241, 0.30) 100%);
  transition: width 1s linear;
}
.cal-text {
  position: relative;
  font-family: monospace;
  font-size: 10px;
  color: #e0e7ff;
  letter-spacing: 0.05em;
  font-weight: 600;
}
.cal-queued {
  background: rgba(15, 23, 42, 0.45);
}
.cal-queued .cal-text { color: #94a3b8; font-weight: 500; }
.pause-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  background: #1e293b;
  color: #cbd5e1;
  border: 1px solid #334155;
  border-radius: 3px;
  padding: 2px 8px;
  font-size: 10px;
  letter-spacing: 0.04em;
  cursor: pointer;
  text-transform: uppercase;
  font-weight: 600;
}
.pause-btn:hover { background: #334155; color: white; }
</style>
