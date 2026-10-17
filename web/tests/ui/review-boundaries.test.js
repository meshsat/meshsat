// The review of b833dd9 (17 Oct 2026 22:03 CEST), EVT-R1, its cases as
// supplied: a settled card recovers from a change before the stream's
// first open, a failed read after an event, and a lost event. [MESHSAT-1584]
import assert from 'node:assert/strict'
import { test } from 'vitest'
import {
  bodyText, flush, intent, json, label, mountStatus, openStreams, reconciling, serve, timers, useHarness
} from './harness.js'

useHarness()

const before = () => intent('closed', 'alice', {
  state: 'closed', outcome: 'accepted_unconfirmed', label: 'accepted, not confirmed', routes: []
})
const after = () => intent('closed', 'alice', {
  state: 'closed', outcome: 'delivered_device', label: 'delivered to device', routes: []
})
const event = { type: 'delivery_intent', data: { intent_id: 'closed', transition: 'T14' } }

test('review_first_open_reconciles_a_change_after_the_initial_snapshot', async () => {
  let reads = 0
  let current = before()
  serve(async () => { reads++; return json(current) })
  mountStatus({ intentId: 'closed' })
  await flush()
  assert.equal(label(), 'accepted, not confirmed')
  // As supplied this line was assert.equal(timers.size, 0), the defect's
  // state; with the fix the settled card keeps one check a minute, and
  // nothing faster.
  assert.ok(reconciling(), JSON.stringify([...timers.values()]))
  current = after()
  openStreams()[0].reopen()
  await flush()
  const evidence = { reads, label: label(), timers: timers.size }
  console.log(JSON.stringify({ scenario: 'first_open', ...evidence }))
  assert.equal(label(), 'delivered to device', JSON.stringify(evidence))
})

test('review_a_failed_event_read_on_a_closed_card_schedules_recovery', async () => {
  let reads = 0
  let current = before()
  let failNext = false
  serve(async () => {
    reads++
    if (failNext) { failNext = false; return json({ error: 'temporary unavailable' }, 503) }
    return json(current)
  })
  mountStatus({ intentId: 'closed' })
  await flush()
  openStreams()[0].reopen()
  await flush()
  current = after()
  failNext = true
  openStreams()[0].emit(event)
  await flush()
  const evidence = { reads, label: label(), timers: timers.size, error_shown: bodyText().includes('temporary unavailable') }
  console.log(JSON.stringify({ scenario: 'failed_event_read', ...evidence }))
  assert.ok(timers.size > 0, JSON.stringify(evidence))
})

test('review_a_closed_card_has_reconciliation_if_its_event_is_dropped', async () => {
  let reads = 0
  let current = before()
  serve(async () => { reads++; return json(current) })
  mountStatus({ intentId: 'closed' })
  await flush()
  openStreams()[0].reopen()
  await flush()
  current = after()
  openStreams()[0].emit({ type: 'packet', data: {} })
  await flush()
  const evidence = { reads, label: label(), timers: timers.size, stream_open: openStreams().length }
  console.log(JSON.stringify({ scenario: 'dropped_event', ...evidence }))
  assert.ok(timers.size > 0 || label() === 'delivered to device', JSON.stringify(evidence))
})
