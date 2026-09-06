<script setup>
// TTC mode: the booth demo screen (MESHSAT-826).
//
// One screen, one story, driven only by what this kit actually observes:
// a Meshtastic message leaves the T-Deck on mesh A, this kit relays it
// over the APRS link (SMS when APRS is silent), the far kit hands it to
// mesh B, and the reply comes back the same way. There is no network
// between the kits at the booth, so the far half is drawn quiet until
// the far kit is heard over the air.
//
// Real signals used:
//   SSE /api/events   `packet` (lora / aprs / sms, rx / tx), `inbound`,
//                     `delivery_*` (queued, delivered/sent, retry, dead)
//   GET /api/aprs/status      callsign (which kit we are), receive state
//   GET /api/devices/health   the channel chips
//   GET /api/packets(+/rates) the nerds drawer and the rate lines
//   GET /api/deliveries       the ledger tail
//   GET /api/nodes            node names for the end devices
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import api from '@/api/client'
import { useMeshsatStore } from '@/stores/meshsat'
import SpectrumWaterfall from '@/components/SpectrumWaterfall.vue'

const router = useRouter()
const route = useRoute()
const store = useMeshsatStore()

// ── identity ─────────────────────────────────────────────────────────
// Which kit is this panel? From the APRS callsign, overridable with ?kit=.
const KITS = {
  parallax: { name: 'parallax', callsign: 'MSPRLX-10', side: 'left', modem: 'RockBLOCK 9704', peer: 'tesseract' },
  tesseract: { name: 'tesseract', callsign: 'MSTSRT-10', side: 'right', modem: 'RockBLOCK 9603', peer: 'parallax' },
}
const kitName = ref(route.query.kit === 'tesseract' ? 'tesseract' : route.query.kit === 'parallax' ? 'parallax' : '')
const me = computed(() => KITS[kitName.value] || KITS.parallax)
const peer = computed(() => KITS[me.value.peer])
// On parallax the near device is the T-Deck (left); on tesseract the T-Echo (right).
const nearIsLeft = computed(() => me.value.side === 'left')

// ── live state ───────────────────────────────────────────────────────
const aprs = ref({})            // /api/aprs/status
const health = ref([])          // /api/devices/health targets
const rates = ref(null)         // /api/packets/rates
const nodes = ref([])           // /api/nodes
const packets = ref([])         // newest first, capped
const deliveries = ref([])
const farHeardAt = ref(0)       // ms epoch of the last APRS frame from the peer
const clock = ref('')
const sseUp = ref(false)
const showText = ref(true)
try { showText.value = localStorage.getItem('meshsat.ttc.hidetext') !== '1' } catch {}

const chip = (name) => {
  const t = health.value.find(x => x.name === name)
  if (!t) return { state: 'unknown', detail: '' }
  return { state: t.state, detail: t.detail || '' }
}
const chips = computed(() => ([
  { key: 'mesh', label: 'LoRa mesh', ...chip('mesh') },
  { key: 'aprs', label: 'APRS 144.800', ...chip('aprs') },
  { key: 'cellular', label: 'SMS', ...chip('cellular') },
]))
const aprsSilent = computed(() => aprs.value.receive_state === 'deaf' || aprs.value.receive_state === 'quiet')
const farAgeS = computed(() => farHeardAt.value ? Math.round((now.value - farHeardAt.value) / 1000) : null)
const farAlive = computed(() => farAgeS.value !== null && farAgeS.value < 600)
const now = ref(Date.now())

// Node names for the end devices, from this kit's own node table.
const nodeName = (id) => {
  if (!id) return ''
  const n = (nodes.value || []).find(x => x.user_id === id || `!${(x.num >>> 0).toString(16).padStart(8, '0')}` === id)
  return n ? (n.short_name || n.long_name || id) : id
}
const nearDeviceNode = ref('')  // last LoRa sender seen on this kit

// ── trip engine ──────────────────────────────────────────────────────
// A trip is one message crossing the screen. Stages are appended only
// when a real event arrives; the dot tweens between anchor points.
const trips = ref([])           // newest first, last 12
const current = ref(null)       // the trip being animated
const dot = reactive({ x: 0, y: 0, visible: false, lane: 'aprs', dir: 'out' })
let tween = null
let raf = 0

// Geometry (SVG viewBox 1280 x 400). Left to right: T-Deck, parallax, air, tesseract, T-Echo.
const G = {
  tdeck: { x: 140, y: 225 }, parallax: { x: 405, y: 225 }, airL: { x: 490, y: 225 },
  airR: { x: 790, y: 225 }, tesseract: { x: 875, y: 225 }, techo: { x: 1140, y: 225 },
  smsY: 335,
}
const kitPos = computed(() => nearIsLeft.value ? G.parallax : G.tesseract)
const devPos = computed(() => nearIsLeft.value ? G.tdeck : G.techo)
const airNear = computed(() => nearIsLeft.value ? G.airL : G.airR)
const airFar = computed(() => nearIsLeft.value ? G.airR : G.airL)

function ease(t) { return t < 0.5 ? 2 * t * t : -1 + (4 - 2 * t) * t }
function moveTo(x, y, ms) {
  tween = { x0: dot.x, y0: dot.y, x1: x, y1: y, t0: performance.now(), ms: Math.max(80, ms) }
  if (!raf) raf = requestAnimationFrame(step)
}
function step(ts) {
  raf = 0
  if (!tween) return
  const k = Math.min(1, (ts - tween.t0) / tween.ms)
  const e = ease(k)
  dot.x = tween.x0 + (tween.x1 - tween.x0) * e
  dot.y = tween.y0 + (tween.y1 - tween.y0) * e
  if (k < 1) raf = requestAnimationFrame(step); else tween = null
}
function jump(x, y) { tween = null; dot.x = x; dot.y = y }

