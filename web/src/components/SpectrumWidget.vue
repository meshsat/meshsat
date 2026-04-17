<!--
  SpectrumWidget.vue
  ------------------
  Compact dashboard widget that answers "is any band under attack?" at a
  glance. Five rows — one per band — each showing state chip, current
  peak-over-baseline delta in dB, and a 60-sample sparkline of that
  delta over time. The rainbow Turbo waterfall lives on the dedicated
  /spectrum page where axes and legends can actually fit; at widget
  size, stacked per-band waterfalls are unreadable (MESHSAT audit
  2026-04-17). Click anywhere outside the controls → /spectrum.
-->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount, watch, nextTick, reactive } from 'vue'
import { useRouter } from 'vue-router'
import { useSpectrumStore } from '@/stores/spectrum'

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

// Sparkline window: 60 samples ≈ 3 min at 3 s/scan. Long enough to see
// a rising attack, short enough to stay visually meaningful at 120 px.
const SPARK_SAMPLES = 60

// Freshness readout — OLDEST scan timestamp across all bands. We use
// min-across-bands, not max, so a single wedged band surfaces as
// "5m ago" instead of being hidden by its still-healthy neighbours.
// Safety semantic: the widget answers "am I OK?" — the worst band
// decides. MIL-STD-1472H §5.2 requires data freshness on a
// safety-critical display.
//
// Cold-boot nuance: if no band has produced a scan yet we return 0 and
// ageText renders "initialising" (no amber). The moment at least one
// band has data we switch to live freshness. This avoids the dashboard
// flashing amber for the first ~12 s of every container restart.
const bandFreshness = computed(() => {
  const entries = []
  for (const n of Object.keys(store.bands)) {
    const ts = store.bands[n]?.rows?.[0]?.ts
    if (!ts) continue
    const ms = Date.parse(ts)
    if (isFinite(ms)) entries.push({ name: n, ms })
  }
  return entries
})
const lastUpdateMs = computed(() => {
  const entries = bandFreshness.value
  if (entries.length === 0) return 0
  let oldest = Infinity
  for (const e of entries) if (e.ms < oldest) oldest = e.ms
  return oldest
})
const hasAnyScanYet = computed(() => bandFreshness.value.length > 0)
const ageText = computed(() => {
  if (!hasAnyScanYet.value) return 'initialising'
  const ms = lastUpdateMs.value
  const dt = Math.max(0, Math.floor((nowMs.value - ms) / 1000))
  if (dt < 60) return `${dt}s ago`
  if (dt < 3600) return `${Math.floor(dt / 60)}m ago`
  return `${Math.floor(dt / 3600)}h ago`
})
// Stale threshold: scan cadence is 3 s (ScanInterval) so >15s without
// a new scan on the OLDEST band means at least one band is wedged.
// Don't flash amber pre-first-scan — that's "initialising", not a
// fault.
const ageStale = computed(() => {
  if (!hasAnyScanYet.value) return false
  return (nowMs.value - lastUpdateMs.value) > 15000
})

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

// Sparklines keyed by band.
const sparks = reactive({})

// Extract oldest-first peak-over-baseline deltas for sparkline plotting.
// Rows are stored newest-at-0; we reverse for left-to-right time flow.
function peakDeltas(b) {
  if (!b || !b.rows || b.baselineStd <= 0) return []
  const base = b.baselineMean
  const take = Math.min(SPARK_SAMPLES, b.rows.length)
  const out = new Array(take)
  for (let i = 0; i < take; i++) {
    const r = b.rows[take - 1 - i]
    const m = r?.max
    out[i] = (typeof m === 'number' && isFinite(m)) ? m - base : null
  }
  return out
}

function currentDelta(b) {
  if (!b || !b.rows?.[0] || b.baselineStd <= 0) return null
  const m = b.rows[0].max
  if (typeof m !== 'number' || !isFinite(m)) return null
  return m - b.baselineMean
}

function fillForState(state) {
  switch (state) {
    case 'jamming':      return 'rgba(220, 38, 38, 0.35)'
    case 'interference': return 'rgba(245, 158, 11, 0.35)'
    case 'degraded':     return 'rgba(234, 179, 8, 0.30)'
    case 'calibrating':  return 'rgba(99, 102, 241, 0.25)'
    default:             return 'rgba(16, 185, 129, 0.28)'
  }
}
function strokeForState(state) {
  switch (state) {
    case 'jamming':      return '#f87171'
    case 'interference': return '#fbbf24'
    case 'degraded':     return '#facc15'
    case 'calibrating':  return '#a5b4fc'
    default:             return '#34d399'
  }
}

