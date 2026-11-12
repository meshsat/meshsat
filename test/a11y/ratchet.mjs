// The ratchet: a run may never find more than the committed baseline.
//
// A run and a baseline both hold, per state key, a map of rule id to the
// number of failing elements. The pseudo-rule `meshsat-unlabelled-control`
// counts visible form controls with no programmatic label (axe accepts a
// placeholder as a name; WCAG 2.1 AA wants a label). Pure functions only,
// so the rules are unit-tested without a browser (ratchet.test.mjs).

export const UNLABELLED = 'meshsat-unlabelled-control'
// Ids used twice where a label or ARIA reference points at them.
export const DUPLICATE_ID = 'meshsat-duplicate-id'
// Contrast axe left undecided, measured directly (run.mjs FALLBACK).
export const CONTRAST_FALLBACK = 'meshsat-contrast-fallback'
// Contrast axe left for review that the direct measurement could not reach.
export const CONTRAST_UNRESOLVED = 'meshsat-contrast-unresolved'
// A JavaScript error on the page while the state was measured.
export const PAGE_ERROR = 'meshsat-page-error'
// A state must keep showing at least this share of the visible text it had
// when the baseline was written, so a screen that broke cannot score zero.
export const CONTENT_FLOOR = 0.8

const sum = (counts) => Object.values(counts || {}).reduce((n, c) => n + c, 0)

/** Total findings in a set of states. */
export function total (states) {
  return Object.values(states || {}).reduce((n, s) => n + sum(s.counts ?? s), 0)
}

/**
 * Compare a run with the baseline.
 * run:      { axe, tags, partial, states: { key: { status, error?, counts } } }
 * baseline: { axe, tags, states: { key: counts } }
 * Returns { ok, failures: [{ kind, state?, rule?, base?, now? }], tighten: [...], notes: [...] }.
 */
export function compare (baseline, run) {
  const failures = []
  const tighten = []
  const notes = []
  if (baseline.axe !== run.axe || JSON.stringify(baseline.tags) !== JSON.stringify(run.tags)) {
    failures.push({ kind: 'engine', base: `${baseline.axe} ${baseline.tags}`, now: `${run.axe} ${run.tags}` })
  }
  for (const [key, st] of Object.entries(run.states)) {
    if (st.status !== 'ok') { failures.push({ kind: 'error', state: key, now: st.error || st.status }); continue }
    const base = baseline.states[key]
    const counts = st.counts || {}
    const floor = baseline.content?.[key]
    if (floor !== undefined && typeof st.content === 'number' && st.content < floor) {
      failures.push({ kind: 'content-drop', state: key, base: floor, now: st.content })
    }
    if (base === undefined) {
      if (sum(counts) > 0) failures.push({ kind: 'new-state', state: key, now: sum(counts) })
      else notes.push({ kind: 'new-clean-state', state: key })
      continue
    }
    for (const [rule, now] of Object.entries(counts)) {
      if (now === 0) continue
      const b = base[rule]
      if (b === undefined) failures.push({ kind: 'new-rule', state: key, rule, base: 0, now })
      else if (now > b) failures.push({ kind: 'increase', state: key, rule, base: b, now })
      else if (now < b) tighten.push({ state: key, rule, base: b, now })
    }
    for (const [rule, b] of Object.entries(base)) {
      if (b > 0 && !counts[rule]) tighten.push({ state: key, rule, base: b, now: 0 })
    }
  }
  if (!run.partial) {
    for (const key of Object.keys(baseline.states)) {
      if (!(key in run.states)) failures.push({ kind: 'missing', state: key })
    }
  }
  return { ok: failures.length === 0, failures, tighten, notes }
}

/** Only non-zero counts, keys sorted, so the baseline diffs cleanly. */
function clean (counts) {
  const o = {}
  for (const k of Object.keys(counts || {}).sort()) if (counts[k] > 0) o[k] = counts[k]
  return o
}

/**
 * The baseline a run would write. A partial run keeps the states it did not
 * measure. Raising a count needs a reason, which is kept in `notes`.
 */
export function nextBaseline (baseline, run, { allowIncrease = '', commit = '', date = '' } = {}) {
  const prev = baseline || { states: {}, notes: [] }
  const result = baseline ? compare(baseline, run) : { failures: [] }
  const raised = result.failures.filter((f) => f.kind === 'increase' || f.kind === 'new-rule')
  const errors = result.failures.filter((f) => f.kind === 'error')
  if (errors.length) throw new Error(`cannot write a baseline from a run with errored states: ${errors.map((e) => e.state).join(', ')}`)
  if (raised.length && !allowIncrease) {
    throw new Error(`refusing to raise ${raised.length} count(s) without --allow-increase "<reason>": ` +
      raised.slice(0, 5).map((f) => `${f.state} ${f.rule} ${f.base}->${f.now}`).join('; '))
  }
  const states = run.partial ? { ...prev.states } : {}
  for (const [key, st] of Object.entries(run.states)) states[key] = clean(st.counts)
  const sorted = {}
  for (const k of Object.keys(states).sort()) sorted[k] = states[k]
  const content = run.partial ? { ...(prev.content || {}) } : {}
  for (const [key, st] of Object.entries(run.states)) if (typeof st.content === 'number') content[key] = Math.floor(st.content * CONTENT_FLOOR)
  const sortedContent = {}
  for (const k of Object.keys(content).sort()) sortedContent[k] = content[k]
  const notes = [...(prev.notes || [])]
  if (raised.length) notes.push({ date, commit, reason: allowIncrease, raised: raised.map((f) => `${f.state} ${f.rule} ${f.base}->${f.now}`) })
  return { schema: 1, axe: run.axe, tags: run.tags, measured: { commit, date, total: total(sorted) }, notes, states: sorted, content: sortedContent }
}