function newTrip(dir, seed) {
  const t = {
    id: Date.now().toString(36) + Math.random().toString(36).slice(2, 6),
    dir, lane: 'aprs', text: seed.text || '', from: seed.from || '', bytes: seed.bytes || 0,
    rssi: seed.rssi || 0, snr: seed.snr || 0, hops: seed.hops || 0,
    startedAt: Date.now(), stages: [], legs: {}, done: false, failed: false,
  }
  trips.value.unshift(t); trips.value.splice(12)
  current.value = t
  dot.dir = dir; dot.lane = 'aprs'; dot.visible = true
  return t
}
function stage(t, name, extra) {
  const at = Date.now()
  const prev = t.stages[t.stages.length - 1]
  t.stages.push({ name, at, ...extra })
  if (prev) t.legs[name] = at - prev.at
}
const legLabel = computed(() => {
  const t = current.value; if (!t) return []
  const out = []
  const l = t.legs
  if (t.dir === 'out') {
    if (l.queued !== undefined) out.push({ k: 'LoRa in to relay rule', v: l.queued })
    if (l.sent !== undefined) out.push({ k: t.lane === 'sms' ? 'SMS accepted by KPN' : 'On the air (APRS)', v: l.sent })
  } else {
    if (l.queued !== undefined) out.push({ k: 'Air in to relay rule', v: l.queued })
    if (l.sent !== undefined) out.push({ k: 'Handed to LoRa', v: l.sent })
  }
  return out
})

// Event wiring. `out` = leaves the near device, crosses the air.
// `in` = arrives from the air, ends at the near device.
function onPacket(p) {
  packets.value.unshift(p); packets.value.splice(400)
  if (p.bearer === 'lora' && p.dir === 'rx') {
    nearDeviceNode.value = p.from
    // A text message from the near device starts an outbound trip.
    if (p.portnum === 1 || (p.portnum_name || '').includes('TEXT')) {
      const t = newTrip('out', p)
      stage(t, 'lora_rx')
      jump(devPos.value.x, devPos.value.y)
      moveTo(kitPos.value.x, kitPos.value.y, 900)
    }
  } else if (p.bearer === 'aprs' && p.dir === 'rx') {
    if (p.from && peer.value.callsign && p.from.toUpperCase().startsWith(peer.value.callsign.split('-')[0])) {
      farHeardAt.value = Date.now()
    }
    // A frame from the far kit starts an inbound trip (text may be
    // empty: encrypted frames decode inside the bridge).
    if (!current.value || current.value.done || current.value.dir !== 'in') {
      const t = newTrip('in', p)
      stage(t, 'aprs_rx')
      jump(airFar.value.x, airFar.value.y)
      moveTo(airNear.value.x, airNear.value.y, 1400)
    }
  } else if (p.bearer === 'aprs' && p.dir === 'tx') {
    const t = current.value
    if (t && t.dir === 'out' && !t.done) { stage(t, 'aprs_tx', { bytes: p.bytes, raw: p.raw }); t.bytes = p.bytes || t.bytes }
  } else if (p.bearer === 'sms' && p.dir === 'rx') {
    if (!current.value || current.value.done || current.value.dir !== 'in') {
      const t = newTrip('in', p); t.lane = 'sms'; dot.lane = 'sms'
      stage(t, 'sms_rx')
      jump(airFar.value.x, G.smsY); moveTo(airNear.value.x, G.smsY, 1400)
    }
  } else if (p.bearer === 'lora' && p.dir === 'tx') {
    const t = current.value
    if (t && t.dir === 'in' && !t.done) { stage(t, 'lora_tx', { bytes: p.bytes }) }
  }
}
function onDelivery(ev) {
  const d = ev.data || {}
  const ch = d.channel || ''
  const t = current.value
  if (!t || t.done) return
  const status = (d.status || ev.type.replace('delivery_', '')).toLowerCase()
  if (status === 'queued') {
    if (t.dir === 'out' && (ch.startsWith('aprs') || ch.startsWith('cellular'))) {
      t.lane = ch.startsWith('cellular') ? 'sms' : 'aprs'; dot.lane = t.lane
      stage(t, 'queued', { channel: ch }); t.msgRef = d.msg_ref
      moveTo(kitPos.value.x, t.lane === 'sms' ? G.smsY : kitPos.value.y, 450)
    } else if (t.dir === 'in' && ch.startsWith('mesh')) {
      stage(t, 'queued', { channel: ch }); t.msgRef = d.msg_ref
      moveTo(kitPos.value.x, kitPos.value.y, 500)
    }
  } else if (status === 'delivered' || status === 'sent') {
    if (t.dir === 'out' && (ch.startsWith('aprs') || ch.startsWith('cellular'))) {
      stage(t, 'sent', { channel: ch, latency: d.latency_ms })
      const y = t.lane === 'sms' ? G.smsY : airFar.value.y
      moveTo(airFar.value.x, y, 1600)
      finish(t, false, 1800)
    } else if (t.dir === 'in' && ch.startsWith('mesh')) {
      stage(t, 'sent', { channel: ch, latency: d.latency_ms })
      moveTo(devPos.value.x, devPos.value.y, 900)
      finish(t, false, 1100)
    }
  } else if (status === 'dead' || status === 'failed') {
    if (ch.startsWith('aprs') || ch.startsWith('cellular') || ch.startsWith('mesh')) {
      stage(t, 'failed', { channel: ch, error: d.error })
      finish(t, true, 200)
    }
  }
}
function finish(t, failed, after) {
  t.failed = failed
  setTimeout(() => { t.done = true; if (current.value === t) dot.visible = false }, after)
}
function onEvent(ev) {
  if (!ev || typeof ev !== 'object') return
  if (ev.type === 'connected_to_stream') { sseUp.value = true; return }
  if (ev.type === 'packet') { try { onPacket(typeof ev.data === 'string' ? JSON.parse(ev.data) : ev.data) } catch {} ; return }
  if (typeof ev.type === 'string' && ev.type.startsWith('delivery_')) {
    try { onDelivery({ type: ev.type, data: typeof ev.data === 'string' ? JSON.parse(ev.data) : ev.data }) } catch {}
    return
  }
  // A text message on this kit's inbox that we have not seen as a packet
  // (older bridge without the packet feed): still draw the outbound leg.
  if (ev.type === 'message' && ev.data && !current.value) {
    try {
      const m = typeof ev.data === 'string' ? JSON.parse(ev.data) : ev.data
      if (m.source === 'mesh' && m.text) { onPacket({ bearer: 'lora', dir: 'rx', from: m.from, text: m.text, portnum: 1, bytes: (m.text || '').length }) }
    } catch {}
  }
}

