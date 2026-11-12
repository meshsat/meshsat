// Secondary text meets WCAG 2.1 AA on every dark surface, and lifting the
// text greys left the background and border greys alone. [MESHSAT-1843]
import assert from 'node:assert/strict'
import { test } from 'vitest'
import resolveConfig from 'tailwindcss/resolveConfig'
import config from '../../tailwind.config.js'

const theme = resolveConfig(config).theme

function luminance (hex) {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
    .map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
function contrast (a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

const surfaces = { 700: '#24242C', 800: '#15151B', 900: '#0B0B0F', 950: '#040406' }

test('every text grey from 400 to 700 is at least 4.5:1 on gray-700, 800, 900 and 950', () => {
  for (const shade of [400, 500, 600, 700]) {
    for (const [bg, hex] of Object.entries(surfaces)) {
      const ratio = contrast(theme.textColor.gray[shade], hex)
      assert.ok(ratio >= 4.5, `text-gray-${shade} on gray-${bg}: ${ratio.toFixed(2)}`)
    }
  }
})

test('placeholders follow the text greys', () => {
  for (const shade of [400, 500, 600, 700]) assert.equal(theme.placeholderColor.gray[shade], theme.textColor.gray[shade])
})

test('the order of emphasis holds: 300 > 400 > 500 >= 600', () => {
  const l = (s) => luminance(theme.textColor.gray[s])
  assert.ok(l(300) > l(400) && l(400) > l(500) && l(500) >= l(600))
})

test('background and border greys are the brand scale, untouched', () => {
  for (const [shade, hex] of Object.entries({ 500: '#5C5C68', 600: '#3A3A44', 700: '#24242C', 800: '#15151B' })) {
    assert.equal(theme.backgroundColor.gray[shade], hex)
    assert.equal(theme.borderColor.gray[shade], hex)
  }
})
