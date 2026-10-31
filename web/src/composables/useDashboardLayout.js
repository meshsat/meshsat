// The engineer dashboard's widgets: which exist, which this screen shows,
// and what happens when hardware comes and goes. [MESHSAT-1588]
//
// - A hardware widget whose device is gone is hidden ("not connected").
//   A USB device the Bridge names (deviceTypes: the radio's class in
//   /api/devices, the scan it runs every 5 s) is there while the scan sees
//   it or its gateway is connected, so it goes the moment it is unplugged.
//   Other hardware (a modem on the Pi's UART, a ModemManager modem) is
//   there while its gateway is enabled: the Bridge disables a gateway
//   whose hardware it cannot find. A device that is there but down stays
//   on screen, showing it is down.
// - A device that shows up for the first time on this screen brings its
//   widget, at its default place, and the arrival is announced once.
// - A widget the person removed stays removed, hardware or not; when its
//   device shows up they are offered it instead.
// - The layout is this screen's (localStorage): the kiosk panel and a
//   laptop want different dashboards. Storage that fails or is empty
//   gives the default layout.
import { computed, ref, watch } from 'vue'

// Default order. kind: core (always available), hardware (needs its
// device), software (always available, but may be removed like any).
export const WIDGETS = [
  { id: 'iridium',   label: 'Satellite',  kind: 'hardware', gateways: ['iridium', 'iridium_imt'], device: 'Iridium modem' },
  { id: 'mesh',      label: 'Meshtastic', kind: 'core' },
  { id: 'cellular',  label: 'Cellular',   kind: 'hardware', gateways: ['cellular'], device: 'cellular modem' },
  { id: 'aprs',      label: 'APRS',       kind: 'hardware', gateways: ['aprs'], device: 'APRS radio' },
  { id: 'tetra',     label: 'TETRA',      kind: 'hardware', gateways: ['tetra'], deviceTypes: ['tetra'], device: 'TETRA radio' },
  { id: 'zigbee',    label: 'ZigBee',     kind: 'hardware', gateways: ['zigbee'], deviceTypes: ['zigbee'], device: 'ZigBee dongle' },
  { id: 'reticulum', label: 'Reticulum',  kind: 'software' },
  { id: 'tak',       label: 'TAK',        kind: 'software' },
  { id: 'sos',       label: 'SOS',        kind: 'core' },
  { id: 'location',  label: 'Location',   kind: 'core' },
  { id: 'queue',     label: 'Queue',      kind: 'core' },
  { id: 'burst',     label: 'Burst queue', kind: 'software' },
  { id: 'activity',  label: 'Activity',   kind: 'core' },
]
const BY_ID = Object.fromEntries(WIDGETS.map(w => [w.id, w]))
const DEFAULT_ORDER = WIDGETS.map(w => w.id)
const STORAGE_KEY = 'meshsat.dashboard.v2'
const LEGACY_ORDER_KEY = 'meshsat-widget-order'

function readStored() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) {
      const s = JSON.parse(raw)
      if (s && Array.isArray(s.order)) {
        return { order: s.order, removed: Array.isArray(s.removed) ? s.removed : [], seen: Array.isArray(s.seen) ? s.seen : [], forced: Array.isArray(s.forced) ? s.forced : [] }
      }
    }
    // The drag order saved before widgets could be removed.
    const legacy = JSON.parse(localStorage.getItem(LEGACY_ORDER_KEY))
    if (Array.isArray(legacy)) return { order: legacy, removed: [], seen: [], forced: [] }
  } catch { /* storage blocked or corrupt: default layout */ }
  return null
}

// mergeOrder keeps the saved order and puts every widget it does not know
// (a widget added in a later release) at its default place.
export function mergeOrder(saved) {
  const known = (saved || []).filter(id => BY_ID[id])
  const out = [...new Set(known)]
  DEFAULT_ORDER.forEach((id, i) => {
    if (out.includes(id)) return
    const before = DEFAULT_ORDER.slice(0, i).reverse().find(p => out.includes(p))
    out.splice(before ? out.indexOf(before) + 1 : 0, 0, id)
  })
  return out
}