// ── polling ──────────────────────────────────────────────────────────
let sse = null
let timers = []
async function poll() {
  const [a, h, r, n, d] = await Promise.allSettled([
    api.get('/aprs/status'), api.get('/devices/health'), api.get('/packets/rates'),
    api.get('/nodes'), api.get('/deliveries?limit=20'),
  ])
  if (a.status === 'fulfilled' && a.value) {
    aprs.value = a.value
    if (!kitName.value && a.value.callsign) {
      const cs = String(a.value.callsign).toUpperCase()
      kitName.value = cs.startsWith('MSTSRT') ? 'tesseract' : 'parallax'
    }
  }
  if (h.status === 'fulfilled' && h.value) health.value = h.value.targets || []
  if (r.status === 'fulfilled' && r.value) rates.value = r.value
  if (n.status === 'fulfilled' && n.value) nodes.value = Array.isArray(n.value) ? n.value : (n.value.nodes || [])
  if (d.status === 'fulfilled' && d.value) deliveries.value = Array.isArray(d.value) ? d.value : (d.value.deliveries || [])
}
async function loadPackets() {
  try {
    const p = await api.get('/packets?limit=200')
    if (p && Array.isArray(p.packets)) packets.value = p.packets
    // Seed "far heard" from the ring so the far island is right on load.
    const far = packets.value.find(x => x.bearer === 'aprs' && x.dir === 'rx' && x.from && x.from.toUpperCase().startsWith(peer.value.callsign.split('-')[0]))
    if (far) farHeardAt.value = new Date(far.time).getTime()
  } catch {}
}
const rate = (bearer, dir) => {
  const r = rates.value && rates.value.bearers && rates.value.bearers[bearer]
  return r ? (r[dir] || 0) : 0
}
// Fallback rate from the local ring when /api/packets/rates is missing.
const localRate = (bearer, dir, windowS = 60) => {
  const cut = Date.now() - windowS * 1000
  return packets.value.filter(p => p.bearer === bearer && p.dir === dir && new Date(p.time).getTime() >= cut).length
}
const rateOf = (bearer, dir) => rates.value ? rate(bearer, dir) : localRate(bearer, dir)

// ── nerds drawer ─────────────────────────────────────────────────────
const drawer = ref(false)
const tab = ref('packets')
const tabs = [
  { k: 'packets', l: 'Packets' }, { k: 'rates', l: 'Rates' }, { k: 'pipeline', l: 'Pipeline' },
  { k: 'ledger', l: 'Ledger' }, { k: 'health', l: 'Health' },
]
const fmtT = (iso) => { try { return new Date(iso).toISOString().slice(11, 23).replace('T', ' ') } catch { return '' } }
function spark(bearer, dir) {
  // 30 buckets of 10 s over the last 5 min, from the local ring.
  const n = 30, w = 10000, nowMs = Date.now()
  const b = new Array(n).fill(0)
  for (const p of packets.value) {
    if (p.bearer !== bearer || p.dir !== dir) continue
    const age = nowMs - new Date(p.time).getTime()
    if (age < 0 || age >= n * w) continue
    b[n - 1 - Math.floor(age / w)]++
  }
  const max = Math.max(1, ...b)
  return b.map((v, i) => `${(i / (n - 1) * 100).toFixed(1)},${(100 - v / max * 100).toFixed(1)}`).join(' ')
}
const lastOut = computed(() => trips.value.find(t => t.dir === 'out'))
const lastIn = computed(() => trips.value.find(t => t.dir === 'in'))
const pipeline = computed(() => {
  const t = lastOut.value; if (!t) return null
  const typed = (t.text || '').length
  const air = t.bytes || 0
  return { typed, air, ratio: typed ? (air / typed).toFixed(2) : '', lane: t.lane }
})

// ── attract cycle, exit, text toggle ─────────────────────────────────
const IDLE_MS = 180000
const CYCLE = [{ v: 'route', ms: 60000 }, { v: 'spectrum', ms: 30000 }, { v: 'nerds', ms: 30000 }]
const view = ref('route')       // route | spectrum | nerds (attract)
let lastTouch = Date.now()
let cycleIdx = 0
let cycleAt = 0
function touch() {
  lastTouch = Date.now()
  if (view.value !== 'route') { view.value = 'route'; cycleIdx = 0 }
}
function tickAttract() {
  const idle = Date.now() - lastTouch
  if (idle < IDLE_MS) return
  if (!cycleAt || Date.now() - cycleAt >= CYCLE[cycleIdx].ms) {
    cycleIdx = (cycleIdx + 1) % CYCLE.length
    view.value = CYCLE[cycleIdx].v
    cycleAt = Date.now()
  }
}
let pressTimer = null
function pressStart() { pressTimer = setTimeout(() => router.push('/'), 1500) }
function pressEnd() { if (pressTimer) { clearTimeout(pressTimer); pressTimer = null } }
function onKey(e) { if (e.key === 'Escape') router.push('/') }
function toggleText() {
  showText.value = !showText.value
  try { localStorage.setItem('meshsat.ttc.hidetext', showText.value ? '0' : '1') } catch {}
}

// ── operator test control: one frame through the ledger ─────────────
const testArmed = ref(false)
const testBusy = ref(false)
const testNote = ref('')
async function sendTest() {
  if (!testArmed.value) { testArmed.value = true; setTimeout(() => { testArmed.value = false }, 4000); return }
  testArmed.value = false; testBusy.value = true; testNote.value = ''
  try {
    const text = `TTC test ${new Date().toISOString().slice(11, 19)}Z`
    await api.post('/messages/send', { text, gateway: 'aprs', precedence: 'Routine' })
    // Draw it as an outbound trip from the kit itself (no LoRa leg).
    const t = newTrip('out', { text, from: me.value.callsign, bytes: text.length })
    stage(t, 'test')
    jump(kitPos.value.x, kitPos.value.y)
    testNote.value = 'queued on the ledger'
  } catch (e) {
    testNote.value = (e && e.message) || 'send failed'
  } finally { testBusy.value = false }
}

