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

const DIR = path.join(path.dirname(fileURLToPath(import.meta.url)), 'fixtures/rich')
const files = fs.readdirSync(DIR).filter((f) => f.endsWith('.json'))

function * leaves (v, key = '') {
  if (Array.isArray(v)) for (const x of v) yield * leaves(x, key)
  else if (v && typeof v === 'object') for (const [k, x] of Object.entries(v)) yield * leaves(x, k)
  else yield [key, v]
}

test('every fixture is valid JSON', () => {
  assert.ok(files.length > 50)
  for (const f of files) JSON.parse(fs.readFileSync(path.join(DIR, f), 'utf8'))
})

test('positions sit in open water off the coast (52.00-52.10 N, 3.90-3.99 E) or are zero', () => {
  for (const f of files) {
    for (const [k, v] of leaves(JSON.parse(fs.readFileSync(path.join(DIR, f), 'utf8')))) {
      if (typeof v !== 'number' || v === 0) continue
      if (/(^|_)(lat|latitude)$/i.test(k)) assert.ok(v >= 52.0 && v <= 52.1, `${f}: ${k}=${v}`)
      if (/(^|_)(lon|lng|longitude)$/i.test(k)) assert.ok(v >= 3.9 && v <= 3.99, `${f}: ${k}=${v}`)
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
    [/msat-ttc|MSPRLX|MSTSRT/, 'kit channel or callsign']
  ]
  for (const f of files) {
    const text = fs.readFileSync(path.join(DIR, f), 'utf8')
    for (const [re, what] of rules) assert.doesNotMatch(text, re, `${f}: ${what}`)
  }
})

test('node ids are the synthetic !a11ceNNN range', () => {
  for (const f of files) {
    for (const m of fs.readFileSync(path.join(DIR, f), 'utf8').matchAll(/"!([0-9a-f]{8})"/g)) {
      assert.ok(m[1].startsWith('a11ce') || m[1] === 'ffffffff', `${f}: node !${m[1]}`)
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
