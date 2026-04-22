<!--
  SpectrumBandDetailView.vue
  --------------------------
  Per-band historical waterfall for the RF Spectrum Monitor
  (MESHSAT-650). Loads persisted scan rows + state transitions over a
  configurable time window (default 6 h) and renders a tall waterfall
  + time axis + alert-marker overlay.

  Design choices worth knowing:

  - Server-side row cap is 2000 (5000 hard max). The detail view can
    ask for a 7-day window; that's ~20k candidate rows, so the server
    already truncates by newest-first. Once we have rows, we render
    directly into canvas pixels — bin count varies per band (12-120),
    so we upsample horizontally via linear interpolation just like
    the live main-page waterfall.

  - Palette range is per-row MAD (same as SpectrumWaterfall.vue after
    MESHSAT-649). A jamming row has a high peak but a low MAD (the
    jammer is narrowband, the rest of the row is noise floor), so
    the hot spike still reads as deep red against a cool floor.

  - Alert overlay: each spectrum_transitions row becomes a horizontal
    line at its timestamp. Red for jamming, amber for interference,
    cyan for clear-after-event. A small label on the right edge shows
    which state the line marks so the operator doesn't have to decode
    by colour alone.

  - Zoom/range: buttons for 5 min / 1 h / 6 h / 24 h. Custom range
    uses two datetime-local inputs. Changing any control refetches
    history + transitions; no live updates on this page (the main
    page owns the SSE connection).
-->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount, watch, nextTick, reactive } from 'vue'
import { useRouter } from 'vue-router'
import { useSpectrumStore } from '@/stores/spectrum'

// `modal: true` = embedded in SpectrumBandDetailModal. We drop the Back
// button (modal has its own close button), change the root sizing from
// min-height:100vh to fill the modal panel, and skip the router push.
// When standalone (default), the view keeps its route-style chrome for
// deep-link access.
const props = defineProps({
  band: { type: String, required: true },
  modal: { type: Boolean, default: false },
})
const emit = defineEmits(['request-close'])
const router = useRouter()
const store = useSpectrumStore()

// Time-window presets. 6 h is the default per MESHSAT-650; the 24 h
// preset matches the shipped retention floor, so operators always see
// everything the bridge still has unless they've bumped retention.
const PRESETS = [
  { key: '5m',  label: '5 min',  minutes: 5 },
  { key: '1h',  label: '1 hour', minutes: 60 },
  { key: '6h',  label: '6 hours', minutes: 6 * 60 },
  { key: '24h', label: '24 hours', minutes: 24 * 60 },
]
const preset = ref('6h')
const customFromMs = ref(null)
const customToMs = ref(null)

const bandMeta = computed(() => store.bands[props.band]?.meta || null)
const bandLabel = computed(() => bandMeta.value?.label || props.band)

// Live status is polled off the main-page store so the detail-view
// header (baseline, state badge) stays fresh without this view
// opening its own SSE stream — SSE is owned by SpectrumWaterfall.
const bandState = computed(() => store.bands[props.band]?.state || 'calibrating')
const bandBaselineMean = computed(() => store.bands[props.band]?.baselineMean || 0)
const bandBaselineStd = computed(() => store.bands[props.band]?.baselineStd || 0)

// Resolve the window to (fromMs, toMs) regardless of which UI
// control the operator is using. `now` is captured once per fetch to
// prevent a drifting "to" value that would invalidate cached rows.
function resolveWindow() {
  if (preset.value === 'custom' && customFromMs.value && customToMs.value) {
    return { from: customFromMs.value, to: customToMs.value }
  }
  const p = PRESETS.find(x => x.key === preset.value) || PRESETS[2]
  const to = Date.now()
  const from = to - p.minutes * 60 * 1000
  return { from, to }
}

const scanRows = ref([])       // newest-first, from LoadScansRange
const transitions = ref([])    // newest-first, from LoadTransitionsRange
const loading = ref(false)
const loadError = ref('')