const displayText = (t) => !t ? '' : (showText.value ? (t.text || (t.dir === 'in' ? 'encrypted frame, decoded inside the bridge' : '')) : 'text hidden')

onMounted(async () => {
  document.documentElement.classList.add('ttc-mode')
  window.addEventListener('keydown', onKey)
  window.addEventListener('pointerdown', touch, { passive: true })
  await poll()
  await loadPackets()
  sse = api.sse('/events', onEvent, () => { sseUp.value = false })
  timers.push(setInterval(poll, 5000))
  timers.push(setInterval(() => { now.value = Date.now(); clock.value = new Date().toISOString().slice(11, 19) + 'Z'; tickAttract() }, 1000))
  clock.value = new Date().toISOString().slice(11, 19) + 'Z'
})
onUnmounted(() => {
  document.documentElement.classList.remove('ttc-mode')
  window.removeEventListener('keydown', onKey)
  window.removeEventListener('pointerdown', touch)
  if (sse) sse.close()
  timers.forEach(clearInterval)
  if (raf) cancelAnimationFrame(raf)
})
</script>

<template>
  <div class="ttc fixed inset-0 z-[60] bg-gray-950 text-gray-50 flex flex-col select-none overflow-hidden">
    <!-- header: mark (long-press exits), tagline, chips, clock -->
    <header class="flex items-center gap-4 px-5 h-14 shrink-0 border-b border-gray-800">
      <div class="flex items-center gap-2 shrink-0"
           @pointerdown="pressStart" @pointerup="pressEnd" @pointercancel="pressEnd" @pointerleave="pressEnd"
           title="Hold to leave TTC mode">
        <img src="/meshsat-mark.png" alt="" class="h-7 w-auto" draggable="false" />
        <span class="font-display font-semibold text-base tracking-wide">MeshSat</span>
      </div>
      <p class="font-sans text-sm text-gray-300 ml-2">Keeping people connected when the network is not.</p>
      <div class="ml-auto flex items-center gap-2">
        <span v-for="c in chips" :key="c.key"
          class="chip font-mono text-[11px] px-2 py-1 rounded border"
          :class="c.state === 'ok' ? 'border-emerald-500/40 text-emerald-300' : c.state === 'healing' ? 'border-amber-500/50 text-amber-300' : 'border-gray-700 text-gray-500'"
          :title="c.detail">
          <span class="inline-block w-1.5 h-1.5 rounded-full mr-1 align-middle"
            :class="c.state === 'ok' ? 'bg-emerald-400' : c.state === 'healing' ? 'bg-amber-400 animate-pulse' : 'bg-gray-600'" />{{ c.label }}
        </span>
        <span class="font-mono text-lg text-gray-200 tabular-nums ml-3">{{ clock }}</span>
      </div>
    </header>

    <!-- ROUTE VIEW -->
    <main v-show="view === 'route'" class="flex-1 flex flex-col min-h-0">
      <div class="route-wrap flex-1 min-h-0 flex items-center">
        <svg class="w-full h-full" viewBox="0 0 1280 470" preserveAspectRatio="xMidYMid meet" aria-label="Message route">
          <defs>
            <filter id="glow" x="-100%" y="-100%" width="300%" height="300%">
              <feGaussianBlur stdDeviation="6" result="b" /><feMerge><feMergeNode in="b" /><feMergeNode in="SourceGraphic" /></feMerge>
            </filter>
            <radialGradient id="dotg"><stop offset="0" stop-color="#FFE2D1" /><stop offset="0.45" stop-color="#F96118" /><stop offset="1" stop-color="#F96118" stop-opacity="0" /></radialGradient>
          </defs>

          <!-- islands: mesh A (left) and mesh B (right) -->
          <g :class="['island', nearIsLeft ? 'near' : (farAlive ? 'far-alive' : 'far')]">
            <rect x="36" y="64" width="464" height="330" rx="28" />
            <text x="62" y="98" class="island-label">mesh A</text>
            <text x="62" y="117" class="island-sub">LoRa 868 MHz, its own channel key</text>
          </g>
          <g :class="['island', !nearIsLeft ? 'near' : (farAlive ? 'far-alive' : 'far')]">
            <rect x="780" y="64" width="464" height="330" rx="28" />
            <text x="806" y="98" class="island-label">mesh B</text>
            <text x="806" y="117" class="island-sub">LoRa 868 MHz, a different channel key</text>
          </g>

          <!-- lanes -->
          <g class="lanes">
            <line :x1="G.tdeck.x + 84" :y1="G.tdeck.y" :x2="G.parallax.x - 84" :y2="G.parallax.y" class="lane lora" :class="nearIsLeft ? 'near' : (farAlive ? 'far-alive' : 'far')" />
            <line :x1="G.tesseract.x + 84" :y1="G.tesseract.y" :x2="G.techo.x - 44" :y2="G.techo.y" class="lane lora" :class="!nearIsLeft ? 'near' : (farAlive ? 'far-alive' : 'far')" />
            <!-- APRS air link -->
            <g class="air" :class="{ silent: aprsSilent }">
              <line :x1="G.airL.x" :y1="G.airL.y" :x2="G.airR.x" :y2="G.airR.y" class="lane air-line" />
              <g v-for="i in 3" :key="'wl'+i" class="wave" :style="{ animationDelay: (i * 0.5) + 's' }">
                <path :d="`M ${G.airL.x + 6 + i*14} ${G.airL.y - 12 - i*8} A ${14 + i*8} ${14 + i*8} 0 0 1 ${G.airL.x + 6 + i*14} ${G.airL.y + 12 + i*8}`" />
              </g>
              <g v-for="i in 3" :key="'wr'+i" class="wave" :style="{ animationDelay: (i * 0.5) + 's' }">
                <path :d="`M ${G.airR.x - 6 - i*14} ${G.airR.y - 12 - i*8} A ${14 + i*8} ${14 + i*8} 0 0 0 ${G.airR.x - 6 - i*14} ${G.airR.y + 12 + i*8}`" />
              </g>
              <text :x="(G.airL.x + G.airR.x)/2" :y="G.airL.y - 92" class="air-label" text-anchor="middle">APRS on 144.800 MHz</text>
              <text :x="(G.airL.x + G.airR.x)/2" :y="G.airL.y - 72" class="air-sub" text-anchor="middle">AX.25, 1200 baud, encrypted</text>
              <text v-if="aprsSilent" :x="(G.airL.x + G.airR.x)/2" :y="G.airL.y + 44" class="air-warn" text-anchor="middle">receiver silent on this kit, SMS carries the reply</text>
            </g>
            <!-- SMS fallback lane -->
            <g class="sms">
              <line :x1="G.airL.x" :y1="G.smsY" :x2="G.airR.x" :y2="G.smsY" class="lane sms-line" />
              <text :x="(G.airL.x + G.airR.x)/2" :y="G.smsY + 24" class="sms-label" text-anchor="middle">SMS over KPN when the air link is silent</text>
            </g>
          </g>

          <!-- stations -->
          <g :transform="`translate(${G.tdeck.x},${G.tdeck.y})`" class="station" :class="nearIsLeft ? 'near' : (farAlive ? 'far-alive' : 'far')">
            <!-- LilyGO T-Deck Plus, front: 2.8 inch screen over a 35-key
                 keyboard, trackball at the lower left, SMA stub top left -->
            <g class="device" transform="scale(1.35)">
              <rect x="-60" y="-38" width="120" height="76" rx="9" class="body" />
              <rect x="-54" y="-32" width="108" height="42" rx="2" class="bezel" />
              <rect x="-50" y="-29" width="100" height="36" rx="1" class="screen" />
              <g class="ui">
                <rect x="-46" y="-25" width="44" height="7" rx="3" class="bubble" />
                <rect x="-2" y="-15" width="48" height="7" rx="3" class="bubble far" />
                <rect x="-46" y="-5" width="30" height="7" rx="3" class="bubble" />
              </g>
              <circle cx="-49" cy="18" r="5.5" class="trackball" /><circle cx="-49" cy="18" r="2.4" class="trackball-in" />
              <g class="keys">
                <rect v-for="k in 10" :key="'r1'+k" :x="-39 + (k-1)*9" y="13" width="7.6" height="4.6" rx="1" />
                <rect v-for="k in 10" :key="'r2'+k" :x="-39 + (k-1)*9" y="19" width="7.6" height="4.6" rx="1" />
                <rect v-for="k in 10" :key="'r3'+k" :x="-39 + (k-1)*9" y="25" width="7.6" height="4.6" rx="1" />
                <rect x="-39" y="31" width="16.6" height="4.6" rx="1" /><rect x="-21" y="31" width="43.6" height="4.6" rx="1" /><rect x="24" y="31" width="25.6" height="4.6" rx="1" />
              </g>
              <rect x="-58" y="-48" width="7" height="10" rx="1.5" class="sma" />
              <line x1="-54.5" y1="-48" x2="-60" y2="-72" class="ant" />
            </g>
            <text y="72" text-anchor="middle" class="st-name">T-Deck Plus</text>
            <text y="90" text-anchor="middle" class="st-sub">{{ nearIsLeft && nearDeviceNode ? nodeName(nearDeviceNode) : 'Meshtastic, keyboard' }}</text>
          </g>

          <g :transform="`translate(${G.parallax.x},${G.parallax.y})`" class="station kit" :class="nearIsLeft ? 'near' : (farAlive ? 'far-alive' : 'far')">
            <!-- MeshSat field kit V1: the CAD hero render from the
                 meshsat-fieldkit repo, recoloured onto the brand palette -->
            <image href="/kit-v1.png" x="-82" y="-108" width="164" height="194" class="kit-img" />
            <text y="108" text-anchor="middle" class="st-name">parallax</text>
            <text y="126" text-anchor="middle" class="st-sub">MeshSat kit, {{ KITS.parallax.callsign }}, {{ KITS.parallax.modem }}</text>
          </g>

          <g :transform="`translate(${G.tesseract.x},${G.tesseract.y})`" class="station kit" :class="!nearIsLeft ? 'near' : (farAlive ? 'far-alive' : 'far')">
            <!-- MeshSat field kit V1: the CAD hero render from the
                 meshsat-fieldkit repo, recoloured onto the brand palette -->
            <image href="/kit-v1.png" x="-82" y="-108" width="164" height="194" class="kit-img" />
            <text y="108" text-anchor="middle" class="st-name">tesseract</text>
            <text y="126" text-anchor="middle" class="st-sub">MeshSat kit, {{ KITS.tesseract.callsign }}, {{ KITS.tesseract.modem }}</text>
          </g>

          <g :transform="`translate(${G.techo.x},${G.techo.y})`" class="station" :class="!nearIsLeft ? 'near' : (farAlive ? 'far-alive' : 'far')">
            <!-- LilyGO T-Echo, front: 1.54 inch e-paper, one front button,
                 side buttons, SMA stub top right -->
            <g class="device paper" transform="scale(1.35)">
              <rect x="-30" y="-46" width="60" height="92" rx="10" class="body" />
              <rect x="-25" y="-41" width="50" height="50" rx="2" class="bezel" />
              <rect x="-22" y="-38" width="44" height="44" class="epaper" />
              <g class="ink">
                <rect x="-18" y="-33" width="24" height="3" rx="1" /><rect x="-18" y="-27" width="34" height="3" rx="1" />
                <rect x="-18" y="-21" width="28" height="3" rx="1" /><rect x="-18" y="-15" width="36" height="3" rx="1" />
                <rect x="-18" y="-4" width="20" height="3" rx="1" />
              </g>
              <circle cx="0" cy="26" r="6" class="btn" /><circle cx="0" cy="26" r="2.5" class="btn-in" />
              <rect x="29" y="-20" width="3" height="10" rx="1" class="sidebtn" /><rect x="29" y="-6" width="3" height="10" rx="1" class="sidebtn" />
              <rect x="20" y="-56" width="7" height="10" rx="1.5" class="sma" />
              <line x1="23.5" y1="-56" x2="30" y2="-80" class="ant" />
            </g>
            <text y="86" text-anchor="middle" class="st-name">T-Echo</text>
            <text y="104" text-anchor="middle" class="st-sub">{{ !nearIsLeft && nearDeviceNode ? nodeName(nearDeviceNode) : 'Meshtastic, e-paper' }}</text>
          </g>

          <!-- far-side proof line -->
          <text :x="nearIsLeft ? 1012 : 268" y="424" text-anchor="middle" class="far-note">
            {{ farAlive ? `${peer.name} heard over the air ${farAgeS} s ago` : `${peer.name} not heard yet on this kit` }}
          </text>

          <!-- the message -->
          <g v-if="dot.visible" class="msg" :class="[dot.lane, current && current.failed ? 'failed' : '']" :transform="`translate(${dot.x},${dot.y})`">
            <circle r="22" fill="url(#dotg)" opacity="0.55" />
            <circle r="7" class="core" filter="url(#glow)" />
          </g>
        </svg>
      </div>

      <!-- strip under the route: last message, legs, rates -->
      <section class="grid grid-cols-12 gap-3 px-5 pb-4 shrink-0">
        <div class="col-span-5 rounded-lg border border-gray-800 bg-gray-900/60 px-4 py-3 min-h-[92px]">
          <div class="font-sans text-xs text-gray-400 mb-1">
            {{ current ? (current.dir === 'out' ? `Last message out, from ${nodeName(current.from) || current.from || 'the mesh'}` : `Last message in, from ${current.from || peer.callsign}`) : 'Waiting for the first message' }}
          </div>
          <button type="button" class="text-left w-full font-display text-2xl leading-tight text-gray-50 break-words" @click="toggleText" :title="showText ? 'Tap to hide message text' : 'Tap to show message text'">
            {{ current ? displayText(current) : `Type on the ${nearIsLeft ? 'T-Deck' : 'T-Echo'}. The message crosses this screen as it really travels.` }}
          </button>
          <div v-if="current" class="font-mono text-[11px] text-gray-400 mt-1">
            {{ current.bytes || (current.text || '').length }} bytes on the wire
            <span v-if="current.rssi"> · RSSI {{ current.rssi }} dBm</span><span v-if="current.snr"> · SNR {{ current.snr }} dB</span>
            <span v-if="current.hops"> · {{ current.hops }} hop{{ current.hops > 1 ? 's' : '' }}</span>
            <span v-if="current.failed" class="text-red-300"> · failed</span>
          </div>
        </div>
        <div class="col-span-3 rounded-lg border border-gray-800 bg-gray-900/60 px-4 py-3">
          <div class="font-sans text-xs text-gray-400 mb-1">Legs, measured</div>
          <div v-if="legLabel.length" class="space-y-1">
            <div v-for="l in legLabel" :key="l.k" class="flex items-baseline justify-between gap-2">
              <span class="font-sans text-xs text-gray-300">{{ l.k }}</span>
              <span class="font-mono text-base tabular-nums text-teal-300">{{ l.v }} ms</span>
            </div>
          </div>
          <div v-else class="font-mono text-sm text-gray-500">no trip yet</div>
        </div>
        <div class="col-span-4 rounded-lg border border-gray-800 bg-gray-900/60 px-4 py-3">
          <div class="font-sans text-xs text-gray-400 mb-1">Last minute on this kit</div>
          <div class="grid grid-cols-2 gap-x-3 font-mono text-sm tabular-nums">
            <span class="text-gray-300">LoRa</span><span class="text-gray-50">{{ rateOf('lora','rx') }} in · {{ rateOf('lora','tx') }} out</span>
            <span class="text-gray-300">APRS</span><span class="text-gray-50">{{ rateOf('aprs','rx') }} in · {{ rateOf('aprs','tx') }} out</span>
            <span class="text-gray-300">SMS</span><span class="text-gray-50">{{ rateOf('sms','rx') }} in · {{ rateOf('sms','tx') }} out</span>
          </div>
          <div class="mt-2 flex items-center gap-2">
            <button type="button" @click="drawer = !drawer" class="font-mono text-[11px] px-2 py-1 rounded border border-gray-700 text-gray-300 hover:border-teal-500 hover:text-teal-300 whitespace-nowrap">stats for nerds</button>
            <button type="button" @click="sendTest" :disabled="testBusy" class="font-mono text-[11px] px-2 py-1 rounded border whitespace-nowrap" :class="testArmed ? 'border-teal-500 text-teal-300' : 'border-gray-700 text-gray-400 hover:text-gray-200'">
              {{ testArmed ? 'tap again to send' : 'test frame' }}
            </button>
            <span v-if="testNote" class="font-mono text-[11px] text-gray-400">{{ testNote }}</span>
          </div>
        </div>
      </section>
    </main>

    <!-- SPECTRUM (attract) -->
    <main v-show="view === 'spectrum'" class="flex-1 min-h-0 overflow-hidden px-5 py-3">
      <div class="font-sans text-sm text-gray-300 mb-2">What the kit's software-defined radio hears right now, 868 MHz and 144.8 MHz among them. Touch to return.</div>
      <div class="h-full overflow-hidden"><SpectrumWaterfall v-if="view === 'spectrum'" /></div>
    </main>

    <!-- NERDS (drawer, also the attract page) -->
    <section v-show="drawer || view === 'nerds'"
      class="nerds absolute left-0 right-0 bottom-0 bg-gray-900 border-t border-gray-700 flex flex-col"
      :class="view === 'nerds' ? 'top-14' : 'h-[46%]'">
      <div class="flex items-center gap-1 px-4 pt-2 shrink-0">
        <button v-for="t in tabs" :key="t.k" type="button" @click="tab = t.k"
          class="font-mono text-xs px-3 py-1.5 rounded-t border-b-2"
          :class="tab === t.k ? 'border-teal-500 text-teal-300' : 'border-transparent text-gray-400 hover:text-gray-200'">{{ t.l }}</button>
        <span class="ml-auto font-mono text-[11px] text-gray-500">{{ sseUp ? 'live' : 'stream reconnecting' }}</span>
        <button v-if="view !== 'nerds'" type="button" @click="drawer = false" class="ml-3 font-mono text-xs text-gray-400 hover:text-gray-100 px-2">close</button>
      </div>
      <div class="flex-1 min-h-0 overflow-auto px-4 pb-3 pt-2">
        <!-- packets -->
        <table v-if="tab === 'packets'" class="w-full font-mono text-[11px] leading-5 tabular-nums">
          <thead class="text-gray-500 text-left sticky top-0 bg-gray-900"><tr>
            <th class="pr-3 font-normal">time</th><th class="pr-3 font-normal">bearer</th><th class="pr-3 font-normal">dir</th>
            <th class="pr-3 font-normal">from</th><th class="pr-3 font-normal">to</th><th class="pr-3 font-normal text-right">bytes</th>
            <th class="pr-3 font-normal text-right">rssi</th><th class="pr-3 font-normal text-right">snr</th><th class="pr-3 font-normal text-right">hops</th>
            <th class="pr-3 font-normal">port</th><th class="font-normal">payload</th>
          </tr></thead>
          <tbody>
            <tr v-for="(p, i) in packets.slice(0, 60)" :key="i" class="border-t border-gray-800/60 align-top">
              <td class="pr-3 text-gray-400 whitespace-nowrap">{{ fmtT(p.time) }}</td>
              <td class="pr-3" :class="p.bearer === 'lora' ? 'text-sky-300' : p.bearer === 'aprs' ? 'text-teal-300' : 'text-amber-300'">{{ p.bearer }}</td>
              <td class="pr-3 text-gray-300">{{ p.dir }}</td>
              <td class="pr-3 text-gray-200 whitespace-nowrap">{{ p.from }}</td>
              <td class="pr-3 text-gray-200 whitespace-nowrap">{{ p.to }}</td>
              <td class="pr-3 text-right text-gray-200">{{ p.bytes }}</td>
              <td class="pr-3 text-right text-gray-400">{{ p.rssi || '' }}</td>
              <td class="pr-3 text-right text-gray-400">{{ p.snr || '' }}</td>
              <td class="pr-3 text-right text-gray-400">{{ p.hops || '' }}</td>
              <td class="pr-3 text-gray-400 whitespace-nowrap">{{ p.portnum_name || p.path || '' }}</td>
              <td class="text-gray-300 break-all">{{ showText ? (p.text || p.raw || '') : (p.text ? 'text hidden' : (p.raw || '')) }}</td>
            </tr>
            <tr v-if="!packets.length"><td colspan="11" class="text-gray-500 py-3">No packets in the ring yet. This bridge build may predate the packet feed.</td></tr>
          </tbody>
        </table>
        <!-- rates -->
        <div v-else-if="tab === 'rates'" class="grid grid-cols-3 gap-4">
          <div v-for="b in ['lora','aprs','sms']" :key="b" class="rounded border border-gray-800 p-3">
            <div class="font-mono text-xs text-gray-400 mb-1">{{ b }} · last 5 minutes, 10 s buckets</div>
            <div v-for="d in ['rx','tx']" :key="d" class="mb-2">
              <div class="flex justify-between font-mono text-[11px]"><span class="text-gray-400">{{ d }}</span><span class="text-gray-200">{{ rateOf(b, d) }} / min</span></div>
              <svg viewBox="0 0 100 100" preserveAspectRatio="none" class="w-full h-8"><polyline :points="spark(b, d)" fill="none" :class="d === 'rx' ? 'stroke-teal-400' : 'stroke-sky-300'" stroke-width="2" vector-effect="non-scaling-stroke" /></svg>
            </div>
          </div>
        </div>
        <!-- pipeline -->
        <div v-else-if="tab === 'pipeline'" class="font-mono text-sm">
          <div v-if="pipeline" class="grid grid-cols-4 gap-3">
            <div class="rounded border border-gray-800 p-3"><div class="text-xs text-gray-400">typed</div><div class="text-2xl text-gray-50">{{ pipeline.typed }} B</div></div>
            <div class="rounded border border-gray-800 p-3"><div class="text-xs text-gray-400">smaz2 then AES-256-GCM then base64</div><div class="text-2xl text-gray-50">{{ pipeline.air }} B</div><div class="text-xs text-gray-500">on the {{ pipeline.lane === 'sms' ? 'SMS' : 'AX.25' }} wire</div></div>
            <div class="rounded border border-gray-800 p-3"><div class="text-xs text-gray-400">overhead</div><div class="text-2xl text-gray-50">{{ pipeline.ratio }}x</div></div>
            <div class="rounded border border-gray-800 p-3"><div class="text-xs text-gray-400">last trip legs</div><div v-for="(v,k) in (lastOut ? lastOut.legs : {})" :key="k" class="text-xs text-gray-300">{{ k }} {{ v }} ms</div></div>
          </div>
          <div v-else class="text-gray-500">No outbound trip observed yet.</div>
          <div class="mt-3 text-xs text-gray-400">Inbound frames decode inside the bridge before the relay rule runs; the reply's decoded size shows in the ledger row.</div>
        </div>
        <!-- ledger -->
        <table v-else-if="tab === 'ledger'" class="w-full font-mono text-[11px] leading-5 tabular-nums">
          <thead class="text-gray-500 text-left"><tr><th class="pr-3 font-normal">ref</th><th class="pr-3 font-normal">channel</th><th class="pr-3 font-normal">status</th><th class="pr-3 font-normal">to</th><th class="pr-3 font-normal text-right">retries</th><th class="font-normal">text</th></tr></thead>
          <tbody>
            <tr v-for="d in deliveries" :key="d.id" class="border-t border-gray-800/60">
              <td class="pr-3 text-gray-400">{{ d.msg_ref || d.id }}</td><td class="pr-3 text-gray-200">{{ d.channel || d.channel_id }}</td>
              <td class="pr-3" :class="d.status === 'delivered' ? 'text-emerald-300' : (d.status === 'dead' || d.status === 'failed') ? 'text-red-300' : 'text-gray-300'">{{ d.status }}</td>
              <td class="pr-3 text-gray-400">{{ d.destination || '' }}</td><td class="pr-3 text-right text-gray-400">{{ d.retries ?? d.attempts ?? '' }}</td>
              <td class="text-gray-300 break-all">{{ showText ? (d.text_preview || d.text || '') : 'text hidden' }}</td>
            </tr>
            <tr v-if="!deliveries.length"><td colspan="6" class="text-gray-500 py-3">Ledger empty.</td></tr>
          </tbody>
        </table>
        <!-- health -->
        <div v-else class="grid grid-cols-2 gap-2 font-mono text-xs">
          <div v-for="t in health" :key="t.name" class="flex items-center gap-3 rounded border border-gray-800 px-3 py-2">
            <span class="w-2 h-2 rounded-full" :class="t.state === 'ok' ? 'bg-emerald-400' : t.state === 'healing' ? 'bg-amber-400 animate-pulse' : 'bg-gray-600'" />
            <span class="w-20 text-gray-200">{{ t.name }}</span>
            <span class="text-gray-400 flex-1 truncate">{{ t.detail }}</span>
            <span class="text-gray-500">{{ (t.interfaces || []).join(' ') }}</span>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
