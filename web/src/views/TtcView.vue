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
import TtcDeviceTDeck from '@/components/TtcDeviceTDeck.vue'
import TtcDeviceTEcho from '@/components/TtcDeviceTEcho.vue'
import PowerWidget from '@/components/PowerWidget.vue'

const router = useRouter()
const route = useRoute()
const store = useMeshsatStore()

// ── identity ─────────────────────────────────────────────────────────
// Which kit is this panel? From the APRS callsign, overridable with ?kit=.
// Booth placement (owner, 6 Sep 2026): tesseract on the LEFT of parallax.
// The keyboard T-Deck sits with parallax, the e-paper T-Echo with
// tesseract, so the story runs right to left across the table and the
// screens follow the table. `side` is where the box stands, `device` is
// what is paired with it, `mesh` the island letter.
const KITS = {
  parallax: { name: 'parallax', callsign: 'MSPRLX-10', side: 'right', device: 'tdeck', mesh: 'B', channel: 'msat-ttc-02', modem: 'RockBLOCK 9704', peer: 'tesseract' },
  tesseract: { name: 'tesseract', callsign: 'MSTSRT-10', side: 'left', device: 'techo', mesh: 'A', channel: 'msat-ttc-01', modem: 'RockBLOCK 9603', peer: 'parallax' },
}
const LEFT_KIT = 'tesseract'
const DEVICE_NAME = { tdeck: 'T-Deck', techo: 'T-Echo' }
const kitName = ref(route.query.kit === 'tesseract' ? 'tesseract' : route.query.kit === 'parallax' ? 'parallax' : '')
const me = computed(() => KITS[kitName.value] || KITS.parallax)
const peer = computed(() => KITS[me.value.peer])
// Geometry follows placement; the drawing follows the paired device.
const nearIsLeft = computed(() => me.value.side === 'left')
const nearDev = computed(() => me.value.device)
const nearDevName = computed(() => DEVICE_NAME[nearDev.value])
const leftKit = computed(() => KITS[LEFT_KIT])
const rightKit = computed(() => KITS[KITS[LEFT_KIT].peer])

// ── layout: a diptych by default ─────────────────────────────────────
// `half` (default): this panel shows its own half of the route at large
// scale and the air link runs off the screen edge toward the other kit.
// Two kits side by side, parallax on the left, tesseract on the right,
// form one picture with the real air gap between the screens. `full`
// (?layout=full): the whole route on one panel, for a lone kit.
const layout = ref(route.query.layout === 'full' ? 'full' : 'half')

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
const aprsSilent = computed(() => aprs.value.receive_state === 'deaf')
const aprsQuiet = computed(() => aprs.value.receive_state === 'quiet')
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