// Canvas refs. Waterfall height is dynamic — the canvas fills its
// flex container, which is the viewport-minus-chrome when standalone
// and ~70vh when embedded in the modal. Spectrum overlay stays a
// fixed 140 px (detail on a trace more than 140 px tall isn't useful
// — we'd just be stretching 25 dB of y-axis vertically).
const WATERFALL_COLS = 1024
const SPECTRUM_H = 140
const waterfallH = ref(640) // set from the element's live clientHeight
const waterfallCanvas = ref(null)
const spectrumCanvas = ref(null)
let resizeObs = null
const hover = reactive({ inside: false, x: 0, y: 0, freqHz: 0, power: 0, ts: null })

// ---- Turbo colormap (copied from SpectrumWaterfall.vue so detail
//      page is self-contained; colormap rarely changes and keeping
//      them in lock-step is easier as a copy). ----
function turbo(t) {
  t = Math.max(0, Math.min(1, t))
  const r = 34.61 + t * (1172.33 - t * (10793.56 - t * (33300.12 - t * (38394.49 - t * 14825.05))))
  const g = 23.31 + t * (557.33 + t * (1225.33 - t * (3574.96 - t * (1073.77 + t * 707.56))))
  const b = 27.2 + t * (3211.1 - t * (15327.97 - t * (27814 - t * (22569.18 - t * 6838.66))))
  return [Math.max(0, Math.min(255, r)) | 0,
          Math.max(0, Math.min(255, g)) | 0,
          Math.max(0, Math.min(255, b)) | 0]
}

function rowPaletteRange(powers) {
  if (!powers?.length) return { floor: -80, ceil: -10 }
  const finite = powers.filter(p => Number.isFinite(p))
  if (finite.length === 0) return { floor: -80, ceil: -10 }
  const sorted = finite.slice().sort((a, b) => a - b)
  const median = sorted[Math.floor(sorted.length / 2)]
  const absDev = sorted.map(p => Math.abs(p - median)).sort((a, b) => a - b)
  const mad = absDev[Math.floor(absDev.length / 2)]
  const scale = Math.max(1.4826 * mad, 0.5)
  return { floor: median - 2 * scale, ceil: median + 10 * scale }
}

