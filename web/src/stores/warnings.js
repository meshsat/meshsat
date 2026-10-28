import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import api from '@/api/client'

// The public warnings the Hub relayed to this kit (MESHSAT-1734,
// docs/warning-relay.md): one list for the Warnings page and for the banner
// on the operator dashboard and the booth screen, so they never disagree.
//
// The list is GET /api/warnings, newest received first. It is read again on
// every `warning` event of the Bridge's event stream (the relay emits one for
// every digest it keeps), whenever the stream (re)opens, and on a slow poll
// as the safety net. In force is the Bridge's own rule (warnings.InForce):
// kept, not cancelled or replaced, not a Cancel, and its expiry ahead; a
// warning that names no expiry counts for 24 hours after it was sent. The
// clock ticks here so a banner drops the minute its warning expires, with
// no event and no poll.
//
// start() and stop() are reference counted: the first consumer on a page
// opens the stream and the timers, the last one closes them. A page with the
// banner and the Warnings page open at once holds one stream.

export const POLL_MS = 60000
export const TICK_MS = 15000
export const NO_EXPIRY_IN_FORCE_MS = 24 * 60 * 60 * 1000

// expiry is the row's expires as a Date, or null when the digest named
// none: the API writes Go's zero time (year 1) for that.
export function expiry(w) {
  const v = w && w.expires
  if (!v || typeof v !== 'string' || v.startsWith('0001-')) return null
  const d = new Date(v)
  return isNaN(d.getTime()) ? null : d
}

export function isInForce(w, now = Date.now()) {
  if (!w || w.cancelled || w.msg_type === 'Cancel') return false
  const exp = expiry(w)
  if (!exp) {
    const sent = new Date(w.sent).getTime()
    return !isNaN(sent) && now - sent <= NO_EXPIRY_IN_FORCE_MS
  }
  return exp.getTime() > now
}

// bearers is the row's bearers column (a JSON array as a string) as an array.
export function bearers(w) {
  const v = w && w.bearers
  if (Array.isArray(v)) return v
  if (typeof v !== 'string' || !v) return []
  try {
    const parsed = JSON.parse(v)
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}

// headline is the first line the mesh gets (warnings.headline), without the
// warning's own words: `[TEST ][UPDATE: ]WARNING <event> <severity>[ until
// HH:MM UTC]`, or `[TEST ]CANCELLED: <event>` for a Cancel.
export function headline(w) {
  if (!w) return ''
  let s = w.test ? 'TEST ' : ''
  if (w.msg_type === 'Cancel') return s + 'CANCELLED: ' + (w.event || 'Other')
  if (w.msg_type === 'Update') s += 'UPDATE: '
  s += 'WARNING ' + (w.event || 'Other') + ' ' + (w.severity || 'Unknown')
  const exp = expiry(w)
  if (exp) s += ' until ' + exp.toISOString().slice(11, 16) + ' UTC'
  return s
}

// The source as the row names it (the Hub's feed id) to the sender a person
// would recognise (warnings.SourceLabel); the label line is LabelFor.
const SOURCE_LABELS = { dwd: 'DWD', 'meteoalarm-nl': 'KNMI via MeteoAlarm', test: 'MeshSat test' }
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

export function sourceLabel(source) {
  return SOURCE_LABELS[source] || 'unknown source'
}

export function label(w) {
  if (!w) return ''
  const d = new Date(w.sent)
  const issued = isNaN(d.getTime()) ? '' : `${d.getUTCDate()} ${MONTHS[d.getUTCMonth()]} ${d.toISOString().slice(11, 16)}`
  return `Relay of ${sourceLabel(w.source)} warning, issued ${issued} UTC via MeshSat, not an official NL-Alert`
}

export const useWarningsStore = defineStore('warnings', () => {
  const rows = ref([])
  const loaded = ref(false)
  const error = ref('')
  const now = ref(Date.now())
  const streamUp = ref(false)

  let users = 0
  let stream = null
  let pollTimer = null
  let tickTimer = null
  let reading = null

  // inForce is what a person at this kit should still see, newest sent
  // first; the banner shows it.
  const inForce = computed(() => rows.value
    .filter(w => isInForce(w, now.value))
    .sort((a, b) => new Date(b.sent) - new Date(a.sent)))

  async function fetch() {
    if (reading) return reading
    reading = (async () => {
      try {
        const list = await api.get('/warnings', { limit: 500 })
        rows.value = Array.isArray(list) ? list : []
        error.value = ''
      } catch (e) {
        error.value = e.message || 'could not read the warnings'
      } finally {
        loaded.value = true
        now.value = Date.now()
        reading = null
      }
    })()
    return reading
  }

  // noteEvent takes one event of the Bridge's stream. A `warning` event is
  // the row the relay kept (plus label and expired): it goes into the list
  // at once, then the list is read again, the read being the authority.
  function noteEvent(ev) {
    if (!ev || ev.type !== 'warning') return
    let data = ev.data
    if (typeof data === 'string') { try { data = JSON.parse(data) } catch { data = null } }
    if (data && data.id && data.msg_type) {
      const { label: _label, expired: _expired, ...row } = data
      const i = rows.value.findIndex(w => w.id === row.id && w.msg_type === row.msg_type)
      if (i >= 0) rows.value.splice(i, 1, { ...rows.value[i], ...row })
      else rows.value.unshift(row)
      now.value = Date.now()
    }
    fetch()
  }

  function open() {
    if (stream || typeof EventSource === 'undefined') return
    try {
      stream = api.sse('/events', (ev) => {
        if (ev && ev.type === 'connected_to_stream') { streamUp.value = true; fetch(); return }
        noteEvent(ev)
      }, () => { streamUp.value = false })
    } catch {
      stream = null
    }
  }

  function close() {
    if (stream) stream.close()
    stream = null
    streamUp.value = false
  }

  function start() {
    users++
    if (users > 1) return
    fetch()
    open()
    pollTimer = setInterval(fetch, POLL_MS)
    tickTimer = setInterval(() => { now.value = Date.now() }, TICK_MS)
  }

  function stop() {
    if (users > 0) users--
    if (users > 0) return
    close()
    if (pollTimer) clearInterval(pollTimer)
    if (tickTimer) clearInterval(tickTimer)
    pollTimer = null
    tickTimer = null
  }

  return { rows, loaded, error, now, streamUp, inForce, fetch, noteEvent, start, stop }
})
