// Accessibility rules read from the source, for what the browser run
// (test/a11y) only sees when the data happens to show it: a faded colour
// in a row that appears for one transport, a list long enough to scroll on
// a kit. [MESHSAT-1843]
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { test } from 'vitest'
import resolveConfig from 'tailwindcss/resolveConfig'
import config from '../../tailwind.config.js'

const SRC = path.resolve(__dirname, '../../src')
const theme = resolveConfig(config).theme

function files (dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((d) => {
    const p = path.join(dir, d.name)
    return d.isDirectory() ? files(p) : /\.(vue|js)$/.test(d.name) ? [p] : []
  })
}

const hex = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16))
const lum = (c) => { const [r, g, b] = c.map((v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4 }); return 0.2126 * r + 0.7152 * g + 0.0722 * b }
const ratio = (a, b) => { const [h, l] = [lum(a), lum(b)].sort((x, y) => y - x); return (h + 0.05) / (l + 0.05) }
const mix = (fg, bg, a) => fg.map((v, i) => v * a + bg[i] * (1 - a))
// The dark surfaces text sits on, the lightest (gray-700) included.
const SURFACES = ['#040406', '#0B0B0F', '#15151B', '#24242C'].map(hex)

function textColour (name, shade) {
  let c = theme.textColor[name]
  if (c === undefined && name.includes('-')) { const [a, ...b] = name.split('-'); c = theme.textColor[a]?.[b.join('-')] }
  const v = typeof c === 'string' ? c : c?.[shade]
  return typeof v === 'string' && /^#[0-9a-fA-F]{6}$/.test(v) ? v : null
}

test('every faded text colour (text-<colour>/NN) keeps 4.5:1 on the dark surfaces', () => {
  const bad = []
  const RE = /(?<![\w-])((?:[a-z-]+:)*)text-([a-z]+(?:-[a-z]+)*)(?:-(\d{2,3}))?\/(\d{1,2})(?![\w/])/g
  for (const f of files(SRC)) {
    for (const m of fs.readFileSync(f, 'utf8').matchAll(RE)) {
      if (/disabled:|placeholder:/.test(m[1])) continue
      const fg = textColour(m[2], m[3])
      if (!fg) continue
      const a = Number(m[4]) / 100
      const worst = Math.min(...SURFACES.map((bg) => ratio(mix(hex(fg), bg, a), bg)))
      if (worst < 4.5) bad.push(`${path.relative(SRC, f)}: ${m[0]} ${worst.toFixed(2)}:1`)
    }
  }
  assert.deepEqual(bad, [])
})

// The screens of SIDN D5 and the components they render.
const SCREENS = ['views/DashboardView.vue', 'views/MessagesView.vue', 'views/InboxView.vue', 'views/ComposeView.vue',
  'views/MapView.vue', 'views/SettingsView.vue', 'components/ContactPicker.vue', 'components/StatusStrip.vue',
  'components/NavBar.vue', 'components/IntentStatus.vue', 'components/MessageBubble.vue', 'components/TetraSettings.vue',
  'components/MediaHopSettings.vue', 'components/TimeSyncPanel.vue']

test('every scrolling container on the D5 screens can be reached from the keyboard', () => {
  const bad = []
  for (const rel of SCREENS) {
    const s = fs.readFileSync(path.join(SRC, rel), 'utf8')
    const t = s.slice(s.indexOf('<template>'))
    for (const m of t.matchAll(/<(div|pre|ul|section|table|tbody)\b([^>]*\boverflow-(?:y-)?auto\b[^>]*)>/g)) {
      if (/tabindex=/.test(m[2])) continue
      const tag = m[1]
      let depth = 1
      let inner = ''
      const re = new RegExp(`<${tag}\\b|</${tag}>`, 'g')
      re.lastIndex = m.index + m[0].length
      for (let mm; (mm = re.exec(t));) {
        depth += mm[0].startsWith('</') ? -1 : 1
        if (depth === 0) { inner = t.slice(m.index + m[0].length, mm.index); break }
      }
      // Something focusable inside (or a component that may render one) is enough.
      if (/<(button|a|input|select|textarea|router-link)\b|tabindex="0"|<[A-Z]\w+/.test(inner)) continue
      bad.push(`${rel}: <${tag}${m[2].slice(0, 80)}>`)
    }
  }
  assert.deepEqual(bad, [])
})