// drawSpark renders the peak-delta sparkline. Y-axis is 0..yMax dB
// above baseline, where yMax auto-scales (min 12 dB so quiet bands
// don't show a flat-line-that-looks-like-no-data).
function drawSpark(name) {
  const b = store.bands[name]
  const canvas = sparks[name]
  if (!canvas || !b) return
  const dpr = Math.min(2, window.devicePixelRatio || 1)
  const cssW = canvas.clientWidth || 140
  const cssH = canvas.clientHeight || 22
  const W = Math.max(60, Math.floor(cssW * dpr))
  const H = Math.max(12, Math.floor(cssH * dpr))
  if (canvas.width !== W) canvas.width = W
  if (canvas.height !== H) canvas.height = H

  const ctx = canvas.getContext('2d')
  ctx.clearRect(0, 0, W, H)

  const samples = peakDeltas(b)
  if (samples.length === 0) return

  let mx = 0
  for (const v of samples) if (v != null && v > mx) mx = v
  const yMax = Math.max(12, mx * 1.15)

  const yAt = (dB) => {
    const d = Math.max(0, Math.min(yMax, dB))
    return H - (d / yMax) * H
  }

  // Baseline reference (y=0) — subtle dashed line
  ctx.strokeStyle = 'rgba(148, 163, 184, 0.35)'
  ctx.lineWidth = 1
  ctx.setLineDash([2 * dpr, 3 * dpr])
  ctx.beginPath()
  ctx.moveTo(0, H - 0.5)
  ctx.lineTo(W, H - 0.5)
  ctx.stroke()
  ctx.setLineDash([])

  const state = b.state || 'clear'
  const n = samples.length
  const xAt = (i) => (n === 1) ? W / 2 : (i / (n - 1)) * W

  // Fill area under curve
  ctx.fillStyle = fillForState(state)
  ctx.beginPath()
  ctx.moveTo(0, H)
  let started = false
  for (let i = 0; i < n; i++) {
    const v = samples[i]
    if (v == null) continue
    const x = xAt(i)
    const y = yAt(v)
    if (!started) { ctx.lineTo(x, y); started = true } else { ctx.lineTo(x, y) }
  }
  if (started) {
    ctx.lineTo(W, H)
    ctx.closePath()
    ctx.fill()
  }

  // Stroke line on top
  ctx.strokeStyle = strokeForState(state)
  ctx.lineWidth = 1.3 * dpr
  ctx.lineJoin = 'round'
  ctx.beginPath()
  started = false
  for (let i = 0; i < n; i++) {
    const v = samples[i]
    if (v == null) continue
    const x = xAt(i)
    const y = yAt(v)
    if (!started) { ctx.moveTo(x, y); started = true } else { ctx.lineTo(x, y) }
  }
  ctx.stroke()
}
function redrawAll() { for (const n of orderedBands.value) drawSpark(n) }