// presence of a widget's hardware: true, false, or null while the lists
// it needs have not loaded (nothing is hidden or announced then).
export function presence(widget, gateways, gatewaysLoaded, devices, devicesLoaded) {
  if (widget.kind !== 'hardware') return true
  if (!gatewaysLoaded) return null
  const gws = (gateways || []).filter(g => widget.gateways.includes(g.type))
  if (widget.deviceTypes) {
    if (!devicesLoaded) return null
    if ((devices || []).some(d => widget.deviceTypes.includes(d.device_type))) return true
    return gws.some(g => g.enabled && g.connected)
  }
  return gws.some(g => g.enabled)
}

export function useDashboardLayout(store) {
  const stored = readStored()
  const order = ref(mergeOrder(stored?.order))
  const removed = ref(stored?.removed || [])
  const forced = ref(stored?.forced || [])  // shown although not detected
  const seen = ref(stored?.seen || [])      // hardware this screen has seen
  const customizing = ref(false)
  const arrivals = ref([])                  // { id, label, device, offered } announced once
  const fresh = ref([])                     // ids that just arrived (the ring)

  function persist() {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ order: order.value, removed: removed.value, forced: forced.value, seen: seen.value }))
    } catch { /* storage blocked: the layout lasts this visit */ }
  }

  const loaded = computed(() => !!store.gatewaysLoaded)
  const present = computed(() => Object.fromEntries(WIDGETS.map(w =>
    [w.id, presence(w, store.gateways, loaded.value, store.devices, !!store.devicesLoaded)])))

  const visible = computed(() => order.value.filter(id => {
    if (removed.value.includes(id)) return false
    const p = present.value[id]
    return p !== false || forced.value.includes(id)
  }))
  const notDetected = computed(() => WIDGETS.filter(w => present.value[w.id] === false && !forced.value.includes(w.id) && !removed.value.includes(w.id)))
  const removedWidgets = computed(() => removed.value.map(id => BY_ID[id]).filter(Boolean))

  // Arrivals: a hardware widget's device present for the first time on
  // this screen. The first load only records what is there.
  let firstLook = true
  watch(present, now => {
    if (Object.values(now).some(v => v === null)) return
    const newly = WIDGETS.filter(w => w.kind === 'hardware' && now[w.id] === true && !seen.value.includes(w.id))
    if (newly.length === 0) { firstLook = false; return }
    seen.value = [...seen.value, ...newly.map(w => w.id)]
    if (!firstLook) {
      for (const w of newly) {
        const offered = removed.value.includes(w.id)
        arrivals.value = [...arrivals.value, { id: w.id, label: w.label, device: w.device, offered }]
        if (!offered) {
          fresh.value = [...fresh.value, w.id]
          setTimeout(() => { fresh.value = fresh.value.filter(x => x !== w.id) }, 6000)
        }
      }
    }
    firstLook = false
    persist()
  }, { immediate: true })

  function remove(id) {
    if (!removed.value.includes(id)) removed.value = [...removed.value, id]
    forced.value = forced.value.filter(x => x !== id)
    persist()
  }
  function add(id) {
    removed.value = removed.value.filter(x => x !== id)
    if (present.value[id] === false && !forced.value.includes(id)) forced.value = [...forced.value, id]
    persist()
  }
  function showAnyway(id) {
    if (!forced.value.includes(id)) forced.value = [...forced.value, id]
    persist()
  }
  function unforce(id) {
    forced.value = forced.value.filter(x => x !== id)
    persist()
  }
  function reorder(next) {
    order.value = mergeOrder(next)
    persist()
  }
  function reset() {
    order.value = [...DEFAULT_ORDER]
    removed.value = []
    forced.value = []
    persist()
  }
  function dismissArrival(id) {
    arrivals.value = arrivals.value.filter(a => a.id !== id)
  }
  // undoArrival is the announcement's "Hide": the widget goes, as if
  // removed by hand.
  function undoArrival(id) {
    remove(id)
    dismissArrival(id)
  }
  function acceptOffer(id) {
    add(id)
    dismissArrival(id)
    fresh.value = [...fresh.value, id]
    setTimeout(() => { fresh.value = fresh.value.filter(x => x !== id) }, 6000)
  }

  return {
    order, visible, notDetected, removedWidgets, present, customizing, arrivals, fresh,
    remove, add, showAnyway, unforce, reorder, reset, dismissArrival, undoArrival, acceptOffer,
    label: id => BY_ID[id]?.label || id,
    widget: id => BY_ID[id],
  }
}