// drawWaterfall paints the full scan array into the canvas. Row 0 at
// the top = newest. We letterbox the Y axis: if rows fill less than
// canvas height, top portion is dark; more than canvas height and we
// stride (skip rows) so the oldest still shows at the bottom. That's
// a form of visual downsampling but cheaper than median-aggregation
// for the common case where the server cap (2000) yields a similar
// aspect to our 640-row canvas.
function drawWaterfall() {
  const canvas = waterfallCanvas.value
  if (!canvas) return
  const H = Math.max(60, Math.floor(waterfallH.value))
  canvas.width = WATERFALL_COLS
  canvas.height = H

  const ctx = canvas.getContext('2d')
  const img = ctx.createImageData(WATERFALL_COLS, H)
  const rows = scanRows.value
  if (rows.length === 0) {
    for (let i = 0; i < img.data.length; i += 4) {
      img.data[i] = 10; img.data[i+1] = 14; img.data[i+2] = 26; img.data[i+3] = 255
    }
    ctx.putImageData(img, 0, 0)
    return
  }

  // Time-proportional row placement. Canvas y=0 is the newest edge of
  // the visible window (≈ now); y=H is the oldest edge (fromMs). Each
  // row sits at its true timestamp so a fresh-deploy kit with only a
  // handful of rows in a 6-hour window correctly shows a thin band of
  // colour at the very top + dark below — a lie would be stretching
  // 8 rows to fill 640 px and suggesting we have 6 hours of data.
  const { from, to } = resolveWindow()
  const windowSpan = to - from || 1
  const newestTs = +new Date(rows[0].ts || rows[0].TS)

  // Prefill the whole canvas as "no data" so any y uncovered by a row
  // stays dark charcoal.
  for (let i = 0; i < img.data.length; i += 4) {
    img.data[i] = 10; img.data[i+1] = 14; img.data[i+2] = 26; img.data[i+3] = 255
  }

  // Paint each row as a horizontal band from its own time down to the
  // NEXT (older) row's time. Clamped so a single isolated row still
  // gets a visible 2 px-ish band and a pileup of rows doesn't draw a
  // 1 px-tall band.
  for (let r = 0; r < rows.length; r++) {
    const row = rows[r]
    const powers = row?.powers || row?.Powers || []
    if (powers.length === 0) continue
    const ts = +new Date(row.ts || row.TS)
    const tsNext = r + 1 < rows.length
      ? +new Date(rows[r + 1].ts || rows[r + 1].TS)
      : ts - 1000 // 1 s slice for the very last row

    // Map timestamps → canvas Y. to - ts is "how old"; scale by span.
    let yTop = Math.floor(((to - ts) / windowSpan) * H)
    let yBot = Math.floor(((to - tsNext) / windowSpan) * H)
    if (yBot <= yTop) yBot = yTop + 1
    // Keep thickness sensible: minimum 2 px (thin-but-visible), max
    // 10 % of canvas (prevents one old row from dominating the view
    // after a long gap).
    if (yBot - yTop < 2) yBot = yTop + 2
    const maxThick = Math.max(4, Math.floor(H / 10))
    if (yBot - yTop > maxThick) yBot = yTop + maxThick
    if (yTop < 0) yTop = 0
    if (yBot > H) yBot = H
    if (yTop >= H) continue

    const { floor, ceil } = rowPaletteRange(powers)
    const span = ceil - floor || 1
    const nBins = powers.length
    for (let y = yTop; y < yBot; y++) {
      for (let x = 0; x < WATERFALL_COLS; x++) {
        const fBin = (x / (WATERFALL_COLS - 1)) * (nBins - 1)
        const i0 = Math.floor(fBin)
        const i1 = Math.min(nBins - 1, i0 + 1)
        const frac = fBin - i0
        const p = powers[i0] * (1 - frac) + powers[i1] * frac
        const t = Math.max(0, Math.min(1, (p - floor) / span))
        const [r2, g2, b2] = turbo(t)
        const off = (y * WATERFALL_COLS + x) * 4
        img.data[off] = r2
        img.data[off + 1] = g2
        img.data[off + 2] = b2
        img.data[off + 3] = 255
      }
    }
  }
  ctx.putImageData(img, 0, 0)
  // Silence the otherwise-unused newestTs — it's handy when debugging
  // the oldest-row placement; keep the reference so lint doesn't drop it.
  void newestTs
}

