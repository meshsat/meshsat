// The fixtures are synthetic and must stay that way: this repository is
// public. `node --test fixtures.test.mjs` fails on anything that looks like
// real data (a position on land near the kits, a real phone number, an IMEI,
// a private address, a mail address, a host name of ours).
import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { loadFixtures, emptied } from './serve.mjs'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const DIR = path.join(HERE, 'fixtures/rich')
// The public demo's answers (internal/demo/fixtures, bridge.meshsat.net)
// are held to the same rules: they are published even more widely.
// [MESHSAT-1845]
const DEMO = path.join(HERE, '../../internal/demo/fixtures')
const DEMO_SHORT_NAMES = /^(SV01|RB01|RB02|SWIM|CMD1|BUOY|NOD[A-Z])$/
const sets = [DIR, DEMO].map((dir) => ({ dir, files: fs.readdirSync(dir).filter((f) => f.endsWith('.json')) }))
const all = sets.flatMap(({ dir, files }) => files.map((f) => ({ dir, f, label: path.relative(HERE, path.join(dir, f)) })))

function * leaves (v, key = '') {
  if (Array.isArray(v)) for (const x of v) yield * leaves(x, key)
  else if (v && typeof v === 'object') for (const [k, x] of Object.entries(v)) yield * leaves(x, k)
  else yield [key, v]
}

test('every fixture is valid JSON', () => {
  for (const { files } of sets) assert.ok(files.length > 50)
  for (const { dir, f } of all) JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'))
})

test('positions sit in open water off the coast (52.00-52.10 N, 3.90-3.99 E) or are zero', () => {
  for (const { dir, f, label } of all) {
    for (const [k, v] of leaves(JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8')))) {
      if (typeof v !== 'number' || v === 0) continue
      if (/(^|_)(lat|latitude)$/i.test(k)) assert.ok(v >= 52.0 && v <= 52.1, `${label}: ${k}=${v}`)
      if (/(^|_)(lon|lng|longitude)$/i.test(k)) assert.ok(v >= 3.9 && v <= 3.99, `${label}: ${k}=${v}`)
    }
  }
})

test('no real-looking personal data or infrastructure names', () => {
  const rules = [
    [/\+(?!447700900\d{3})\d{8,}/, 'phone number outside +44 7700 900xxx'],
    [/"0[67]\d{8}"/, 'Dutch mobile number'],
    [/(?<![\d.])(?!0{15})\d{15}(?![\d.])/, '15-digit number (IMEI) that is not all zeros'],
    [/\b89\d{17,18}\b/, 'ICCID'],
    [/@(?!example\.)[a-z0-9-]+\.[a-z]/i, 'mail address'],
    [/\b(10\.\d+|192\.168|172\.(1[6-9]|2\d|3[01]))\.\d+\.\d+/, 'private IPv4 address'],
    [/nllei|tesseract|parallax|nuclearlighters|papadopoulos|meshsat\.net/i, 'host or person name of ours'],
    [/msat-ttc|MSPRLX|MSTSRT/, 'kit channel or callsign'],
    [/"(TESS|PRLX|MSP[A-Z])"|flaneur|E2E-\d/, 'real node short name or crew callsign']
  ]
  for (const { dir, f, label } of all) {
    const text = fs.readFileSync(path.join(dir, f), 'utf8')
    for (const [re, what] of rules) assert.doesNotMatch(text, re, `${label}: ${what}`)
  }
})

test('names and ids that identify real nodes, crews or cells are synthetic', () => {
  for (const { dir, f, label } of all) {
    for (const [k, v] of leaves(JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8')))) {
      if (k === 'short_name' && v) assert.match(v, dir === DEMO ? DEMO_SHORT_NAMES : /^NOD[A-Z]$/, `${label}: short_name ${v}`)
      if (k === 'cell_id' && v) assert.match(v, /^0+1?$/, `${label}: cell_id ${v}`)
      if (k === 'node_id' && typeof v === 'string' && v && !/^[0-9a-f]+$/.test(v)) assert.match(v, /^(!a11ce[0-9a-f]{3}|TAK-CREW-\d+)$/, `${label}: node_id ${v}`)
    }
  }
})

test('node ids are the synthetic !a11ceNNN range', () => {
  for (const { dir, f, label } of all) {
    for (const m of fs.readFileSync(path.join(dir, f), 'utf8').matchAll(/"!([0-9a-f]{8})"/g)) {
      assert.ok(m[1].startsWith('a11ce') || m[1] === 'ffffffff', `${label}: node !${m[1]}`)
    }
  }
})

test('the empty profile empties every list and keeps the shapes', () => {
  assert.deepEqual(emptied({ a: [1, 2], b: { c: [3], count: 4 }, d: 'x' }), { a: [], b: { c: [], count: 0 }, d: 'x' })
  const empty = loadFixtures(DIR, 'empty')
  const rich = loadFixtures(DIR, 'rich')
  assert.deepEqual([...empty.keys()].sort(), [...rich.keys()].sort())
  for (const [k, v] of empty) for (const [, x] of leaves(v)) assert.ok(!Array.isArray(x), k)
})
