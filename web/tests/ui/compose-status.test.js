// The UI cases the B10 closure reviews asked for (reviews of ed8dbaa and
// 5bef352, 17 Oct 2026): a key kept through later refusals, status replies
// kept in order, a partial send's error beside its queued row. [MESHSAT-1584]
import { test } from 'vitest'
import {
  bodyText, button, contacts, failWith, flush, input, intent, json, label, mountCompose, mountStatus,
  pick, send, serve, startTick, timers, useHarness
} from './harness.js'

useHarness()

for (const code of [401, 408, 429]) {
  test(`unresolved_then_${code}_keeps_original_key`, async () => {
    const keys = []
    const committed = new Set()
    serve(async (url, opts) => {
      const u = new URL(url, 'http://review.local')
      if (u.pathname === '/api/directory/contacts') return json(contacts)
      if (u.pathname === '/api/deliveries/intents') return json([])
      if (u.pathname.startsWith('/api/deliveries/intents/')) return json(intent('reply'))
      const key = opts.headers['Idempotency-Key']
      keys.push(key)
      if (keys.length === 2) return json({ error: `intermediary refused retry: ${code}` }, code)
      committed.add(key)
      if (keys.length === 1) return json({ error: 'origin committed, response missed' }, 504)
      return json({ queued: true, intent_ids: ['reply'], per_bearer: [] })
    })
    mountCompose()
    await flush()
    await pick('Alice')
    await input(document.querySelector('textarea'), 'unchanged request')
    await send()
    await send()
    await send()
    failWith(keys.every(k => k === keys[0]) && committed.size === 1, 'a later rejection cleared an earlier unresolved operation',
      { keys, committed_operations: committed.size })
  })
}

test('closed_status_refresh_replies_remain_ordered', async () => {
  let read = 0
  const settledRoutes = [{
    route_index: 1, kind: 'MESHTASTIC', admitted: true, paid: false, facts: [],
    attempts: [{ delivery_id: 1, transmissions: [{ id: 1, state: 'done', outcome_class: 'accepted' }] }]
  }]
  let releaseOld
  serve(async () => {
    read++
    if (read === 2) return new Promise(resolve => { releaseOld = resolve })
    return json(intent('done', 'alice', {
      state: 'closed', routes: settledRoutes, outcome: read === 1 ? 'accepted_unconfirmed' : 'delivered_device',
      label: read === 1 ? 'accepted, not confirmed' : 'delivered to device'
    }))
  })
  mountStatus({ intentId: 'done' })
  await flush()
  document.querySelector('[data-role=refresh]').click()
  await flush()
  document.querySelector('[data-role=refresh]').click()
  await flush()
  const newest = label()
  releaseOld(json(intent('done', 'alice', { state: 'closed', routes: settledRoutes, outcome: 'accepted_unconfirmed', label: 'accepted, not confirmed' })))
  await flush()
  const evidence = { newest, displayed_after_old_reply: label(), further_timers: timers.size, reads: read }
  failWith(evidence.displayed_after_old_reply === newest, 'an older completed response overwrote a newer terminal status', evidence)
})

test('poll_and_refresh_overlap_newest_wins', async () => {
  let read = 0
  let releaseFirstPoll
  const tx = s => [{ route_index: 1, kind: 'MESHTASTIC', admitted: true, paid: false, facts: [], attempts: [{ delivery_id: 1, transmissions: [{ id: 1, state: s }] }] }]
  serve(async () => {
    read++
    if (read === 1) return json(intent('x', 'alice', { state: 'closed', outcome: 'may_have_been_sent', label: 'may have been sent', routes: tx('handed_off') }))
    if (read === 2) return new Promise(r => { releaseFirstPoll = r }) // the 10 s poll, slow
    return json(intent('x', 'alice', { state: 'closed', outcome: 'delivered_device', label: 'delivered to device', routes: tx('done') }))
  })
  mountStatus({ intentId: 'x' })
  await flush()
  await startTick() // the poll starts and stays pending
  document.querySelector('[data-role=refresh]').click(); await flush() // a newer read answers
  const newest = label()
  releaseFirstPoll(json(intent('x', 'alice', { state: 'closed', outcome: 'may_have_been_sent', label: 'may have been sent', routes: tx('handed_off') })))
  await flush()
  const ev = { newest, after_old_poll: label(), timers: timers.size }
  failWith(ev.newest === 'delivered to device' && ev.after_old_poll === 'delivered to device' && ev.timers === 0, 'an older poll overwrote a newer Refresh', ev)
})

test('read_from_before_cancel_does_not_restore_active', async () => {
  let read = 0
  let releaseOld
  serve(async (url, opts) => {
    if (opts.method === 'POST') return json(intent('y', 'alice', { state: 'closed', outcome: 'cancelled', label: 'cancelled' }))
    read++
    if (read === 1) return json(intent('y'))
    if (read === 2) return new Promise(r => { releaseOld = r })
    return json(intent('y', 'alice', { state: 'closed', outcome: 'cancelled', label: 'cancelled' }))
  })
  mountStatus({ intentId: 'y' })
  await flush()
  await startTick() // poll read 2 stays pending
  button('Cancel').click(); await flush()
  releaseOld(json(intent('y'))) // the active snapshot from before the cancel
  await flush()
  const ev = { label: label(), cancel_button: [...document.querySelectorAll('button')].some(b => b.textContent.trim() === 'Cancel') }
  failWith(ev.label === 'cancelled' && !ev.cancel_button, 'a read from before Cancel brought the card back to active', ev)
})

test('partial_send_shows_the_error_beside_the_queued_row', async () => {
  serve(async (url) => {
    const u = new URL(url, 'http://review.local')
    if (u.pathname === '/api/directory/contacts') return json(contacts)
    if (u.pathname === '/api/deliveries/intents') return json([])
    return json({
      queued: true, strategy: 'ALL_BEARERS', precedence: 'Routine', submission_id: 's1',
      per_bearer: [{ kind: 'MESHTASTIC', delivery_ids: [7], error: 'mesh address "!zzzzzzzz" is not a node id (!xxxxxxxx)' }]
    }, 207)
  })
  mountCompose()
  await flush()
  await pick('Alice')
  await input(document.querySelector('textarea'), 'partial')
  await send()
  const row = bodyText()
  const shown = /queued #7/.test(row) && /not all: mesh address "!zzzzzzzz" is not a node id/.test(row)
  failWith(shown, 'a bearer with a queued row and a refused address showed only the row', { body: row.slice(0, 400) })
})

test('later_refusal_after_unresolved_says_still_unresolved', async () => {
  let n = 0
  serve(async (url) => {
    const u = new URL(url, 'http://review.local')
    if (u.pathname === '/api/directory/contacts') return json(contacts)
    if (u.pathname === '/api/deliveries/intents') return json([])
    n++
    if (n === 1) return json({ error: 'timeout' }, 504)
    return json({ error: 'too many requests' }, 429)
  })
  mountCompose()
  await flush()
  await pick('Alice')
  await input(document.querySelector('textarea'), 'unchanged')
  await send(); await send()
  const body = bodyText()
  failWith(/earlier send is still not known to have been queued \(now HTTP 429/.test(body), 'the operator was not told the earlier send is still unresolved', { body: body.slice(0, 300) })
})