// drawSpectrumOverlay renders the most-recent scan's trace above the
// waterfall so the operator can correlate the current FFT shape with
// the history band. Matches the main-page spectrum panel but without
// the three reference lines (baseline might have moved since the
// oldest row was taken, so hardcoding a single line would mislead).
function drawSpectrumOverlay() {
  const canvas = spectrumCanvas.value
  if (!canvas) return
  const W = canvas.clientWidth || 1024
  const H = SPECTRUM_H
  canvas.width = W
  canvas.height = H

  const ctx = canvas.getContext('2d')
  ctx.clearRect(0, 0, W, H)

  const rows = scanRows.value
  const top = rows[0]
  const powers = top?.powers || top?.Powers
  if (!powers?.length) return

  // Per-row min/max with a minimum 4 dB visible span — prevents the
  // trace from looking like a flat dead line when the band is locked-
  // carrier quiet (powers vary by <1 dB). Matches the main-page panel
  // logic in SpectrumWaterfall.vue::spectrumYRange. [MESHSAT-651]
  const mnRaw = Math.min(...powers)
  const mxRaw = Math.max(...powers)
  const dataSpan = mxRaw - mnRaw
  const MIN_SPAN_DB = 4
  const span = Math.max(dataSpan, MIN_SPAN_DB)
  const pad = Math.max(span * 0.3, 1.5)
  const midPoint = (mnRaw + mxRaw) / 2
  const halfSpan = span / 2
  const yTop = midPoint + halfSpan + pad
  const yBot = midPoint - halfSpan - pad
  const yRange = yTop - yBot
  const yAt = (p) => ((yTop - p) / yRange) * H

  // Fill
  const fillGrad = ctx.createLinearGradient(0, 0, 0, H)
  fillGrad.addColorStop(0, 'rgba(239, 68, 68, 0.45)')
  fillGrad.addColorStop(0.5, 'rgba(251, 191, 36, 0.30)')
  fillGrad.addColorStop(1, 'rgba(16, 185, 129, 0.05)')
  ctx.fillStyle = fillGrad
  ctx.beginPath()
  const xStep = W / powers.length
  ctx.moveTo(0, H)
  for (let i = 0; i < powers.length; i++) {
    ctx.lineTo(i * xStep + xStep/2, yAt(powers[i]))
  }
  ctx.lineTo(W, H)
  ctx.closePath()
  ctx.fill()

  // Trace line
  ctx.strokeStyle = '#60a5fa'
  ctx.lineWidth = 1.4
  ctx.beginPath()
  for (let i = 0; i < powers.length; i++) {
    const x = i * xStep + xStep/2
    const y = yAt(powers[i])
    if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y)
  }
  ctx.stroke()
}

// transitionMarkers maps each persisted transition to a Y coordinate
// on the waterfall canvas + a colour. The waterfall row index for a
// given timestamp is `(rows.length - 1) - (ts - oldestTs) / totalSpan *
// (rows.length - 1)` but we render against CANVAS pixels, so we map
// timestamp → canvas Y directly using the window bounds.
const transitionMarkers = computed(() => {
  if (scanRows.value.length === 0) return []
  const newestTs = +new Date(scanRows.value[0].ts || scanRows.value[0].TS)
  const oldestTs = +new Date(scanRows.value[scanRows.value.length - 1].ts || scanRows.value[scanRows.value.length - 1].TS)
  const span = newestTs - oldestTs || 1
  const out = []
  for (const t of transitions.value) {
    const ts = +new Date(t.ts || t.TS)
    if (ts < oldestTs || ts > newestTs) continue
    const yPct = ((newestTs - ts) / span) * 100 // 0% = newest row at top
    let color = 'rgba(148, 163, 184, 0.75)'
    if (t.new_state === 'jamming' || t.NewState === 'jamming') color = 'rgba(220, 38, 38, 0.85)'
    else if (t.new_state === 'interference' || t.NewState === 'interference') color = 'rgba(245, 158, 11, 0.85)'
    else if (t.new_state === 'degraded' || t.NewState === 'degraded') color = 'rgba(234, 179, 8, 0.85)'
    else if (t.new_state === 'clear' || t.NewState === 'clear') color = 'rgba(52, 211, 153, 0.85)'
    out.push({
      yPct,
      color,
      state: t.new_state || t.NewState,
      oldState: t.old_state || t.OldState,
      peakDB: t.peak_db ?? t.PeakDB,
      peakFreqHz: t.peak_freq_hz ?? t.PeakFreqHz,
      ts,
    })
  }
  return out
})

// Time-axis tick labels along the right side of the waterfall. Five
// ticks keeps it readable without clutter.
const timeTicks = computed(() => {
  if (scanRows.value.length === 0) return []
  const newest = +new Date(scanRows.value[0].ts || scanRows.value[0].TS)
  const oldest = +new Date(scanRows.value[scanRows.value.length - 1].ts || scanRows.value[scanRows.value.length - 1].TS)
  const span = newest - oldest
  if (span <= 0) return []
  const out = []
  for (let i = 0; i <= 5; i++) {
    const ts = newest - (span * i) / 5
    out.push({ yPct: (i / 5) * 100, label: fmtTick(ts, span) })
  }
  return out
})

