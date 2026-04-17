<!--
  SpectrumWaterfall.vue
  ---------------------
  Per-band RF spectrum analyser panels, modelled after desktop SDR
  tools (SDR#, gqrx, CubicSDR). Each panel has:

    * a live FFT trace (current scan as a filled line chart, dBm on Y,
      frequency on X)
    * three reference lines overlaying the trace:
        - baseline mean (solid white, per band calibration)
        - jamming threshold (dashed red, baseline + 3σ)
        - interference threshold (dashed amber, baseline + 6σ)
    * a scrolling waterfall below the trace, same X axis, time on Y,
      rendered with the Turbo colormap and smooth (non-pixelated)
      interpolation so it reads as a continuous heatmap rather than
      blocky tiles
    * SVG axes with dBm ticks (left) and frequency ticks (bottom)
    * a hover cursor that reports frequency + instantaneous power

  Per-band panels are stacked. The jammed/interference states change
  the panel border and add a ribbon along the right edge.
-->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount, watch, nextTick, reactive } from 'vue'
import { useSpectrumStore } from '@/stores/spectrum'

const store = useSpectrumStore()

const BAND_ORDER = ['lora_868', 'aprs_144', 'gps_l1', 'lte_b20_dl', 'lte_b8_dl']
const orderedBands = computed(() => {
  const keys = Object.keys(store.bands)
  return BAND_ORDER.filter(b => keys.includes(b)).concat(
    keys.filter(k => !BAND_ORDER.includes(k)).sort()
  )
})

// Turbo colormap lookup (Mikhail Markov's polynomial fit — perceptually
// uniform, far better than jet, standard in modern RF tools). Input is
// a normalised 0..1 value; output is [r, g, b] each 0..255.
function turbo(t) {
  t = Math.max(0, Math.min(1, t))
  const r = 34.61 + t * (1172.33 - t * (10793.56 - t * (33300.12 - t * (38394.49 - t * 14825.05))))
  const g = 23.31 + t * (557.33 + t * (1225.33 - t * (3574.96 - t * (1073.77 + t * 707.56))))
  const b = 27.2 + t * (3211.1 - t * (15327.97 - t * (27814 - t * (22569.18 - t * 6838.66))))
  return [Math.max(0, Math.min(255, r)) | 0,
          Math.max(0, Math.min(255, g)) | 0,
          Math.max(0, Math.min(255, b)) | 0]
}

// Normalise a power sample to 0..1 for the colormap. Floor is baseline
// - 2σ (below that is blue/dark), ceiling is baseline + 10σ (above is
// clipped to deep red). Keeps the colormap locked to the band's own
// noise floor rather than an absolute dBm range, so two physically
// different bands render comparably.
function normPower(power, baseMean, baseStd) {
  const floor = baseMean - 2 * (baseStd || 1)
  const ceil  = baseMean + 10 * (baseStd || 1)
  if (!isFinite(power)) return 0
  return (power - floor) / (ceil - floor)
}

// ---- Per-panel rendering ----

// Each panel uses 3 canvases layered visually:
//   1. spectrum (live FFT trace + fill + reference lines)
//   2. waterfall (time/freq heatmap)
// A single SVG overlay draws the axes and hover cursor so we don't
// have to fight Canvas text rendering quality.
const spectrumCanvases = reactive({})
const waterfallCanvases = reactive({})

// Layout constants (CSS px; canvases use these dimensions internally
// but are scaled to fit via their CSS width rule).
const PANEL_SPECTRUM_H = 110
const PANEL_WATERFALL_H = 160
const PANEL_AXIS_GUTTER_L = 46  // room for dBm labels
const PANEL_AXIS_GUTTER_B = 18  // room for freq labels

// Hover state per panel — keyed by band name.
const hover = reactive({}) // { [band]: { x, y, freqHz, power, inside } }

function setHoverOutside(band) {
  hover[band] = { inside: false }
}
function updateHover(band, e, el) {
  const rect = el.getBoundingClientRect()
  const x = e.clientX - rect.left
  const y = e.clientY - rect.top
  const plotX = x - PANEL_AXIS_GUTTER_L
  const plotW = rect.width - PANEL_AXIS_GUTTER_L
  if (plotX < 0 || plotX > plotW) { hover[band] = { inside: false }; return }
  const b = store.bands[band]
  if (!b || !b.rows?.[0]?.powers) { hover[band] = { inside: false }; return }
  const powers = b.rows[0].powers
  const bin = Math.floor((plotX / plotW) * powers.length)
  const safeBin = Math.max(0, Math.min(powers.length - 1, bin))
  const freqSpan = (b.meta.freqHigh - b.meta.freqLow)
  const freqHz = b.meta.freqLow + (safeBin + 0.5) * (freqSpan / powers.length)
  hover[band] = {
    inside: true,
    x, y,
    freqHz,
    power: powers[safeBin],
  }
}

