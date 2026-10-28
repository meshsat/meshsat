// The Warnings page and the public-warning banner against controlled API
// answers and the bridge's event stream: the list in force first with its
// marks, states and bearers; a `warning` event puts the row on the page at
// once and reads the list again; the banner shows the headline and the
// label while a warning is in force and nothing otherwise; and the store's
// words and in-force rule match the relay's. [MESHSAT-1734]
import assert from 'node:assert/strict'
import { createApp, h } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import { test } from 'vitest'
import WarningsView from '@/views/WarningsView.vue'
import WarningBanner from '@/components/WarningBanner.vue'
import { expiry, headline, isInForce, label, NO_EXPIRY_IN_FORCE_MS } from '@/stores/warnings'
import { flush, json, mountRoot, openStreams, serve, useHarness } from './harness.js'

useHarness()

function bodyText() { return document.body.textContent.replace(/\s+/g, ' ') }

const HOUR = 3600 * 1000
const ahead = new Date(Date.now() + 2 * HOUR).toISOString()
const hhmm = ahead.slice(11, 16)

// Four rows as GET /api/warnings lists them, newest received first: an
// expired real warning, a Cancel of an older one, the cancelled one, and a
// live test warning that went out on everything.
const live = {
  id: 'a1b2c3d4e5f60718', msg_type: 'Alert', test: true, source: 'test', sent: '2026-10-28T10:11:41Z', expires: ahead,
  severity: 'Moderate', urgency: 'Expected', certainty: 'Observed', category: 'Met', event: 'Wind', language: 'nl',
  text: 'Kans op windstoten tot 90 km/u', via: 'hub', received_at: '2026-10-28T10:12:00Z', cancelled: false,
  bearers: '["mesh-alert","mesh-text","aprs","lxmf"]'
}
const cancel = {
  id: '0f1e2d3c4b5a6978', ref: '1122334455667788', msg_type: 'Cancel', test: false, source: 'meteoalarm-nl', sent: '2026-10-27T09:00:00Z',
  expires: '0001-01-01T00:00:00Z', severity: 'Unknown', urgency: 'Unknown', certainty: 'Unknown', category: 'Met', event: 'Rain',
  text: '', via: 'iridium_0', received_at: '2026-10-27T09:05:00Z', cancelled: true, bearers: '["mesh-alert","mesh-text"]'
}
const cancelled = {
  id: '1122334455667788', msg_type: 'Alert', test: false, source: 'meteoalarm-nl', sent: '2026-10-27T06:00:00Z', expires: ahead,
  severity: 'Severe', urgency: 'Immediate', certainty: 'Likely', category: 'Met', event: 'Rain', text: 'Zware regen',
  via: 'hub', received_at: '2026-10-27T06:01:00Z', cancelled: true, bearers: '["mesh-alert","mesh-text","aprs"]'
}
const expired = {
  id: '99aabbccddeeff00', msg_type: 'Alert', test: false, source: 'dwd', sent: '2026-10-20T06:00:00Z', expires: '2026-10-20T18:00:00Z',
  severity: 'Extreme', urgency: 'Immediate', certainty: 'Observed', category: 'Met', event: 'Thunderstorm', text: 'Schwere Gewitter',
  via: 'hub', received_at: '2026-10-20T18:30:00Z', cancelled: false, bearers: '[]'
}

function api(list, reads) {
  return async (url) => {
    const u = new URL(url, 'http://review.local')
    if (u.pathname === '/api/warnings') { reads.push(u.search); return json(list()) }
    return json({})
  }
}

test('the_page_lists_in_force_first_with_marks_states_and_bearers', async () => {
  const reads = []
  serve(api(() => [expired, cancel, cancelled, live], reads))
  mountRoot(WarningsView)
  await flush()
  const rows = [...document.querySelectorAll('tr[data-state]')]
  assert.deepEqual(rows.map(r => r.dataset.state), ['in force', 'expired', 'cancellation', 'cancelled'])
  const first = rows[0].textContent.replace(/\s+/g, ' ')
  assert.match(first, /TEST/)
  assert.match(first, /Wind/)
  assert.match(first, /Kans op windstoten tot 90 km\/u/)
  assert.match(first, new RegExp(`TEST WARNING Wind Moderate until ${hhmm} UTC`))
  assert.match(first, /MeshSat test/)
  assert.match(first, /Relay of MeshSat test warning, issued 28 Oct 10:11 UTC via MeshSat, not an official NL-Alert/)
  assert.match(first, /Hub/)
  assert.match(first, /mesh alert, mesh text, APRS bulletin, LXMF/)
  const third = rows[2].textContent.replace(/\s+/g, ' ')
  assert.match(third, /CANCELLED/)
  assert.match(third, /refers to 1122334455667788/)
  assert.match(third, /satellite/)
  assert.match(third, /no expiry given/)
  assert.match(rows[1].textContent.replace(/\s+/g, ' '), /none/)
  assert.equal(reads.length, 1)
  assert.equal(openStreams().length, 1)
  assert.equal(openStreams()[0].url, '/api/events')
})

