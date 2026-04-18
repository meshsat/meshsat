// USMTF template skeleton — SALUTE + MEDEVAC 9-line + SITREP.
// [MESHSAT-563]
//
// Structured field forms compile to the wire as slash-delimited
// strings that survive any bearer. SALUTE and SITREP round-trip
// cleanly as slash; the full USMTF spec uses XML for TAK-bound
// traffic, which we layer on later (see MESHSAT-569 for the full
// 20+ template library).
//
// Registry shape (extensible):
//   {
//     id, name, short,
//     fields: [{key, label, placeholder, required, type}, ...],
//     toWire(values)   -> "TEMPLATE/FIELD1/FIELD2/..."
//     fromWire(s)      -> {field1: ..., field2: ...}
//   }
//
// Spec calls for `.ts`; we ship as `.js` to match the surrounding
// composables (see MESHSAT-558 note).

function slashJoin(prefix, values) {
  return prefix + '/' + values.map(v => String(v ?? '').replace(/\//g, '\\/')).join('/')
}

function slashSplit(s) {
  // Protect escaped slashes during split.
  const parts = []
  let cur = ''
  let esc = false
  for (const ch of s) {
    if (esc) { cur += ch; esc = false; continue }
    if (ch === '\\') { esc = true; continue }
    if (ch === '/')  { parts.push(cur); cur = ''; continue }
    cur += ch
  }
  parts.push(cur)
  return parts
}

export const templates = {
  salute: {
    id: 'salute',
    name: 'SALUTE report',
    short: 'SALUTE',
    fields: [
      { key: 'size',     label: 'Size',     placeholder: '6x personnel',       required: true },
      { key: 'activity', label: 'Activity', placeholder: 'patrolling',         required: true },
      { key: 'location', label: 'Location', placeholder: 'MGRS 31U...',        required: true },
      { key: 'unit',     label: 'Unit',     placeholder: 'unknown uniformed',  required: false },
      { key: 'time',     label: 'Time',     placeholder: 'DTG e.g. 181530ZAPR26', required: true },
      { key: 'equip',    label: 'Equipment', placeholder: 'AK-47, ruck',       required: false }
    ],
    toWire(v) { return slashJoin('SALUTE', [v.size, v.activity, v.location, v.unit, v.time, v.equip]) },
    fromWire(s) {
      const p = slashSplit(s)
      if (p[0] !== 'SALUTE') throw new Error('not a SALUTE message')
      return { size: p[1], activity: p[2], location: p[3], unit: p[4], time: p[5], equip: p[6] }
    }
  },

  medevac9: {
    id: 'medevac9',
    name: 'MEDEVAC 9-line',
    short: 'MEDEVAC',
    fields: [
      { key: 'line1', label: 'Line 1 — Pickup location',    placeholder: 'MGRS 31U...', required: true },
      { key: 'line2', label: 'Line 2 — Radio freq / callsign', placeholder: '152.5 / HAWK', required: true },
      { key: 'line3', label: 'Line 3 — Patients by precedence', placeholder: 'A=urgent,B=urgent surg...', required: true },
      { key: 'line4', label: 'Line 4 — Special equipment',  placeholder: 'A=none B=hoist C=ext D=vent', required: true },
      { key: 'line5', label: 'Line 5 — Patients by type',   placeholder: 'L=litter A=ambulatory', required: true },
      { key: 'line6', label: 'Line 6 — Security at pickup', placeholder: 'N=none P=possible E=hostile', required: true },
      { key: 'line7', label: 'Line 7 — Method of marking',  placeholder: 'A=panels B=pyro C=smoke D=none E=other', required: true },
      { key: 'line8', label: 'Line 8 — Patient nationality', placeholder: 'A=US mil B=US civ C=non-US mil D=non-US civ E=EPW', required: true },
      { key: 'line9', label: 'Line 9 — NBC contamination',  placeholder: 'N=nuke B=bio C=chem', required: false }
    ],
    toWire(v) { return slashJoin('MEDEVAC',
      [v.line1,v.line2,v.line3,v.line4,v.line5,v.line6,v.line7,v.line8,v.line9]) },
    fromWire(s) {
      const p = slashSplit(s)
      if (p[0] !== 'MEDEVAC') throw new Error('not a MEDEVAC message')
      return {
        line1: p[1], line2: p[2], line3: p[3], line4: p[4], line5: p[5],
        line6: p[6], line7: p[7], line8: p[8], line9: p[9]
      }
    }
  },

  sitrep: {
    id: 'sitrep',
    name: 'SITREP',
    short: 'SITREP',
    fields: [
      { key: 'time',      label: 'DTG',         placeholder: '181530ZAPR26',    required: true },
      { key: 'location',  label: 'Location',    placeholder: 'MGRS 31U...',     required: true },
      { key: 'situation', label: 'Situation',   placeholder: 'holding pos, contact negative', required: true },
      { key: 'status',    label: 'Status',      placeholder: 'green/amber/red', required: true },
      { key: 'actions',   label: 'Actions',     placeholder: 'continuing patrol', required: false },
      { key: 'asks',      label: 'Requests',    placeholder: 'resupply H+6',    required: false }
    ],
    toWire(v) { return slashJoin('SITREP', [v.time, v.location, v.situation, v.status, v.actions, v.asks]) },
    fromWire(s) {
      const p = slashSplit(s)
      if (p[0] !== 'SITREP') throw new Error('not a SITREP message')
      return { time: p[1], location: p[2], situation: p[3], status: p[4], actions: p[5], asks: p[6] }
    }
  }
}

export function templateList() {
  return Object.values(templates)
}

export function detectTemplate(wire) {
  for (const t of Object.values(templates)) {
    try { t.fromWire(wire); return t.id } catch { /* not this one */ }
  }
  return null
}
