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
import TtcDeviceTDeckPro from '@/components/TtcDeviceTDeckPro.vue'
import PowerWidget from '@/components/PowerWidget.vue'

const router = useRouter()
const route = useRoute()
const store = useMeshsatStore()

// ── identity ─────────────────────────────────────────────────────────
// Which kit is this panel? From the APRS callsign, overridable with ?kit=.
// Booth placement (owner, 6 Sep 2026): tesseract on the LEFT of parallax.
// The T-Deck Plus sits with parallax, the e-paper T-Deck Pro with
// tesseract (owner, 11 Sep 2026; the T-Echo is kept as a device type),
// so the story runs right to left across the table and the screens
// follow the table. `side` is where the box stands, `device` is what is
// paired with it, `mesh` the island letter.
const KITS = {
  parallax: { name: 'parallax', callsign: 'MSPRLX-10', side: 'right', device: 'tdeck', mesh: 'B', channel: 'msat-ttc-02', modem: 'RockBLOCK 9704', peer: 'tesseract' },
  tesseract: { name: 'tesseract', callsign: 'MSTSRT-10', side: 'left', device: 'tdeckpro', mesh: 'A', channel: 'msat-ttc-01', modem: 'RockBLOCK 9704', peer: 'parallax' },
}
const LEFT_KIT = 'tesseract'
// Everything the drawing needs per handheld: the photo component, the
// half-width of the photo where the LoRa lane ends (near at 1.85, full at
// 1.35), where the caption sits under it, and whether a visitor types on
// it or only reads it.
const DEVICES = {
  tdeck:    { name: 'T-Deck', title: 'T-Deck Plus', sub: 'Meshtastic, keyboard', img: '/tdeck-plus.png', comp: TtcDeviceTDeck, edgeNear: 78, edgeFull: 50, nameNear: 120, nameFull: 72, keyboard: true },
  tdeckpro: { name: 'T-Deck Pro', title: 'T-Deck Pro', sub: 'Meshtastic, keyboard, e-paper', img: '/tdeck-pro.png', comp: TtcDeviceTDeckPro, edgeNear: 72, edgeFull: 48, nameNear: 120, nameFull: 72, keyboard: true },
  techo:    { name: 'T-Echo', title: 'T-Echo', sub: 'Meshtastic, e-paper', img: '/techo.png', comp: TtcDeviceTEcho, edgeNear: 56, edgeFull: 36, nameNear: 136, nameFull: 86, keyboard: false },
}
const DEVICE_NAME = Object.fromEntries(Object.entries(DEVICES).map(([k, d]) => [k, d.name]))
const kitName = ref(route.query.kit === 'tesseract' ? 'tesseract' : route.query.kit === 'parallax' ? 'parallax' : '')
const me = computed(() => KITS[kitName.value] || KITS.parallax)
const peer = computed(() => KITS[me.value.peer])
// Geometry follows placement; the drawing follows the paired device.
const nearIsLeft = computed(() => me.value.side === 'left')
const nearDev = computed(() => me.value.device)
// The one thing a visitor needs, as two lines that fit the 1280-unit
// drawing at 22 units (the panel shows that as 14 CSS px).
const visitorLines = computed(() => {
  const side = nearIsLeft.value ? 'right' : 'left'
  if (layout.value !== 'half') return ['A message typed on a T-Deck leaves over the radio and lands on the other mesh.', 'No internet, no phone network in between.']
  return DEVICES[nearDev.value].keyboard
    ? [`Pick up the ${nearDevName.value} and send a message.`, `It leaves over the radio and lands on ${peer.value.name}, the kit on the ${side}. No internet, no phone network.`]
    : [`Messages from ${peer.value.name}, the kit on the ${side}, arrive over the radio and land on the ${nearDevName.value}.`, 'Press its button to send one back.']
})
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
  { key: 'mesh', label: 'LoRa mesh', short: 'LoRa', ...chip('mesh') },
  { key: 'aprs', label: 'APRS 144.800', short: 'APRS', ...chip('aprs') },
  { key: 'cellular', label: 'SMS', short: 'SMS', ...chip('cellular') },
  { key: 'sat', label: 'Satellite', short: 'SAT', state: flow.value.imt && flow.value.imt.connected ? 'ok' : flow.value.imt && flow.value.imt.running ? 'healing' : 'unknown', detail: 'Iridium IMT' },
]))
const aprsSilent = computed(() => aprs.value.receive_state === 'deaf')
const aprsQuiet = computed(() => aprs.value.receive_state === 'quiet')
const farAgeS = computed(() => farHeardAt.value ? Math.round((now.value - farHeardAt.value) / 1000) : null)
const farAlive = computed(() => farAgeS.value !== null && farAgeS.value < 600)
const now = ref(Date.now())

// Node names for the end devices, from this kit's own node table.
// The island subtitle wraps onto a second line past 44 characters (the
// panel shows 21 SVG units as 14 CSS px, a 720-unit island holds ~44).
const lastHeardLines = computed(() => {
  const parts = String(lastHeardLine.value || '').split(', ')
  const lines = []
  for (const part of parts) {
    const cur = lines[lines.length - 1]
    if (cur !== undefined && (cur + ', ' + part).length <= 44) lines[lines.length - 1] = cur + ', ' + part
    else lines.push(part)
  }
  return lines.slice(0, 2)
})
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
const dot = reactive({ x: 0, y: 0, visible: false, lane: 'aprs', dir: 'out', waiting: false })
let tween = null
let raf = 0

// Geometry (SVG viewBox 1280 x 470).
// Full route, left to right: T-Deck Pro, tesseract, air, parallax, T-Deck Plus.
const G = {
  devL: { x: 150, y: 240 }, kitL: { x: 392, y: 240 }, airL: { x: 490, y: 240 },
  airR: { x: 790, y: 240 }, kitR: { x: 888, y: 240 }, devR: { x: 1130, y: 240 },
  satY: 166, aprsY: 240, hubY: 314, smsY: 386,
}
// Half route: the near device and kit large, the air leaving over an edge.
const HALF_LEFT = { dev: { x: 236, y: 300 }, kit: { x: 640, y: 300 }, airNear: { x: 790, y: 300 }, airFar: { x: 1340, y: 300 }, edge: 1280, satY: 132, aprsY: 226, hubY: 320, smsY: 414, isl: { x: 44, w: 736 } }
const HALF_RIGHT = { dev: { x: 1044, y: 300 }, kit: { x: 640, y: 300 }, airNear: { x: 490, y: 300 }, airFar: { x: -60, y: 300 }, edge: 0, satY: 132, aprsY: 226, hubY: 320, smsY: 414, isl: { x: 500, w: 736 } }
const P = computed(() => {
  if (layout.value === 'full') {
    return nearIsLeft.value
      ? { dev: G.devL, kit: G.kitL, airNear: G.airL, airFar: G.airR, satY: G.satY, aprsY: G.aprsY, hubY: G.hubY, smsY: G.smsY, edge: G.airR.x }
      : { dev: G.devR, kit: G.kitR, airNear: G.airR, airFar: G.airL, satY: G.satY, aprsY: G.aprsY, hubY: G.hubY, smsY: G.smsY, edge: G.airL.x }
  }
  return nearIsLeft.value ? HALF_LEFT : HALF_RIGHT
})
const kitPos = computed(() => P.value.kit)
const devPos = computed(() => P.value.dev)
const airNear = computed(() => P.value.airNear)
const airFar = computed(() => P.value.airFar)
const smsY = computed(() => P.value.smsY)
const hubY = computed(() => P.value.hubY)
const aprsY = computed(() => P.value.aprsY)
// Lane y by lane name; the message dot and the inbound/outbound animations
// use it so a message always rides the lane it actually took.
const satY = computed(() => P.value.satY)
const laneY = (lane) => lane === 'sms' ? smsY.value : lane === 'hub' ? hubY.value : lane === 'sat' ? satY.value : aprsY.value
// The satellite glyph sits where the Hub cloud sits on its own lane.
const satX = computed(() => layout.value === 'full' ? 524 : rowIconX.value)
// The Hub cloud sits on the far side of the Hub lane so the lane label
// stays readable in the middle.
const cloudX = computed(() => layout.value === 'full' ? 756 : rowIconX.value)
// Lane text is centred a little away from the near end, where the cloud
// and the radio waves live.
const laneTextX = computed(() => (airNear.value.x + P.value.edge) / 2 + (nearIsLeft.value ? 44 : -44))
// The lanes leave from the kit's edge, not from a point floating beside it.
const laneStartX = computed(() => kitPos.value.x + (nearIsLeft.value ? 124 : -124))
// Route rows (MESHSAT-987): the icon sits on the line at the inner end, the
// name and its fact ride just above it, the state word is pinned to the outer
// end, and the detail sits under the chosen row. `rowDir` is +1 when this
// kit's lanes leave to the right and -1 when they leave to the left, so the
// whole row mirrors with the diptych and both kits keep one alignment edge.
const rowDir = computed(() => (nearIsLeft.value ? 1 : -1))
const rowIconX = computed(() => laneStartX.value + rowDir.value * 66)
const rowTextX = computed(() => rowIconX.value + rowDir.value * 34)
const rowStateX = computed(() => P.value.edge - rowDir.value * 26)
const rowAnchor = computed(() => (nearIsLeft.value ? 'start' : 'end'))
const rowStateAnchor = computed(() => (nearIsLeft.value ? 'end' : 'start'))
const rowCaptionX = computed(() => (laneStartX.value + P.value.edge) / 2)