// Geometry (SVG viewBox 1280 x 470).
// Full route, left to right: T-Deck, parallax, air, tesseract, T-Echo.
const G = {
  devL: { x: 140, y: 225 }, kitL: { x: 405, y: 225 }, airL: { x: 490, y: 225 },
  airR: { x: 790, y: 225 }, kitR: { x: 875, y: 225 }, devR: { x: 1140, y: 225 },
  smsY: 335,
}
// Half route: the near device and kit large, the air leaving over an edge.
const HALF_LEFT = { dev: { x: 240, y: 240 }, kit: { x: 640, y: 240 }, airNear: { x: 790, y: 240 }, airFar: { x: 1340, y: 240 }, edge: 1280, smsY: 350, isl: { x: 56, w: 720 } }
const HALF_RIGHT = { dev: { x: 1040, y: 240 }, kit: { x: 640, y: 240 }, airNear: { x: 490, y: 240 }, airFar: { x: -60, y: 240 }, edge: 0, smsY: 350, isl: { x: 504, w: 720 } }
const P = computed(() => {
  if (layout.value === 'full') {
    return nearIsLeft.value
      ? { dev: G.devL, kit: G.kitL, airNear: G.airL, airFar: G.airR, smsY: G.smsY }
      : { dev: G.devR, kit: G.kitR, airNear: G.airR, airFar: G.airL, smsY: G.smsY }
  }
  return nearIsLeft.value ? HALF_LEFT : HALF_RIGHT
})
const kitPos = computed(() => P.value.kit)
const devPos = computed(() => P.value.dev)
const airNear = computed(() => P.value.airNear)
const airFar = computed(() => P.value.airFar)
const smsY = computed(() => P.value.smsY)

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
  replaying.value = false
  dot.dir = dir; dot.lane = 'aprs'; dot.visible = true
  flashNear()
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
      jump(airFar.value.x, smsY.value); moveTo(airNear.value.x, smsY.value, 1400)
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
    if (t.dir === 'out' && (ch.startsWith('aprs') || ch.startsWith('cellular')) && !t.laneCommitted) {
      t.lane = ch.startsWith('cellular') ? 'sms' : 'aprs'; dot.lane = t.lane
      stage(t, 'queued', { channel: ch }); t.msgRef = d.msg_ref
      moveTo(kitPos.value.x, t.lane === 'sms' ? smsY.value : kitPos.value.y, 450)
    } else if (t.dir === 'in' && ch.startsWith('mesh')) {
      stage(t, 'queued', { channel: ch }); t.msgRef = d.msg_ref
      moveTo(kitPos.value.x, kitPos.value.y, 500)
    }
  } else if (status === 'delivered' || status === 'sent') {
    if (t.dir === 'out' && (ch.startsWith('aprs') || ch.startsWith('cellular'))) {
      // The channel is the authority for the lane, not the earlier
      // `queued` event: a relayed mesh text fragments against the SMS
      // size and the dispatcher skips `queued` for fragmented sends,
      // so the SMS lane was never lit. A relayed message also produces
      // several fragment deliveries; only the first commits the lane
      // and drives the dot. [MESHSAT-826]
      if (t.laneCommitted) return
      t.laneCommitted = true
      t.lane = ch.startsWith('cellular') ? 'sms' : 'aprs'; dot.lane = t.lane
      stage(t, 'sent', { channel: ch, latency: d.latency_ms })
      if (t.lane === 'sms') {
        moveTo(kitPos.value.x, smsY.value, 350)
        setTimeout(() => moveTo(airFar.value.x, smsY.value, 1500), 380)
        finish(t, false, 2000)
      } else {
        moveTo(airFar.value.x, airFar.value.y, 1600)
        finish(t, false, 1800)
      }
    } else if (t.dir === 'in' && ch.startsWith('mesh')) {
      if (t.outCommitted) return
      t.outCommitted = true
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

// ── the moment a message is heard: one flash on the near device ──────
const flash = ref(false)
let flashTimer = 0
function flashNear() {
  flash.value = false
  clearTimeout(flashTimer)
  requestAnimationFrame(() => { flash.value = true; flashTimer = setTimeout(() => { flash.value = false }, 1300) })
}

// ── plain-language status of the current trip ────────────────────────
const hhmmss = (ms) => new Date(ms).toISOString().slice(11, 19) + 'Z'
const statusLine = computed(() => {
  const t = current.value; if (!t) return ''
  const last = t.stages[t.stages.length - 1]
  const name = last ? last.name : ''
  const nearDev = nearDevName.value
  if (t.failed) return 'did not get out, the ledger has the reason'
  if (t.dir === 'out') {
    if (name === 'sent' || name === 'aprs_tx') {
      const via = t.lane === 'sms' ? 'as one SMS' : 'over the radio'
      return layout.value === 'half' ? `left this kit ${via} at ${hhmmss(last.at)}, the other screen shows it arriving` : `on its way ${via} to ${peer.value.name}`
    }
    if (name === 'queued') return 'inside the kit, picking a way out'
    if (name === 'test') return 'a test frame from this kit'
    return `heard on the mesh from the ${nearDev}`
  }
  if (name === 'sent' || name === 'lora_tx') return `on this mesh now, look at the ${nearDev}`
  if (name === 'queued') return 'inside the kit, going out on LoRa'
  return `came in ${t.lane === 'sms' ? 'as an SMS' : 'over the radio'} from ${peer.value.name}`
})
// Only packets this kit could decrypt count as "heard on this mesh".
// Frames from other meshes on the same frequency arrive as ENCRYPTED_RELAY
// with portnum 0; they are counted separately, because a visitor should
// see that the kit hears the other mesh and cannot read it.
const lastHeardLine = computed(() => {
  const rx = packets.value.filter(x => x.bearer === 'lora' && x.dir === 'rx')
  const p = rx.find(x => x.portnum > 0)
  const cut = now.value - 300000
  const foreign = rx.filter(x => !(x.portnum > 0) && new Date(x.time).getTime() >= cut).length
  const tail = foreign ? `, ${foreign} unreadable from other meshes in 5 min` : ''
  if (!p) return `LoRa 868 MHz, nothing readable heard yet${tail}`
  const age = Math.max(0, Math.round((now.value - new Date(p.time).getTime()) / 1000))
  const who = nodeName(p.from) || p.from
  return `LoRa 868 MHz, last heard ${who} ${age < 60 ? age + ' s' : Math.round(age / 60) + ' min'} ago${tail}`
})
const insideMs = computed(() => {
  const t = current.value; if (!t || t.stages.length < 2) return null
  const first = t.stages[0].at, sent = t.stages.find(st => st.name === 'sent')
  if (!sent) return null
  return Math.max(1, sent.at - first)
})
const msgSize = computed(() => {
  const n = (current.value && current.value.text ? current.value.text : '').length
  return n > 100 ? 'text-2xl' : n > 48 ? 'text-3xl' : 'text-4xl'
})

// ── replay of the last real message while idle ───────────────────────
// Honest attract motion: the last trip's path, replayed and labelled.
const replaying = ref(false)
let lastReplayAt = 0
function replayLast() {
  const t = trips.value.find(x => x.done && !x.failed)
  if (!t || replaying.value) return
  replaying.value = true
  dot.lane = t.lane; dot.visible = true
  const y = t.lane === 'sms' ? smsY.value : kitPos.value.y
  const pts = t.dir === 'out'
    ? [[devPos.value.x, devPos.value.y], [kitPos.value.x, kitPos.value.y], [airFar.value.x, y]]
    : [[airFar.value.x, y], [kitPos.value.x, kitPos.value.y], [devPos.value.x, devPos.value.y]]
  if (t.stages.some(st => st.name === 'test')) pts.shift()
  jump(pts[0][0], pts[0][1])
  let d = 60
  for (let i = 1; i < pts.length; i++) { const [x, yy] = pts[i]; setTimeout(() => moveTo(x, yy, i === 1 ? 900 : 1500), d); d += i === 1 ? 950 : 1550 }
  setTimeout(() => { if (replaying.value) { dot.visible = false; replaying.value = false } }, d + 900)
}
const replayAge = computed(() => {
  const t = trips.value.find(x => x.done && !x.failed)
  return t ? Math.max(1, Math.round((now.value - t.startedAt) / 60000)) : 0
})

// ── what is this? tap cards for the drawings ─────────────────────────
const card = ref(null)
const cards = computed(() => ({
  tdeck: {
    title: 'LilyGO T-Deck Plus',
    lead: 'The keyboard device on the table. Type here and the message goes out on the local LoRa mesh.',
    facts: [
      ['Radio', 'Semtech SX1262, LoRa at 868 MHz'],
      ['Brain', 'ESP32-S3, Meshtastic firmware'],
      ['Screen', '2.8 inch touch, 35-key keyboard, trackball'],
      ['Also', 'GPS, its own battery, no phone or internet needed'],
      ['On this mesh', nearDev.value === 'tdeck' ? (nearDeviceNode.value ? `seen as ${nodeName(nearDeviceNode.value)}` : 'nothing heard from it yet') : 'on the other kit\'s mesh'],
    ],
  },
  techo: {
    title: 'LilyGO T-Echo',
    lead: 'The e-paper device on the far side. Messages land here; its button sends a reply.',
    facts: [
      ['Radio', 'Semtech SX1262, LoRa at 868 MHz'],
      ['Brain', 'nRF52840, Meshtastic firmware'],
      ['Screen', '1.54 inch e-paper, readable with the power off'],
      ['Also', 'GPS, temperature and pressure sensor, canned replies'],
      ['On this mesh', nearDev.value === 'techo' ? (nearDeviceNode.value ? `seen as ${nodeName(nearDeviceNode.value)}` : 'nothing heard from it yet') : 'on the other kit\'s mesh'],
    ],
  },
  kit: {
    title: `MeshSat field kit ${me.value.name}`,
    lead: 'A hand-built prototype. It listens on the mesh and finds a way out for every message: radio, SMS, satellite.',
    facts: [
      ['Computer', 'Raspberry Pi 5, 8 GB, in an IP67 case with this touch panel'],
      ['Power', '50 Wh UPS on four 18650 cells, mains or 12 V'],
      ['Satellite', `${me.value.modem}, Iridium`],
      ['Radio', '2 m transceiver with a software modem for APRS'],
      ['Also', 'LTE modem for SMS, RTL-SDR watching the bands, ZigBee, GPS, Meshtastic radio'],
      ['Right now', `${chips.value.filter(c => c.state === 'ok').length} of 3 demo channels up, ${rateOf('lora', 'rx') + rateOf('aprs', 'rx')} packets heard in the last minute`],
    ],
  },
  air: {
    title: 'The air link',
    lead: 'How a message gets from this kit to the other one with no network in between.',
    facts: [
      ['Radio', 'APRS on 144.800 MHz, amateur radio packets at 1200 baud'],
      ['Format', 'AX.25 frames from a software modem in the kit'],
      ['Privacy', 'compressed, then AES-256-GCM, then base64; both kits share the key'],
      ['Size', 'a 26-byte text becomes 76 bytes on the air'],
      ['Fallback', 'when the radio is silent the same message goes as one SMS over LTE'],
      ['Right now', aprsSilent.value ? 'the receiver on this kit is silent, SMS carries replies' : `receiver ok, ${rateOf('aprs', 'rx')} in and ${rateOf('aprs', 'tx')} out in the last minute`],
    ],
  },
}))
function openCard(k) { card.value = k; touch() }

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
const CYCLE = [{ v: 'route', ms: 90000 }, { v: 'spectrum', ms: 25000 }, { v: 'nerds', ms: 20000 }]
const view = ref('route')       // route | spectrum | nerds (attract)
let lastTouch = Date.now()
let cycleIdx = 0
let cycleAt = 0
function touch() {
  lastTouch = Date.now()
  if (view.value !== 'route') { view.value = 'route'; cycleIdx = 0 }
}
function closeCard() { card.value = null }
function tickAttract() {
  const idle = Date.now() - lastTouch
  // Replay the last real message every 45 s once the screen has been
  // untouched for 90 s and nothing live is moving.
  if (view.value === 'route' && idle > 90000 && (!current.value || current.value.done) && Date.now() - lastReplayAt > 45000 && trips.value.length) {
    lastReplayAt = Date.now(); replayLast()
  }
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
    testNote.value = 'sent'
    setTimeout(() => { testNote.value = '' }, 4000)
  } catch (e) {
    const m = (e && e.message) || ''
    testNote.value = /fetch|network/i.test(m) ? 'the kit did not answer, try again' : (m || 'could not send')
    setTimeout(() => { testNote.value = '' }, 6000)
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
    <header class="relative flex items-center gap-4 px-5 h-14 shrink-0 border-b border-gray-800">
      <div class="flex items-center gap-2 shrink-0"
           @pointerdown="pressStart" @pointerup="pressEnd" @pointercancel="pressEnd" @pointerleave="pressEnd"
           title="Hold to leave TTC mode">
        <img src="/meshsat-mark.png" alt="" class="h-7 w-auto" draggable="false" />
        <span class="font-display font-semibold text-base tracking-wide">MeshSat</span>
      </div>
      <!-- which box is this: centred, the one word a visitor and the crew both use -->
      <div class="absolute left-1/2 -translate-x-1/2 flex items-baseline gap-2 pointer-events-none">
        <span class="font-display text-2xl text-gray-50 tracking-wide">{{ me.name }}</span>
        <span class="font-mono text-sm text-gray-500">{{ me.callsign }}</span>
      </div>
      <div class="ml-auto flex items-center gap-2">
        <span v-for="c in chips" :key="c.key"
          class="chip font-mono text-[11px] px-2 py-1 rounded border"
          :class="c.state === 'ok' ? 'border-emerald-500/40 text-emerald-300' : c.state === 'healing' ? 'border-amber-500/50 text-amber-300' : 'border-gray-700 text-gray-500'"
          :title="c.detail">
          <span class="inline-block w-1.5 h-1.5 rounded-full mr-1 align-middle"
            :class="c.state === 'ok' ? 'bg-emerald-400' : c.state === 'healing' ? 'bg-amber-400 animate-pulse' : 'bg-gray-600'" />{{ c.label }}
        </span>
        <PowerWidget :kit="me.name" compact />
        <span class="font-mono text-lg text-gray-200 tabular-nums ml-1">{{ clock }}</span>
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

          <!-- the one sentence a visitor needs -->
          <text x="640" y="40" text-anchor="middle" class="visitor-line">
            <template v-if="layout === 'half'">
              {{ nearDev === 'tdeck'
                ? `Pick up the T-Deck and send a message. It leaves this box over the radio and lands on ${peer.name}, the kit on the ${nearIsLeft ? 'right' : 'left'}, with no internet and no phone network in between.`
                : `Messages from ${peer.name}, the kit on the ${nearIsLeft ? 'right' : 'left'}, arrive over the radio and land on the T-Echo. Press its button to send one back.` }}
            </template>
            <template v-else>A message typed on the T-Deck leaves over the radio and lands on the other mesh. No internet, no phone network in between.</template>
          </text>

          <!-- ═══ HALF LAYOUT: this kit's half, the air leaving over the edge ═══ -->
          <template v-if="layout === 'half'">
            <g class="island near">
              <rect :x="P.isl.x" y="66" :width="P.isl.w" height="350" rx="30" />
              <text :x="P.isl.x + P.isl.w / 2" y="98" text-anchor="middle" class="island-label">mesh {{ me.mesh }}, {{ me.channel }}</text>
              <text :x="P.isl.x + P.isl.w / 2" y="118" text-anchor="middle" class="island-sub">{{ lastHeardLine }}</text>
            </g>
            <g class="lanes">
              <line :x1="nearIsLeft ? P.dev.x + (nearDev === 'tdeck' ? 60 : 44) : P.kit.x + 100" :y1="P.dev.y"
                    :x2="nearIsLeft ? P.kit.x - 100 : P.dev.x - (nearDev === 'tdeck' ? 60 : 44)" :y2="P.dev.y" class="lane lora near" />
              <g class="air tap" :class="{ silent: aprsSilent }" @click="openCard('air')">
                <rect :x="Math.min(P.airNear.x, P.edge) - 10" :y="P.dev.y - 120" :width="Math.abs(P.edge - P.airNear.x) + 20" height="260" class="hit" />
                <line :x1="P.airNear.x" :y1="P.dev.y" :x2="P.edge" :y2="P.dev.y" class="lane air-line" />
                <g v-for="i in 3" :key="'wn'+i" class="wave" :style="{ animationDelay: (i * 0.5) + 's' }">
                  <path :d="nearIsLeft
                    ? `M ${P.airNear.x + 6 + i*16} ${P.dev.y - 14 - i*10} A ${16 + i*10} ${16 + i*10} 0 0 1 ${P.airNear.x + 6 + i*16} ${P.dev.y + 14 + i*10}`
                    : `M ${P.airNear.x - 6 - i*16} ${P.dev.y - 14 - i*10} A ${16 + i*10} ${16 + i*10} 0 0 0 ${P.airNear.x - 6 - i*16} ${P.dev.y + 14 + i*10}`" />
                </g>
                <g v-for="i in 3" :key="'we'+i" class="wave" :style="{ animationDelay: (i * 0.5 + 0.25) + 's' }">
                  <path :d="nearIsLeft
                    ? `M ${P.edge - 40 - i*16} ${P.dev.y - 14 - i*10} A ${16 + i*10} ${16 + i*10} 0 0 1 ${P.edge - 40 - i*16} ${P.dev.y + 14 + i*10}`
                    : `M ${P.edge + 40 + i*16} ${P.dev.y - 14 - i*10} A ${16 + i*10} ${16 + i*10} 0 0 0 ${P.edge + 40 + i*16} ${P.dev.y + 14 + i*10}`" />
                </g>
                <text :x="(P.airNear.x + P.edge) / 2" :y="P.dev.y - 96" class="air-label" text-anchor="middle">APRS on 144.800 MHz</text>
                <text :x="(P.airNear.x + P.edge) / 2" :y="P.dev.y - 74" class="air-sub" text-anchor="middle">amateur radio packets, encrypted</text>
                <text :x="(P.airNear.x + P.edge) / 2" :y="P.dev.y - 54" class="air-sub" text-anchor="middle">{{ nearDev === 'tdeck' ? `to ${peer.name}, on the ${nearIsLeft ? 'right' : 'left'}` : `from ${peer.name}, on the ${nearIsLeft ? 'right' : 'left'}` }}</text>
                <text v-if="aprsSilent" :x="(P.airNear.x + P.edge) / 2" :y="P.dev.y + 46" class="air-warn" text-anchor="middle">this kit's receiver is silent, SMS carries replies</text>
                <text v-else-if="aprsQuiet" :x="(P.airNear.x + P.edge) / 2" :y="P.dev.y + 46" class="air-sub" text-anchor="middle">nothing heard on the radio for a few minutes</text>
                <text :x="(P.airNear.x + P.edge) / 2" :y="P.dev.y + 26" class="air-tap" text-anchor="middle">tap any drawing for details</text>
              </g>
              <g class="sms">
                <line :x1="P.airNear.x" :y1="P.smsY" :x2="P.edge" :y2="P.smsY" class="lane sms-line" />
                <text :x="(P.airNear.x + P.edge) / 2" :y="P.smsY + 26" class="sms-label" text-anchor="middle">SMS over LTE when the radio is silent</text>
              </g>
            </g>

            <!-- near device -->
            <g :transform="`translate(${P.dev.x},${P.dev.y + 14})`" class="station near tap" :class="{ flash }" @click="openCard(nearDev)">
              <rect x="-110" y="-110" width="220" height="250" class="hit" rx="16" />
              <TtcDeviceTDeck v-if="nearDev === 'tdeck'" :scale="1.5" />
              <TtcDeviceTEcho v-else :scale="1.5" />
              <text :y="nearDev === 'tdeck' ? 88 : 100" text-anchor="middle" class="st-name">{{ nearDev === 'tdeck' ? 'T-Deck Plus' : 'T-Echo' }}</text>
              <text :y="nearDev === 'tdeck' ? 110 : 122" text-anchor="middle" class="st-sub">{{ nearDev === 'tdeck' ? 'Meshtastic, keyboard' : 'Meshtastic, e-paper' }}</text>
            </g>

            <!-- near kit -->
            <g :transform="`translate(${P.kit.x},${P.kit.y})`" class="station kit near tap" :class="{ flash }" @click="openCard('kit')">
              <rect x="-110" y="-130" width="220" height="290" class="hit" rx="16" />
              <image href="/kit-v1.png" x="-97" y="-128" width="194" height="230" class="kit-img" />
              <text y="128" text-anchor="middle" class="st-name">{{ me.name }}</text>
              <text y="150" text-anchor="middle" class="st-sub">MeshSat kit, {{ me.callsign }}</text>
            </g>
          </template>

          <!-- ═══ FULL LAYOUT: the whole route on one panel, in table order ═══ -->
          <template v-else>
            <g :class="['island', leftKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far')]">
              <rect x="36" y="64" width="464" height="330" rx="28" />
              <text x="62" y="98" class="island-label">mesh {{ leftKit.mesh }}, {{ leftKit.channel }}</text>
              <text x="62" y="117" class="island-sub">LoRa 868 MHz, its own channel key</text>
            </g>
            <g :class="['island', rightKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far')]">
              <rect x="780" y="64" width="464" height="330" rx="28" />
              <text x="806" y="98" class="island-label">mesh {{ rightKit.mesh }}, {{ rightKit.channel }}</text>
              <text x="806" y="117" class="island-sub">LoRa 868 MHz, a different channel key</text>
            </g>
            <g class="lanes">
              <line :x1="G.devL.x + (leftKit.device === 'tdeck' ? 50 : 36)" :y1="G.devL.y" :x2="G.kitL.x - 84" :y2="G.kitL.y" class="lane lora" :class="leftKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far')" />
              <line :x1="G.kitR.x + 84" :y1="G.kitR.y" :x2="G.devR.x - (rightKit.device === 'tdeck' ? 50 : 36)" :y2="G.devR.y" class="lane lora" :class="rightKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far')" />
              <g class="air tap" :class="{ silent: aprsSilent }" @click="openCard('air')">
                <rect :x="G.airL.x" :y="G.airL.y - 110" :width="G.airR.x - G.airL.x" height="220" class="hit" />
                <line :x1="G.airL.x" :y1="G.airL.y" :x2="G.airR.x" :y2="G.airR.y" class="lane air-line" />
                <g v-for="i in 3" :key="'wl'+i" class="wave" :style="{ animationDelay: (i * 0.5) + 's' }">
                  <path :d="`M ${G.airL.x + 6 + i*14} ${G.airL.y - 12 - i*8} A ${14 + i*8} ${14 + i*8} 0 0 1 ${G.airL.x + 6 + i*14} ${G.airL.y + 12 + i*8}`" />
                </g>
                <g v-for="i in 3" :key="'wr'+i" class="wave" :style="{ animationDelay: (i * 0.5) + 's' }">
                  <path :d="`M ${G.airR.x - 6 - i*14} ${G.airR.y - 12 - i*8} A ${14 + i*8} ${14 + i*8} 0 0 0 ${G.airR.x - 6 - i*14} ${G.airR.y + 12 + i*8}`" />
                </g>
                <text :x="(G.airL.x + G.airR.x)/2" :y="G.airL.y - 92" class="air-label" text-anchor="middle">APRS on 144.800 MHz</text>
                <text :x="(G.airL.x + G.airR.x)/2" :y="G.airL.y - 72" class="air-sub" text-anchor="middle">amateur radio packets, encrypted</text>
                <text v-if="aprsSilent" :x="(G.airL.x + G.airR.x)/2" :y="G.airL.y + 44" class="air-warn" text-anchor="middle">this kit's receiver is silent, SMS carries replies</text>
              </g>
              <g class="sms">
                <line :x1="G.airL.x" :y1="G.smsY" :x2="G.airR.x" :y2="G.smsY" class="lane sms-line" />
                <text :x="(G.airL.x + G.airR.x)/2" :y="G.smsY + 24" class="sms-label" text-anchor="middle">SMS over LTE when the radio is silent</text>
              </g>
            </g>

            <!-- left slot: device + kit -->
            <g :transform="`translate(${G.devL.x},${G.devL.y})`" class="station tap" :class="[leftKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far'), { flash: flash && leftKit.name === me.name }]" @click="openCard(leftKit.device)">
              <rect x="-90" y="-80" width="180" height="180" class="hit" rx="14" />
              <TtcDeviceTDeck v-if="leftKit.device === 'tdeck'" />
              <TtcDeviceTEcho v-else />
              <text :y="leftKit.device === 'tdeck' ? 72 : 86" text-anchor="middle" class="st-name">{{ leftKit.device === 'tdeck' ? 'T-Deck Plus' : 'T-Echo' }}</text>
              <text :y="leftKit.device === 'tdeck' ? 90 : 104" text-anchor="middle" class="st-sub">{{ leftKit.device === 'tdeck' ? 'Meshtastic, keyboard' : 'Meshtastic, e-paper' }}</text>
            </g>
            <g :transform="`translate(${G.kitL.x},${G.kitL.y})`" class="station kit tap" :class="[leftKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far'), { flash: flash && leftKit.name === me.name }]" @click="openCard('kit')">
              <rect x="-90" y="-112" width="180" height="250" class="hit" rx="14" />
              <image href="/kit-v1.png" x="-82" y="-108" width="164" height="194" class="kit-img" />
              <text y="108" text-anchor="middle" class="st-name">{{ leftKit.name }}</text>
              <text y="126" text-anchor="middle" class="st-sub">MeshSat kit, {{ leftKit.callsign }}</text>
            </g>

            <!-- right slot: kit + device -->
            <g :transform="`translate(${G.kitR.x},${G.kitR.y})`" class="station kit tap" :class="[rightKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far'), { flash: flash && rightKit.name === me.name }]" @click="openCard('kit')">
              <rect x="-90" y="-112" width="180" height="250" class="hit" rx="14" />
              <image href="/kit-v1.png" x="-82" y="-108" width="164" height="194" class="kit-img" />
              <text y="108" text-anchor="middle" class="st-name">{{ rightKit.name }}</text>
              <text y="126" text-anchor="middle" class="st-sub">MeshSat kit, {{ rightKit.callsign }}</text>
            </g>
            <g :transform="`translate(${G.devR.x},${G.devR.y})`" class="station tap" :class="[rightKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far'), { flash: flash && rightKit.name === me.name }]" @click="openCard(rightKit.device)">
              <rect x="-90" y="-80" width="180" height="180" class="hit" rx="14" />
              <TtcDeviceTDeck v-if="rightKit.device === 'tdeck'" />
              <TtcDeviceTEcho v-else />
              <text :y="rightKit.device === 'tdeck' ? 72 : 86" text-anchor="middle" class="st-name">{{ rightKit.device === 'tdeck' ? 'T-Deck Plus' : 'T-Echo' }}</text>
              <text :y="rightKit.device === 'tdeck' ? 90 : 104" text-anchor="middle" class="st-sub">{{ rightKit.device === 'tdeck' ? 'Meshtastic, keyboard' : 'Meshtastic, e-paper' }}</text>
            </g>

            <text :x="nearIsLeft ? 1012 : 268" y="424" text-anchor="middle" class="far-note">
              {{ farAlive ? `${peer.name} heard over the air ${farAgeS} s ago` : `${peer.name} not heard yet on this kit` }}
            </text>
          </template>

          <!-- replay label -->
          <text v-if="replaying" x="640" y="452" text-anchor="middle" class="replay-note">replaying the last real message, {{ replayAge }} min ago</text>

          <!-- the message -->
          <g v-if="dot.visible" class="msg" :class="[dot.lane, current && current.failed ? 'failed' : '', replaying ? 'replay' : '']" :transform="`translate(${dot.x},${dot.y})`">
            <circle r="26" fill="url(#dotg)" opacity="0.55" />
            <circle r="8" class="core" filter="url(#glow)" />
          </g>
        </svg>
      </div>

      <!-- strip: the message, its status, the QR, the numbers -->
      <section class="grid grid-cols-12 gap-3 px-5 pb-4 shrink-0">
        <div class="col-span-2 flex items-center gap-3">
          <img src="/qr-meshsat.svg" alt="QR code for meshsat.net" class="qr w-[92px] h-[92px] shrink-0" draggable="false" />
          <div class="font-sans text-sm leading-snug text-gray-300">meshsat.net<br /><span class="text-gray-500">open source, GPLv3</span></div>
        </div>
        <div class="col-span-6 rounded-lg border border-gray-800 bg-gray-900/60 px-4 py-3 min-h-[104px] flex flex-col justify-center">
          <button type="button" class="text-left w-full font-display leading-tight text-gray-50 break-words" :class="current ? msgSize : 'text-2xl'" @click="toggleText" :title="showText ? 'Tap to hide message text' : 'Tap to show message text'">
            {{ current ? displayText(current) : (nearDev === 'tdeck' ? 'Your message will appear here the moment this kit hears it.' : 'The next message from the other kit will appear here the moment it lands.') }}
          </button>
          <div v-if="current" class="font-sans text-sm text-gray-300 mt-1">
            {{ statusLine }}
            <span class="text-gray-500"> · {{ current.bytes || (current.text || '').length }} bytes</span>
            <span v-if="current.rssi" class="text-gray-500"> · {{ current.rssi }} dBm</span>
            <span v-if="current.snr" class="text-gray-500"> · SNR {{ current.snr }} dB</span>
          </div>
        </div>
        <div class="col-span-4 rounded-lg border border-gray-800 bg-gray-900/60 px-4 py-3">
          <div class="grid grid-cols-2 gap-x-3 font-mono text-sm tabular-nums">
            <span class="text-gray-400">last minute</span><span></span>
            <span class="text-gray-300">LoRa</span><span class="text-gray-50">{{ rateOf('lora','rx') }} in · {{ rateOf('lora','tx') }} out</span>
            <span class="text-gray-300">APRS</span><span class="text-gray-50">{{ rateOf('aprs','rx') }} in · {{ rateOf('aprs','tx') }} out</span>
            <span class="text-gray-300">SMS</span><span class="text-gray-50">{{ rateOf('sms','rx') }} in · {{ rateOf('sms','tx') }} out</span>
          </div>
          <div v-if="insideMs !== null" class="font-mono text-xs text-gray-400 mt-1">in and out of this kit in <span class="text-teal-300">{{ insideMs }} ms</span></div>
          <div class="mt-2 flex items-center gap-2">
            <button type="button" @click="drawer = !drawer" class="font-mono text-sm px-3 py-2 rounded border border-gray-700 text-gray-300 hover:border-teal-500 hover:text-teal-300 whitespace-nowrap">stats for nerds</button>
            <button type="button" @click="sendTest" :disabled="testBusy" class="font-mono text-sm px-3 py-2 rounded border whitespace-nowrap" :class="testArmed ? 'border-teal-500 text-teal-300' : 'border-gray-700 text-gray-400 hover:text-gray-200'">
              {{ testArmed ? 'tap again to send' : 'test frame' }}
            </button>
            <span v-if="testNote" class="font-mono text-xs text-gray-400">{{ testNote }}</span>
          </div>
        </div>
      </section>
    </main>

    <!-- WHAT IS THIS: tap card -->
    <div v-if="card" class="absolute inset-0 z-10" @click.self="closeCard">
      <section class="absolute left-0 right-0 bottom-0 bg-gray-900 border-t border-gray-700 px-6 pt-5 pb-6 card-sheet" role="dialog" :aria-label="cards[card].title">
        <div class="flex items-start gap-6">
          <div class="flex-1 min-w-0">
            <h2 class="font-display text-2xl text-gray-50">{{ cards[card].title }}</h2>
            <p class="font-sans text-base text-gray-300 mt-1 max-w-[70ch]">{{ cards[card].lead }}</p>
            <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 mt-4 font-sans text-base">
              <template v-for="f in cards[card].facts" :key="f[0]">
                <dt class="text-gray-400">{{ f[0] }}</dt><dd class="text-gray-100">{{ f[1] }}</dd>
              </template>
            </dl>
          </div>
          <button type="button" @click="closeCard" class="font-mono text-sm px-4 py-2 rounded border border-gray-600 text-gray-200 hover:border-teal-500 hover:text-teal-300 shrink-0">close</button>
        </div>
      </section>
    </div>

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
.island .island-label { font-family: 'IBM Plex Mono', monospace; font-size: 18px; fill: #E4DAC6; letter-spacing: 0.04em; }
.island .island-sub { font-family: 'IBM Plex Sans', sans-serif; font-size: 14px; fill: #AE9C7A; }
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
.air-label { font-family: 'IBM Plex Mono', monospace; font-size: 22px; fill: #F7F7F4; letter-spacing: 0.02em; }
.air-sub { font-family: 'IBM Plex Sans', sans-serif; font-size: 14px; fill: #B4B4BD; }
.air-warn { font-family: 'IBM Plex Sans', sans-serif; font-size: 14px; fill: #FCD34D; }
.air-tap { font-family: 'IBM Plex Sans', sans-serif; font-size: 14px; fill: #6A6A78; }
.visitor-line { font-family: 'IBM Plex Sans', sans-serif; font-size: 17px; fill: #D6D6DC; }
.replay-note { font-family: 'IBM Plex Sans', sans-serif; font-size: 14px; fill: #8A8A96; }
.sms-line { stroke: #E0B458; stroke-width: 1.5; stroke-dasharray: 10 8; opacity: 0.55; }
.sms-label { font-family: 'IBM Plex Sans', sans-serif; font-size: 14px; fill: #AE9C7A; }
.station :deep(.device .body) { fill: #0E0E14; stroke: #C8B89A; stroke-width: 1.4; }
.station :deep(.device.photo .body) { fill: none; stroke: none; }
.station :deep(.device.photo .photo-img) { filter: drop-shadow(0 0 3px rgba(200, 184, 154, 0.35)); }
.station :deep(.device.photo .epaper) { fill: #DCD6C8; }
.station :deep(.device .bezel) { fill: #08080B; stroke: #7A6B50; stroke-width: 0.8; }
.station :deep(.device .screen) { fill: #101018; stroke: none; }
.station :deep(.device .ui .bubble) { fill: #C8B89A; opacity: 0.55; }
.station :deep(.device .ui .bubble.far) { fill: #F96118; opacity: 0.7; }
.station :deep(.device .trackball) { fill: #1B1B22; stroke: #C8B89A; stroke-width: 1; }
.station :deep(.device .trackball-in) { fill: #C8B89A; }
.station :deep(.device .keys rect) { fill: #1B1B22; stroke: #7A6B50; stroke-width: 0.6; }
.station :deep(.device .sma) { fill: #24242C; stroke: #C8B89A; stroke-width: 1; }
.station :deep(.device .ant) { stroke: #C8B89A; stroke-width: 2.2; stroke-linecap: round; }
.station :deep(.device .epaper) { fill: #E4DAC6; }
.station :deep(.device .ink rect) { fill: #24242C; }
.station :deep(.device .btn) { fill: #15151B; stroke: #C8B89A; stroke-width: 1; }
.station :deep(.device .btn-in) { fill: #C8B89A; }
.station :deep(.device .sidebtn) { fill: #24242C; stroke: #7A6B50; stroke-width: 0.6; }
.station .kit-img { filter: drop-shadow(0 0 8px rgba(200, 184, 154, 0.10)); }
.station.near .kit-img { filter: drop-shadow(0 0 10px rgba(200, 184, 154, 0.18)); }
.station.far { opacity: 0.35; }
.station.far-alive { opacity: 0.85; }
.st-name { font-family: 'IBM Plex Mono', monospace; font-size: 20px; fill: #F7F7F4; }
.st-sub { font-family: 'IBM Plex Sans', sans-serif; font-size: 14px; fill: #8A8A96; }
.far-note { font-family: 'IBM Plex Sans', sans-serif; font-size: 14px; fill: #8A8A96; }
/* Tap targets: an invisible hit area over each drawing, a pointer cursor
   on the laptop, and a one-second flash when the kit hears a message. */
.tap { cursor: pointer; }
.tap .hit { fill: transparent; stroke: none; }
.station.flash :deep(.device .body), .station.flash :deep(.device .bezel) { stroke: #F96118; animation: flashstroke 1.2s ease-out forwards; }
.station.flash :deep(.device.photo .body) { stroke: none; animation: none; }
.station.flash :deep(.device.photo .photo-img) { animation: flashglow 1.2s ease-out forwards; }
.station.flash .kit-img { filter: drop-shadow(0 0 18px rgba(249, 97, 24, 0.55)); animation: flashglow 1.2s ease-out forwards; }
@keyframes flashstroke { 0% { stroke: #F96118; } 100% { stroke: #C8B89A; } }
@keyframes flashglow { 0% { filter: drop-shadow(0 0 18px rgba(249, 97, 24, 0.55)); } 100% { filter: drop-shadow(0 0 10px rgba(200, 184, 154, 0.18)); } }
.msg.replay .core { fill: #C8B89A; }
.msg.replay circle:first-child { opacity: 0.25; }
.qr { image-rendering: pixelated; }
.card-sheet { box-shadow: 0 -12px 40px rgba(0, 0, 0, 0.6); }
.msg .core { fill: #F96118; }
.msg.sms .core { fill: #E0B458; }
.msg.failed .core { fill: #F0655A; }
.chip { transition: color 0.3s, border-color 0.3s; }
.route-wrap { padding: 0 12px; }
@media (prefers-reduced-motion: reduce) {
  .wave path { animation: none; opacity: 0.25; }
  .station.flash :deep(.device .body), .station.flash :deep(.device .bezel), .station.flash :deep(.device.photo .photo-img), .station.flash .kit-img { animation: none; }
}
</style>