function fmtTick(ts, spanMs) {
  const d = new Date(ts)
  if (spanMs < 2 * 60 * 60 * 1000) {
    // < 2 h window — HH:MM:SS is useful
    return d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }
  // Larger windows — HH:MM is enough
  return d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
}

async function reload() {
  if (!bandMeta.value && !store.enabled) return
  loading.value = true
  loadError.value = ''
  try {
    const { from, to } = resolveWindow()
    const [range, trs] = await Promise.all([
      store.loadRange(props.band, from, to, 2000),
      store.loadTransitions(props.band, from, to),
    ])
    scanRows.value = range.rows || []
    transitions.value = trs || []
    await nextTick()
    drawWaterfall()
    drawSpectrumOverlay()
  } catch (e) {
    loadError.value = String(e?.message || e)
  } finally {
    loading.value = false
  }
}

function zoomIn() {
  // Narrow the preset by one step (24h → 6h → 1h → 5m)
  const idx = PRESETS.findIndex(p => p.key === preset.value)
  if (idx > 0) preset.value = PRESETS[idx - 1].key
}
function zoomOut() {
  const idx = PRESETS.findIndex(p => p.key === preset.value)
  if (idx >= 0 && idx < PRESETS.length - 1) preset.value = PRESETS[idx + 1].key
}

function onCustomChange() {
  if (customFromMs.value && customToMs.value && customToMs.value > customFromMs.value) {
    preset.value = 'custom'
    reload()
  }
}

// Hover: map mouse X → bin index → frequency; mouse Y → source row →
// timestamp + power. Light enough to compute on every mousemove.
function updateHover(e) {
  const canvas = waterfallCanvas.value
  if (!canvas) return
  const rect = canvas.getBoundingClientRect()
  const xPx = e.clientX - rect.left
  const yPx = e.clientY - rect.top
  if (xPx < 0 || xPx > rect.width || yPx < 0 || yPx > rect.height) {
    hover.inside = false
    return
  }
  const rows = scanRows.value
  if (rows.length === 0 || !bandMeta.value) {
    hover.inside = false
    return
  }
  const rowIdx = Math.min(rows.length - 1,
    Math.floor((yPx / rect.height) * rows.length))
  const row = rows[rowIdx]
  const powers = row?.powers || row?.Powers || []
  if (powers.length === 0) { hover.inside = false; return }
  const binIdx = Math.min(powers.length - 1,
    Math.floor((xPx / rect.width) * powers.length))
  const freqSpan = bandMeta.value.freqHigh - bandMeta.value.freqLow
  const freqHz = bandMeta.value.freqLow + (binIdx + 0.5) * (freqSpan / powers.length)

  hover.inside = true
  hover.x = xPx
  hover.y = yPx
  hover.freqHz = freqHz
  hover.power = powers[binIdx]
  hover.ts = row.ts || row.TS
}
function leaveHover() { hover.inside = false }

onMounted(() => {
  // Make sure the store is connected — if the user navigated here
  // without visiting /spectrum first, bands[] may be empty and we
  // need hardware + status pulled before the header reads right.
  store.connect()
  // Track the waterfall container's height so the canvas always fills
  // its flex slot, in both the standalone route and the 95 vh modal.
  nextTick(() => {
    const wrap = waterfallCanvas.value?.parentElement
    if (!wrap) { reload(); return }
    const apply = () => {
      const h = wrap.clientHeight
      if (h > 0 && Math.abs(h - waterfallH.value) > 1) {
        waterfallH.value = h
        drawWaterfall()
      }
    }
    resizeObs = new ResizeObserver(apply)
    resizeObs.observe(wrap)
    apply()
    reload()
  })
})
onBeforeUnmount(() => {
  if (resizeObs) { resizeObs.disconnect(); resizeObs = null }
  /* SSE owned by main view; nothing else to tear down here */
})
watch(() => preset.value, () => { if (preset.value !== 'custom') reload() })
watch(() => props.band, () => reload())