// ── Booth flow selector (MESHSAT-962) ──
// The three paths are drawn together; the chosen one is lit, the other two
// are dimmed, and tapping a lane selects it for THIS kit's outbound
// messages (inbound is always open on both kits). A second tap on the
// chosen lane opens its card. Inbound and outbound messages animate on the
// lane they actually took, whatever is selected.
const flow = ref({ path: '', rules: {}, hub_number: '', peer_number: '', ready: false, issues: [] })
const flowBusy = ref(false)
// The satellite leg exists on the panel before the modem is fitted: the
// lane reads "modem not answering" until the 9704 gateway is up.
const satNoModem = computed(() => !(flow.value.imt && flow.value.imt.connected))
// One row per route, riding its own lane line: an icon, the name, the fact
// that identifies the bearer, and a state word only when that bearer is
// degraded. The sentence belongs to the chosen route alone — four captions
// at once read as a wall of text on a 7-inch panel. [MESHSAT-987]
const lanes = computed(() => ([
  { key: 'imt', lane: 'sat', card: 'sat', name: 'Satellite', fact: 'Iridium',
    state: satNoModem.value ? 'no modem' : '',
    detail: satNoModem.value
      ? 'The modem is not answering yet.'
      : 'Up to space, then down to the other kit.' },
  { key: 'aprs', lane: 'aprs', card: 'air', name: 'APRS radio', fact: '144.800 MHz',
    state: aprsSilent.value ? 'silent' : '',
    detail: aprsSilent.value
      ? 'This kit hears nothing right now.'
      : 'Radio, straight to the other kit.' },
  { key: 'hub_sms', lane: 'hub', card: 'hub', name: 'SMS via the Hub', fact: '',
    state: '', detail: 'The Hub passes it on by SMS.' },
  { key: 'b2b_sms', lane: 'sms', card: 'sms', name: 'SMS kit to kit', fact: '',
    state: '', detail: 'One text to the other kit\'s SIM.' },
]))
const laneCard = (lane) => (lanes.value.find(l => l.lane === lane) || {}).card || 'air'
async function selectPath(key) {
  touch()
  const ln = lanes.value.find(l => l.key === key)
  if (!ln) return
  if (flow.value.path === key) { openCard(ln.card); return }
  if (flowBusy.value) return
  const before = flow.value.path
  flow.value.path = key
  flowBusy.value = true
  try {
    const r = await api.put('/ttc/flow', { path: key })
    if (r && r.path) flow.value = r
  } catch (e) {
    flow.value.path = before
  } finally {
    flowBusy.value = false
  }
}
// Phone numbers as the modem reports them may carry or drop the country
// code; compare the last nine digits.
const numEq = (a, b) => {
  const x = String(a || '').replace(/\D/g, ''), y = String(b || '').replace(/\D/g, '')
  return x.length >= 6 && y.length >= 6 && x.slice(-9) === y.slice(-9)
}
const isHubNumber = (n) => numEq(n, flow.value.hub_number)
// Which lane an SMS rides: the Hub's number means the Hub path, anything
// else is kit to kit.
const smsLane = (number) => isHubNumber(number) ? 'hub' : 'sms'

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
    if (l.sent !== undefined) out.push({ k: t.lane === 'aprs' ? 'On the air (APRS)' : t.lane === 'hub' ? 'SMS to the Hub accepted by KPN' : t.lane === 'sat' ? 'Accepted by the satellite (MO)' : 'SMS accepted by KPN', v: l.sent })
  } else {
    if (l.queued !== undefined) out.push({ k: t.lane === 'sat' ? 'Satellite in to relay rule' : 'Air in to relay rule', v: l.queued })
    if (l.sent !== undefined) out.push({ k: 'Handed to LoRa', v: l.sent })
  }
  return out
})

// Event wiring. `out` = leaves the near device, crosses the air.
// `in` = arrives from the air, ends at the near device.
// Housekeeping between the kits: routing frames to RTICUL, OOB "MS:" frames,
// anything without text, and the kits' own status beacons (APRS status
// frames start with '>'): they pulse the air link and are never a message.
const isHousekeeping = (p) => {
  const text = (p.text || '').trim()
  return !text || (p.to || '').toUpperCase() === 'RTICUL' || /^MS:/.test(text) || text.startsWith('>')
}
const airPulse = ref(false)
let airPulseTimer = null
function pulseAir() {
  airPulse.value = true
  if (airPulseTimer) clearTimeout(airPulseTimer)
  airPulseTimer = setTimeout(() => { airPulse.value = false }, 1400)
}
// One inbound trip at a time: a text from the far kit that arrives while
// an inbound trip is still travelling is dropped here. The repeated APRS
// copy of every message (tx_repeat 2, 4 s apart) and the bridge's own
// "inbound" event for the same frame both land within seconds of the
// first, so an identical text from the same sender inside 15 s is the same
// message. [MESHSAT-1000]
let lastInbound = { from: '', text: '', at: 0 }
function startInboundTrip(p) {
  const key = { from: (p.from || '').toUpperCase(), text: (p.text || '').trim(), at: Date.now() }
  if (key.text && key.text === lastInbound.text && key.from === lastInbound.from && key.at - lastInbound.at < 15000) return
  if (current.value && !current.value.done && current.value.dir === 'in') return
  lastInbound = key
  if (p.bearer === 'sat') {
    const t = newTrip('in', p); t.lane = 'sat'; dot.lane = 'sat'
    stage(t, 'sat_rx')
    jump(airFar.value.x, satY.value)
    moveTo(satX.value, satY.value, 900)
    setTimeout(() => moveTo(airNear.value.x, satY.value, 700), 950)
    return
  }
  if (p.bearer === 'sms') {
    const lane = smsLane(p.from)
    const t = newTrip('in', p); t.lane = lane; dot.lane = lane
    stage(t, 'sms_rx')
    jump(airFar.value.x, laneY(lane))
    if (lane === 'hub') {
      moveTo(cloudX.value, hubY.value, 700)
      setTimeout(() => moveTo(airNear.value.x, hubY.value, 700), 750)
    } else {
      moveTo(airNear.value.x, laneY(lane), 1400)
    }
    return
  }
  const t = newTrip('in', p)
  stage(t, 'aprs_rx')
  jump(airFar.value.x, aprsY.value)
  moveTo(airNear.value.x, aprsY.value, 1400)
}
// The bridge's "inbound" event: a gateway text after the ingress transforms
// ran. On an encrypted bearer the packet feed shows the frame as it crossed
// the air, ciphertext with no text, so the packet path above treats it as
// housekeeping and only pulses the link; this event is where the message
// itself arrives. Found 10 Sep 2026: a T-Deck text reached the T-Echo but
// tesseract's screen never drew it. [MESHSAT-1000]
const inboundBearer = (source) => source === 'aprs' ? 'aprs'
  : (source === 'cellular' || source === 'sms') ? 'sms'
  : (source || '').startsWith('iridium') ? 'sat' : ''