/* Islands and lanes: Sand for the network diagram, Signal Orange only
   for the message and what is alive right now. The far half is quiet
   until the far kit is heard over the air. */
.island rect { fill: rgba(200, 184, 154, 0.04); stroke: #C8B89A; stroke-width: 1.5; stroke-dasharray: 6 8; }
.island .island-label { font-family: 'IBM Plex Mono', monospace; font-size: 15px; fill: #E4DAC6; letter-spacing: 0.04em; }
.island .island-sub { font-family: 'IBM Plex Sans', sans-serif; font-size: 11px; fill: #AE9C7A; }
.island.far { opacity: 0.35; }
.island.far-alive { opacity: 0.85; }
.lane { stroke: #8E7C5C; stroke-width: 2; }
.lane.lora.far { opacity: 0.35; }
.lane.lora.far-alive { opacity: 0.85; }
.air-line { stroke: #F7F7F4; stroke-opacity: 0.55; stroke-width: 2; }
.air.silent .air-line { stroke-dasharray: 3 9; stroke-opacity: 0.3; }
.wave path { fill: none; stroke: #F7F7F4; stroke-width: 1.5; opacity: 0; animation: wave 3s ease-out infinite; }
.air.silent .wave path { animation: none; opacity: 0.12; }
@keyframes wave { 0% { opacity: 0; } 20% { opacity: 0.7; } 100% { opacity: 0; } }
.air-label { font-family: 'IBM Plex Mono', monospace; font-size: 16px; fill: #F7F7F4; letter-spacing: 0.02em; }
.air-sub { font-family: 'IBM Plex Sans', sans-serif; font-size: 11px; fill: #B4B4BD; }
.air-warn { font-family: 'IBM Plex Sans', sans-serif; font-size: 12px; fill: #FCD34D; }
.sms-line { stroke: #E0B458; stroke-width: 1.5; stroke-dasharray: 10 8; opacity: 0.55; }
.sms-label { font-family: 'IBM Plex Sans', sans-serif; font-size: 11px; fill: #AE9C7A; }
.station .device .body { fill: #0E0E14; stroke: #C8B89A; stroke-width: 1.4; }
.station .device .bezel { fill: #08080B; stroke: #7A6B50; stroke-width: 0.8; }
.station .device .screen { fill: #101018; stroke: none; }
.station .device .ui .bubble { fill: #C8B89A; opacity: 0.55; }
.station .device .ui .bubble.far { fill: #F96118; opacity: 0.7; }
.station .device .trackball { fill: #1B1B22; stroke: #C8B89A; stroke-width: 1; }
.station .device .trackball-in { fill: #C8B89A; }
.station .device .keys rect { fill: #1B1B22; stroke: #7A6B50; stroke-width: 0.6; }
.station .device .sma { fill: #24242C; stroke: #C8B89A; stroke-width: 1; }
.station .device .ant { stroke: #C8B89A; stroke-width: 2.2; stroke-linecap: round; }
.station .device .epaper { fill: #E4DAC6; }
.station .device .ink rect { fill: #24242C; }
.station .device .btn { fill: #15151B; stroke: #C8B89A; stroke-width: 1; }
.station .device .btn-in { fill: #C8B89A; }
.station .device .sidebtn { fill: #24242C; stroke: #7A6B50; stroke-width: 0.6; }
.station .kit-img { filter: drop-shadow(0 0 8px rgba(200, 184, 154, 0.10)); }
.station.near .kit-img { filter: drop-shadow(0 0 10px rgba(200, 184, 154, 0.18)); }
.station.far { opacity: 0.35; }
.station.far-alive { opacity: 0.85; }
.st-name { font-family: 'IBM Plex Mono', monospace; font-size: 15px; fill: #F7F7F4; }
.st-sub { font-family: 'IBM Plex Sans', sans-serif; font-size: 11px; fill: #8A8A96; }
.far-note { font-family: 'IBM Plex Sans', sans-serif; font-size: 12px; fill: #8A8A96; }
.msg .core { fill: #F96118; }
.msg.sms .core { fill: #E0B458; }
.msg.failed .core { fill: #F0655A; }
.chip { transition: color 0.3s, border-color 0.3s; }
.route-wrap { padding: 0 12px; }
@media (prefers-reduced-motion: reduce) {
  .wave path { animation: none; opacity: 0.25; }
}
</style>
