// One event stream for every delivery status card on the page. The bridge
// names an intent in a `delivery_intent` or `delivery_attempt` event
// whenever it writes about it (a new intent, a handoff, an attempt's end,
// matched evidence, a transition); a card watching that intent reads it
// again at once. The stream is opened by the first card and closed with the
// last. Events are hints: the card's read is the authority, and after the
// stream reconnects every watched card reads again, since events in the gap
// are lost. [MESHSAT-1584]

const listeners = new Map() // intent id -> Set of callbacks
let source = null
let opened = false

function call(fns) {
  for (const fn of [...fns]) {
    try { fn() } catch { /* one card's failure is its own */ }
  }
}

function onMessage(e) {
  let ev
  try { ev = JSON.parse(e.data) } catch { return }
  if (!ev || (ev.type !== 'delivery_intent' && ev.type !== 'delivery_attempt')) return
  const id = ev.data?.intent_id
  if (id && listeners.has(id)) call(listeners.get(id))
}

function onOpen() {
  if (opened) for (const fns of listeners.values()) call(fns)
  opened = true
}

function open() {
  if (source || typeof EventSource === 'undefined') return
  try {
    source = new EventSource('/api/events')
  } catch {
    source = null
    return
  }
  opened = false
  source.onmessage = onMessage
  source.onopen = onOpen
}

function close() {
  if (source) source.close()
  source = null
  opened = false
}

// watchIntent calls fn whenever the bridge says it wrote about intent id;
// the returned function stops watching.
export function watchIntent(id, fn) {
  let fns = listeners.get(id)
  if (!fns) listeners.set(id, fns = new Set())
  fns.add(fn)
  open()
  return () => {
    fns.delete(fn)
    if (!fns.size && listeners.get(id) === fns) listeners.delete(id)
    if (!listeners.size) close()
  }
}
