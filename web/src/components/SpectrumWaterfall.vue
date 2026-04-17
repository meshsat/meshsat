<!--
  SpectrumWaterfall.vue
  ---------------------
  Real-time RTL-SDR jamming visualisation. Renders one Canvas per
  monitored band, stacked vertically. Each canvas is a scrolling
  waterfall — newest scan on top, rows age downward — with colour
  mapped to power relative to the band's calibrated baseline.

  Rendering model:
    * The store keeps a ring of power arrays per band; we draw that
      ring on every scan event (one drawBand per band that got new
      data). Full-image redraw is ~1-5 ms per band for the bin counts
      we monitor (12-120 bins × 100 rows), cheap enough to do on
      every update rather than implementing incremental scroll.
    * Bin width auto-fits the canvas CSS width, so narrow (600 kHz
      LoRa) and wide (3 MHz LTE) bands both fill the same row.
    * Jammed bins — power > baseline + 3σ — are over-drawn in red
      even within the normal waterfall, so the operator can see
      the jamming footprint inside the waterfall itself (not only
      on the band border).

  State banner:
    * State is drawn as a coloured strip at the right edge of the
      canvas so jamming stands out even in a glance.
    * On jamming the band title also flashes (CSS animation).
-->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount, watch, nextTick } from 'vue'
import { useSpectrumStore } from '@/stores/spectrum'

const store = useSpectrumStore()

// Canvas refs keyed by band name — reactive because the list of bands
// is discovered from the SSE stream rather than hardcoded client-side.
const canvases = ref({})

// Deterministic ordering so the five waterfalls don't reshuffle as
// events arrive. Mirrors the server's DefaultBands order.
const BAND_ORDER = ['lora_868', 'aprs_144', 'gps_l1', 'lte_b20_dl', 'lte_b8_dl']

const orderedBands = computed(() => {
  const keys = Object.keys(store.bands)
  return BAND_ORDER.filter(b => keys.includes(b)).concat(
    keys.filter(k => !BAND_ORDER.includes(k)).sort()
  )
})

// Freq formatter: sub-GHz in MHz with 1 decimal, GHz with 3 decimals.
function fmtFreq(hz) {
  if (hz >= 1e9) return (hz / 1e9).toFixed(3) + ' GHz'
  return (hz / 1e6).toFixed(1) + ' MHz'
}