function drawSpectrum(bandName) {
  const band = store.bands[bandName]
  const canvas = spectrumCanvases[bandName]
  if (!band || !canvas) return
  const rows = band.rows
  const top = rows[0]
  if (!top || !top.powers?.length) {
    // Clear the canvas so old data doesn't linger during calibration
    const cw = canvas.width, ch = canvas.height
    const ctx = canvas.getContext('2d')
    if (ctx) ctx.clearRect(0, 0, cw, ch)
    return
  }

  // CSS width × device pixel ratio for crispness, height fixed per
  // layout. We size the canvas buffer to a reasonable max (1200px)
  // so retina doesn't blow up the framebuffer on 4K displays.
  const cssW = canvas.clientWidth || 600
  const cssH = PANEL_SPECTRUM_H
  const dpr = Math.min(2, window.devicePixelRatio || 1)
  const W = Math.min(1200, Math.floor(cssW * dpr))
  const H = Math.floor(cssH * dpr)
  if (canvas.width !== W) canvas.width = W
  if (canvas.height !== H) canvas.height = H

  const ctx = canvas.getContext('2d')
  ctx.clearRect(0, 0, W, H)

  const plotL = PANEL_AXIS_GUTTER_L * dpr
  const plotW = W - plotL
  const plotT = 4 * dpr
  const plotH = H - plotT - 2 * dpr

  // Y-axis range. Lock to baseline ± generous margins (same as the
  // colormap normalisation) so the trace sits in the middle at rest
  // and rides upward during jamming.
  const yTop = band.baselineMean + 12 * (band.baselineStd || 1)
  const yBot = band.baselineMean - 6 * (band.baselineStd || 1)
  const yRange = yTop - yBot
  const yAt = (dB) => plotT + ((yTop - dB) / yRange) * plotH

  // Gridlines + fill-zone background.
  const bgGrad = ctx.createLinearGradient(0, plotT, 0, plotT + plotH)
  bgGrad.addColorStop(0, '#0b1220')
  bgGrad.addColorStop(1, '#020617')
  ctx.fillStyle = bgGrad
  ctx.fillRect(plotL, plotT, plotW, plotH)

  // Horizontal gridlines every 5 dB
  ctx.strokeStyle = 'rgba(148, 163, 184, 0.08)'
  ctx.lineWidth = 1
  const stepDB = 5
  const firstTick = Math.ceil(yBot / stepDB) * stepDB
  for (let dB = firstTick; dB <= yTop; dB += stepDB) {
    const y = yAt(dB)
    ctx.beginPath()
    ctx.moveTo(plotL, y)
    ctx.lineTo(plotL + plotW, y)
    ctx.stroke()
  }

  // Baseline line (solid thin white)
  ctx.strokeStyle = 'rgba(226, 232, 240, 0.55)'
  ctx.lineWidth = 1 * dpr
  ctx.setLineDash([])
  {
    const y = yAt(band.baselineMean)
    ctx.beginPath()
    ctx.moveTo(plotL, y)
    ctx.lineTo(plotL + plotW, y)
    ctx.stroke()
  }

  // Interference threshold (dashed amber) — baseline + 6σ
  ctx.strokeStyle = 'rgba(245, 158, 11, 0.75)'
  ctx.setLineDash([4 * dpr, 4 * dpr])
  {
    const y = yAt(band.threshInterference || (band.baselineMean + 6 * band.baselineStd))
    ctx.beginPath()
    ctx.moveTo(plotL, y)
    ctx.lineTo(plotL + plotW, y)
    ctx.stroke()
  }

  // Jamming threshold (dashed red) — baseline + 3σ
  ctx.strokeStyle = 'rgba(220, 38, 38, 0.85)'
  ctx.setLineDash([6 * dpr, 3 * dpr])
  {
    const y = yAt(band.threshJamming || (band.baselineMean + 3 * band.baselineStd))
    ctx.beginPath()
    ctx.moveTo(plotL, y)
    ctx.lineTo(plotL + plotW, y)
    ctx.stroke()
  }
  ctx.setLineDash([])

  // FFT trace fill. Use a soft gradient (green → yellow at the threshold)
  const powers = top.powers
  const n = powers.length
  const xStep = plotW / n

  // Fill under the trace
  const fillGrad = ctx.createLinearGradient(0, plotT, 0, plotT + plotH)
  fillGrad.addColorStop(0, 'rgba(239, 68, 68, 0.55)')
  fillGrad.addColorStop(0.35, 'rgba(251, 191, 36, 0.40)')
  fillGrad.addColorStop(0.75, 'rgba(16, 185, 129, 0.25)')
  fillGrad.addColorStop(1, 'rgba(16, 185, 129, 0.02)')
  ctx.fillStyle = fillGrad
  ctx.beginPath()
  ctx.moveTo(plotL, plotT + plotH)
  for (let i = 0; i < n; i++) {
    const x = plotL + i * xStep + xStep / 2
    const y = yAt(powers[i])
    if (i === 0) ctx.lineTo(x, y)
    else ctx.lineTo(x, y)
  }
  ctx.lineTo(plotL + plotW, plotT + plotH)
  ctx.closePath()
  ctx.fill()

  // Trace line
  ctx.strokeStyle = '#60a5fa'
  ctx.lineWidth = 1.4 * dpr
  ctx.lineJoin = 'round'
  ctx.beginPath()
  for (let i = 0; i < n; i++) {
    const x = plotL + i * xStep + xStep / 2
    const y = yAt(powers[i])
    if (i === 0) ctx.moveTo(x, y)
    else ctx.lineTo(x, y)
  }
  ctx.stroke()

  // Peak marker
  let peakIdx = 0
  for (let i = 1; i < n; i++) if (powers[i] > powers[peakIdx]) peakIdx = i
  const peakX = plotL + peakIdx * xStep + xStep / 2
  const peakY = yAt(powers[peakIdx])
  ctx.fillStyle = '#facc15'
  ctx.beginPath()
  ctx.arc(peakX, peakY, 2.5 * dpr, 0, Math.PI * 2)
  ctx.fill()
}

