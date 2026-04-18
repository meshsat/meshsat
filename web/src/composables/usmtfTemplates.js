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

// Extended USMTF library [MESHSAT-569]. Kept deliberately small —
// one template = one common field-report type. The registry shape
// lets operators add local templates without touching core code;
// future work is a runtime-editable template editor (out of scope
// for Phase 6).

templates.oprep = {
  id: 'oprep', name: 'OPREP-3 (operational report)', short: 'OPREP',
  fields: [
    { key: 'dtg',       label: 'DTG',       placeholder: '181530ZAPR26', required: true },
    { key: 'origin',    label: 'Originator',  placeholder: 'callsign',   required: true },
    { key: 'event',     label: 'Event type',  placeholder: 'significant event', required: true },
    { key: 'summary',   label: 'Summary',     placeholder: 'brief',      required: true },
    { key: 'impact',    label: 'Impact',      placeholder: 'GREEN/AMBER/RED', required: false },
    { key: 'actions',   label: 'Actions',     placeholder: 'taken / planned', required: false }
  ],
  toWire(v)  { return slashJoin('OPREP', [v.dtg,v.origin,v.event,v.summary,v.impact,v.actions]) },
  fromWire(s) {
    const p = slashSplit(s); if (p[0] !== 'OPREP') throw new Error('not an OPREP message')
    return { dtg:p[1], origin:p[2], event:p[3], summary:p[4], impact:p[5], actions:p[6] }
  }
}

templates.casevac = {
  id: 'casevac', name: 'CASEVAC request', short: 'CASEVAC',
  fields: [
    { key: 'location', label: 'Pickup location (MGRS)', placeholder: '31U...', required: true },
    { key: 'freq',     label: 'Freq / Callsign',        placeholder: '152.5 / HAWK', required: true },
    { key: 'patients', label: 'Patients',               placeholder: '1A 1B',  required: true },
    { key: 'equip',    label: 'Equipment needed',       placeholder: 'none',   required: false },
    { key: 'type',     label: 'Litter / Ambulatory',    placeholder: '2L 1A',  required: true },
    { key: 'security', label: 'Security',               placeholder: 'NONE',   required: true },
    { key: 'marking',  label: 'Marking method',         placeholder: 'SMOKE',  required: true }
  ],
  toWire(v)  { return slashJoin('CASEVAC', [v.location,v.freq,v.patients,v.equip,v.type,v.security,v.marking]) },
  fromWire(s) {
    const p = slashSplit(s); if (p[0] !== 'CASEVAC') throw new Error('not a CASEVAC message')
    return { location:p[1], freq:p[2], patients:p[3], equip:p[4], type:p[5], security:p[6], marking:p[7] }
  }
}

templates.logpac = {
  id: 'logpac', name: 'LOGPAC request', short: 'LOGPAC',
  fields: [
    { key: 'dtg',      label: 'DTG',        placeholder: '181530ZAPR26', required: true },
    { key: 'unit',     label: 'Unit',       placeholder: 'A/1-22',       required: true },
    { key: 'location', label: 'Location',   placeholder: 'MGRS 31U...',  required: true },
    { key: 'class',    label: 'Class(es)',  placeholder: 'I/III/V/IX',   required: true },
    { key: 'qty',      label: 'Quantity',   placeholder: '3 pax x MRE x 5 days', required: true },
    { key: 'urgency',  label: 'Urgency',    placeholder: 'R=routine U=urgent F=flash', required: true }
  ],
  toWire(v) { return slashJoin('LOGPAC', [v.dtg,v.unit,v.location,v.class,v.qty,v.urgency]) },
  fromWire(s) { const p=slashSplit(s); if (p[0]!=='LOGPAC') throw new Error('not a LOGPAC message')
    return { dtg:p[1],unit:p[2],location:p[3],class:p[4],qty:p[5],urgency:p[6] } }
}

templates.hazard = {
  id: 'hazard', name: 'HAZARDREP', short: 'HAZARD',
  fields: [
    { key: 'dtg',      label: 'DTG',         placeholder: '181530ZAPR26', required: true },
    { key: 'type',     label: 'Hazard type', placeholder: 'CBRN / UXO / fire / civilian', required: true },
    { key: 'location', label: 'Location',    placeholder: 'MGRS 31U...',  required: true },
    { key: 'desc',     label: 'Description', placeholder: 'details',      required: true },
    { key: 'action',   label: 'Action',      placeholder: 'bypass / mark', required: false }
  ],
  toWire(v) { return slashJoin('HAZARD', [v.dtg,v.type,v.location,v.desc,v.action]) },
  fromWire(s) { const p=slashSplit(s); if (p[0]!=='HAZARD') throw new Error('not a HAZARD message')
    return { dtg:p[1],type:p[2],location:p[3],desc:p[4],action:p[5] } }
}

templates.eei = {
  id: 'eei', name: 'EEI (essential elements of info)', short: 'EEI',
  fields: [
    { key: 'req_by',  label: 'Requested by',  placeholder: 'callsign',   required: true },
    { key: 'topic',   label: 'Topic',         placeholder: 'enemy disposition', required: true },
    { key: 'details', label: 'Details',       placeholder: 'specific questions', required: true },
    { key: 'by',      label: 'Needed by DTG', placeholder: '181530ZAPR26', required: true }
  ],
  toWire(v) { return slashJoin('EEI', [v.req_by,v.topic,v.details,v.by]) },
  fromWire(s) { const p=slashSplit(s); if (p[0]!=='EEI') throw new Error('not an EEI message')
    return { req_by:p[1],topic:p[2],details:p[3],by:p[4] } }
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
