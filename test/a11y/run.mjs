#!/usr/bin/env node
// The accessibility run of the Bridge web app (WCAG 2.1 AA, axe-core).
//
//   node run.mjs --dist ../../web/dist [--out DIR] [--filter REGEX] [--update-baseline [--allow-increase "why"]]
//   node run.mjs --live tesseract|parallax|http://host:6050 [--dist ../../web/dist] [--out DIR]
//
// Fixture mode loads the built app from disk with synthetic API answers and
// compares the findings with baseline.json: any rise fails (exit 1). Live mode
// browses a real kit read-only and records counts only (no page content).
import fs from 'node:fs'
import path from 'node:path'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'
import { AxeBuilder } from '@axe-core/playwright'
import axeCore from 'axe-core'
import { VARIANTS, PROFILES, statesFor, stateKey } from './states.mjs'
import { ORIGIN, loadFixtures, routeFixtures, routeLive, keyShape } from './serve.mjs'
import { compare, nextBaseline, total, UNLABELLED } from './ratchet.mjs'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO = path.resolve(HERE, '../..')
export const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']
const FIXED_NOW = '2026-11-01T10:00:00Z'
const BOTTOM_NAV = 'nav[aria-label="Primary navigation"]'
const QUIET_CSS = '*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}'

function args (argv) {
  const a = { out: 'a11y-results', screens: true }
  for (let i = 0; i < argv.length; i++) {
    const k = argv[i]
    const v = () => argv[++i]
    if (k === '--dist') a.dist = path.resolve(v())
    else if (k === '--live') a.live = v()
    else if (k === '--out') a.out = v()
    else if (k === '--filter') a.filter = new RegExp(v())
    else if (k === '--update-baseline') a.update = true
    else if (k === '--allow-increase') a.allowIncrease = v()
    else if (k === '--baseline') a.baseline = path.resolve(v())
    else if (k === '--no-screens') a.screens = false
    else throw new Error(`unknown argument ${k}`)
  }
  a.baseline ||= path.join(HERE, 'baseline.json')
  if (!a.dist && !a.live) throw new Error('give --dist <web/dist> or --live <kit>')
  return a
}

