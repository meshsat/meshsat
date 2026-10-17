// The dashboard's smart layout (useDashboardLayout): widgets follow the
// hardware the Bridge detects, the person's choices stick, and a device
// plugged in for the first time brings its widget once. [MESHSAT-1588]
import assert from 'node:assert/strict'
import { test, beforeEach } from 'vitest'
import { nextTick, reactive } from 'vue'
import { useDashboardLayout, mergeOrder, WIDGETS } from '@/composables/useDashboardLayout'

const KITLIKE = [
  { type: 'aprs', enabled: true, connected: true },
  { type: 'cellular', enabled: true, connected: true },
  { type: 'iridium_imt', enabled: true, connected: false }, // present, no sky: stays on screen
  { type: 'zigbee', enabled: false, connected: false },     // dongle out: hidden
]

function fakeStore(gateways = [], loaded = false) {
  return reactive({ gateways, gatewaysLoaded: loaded })
}

beforeEach(() => localStorage.clear())

test('an_old_saved_order_gains_new_widgets_in_their_default_place', () => {
  const legacy = ['mesh', 'iridium', 'cellular', 'aprs', 'zigbee', 'reticulum', 'tak', 'sos', 'location', 'queue', 'burst', 'hemb', 'activity', 'gone']
  const merged = mergeOrder(legacy)
  assert.equal(merged.length, WIDGETS.length)
  assert.equal(merged[0], 'mesh') // the person's order is kept
  assert.equal(merged[merged.indexOf('aprs') + 1], 'tetra') // TETRA after APRS, as by default
  assert.ok(!merged.includes('gone'))
})

test('nothing_hides_or_is_announced_before_the_gateways_load', async () => {
  const store = fakeStore()
  const l = useDashboardLayout(store)
  assert.ok(l.visible.value.includes('zigbee'))
  assert.ok(l.visible.value.includes('tetra'))
  store.gateways = KITLIKE
  store.gatewaysLoaded = true
  await nextTick()
  assert.ok(!l.visible.value.includes('zigbee'), 'an unplugged dongle hides its widget')
  assert.ok(!l.visible.value.includes('tetra'), 'no radio, no TETRA widget')
  assert.ok(l.visible.value.includes('iridium'), 'a modem without sky is still there')
  assert.ok(l.visible.value.includes('mesh') && l.visible.value.includes('sos'), 'core widgets always show')
  assert.deepEqual(l.arrivals.value, [], 'what is there at the first look is not announced')
  assert.deepEqual(l.notDetected.value.map(w => w.id), ['tetra', 'zigbee'])
})

test('a_radio_plugged_in_brings_its_widget_once', async () => {
  const store = fakeStore(KITLIKE, true)
  const l = useDashboardLayout(store)
  await nextTick()
  store.gateways = [...KITLIKE, { type: 'tetra', enabled: true, connected: true }]
  await nextTick()
  assert.ok(l.visible.value.includes('tetra'))
  assert.deepEqual(l.arrivals.value.map(a => [a.id, a.offered]), [['tetra', false]])
  assert.ok(l.fresh.value.includes('tetra'), 'the arrival ring')
  l.dismissArrival('tetra')

  // Unplugged: the widget hides by itself; plugged in again: it comes
  // back without a second announcement.
  store.gateways = [...KITLIKE, { type: 'tetra', enabled: false, connected: false }]
  await nextTick()
  assert.ok(!l.visible.value.includes('tetra'))
  store.gateways = [...KITLIKE, { type: 'tetra', enabled: true, connected: true }]
  await nextTick()
  assert.ok(l.visible.value.includes('tetra'))
  assert.deepEqual(l.arrivals.value, [])
})

test('a_removed_widget_is_offered_not_forced_when_its_device_arrives', async () => {
  const store = fakeStore(KITLIKE, true)
  const l = useDashboardLayout(store)
  await nextTick()
  l.remove('tetra')
  store.gateways = [...KITLIKE, { type: 'tetra', enabled: true, connected: true }]
  await nextTick()
  assert.ok(!l.visible.value.includes('tetra'), 'removed stays removed')
  assert.deepEqual(l.arrivals.value.map(a => [a.id, a.offered]), [['tetra', true]])
  l.acceptOffer('tetra')
  assert.ok(l.visible.value.includes('tetra'))
  assert.deepEqual(l.arrivals.value, [])
})

test('the_announcements_remove_undoes_the_arrival', async () => {
  const store = fakeStore(KITLIKE, true)
  const l = useDashboardLayout(store)
  await nextTick()
  store.gateways = [...KITLIKE, { type: 'tetra', enabled: true, connected: true }]
  await nextTick()
  l.undoArrival('tetra')
  assert.ok(!l.visible.value.includes('tetra'))
  assert.deepEqual(l.removedWidgets.value.map(w => w.id), ['tetra'])
})

test('show_anyway_and_reset', async () => {
  const store = fakeStore(KITLIKE, true)
  const l = useDashboardLayout(store)
  await nextTick()
  l.showAnyway('zigbee')
  assert.ok(l.visible.value.includes('zigbee'))
  assert.ok(!l.notDetected.value.some(w => w.id === 'zigbee'))
  l.remove('hemb')
  l.reorder(['activity', ...l.order.value.filter(x => x !== 'activity')])
  l.reset()
  assert.ok(!l.visible.value.includes('zigbee'), 'reset drops show-anyway')
  assert.ok(l.visible.value.includes('hemb'), 'reset brings removed widgets back')
  assert.equal(l.order.value[0], 'iridium')
})

test('the_layout_is_this_screens_and_survives_a_reload', async () => {
  const store = fakeStore(KITLIKE, true)
  const a = useDashboardLayout(store)
  await nextTick()
  a.remove('burst')
  a.reorder(['queue', ...a.order.value.filter(x => x !== 'queue')])
  const b = useDashboardLayout(fakeStore(KITLIKE, true))
  await nextTick()
  assert.equal(b.order.value[0], 'queue')
  assert.ok(!b.visible.value.includes('burst'))
})

test('blocked_storage_gives_the_default_layout', () => {
  const real = Storage.prototype.getItem
  Storage.prototype.getItem = () => { throw new Error('blocked') }
  try {
    const l = useDashboardLayout(fakeStore())
    assert.equal(l.order.value.length, WIDGETS.length)
  } finally {
    Storage.prototype.getItem = real
  }
})
