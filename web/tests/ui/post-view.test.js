// The Community Post's operator screen against controlled API answers: the
// post's state and its close, reopen and purge buttons, and the printer line.
// [MESHSAT-1733]
import assert from 'node:assert/strict'
import { test } from 'vitest'
import PostView from '@/views/PostView.vue'
import { button, flush, json, mountRoot, serve, useHarness } from './harness.js'

useHarness()

function bodyText() { return document.body.textContent.replace(/\s+/g, ' ') }

const open = {
  enabled: true, listen: '0.0.0.0:8081', portal_url: 'http://10.44.0.1/', bearer: 'auto', max_chars: 150, max_bytes: 240,
  board_days: 30, auto_approve: false, posts_per_hour: 3, posts_per_day: 10, operator: 'Gemeente Leiden',
  purge_days: 30, closed: false, print: false, printer: { print: false, heard: false }
}
const queue = { pending: [], recent: [], counts: {} }

function api(config, calls) {
  return async (url, opts) => {
    const u = new URL(url, 'http://review.local')
    if (opts?.method === 'POST') {
      calls.push({ path: u.pathname, body: opts.body ? JSON.parse(opts.body) : null })
      if (u.pathname.endsWith('/close')) return json({ closed: true, closed_at: '2026-10-28T12:00:00Z', purge_at: '2026-11-27T12:00:00Z' })
      if (u.pathname.endsWith('/open')) return json({ closed: false })
      if (u.pathname.endsWith('/purge')) return json({ purged: { messages: 2, replies: 1, board: 3, sessions: 4 }, state: { closed: false } })
    }
    if (u.pathname.endsWith('/post/config')) return json(config())
    return json(queue)
  }
}

test('an_open_post_offers_close_and_purge_and_says_slips_are_off', async () => {
  const calls = []
  serve(api(() => open, calls))
  mountRoot(PostView)
  await flush()
  assert.match(bodyText(), /Open\. Closing it stops new messages/)
  assert.match(document.querySelector('[data-testid="post-printer"]').textContent, /Slips: off \(MESHSAT_POST_PRINT\)/)
  assert.equal(document.querySelector('[data-testid="post-closed"]'), null)
  button('Close the post').click()
  await flush(); await flush()
  assert.deepEqual(calls.map(c => c.path), ['/api/post/close'])
  button('Purge now').click()
  await flush(); await flush()
  assert.deepEqual(calls[1], { path: '/api/post/purge', body: { confirm: true } })
  assert.match(bodyText(), /Purged\. 2 messages, 1 replies, 3 board lines, 4 phones\./)
})

test('a_closed_post_shows_the_purge_day_and_offers_to_reopen', async () => {
  const calls = []
  let config = { ...open, closed: true, closed_at: '2026-10-28T12:00:00Z', purge_at: '2026-11-27T12:00:00Z' }
  serve(api(() => config, calls))
  mountRoot(PostView)
  await flush()
  assert.ok(document.querySelector('[data-testid="post-closed"]'), 'the closed banner is missing')
  assert.match(document.querySelector('[data-testid="post-state"]').textContent, /Closed since .*30 days after closing/)
  assert.equal([...document.querySelectorAll('button')].find(b => b.textContent.trim() === 'Close the post'), undefined)
  config = open
  button('Reopen the post').click()
  await flush(); await flush()
  assert.deepEqual(calls.map(c => c.path), ['/api/post/open'])
  assert.match(bodyText(), /The post is open again/)
  assert.equal(document.querySelector('[data-testid="post-closed"]'), null)
})

test('the_printer_line_follows_the_report', async () => {
  const heard = { ...open, print: true, printer: { print: true, heard: true, age_seconds: 12, report: { service: 'meshsat-receipt-printer', printer: '5A:4A:A1:18:4A:B9', count: 42, queued: 1, last_slip_at: '2026-10-28T12:00:00Z' } } }
  serve(api(() => heard, []))
  mountRoot(PostView)
  await flush()
  const line = document.querySelector('[data-testid="post-printer"]').textContent
  assert.match(line, /Slips: on, printer service heard 12 s ago, printer 5A:4A:A1:18:4A:B9, booth slips at #42, last slip .*, 1 waiting\./)
})

test('the_printer_line_says_when_no_service_reported', async () => {
  const silent = { ...open, print: true, printer: { print: true, heard: false } }
  serve(api(() => silent, []))
  mountRoot(PostView)
  await flush()
  assert.match(document.querySelector('[data-testid="post-printer"]').textContent, /Slips: on, but no printer service has reported/)
})

// A post that did not go out (the post was closed before it left, or the Hub
// could not send it) can be sent again from Recent; one that is on its way
// cannot. [MESHSAT-1807]
test('a_failed_post_can_be_sent_again_from_recent', async () => {
  const calls = []
  const recent = [
    { code: 'K7QW2X', recipient: '+31612345678', text: 'water at the north gate', status: 'failed' },
    { code: 'ABCDEF', recipient: '+31612345679', text: 'on my way', status: 'queued' }
  ]
  serve(async (url, opts) => {
    const u = new URL(url, 'http://review.local')
    if (opts?.method === 'POST') { calls.push(u.pathname); return json({}) }
    if (u.pathname.endsWith('/post/config')) return json(open)
    return json({ pending: [], recent, counts: { failed: 1, queued: 1 } })
  })
  mountRoot(PostView)
  await flush()
  const again = [...document.querySelectorAll('button')].filter(b => b.textContent.trim() === 'Send again')
  assert.equal(again.length, 1, 'only the failed post offers Send again')
  again[0].click()
  await flush(); await flush()
  assert.deepEqual(calls, ['/api/post/K7QW2X/approve'])
})

// The "Safe and well" board: the operator sees every line and takes one off
// after a confirm; the screen asks the Bridge with DELETE. [MESHSAT-1814]
test('the_board_lists_every_line_and_remove_deletes_one', async () => {
  const calls = []
  const lines = [
    { id: 7, name: 'Maria Papadaki', status: 'safe', place: 'the school', created_at: '2026-11-07T12:00:00Z', has_remove_code: true },
    { id: 5, name: 'Nikos Georgiou', status: 'looking for Maria', place: '', created_at: '2026-11-07T11:00:00Z', has_remove_code: false }
  ]
  serve(async (url, opts) => {
    const u = new URL(url, 'http://review.local')
    if (opts?.method === 'DELETE') { calls.push(u.pathname); return json({ status: 'removed' }) }
    if (u.pathname.endsWith('/post/config')) return json(open)
    if (u.pathname.endsWith('/post/board')) return json(lines)
    return json(queue)
  })
  mountRoot(PostView)
  await flush(); await flush(); await flush()
  const board = document.querySelector('[data-testid="post-board"]').textContent.replace(/\s+/g, ' ')
  assert.match(board, /Safe and well \(2\)/)
  assert.match(board, /Maria Papadaki/)
  assert.match(board, /the school/)
  assert.match(board, /Nikos Georgiou/)
  const remove = [...document.querySelectorAll('[data-testid="post-board"] button')].filter(b => b.textContent.trim() === 'Remove')
  assert.equal(remove.length, 2)
  remove[1].click()
  await flush(); await flush()
  assert.deepEqual(calls, ['/api/post/board/5'])
  assert.match(bodyText(), /"Nikos Georgiou" is off the board\./)
})
