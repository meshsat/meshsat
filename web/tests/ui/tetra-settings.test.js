// Settings > TETRA against controlled API answers: no radio, the radio's
// status, saving the settings and a test send through the ledger.
// [MESHSAT-1588]
import assert from 'node:assert/strict'
import { test } from 'vitest'
import TetraSettings from '@/components/TetraSettings.vue'
import { bodyText, button, flush, input, json, mountRoot, serve, tick, useHarness } from './harness.js'

useHarness()

const status = {
  configured: true,
  gateway: {
    state: 'connected', port: '/dev/ttyUSB1', mode: 'dmo', group: '2000', self: '100', destination: '2000',
    battery_pct: 100, max_text_bytes: 130, messages_in: 3, messages_out: 9, errors: 0, reports: 0,
    refused_too_long: 0, non_text_in: 0,
    radio: { model: 'MTP3550 350-470MHz PTBP952HE', firmware: 'R35.000.9426', itsi: '7530236100000100', gtsi: '7530236100002000' },
    config: { mode: 'dmo', destination: '', prefix: '', port: '' }
  }
}

test('no_radio_says_how_to_add_one', async () => {
  serve(async () => json({ configured: false }))
  mountRoot(TetraSettings)
  await flush()
  assert.match(bodyText(), /No TETRA radio on this Bridge/)
  assert.equal(document.querySelector('[data-testid="tetra-save"]'), null)
})

test('status_shows_the_radio_and_the_measured_limit', async () => {
  serve(async () => json(status))
  mountRoot(TetraSettings)
  await flush()
  const text = bodyText()
  for (const want of ['connected', 'MTP3550', 'R35.000.9426', 'Direct Mode (DMO)', '2000 / 100', '100%', '130 bytes (126 Latin-1 characters)', '3 / 9']) {
    assert.ok(text.includes(want), `missing ${want}`)
  }
})

test('save_puts_the_gateway_config', async () => {
  const puts = []
  serve(async (url, opts) => {
    const u = new URL(url, 'http://review.local')
    if (opts?.method === 'PUT') {
      puts.push({ path: u.pathname, body: JSON.parse(opts.body) })
      return json({ status: 'ok' })
    }
    return json(status)
  })
  mountRoot(TetraSettings)
  await flush()
  const mode = document.querySelector('[data-testid="tetra-mode"]')
  mode.value = 'tmo'
  mode.dispatchEvent(new Event('change', { bubbles: true }))
  await input(document.querySelector('[data-testid="tetra-destination"]'), ' 2001 ')
  button('Save TETRA settings').click()
  await flush()
  assert.deepEqual(puts, [{ path: '/api/gateways/tetra', body: { enabled: true, config: { mode: 'tmo', destination: '2001', prefix: '', port: '' } } }])
  assert.match(bodyText(), /Saved/)
})

test('send_queues_and_follows_the_delivery', async () => {
  const posts = []
  serve(async (url, opts) => {
    const u = new URL(url, 'http://review.local')
    if (u.pathname === '/api/tetra/send') {
      posts.push(JSON.parse(opts.body))
      return json({ status: 'queued', delivery_id: 2301, to: 'tetra:2000' }, 202)
    }
    if (u.pathname === '/api/deliveries/2301') return json({ id: 2301, status: 'delivered' })
    return json(status)
  })
  mountRoot(TetraSettings)
  await flush()
  await input(document.querySelector('[data-testid="tetra-text"]'), 'hello over DMO')
  assert.match(bodyText(), /18 bytes, one SDS/)
  button('Send over TETRA').click()
  await flush()
  assert.deepEqual(posts, [{ text: 'hello over DMO' }])
  assert.match(bodyText(), /Queued as delivery 2301 to tetra:2000/)
  await tick() // the first poll of the delivery
  assert.match(bodyText(), /Delivery 2301 to tetra:2000: delivered/)
})
