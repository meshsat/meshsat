// The B10 review's UI cases (review of 9276937, 17 Oct 2026) as supplied,
// then the other cases it asked for: the retry key, the history bound to the
// picked contact, the status card's polling. [MESHSAT-1584]
import assert from 'node:assert/strict'
import { expect, test } from 'vitest'
import {
  bodyText, button, contacts, failWith, flush, input, intent, json, label, mountCompose, mountStatus,
  pick, send, serve, tick, timers, useHarness
} from './harness.js'

useHarness()

async function retryCase(firstFailure) {
  const keys = []
  const committed = new Set()
  serve(async (url, opts) => {
    const u = new URL(url, 'http://review.local')
    if (u.pathname === '/api/directory/contacts') return json(contacts)
    if (u.pathname === '/api/deliveries/intents') return json([])
    if (u.pathname.startsWith('/api/deliveries/intents/')) return json(intent('reply'))
    assert.equal(u.pathname, '/api/messages/send-to-contact')
    assert.equal(opts.headers['X-MeshSat-Client'], 'spa')
    const key = opts.headers['Idempotency-Key']
    assert.match(key, /^spa-[0-9a-f]{32}$/)
    keys.push(key)
    committed.add(key)
    if (keys.length === 1) {
      if (firstFailure === 'network') throw new TypeError('network response lost')
      return json({ error: 'gateway did not receive upstream response' }, 504)
    }
    return json({ queued: true, intent_ids: ['reply'], per_bearer: [], existing: committed.size === 1 })
  })
  mountCompose()
  await flush()
  await pick('Alice')
  await input(document.querySelector('textarea'), 'unchanged request')
  await send()
  await send()
  failWith(keys[0] === keys[1], 'unchanged ambiguous retry minted a new key', { keys, committed_operations: committed.size })
}

test('network_loss_reuses_key_control', () => retryCase('network'))
test('http_504_reuses_key', () => retryCase('504'))

test('successful_send_starts_new_key_control', async () => {
  const keys = []
  serve(async (url, opts) => {
    const u = new URL(url, 'http://review.local')
    if (u.pathname === '/api/directory/contacts') return json(contacts)
    if (u.pathname === '/api/deliveries/intents') return json([])
    if (u.pathname.startsWith('/api/deliveries/intents/')) return json(intent('reply'))
    keys.push(opts.headers['Idempotency-Key'])
    return json({ queued: true, intent_ids: ['reply'], per_bearer: [] })
  })
  mountCompose()
  await flush()
  await pick('Alice')
  for (let i = 0; i < 2; i++) {
    await input(document.querySelector('textarea'), 'intentional second message')
    await send()
  }
  assert.notEqual(keys[0], keys[1])
})

test('recent_history_remains_bound_to_selected_contact', async () => {
  let releaseAlice
  const old = new Promise(resolve => { releaseAlice = resolve })
  const cancelled = []
  serve(async (url) => {
    const u = new URL(url, 'http://review.local')
    if (u.pathname === '/api/directory/contacts') return json(contacts)
    if (u.pathname === '/api/deliveries/intents') {
      if (u.searchParams.get('contact') === 'alice') return old
      return json([intent('bob-intent', 'bob')])
    }
    if (u.pathname.endsWith('/cancel')) {
      cancelled.push(u.pathname)
      return json(intent('alice-intent', 'alice', { state: 'closed', outcome: 'cancelled', label: 'cancelled' }))
    }
    return json(intent('alice-intent'))
  })
  mountCompose()
  await flush()
  await pick('Alice')
  await pick('Bob')
  assert.match(document.body.textContent, /bob message/)
  releaseAlice(json([intent('alice-intent')]))
  await flush()
  const body = bodyText()
  const stale = [...document.querySelectorAll('button')].find(e => e.textContent.includes('alice message'))
  if (stale) {
    stale.click()
    await flush()
    button('Cancel').click()
    await flush()
  }
  const evidence = {
    selected: document.querySelector('#contact-picker input').value,
    displayed_alice_under_bob: body.includes('Recent to Bob') && body.includes('alice message'), cancelled
  }
  failWith(!evidence.displayed_alice_under_bob && cancelled.length === 0, 'old history replaced the selected contact history and exposed the old cancel action', evidence)
})

test('active_poll_then_closed_control', async () => {
  let reads = 0
  serve(async () => json(intent('normal', 'alice', ++reads === 1 ? {} : { state: 'closed', outcome: 'accepted_unconfirmed', label: 'accepted, not confirmed' })))
  mountStatus({ intentId: 'normal' })
  await flush()
  assert.equal(label(), 'sending')
  const interval = await tick()
  assert.equal(interval, 2000)
  assert.equal(label(), 'accepted, not confirmed')
  assert.equal(timers.size, 0)
})