function onInbound(d) {
  if (!d || typeof d !== 'object' || !d.text) return
  const bearer = inboundBearer(d.source)
  if (!bearer) return
  const p = { bearer, dir: 'rx', from: d.from || '', text: d.text, bytes: d.bytes || d.text.length, portnum: 1, time: new Date().toISOString() }
  if (isHousekeeping(p)) return
  if (bearer === 'aprs' && p.from && peer.value.callsign && p.from.toUpperCase().startsWith(peer.value.callsign.split('-')[0])) {
    farHeardAt.value = Date.now()
  }
  startInboundTrip(p)
}
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
    // Routing and management frames between the kits (Reticulum to
    // RTICUL, OOB "MS:" frames, anything without text) are not messages:
    // they pulse the air link so the visitor sees the link is alive, and
    // never touch the message box or start a trip.
    if (isHousekeeping(p)) { pulseAir(); return }
    // A text frame from the far kit starts an inbound trip.
    startInboundTrip(p)
  } else if (p.bearer === 'aprs' && p.dir === 'tx') {
    const t = current.value
    if (t && t.dir === 'out' && !t.done) { stage(t, 'aprs_tx', { bytes: p.bytes, raw: p.raw }); t.bytes = p.bytes || t.bytes }
  } else if (p.bearer === 'sat' && p.dir === 'rx') {
    if (isHousekeeping(p)) return
    startInboundTrip(p)
  } else if (p.bearer === 'sat' && p.dir === 'tx') {
    const t = current.value
    if (t && t.dir === 'out' && !t.done) { stage(t, 'sat_tx', { bytes: p.bytes, mo: p.path }); t.bytes = p.bytes || t.bytes }
  } else if (p.bearer === 'sms' && p.dir === 'rx') {
    if (isHousekeeping(p)) { pulseAir(); return }
    startInboundTrip(p)
  } else if (p.bearer === 'lora' && p.dir === 'tx') {
    const t = current.value
    if (t && t.dir === 'in' && !t.done) { stage(t, 'lora_tx', { bytes: p.bytes }) }
  }
}
function onDelivery(ev) {
  const d = ev.data || {}
  const ch = d.channel || ''
  let t = current.value
  // A delivery that names a trip we started (composer sends carry the
  // ledger's msg_ref) wins over whatever is current, so a routing frame in
  // between cannot swallow the journey.
  if (d.msg_ref) {
    const own = trips.value.find(x => x.msgRef === d.msg_ref && !x.done)
    if (own && own !== t) { t = own; current.value = own; dot.dir = own.dir; dot.lane = own.lane; dot.visible = true }
  }
  if (!t || t.done) return
  const status = (d.status || ev.type.replace('delivery_', '')).toLowerCase()
  const satCh = ch.startsWith('iridium')
  if (status === 'queued') {
    if (t.dir === 'out' && satCh && !t.laneCommitted) {
      // Satellite: the send blocks in the modem for a while; the dot climbs
      // to the satellite and waits there. A second text behind a first one
      // parks at the kit: the worker sends one at a time. [MESHSAT-962]
      t.lane = 'sat'; dot.lane = 'sat'; t.msgRef = d.msg_ref
      const busy = trips.value.some(x => x !== t && !x.done && x.dir === 'out' && x.lane === 'sat' && !x.laneCommitted && x.stages.some(st => st.name === 'queued'))
      if (busy) {
        stage(t, 'waiting', { channel: ch }); dot.waiting = true
        moveTo(kitPos.value.x, satY.value, 450)
      } else {
        stage(t, 'queued', { channel: ch }); dot.waiting = true
        moveTo(kitPos.value.x, satY.value, 450)
        setTimeout(() => { if (current.value === t) moveTo(satX.value, satY.value, 1600) }, 480)
      }
    } else if (t.dir === 'out' && (ch.startsWith('aprs') || ch.startsWith('cellular')) && !t.laneCommitted) {
      t.lane = ch.startsWith('cellular') ? smsLane(d.destination) : 'aprs'; dot.lane = t.lane
      stage(t, 'queued', { channel: ch }); t.msgRef = d.msg_ref
      moveTo(kitPos.value.x, laneY(t.lane), 450)
    } else if (t.dir === 'in' && ch.startsWith('mesh')) {
      stage(t, 'queued', { channel: ch }); t.msgRef = d.msg_ref
      moveTo(kitPos.value.x, kitPos.value.y, 500)
    }
  } else if (status === 'retry' && satCh && t.dir === 'out') {
    stage(t, 'retry', { channel: ch, error: d.error, next: d.next_retry })
    dot.waiting = true
  } else if (status === 'delivered' || status === 'sent') {
    if (t.dir === 'out' && satCh) {
      if (t.laneCommitted) return
      t.laneCommitted = true; t.lane = 'sat'; dot.lane = 'sat'; dot.waiting = false
      stage(t, 'sent', { channel: ch, latency: d.latency_ms })
      moveTo(satX.value, satY.value, 300)
      setTimeout(() => moveTo(airFar.value.x, satY.value, 1200), 330)
      finish(t, false, 1700)
    } else if (t.dir === 'out' && (ch.startsWith('aprs') || ch.startsWith('cellular'))) {
      // The channel is the authority for the lane, not the earlier
      // `queued` event: a relayed mesh text fragments against the SMS
      // size and the dispatcher skips `queued` for fragmented sends,
      // so the SMS lane was never lit. A relayed message also produces
      // several fragment deliveries; only the first commits the lane
      // and drives the dot. [MESHSAT-826]
      if (t.laneCommitted) return
      t.laneCommitted = true
      t.lane = ch.startsWith('cellular') ? smsLane(d.destination) : 'aprs'; dot.lane = t.lane
      stage(t, 'sent', { channel: ch, latency: d.latency_ms })
      if (t.lane === 'hub') {
        moveTo(kitPos.value.x, hubY.value, 350)
        setTimeout(() => moveTo(cloudX.value, hubY.value, 900), 380)
        setTimeout(() => moveTo(airFar.value.x, hubY.value, 900), 1330)
        finish(t, false, 2400)
      } else if (t.lane === 'sms') {
        moveTo(kitPos.value.x, smsY.value, 350)
        setTimeout(() => moveTo(airFar.value.x, smsY.value, 1500), 380)
        finish(t, false, 2000)
      } else {
        moveTo(kitPos.value.x, aprsY.value, 350)
        setTimeout(() => moveTo(airFar.value.x, aprsY.value, 1400), 380)
        finish(t, false, 1900)
      }
    } else if (t.dir === 'in' && ch.startsWith('mesh')) {
      if (t.outCommitted) return
      t.outCommitted = true
      stage(t, 'sent', { channel: ch, latency: d.latency_ms })
      moveTo(devPos.value.x, devPos.value.y, 900)
      finish(t, false, 1100)
    }
  } else if (status === 'dead' || status === 'failed') {
    if (ch.startsWith('aprs') || ch.startsWith('cellular') || ch.startsWith('mesh') || satCh) {
      dot.waiting = false
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
  if (ev.type === 'inbound') { try { onInbound(typeof ev.data === 'string' ? JSON.parse(ev.data) : ev.data) } catch {} ; return }
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
  if (replaying.value) return `replaying the last real message, ${replayAge.value} min ago`
  if (t.failed) return t.lane === 'sat' ? 'no satellite in view, it did not get out' : 'did not get out this time'
  if (t.dir === 'out') {
    if (t.lane === 'sat') {
      if (name === 'sent' || name === 'sat_tx') return `accepted by the satellite at ${hhmmss(last.at)}, the other screen shows it arriving`
      if (name === 'waiting') return 'waiting for the satellite slot, one message at a time'
      if (name === 'retry') return satNoModem.value ? 'satellite modem not answering, trying again' : `no satellite in view yet, trying again${last.next ? ' at ' + hhmmss(Date.parse(last.next)) : ''}`
      if (name === 'queued') return 'on its way up to the satellite, this can take a minute or two'
    }
    if (name === 'sent' || name === 'aprs_tx') {
      const via = t.lane === 'sms' ? 'as one SMS' : t.lane === 'hub' ? 'as one SMS to the Hub' : 'over the radio'
      return layout.value === 'half' ? `left this kit ${via} at ${hhmmss(last.at)}, the other screen shows it arriving` : `on its way ${via} to ${peer.value.name}`
    }
    if (name === 'queued') return 'inside the kit, picking a way out'
    if (name === 'test') return 'a test frame from this kit'
    if (name === 'typed_remote') return `typed on this kit, on its way to ${peer.value.name}`
    return `heard on the mesh from the ${nearDev}`
  }
  if (name === 'sent' || name === 'lora_tx') return `on this mesh now, look at the ${nearDev}`
  if (name === 'queued') return 'inside the kit, going out on LoRa'
  if (name === 'typed_local') return `typed on this kit, sent to the ${nearDev} over LoRa`
  if (t.lane === 'sat') return `came in from space, sent by ${peer.value.name}`
  return t.lane === 'hub' ? `came in as an SMS from the Hub, sent by ${peer.value.name}` : `came in ${t.lane === 'sms' ? 'as an SMS' : 'over the radio'} from ${peer.value.name}`
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
  const tail = foreign ? `, ${foreign} packet${foreign === 1 ? '' : 's'} from other meshes in 5 min, not ours` : ''
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
  return n > 44 ? 'text-xl' : n > 20 ? 'text-2xl' : 'text-3xl'
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
  const y = laneY(t.lane)
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
  tdeckpro: {
    title: 'LilyGO T-Deck Pro',
    lead: 'The e-paper keyboard device on the table. Type here and the message goes out on the local LoRa mesh.',
    facts: [
      ['Radio', 'Semtech SX1262, LoRa at 868 MHz'],
      ['Brain', 'ESP32-S3, Meshtastic firmware'],
      ['Screen', '3.1 inch e-paper touch, readable with the power off, physical keyboard'],
      ['Also', 'GPS, its own battery, no phone or internet needed'],
      ['On this mesh', nearDev.value === 'tdeckpro' ? (nearDeviceNode.value ? `seen as ${nodeName(nearDeviceNode.value)}` : 'nothing heard from it yet') : 'on the other kit\'s mesh'],
    ],
  },
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
  sat: {
    title: 'Satellite, Iridium',
    lead: 'The kit sends the text up to an Iridium satellite. The MeshSat Hub gets it from the ground station and sends it back up to the other kit.',
    facts: [
      ['Path', `this kit's 9704 to Cloudloop, the Hub, Cloudloop to ${peer.value.name}'s 9704`],
      ['Modem', 'RockBLOCK 9704, Iridium Messaging Transport'],
      ['Sky', 'needs a view of the sky: outside, or the antenna at a window'],
      ['Time', 'tens of seconds to a few minutes per hop'],
      ['Cost', 'paid per message, so one at a time'],
      ['Right now', !flow.value.imt || !flow.value.imt.running ? 'no satellite gateway on this kit' : !flow.value.imt.connected ? 'modem not answering' : `modem connected${flow.value.imt.last_mo_at ? ', last uplink ' + Math.max(0, Math.round((Date.now() - Date.parse(flow.value.imt.last_mo_at)) / 60000)) + ' min ago' : ''}${flow.value.imt.queued ? ', ' + flow.value.imt.queued + ' waiting' : ''}`],
    ],
  },
  hub: {
    title: 'SMS via the Hub',
    lead: 'The kit texts the MeshSat Hub; the Hub texts the other kit. No internet on either kit: two SMS carry the message.',
    facts: [
      ['Path', `this kit's SIM to the Hub at ${flow.value.hub_number || 'its number'}, then the Hub to ${peer.value.name}'s SIM`],
      ['Why', 'the Hub also talks to kits over satellite; indoors there is no sky, so SMS is the fallback'],
      ['Privacy', 'in the clear between kit and Hub for this demo; kit to kit SMS stays encrypted'],
      ['Size', 'one text message each hop, up to 160 characters'],
      ['Right now', flow.value.path === 'hub_sms' ? 'chosen for the next message from this kit' : 'tap the lane to choose it'],
    ],
  },
  sms: {
    title: 'SMS kit to kit',
    lead: 'One text message from this kit\'s SIM straight to the other kit\'s SIM over LTE.',
    facts: [
      ['Path', `this kit's SIM to ${peer.value.name}'s SIM at ${flow.value.peer_number || 'its number'}`],
      ['Network', 'the public mobile network, nothing else in between'],
      ['Privacy', 'compressed, then AES-256-GCM, then base64; both kits share the key'],
      ['Size', 'one text message, up to 160 characters'],
      ['Right now', flow.value.path === 'b2b_sms' ? 'chosen for the next message from this kit' : 'tap the lane to choose it'],
    ],
  },
  air: {
    title: 'APRS radio, kit to kit',
    lead: 'How a message gets from this kit to the other one with no network in between.',
    facts: [
      ['Radio', 'APRS on 144.800 MHz, amateur radio packets at 1200 baud'],
      ['Format', 'AX.25 frames from a software modem in the kit'],
      ['Privacy', 'compressed, then AES-256-GCM, then base64; both kits share the key'],
      ['Size', 'a 26-byte text becomes 76 bytes on the air'],
      ['Fallback', 'when the other kit\'s receiver is silent the same message goes as one SMS over LTE'],
      ['Right now', aprsSilent.value ? 'the receiver on this kit is silent, SMS carries replies' : `receiver ok, ${rateOf('aprs', 'rx')} in and ${rateOf('aprs', 'tx')} out in the last minute`],
    ],
  },
}))
function openCard(k) { card.value = k; touch() }

