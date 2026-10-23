// Settings > Media Hopping against controlled API answers: off by default
// with no links, saving the switch, and the links with their state,
// re-admission dwell and repeat count while it watches. [MESHSAT-1650]
import assert from 'node:assert/strict'
import { test } from 'vitest'
import MediaHopSettings from '@/components/MediaHopSettings.vue'
import { bodyText, flush, json, mountRoot, serve, useHarness } from './harness.js'

useHarness()

const config = { trigger_window_s: 900, drop_dwell_s: 10, readmit_dwell_s: 600, readmit_max_s: 3600, flap_memory_s: 3600, radio_repeat_s: 60, silence_after_s: 600, min_set: 2 }

function hopset(mode, links = []) {
  return { mode, gating: mode === 'on', config, links }
}

const watching = hopset('watch', [
  { interface_id: 'mesh_0', channel_type: 'mesh', paid: false, state: 'excluded', usable: false, down: false, deaf: false, absent: false,
    retained: false, since: '2026-10-23T14:00:00Z', reason: 'delivery:!0000abcd, silence', readmit_after: '20m0s', flaps: 1,
    signs: [{ source: 'delivery', key: '!0000abcd', at: '2026-10-23T14:00:00Z' }, { source: 'silence', key: '-', at: '2026-10-23T14:00:00Z' }],
    last_positive: '2026-10-23T13:50:00Z' },
  { interface_id: 'aprs_0', channel_type: 'aprs', paid: false, state: 'admitted', usable: true, down: true, deaf: true, absent: false,
    retained: false, since: '2026-10-23T13:00:00Z', flaps: 0, signs: [] },
  { interface_id: 'tetra_0', channel_type: 'tetra', paid: false, state: 'excluded', usable: true, down: false, deaf: false, absent: false,
    retained: true, since: '2026-10-23T13:00:00Z', flaps: 0, signs: [] }
])

test('off_is_the_default_and_shows_no_links', async () => {
  serve(async url => {
    const u = new URL(url, 'http://review.local')
    return json(u.pathname.endsWith('/mode') ? { mode: 'off' } : hopset('off'))
  })
  mountRoot(MediaHopSettings)
  await flush()
  assert.equal(document.querySelector('[data-testid="hop-mode-off"]').checked, true)
  assert.match(bodyText(), /Off \(now\)/)
  assert.match(bodyText(), /routed exactly as before media hopping/)
  assert.equal(document.querySelector('[data-testid="hop-status"]'), null)
  assert.equal(document.querySelector('[data-testid="hop-save"]').disabled, true)
})

test('save_puts_the_mode', async () => {
  let current = 'off'
  const puts = []
  serve(async (url, opts) => {
    const u = new URL(url, 'http://review.local')
    if (opts?.method === 'PUT') {
      const body = JSON.parse(opts.body)
      puts.push({ path: u.pathname, body })
      current = body.mode
      return json({ mode: current })
    }
    return json(u.pathname.endsWith('/mode') ? { mode: current } : hopset(current))
  })
  mountRoot(MediaHopSettings)
  await flush()
  const watch = document.querySelector('[data-testid="hop-mode-watch"]')
  watch.checked = true
  watch.dispatchEvent(new Event('change', { bubbles: true }))
  await flush()
  document.querySelector('[data-testid="hop-save"]').click()
  await flush()
  assert.deepEqual(puts, [{ path: '/api/hopset/mode', body: { mode: 'watch' } }])
  assert.match(document.querySelector('[data-testid="hop-save-msg"]').textContent, /Media hopping is watch now/)
  assert.match(bodyText(), /Watch \(now\)/)
})

test('watch_shows_each_link_with_its_dwell_and_repeats', async () => {
  serve(async url => {
    const u = new URL(url, 'http://review.local')
    return json(u.pathname.endsWith('/mode') ? { mode: 'watch' } : watching)
  })
  mountRoot(MediaHopSettings)
  await flush()
  const mesh = document.querySelector('[data-testid="hop-link-mesh_0"]').textContent
  for (const want of ['excluded', 'delivery:!0000abcd, silence', '20m0s', '1']) {
    assert.ok(mesh.includes(want), `mesh_0 row missing ${want}: ${mesh}`)
  }
  assert.match(document.querySelector('[data-testid="hop-link-aprs_0"]').textContent, /no, deaf/)
  assert.match(document.querySelector('[data-testid="hop-link-tetra_0"]').textContent, /yes, kept by the floor/)
  assert.match(bodyText(), /watching: routing unchanged/)
  const cfgText = document.querySelector('[data-testid="hop-config"]').textContent
  for (const want of ['15 min', '10 s', '10 min', '60 min']) {
    assert.ok(cfgText.includes(want), `config missing ${want}`)
  }
})
