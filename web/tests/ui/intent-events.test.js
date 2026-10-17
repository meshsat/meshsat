// The delivery status card follows the bridge's event stream (event-driven
// status refresh, tracked since the B10 reviews): an event naming its
// intent reads it again at once, a closed card that stopped polling still
// shows a late answer, events during a read give one more read, one stream
// serves every card, and a reconnect reads every watched card again.
// [MESHSAT-1584]
import assert from 'node:assert/strict'
import { h } from 'vue'
import { test } from 'vitest'
import IntentStatus from '@/components/IntentStatus.vue'
import {
  failWith, flush, intent, json, label, mountRoot, mountStatus, openStreams, serve, streams, timers, unmount, useHarness
} from './harness.js'

useHarness()

const written = id => ({ type: 'delivery_intent', message: '', data: { intent_id: id, change: 'written' } })
const attemptEnd = id => ({ type: 'delivery_attempt', message: '', data: { id: 7, intent_id: id, status: 'accepted' } })
const settled = [{
  route_index: 1, kind: 'MESHTASTIC', admitted: true, paid: false, facts: [],
  attempts: [{ delivery_id: 1, transmissions: [{ id: 1, state: 'done', outcome_class: 'accepted' }] }]
}]

test('an_event_for_the_intent_reads_it_again_at_once', async () => {
  let reads = 0
  serve(async () => json(intent('x', 'alice', ++reads === 1 ? {} : { state: 'closed', outcome: 'acked_station', label: 'acked by station (unauthenticated)' })))
  mountStatus({ intentId: 'x' })
  await flush()
  assert.equal(label(), 'sending')
  assert.equal(openStreams().length, 1)
  assert.equal(openStreams()[0].url, '/api/events')
  openStreams()[0].emit(written('x'))
  await flush()
  const ev = { reads, label: label() }
  failWith(reads === 2 && ev.label === 'acked by station (unauthenticated)', 'the card waited for its poll instead of reading on the event', ev)
})

test('an_attempt_event_reads_it_again_too', async () => {
  let reads = 0
  serve(async () => { reads++; return json(intent('x')) })
  mountStatus({ intentId: 'x' })
  await flush()
  openStreams()[0].emit(attemptEnd('x'))
  await flush()
  failWith(reads === 2, 'a delivery_attempt event naming the intent was ignored', { reads })
})

test('an_event_for_another_intent_reads_nothing', async () => {
  let reads = 0
  serve(async () => { reads++; return json(intent('x')) })
  mountStatus({ intentId: 'x' })
  await flush()
  openStreams()[0].emit(written('other'))
  openStreams()[0].emit({ type: 'message', data: { intent_id: 'x' } })
  await flush()
  failWith(reads === 1, 'an event about something else made the card read', { reads })
})

test('a_closed_card_that_stopped_polling_shows_a_late_answer', async () => {
  let shown = { outcome: 'accepted_unconfirmed', label: 'accepted, not confirmed' }
  serve(async () => json(intent('done', 'alice', { state: 'closed', routes: settled, ...shown })))
  mountStatus({ intentId: 'done' })
  await flush()
  failWith(timers.size === 0 && label() === 'accepted, not confirmed', 'the settled card is still polling', { timers: timers.size })
  shown = { outcome: 'delivered_device', label: 'delivered to device' } // T14: a late ACK upgraded it
  openStreams()[0].emit(written('done'))
  await flush()
  const ev = { label: label(), timers: timers.size }
  failWith(ev.label === 'delivered to device' && ev.timers === 0, 'a late answer to a closed intent did not show', ev)
})

test('events_during_a_read_give_one_more_read_and_the_newest_wins', async () => {
  let reads = 0
  let release
  serve(async () => {
    reads++
    if (reads === 1) return new Promise(r => { release = r })
    return json(intent('x', 'alice', { state: 'closed', routes: settled, outcome: 'delivered_device', label: 'delivered to device' }))
  })
  mountStatus({ intentId: 'x' })
  await flush()
  for (let i = 0; i < 3; i++) openStreams()[0].emit(written('x'))
  await flush()
  const whileOut = reads
  release(json(intent('x'))) // the first read's answer, from before the events
  await flush()
  await flush() // and the read that follows it
  const ev = { reads_while_out: whileOut, reads, label: label() }
  failWith(whileOut === 1 && reads === 2 && ev.label === 'delivered to device', 'events during a read were not folded into one more read', ev)
})

test('one_stream_serves_every_card_and_closes_with_the_last', async () => {
  const reads = { a: 0, b: 0 }
  serve(async (url) => { const id = url.split('/').pop(); reads[id]++; return json(intent(id)) })
  mountRoot({ render: () => [h(IntentStatus, { intentId: 'a' }), h(IntentStatus, { intentId: 'b' })] })
  await flush()
  failWith(streams.length === 1, 'each card opened its own stream', { streams: streams.length })
  openStreams()[0].emit(written('b'))
  await flush()
  failWith(reads.a === 1 && reads.b === 2, 'the event reached the wrong card', reads)
  unmount()
  failWith(openStreams().length === 0, 'the stream stayed open after the last card went', { open: openStreams().length })
})

test('a_reconnect_reads_every_watched_card_again', async () => {
  let reads = 0
  serve(async () => { reads++; return json(intent('done', 'alice', { state: 'closed', routes: settled, outcome: 'accepted_unconfirmed', label: 'accepted, not confirmed' })) })
  mountStatus({ intentId: 'done' })
  await flush()
  openStreams()[0].reopen() // the first open: nothing was missed
  await flush()
  const afterFirst = reads
  openStreams()[0].reopen() // a reconnect: events in the gap are lost
  await flush()
  failWith(afterFirst === 1 && reads === 2, 'a reconnect did not read the card again', { after_first_open: afterFirst, reads })
})

test('no_event_stream_leaves_the_polls', async () => {
  globalThis.EventSource = undefined
  let reads = 0
  serve(async () => json(intent('x', 'alice', ++reads === 1 ? {} : { state: 'closed', outcome: 'accepted_unconfirmed', label: 'accepted, not confirmed' })))
  mountStatus({ intentId: 'x' })
  await flush()
  failWith(timers.size === 1 && label() === 'sending', 'a browser without EventSource lost the card', { timers: timers.size })
})