function commitSha () {
  if (process.env.CI_COMMIT_SHA) return process.env.CI_COMMIT_SHA
  try { return execFileSync('git', ['-C', REPO, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim() } catch { return '' }
}

function bundleOf (html) {
  const m = String(html).match(/\/assets\/(index-[A-Za-z0-9_-]+\.js)/)
  return m ? m[1] : ''
}

async function kitBase (live) {
  if (/^https?:\/\//.test(live)) return live.replace(/\/+$/, '')
  const host = execFileSync(path.join(REPO, 'scripts/kit-host.sh'), [live], { encoding: 'utf8', timeout: 30000 }).trim()
  if (!host) throw new Error(`${live}: no address answers`)
  return `http://${host}:6050`
}

// Visible form controls with no programmatic label, inside the measured part.
async function unlabelledControls (page, scope) {
  return page.evaluate(({ scope, bottom }) => {
    const visible = (el) => {
      const r = el.getBoundingClientRect()
      const cs = getComputedStyle(el)
      return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none'
    }
    const inScope = (el) => scope === 'shell' ? !el.closest('main') : !el.closest('header') && !el.closest(bottom)
    const out = []
    for (const el of document.querySelectorAll('input, select, textarea')) {
      if (el.type === 'hidden' || !visible(el) || !inScope(el) || el.closest('[aria-hidden="true"]')) continue
      const named = (el.labels && el.labels.length > 0) || el.getAttribute('aria-label')?.trim() ||
        el.getAttribute('aria-labelledby')?.trim() || el.getAttribute('title')?.trim()
      if (!named) {
        const id = el.id ? `#${el.id}` : ''
        const ph = el.getAttribute('placeholder')
        out.push(`${el.tagName.toLowerCase()}${id}${el.type && el.tagName === 'INPUT' ? `[type=${el.type}]` : ''}${ph ? ` placeholder="${ph.slice(0, 40)}"` : ''}`)
      }
    }
    return out
  }, { scope, bottom: BOTTOM_NAV })
}

async function measure ({ ctx, base, variant, state, live, screensDir, key }) {
  const t0 = Date.now()
  const page = await ctx.newPage()
  const pageErrors = []
  page.on('pageerror', (e) => pageErrors.push(String(e.message || e).slice(0, 200)))
  page.on('dialog', (d) => d.dismiss().catch(() => {}))
  try {
    if (!live) await page.clock.setFixedTime(new Date(FIXED_NOW))
    const sep = state.path.includes('?') ? '&' : '?'
    await page.goto(`${base}${state.path}${sep}${variant.query}`, { waitUntil: 'load', timeout: 30000 })
    await page.locator('main').first().waitFor({ state: 'attached', timeout: 10000 })
    await page.evaluate(() => document.fonts.ready)
    await page.waitForTimeout(live ? 1500 : 700)
    if (state.act) {
      try { await state.act(page) } catch (e) {
        // A sub-state that needs data a kit lacks is not reachable there.
        if (live) return { status: 'unreachable', error: String(e.message).split('\n')[0], ms: Date.now() - t0 }
        throw e
      }
      await page.waitForTimeout(500)
    }
    await page.addStyleTag({ content: QUIET_CSS })
    await page.waitForTimeout(100)

    let builder = new AxeBuilder({ page }).withTags(TAGS)
    builder = state.scope === 'shell' ? builder.exclude('main') : builder.exclude('header').exclude(BOTTOM_NAV)
    const r = await builder.analyze()
    const unlabelled = await unlabelledControls(page, state.scope)

    const counts = {}
    for (const v of r.violations) counts[v.id] = v.nodes.length
    if (unlabelled.length) counts[UNLABELLED] = unlabelled.length
    const incomplete = {}
    for (const v of r.incomplete) incomplete[v.id] = v.nodes.length
    const violations = r.violations.map((v) => ({
      id: v.id, impact: v.impact, help: v.help,
      nodes: v.nodes.map((n) => live
        ? { target: n.target }
        : { target: n.target, html: n.html.slice(0, 200), summary: (n.failureSummary || '').slice(0, 300) })
    }))
    if (screensDir) {
      await page.screenshot({ path: path.join(screensDir, `${key.replace(/[/:]/g, '_')}.jpg`), fullPage: true, type: 'jpeg', quality: 60 })
    }
    return { status: 'ok', counts, incomplete, violations, unlabelled: live ? unlabelled.length : unlabelled, pageErrors, ms: Date.now() - t0 }
  } catch (e) {
    return { status: 'error', error: String(e.message || e).split('\n')[0].slice(0, 300), pageErrors, ms: Date.now() - t0 }
  } finally {
    await page.close().catch(() => {})
  }
}

// Proof that the run can fail: known defects injected into the real app must
// be found, and a baseline of zero must then fail the ratchet.
async function canary (ctx, base, axeVersion) {
  const page = await ctx.newPage()
  try {
    await page.goto(`${base}/?shell=engineer&saverMs=0&bundleWatchMs=0&bundleFreshMs=0`, { waitUntil: 'load' })
    await page.locator('main').first().waitFor({ state: 'attached' })
    await page.evaluate(() => {
      const d = document.createElement('div')
      d.id = 'a11y-canary'
      d.style.background = '#15151B'
      d.innerHTML = '<p style="color:#5C5C68;background:#15151B;font-size:12px">low contrast canary text</p>' +
        '<input type="text" placeholder="canary"><div aria-label="canary strip"></div>'
      document.querySelector('main').prepend(d)
    })
    const r = await new AxeBuilder({ page }).withTags(TAGS).include('#a11y-canary').analyze()
    const found = r.violations.map((v) => v.id).sort()
    const unl = (await unlabelledControls(page, 'view')).filter((s) => s.includes('canary')).length
    const need = ['aria-prohibited-attr', 'color-contrast']
    const counts = Object.fromEntries(r.violations.map((v) => [v.id, v.nodes.length]))
    counts[UNLABELLED] = unl
    const ratchet = compare({ axe: axeVersion, tags: TAGS, states: { canary: {} } },
      { axe: axeVersion, tags: TAGS, states: { canary: { status: 'ok', counts } } })
    const ok = need.every((id) => found.includes(id)) && unl === 1 && !ratchet.ok
    return { ok, found, unlabelled: unl, ratchetFailed: !ratchet.ok }
  } catch (e) {
    return { ok: false, error: String(e.message || e).split('\n')[0] }
  } finally {
    await page.close().catch(() => {})
  }
}

function esc (s) { return String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c])) }

