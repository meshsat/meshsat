// The compose and delivery-status views under test: the real components with
// the real store and API client in jsdom. Only HTTP answers (serve), timers
// (a queue the test runs with tick) and confirm() are controlled. Moved here
// from the scratch harness the B10 reviews ran on. The bridge's event stream
// is a fake EventSource the test speaks through (streams). [MESHSAT-1584]
import assert from 'node:assert/strict'
import { createApp, nextTick } from 'vue'
import { createPinia } from 'pinia'
import { afterEach, beforeEach } from 'vitest'
import ComposeView from '@/views/ComposeView.vue'
import IntentStatus from '@/components/IntentStatus.vue'

export const timers = new Map()
export const streams = []

// FakeEventSource is the page's /api/events stream: emit sends an event,
// reopen is the browser reconnecting.
class FakeEventSource {
  constructor(url) {
    this.url = url
    this.closed = false
    this.onmessage = null
    this.onopen = null
    streams.push(this)
  }
  close() { this.closed = true }
  emit(event) { if (!this.closed && this.onmessage) this.onmessage({ data: JSON.stringify(event) }) }
  reopen() { if (!this.closed && this.onopen) this.onopen() }
}

// openStreams are the streams the page holds open now.
export function openStreams() { return streams.filter(s => !s.closed) }
let serial = 0
let app = null
let api = null
const saved = {}

// useHarness installs the controlled globals around every test of a file.
export function useHarness() {
  beforeEach(() => {
    for (const k of ['setTimeout', 'clearTimeout', 'fetch', 'confirm', 'EventSource']) saved[k] = globalThis[k]
    timers.clear()
    streams.length = 0
    serial = 0
    api = null
    globalThis.setTimeout = (fn, ms) => { const id = ++serial; timers.set(id, { fn, ms }); return id }
    globalThis.clearTimeout = id => timers.delete(id)
    globalThis.fetch = (...args) => api(...args)
    globalThis.confirm = () => true
    globalThis.EventSource = FakeEventSource
    document.body.innerHTML = '<div id="app"></div>'
  })
  afterEach(() => {
    unmount()
    timers.clear()
    for (const k of Object.keys(saved)) globalThis[k] = saved[k]
  })
}

// serve answers every request of the test with fn(url, opts).
export function serve(fn) { api = fn }

export function mountRoot(component, props) {
  app = createApp(component, props)
  app.use(createPinia())
  app.mount('#app')
  return app
}

export function unmount() {
  if (app) app.unmount()
  app = null
}

export function mountCompose() { return mountRoot(ComposeView) }

export function mountStatus(props) { return mountRoot(IntentStatus, props) }

export const contacts = [
  { id: 'alice', display_name: 'Alice', trust_level: 3, team: 'red', addresses: [{ kind: 'MESHTASTIC', value: '!00000001' }] },
  { id: 'bob', display_name: 'Bob', trust_level: 3, team: 'red', addresses: [{ kind: 'MESHTASTIC', value: '!00000002' }] }
]

export function json(body, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

export function intent(id, contact = 'alice', extra = {}) {
  return {
    id, contact_id: contact, text: `${contact} message`, state: 'active', outcome: 'pending', label: 'sending',
    currency: 'EUR', incurred_minor: 0, possible_minor: 0, reserved_minor: 0,
    created_at: '2026-10-17T11:30:00Z',
    routes: [{ route_index: 1, kind: 'MESHTASTIC', destination: contact, admitted: true, paid: false, facts: [] }],
    ...extra
  }
}

export async function flush() {
  for (let i = 0; i < 12; i++) { await Promise.resolve(); await nextTick() }
}

export function button(name) {
  const el = [...document.querySelectorAll('button')].find(e => e.textContent.trim() === name)
  assert.ok(el, `button ${name} missing`)
  return el
}

export async function input(el, value) {
  el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
  await flush()
}

export async function pick(name) {
  const field = document.querySelector('#contact-picker input')
  field.dispatchEvent(new FocusEvent('focus'))
  await input(field, name)
  const option = [...document.querySelectorAll('[role=option]')].find(e => e.textContent.includes(name))
  assert.ok(option, `contact ${name} missing`)
  option.click()
  await flush()
}

export async function send() {
  assert.equal(button('Send').disabled, false)
  button('Send').click()
  await flush()
}

// tick runs the oldest pending timer and returns its delay.
export async function tick() {
  const item = timers.entries().next().value
  assert.ok(item, 'no timer available')
  timers.delete(item[0])
  await item[1].fn()
  await flush()
  return item[1].ms
}

// startTick starts the oldest pending timer without waiting for it.
export async function startTick() {
  const item = timers.entries().next().value
  assert.ok(item, 'no timer available')
  timers.delete(item[0])
  item[1].fn()
  await flush()
}

export function label() {
  return document.querySelector('[data-role=label]').textContent.trim()
}

export function bodyText() {
  return document.body.textContent.replace(/\s+/g, ' ')
}

// failWith fails the test with the evidence when the condition does not hold.
export function failWith(condition, message, evidence) {
  if (!condition) throw new Error(`${message}: ${JSON.stringify(evidence)}`)
}
