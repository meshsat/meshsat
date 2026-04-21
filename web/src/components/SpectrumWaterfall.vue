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
import { eccmAction as eccmActionFor } from '@/composables/useEccm'
import SpectrumBandDetailModal from '@/components/SpectrumBandDetailModal.vue'

// modalBand is the band name currently open in the full-screen detail
// modal, or null when closed. [MESHSAT-650]
const modalBand = ref(null)
function openBandModal(name) { modalBand.value = name }
function closeBandModal() { modalBand.value = null }

// 1 Hz re-render tick for the calibration countdown — same pattern as
// the compact widget. Cleared on unmount.
const nowMs = ref(Date.now())

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
const PANEL_AXIS_GUTTER_L = 46  // room for dBm labels (left, FFT Y-axis)
const PANEL_AXIS_GUTTER_R = 72  // room for dBm colour legend + time labels
const PANEL_AXIS_GUTTER_B = 18  // room for freq labels (bottom)
const PANEL_LEGEND_BAR_W  = 14  // width of the Turbo gradient stripe inside the right gutter

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
  const plotW = rect.width - PANEL_AXIS_GUTTER_L - PANEL_AXIS_GUTTER_R
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

  // Y-axis range. After calibration we lock to baseline ± σ margins
  // so the trace sits centred at rest and rides upward during
  // jamming. During calibration (std=0) we derive the range from
  // the scan's own min/max + padding so the trace still renders
  // meaningfully — otherwise yRange collapses to ~18 dB around an
  // uninformed centre and the trace clips to top/bottom.
  let yTop, yBot
  if (band.baselineStd > 0) {
    yTop = band.baselineMean + 12 * band.baselineStd
    yBot = band.baselineMean - 6 * band.baselineStd
  } else {
    let mn = Infinity, mx = -Infinity
    for (const p of top.powers) {
      if (!isFinite(p)) continue
      if (p < mn) mn = p
      if (p > mx) mx = p
    }
    if (!isFinite(mn) || mn === mx) { mn = -80; mx = -10 }
    const pad = (mx - mn) * 0.4
    yTop = mx + pad
    yBot = mn - pad
  }
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

// WATERFALL_COLS is the render width (in canvas pixels) we upsample
// the FFT rows into. rtl_power produces very few bins per band
// (12-120), so drawing at native bin resolution + CSS scaling yields
// a soft/mushy image. Upsampling to a fixed wide buffer + linear
// interpolation between bins produces the crisp, evenly-shaded
// spectrogram look of desktop SDR tools (SDR#, gqrx, CubicSDR).
const WATERFALL_COLS = 512

// Palette range is sized from a robust statistic of the row itself
// (median + MAD), not from the band-level baselineStd. baselineStd
// measures scan-to-scan variance of the *scalar* average power — it
// systematically underestimates within-scan per-bin spread and
// saturates the Turbo colormap on natural noise (see MESHSAT-649).
// Per-row MAD is robust to narrowband jammers (they don't pull the
// median), so the noise floor stays cool and spikes still ride up to
// the hot end of the palette. A 0.5 dB minimum scale prevents the
// flat-row degenerate case where the whole row collapses to one
// colour — same floor the legend's RobustScaleDB uses.
function rowPaletteRange(row, band) {
  if (!row || !row.powers?.length) {
    // No row data — fall back to baseline if present, else a sane default
    if (band.baselineStd > 0) {
      return {
        floor: band.baselineMean - 2 * band.baselineStd,
        ceil:  band.baselineMean + 10 * band.baselineStd,
      }
    }
    return { floor: -80, ceil: -10 }
  }
  const finite = []
  for (const p of row.powers) if (isFinite(p)) finite.push(p)
  if (finite.length === 0) return { floor: -80, ceil: -10 }

  const sorted = finite.slice().sort((a, b) => a - b)
  const median = sorted[Math.floor(sorted.length / 2)]
  const absDev = sorted.map(p => Math.abs(p - median)).sort((a, b) => a - b)
  const mad = absDev[Math.floor(absDev.length / 2)]
  const scale = Math.max(1.4826 * mad, 0.5)

  return {
    floor: median - 2 * scale,
    ceil:  median + 10 * scale,
  }
}