function goBack() {
  if (props.modal) emit('request-close')
  else router.push({ name: 'spectrum' })
}
</script>

<template>
  <div class="sd-root" :class="{ 'sd-modal': props.modal }">
    <div class="sd-head">
      <button v-if="!props.modal" class="sd-back" @click="goBack" aria-label="Back to spectrum overview">
        ← Back
      </button>
      <div class="sd-title">
        <h1>{{ bandLabel }}</h1>
        <div class="sd-sub">
          <span class="sd-band-id">{{ props.band }}</span>
          <span v-if="bandMeta">
            {{ (bandMeta.freqLow / 1e6).toFixed(3) }}–{{ (bandMeta.freqHigh / 1e6).toFixed(3) }} MHz
          </span>
          <span class="sd-state" :class="'state-' + bandState">{{ bandState }}</span>
          <span v-if="bandBaselineStd > 0">
            baseline {{ bandBaselineMean.toFixed(1) }} dB ± {{ bandBaselineStd.toFixed(2) }}
          </span>
        </div>
      </div>
    </div>

    <!-- Controls -->
    <div class="sd-controls">
      <div class="sd-control-group">
        <label>Window</label>
        <div class="sd-preset-row">
          <button v-for="p in PRESETS" :key="p.key"
                  :class="['sd-preset', { active: preset === p.key }]"
                  @click="preset = p.key">
            {{ p.label }}
          </button>
          <button class="sd-preset" :class="{ active: preset === 'custom' }"
                  @click="preset = 'custom'">Custom</button>
        </div>
      </div>
      <div class="sd-control-group">
        <label>Zoom</label>
        <div class="sd-zoom-row">
          <button class="sd-zoom" @click="zoomIn" :disabled="preset === '5m'">− narrower</button>
          <button class="sd-zoom" @click="zoomOut" :disabled="preset === '24h'">+ wider</button>
          <button class="sd-zoom" @click="reload">refresh</button>
        </div>
      </div>
      <div v-if="preset === 'custom'" class="sd-control-group">
        <label>Custom range (local time)</label>
        <div class="sd-custom-row">
          <input type="datetime-local"
                 @change="e => { customFromMs = e.target.valueAsNumber; onCustomChange() }" />
          <span>→</span>
          <input type="datetime-local"
                 @change="e => { customToMs = e.target.valueAsNumber; onCustomChange() }" />
        </div>
      </div>
    </div>

    <!-- Summary strip: row count + first/last ts -->
    <div class="sd-summary">
      <span v-if="loading" class="sd-loading">Loading…</span>
      <span v-else-if="loadError" class="sd-error">{{ loadError }}</span>
      <span v-else>
        <strong>{{ scanRows.length }}</strong> rows ·
        <strong>{{ transitions.length }}</strong> transition<span v-if="transitions.length !== 1">s</span>
        <span v-if="scanRows.length > 0">
          · {{ new Date(scanRows[scanRows.length-1].ts || scanRows[scanRows.length-1].TS).toLocaleString('en-GB') }}
          →
          {{ new Date(scanRows[0].ts || scanRows[0].TS).toLocaleString('en-GB') }}
        </span>
      </span>
    </div>

    <!-- Spectrum trace (latest row in the window) -->
    <div class="sd-panel">
      <div class="sd-panel-head">
        <span>Spectrum — newest in window</span>
      </div>
      <canvas ref="spectrumCanvas" class="sd-spectrum"></canvas>
    </div>

    <!-- Waterfall + transition overlay + time axis -->
    <div class="sd-panel">
      <div class="sd-panel-head">
        <span>Historical waterfall</span>
        <span class="sd-legend">
          <span class="dot dot-jamming"></span> jamming
          <span class="dot dot-interference"></span> interference
          <span class="dot dot-degraded"></span> degraded
          <span class="dot dot-clear"></span> clear
        </span>
      </div>
      <div class="sd-waterfall-wrap"
           @mousemove="updateHover"
           @mouseleave="leaveHover">
        <canvas ref="waterfallCanvas" class="sd-waterfall"></canvas>
        <!-- Transition marker lines — horizontal rules over the canvas -->
        <div v-for="(m, i) in transitionMarkers" :key="i"
             class="sd-marker"
             :style="{ top: m.yPct + '%', backgroundColor: m.color }">
          <span class="sd-marker-label" :style="{ color: m.color }">
            {{ m.oldState }}→{{ m.state }}
          </span>
        </div>
        <!-- Time axis on the right edge -->
        <div class="sd-time-axis">
          <div v-for="(t, i) in timeTicks" :key="i"
               class="sd-time-tick" :style="{ top: t.yPct + '%' }">
            {{ t.label }}
          </div>
        </div>
        <!-- Hover readout -->
        <div v-if="hover.inside" class="sd-hover"
             :style="{ left: hover.x + 'px', top: hover.y + 'px' }">
          <span>{{ (hover.freqHz / 1e6).toFixed(3) }} MHz</span>
          <span>{{ hover.power?.toFixed?.(1) }} dBm</span>
          <span v-if="hover.ts">{{ new Date(hover.ts).toLocaleTimeString('en-GB') }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.sd-root {
  padding: 16px 20px 24px;
  background: #020617;
  color: #e2e8f0;
  font-family: Inter, system-ui, sans-serif;
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  gap: 0;
}
/* Modal-embedded variant — fills the modal panel rather than the
   viewport. Close button is provided by SpectrumBandDetailModal so
   we drop our own back button + give the modal chrome the bg it
   expects from the root. */
.sd-root.sd-modal {
  min-height: 0;
  height: 100%;
  padding: 48px 24px 20px; /* top-pad makes room for modal close button */
}
.sd-head { display: flex; align-items: flex-start; gap: 16px; margin-bottom: 18px; }
.sd-back {
  padding: 6px 12px;
  background: #1e293b;
  border: 1px solid #334155;
  color: #cbd5e1;
  border-radius: 4px;
  font-size: 13px;
  cursor: pointer;
}
.sd-back:hover { background: #334155; color: white; }
.sd-title h1 { margin: 0; font-size: 20px; letter-spacing: 0.02em; color: #f1f5f9; }
.sd-sub { display: flex; flex-wrap: wrap; gap: 10px; margin-top: 4px; font-size: 11px; color: #94a3b8; }
.sd-band-id { font-family: monospace; color: #64748b; }
.sd-state {
  text-transform: uppercase; letter-spacing: 0.1em; font-weight: 700;
  padding: 2px 6px; border-radius: 3px; color: #0b0b0b;
}
.sd-state.state-clear { background: #10b981; }
.sd-state.state-jamming { background: #dc2626; color: #fff; }
.sd-state.state-interference { background: #f59e0b; }
.sd-state.state-degraded { background: #eab308; }
.sd-state.state-calibrating { background: #6366f1; color: #fff; }

.sd-controls {
  display: flex; flex-wrap: wrap; gap: 24px;
  padding: 12px 14px; margin-bottom: 12px;
  background: #0b1220; border: 1px solid #1e293b; border-radius: 6px;
}
.sd-control-group { display: flex; flex-direction: column; gap: 6px; }
.sd-control-group label {
  font-size: 10px; text-transform: uppercase; letter-spacing: 0.1em;
  color: #64748b;
}
.sd-preset-row, .sd-zoom-row, .sd-custom-row {
  display: flex; gap: 6px; align-items: center;
}
.sd-preset, .sd-zoom {
  padding: 5px 12px; font-size: 12px; cursor: pointer;
  background: #1e293b; color: #cbd5e1;
  border: 1px solid #334155; border-radius: 3px;
}
.sd-preset:hover, .sd-zoom:hover { background: #334155; color: white; }
.sd-preset.active { background: #2563eb; color: white; border-color: #2563eb; }
.sd-preset[disabled], .sd-zoom[disabled] { opacity: 0.4; cursor: not-allowed; }
.sd-custom-row input {
  padding: 4px 8px; font-size: 12px;
  background: #0f172a; border: 1px solid #334155; border-radius: 3px;
  color: #e2e8f0;
  color-scheme: dark;
}

.sd-summary {
  font-size: 12px; color: #94a3b8; padding: 6px 2px; margin-bottom: 10px;
}
.sd-loading { color: #fbbf24; }
.sd-error { color: #f87171; }

.sd-panel {
  border: 1px solid #1e293b; border-radius: 6px; margin-bottom: 14px;
  background: #030a1a;
}
/* The last panel (waterfall) should absorb leftover vertical space
   so the view fills the container whether we're standalone-page or
   inside the 95vh modal. Controls + spectrum + header stay at their
   natural size; waterfall grows. */
.sd-root .sd-panel:last-child { flex: 1 1 auto; display: flex; flex-direction: column; min-height: 320px; }
.sd-root .sd-panel:last-child .sd-waterfall-wrap { flex: 1 1 auto; height: auto; min-height: 280px; }
.sd-panel-head {
  display: flex; justify-content: space-between; align-items: center;
  padding: 6px 10px; border-bottom: 1px solid #0f172a;
  font-size: 11px; text-transform: uppercase; letter-spacing: 0.08em;
  color: #cbd5e1;
}
.sd-legend {
  display: flex; gap: 10px; align-items: center;
  text-transform: none; letter-spacing: 0; font-size: 10px; color: #94a3b8;
}
.sd-legend .dot {
  width: 8px; height: 8px; border-radius: 50%; display: inline-block;
  margin-right: 3px;
}
.sd-legend .dot-jamming { background: #dc2626; }
.sd-legend .dot-interference { background: #f59e0b; }
.sd-legend .dot-degraded { background: #eab308; }
.sd-legend .dot-clear { background: #10b981; }

.sd-spectrum {
  display: block; width: 100%; height: 140px;
}

.sd-waterfall-wrap {
  position: relative;
  width: 100%;
  height: 640px;
  cursor: crosshair;
}
.sd-waterfall {
  position: absolute; inset: 0;
  width: calc(100% - 56px); /* leave room for the time axis */
  height: 100%;
  display: block;
  image-rendering: auto;
}
.sd-marker {
  position: absolute; left: 0; right: 56px;
  height: 1.5px; pointer-events: none;
  box-shadow: 0 0 4px currentColor;
}
.sd-marker-label {
  position: absolute; right: 64px; top: -8px;
  font-family: monospace; font-size: 10px;
  padding: 1px 5px;
  background: rgba(2, 6, 23, 0.88);
  border-radius: 3px;
  white-space: nowrap;
}
.sd-time-axis {
  position: absolute; top: 0; right: 0; bottom: 0;
  width: 56px;
  pointer-events: none;
}
.sd-time-tick {
  position: absolute; right: 4px;
  font-family: monospace; font-size: 10px;
  color: #64748b;
  transform: translateY(-50%);
}
.sd-hover {
  position: absolute; pointer-events: none;
  transform: translate(10px, 10px);
  background: rgba(2, 6, 23, 0.92);
  border: 1px solid #334155; border-radius: 3px;
  padding: 3px 7px; font-family: monospace; font-size: 11px;
  color: #e2e8f0; display: flex; gap: 10px; z-index: 3;
}
</style>