// A card closed by a cancel, with no send of its own in flight, stops
// checking; a closed card follows a send that is still settling (below).
test('cancelled_in_flight_status_observation', async () => {
  let phase = 'active'
  function current() {
    return intent('paid', 'alice', {
      state: phase === 'active' ? 'active' : 'closed',
      outcome: phase === 'settled' ? 'accepted_unconfirmed' : phase === 'active' ? 'pending' : 'may_have_been_sent',
      label: phase === 'settled' ? 'accepted, not confirmed' : phase === 'active' ? 'sending' : 'may have been sent',
      possible_minor: phase === 'settled' ? 0 : 17,
      incurred_minor: phase === 'settled' ? 17 : 0,
      notes: phase === 'settled' ? [] : ['may still be sent on cellular_0'],
      routes: [{ route_index: 1, kind: 'SMS', destination: '+31600000001', admitted: true, paid: true, facts: [] }]
    })
  }
  serve(async (url, opts) => {
    if (opts.method === 'POST') phase = 'cancelled'
    return json(current())
  })
  mountStatus({ intentId: 'paid' })
  await flush()
  button('Cancel').click()
  await flush()
  await tick()
  phase = 'settled'
  assert.equal(timers.size, 0)
  assert.equal(label(), 'may have been sent')
})

async function retryStatus(first, want) {
  const keys = []
  serve(async (url, opts) => {
    const u = new URL(url, 'http://review.local')
    if (u.pathname === '/api/directory/contacts') return json(contacts)
    if (u.pathname === '/api/deliveries/intents') return json([])
    if (u.pathname.startsWith('/api/deliveries/intents/')) return json(intent('reply'))
    keys.push(opts.headers['Idempotency-Key'])
    if (keys.length === 1) return first()
    return json({ queued: true, intent_ids: ['reply'], per_bearer: [] })
  })
  mountCompose()
  await flush()
  await pick('Alice')
  await input(document.querySelector('textarea'), 'same text')
  await send()
  const shown = bodyText()
  await send()
  const evidence = { keys, unresolved_text_shown: /Not known whether it was queued/.test(shown) }
  failWith(keys[0] === keys[1], 'the unchanged message got a new key', evidence)
  if (want === 'unresolved') failWith(evidence.unresolved_text_shown, 'the operator was not told the first send is unresolved', evidence)
  if (want === 'definitive') failWith(!evidence.unresolved_text_shown, 'a definitive refusal was shown as unresolved', evidence)
}

test('http_502_reuses_key', () => retryStatus(() => json({ error: 'bad gateway' }, 502), 'unresolved'))
test('http_500_reuses_key', () => retryStatus(() => json({ error: 'internal' }, 500), 'unresolved'))
test('http_503_from_a_proxy_reuses_key', () => retryStatus(() => new Response('<html>unavailable</html>', { status: 503 }), 'unresolved'))
test('interrupted_response_body_reuses_key', () => retryStatus(() => { throw new TypeError('body stream interrupted') }, 'unresolved'))
// A refusal that wrote nothing keeps the key for the unchanged message
// (review of ed8dbaa, B10-R1): a retry under it is safe, and only a
// success starts a new one.
test('bridge_503_nothing_queued_keeps_key', () => retryStatus(() => json({ queued: false, per_bearer: [{ kind: 'MESHTASTIC', error: 'mesh offline' }] }, 503), 'definitive'))
test('http_422_no_route_keeps_key', () => retryStatus(() => json({ queued: false, per_bearer: [{ kind: 'MESHTASTIC', error: 'not used: skipped_offline' }] }, 422), 'definitive'))

// historyOrder holds every history read until the case answers it, by
// contact and read number (alice1, bob2, ...).
async function historyOrder(script) {
  const pending = {}
  const cancelled = []
  let n = 0
  serve(async (url) => {
    const u = new URL(url, 'http://review.local')
    if (u.pathname === '/api/directory/contacts') return json(contacts)
    if (u.pathname === '/api/deliveries/intents') {
      const c = u.searchParams.get('contact')
      const key = `${c}${++n}`
      return new Promise((resolve, reject) => { pending[key] = { resolve, reject, c } })
    }
    if (u.pathname.endsWith('/cancel')) { cancelled.push(u.pathname); return json(intent('x', 'alice', { state: 'closed' })) }
    return json(intent(u.pathname.split('/').pop(), u.pathname.includes('bob') ? 'bob' : 'alice'))
  })
  mountCompose()
  await flush()
  await script({ pending, cancelled })
}

function shownHistory() {
  const body = bodyText()
  const m = body.match(/Recent to (\w+)/)
  return { heading: m ? m[1] : '', alice: body.includes('alice message'), bob: body.includes('bob message') }
}

test('history_fast_alice_slow_bob', () => historyOrder(async ({ pending }) => {
  await pick('Alice'); pending.alice1.resolve(json([intent('alice-intent', 'alice')])); await flush()
  await pick('Bob')
  let h = shownHistory()
  failWith(!h.alice, 'Alice history still shown after picking Bob', h)
  pending.bob2.resolve(json([intent('bob-intent', 'bob')])); await flush()
  h = shownHistory()
  failWith(h.heading === 'Bob' && h.bob && !h.alice, 'Bob history not shown alone', h)
}))