function drawWaterfall(bandName) {
  const band = store.bands[bandName]
  const canvas = waterfallCanvases[bandName]
  if (!band || !canvas) return
  const rows = band.rows
  const nRows = store.WATERFALL_ROWS

  if (canvas.width !== WATERFALL_COLS) canvas.width = WATERFALL_COLS
  if (canvas.height !== nRows) canvas.height = nRows

  const ctx = canvas.getContext('2d')
  const img = ctx.createImageData(WATERFALL_COLS, nRows)

  for (let y = 0; y < nRows; y++) {
    const row = rows[y]
    if (!row || !row.powers || row.powers.length === 0) {
      // No data row — dark charcoal fill so the empty ring is visible
      // but doesn't pretend to be a valid reading.
      for (let x = 0; x < WATERFALL_COLS; x++) {
        const off = (y * WATERFALL_COLS + x) * 4
        img.data[off] = 15; img.data[off + 1] = 15; img.data[off + 2] = 25; img.data[off + 3] = 255
      }
      continue
    }

    const powers = row.powers
    const nBins = powers.length
    const { floor, ceil } = rowPaletteRange(row, band)
    const span = ceil - floor || 1

    // Linear interpolation between adjacent bins. WATERFALL_COLS is
    // typically ~5-30× the native bin count; evenly mapping x -> bin
    // position and lerping avoids the blocky step look.
    for (let x = 0; x < WATERFALL_COLS; x++) {
      const fBin = (x / (WATERFALL_COLS - 1)) * (nBins - 1)
      const i0 = Math.floor(fBin)
      const i1 = Math.min(nBins - 1, i0 + 1)
      const frac = fBin - i0
      const p = powers[i0] * (1 - frac) + powers[i1] * frac
      const t = Math.max(0, Math.min(1, (p - floor) / span))
      const [r, g, b] = turbo(t)
      const off = (y * WATERFALL_COLS + x) * 4
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

let tickTimer = null
onMounted(() => {
  store.connect()
  nextTick(redrawAll)
  window.addEventListener('resize', onResize)
  tickTimer = setInterval(() => { nowMs.value = Date.now() }, 1000)
})
onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize)
  if (tickTimer) { clearInterval(tickTimer); tickTimer = null }
})
function onResize() { nextTick(redrawAll) }

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

  // Colour-legend labels covering the SAME dBm range the waterfall
  // Turbo colormap uses — baseline - 2σ (floor) → baseline + 10σ
  // (ceiling), per rowPaletteRange. Without these labels the hue is
  // unreadable; with them, operators can estimate the dBm of any
  // colour they see. Shown on the right gutter next to a gradient bar.
  // Scale uses RobustScaleDB-equivalent (max of std, 1.4826*MAD, 0.5)
  // so the legend stays meaningful on locked-carrier bands where Std
  // alone collapses. We replicate the arithmetic locally to avoid
  // plumbing RobustScaleDB through the store.
  const scale = Math.max(
    b.baselineStd || 0,
    1.4826 * (b.baselineMad || 0),
    0.5,
  )
  const legendFloor = b.baselineMean - 2 * scale
  const legendCeil  = b.baselineMean + 10 * scale
  const legendStops = [0, 0.25, 0.5, 0.75, 1.0]
  const legendLabels = legendStops.map(t => ({
    t,
    dB: legendFloor + (legendCeil - legendFloor) * (1 - t), // y=0 is top (hottest)
    y: t * PANEL_WATERFALL_H,
  }))

  // Time-axis labels on the waterfall right gutter. Row 0 = now
  // (top), rows age downward. Scan cadence is ScanInterval (3 s
  // backend default) so rows cover ~3 s each. Mark 5 ticks at 0/1m/2m/3m/4m
  // of elapsed time — precise enough without cluttering. If the
  // latest row has a timestamp we anchor the tick labels to it,
  // otherwise fall back to "-Nm" relative labels.
  const ScanIntervalSec = 3 // matches backend ScanInterval — if it changes, update here
  const tLabels = []
  for (let mins = 0; mins <= 4; mins++) {
    const rowIdx = Math.min(store.WATERFALL_ROWS - 1, Math.floor((mins * 60) / ScanIntervalSec))
    const y = (rowIdx / (store.WATERFALL_ROWS - 1)) * PANEL_WATERFALL_H
    tLabels.push({ y, label: mins === 0 ? 'now' : `-${mins}m` })
  }

  return { dbLabels, fLabels, yTop, yBot, legendFloor, legendCeil, legendLabels, tLabels }
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
    case 'degraded': return '#eab308'
    case 'clear': return '#10b981'
    case 'calibrating': return '#6366f1'
    default: return '#6b7280'
  }
}

