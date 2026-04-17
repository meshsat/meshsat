// useEccm.js
// -----------
// Single source of ECCM (Electronic Counter-Counter-Measures) guidance
// for the RF spectrum UI. Both SpectrumWaterfall (per-band red banner
// on bad state) and SpectrumView (always-visible quick-reference table)
// import from here so the text cannot drift between surfaces.
//
// Strings are externalised to src/locales/en.json to make future i18n
// trivial: wire vue-i18n, replace the import with a t() lookup keyed
// on 'eccm.bands.<band>.<state>', and locales/* becomes the translation
// catalog. Today we only ship English; the structure is future-proof.
import locale from '@/locales/en.json'

const ECCM = locale.eccm

// Canonical ordered band list for the quick-reference table. Derived
// from the JSON instead of hardcoded so a new band (e.g. DCF77 77.5 kHz)
// is picked up automatically when added to locales.
export const BAND_ORDER = ['lora_868', 'aprs_144', 'gps_l1', 'lte_b20_dl', 'lte_b8_dl']

// eccmAction(bandName, state) → operator-facing recommendation text.
// Falls back to generic text if the band doesn't have specific
// guidance. Returns '' for clear/unknown states so callers can v-if on
// truthiness.
export function eccmAction(bandName, state) {
  if (!state) return ''
  const perBand = ECCM.bands?.[bandName]
  if (perBand && typeof perBand[state] === 'string') return perBand[state]
  return ECCM.generic?.[state] || ''
}

// shortLabel(bandName) — the 1-2 word pretty label for the quick-ref
// table and anywhere else the full "LoRa EU868" form is too long.
export function shortLabel(bandName) {
  return ECCM.bands?.[bandName]?.short || bandName
}

// quickReferenceRows() — array shape the quick-reference table
// consumes. Guarantees interference + jamming columns are present for
// every band in BAND_ORDER, even if the JSON omits one.
export function quickReferenceRows() {
  return BAND_ORDER.map(band => ({
    band,
    label: shortLabel(band),
    interference: eccmAction(band, 'interference'),
    jamming: eccmAction(band, 'jamming'),
  }))
}

// miji9Footer — the MIJI-9 field reference shown under the table.
export const miji9Footer = ECCM.miji9 || ''