function writeReports (out, report) {
  fs.writeFileSync(path.join(out, 'results.json'), JSON.stringify(report, null, 1))
  const views = {}
  for (const [key, st] of Object.entries(report.states)) {
    const [variant, profile, vs] = key.split('/')
    const view = vs.split(':')[0]
    const row = (views[view] ||= {})
    const cell = (row[`${variant} ${profile}`] ||= { axe: 0, unl: 0, states: 0, errors: 0 })
    cell.states++
    if (st.status !== 'ok') { cell.errors++; continue }
    for (const [rule, n] of Object.entries(st.counts)) { if (rule === UNLABELLED) cell.unl += n; else cell.axe += n }
  }
  const cols = [...new Set(Object.values(views).flatMap((r) => Object.keys(r)))].sort()
  const lines = [
    `# Accessibility run: ${report.mode}`, '',
    `- Commit: ${report.commit || 'unknown'}${report.bundle ? `, bundle ${report.bundle}` : ''}`,
    `- axe-core ${report.axe}, tags ${report.tags.join(', ')}, ${report.rules.length} rules; ${report.browser}`,
    `- States: ${Object.keys(report.states).length}; findings ${report.total} (axe violations plus unlabelled controls)`,
    report.canary ? `- Canary: ${report.canary.ok ? 'PASS' : 'FAIL'} (${JSON.stringify(report.canary)})` : '',
    report.ratchet ? `- Ratchet: ${report.ratchet.ok ? 'PASS' : 'FAIL'}${report.ratchet.baseline ? '' : ' (no baseline yet: measure mode)'}` : '', '',
    '| Screen | ' + cols.join(' | ') + ' |', '|---|' + cols.map(() => '---').join('|') + '|',
    ...Object.keys(views).sort().map((v) => `| ${v} | ` + cols.map((c) => {
      const x = views[v][c]
      return x ? `${x.axe} / ${x.unl}${x.errors ? ` (${x.errors} err)` : ''}` : '–'
    }).join(' | ') + ' |'),
    '', 'Cells: axe violations / unlabelled controls, summed over the states of that screen.', ''
  ]
  if (report.ratchet?.failures?.length) {
    lines.push('## Ratchet failures', '', ...report.ratchet.failures.slice(0, 200).map((f) => `- ${f.kind} ${f.state || ''} ${f.rule || ''} ${f.base ?? ''} -> ${f.now ?? ''}`), '')
  }
  if (report.ratchet?.tighten?.length) lines.push(`## Lower than the baseline: ${report.ratchet.tighten.length} counts (commit baseline.next.json)`, '')
  const rules = {}
  for (const st of Object.values(report.states)) for (const [r, n] of Object.entries(st.counts || {})) rules[r] = (rules[r] || 0) + n
  lines.push('## Findings by rule', '', ...Object.entries(rules).sort((a, b) => b[1] - a[1]).map(([r, n]) => `- ${r}: ${n}`), '')
  if (report.missingFixtures?.length) lines.push(`## API paths without a fixture (answered 404): ${report.missingFixtures.length}`, '', report.missingFixtures.join(', '), '')
  fs.writeFileSync(path.join(out, 'summary.md'), lines.filter((l) => l !== null).join('\n'))

  const failed = new Map()
  for (const f of report.ratchet?.failures || []) if (f.state) failed.set(f.state, `${f.kind} ${f.rule || ''} ${f.base ?? ''} -> ${f.now ?? ''}`)
  const cases = Object.entries(report.states).map(([key, st]) => {
    const body = failed.has(key) ? `<failure message="${esc(failed.get(key))}"/>` : (st.status === 'error' ? `<error message="${esc(st.error)}"/>` : '')
    return `  <testcase classname="a11y.${esc(key.split('/')[0])}" name="${esc(key)}" time="${((st.ms || 0) / 1000).toFixed(2)}">${body}</testcase>`
  })
  if (report.canary) cases.push(`  <testcase classname="a11y.canary" name="canary">${report.canary.ok ? '' : `<failure message="${esc(JSON.stringify(report.canary))}"/>`}</testcase>`)
  fs.writeFileSync(path.join(out, 'junit.xml'), `<?xml version="1.0" encoding="UTF-8"?>\n<testsuite name="a11y" tests="${cases.length}" failures="${failed.size + (report.canary && !report.canary.ok ? 1 : 0)}">\n${cases.join('\n')}\n</testsuite>\n`)
}