// Map a power value in dB to a waterfall RGB. Baseline-relative so
// quiet and noisy bands look the same at rest. The palette walks from
// dark navy (below baseline) through green and yellow up to red at
// and above the jamming threshold.
function colourFor(power, baselineMean, baselineStd, threshJam) {
  if (!isFinite(power)) return [0, 0, 0]
  const delta = power - baselineMean
  if (delta < -baselineStd) return [8, 12, 40]
  if (delta < baselineStd) {
    // near baseline — cool blue/teal
    const t = (delta + baselineStd) / (2 * baselineStd)
    return [Math.round(8 + t * 30), Math.round(12 + t * 80), Math.round(40 + t * 120)]
  }
  if (power < threshJam) {
    // elevated but sub-jamming — green to yellow
    const span = threshJam - (baselineMean + baselineStd)
    const t = span > 0 ? (power - (baselineMean + baselineStd)) / span : 0
    return [Math.round(t * 220), Math.round(180 + t * 60), Math.round(60 - t * 60)]
  }
  // at or above the jamming threshold — bright red regardless of bin,
  // so a jammed band reads red in the image and not just at the border
  const over = Math.min(1, (power - threshJam) / 10)
  return [220 + Math.round(over * 35), Math.round(40 - over * 30), Math.round(40 - over * 30)]
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

function drawBand(bandName) {
  const band = store.bands[bandName]
  const canvas = canvases.value[bandName]
  if (!band || !canvas) return

  // The canvas' internal resolution is fixed to the bin count on X and
  // WATERFALL_ROWS on Y; the CSS layout scales it. This keeps one
  // canvas pixel == one (bin, row) sample, so we don't pay any
  // interpolation cost and the operator sees crisp per-bin blocks.
  const rows = band.rows
  const nRows = store.WATERFALL_ROWS
  const nBins = rows[0]?.powers?.length || 1
  if (canvas.width !== nBins) canvas.width = nBins
  if (canvas.height !== nRows) canvas.height = nRows

  const ctx = canvas.getContext('2d')
  if (!ctx) return
  const img = ctx.createImageData(nBins, nRows)

  const base = band.baselineMean
  const std = band.baselineStd || 1
  const threshJam = band.threshJamming || (base + 3 * std)

  for (let y = 0; y < nRows; y++) {
    const row = rows[y]
    if (!row || !row.powers || row.powers.length === 0) {
      // unknown row — paint dark grey so it's obvious where history
      // ends and not confused with below-baseline quiet bins
      for (let x = 0; x < nBins; x++) {
        const off = (y * nBins + x) * 4
        img.data[off] = 20
        img.data[off + 1] = 20
        img.data[off + 2] = 20
        img.data[off + 3] = 255
      }
      continue
    }
    for (let x = 0; x < nBins; x++) {
      const p = row.powers[x]
      const [r, g, b] = colourFor(p, base, std, threshJam)
      const off = (y * nBins + x) * 4
      img.data[off] = r
      img.data[off + 1] = g
      img.data[off + 2] = b
      img.data[off + 3] = 255
    }
  }
  ctx.putImageData(img, 0, 0)
}

function redrawAll() {
  for (const name of orderedBands.value) drawBand(name)
}

// Re-draw a band whenever its ring changes length or top-of-ring
// timestamp changes. Using a shallow watch on the band's rows length
// plus ts is cheaper than deep-watching the whole powers array.
watch(
  () => orderedBands.value.map(n => ({
    n,
    len: store.bands[n]?.rows?.length || 0,
    ts: store.bands[n]?.rows?.[0]?.ts || '',
  })),
  async () => {
    await nextTick()
    redrawAll()
  },
  { deep: true }
)

onMounted(() => {
  store.connect()
  // Initial draw so the panels render calibrating/empty state even
  // before the first scan arrives.
  nextTick(redrawAll)
})
onBeforeUnmount(() => {
  // Do NOT disconnect — other components (notably the alert modal
  // mounted in App.vue) share this store's SSE connection.
})
</script>

<template>
  <div class="spectrum-waterfall">
    <div class="header">
      <h3>RTL-SDR Spectrum Waterfall</h3>
      <div class="conn" :class="{ ok: store.connected, bad: !store.connected }">
        <span class="dot"></span>
        {{ store.connected ? 'streaming' : (store.enabled ? 'reconnecting' : 'hardware not present') }}
      </div>
    </div>
    <div v-for="name in orderedBands" :key="name" class="band-row"
         :class="{ jamming: store.bands[name]?.state === 'jamming',
                   interference: store.bands[name]?.state === 'interference' }">
      <div class="band-meta">
        <div class="band-title">{{ store.bands[name]?.meta?.label || name }}</div>
        <div class="band-sub">
          <span>{{ fmtFreq(store.bands[name]?.meta?.freqLow) }}-{{ fmtFreq(store.bands[name]?.meta?.freqHigh) }}</span>
          <span class="iface">iface: {{ store.bands[name]?.meta?.interfaceID || '—' }}</span>
        </div>
      </div>
      <canvas
        :ref="el => { if (el) canvases[name] = el }"
        class="waterfall-canvas"
      />
      <div class="band-state" :style="{ background: stateColour(store.bands[name]?.state) }">
        {{ store.bands[name]?.state || 'calibrating' }}
      </div>
    </div>
    <div v-if="orderedBands.length === 0" class="empty">
      No spectrum data. Check that an RTL-SDR dongle is plugged in and that rtl_power is installed in the container.
    </div>
  </div>
</template>

<style scoped>
.spectrum-waterfall {
  background: #0f172a;
  border: 1px solid #1e293b;
  border-radius: 6px;
  padding: 12px;
  color: #e2e8f0;
}
.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}
.header h3 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  letter-spacing: 0.02em;
}
.conn { font-size: 11px; display: flex; align-items: center; gap: 4px; }
.conn .dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #6b7280;
}
.conn.ok .dot { background: #10b981; box-shadow: 0 0 6px #10b981; }
.conn.bad .dot { background: #f97316; }

.band-row {
  display: grid;
  grid-template-columns: 180px 1fr 100px;
  gap: 8px;
  align-items: stretch;
  margin-top: 6px;
  border: 1px solid #1e293b;
  border-radius: 4px;
  overflow: hidden;
  background: #020617;
  min-height: 72px;
}
.band-row.jamming {
  border-color: #dc2626;
  box-shadow: 0 0 0 1px #dc2626, 0 0 12px rgba(220, 38, 38, 0.4) inset;
  animation: jam-flash 1.2s infinite alternate;
}
.band-row.interference {
  border-color: #f59e0b;
}
@keyframes jam-flash {
  from { box-shadow: 0 0 0 1px #dc2626, 0 0 8px rgba(220, 38, 38, 0.25) inset; }
  to   { box-shadow: 0 0 0 2px #dc2626, 0 0 18px rgba(220, 38, 38, 0.55) inset; }
}
.band-meta {
  padding: 8px 10px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  border-right: 1px solid #1e293b;
  background: #0f172a;
}
.band-title { font-size: 12px; font-weight: 600; }
.band-sub { font-size: 10px; color: #94a3b8; margin-top: 4px; display: flex; flex-direction: column; gap: 2px; }
.band-sub .iface { font-family: monospace; }

.waterfall-canvas {
  width: 100%;
  height: 100%;
  min-height: 72px;
  display: block;
  image-rendering: pixelated;
}
.band-state {
  color: #0b0b0b;
  font-size: 11px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0 6px;
}
.empty {
  padding: 12px;
  font-size: 12px;
  color: #94a3b8;
  font-style: italic;
}
</style>