function drawWaterfall(bandName) {
  const band = store.bands[bandName]
  const canvas = waterfallCanvases[bandName]
  if (!band || !canvas) return
  const rows = band.rows
  const nRows = store.WATERFALL_ROWS
  const nBins = rows[0]?.powers?.length || 1

  // Intrinsic canvas resolution: one canvas pixel per (bin, row)
  // sample. The CSS layer stretches it to the plot area via
  // image-rendering: auto so the browser bilinear-interpolates and
  // the result reads as a smooth heatmap (not the blocky tiles the
  // previous revision had).
  if (canvas.width !== nBins) canvas.width = nBins
  if (canvas.height !== nRows) canvas.height = nRows

  const ctx = canvas.getContext('2d')
  const img = ctx.createImageData(nBins, nRows)

  const base = band.baselineMean
  const std = band.baselineStd || 1

  for (let y = 0; y < nRows; y++) {
    const row = rows[y]
    if (!row || !row.powers || row.powers.length === 0) {
      for (let x = 0; x < nBins; x++) {
        const off = (y * nBins + x) * 4
        img.data[off] = 15
        img.data[off + 1] = 15
        img.data[off + 2] = 25
        img.data[off + 3] = 255
      }
      continue
    }
    for (let x = 0; x < nBins; x++) {
      const t = normPower(row.powers[x], base, std)
      const [r, g, b] = turbo(t)
      const off = (y * nBins + x) * 4
      img.data[off] = r
      img.data[off + 1] = g
      img.data[off + 2] = b
      img.data[off + 3] = 255
    }
  }
  ctx.putImageData(img, 0, 0)
}

function redraw(bandName) {
  drawSpectrum(bandName)
  drawWaterfall(bandName)
}

function redrawAll() {
  for (const name of orderedBands.value) redraw(name)
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
onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize)
})
function onResize() { nextTick(redrawAll) }

// ---- Axes (SVG) computed per band ----

function axesFor(bandName) {
  const b = store.bands[bandName]
  if (!b) return null
  const yTop = b.baselineMean + 12 * (b.baselineStd || 1)
  const yBot = b.baselineMean - 6 * (b.baselineStd || 1)
  // dBm labels every 5 dB, snapped to round numbers
  const stepDB = 5
  const first = Math.ceil(yBot / stepDB) * stepDB
  const dbLabels = []
  for (let dB = first; dB <= yTop; dB += stepDB) {
    dbLabels.push({ dB, y: ((yTop - dB) / (yTop - yBot)) * PANEL_SPECTRUM_H })
  }
  // Frequency ticks: 4 equally spaced labels across the band
  const fLabels = []
  const span = b.meta.freqHigh - b.meta.freqLow
  for (let i = 0; i <= 4; i++) {
    const fHz = b.meta.freqLow + (span * i) / 4
    const t = i / 4
    fLabels.push({ fHz, label: fmtFreq(fHz, span), t })
  }
  return { dbLabels, fLabels, yTop, yBot }
}