async function main () {
  const a = args(process.argv.slice(2))
  const out = path.resolve(a.out)
  fs.mkdirSync(out, { recursive: true })
  const screensDir = a.screens && !a.live ? path.join(out, 'screens') : null
  if (screensDir) fs.mkdirSync(screensDir, { recursive: true })
  const live = !!a.live
  const base = live ? await kitBase(a.live) : ORIGIN
  const distBundle = a.dist ? bundleOf(fs.readFileSync(path.join(a.dist, 'index.html'))) : ''

  const browser = await chromium.launch({ headless: true })
  const report = {
    schema: 1, mode: live ? `live ${a.live}` : 'fixtures', axe: axeCore.version, tags: TAGS, rules: [],
    browser: `chromium ${browser.version()}`, commit: commitSha(), date: new Date().toISOString(),
    bundle: distBundle, partial: !!a.filter, states: {}, missingFixtures: []
  }
  const missing = new Set()
  const shapes = live ? new Map() : null
  try {
    if (live) {
      const res = await fetch(`${base}/`)
      report.kitBundle = bundleOf(await res.text())
      if (distBundle && report.kitBundle !== distBundle) {
        console.error(`kit serves ${report.kitBundle}, the dist is ${distBundle}: not the audited build`)
        report.bundleMismatch = true
      }
    }
    const profiles = live ? ['live'] : PROFILES
    for (const variant of VARIANTS) {
      for (const profile of profiles) {
        const ctx = await browser.newContext({ ...variant.context, reducedMotion: 'reduce', serviceWorkers: 'block', locale: 'en-GB', timezoneId: 'UTC' })
        await ctx.addInitScript(() => { try { localStorage.clear(); sessionStorage.clear() } catch {} })
        if (live) await routeLive(ctx, { base, shapes })
        else await routeFixtures(ctx, { dist: a.dist, fixtures: loadFixtures(path.join(HERE, 'fixtures/rich'), profile), missing })
        for (const state of statesFor(variant, live ? 'rich' : profile)) {
          const key = stateKey(variant, profile, state)
          if (a.filter && !a.filter.test(key)) continue
          const st = await measure({ ctx, base, variant, state, live, screensDir, key })
          report.states[key] = st
          const n = st.counts ? Object.values(st.counts).reduce((x, y) => x + y, 0) : '-'
          console.log(`${st.status.padEnd(11)} ${String(n).padStart(4)}  ${key}${st.error ? `  (${st.error})` : ''}`)
        }
        if (!report.rules.length) {
          const p = await ctx.newPage()
          await p.goto(`${base}/?${variant.query}`, { waitUntil: 'load' })
          await new AxeBuilder({ page: p }).withTags(TAGS).include('header').analyze()
          report.rules = await p.evaluate((tags) => window.axe.getRules(tags).map((r) => r.ruleId).sort(), TAGS)
          await p.close()
        }
        if (!live && variant.id === 'desktop-engineer' && profile === 'rich' && !a.filter) report.canary = await canary(ctx, base, axeCore.version)
        await ctx.close()
      }
    }
  } finally {
    await browser.close()
  }
  report.missingFixtures = [...missing].sort()
  const measured = Object.fromEntries(Object.entries(report.states).filter(([, s]) => s.status === 'ok' || s.status === 'error'))
  report.total = total(Object.fromEntries(Object.entries(measured).map(([k, s]) => [k, s.counts || {}])))

  let exit = 0
  if (live) {
    // Counts only: this is what may be committed from a kit run.
    const kits = { kit: a.live, date: report.date, kitBundle: report.kitBundle, distBundle, axe: report.axe, tags: TAGS, total: report.total,
      states: Object.fromEntries(Object.entries(report.states).map(([k, s]) => [k, s.status === 'ok' ? s.counts : { status: s.status }])) }
    fs.writeFileSync(path.join(out, 'kits.json'), JSON.stringify(kits, null, 1))
    const fx = loadFixtures(path.join(HERE, 'fixtures/rich'), 'rich')
    const drift = []
    for (const [k, shape] of shapes) {
      if (!fx.has(k)) continue
      const want = JSON.stringify(Object.keys(keyShape(fx.get(k)) || {}))
      const got = JSON.stringify(Object.keys(shape || {}))
      if (want !== got) drift.push({ path: k, fixture: want, live: got })
    }
    report.fixtureDrift = drift
    if (drift.length) console.log(`fixture key drift on ${drift.length} paths (see results.json)`)
  } else {
    const run = { axe: report.axe, tags: TAGS, partial: report.partial, states: report.states }
    const baseline = fs.existsSync(a.baseline) ? JSON.parse(fs.readFileSync(a.baseline, 'utf8')) : null
    if (baseline) {
      report.ratchet = { baseline: true, ...compare(baseline, run) }
      if (!report.ratchet.ok) exit = 1
    } else {
      report.ratchet = { baseline: false, ok: true, failures: [], tighten: [], notes: [] }
      for (const [k, s] of Object.entries(report.states)) if (s.status === 'error') { report.ratchet.ok = false; report.ratchet.failures.push({ kind: 'error', state: k, now: s.error }) }
    }
    if (report.canary && !report.canary.ok) exit = 1
    const date = report.date.slice(0, 10)
    try {
      const next = nextBaseline(baseline, run, { commit: report.commit, date })
      fs.writeFileSync(path.join(out, 'baseline.next.json'), JSON.stringify(next, null, 1) + '\n')
    } catch (e) { console.log(`no baseline.next.json: ${e.message}`) }
    if (a.update) {
      const next = nextBaseline(baseline, run, { allowIncrease: a.allowIncrease, commit: report.commit, date })
      fs.writeFileSync(a.baseline, JSON.stringify(next, null, 1) + '\n')
      console.log(`baseline written: ${next.measured.total} findings in ${Object.keys(next.states).length} states`)
      exit = 0
    }
  }
  writeReports(out, report)
  console.log(fs.readFileSync(path.join(out, 'summary.md'), 'utf8').split('\n').slice(0, 30).join('\n'))
  process.exitCode = exit
}

main().catch((e) => { console.error(e); process.exitCode = 1 })
