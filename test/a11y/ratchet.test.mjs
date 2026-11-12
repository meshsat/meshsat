// The ratchet's rules, without a browser: `node --test ratchet.test.mjs`.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { compare, nextBaseline, total, UNLABELLED } from './ratchet.mjs'

const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']
const base = (states) => ({ schema: 1, axe: '4.13.0', tags: TAGS, states })
const run = (states, extra = {}) => ({ axe: '4.13.0', tags: TAGS, states, ...extra })
const ok = (counts) => ({ status: 'ok', counts })

test('the same counts pass', () => {
  const r = compare(base({ a: { 'color-contrast': 3 } }), run({ a: ok({ 'color-contrast': 3 }) }))
  assert.equal(r.ok, true)
  assert.deepEqual(r.failures, [])
})

test('one more failing element fails', () => {
  const r = compare(base({ a: { 'color-contrast': 3 } }), run({ a: ok({ 'color-contrast': 4 }) }))
  assert.equal(r.ok, false)
  assert.deepEqual(r.failures, [{ kind: 'increase', state: 'a', rule: 'color-contrast', base: 3, now: 4 }])
})

test('a rise in one state fails even when the total falls', () => {
  const r = compare(base({ a: { label: 5 }, b: { label: 1 } }), run({ a: ok({ label: 0 }), b: ok({ label: 2 }) }))
  assert.equal(r.ok, false)
  assert.equal(r.failures[0].state, 'b')
})

test('a rule the baseline did not have fails', () => {
  const r = compare(base({ a: { label: 1 } }), run({ a: ok({ label: 1, 'select-name': 1 }) }))
  assert.equal(r.ok, false)
  assert.deepEqual(r.failures, [{ kind: 'new-rule', state: 'a', rule: 'select-name', base: 0, now: 1 }])
})

test('an unlabelled control counts like an axe finding', () => {
  const r = compare(base({ a: {} }), run({ a: ok({ [UNLABELLED]: 1 }) }))
  assert.equal(r.ok, false)
  assert.equal(r.failures[0].rule, UNLABELLED)
})

test('a new state with findings fails, a new clean state passes with a note', () => {
  assert.equal(compare(base({}), run({ n: ok({ label: 1 }) })).ok, false)
  const clean = compare(base({}), run({ n: ok({}) }))
  assert.equal(clean.ok, true)
  assert.deepEqual(clean.notes, [{ kind: 'new-clean-state', state: 'n' }])
})

test('a state the baseline has but the run lost fails, unless the run is partial', () => {
  assert.deepEqual(compare(base({ a: {}, gone: {} }), run({ a: ok({}) })).failures, [{ kind: 'missing', state: 'gone' }])
  assert.equal(compare(base({ a: {}, gone: {} }), run({ a: ok({}) }, { partial: true })).ok, true)
})

test('a state that errored fails', () => {
  const r = compare(base({ a: {} }), run({ a: { status: 'error', error: 'timeout' } }))
  assert.deepEqual(r.failures, [{ kind: 'error', state: 'a', now: 'timeout' }])
})

test('a different axe version or tag set fails until the baseline is measured again', () => {
  assert.equal(compare(base({}), { ...run({}), axe: '4.14.0' }).failures[0].kind, 'engine')
  assert.equal(compare(base({}), { ...run({}), tags: ['wcag2a'] }).failures[0].kind, 'engine')
})

test('a decrease passes and is offered as a tighter baseline', () => {
  const r = compare(base({ a: { 'color-contrast': 5, label: 2 } }), run({ a: ok({ 'color-contrast': 3 }) }))
  assert.equal(r.ok, true)
  assert.deepEqual(r.tighten, [
    { state: 'a', rule: 'color-contrast', base: 5, now: 3 },
    { state: 'a', rule: 'label', base: 2, now: 0 }
  ])
  const next = nextBaseline(base({ a: { 'color-contrast': 5, label: 2 } }), run({ a: ok({ 'color-contrast': 3 }) }))
  assert.deepEqual(next.states, { a: { 'color-contrast': 3 } })
  assert.equal(next.measured.total, 3)
})

test('the baseline is never raised without a reason, and the reason is kept', () => {
  const b = base({ a: { label: 1 } })
  const r = run({ a: ok({ label: 2 }) })
  assert.throws(() => nextBaseline(b, r), /refusing to raise/)
  const next = nextBaseline(b, r, { allowIncrease: 'Settings gained the TETRA tab', date: '2026-11-13' })
  assert.equal(next.states.a.label, 2)
  assert.equal(next.notes.at(-1).reason, 'Settings gained the TETRA tab')
})

test('a run with errored states cannot become a baseline', () => {
  assert.throws(() => nextBaseline(base({ a: {} }), run({ a: { status: 'error', error: 'x' } })), /errored/)
})

test('a partial run keeps the states it did not measure', () => {
  const next = nextBaseline(base({ a: { label: 1 }, b: { label: 4 } }), run({ a: ok({}) }, { partial: true }))
  assert.deepEqual(next.states, { a: {}, b: { label: 4 } })
})

test('the first baseline is the run, with zeros dropped and keys sorted', () => {
  const next = nextBaseline(null, run({ z: ok({ label: 0, 'color-contrast': 2 }), a: ok({}) }), { commit: 'abc', date: '2026-11-13' })
  assert.deepEqual(Object.keys(next.states), ['a', 'z'])
  assert.deepEqual(next.states.z, { 'color-contrast': 2 })
  assert.deepEqual(next.measured, { commit: 'abc', date: '2026-11-13', total: 2 })
  assert.equal(total({ x: { a: 1, b: 2 }, y: { counts: { c: 3 } } }), 6)
})