function fmtFreq(hz, span) {
  if (hz >= 1e9) return (hz / 1e9).toFixed(3) + ' GHz'
  if (span < 1e6) return (hz / 1e6).toFixed(3) + ' MHz'
  return (hz / 1e6).toFixed(2) + ' MHz'
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
</script>

<template>
  <div class="sa-root">
    <div class="sa-head">
      <h3>RF SPECTRUM — 5 monitored bands</h3>
      <div class="sa-head-right">
        <button type="button" class="sa-pause"
                :class="{ paused: store.paused }"
                :title="store.paused ? 'Resume waterfall' : 'Pause waterfall'"
                @click="store.togglePause()">
          <svg v-if="!store.paused" viewBox="0 0 16 16" width="11" height="11">
            <rect x="3" y="2" width="3" height="12" fill="currentColor" />
            <rect x="10" y="2" width="3" height="12" fill="currentColor" />
          </svg>
          <svg v-else viewBox="0 0 16 16" width="11" height="11">
            <polygon points="3,2 14,8 3,14" fill="currentColor" />
          </svg>
          {{ store.paused ? 'PLAY' : 'PAUSE' }}
        </button>
        <div class="sa-conn" :class="{ ok: store.connected, bad: !store.connected }">
          <span class="dot"></span>
          {{ store.paused ? 'paused' : (store.connected ? 'streaming' : (store.enabled ? 'reconnecting' : 'RTL-SDR not present')) }}
        </div>
      </div>
    </div>

    <div v-for="name in orderedBands" :key="name"
         class="sa-panel"
         :class="['state-' + (store.bands[name]?.state || 'calibrating')]">
      <div class="sa-panel-head">
        <div class="sa-panel-title">{{ store.bands[name]?.meta?.label || name }}
          <span class="sa-id">{{ name }}</span>
        </div>
        <div class="sa-panel-meta">
          <span>iface: {{ store.bands[name]?.meta?.interfaceID || '—' }}</span>
          <span>baseline: {{ store.bands[name]?.baselineMean?.toFixed?.(1) }} dB ± {{ store.bands[name]?.baselineStd?.toFixed?.(2) }}</span>
          <span class="sa-state" :style="{ background: stateColour(store.bands[name]?.state) }">
            {{ store.bands[name]?.state || 'calibrating' }}
          </span>
        </div>
      </div>

      <div class="sa-plot"
           @mousemove="e => updateHover(name, e, $event.currentTarget)"
           @mouseleave="setHoverOutside(name)">
        <!-- Spectrum canvas -->
        <canvas :ref="el => { if (el) spectrumCanvases[name] = el }"
                class="sa-spectrum-canvas" />
        <!-- Waterfall canvas -->
        <canvas :ref="el => { if (el) waterfallCanvases[name] = el }"
                class="sa-waterfall-canvas" />

        <!-- Axis overlay SVG. viewBox matches the plot pixel rect. -->
        <svg class="sa-axes" v-if="axesFor(name)"
             :viewBox="`0 0 1000 ${PANEL_SPECTRUM_H + PANEL_WATERFALL_H + PANEL_AXIS_GUTTER_B}`"
             preserveAspectRatio="none">
          <!-- dBm labels on left gutter -->
          <g v-for="(lb, i) in axesFor(name).dbLabels" :key="'db'+i"
             class="sa-axis-label">
            <text :x="PANEL_AXIS_GUTTER_L - 4" :y="lb.y + 3" text-anchor="end">
              {{ lb.dB }}
            </text>
          </g>
          <!-- Freq labels along the bottom -->
          <g v-for="(lb, i) in axesFor(name).fLabels" :key="'f'+i" class="sa-axis-label">
            <text :x="PANEL_AXIS_GUTTER_L + lb.t * (1000 - PANEL_AXIS_GUTTER_L)"
                  :y="PANEL_SPECTRUM_H + PANEL_WATERFALL_H + 12"
                  text-anchor="middle">{{ lb.label }}</text>
          </g>
          <!-- Vertical divider between spectrum + waterfall -->
          <line :x1="PANEL_AXIS_GUTTER_L" :x2="1000"
                :y1="PANEL_SPECTRUM_H" :y2="PANEL_SPECTRUM_H"
                class="sa-divider" />
        </svg>

        <!-- Hover readout -->
        <div v-if="hover[name]?.inside" class="sa-hover"
             :style="{ left: hover[name].x + 'px', top: hover[name].y + 'px' }">
          <span>{{ (hover[name].freqHz / 1e6).toFixed(3) }} MHz</span>
          <span>{{ hover[name].power?.toFixed?.(1) }} dB</span>
        </div>
      </div>
    </div>

    <div v-if="orderedBands.length === 0" class="sa-empty">
      <template v-if="!store.enabled">
        RTL-SDR not detected in the container. Plug in the dongle + ensure rtl_power is installed.
      </template>
      <template v-else>
        Loading spectrum status…{{ store.connected ? ' (calibration may take ~2.5 min after restart)' : '' }}
      </template>
    </div>
  </div>
</template>

<style scoped>
.sa-root {
  background: #020617;
  border: 1px solid #1e293b;
  border-radius: 8px;
  padding: 10px 12px;
  color: #e2e8f0;
  font-family: Inter, system-ui, sans-serif;
}
.sa-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}
.sa-head h3 {
  margin: 0;
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.1em;
  color: #cbd5e1;
}
.sa-head-right { display: flex; align-items: center; gap: 12px; }
.sa-pause {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  background: #1e293b;
  color: #cbd5e1;
  border: 1px solid #334155;
  border-radius: 3px;
  padding: 3px 10px;
  font-size: 11px;
  letter-spacing: 0.1em;
  cursor: pointer;
  font-weight: 700;
}
.sa-pause:hover { background: #334155; color: white; }
.sa-pause.paused { background: #facc15; color: #0b0b0b; border-color: #eab308; }
.sa-conn { font-size: 11px; display: flex; align-items: center; gap: 4px; }
.sa-conn .dot { width: 7px; height: 7px; border-radius: 50%; background: #6b7280; display: inline-block; }
.sa-conn.ok .dot { background: #10b981; box-shadow: 0 0 6px #10b981; }
.sa-conn.bad .dot { background: #f97316; }

.sa-panel {
  margin-top: 10px;
  border: 1px solid #1e293b;
  border-radius: 6px;
  background: #030a1a;
  overflow: hidden;
  position: relative;
}
.sa-panel.state-jamming {
  border-color: #dc2626;
  box-shadow: 0 0 0 1px #dc2626, 0 0 18px rgba(220,38,38,0.35) inset;
}
.sa-panel.state-interference { border-color: #f59e0b; }
.sa-panel.state-calibrating { border-color: #6366f1; }

.sa-panel-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 10px;
  border-bottom: 1px solid #0f172a;
  background: #030a1a;
}
.sa-panel-title {
  font-size: 12px;
  font-weight: 600;
  color: #e2e8f0;
  letter-spacing: 0.02em;
}
.sa-panel-title .sa-id { color: #64748b; font-family: monospace; font-size: 10px; margin-left: 6px; }
.sa-panel-meta { display: flex; align-items: center; gap: 10px; font-size: 10px; color: #94a3b8; }
.sa-state {
  color: #0b0b0b;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  padding: 2px 6px;
  border-radius: 3px;
}

.sa-plot {
  position: relative;
  width: 100%;
  /* 110 + 160 + 18 == the SVG viewBox numeric — kept in sync */
  height: 288px;
}
.sa-spectrum-canvas {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  height: 110px;
  display: block;
  image-rendering: auto;
  z-index: 1;
}
.sa-waterfall-canvas {
  position: absolute;
  top: 110px;
  left: 46px; /* start past the left gutter so the waterfall aligns with the trace */
  width: calc(100% - 46px);
  height: 160px;
  display: block;
  image-rendering: auto; /* smooth interpolation — not blocky */
  z-index: 1;
}
.sa-axes {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  pointer-events: none;
  z-index: 2;
}
.sa-axis-label {
  fill: #64748b;
  font-size: 9px;
  font-family: 'Inter', system-ui, sans-serif;
  letter-spacing: 0.04em;
}
.sa-divider {
  stroke: #1e293b;
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}
.sa-hover {
  position: absolute;
  transform: translate(8px, 8px);
  background: rgba(2, 6, 23, 0.92);
  border: 1px solid #334155;
  border-radius: 3px;
  padding: 2px 6px;
  font-size: 10px;
  font-family: monospace;
  color: #e2e8f0;
  z-index: 3;
  display: flex;
  gap: 8px;
  pointer-events: none;
  white-space: nowrap;
}

.sa-empty {
  padding: 16px;
  font-size: 12px;
  color: #94a3b8;
  font-style: italic;
  text-align: center;
}
</style>