// ---- MIJI-9 report metrics (P7-P12) ----

// Scan-peak: the max bin of the most recent FFT sweep. This is what
// the operator sees flicker around in the Spectrum trace.
function scanPeakInfo(name) {
  const b = store.bands[name]
  if (!b) return null
  const row = b.rows?.[0]
  if (!row?.powers?.length) return null
  let idx = 0, mx = row.powers[0]
  for (let i = 1; i < row.powers.length; i++) {
    const p = row.powers[i]
    if (typeof p === 'number' && isFinite(p) && p > mx) { mx = p; idx = i }
  }
  const span = b.meta.freqHigh - b.meta.freqLow
  const freqHz = b.meta.freqLow + (idx + 0.5) * (span / row.powers.length)
  return { freqHz, powerDB: mx }
}

// Event-peak: the MAX observed since the last state transition.
// MIJI-9 field 5 (signal strength / modulation) should reference this,
// not the per-scan peak (which jitters with each sweep).
function eventPeakInfo(name) {
  const b = store.bands[name]
  if (!b) return null
  if (b.eventPeakDB == null || !isFinite(b.eventPeakDB)) return null
  return { freqHz: b.eventPeakFreqHz, powerDB: b.eventPeakDB }
}

// Dwell time = now - state.since (from backend). "jamming for 0:00:34".
function dwellText(name) {
  const b = store.bands[name]
  if (!b || !b.since) return null
  const s = Math.max(0, Math.floor((nowMs.value - b.since.getTime()) / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rs = s % 60
  if (m < 60) return `${m}m ${rs.toString().padStart(2, '0')}s`
  const h = Math.floor(m / 60)
  const rm = m % 60
  return `${h}h ${rm.toString().padStart(2, '0')}m`
}

// ECCM guidance comes from the shared locale (src/composables/useEccm +
// src/locales/en.json). Having a single source prevents drift between
// the banner here and the quick-reference table in SpectrumView. When
// i18n is wired, the composable swaps its import for vue-i18n t() and
// everything downstream keeps working.
function eccmAction(name) {
  const b = store.bands[name]
  if (!b) return ''
  return eccmActionFor(name, b.state)
}

// Occupancy/flatness formatting. Show "—" for bands still calibrating
// (no baseline → comparison threshold is meaningless).
function fmtPct(v) {
  if (typeof v !== 'number' || !isFinite(v)) return '—'
  return (v * 100).toFixed(0) + '%'
}
function fmtNum2(v) {
  if (typeof v !== 'number' || !isFinite(v)) return '—'
  return v.toFixed(2)
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

    <!-- The whole panel is clickable and opens the full-screen detail
         modal for that band (MESHSAT-650). Using a div-with-role
         rather than a button so the inner markup (which has its own
         headings / metric spans / canvases) is legal HTML. Enter /
         Space support keyboard access. -->
    <div v-for="name in orderedBands" :key="name"
         class="sa-panel sa-panel-clickable"
         :class="['state-' + (store.bands[name]?.state || 'calibrating')]"
         role="button"
         tabindex="0"
         :aria-label="`Open ${store.bands[name]?.meta?.label || name} history`"
         @click="openBandModal(name)"
         @keydown.enter.prevent="openBandModal(name)"
         @keydown.space.prevent="openBandModal(name)">
      <div class="sa-panel-head">
        <div class="sa-panel-title">
          {{ store.bands[name]?.meta?.label || name }}
          <span class="sa-id">{{ name }}</span>
          <span class="sa-expand-hint" aria-hidden="true">⤢ expand</span>
        </div>
        <div class="sa-panel-meta">
          <span>iface: {{ store.bands[name]?.meta?.interfaceID || '—' }}</span>
          <span v-if="store.bands[name]?.state !== 'calibrating'">
            baseline: {{ store.bands[name]?.baselineMean?.toFixed?.(1) }} dB ± {{ store.bands[name]?.baselineStd?.toFixed?.(2) }}
          </span>
          <span class="sa-state" :style="{ background: stateColour(store.bands[name]?.state) }">
            {{ store.bands[name]?.state || 'calibrating' }}
          </span>
        </div>
      </div>
      <!-- MIJI-9 metrics strip (P7/P8/P9/P10): peak freq+dBm, dwell
           time, ITU-R SM.1880 occupancy, Wiener-entropy flatness.
           Hidden during calibration — values are meaningless without
           a locked baseline. -->
      <div v-if="store.bands[name]?.state !== 'calibrating' && store.bands[name]?.baselineStd > 0"
           class="sa-metrics">
        <span class="sa-metric" title="Peak of the current FFT sweep">
          <span class="k">peak (now)</span>
          <span class="v">
            <template v-if="scanPeakInfo(name)">
              {{ scanPeakInfo(name).powerDB.toFixed(1) }} dBm @ {{ (scanPeakInfo(name).freqHz / 1e6).toFixed(3) }} MHz
            </template>
            <template v-else>—</template>
          </span>
        </span>
        <span class="sa-metric" title="Max power observed since the last state transition — use this for MIJI-9 reports">
          <span class="k">peak (event)</span>
          <span class="v">
            <template v-if="eventPeakInfo(name)">
              {{ eventPeakInfo(name).powerDB.toFixed(1) }} dBm @ {{ (eventPeakInfo(name).freqHz / 1e6).toFixed(3) }} MHz
            </template>
            <template v-else>—</template>
          </span>
        </span>
        <span class="sa-metric">
          <span class="k">{{ store.bands[name]?.state }} for</span>
          <span class="v">{{ dwellText(name) || '—' }}</span>
        </span>
        <span class="sa-metric" title="Fraction of FFT bins ≥ baseline+6 dB (ITU-R SM.1880)">
          <span class="k">occupancy</span>
          <span class="v">{{ fmtPct(store.bands[name]?.occupancy) }}</span>
        </span>
        <span class="sa-metric" title="Wiener entropy of linear power (0=structured, 1=white noise / barrage)">
          <span class="k">flatness</span>
          <span class="v">{{ fmtNum2(store.bands[name]?.flatness) }}</span>
        </span>
      </div>

      <!-- ECCM recommended-action panel (P12). Visible only when the
           band is degraded/interference/jamming — silent when clear. -->
      <div v-if="eccmAction(name) && ['degraded','interference','jamming'].includes(store.bands[name]?.state)"
           class="sa-eccm"
           :class="'sa-eccm-' + store.bands[name]?.state">
        <span class="sa-eccm-tag">ECCM</span>
        <span class="sa-eccm-text">{{ eccmAction(name) }}</span>
      </div>

      <!-- Calibration strip: visible only during Phase 1. Shows a
           progress bar + countdown for the active band, or a "queued"
           indicator for pending ones. -->
      <template v-if="calibrationInfo(name)">
        <div v-if="calibrationInfo(name).active" class="sa-cal-strip">
          <div class="sa-cal-bar" :style="{ width: calibrationInfo(name).pct + '%' }" />
          <div class="sa-cal-text">
            calibrating baseline · {{ calibrationInfo(name).remainingSec }}s remaining
            ({{ calibrationInfo(name).pct.toFixed(0) }}%)
          </div>
        </div>
        <div v-else class="sa-cal-strip sa-cal-queued">
          <div class="sa-cal-text">queued — waiting for earlier bands to finish calibrating</div>
        </div>
      </template>

      <div class="sa-plot"
           @mousemove="e => updateHover(name, e, $event.currentTarget)"
           @mouseleave="setHoverOutside(name)">
        <!-- Spectrum canvas -->
        <canvas :ref="el => { if (el) spectrumCanvases[name] = el }"
                class="sa-spectrum-canvas" />
        <!-- Waterfall canvas -->
        <canvas :ref="el => { if (el) waterfallCanvases[name] = el }"
                class="sa-waterfall-canvas" />

        <!-- Axis overlay SVG. viewBox matches the plot pixel rect.
             Three gutters: left (dBm FFT axis), right (dBm colour
             legend + time axis), bottom (frequency). -->
        <svg class="sa-axes" v-if="axesFor(name)"
             :viewBox="`0 0 1000 ${PANEL_SPECTRUM_H + PANEL_WATERFALL_H + PANEL_AXIS_GUTTER_B}`"
             preserveAspectRatio="none">
          <!-- Turbo colormap gradient definition — same polynomial as
               the Canvas turbo() function, sampled at 5 stops. Exact
               per-bin fidelity doesn't matter; operators need to
               roughly decode the waterfall hue, not reverse-engineer
               the colourmap. -->
          <defs>
            <linearGradient :id="'legend-' + name" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%"   stop-color="rgb(136, 3, 14)" />
              <stop offset="25%"  stop-color="rgb(248, 184, 37)" />
              <stop offset="50%"  stop-color="rgb(162, 252, 60)" />
              <stop offset="75%"  stop-color="rgb(45, 195, 230)" />
              <stop offset="100%" stop-color="rgb(34, 53, 158)" />
            </linearGradient>
          </defs>

          <!-- dBm labels on left gutter (FFT trace Y-axis) -->
          <g v-for="(lb, i) in axesFor(name).dbLabels" :key="'db'+i"
             class="sa-axis-label">
            <text :x="PANEL_AXIS_GUTTER_L - 4" :y="lb.y + 3" text-anchor="end">
              {{ lb.dB }}
            </text>
          </g>
          <!-- Freq labels along the bottom -->
          <g v-for="(lb, i) in axesFor(name).fLabels" :key="'f'+i" class="sa-axis-label">
            <text :x="PANEL_AXIS_GUTTER_L + lb.t * (1000 - PANEL_AXIS_GUTTER_L - PANEL_AXIS_GUTTER_R)"
                  :y="PANEL_SPECTRUM_H + PANEL_WATERFALL_H + 12"
                  text-anchor="middle">{{ lb.label }}</text>
          </g>
          <!-- Vertical divider between spectrum + waterfall -->
          <line :x1="PANEL_AXIS_GUTTER_L" :x2="1000 - PANEL_AXIS_GUTTER_R"
                :y1="PANEL_SPECTRUM_H" :y2="PANEL_SPECTRUM_H"
                class="sa-divider" />

          <!-- Right gutter: dBm colour legend next to the waterfall -->
          <rect :x="1000 - PANEL_AXIS_GUTTER_R + 4"
                :y="PANEL_SPECTRUM_H"
                :width="PANEL_LEGEND_BAR_W"
                :height="PANEL_WATERFALL_H"
                :fill="'url(#legend-' + name + ')'"
                stroke="#1e293b" stroke-width="0.5"
                vector-effect="non-scaling-stroke" />
          <g v-for="(lb, i) in axesFor(name).legendLabels" :key="'lg'+i" class="sa-axis-label">
            <text :x="1000 - PANEL_AXIS_GUTTER_R + 4 + PANEL_LEGEND_BAR_W + 3"
                  :y="PANEL_SPECTRUM_H + lb.y + 3" text-anchor="start">
              {{ lb.dB.toFixed(0) }}
            </text>
          </g>
          <text :x="1000 - PANEL_AXIS_GUTTER_R + 4"
                :y="PANEL_SPECTRUM_H - 3"
                class="sa-axis-label sa-axis-unit">dBm</text>

          <!-- Right gutter: time axis on waterfall -->
          <g v-for="(lb, i) in axesFor(name).tLabels" :key="'t'+i" class="sa-axis-label">
            <text :x="1000 - 2"
                  :y="PANEL_SPECTRUM_H + lb.y + 3"
                  text-anchor="end">{{ lb.label }}</text>
          </g>
          <text :x="1000 - 2"
                :y="PANEL_SPECTRUM_H - 3"
                class="sa-axis-label sa-axis-unit" text-anchor="end">time</text>
        </svg>

        <!-- Hover readout -->
        <div v-if="hover[name]?.inside" class="sa-hover"
             :style="{ left: hover[name].x + 'px', top: hover[name].y + 'px' }">
          <span>{{ (hover[name].freqHz / 1e6).toFixed(3) }} MHz</span>
          <span>{{ hover[name].power?.toFixed?.(1) }} dB</span>
        </div>
      </div>
    </div>

    <!-- Full-screen detail modal — mounts when a panel is clicked.
         Teleported to <body> by the component itself. [MESHSAT-650] -->
    <SpectrumBandDetailModal v-if="modalBand"
                             :band="modalBand"
                             @close="closeBandModal" />

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
  display: inline-flex;
  align-items: center;
}
.sa-panel-title .sa-id { color: #64748b; font-family: monospace; font-size: 10px; margin-left: 6px; }
/* Affordance: small "⤢ expand" hint on every panel title. Goes from
   muted grey to blue on panel hover so operators notice the whole
   panel is clickable. [MESHSAT-650] */
.sa-expand-hint {
  margin-left: 10px;
  font-size: 10px; font-weight: 500;
  color: #475569;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  transition: color 0.15s ease;
}
.sa-panel-clickable { cursor: zoom-in; }
.sa-panel-clickable:hover .sa-expand-hint { color: #60a5fa; }
.sa-panel-clickable:focus-visible {
  outline: 2px solid #60a5fa;
  outline-offset: 2px;
}
/* Hover feedback on the whole panel — subtle border lift so the
   click target is obvious without shifting the layout. */
.sa-panel-clickable:hover { border-color: #334155; }
.sa-panel-meta { display: flex; align-items: center; gap: 10px; font-size: 10px; color: #94a3b8; }

.sa-cal-strip {
  position: relative;
  height: 22px;
  background: #0b1220;
  border-bottom: 1px solid #1e293b;
  display: flex;
  align-items: center;
  padding: 0 10px;
  overflow: hidden;
}
.sa-cal-bar {
  position: absolute;
  top: 0; bottom: 0; left: 0;
  background: linear-gradient(90deg,
    rgba(99, 102, 241, 0.55) 0%,
    rgba(99, 102, 241, 0.25) 100%);
  transition: width 1s linear;
}
.sa-cal-text {
  position: relative;
  font-family: monospace;
  font-size: 11px;
  color: #c7d2fe;
  letter-spacing: 0.03em;
  font-weight: 600;
}
.sa-cal-queued {
  background: #0f172a;
}
.sa-cal-queued .sa-cal-text { color: #64748b; font-weight: 500; font-style: italic; }
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
  width: calc(100% - 72px); /* leave room for right gutter (legend + time labels) */
  height: 110px;
  display: block;
  image-rendering: auto;
  z-index: 1;
}
.sa-waterfall-canvas {
  position: absolute;
  top: 110px;
  left: 46px; /* align past the left gutter with the spectrum plot area */
  width: calc(100% - 46px - 72px); /* left + right gutter */
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
.sa-axis-unit {
  fill: #94a3b8;
  font-size: 8px;
  text-transform: uppercase;
  letter-spacing: 0.08em;
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

.sa-metrics {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  padding: 6px 10px;
  background: #050b1a;
  border-bottom: 1px solid #0f172a;
  font-family: monospace;
  font-size: 11px;
}
.sa-metric { display: inline-flex; gap: 6px; align-items: baseline; }
.sa-metric .k {
  color: #64748b;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  font-size: 9px;
}
.sa-metric .v { color: #e2e8f0; font-weight: 600; }

.sa-eccm {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  padding: 7px 10px;
  border-bottom: 1px solid #0f172a;
  font-size: 11px;
  color: #fde68a;
  background: rgba(245, 158, 11, 0.08);
  border-left: 3px solid #f59e0b;
}
.sa-eccm.sa-eccm-jamming {
  color: #fecaca;
  background: rgba(220, 38, 38, 0.10);
  border-left-color: #dc2626;
}
.sa-eccm.sa-eccm-degraded {
  color: #fef08a;
  background: rgba(234, 179, 8, 0.06);
  border-left-color: #eab308;
}
.sa-eccm-tag {
  font-family: monospace;
  font-size: 9px;
  letter-spacing: 0.12em;
  padding: 2px 6px;
  background: rgba(0,0,0,0.35);
  border-radius: 3px;
  font-weight: 700;
  flex-shrink: 0;
  color: inherit;
}
.sa-eccm-text { line-height: 1.4; }

.sa-empty {
  padding: 16px;
  font-size: 12px;
  color: #94a3b8;
  font-style: italic;
  text-align: center;
}
</style>