watch(
  () => orderedBands.value.map(n => ({
    n,
    len: store.bands[n]?.rows?.length || 0,
    ts: store.bands[n]?.rows?.[0]?.ts || '',
    st: store.bands[n]?.state || '',
    pz: store.paused,
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
    case 'degraded': return '#eab308'
    case 'clear': return '#10b981'
    case 'calibrating': return '#6366f1'
    default: return '#6b7280'
  }
}
function stateChipClass(state) {
  switch (state) {
    case 'jamming': return 'bg-red-900/50 text-red-200 border-red-700/50'
    case 'interference': return 'bg-amber-900/50 text-amber-200 border-amber-700/50'
    case 'degraded': return 'bg-yellow-900/50 text-yellow-200 border-yellow-700/50'
    case 'calibrating': return 'bg-indigo-900/50 text-indigo-200 border-indigo-700/50'
    case 'clear': return 'bg-emerald-900/40 text-emerald-300 border-emerald-700/40'
    default: return 'bg-gray-800/60 text-gray-400 border-gray-700/50'
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
function deltaText(b) {
  const d = currentDelta(b)
  if (d == null) return '—'
  const sign = d >= 0 ? '+' : ''
  return `${sign}${d.toFixed(1)} dB`
}
function deltaClass(b) {
  const d = currentDelta(b)
  if (d == null) return 'text-gray-500'
  const s = b?.state
  if (s === 'jamming') return 'text-red-300'
  if (s === 'interference') return 'text-amber-300'
  if (s === 'degraded') return 'text-yellow-300'
  if (d >= 6) return 'text-amber-300'
  if (d >= 3) return 'text-yellow-300'
  return 'text-emerald-300'
}
</script>

<template>
  <!-- Disconnected card: explicit, never masquerades as calibration. -->
  <div v-if="!store.enabled"
       class="bg-tactical-surface rounded-lg border border-gray-700 p-3">
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-2">
        <span class="font-display font-semibold text-sm tracking-wide text-gray-500">RF SPECTRUM</span>
        <span class="text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded bg-gray-700/60 text-gray-400 inline-flex items-center gap-1">
          <span class="inline-block w-1.5 h-1.5 rounded-full bg-gray-500"></span>
          disconnected
        </span>
      </div>
      <span class="text-[10px] text-gray-500">no RTL-SDR dongle detected</span>
    </div>
  </div>

  <div v-else
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
                :title="store.paused ? 'Resume updates' : 'Pause updates'"
                @click="onPauseClick"
                @keyup.enter.stop @keyup.space.stop>
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
        <span :class="ageStale ? 'text-amber-400' : 'text-gray-400'" :title="'last scan ' + ageText">
          {{ ageText }}
        </span>
        <span class="text-gray-500">· click for detail</span>
      </div>
    </div>

    <div class="band-rows">
      <div v-for="name in orderedBands" :key="name" class="band-row">
        <div class="band-name">
          <span class="dot" :style="{ background: stateColour(store.bands[name]?.state) }" />
          <span>{{ shortLabel(name) }}</span>
        </div>

        <span class="state-chip"
              :class="stateChipClass(store.bands[name]?.state)">
          {{ store.bands[name]?.state || 'unknown' }}
        </span>

        <div class="delta-cell" :class="deltaClass(store.bands[name])">
          <template v-if="store.bands[name]?.state === 'calibrating'">—</template>
          <template v-else>{{ deltaText(store.bands[name]) }}</template>
        </div>

        <div class="spark-cell">
          <template v-if="calibrationInfo(name)">
            <div v-if="calibrationInfo(name).active" class="cal-inline">
              <div class="cal-bar" :style="{ width: calibrationInfo(name).pct + '%' }" />
              <span class="cal-label">calibrating · {{ calibrationInfo(name).remainingSec }}s</span>
            </div>
            <div v-else class="cal-inline cal-queued">
              <span class="cal-label">queued</span>
            </div>
          </template>
          <canvas v-else
                  :ref="el => { if (el) sparks[name] = el }"
                  class="spark" />
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.band-rows {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.band-row {
  display: grid;
  grid-template-columns: 88px 108px 72px 1fr;
  gap: 8px;
  align-items: center;
  padding: 3px 4px;
  border-radius: 3px;
}
.band-row:hover {
  background: rgba(30, 41, 59, 0.35);
}
.band-name {
  display: flex;
  align-items: center;
  gap: 6px;
  font-family: monospace;
  font-size: 11px;
  color: #cbd5e1;
  letter-spacing: 0.03em;
}
.band-name .dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  display: inline-block;
  flex-shrink: 0;
}
.state-chip {
  font-family: monospace;
  font-size: 9px;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  padding: 2px 6px;
  border-radius: 3px;
  border: 1px solid;
  text-align: center;
  font-weight: 600;
}
.delta-cell {
  font-family: monospace;
  font-size: 11px;
  font-weight: 600;
  text-align: right;
  letter-spacing: 0.02em;
}
.spark-cell {
  position: relative;
  height: 22px;
  min-width: 60px;
  background: rgba(2, 6, 23, 0.55);
  border-radius: 2px;
  overflow: hidden;
}
.spark {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
}
.cal-inline {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0 6px;
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
.cal-label {
  position: relative;
  font-family: monospace;
  font-size: 9px;
  color: #e0e7ff;
  letter-spacing: 0.06em;
  font-weight: 600;
  text-transform: uppercase;
}
.cal-queued {
  background: rgba(15, 23, 42, 0.45);
}
.cal-queued .cal-label { color: #94a3b8; font-weight: 500; }
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