// ── polling ──────────────────────────────────────────────────────────
let sse = null
let timers = []
async function poll() {
  const [a, h, r, n, d, f] = await Promise.allSettled([
    api.get('/aprs/status'), api.get('/devices/health'), api.get('/packets/rates'),
    api.get('/nodes'), api.get('/deliveries?limit=20'), api.get('/ttc/flow'),
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
  if (f.status === 'fulfilled' && f.value && !flowBusy.value) flow.value = f.value
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
const sdrOk = computed(() => { const t = (health.value || []).find(x => x.name === 'rtl_sdr'); return !!t && t.state === 'ok' })
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
    // No spectrum page without a working SDR (a dead widget is worse than none).
    if (CYCLE[cycleIdx].v === 'spectrum' && !sdrOk.value) cycleIdx = (cycleIdx + 1) % CYCLE.length
    view.value = CYCLE[cycleIdx].v
    cycleAt = Date.now()
  }
}
const peerDevName = computed(() => DEVICE_NAME[peer.value.device])

// ── composer: double-tap the kit, type on the panel, send to either mesh ──
// The panel has no keyboard, so the composer draws one. Local mesh is a
// broadcast on this kit's own channel (the handheld on this table hears
// it); Remote mesh queues an SMS to the peer kit, whose relay rule puts it
// on the other mesh, the same path a relayed handheld text takes. A single
// tap on the kit still opens its card, after a 400 ms wait for a second tap.
const COMPOSER_MAX = 120
const KEY_ROWS = [
  ['1', '2', '3', '4', '5', '6', '7', '8', '9', '0'],
  ['q', 'w', 'e', 'r', 't', 'y', 'u', 'i', 'o', 'p'],
  ['a', 's', 'd', 'f', 'g', 'h', 'j', 'k', 'l', '?'],
  ['shift', 'z', 'x', 'c', 'v', 'b', 'n', 'm', '.', ',', 'back'],
]
const composer = ref({ open: false, to: 'local', text: '', shift: false, busy: false, note: '' })
let kitTapAt = 0, kitTapTimer = null
function kitTap() {
  const now = performance.now()
  if (now - kitTapAt < 400) {
    kitTapAt = 0
    if (kitTapTimer) { clearTimeout(kitTapTimer); kitTapTimer = null }
    openComposer(); return
  }
  kitTapAt = now
  kitTapTimer = setTimeout(() => { kitTapTimer = null; openCard('kit') }, 400)
}
function openComposer() {
  card.value = null; drawer.value = false
  composer.value = { open: true, to: 'local', text: '', shift: false, busy: false, note: '' }
}
function closeComposer() { composer.value.open = false }
const deviceImg = (d) => DEVICES[d].img
const composerOptions = computed(() => ([
  { key: 'local', title: 'Local mesh', sub: `to the ${nearDevName.value} on this table`, img: deviceImg(nearDev.value) },
  { key: 'remote', title: 'Remote mesh', sub: `to the ${peerDevName.value} at ${peer.value.name}, over the air`, img: deviceImg(peer.value.device) },
]))
const composerTarget = computed(() => composerOptions.value.find(o => o.key === composer.value.to) || composerOptions.value[0])
function keyTap(k) {
  const c = composer.value
  if (k === 'shift') { c.shift = !c.shift; return }
  if (k === 'back') { c.text = c.text.slice(0, -1); return }
  if (c.text.length >= COMPOSER_MAX) return
  if (k === 'space') { c.text += ' '; return }
  c.text += c.shift ? k.toUpperCase() : k
  c.shift = false
}
async function sendComposed() {
  const c = composer.value
  const text = c.text.trim()
  if (!text || c.busy) return
  const to = c.to
  c.busy = true; c.note = ''
  // The sheet covers the drawing, so it goes first: the journey is the
  // confirmation. The message box carries the text and its status line.
  closeComposer()
  try {
    if (to === 'local') {
      await api.post('/messages/send', { text })
      const t = newTrip('in', { text, from: me.value.callsign, bytes: text.length })
      stage(t, 'typed_local')
      jump(kitPos.value.x, kitPos.value.y)
      moveTo(devPos.value.x, devPos.value.y, 1400)
      finish(t, false, 1700)
    } else {
      // Follow the chosen path: radio, SMS to the Hub, or SMS to the peer.
      const path = flow.value.path
      const req = path === 'aprs' ? { text, gateway: 'aprs', precedence: 'Routine' }
        : path === 'imt' ? { text, gateway: 'iridium_imt', precedence: 'Routine' }
        : path === 'hub_sms' && flow.value.hub_number ? { text, gateway: 'cellular', to: flow.value.hub_number, precedence: 'Routine' }
        : { text, gateway: 'cellular', precedence: 'Routine' }
      const r = await api.post('/messages/send', req)
      // The ledger's delivery events move the dot from here on (SMS lane);
      // they find this trip by reference even if other frames arrive first.
      const t = newTrip('out', { text, from: me.value.callsign, bytes: text.length })
      t.msgRef = (r && r.msg_ref) || ''
      stage(t, 'typed_remote')
      jump(kitPos.value.x, kitPos.value.y)
    }
  } catch (e) {
    const m = (e && e.message) || ''
    composer.value = { open: true, to, text, shift: false, busy: false, note: /fetch|network/i.test(m) ? 'the kit did not answer, try again' : (m || 'could not send') }
    return
  }
  composer.value.busy = false
}

// Leaving TTC mode is an easter egg, not a control a visitor can find:
// three taps on the mark in the top left within 2.5 s, then one tap on
// the QR code in the bottom right within 4 s. Escape still works for a
// keyboard. (Owner request 7 Sep 2026; replaces the long-press.)
let hadNvis = false
const EGG_MARK_TAPS = 3, EGG_MARK_WINDOW_MS = 2500, EGG_QR_WINDOW_MS = 4000
let eggTaps = []          // timestamps of recent mark taps
let eggArmedAt = 0        // when the third mark tap landed
function markTap() {
  const now = performance.now()
  eggTaps = eggTaps.filter(t => now - t < EGG_MARK_WINDOW_MS)
  eggTaps.push(now)
  if (eggTaps.length >= EGG_MARK_TAPS) { eggArmedAt = now; eggTaps = [] }
}
function qrTap() {
  const now = performance.now()
  if (eggArmedAt && now - eggArmedAt < EGG_QR_WINDOW_MS) { eggArmedAt = 0; router.push('/') }
  else eggArmedAt = 0
}
function onKey(e) {
  if (composer.value.open) {
    if (e.key === 'Escape') { closeComposer(); return }
    if (e.key === 'Enter') { sendComposed(); return }
    if (e.key === 'Backspace') { keyTap('back'); return }
    if (e.key === ' ') { e.preventDefault(); keyTap('space'); return }
    if (e.key.length === 1 && composer.value.text.length < COMPOSER_MAX) { composer.value.text += e.key; return }
    return
  }
  if (e.key === 'Escape') router.push('/')
}
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

const displayText = (t) => !t ? '' : (showText.value ? (t.text || (t.dir === 'in' ? 'a frame between the kits, not a message' : '')) : 'text hidden')

onMounted(async () => {
  document.documentElement.classList.add('ttc-mode')
  window.addEventListener('keydown', onKey)
  window.addEventListener('pointerdown', touch, { passive: true })
  // The operator's NVIS night theme (body.theme-nvis) repaints every grey
  // as phosphor green; the booth screen shows the brand, so lift it while
  // TTC mode is up and put it back on exit.
  hadNvis = document.body.classList.contains('theme-nvis')
  document.body.classList.remove('theme-nvis')
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
  if (hadNvis) document.body.classList.add('theme-nvis')
  if (sse) sse.close()
  timers.forEach(clearInterval)
  if (raf) cancelAnimationFrame(raf)
})
</script>

<template>
  <div class="ttc fixed inset-0 z-[60] bg-gray-950 text-gray-50 flex flex-col select-none overflow-hidden">
    <!-- header: mark (first half of the exit egg), hostname, chips, clock -->
    <header class="relative flex items-center gap-3 px-4 h-12 shrink-0 border-b border-gray-800">
      <div class="flex items-center gap-2 shrink-0 select-none" @click="markTap">
        <img src="/meshsat-mark.png" alt="" class="h-7 w-auto" draggable="false" />
        <span class="font-display font-semibold text-base tracking-wide">MeshSat</span>
      </div>
      <!-- which box is this: centred, the one word a visitor and the crew both use -->
      <div class="absolute left-1/2 -translate-x-1/2 flex items-baseline gap-2 pointer-events-none">
        <span class="font-display text-xl lg:text-2xl text-gray-50 tracking-wide">{{ me.name }}</span>
        <span class="hidden lg:inline font-mono text-sm text-gray-500">{{ me.callsign }}</span>
      </div>
      <div class="ml-auto flex items-center gap-1.5">
        <span v-for="c in chips" :key="c.key"
          class="chip font-mono text-[11px] px-1.5 py-0.5 rounded border"
          :class="c.state === 'ok' ? 'border-emerald-500/40 text-emerald-300' : c.state === 'healing' ? 'border-amber-500/50 text-amber-300' : 'border-gray-700 text-gray-500'"
          :title="c.detail">
          <span class="inline-block w-1.5 h-1.5 rounded-full mr-1 align-middle"
            :class="c.state === 'ok' ? 'bg-emerald-400' : c.state === 'healing' ? 'bg-amber-400 animate-pulse' : 'bg-gray-600'" /><span class="lg:hidden">{{ c.short }}</span><span class="hidden lg:inline">{{ c.label }}</span>
        </span>
        <PowerWidget :kit="me.name" compact />
        <span class="font-mono text-sm lg:text-lg text-gray-200 tabular-nums ml-1">{{ clock }}</span>
      </div>
    </header>

    <!-- ROUTE VIEW -->
    <main v-show="view === 'route'" class="flex-1 flex flex-col min-h-0">
      <div class="route-wrap flex-1 min-h-0 flex items-center">
        <svg class="w-full h-full" :class="layout" :viewBox="layout === 'half' ? '0 0 1280 546' : '0 0 1280 462'" :preserveAspectRatio="layout === 'half' ? (nearIsLeft ? 'xMaxYMid meet' : 'xMinYMid meet') : 'xMidYMid meet'" aria-label="Message route">
          <defs>
            <filter id="glow" x="-100%" y="-100%" width="300%" height="300%">
              <feGaussianBlur stdDeviation="6" result="b" /><feMerge><feMergeNode in="b" /><feMergeNode in="SourceGraphic" /></feMerge>
            </filter>
            <radialGradient id="dotg"><stop offset="0" stop-color="#FFE2D1" /><stop offset="0.45" stop-color="#F96118" /><stop offset="1" stop-color="#F96118" stop-opacity="0" /></radialGradient>
          </defs>

          <!-- the one sentence a visitor needs -->
          <text v-for="(l, i) in visitorLines" :key="i" x="640" :y="26 + i * 25" text-anchor="middle" class="visitor-line">{{ l }}</text>

          <!-- ═══ HALF LAYOUT: this kit's half, the air leaving over the edge ═══ -->
          <template v-if="layout === 'half'">
            <g class="island near">
              <rect :x="P.isl.x" y="66" :width="P.isl.w" height="454" rx="34" />
              <text :x="P.isl.x + P.isl.w / 2" y="102" text-anchor="middle" class="island-label">mesh {{ me.mesh }}, {{ me.channel }}</text>
              <text v-for="(l, i) in lastHeardLines" :key="i" :x="P.isl.x + P.isl.w / 2" :y="130 + i * 24" text-anchor="middle" class="island-sub">{{ l }}</text>
            </g>
            <g class="lanes">
              <line :x1="nearIsLeft ? P.dev.x + DEVICES[nearDev].edgeNear : P.kit.x + 124" :y1="P.dev.y"
                    :x2="nearIsLeft ? P.kit.x - 124 : P.dev.x - DEVICES[nearDev].edgeNear" :y2="P.dev.y" class="lane lora near" />
              <g v-for="ln in lanes" :key="ln.lane" class="path tap" :class="[ln.lane, { selected: flow.path === ln.key, dim: flow.path && flow.path !== ln.key, silent: ln.lane === 'aprs' && aprsSilent, nosky: ln.lane === 'sat' && satNoModem }]" @click="selectPath(ln.key)">
                <rect :x="Math.min(laneStartX, P.edge)" :y="laneY(ln.lane) - 40" :width="Math.abs(P.edge - laneStartX)" height="80" class="hit" />
                <line :x1="laneStartX" :y1="laneY(ln.lane)" :x2="P.edge" :y2="laneY(ln.lane)" class="lane path-line" />
                <circle :cx="laneStartX" :cy="laneY(ln.lane)" r="7" class="path-start" />
                <!-- one icon family, one size, one place on every lane -->
                <g class="ic" :class="[ln.lane, { pulse: airPulse && ln.lane === 'aprs',
                     beaming: ln.lane === 'sat' && dot.visible && dot.lane === 'sat' }]"
                   :transform="`translate(${rowIconX},${laneY(ln.lane)}) scale(1.25)`">
                  <template v-if="ln.lane === 'aprs'">
                    <line x1="0" y1="-13" x2="0" y2="9" />
                    <circle cx="0" cy="12" r="2.4" class="solid" />
                    <path class="arc" d="M -7 -11 A 12 12 0 0 0 -7 7" />
                    <path class="arc" d="M 7 -11 A 12 12 0 0 1 7 7" />
                    <path class="arc wide" d="M -13 -16 A 19 19 0 0 0 -13 12" />
                    <path class="arc wide" d="M 13 -16 A 19 19 0 0 1 13 12" />
                  </template>
                  <template v-else-if="ln.lane === 'sat'">
                    <rect class="fillable" x="-7" y="-6" width="14" height="12" rx="2" />
                    <rect class="fillable" x="-21" y="-3.5" width="11" height="7" rx="1" />
                    <rect class="fillable" x="10" y="-3.5" width="11" height="7" rx="1" />
                    <path d="M -3 -6 q 3 -8 6 0" />
                    <line class="beam" x1="-3" y1="7" x2="-7" y2="16" />
                    <line class="beam" x1="3" y1="7" x2="7" y2="16" />
                  </template>
                  <template v-else-if="ln.lane === 'hub'">
                    <path class="fillable" transform="translate(-6.5,3.4) scale(0.62)"
                          d="M -26 8 a 10 10 0 0 1 4 -19 a 13 13 0 0 1 25 -4 a 11 11 0 0 1 22 7 a 9 9 0 0 1 -4 16 z" />
                  </template>
                  <template v-else>
                    <path class="fillable" transform="translate(0,-3)"
                          d="M -17 -10 h 30 a 4 4 0 0 1 4 4 v 11 a 4 4 0 0 1 -4 4 h -16 l -9 7 v -7 h -5 a 4 4 0 0 1 -4 -4 v -11 a 4 4 0 0 1 4 -4 z" />
                  </template>
                </g>
                <text :x="rowTextX" :y="laneY(ln.lane) - 14" :text-anchor="rowAnchor" class="row-name">{{ ln.name }}<tspan
                  v-if="ln.fact" class="row-fact" dx="14">{{ ln.fact }}</tspan></text>
                <text v-if="ln.state" :x="rowStateX" :y="laneY(ln.lane) - 14" :text-anchor="rowStateAnchor" class="row-state warn">{{ ln.state }}</text>
                <text v-else-if="flow.path === ln.key" :x="rowStateX" :y="laneY(ln.lane) - 14" :text-anchor="rowStateAnchor" class="row-state on">chosen</text>
                <text v-if="flow.path === ln.key" :x="rowTextX" :y="laneY(ln.lane) + 28" :text-anchor="rowAnchor" class="row-detail">{{ ln.detail }}</text>
              </g>
              <text :x="rowCaptionX" :y="satY - 48" text-anchor="middle" class="lane-caption">Tap a route to choose it. Tap again for details.</text>
            </g>

            <!-- near device -->
            <g :transform="`translate(${P.dev.x},${P.dev.y + 26})`" class="station near tap" :class="{ flash }" @click="openCard(nearDev)">
              <rect x="-140" y="-150" width="280" height="330" class="hit" rx="16" />
              <component :is="DEVICES[nearDev].comp" :scale="1.85" />
              <text :y="DEVICES[nearDev].nameNear" text-anchor="middle" class="st-name">{{ DEVICES[nearDev].title }}</text>
              <text :y="DEVICES[nearDev].nameNear + 24" text-anchor="middle" class="st-sub">{{ DEVICES[nearDev].sub }}</text>
            </g>

            <!-- near kit -->
            <g :transform="`translate(${P.kit.x},${P.kit.y})`" class="station kit near tap" :class="{ flash }" @click="kitTap">
              <rect x="-130" y="-160" width="260" height="360" class="hit" rx="16" />
              <image href="/kit-v1.png" x="-112" y="-146" width="224" height="265" class="kit-img" />
              <text y="150" text-anchor="middle" class="st-name">{{ me.name }}</text>
              <text y="174" text-anchor="middle" class="st-sub">MeshSat kit, {{ me.callsign }}</text>
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
              <line :x1="G.devL.x + DEVICES[leftKit.device].edgeFull" :y1="G.devL.y" :x2="G.kitL.x - 84" :y2="G.kitL.y" class="lane lora" :class="leftKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far')" />
              <line :x1="G.kitR.x + 84" :y1="G.kitR.y" :x2="G.devR.x - DEVICES[rightKit.device].edgeFull" :y2="G.devR.y" class="lane lora" :class="rightKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far')" />
              <g v-for="ln in lanes" :key="ln.lane" class="path tap" :class="[ln.lane, { selected: flow.path === ln.key, dim: flow.path && flow.path !== ln.key, silent: ln.lane === 'aprs' && aprsSilent }]" @click="selectPath(ln.key)">
                <rect :x="G.airL.x" :y="laneY(ln.lane) - 36" :width="G.airR.x - G.airL.x" height="72" class="hit" />
                <line :x1="G.airL.x" :y1="laneY(ln.lane)" :x2="G.airR.x" :y2="laneY(ln.lane)" class="lane path-line" />
                <g class="ic" :class="[ln.lane, { pulse: airPulse && ln.lane === 'aprs' }]"
                   :transform="`translate(${G.airL.x + 30},${laneY(ln.lane)}) scale(0.82)`">
                  <template v-if="ln.lane === 'aprs'">
                    <line x1="0" y1="-13" x2="0" y2="9" />
                    <circle cx="0" cy="12" r="2.4" class="solid" />
                    <path class="arc" d="M -7 -11 A 12 12 0 0 0 -7 7" />
                    <path class="arc" d="M 7 -11 A 12 12 0 0 1 7 7" />
                  </template>
                  <template v-else-if="ln.lane === 'sat'">
                    <rect class="fillable" x="-7" y="-6" width="14" height="12" rx="2" />
                    <rect class="fillable" x="-21" y="-3.5" width="11" height="7" rx="1" />
                    <rect class="fillable" x="10" y="-3.5" width="11" height="7" rx="1" />
                    <path d="M -3 -6 q 3 -8 6 0" />
                  </template>
                  <template v-else-if="ln.lane === 'hub'">
                    <path class="fillable" transform="translate(-6.5,3.4) scale(0.62)"
                          d="M -26 8 a 10 10 0 0 1 4 -19 a 13 13 0 0 1 25 -4 a 11 11 0 0 1 22 7 a 9 9 0 0 1 -4 16 z" />
                  </template>
                  <template v-else>
                    <path class="fillable" transform="translate(0,-3)" d="M -17 -10 h 30 a 4 4 0 0 1 4 4 v 11 a 4 4 0 0 1 -4 4 h -16 l -9 7 v -7 h -5 a 4 4 0 0 1 -4 -4 v -11 a 4 4 0 0 1 4 -4 z" />
                  </template>
                </g>
                <text :x="G.airL.x + 56" :y="laneY(ln.lane) - 12" text-anchor="start" class="row-name">{{ ln.name }}</text>
                <text v-if="ln.state" :x="G.airR.x - 6" :y="laneY(ln.lane) - 12" text-anchor="end" class="row-state warn">{{ ln.state }}</text>
                <text v-else-if="flow.path === ln.key" :x="G.airR.x - 6" :y="laneY(ln.lane) - 12" text-anchor="end" class="row-state on">chosen</text>
              </g>
            </g>

            <!-- left slot: device + kit -->
            <g :transform="`translate(${G.devL.x},${G.devL.y})`" class="station tap" :class="[leftKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far'), { flash: flash && leftKit.name === me.name }]" @click="openCard(leftKit.device)">
              <rect x="-90" y="-80" width="180" height="180" class="hit" rx="14" />
              <component :is="DEVICES[leftKit.device].comp" />
              <text :y="DEVICES[leftKit.device].nameFull" text-anchor="middle" class="st-name">{{ DEVICES[leftKit.device].title }}</text>
              <text :y="DEVICES[leftKit.device].nameFull + 18" text-anchor="middle" class="st-sub">{{ DEVICES[leftKit.device].sub }}</text>
            </g>
            <g :transform="`translate(${G.kitL.x},${G.kitL.y})`" class="station kit tap" :class="[leftKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far'), { flash: flash && leftKit.name === me.name }]" @click="kitTap">
              <rect x="-90" y="-112" width="180" height="250" class="hit" rx="14" />
              <image href="/kit-v1.png" x="-82" y="-108" width="164" height="194" class="kit-img" />
              <text y="108" text-anchor="middle" class="st-name">{{ leftKit.name }}</text>
              <text y="126" text-anchor="middle" class="st-sub">MeshSat kit, {{ leftKit.callsign }}</text>
            </g>

            <!-- right slot: kit + device -->
            <g :transform="`translate(${G.kitR.x},${G.kitR.y})`" class="station kit tap" :class="[rightKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far'), { flash: flash && rightKit.name === me.name }]" @click="kitTap">
              <rect x="-90" y="-112" width="180" height="250" class="hit" rx="14" />
              <image href="/kit-v1.png" x="-82" y="-108" width="164" height="194" class="kit-img" />
              <text y="108" text-anchor="middle" class="st-name">{{ rightKit.name }}</text>
              <text y="126" text-anchor="middle" class="st-sub">MeshSat kit, {{ rightKit.callsign }}</text>
            </g>
            <g :transform="`translate(${G.devR.x},${G.devR.y})`" class="station tap" :class="[rightKit.name === me.name ? 'near' : (farAlive ? 'far-alive' : 'far'), { flash: flash && rightKit.name === me.name }]" @click="openCard(rightKit.device)">
              <rect x="-90" y="-80" width="180" height="180" class="hit" rx="14" />
              <component :is="DEVICES[rightKit.device].comp" />
              <text :y="DEVICES[rightKit.device].nameFull" text-anchor="middle" class="st-name">{{ DEVICES[rightKit.device].title }}</text>
              <text :y="DEVICES[rightKit.device].nameFull + 18" text-anchor="middle" class="st-sub">{{ DEVICES[rightKit.device].sub }}</text>
            </g>

            <text :x="nearIsLeft ? 1012 : 268" y="446" text-anchor="middle" class="far-note">
              {{ farAlive ? `${peer.name} heard over the air ${farAgeS} s ago` : `${peer.name} not heard yet on this kit` }}
            </text>
          </template>

          <!-- replay label -->

          <!-- the message -->
          <g v-if="dot.visible" class="msg" :class="[dot.lane, current && current.failed ? 'failed' : '', replaying ? 'replay' : '', dot.waiting ? 'waiting' : '']" :transform="`translate(${dot.x},${dot.y})`">
            <circle r="26" fill="url(#dotg)" opacity="0.55" />
            <circle r="8" class="core" filter="url(#glow)" />
          </g>
        </svg>
      </div>

      <!-- strip: one row. The message is the only thing here a visitor reads;
           the QR and the crew's numbers stay small. Empty = one quiet line. -->
      <section class="strip flex items-center gap-4 px-4 pb-2 pt-1 shrink-0" :class="{ live: current }">
        <div class="flex items-center gap-2 shrink-0" @click="qrTap">
          <img src="/qr-meshsat.svg" alt="QR code for meshsat.net" class="qr w-[48px] h-[48px] shrink-0" draggable="false" />
          <div class="font-sans text-[12px] leading-tight text-gray-400">meshsat.net<br /><span class="text-gray-600">open source</span></div>
        </div>
        <div class="min-w-0 flex-1 flex flex-col justify-center">
          <button type="button" class="text-left w-full font-display leading-tight break-words" :class="current ? [msgSize, 'text-gray-50 line-clamp-2'] : 'font-sans text-[15px] text-gray-500'" @click="toggleText" :title="showText ? 'Tap to hide message text' : 'Tap to show message text'">
            {{ current ? displayText(current) : (DEVICES[nearDev].keyboard ? 'Your message appears here the moment this kit hears it.' : 'The next message from the other kit appears here the moment it lands.') }}
          </button>
          <div v-if="current" class="font-sans text-[13px] leading-4 text-gray-300 line-clamp-2">
            {{ statusLine }}
            <span class="text-gray-500"> · {{ current.bytes || (current.text || '').length }} bytes</span>
            <span v-if="current.rssi" class="text-gray-500"> · {{ current.rssi }} dBm</span>
            <span v-if="current.snr" class="text-gray-500"> · SNR {{ current.snr }} dB</span>
            <span v-if="insideMs !== null" class="text-gray-500"> · through this kit in <span class="text-teal-300">{{ insideMs }} ms</span></span>
          </div>
        </div>
        <div class="shrink-0 flex flex-col items-end gap-1">
          <div v-show="!current" class="font-mono text-[12px] leading-4 text-gray-400 tabular-nums whitespace-nowrap">
            <span class="text-gray-500">packets in/out, last minute</span>
            <span class="text-gray-300 ml-2">LoRa</span> {{ rateOf('lora','rx') }}/{{ rateOf('lora','tx') }}
            <span class="text-gray-300 ml-2">APRS</span> {{ rateOf('aprs','rx') }}/{{ rateOf('aprs','tx') }}
            <span class="text-gray-300 ml-2">SMS</span> {{ rateOf('sms','rx') }}/{{ rateOf('sms','tx') }}
            <span class="text-gray-300 ml-2">SAT</span> {{ rateOf('sat','rx') }}/{{ rateOf('sat','tx') }}
          </div>
          <div class="flex items-center gap-2">
            <span v-if="testNote" class="font-mono text-[11px] text-gray-500">{{ testNote }}</span>
            <button type="button" @click="drawer = !drawer" class="font-mono text-[12px] px-3 py-2 rounded border border-gray-700 text-gray-400 hover:border-teal-500 hover:text-teal-300 whitespace-nowrap">Stats for nerds</button>
            <button type="button" @click="sendTest" :disabled="testBusy" class="font-mono text-[12px] px-3 py-2 rounded border whitespace-nowrap" :class="testArmed ? 'border-teal-500 text-teal-300' : 'border-gray-700 text-gray-400 hover:text-gray-100'">
              {{ testArmed ? 'Tap again to send it' : 'Send a test frame' }}
            </button>
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

    <!-- COMPOSER: double-tap the kit, type on the panel, send to either mesh -->
    <div v-if="composer.open" class="absolute inset-0 z-20" @click.self="closeComposer">
      <section class="absolute left-0 right-0 bottom-0 bg-gray-900 border-t border-gray-700 px-4 pt-3 pb-3 card-sheet composer" role="dialog" aria-label="Type a message">
        <div class="flex items-center gap-3">
          <h2 class="font-display text-xl text-gray-50 shrink-0">Type a message</h2>
          <div class="flex items-stretch gap-2 ml-1" role="radiogroup" aria-label="Recipient">
            <button v-for="opt in composerOptions" :key="opt.key" type="button" role="radio" :aria-checked="composer.to === opt.key" @click="composer.to = opt.key"
              class="recipient flex items-center gap-2 rounded-lg border px-3 py-1 text-left" :class="composer.to === opt.key ? 'border-teal-500 bg-teal-500/10' : 'border-gray-700'">
              <img :src="opt.img" alt="" class="h-9 w-auto" draggable="false" />
              <span>
                <span class="block font-display text-base leading-tight" :class="composer.to === opt.key ? 'text-teal-300' : 'text-gray-100'">{{ opt.title }}</span>
                <span class="block font-sans text-xs text-gray-400 leading-tight">{{ opt.sub }}</span>
              </span>
            </button>
          </div>
          <button type="button" @click="closeComposer" class="ml-auto font-mono text-sm text-gray-400 hover:text-gray-100 px-3 py-2">close</button>
        </div>
        <div class="mt-2 rounded-lg border border-gray-700 bg-gray-950 px-4 py-2 min-h-[52px] flex items-center" aria-live="polite">
          <span class="font-mono text-2xl text-gray-50 break-all leading-tight">{{ composer.text }}<span class="caret" aria-hidden="true">|</span></span>
          <span class="ml-auto pl-3 font-mono text-xs text-gray-500 tabular-nums shrink-0">{{ composer.text.length }}/{{ COMPOSER_MAX }}</span>
        </div>
        <div class="keyboard mt-2 flex flex-col gap-1.5" aria-label="On-screen keyboard">
          <div v-for="(row, ri) in KEY_ROWS" :key="ri" class="flex gap-1.5 justify-center">
            <button v-for="k in row" :key="k" type="button" class="key font-mono text-lg rounded-md border border-gray-700 text-gray-100 h-10 select-none"
              :class="[k === 'shift' || k === 'back' ? 'w-[76px] text-sm' : 'w-[62px]', k === 'shift' && composer.shift ? 'border-teal-500 text-teal-300' : '']"
              @click="keyTap(k)">{{ k === 'shift' ? 'shift' : k === 'back' ? 'delete' : (composer.shift ? k.toUpperCase() : k) }}</button>
          </div>
          <div class="flex gap-1.5 justify-center">
            <button type="button" class="key font-mono text-lg rounded-md border border-gray-700 text-gray-100 h-10 w-[430px] select-none" @click="keyTap('space')">space</button>
            <button type="button" class="key send font-display text-base rounded-md h-10 w-[230px] select-none bg-teal-500 text-gray-950 disabled:opacity-40" :disabled="!composer.text.trim() || composer.busy" @click="sendComposed">{{ composer.busy ? 'sending' : `Send to ${composerTarget.title.toLowerCase()}` }}</button>
          </div>
        </div>
        <p class="mt-2 font-sans text-sm min-h-[20px]" :class="composer.note ? 'text-gray-200' : 'text-gray-600'">{{ composer.note || (composer.to === 'local' ? 'Broadcast on this kit\'s own channel.' : `Leaves this kit over SMS, ${peer.name} puts it on its mesh.`) }}</p>
      </section>
    </div>

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
          <div v-for="b in ['lora','aprs','sms','sat']" :key="b" class="rounded border border-gray-800 p-3">
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
.island .island-label { font-family: 'IBM Plex Mono', monospace; font-size: 24px; fill: #E4DAC6; letter-spacing: 0.04em; }
.island .island-sub { font-family: 'IBM Plex Sans', sans-serif; font-size: 21px; fill: #AE9C7A; }
.island.far { opacity: 0.35; }
.island.far-alive { opacity: 0.85; }
.lane { stroke: #8E7C5C; stroke-width: 2; }
.lane.lora.far { opacity: 0.35; }
.lane.lora.far-alive { opacity: 0.85; }
.air-line { stroke: #F7F7F4; stroke-opacity: 0.55; stroke-width: 2; }
.air.silent .air-line { stroke-dasharray: 3 9; stroke-opacity: 0.3; }
@keyframes wave { 0% { opacity: 0; } 20% { opacity: 0.7; } 100% { opacity: 0; } }
.air-label { font-family: 'IBM Plex Mono', monospace; font-size: 28px; fill: #F7F7F4; letter-spacing: 0.02em; }
.air-sub { font-family: 'IBM Plex Sans', sans-serif; font-size: 21px; fill: #B4B4BD; }
.air-warn { font-family: 'IBM Plex Sans', sans-serif; font-size: 21px; fill: #FCD34D; }
.visitor-line { font-family: 'IBM Plex Sans', sans-serif; font-size: 22px; fill: #D6D6DC; }
.replay-note { font-family: 'IBM Plex Sans', sans-serif; font-size: 14px; fill: #8A8A96; }
.sms-line { stroke: #E0B458; stroke-width: 1.5; stroke-dasharray: 10 8; opacity: 0.55; }
/* The three booth paths (MESHSAT-962): the chosen lane is solid and lit,
   the other two are dashed and dim; every label follows its lane. */
.path .path-line { stroke: #F7F7F4; stroke-opacity: 0.6; stroke-width: 2; transition: stroke-opacity 0.3s, stroke-width 0.3s; }
.path.sms .path-line { stroke: #E0B458; }
.path.hub .path-line { stroke: #8FB8DE; }
.path.selected .path-line { stroke-opacity: 1; stroke-width: 3.5; }
.path.dim .path-line { stroke-dasharray: 4 12; stroke-opacity: 0.3; }
.path .path-start { fill: #F7F7F4; opacity: 0; transition: opacity 0.3s; }
.path.sms .path-start { fill: #E0B458; }
.path.hub .path-start { fill: #8FB8DE; }
.path.selected .path-start { opacity: 1; }
.path.silent .path-line { stroke-dasharray: 3 9; stroke-opacity: 0.3; }
/* Route rows (MESHSAT-987): one line of text per lane, riding the line —
   name and fact above it, state pinned to the outer end, and the detail
   sentence under the chosen route only. Four captions at once were a wall
   of text; one is a sentence a visitor actually reads. */
.path .row-name { font-family: 'IBM Plex Mono', monospace; font-size: 23px; fill: #F7F7F4; letter-spacing: 0.02em; transition: fill 0.3s; }
.path .row-fact { font-family: 'IBM Plex Mono', monospace; font-size: 18px; fill: #9E9EAC; }
.path .row-state { font-family: 'IBM Plex Sans', sans-serif; font-size: 18px; fill: #9E9EAC; }
.path .row-state.warn { fill: #FCD34D; }
.path .row-detail { font-family: 'IBM Plex Sans', sans-serif; font-size: 19px; fill: #C4C4CE; }
.path.dim .row-name { fill: #8A8A96; }
.path.dim .row-fact, .path.dim .row-state { fill: #6E6E7E; }
.lane-caption { font-family: 'IBM Plex Sans', sans-serif; font-size: 18px; fill: #82828F; }
/* One icon family: same box, same stroke weight, one per lane, drawn in
   that bearer's colour. Filled only on the chosen route, so which way this
   kit sends reads from across the aisle without reading a word. */
.path .ic { fill: none; stroke: #F7F7F4; stroke-width: 1.7; stroke-linecap: round; stroke-linejoin: round; transition: opacity 0.3s, stroke-width 0.3s; }
.path .ic .solid { fill: #F7F7F4; stroke: none; }
.path.sat .ic { stroke: #B9A7E6; }
.path.hub .ic { stroke: #8FB8DE; }
.path.sms .ic { stroke: #E0B458; }
.path.sat .ic .solid { fill: #B9A7E6; }
.path.hub .ic .solid { fill: #8FB8DE; }
.path.sms .ic .solid { fill: #E0B458; }
.path.selected .ic { stroke-width: 2.2; }
.path.selected .ic .fillable { fill: rgba(247, 247, 244, 0.16); }
.path.selected.sat .ic .fillable { fill: rgba(185, 167, 230, 0.18); }
.path.selected.hub .ic .fillable { fill: rgba(143, 184, 222, 0.18); }
.path.selected.sms .ic .fillable { fill: rgba(224, 180, 88, 0.18); }
.path.dim .ic { opacity: 0.5; }
.path .ic .arc { opacity: 0.75; }
.path .ic .arc.wide { opacity: 0.4; }
.path .ic.pulse .arc { stroke: #F96118; animation: wave 1.2s ease-out infinite; }
.path.silent .ic .arc { opacity: 0.12; animation: none; }
.path .ic .beam { opacity: 0.3; }
.path .ic.beaming .beam { animation: beam 1.2s ease-in-out infinite; }
.path.nosky .ic { opacity: 0.5; }
svg.full .path .row-name { font-size: 19px; }
svg.full .path .row-state { font-size: 15px; }
.msg.hub .core { fill: #8FB8DE; }
/* Satellite lane (MESHSAT-962): lavender so it reads apart from the white
   radio, the blue Hub and the gold SMS; a small satellite glyph instead of
   the radio waves; nosky when the modem is silent. */
.path.sat .path-line, .path.sat .path-start { stroke: #B9A7E6; fill: #B9A7E6; }
.path.sat .path-line { fill: none; }
@keyframes beam { 0%, 100% { opacity: 0.2; } 50% { opacity: 0.9; } }
.path.nosky .path-line { stroke-dasharray: 3 9; stroke-opacity: 0.3; }
.msg.sat .core { fill: #B9A7E6; }
.msg.waiting .core { animation: satwait 1.6s ease-in-out infinite; }
@keyframes satwait { 0%, 100% { r: 8; } 50% { r: 12; } }
.sms-label { font-family: 'IBM Plex Sans', sans-serif; font-size: 21px; fill: #AE9C7A; }
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
.st-name { font-family: 'IBM Plex Mono', monospace; font-size: 24px; fill: #F7F7F4; }
.st-sub { font-family: 'IBM Plex Sans', sans-serif; font-size: 20px; fill: #8A8A96; }
.far-note { font-family: 'IBM Plex Sans', sans-serif; font-size: 21px; fill: #8A8A96; }
svg.full .air-label { font-size: 24px; }
svg.full .air-sub, svg.full .air-warn, svg.full .sms-label { font-size: 18px; }
svg.full .st-name { font-size: 22px; }
svg.full .st-sub { font-size: 16px; }
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
.composer .key { touch-action: manipulation; transition: background-color 80ms, color 80ms; }
.composer .key:active { background: #F96118; color: #040406; border-color: #F96118; }
.composer .recipient { touch-action: manipulation; }
.composer .caret { color: #F96118; margin-left: 1px; animation: caret 1s steps(2, start) infinite; }
@keyframes caret { to { visibility: hidden; } }
.msg .core { fill: #F96118; }
.msg.sms .core { fill: #E0B458; }
.msg.failed .core { fill: #F0655A; }
.chip { transition: color 0.3s, border-color 0.3s; }
.route-wrap { padding: 0 8px; }
.strip { min-height: 60px; }
.strip.live { min-height: 84px; max-height: 124px; }
@media (prefers-reduced-motion: reduce) {
  .path .ic.pulse .arc { animation: none; }
  .path .ic.beaming .beam, .msg.waiting .core { animation: none; }
  .composer .caret { animation: none; }
  .station.flash :deep(.device .body), .station.flash :deep(.device .bezel), .station.flash :deep(.device.photo .photo-img), .station.flash .kit-img { animation: none; }
}
</style>