test('history_rapid_a_b_a_with_late_first_answer', () => historyOrder(async ({ pending }) => {
  await pick('Alice'); await pick('Bob'); await pick('Alice')
  pending.bob2.resolve(json([intent('bob-intent', 'bob')])); await flush()
  pending.alice3.resolve(json([intent('alice-new', 'alice', { text: 'alice newest' })])); await flush()
  pending.alice1.resolve(json([intent('alice-old', 'alice', { text: 'alice stale' })])); await flush()
  const body = bodyText()
  const h = { ...shownHistory(), newest: body.includes('alice newest'), stale: body.includes('alice stale') }
  failWith(h.heading === 'Alice' && h.newest && !h.stale && !h.bob, 'a stale answer replaced the current history', h)
}))

test('history_stale_error_does_not_clear_the_current', () => historyOrder(async ({ pending }) => {
  await pick('Alice'); await pick('Bob')
  pending.bob2.resolve(json([intent('bob-intent', 'bob')])); await flush()
  pending.alice1.reject(new TypeError('network gone')); await flush()
  const h = shownHistory()
  failWith(h.heading === 'Bob' && h.bob, "an earlier contact's error cleared the current history", h)
}))

test('status_card_names_its_recipient', async () => {
  const closed = { state: 'closed', outcome: 'acked_station', label: 'acked by station (unauthenticated)' }
  serve(async (url) => {
    const u = new URL(url, 'http://review.local')
    if (u.pathname === '/api/directory/contacts') return json(contacts)
    if (u.pathname === '/api/deliveries/intents') return json([intent('alice-intent', 'alice', closed)])
    return json(intent('alice-intent', 'alice', closed))
  })
  mountCompose()
  await flush()
  await pick('Alice')
  const entry = [...document.querySelectorAll('button')].find(e => e.textContent.includes('alice message'))
  entry.click(); await flush()
  const r = document.querySelector('[data-intent] [data-role=recipient]').textContent.replace(/\s+/g, ' ').trim()
  failWith(r.startsWith('to Alice'), 'the card does not name its recipient', { r })
})

test('closed_card_keeps_checking_while_a_send_settles', async () => {
  let phase = 'active'
  const tx = s => ({ id: 1, gen: 1, state: s, outcome_class: s === 'done' ? 'accepted' : '', charged_minor: s === 'done' ? 17 : 0 })
  function current() {
    return intent('paid', 'alice', {
      state: phase === 'active' ? 'active' : 'closed',
      outcome: phase === 'settled' ? 'accepted_unconfirmed' : phase === 'active' ? 'pending' : 'may_have_been_sent',
      label: phase === 'settled' ? 'accepted, not confirmed' : phase === 'active' ? 'sending' : 'may have been sent',
      possible_minor: phase === 'settled' ? 0 : 17, incurred_minor: phase === 'settled' ? 17 : 0,
      routes: [{
        route_index: 1, kind: 'SMS', destination: '+31600000001', admitted: true, paid: true, facts: [],
        attempts: [{ delivery_id: 9, round: 1, attempt_no: 1, status: 'sending', transmissions: [tx(phase === 'settled' ? 'done' : 'handed_off')] }]
      }]
    })
  }
  serve(async (url, opts) => { if (opts.method === 'POST') phase = 'cancelled'; return json(current()) })
  mountStatus({ intentId: 'paid' })
  await flush()
  button('Cancel').click(); await flush()
  await tick() // the active phase's timer: the card reads the closed, still settling intent
  const closedLabel = label()
  const recheck = [...timers.values()].map(t => t.ms)
  phase = 'settled'
  if (recheck.length) await tick()
  const ev = {
    closed_label: closedLabel, recheck_ms: recheck, displayed: label(),
    cost: document.querySelector('[data-role=money]').textContent.trim(), timers_after_settle: timers.size
  }
  failWith(closedLabel === 'may have been sent' && recheck.length === 1 && recheck[0] === 10000 &&
    ev.displayed === 'accepted, not confirmed' && ev.timers_after_settle === 0, 'the closed card did not follow the settling send', ev)
})

test('closed_card_refresh_and_fetch_on_open', async () => {
  let gets = 0
  let shown = 'accepted, not confirmed'
  serve(async () => {
    gets++
    return json(intent('done', 'alice', { state: 'closed', outcome: shown === 'accepted, not confirmed' ? 'accepted_unconfirmed' : 'delivered_device', label: shown }))
  })
  mountStatus({ intentId: 'done', initial: intent('done', 'alice', { state: 'closed', outcome: 'may_have_been_sent', label: 'may have been sent' }) })
  await flush()
  const afterOpen = label()
  shown = 'delivered to device'
  document.querySelector('[data-role=refresh]').click(); await flush()
  const ev = { gets, after_open: afterOpen, after_refresh: label(), timers: timers.size }
  failWith(gets === 2 && afterOpen === 'accepted, not confirmed' && ev.after_refresh === 'delivered to device', 'a closed card was not fetched on open or on Refresh', ev)
})

test('no_team_send_offered', async () => {
  serve(async (url) => { const u = new URL(url, 'http://review.local'); if (u.pathname === '/api/directory/contacts') return json(contacts); return json([]) })
  mountCompose()
  await flush()
  expect(document.body.textContent).not.toMatch(/Whole team/)
})