test('a_warning_event_puts_the_row_on_the_page_at_once_and_reads_again', async () => {
  const reads = []
  let list = []
  serve(api(() => list, reads))
  mountRoot(WarningsView)
  await flush()
  assert.match(bodyText(), /No public warning has reached this kit/)
  list = [live]
  openStreams()[0].emit({
    type: 'warning', message: 'TEST WARNING Wind Moderate', time: '2026-10-28T10:12:00Z',
    data: { ...live, label: label(live), expired: false }
  })
  await flush()
  assert.equal(reads.length, 2, 'the event did not make the page read the list again')
  assert.equal(document.querySelectorAll('tr[data-state="in force"]').length, 1)
  assert.match(bodyText(), /Kans op windstoten/)
})

test('the_compact_banner_is_one_line_while_a_warning_is_in_force_and_nothing_otherwise', async () => {
  // The relay keeps the row before it emits, so the list read on the event
  // holds it.
  let list = [expired, cancel, cancelled]
  serve(api(() => list, []))
  mountRoot(WarningBanner, { compact: true })
  await flush()
  assert.equal(document.querySelector('[data-testid="warning-banner"]'), null)
  list = [live, expired, cancel, cancelled]
  openStreams()[0].emit({ type: 'warning', message: '', data: { ...live, label: label(live), expired: false } })
  await flush()
  const banner = document.querySelector('[data-testid="warning-banner"]')
  assert.ok(banner, 'the banner is missing')
  assert.equal(banner.querySelector('[data-role="headline"]').textContent.trim(), `TEST WARNING Wind Moderate until ${hhmm} UTC`)
  assert.equal(banner.querySelector('[data-role="label"]').textContent.trim(), 'Relay of MeshSat test warning, issued 28 Oct 10:11 UTC via MeshSat, not an official NL-Alert')
  assert.equal(banner.querySelector('[data-role="more"]'), null)
  assert.equal(banner.querySelectorAll('[data-role="headline"]').length, 1, 'the booth banner is more than one line')
})

test('the_dashboard_banner_shows_every_warning_in_force_with_its_label_beneath', async () => {
  const second = { ...live, id: 'b2c3d4e5f6071829', test: false, source: 'meteoalarm-nl', event: 'Fog', severity: 'Minor', sent: '2026-10-28T11:00:00Z', text: 'Dichte mist', bearers: '["mesh-alert","mesh-text"]' }
  serve(api(() => [second, live, expired], []))
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => h('div') } }, { path: '/warnings', component: { render: () => h('div') } }] })
  const app = createApp(WarningBanner)
  app.use(createPinia()).use(router)
  app.mount('#app')
  await router.isReady()
  await flush()
  try {
    const banner = document.querySelector('[data-testid="warning-banner"]')
    assert.ok(banner, 'the banner is missing')
    const part = (a, role) => (a.querySelector(`[data-role="${role}"]`)?.textContent || '').trim()
    const lines = [...banner.querySelectorAll('a')].map(a => [part(a, 'headline'), part(a, 'text'), part(a, 'label')])
    assert.deepEqual(lines, [
      [`WARNING Fog Minor until ${hhmm} UTC`, 'Dichte mist', 'Relay of KNMI via MeteoAlarm warning, issued 28 Oct 11:00 UTC via MeshSat, not an official NL-Alert'],
      [`TEST WARNING Wind Moderate until ${hhmm} UTC`, 'Kans op windstoten tot 90 km/u', 'Relay of MeshSat test warning, issued 28 Oct 10:11 UTC via MeshSat, not an official NL-Alert']
    ])
    assert.ok(!banner.textContent.includes('Thunderstorm'), 'an expired warning is on the banner')
    assert.ok(banner.querySelector('a').getAttribute('href').endsWith('/warnings'), 'the banner does not open the page')
  } finally {
    app.unmount()
  }
})

test('the_words_and_the_in_force_rule_are_the_relays', () => {
  assert.equal(headline(live), `TEST WARNING Wind Moderate until ${hhmm} UTC`)
  assert.equal(headline({ ...live, test: false, msg_type: 'Update' }), `UPDATE: WARNING Wind Moderate until ${hhmm} UTC`)
  assert.equal(headline(cancel), 'CANCELLED: Rain')
  assert.equal(headline({ ...live, test: false, expires: '' }), 'WARNING Wind Moderate')
  assert.equal(label(expired), 'Relay of DWD warning, issued 20 Oct 06:00 UTC via MeshSat, not an official NL-Alert')
  assert.equal(expiry(cancel), null)
  assert.equal(expiry({ expires: null }), null)
  assert.ok(expiry(live) instanceof Date)
  const now = Date.now()
  assert.equal(isInForce(live, now), true)
  assert.equal(isInForce(expired, now), false)
  assert.equal(isInForce(cancelled, now), false)
  assert.equal(isInForce(cancel, now), false)
  // No expiry: 24 hours after it was sent, as warnings.NoExpiryInForce.
  const sent = new Date(now - HOUR).toISOString()
  assert.equal(isInForce({ ...live, expires: '', sent }, now), true)
  assert.equal(isInForce({ ...live, expires: '0001-01-01T00:00:00Z', sent }, now + NO_EXPIRY_IN_FORCE_MS), false)
})
