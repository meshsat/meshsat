// Pinia store backing the spectrum waterfall + jamming alert modal.
//
// Owns:
//   - the single SSE connection to /api/spectrum/stream (shared by all
//     mounted components so we don't open N streams)
//   - a rolling buffer of per-bin power rows per band (the waterfall reads
//     this directly; length capped at WATERFALL_ROWS so memory stays
//     bounded)
//   - the list of jamming alerts that still need an operator ACK (the
//     modal reads this; alerts stay visible even after the band returns
//     to clear, until the operator explicitly acknowledges — that is
//     the "sticky until ack" UX the user specified for EW detection).
import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

// How many scan rows to keep per band in the rolling waterfall buffer.
// At 3 s per scan this is ~5 minutes of history, enough to see the onset
// and duration of a jamming event without eating browser memory.
const WATERFALL_ROWS = 100

export const useSpectrumStore = defineStore('spectrum', () => {
  // bands maps band name -> { meta, rows, state, baseline, thresholds }.
  // rows is a ring of {ts, powers, avg, max} — newest at index 0.
  const bands = ref({})
  const connected = ref(false)
  const enabled = ref(true)  // flipped to false if the server returns 503

  // alerts is the list of jamming events that still need an ACK. Each
  // entry: { band, label, state, startedAt, clearedAt, acked, peakDB,
  // baselineDB, freqLow, freqHigh, interfaceID }.
  // clearedAt is populated when the band returns to clear but the entry
  // stays visible until acked.
  const alerts = ref([])

  // activeAlerts computed is what the modal renders — anything not acked.
  const activeAlerts = computed(() =>
    alerts.value.filter(a => !a.acked)
  )

  let es = null
  let reconnectTimer = null

  function ensureBand(evt) {
    if (!bands.value[evt.band]) {
      bands.value[evt.band] = {
        meta: {
          band: evt.band,
          label: evt.label,
          interfaceID: evt.interface_id,
          freqLow: evt.freq_low,
          freqHigh: evt.freq_high,
          binSize: evt.bin_size,
        },
        rows: [],
        state: evt.state || 'calibrating',
        baselineMean: evt.baseline_mean || 0,
        baselineStd: evt.baseline_std || 0,
        threshJamming: evt.thresh_jamming_db || 0,
        threshInterference: evt.thresh_interference_db || 0,
      }
    }
    return bands.value[evt.band]
  }

  function handleScan(evt) {
    const b = ensureBand(evt)
    b.state = evt.state
    b.baselineMean = evt.baseline_mean
    b.baselineStd = evt.baseline_std
    b.threshJamming = evt.thresh_jamming_db
    b.threshInterference = evt.thresh_interference_db
    // Prepend newest at index 0; drop the tail past the cap. Keeping the
    // ring bounded matters — without this, a browser tab left open for a
    // few days would leak hundreds of MB of power arrays.
    b.rows.unshift({
      ts: evt.timestamp,
      powers: evt.powers || [],
      avg: evt.avg_db,
      max: evt.max_db,
    })
    if (b.rows.length > WATERFALL_ROWS) {
      b.rows.length = WATERFALL_ROWS
    }
  }

  function handleTransition(evt) {
    const b = ensureBand(evt)
    b.state = evt.state

    const nonClearStates = ['jamming', 'interference']
    const wasBad = nonClearStates.includes(evt.old_state)
    const isBad = nonClearStates.includes(evt.state)

    if (isBad && !wasBad) {
      // clear -> jamming/interference: new alert (unless we somehow
      // already have an unacked one for this band — dedupe by band).
      const existing = alerts.value.find(a => a.band === evt.band && !a.acked)
      if (!existing) {
        alerts.value.unshift({
          band: evt.band,
          label: evt.label,
          interfaceID: evt.interface_id,
          freqLow: evt.freq_low,
          freqHigh: evt.freq_high,
          state: evt.state,
          startedAt: evt.timestamp,
          clearedAt: null,
          peakDB: evt.max_db,
          powerDB: evt.avg_db,
          baselineDB: evt.baseline_mean,
          acked: false,
        })
      }
    } else if (wasBad && !isBad) {
      // jamming/interference -> clear: mark clearedAt but keep it in
      // the list so the modal can show "recovered, awaiting ACK".
      const a = alerts.value.find(x => x.band === evt.band && !x.acked && !x.clearedAt)
      if (a) {
        a.clearedAt = evt.timestamp
      }
    } else if (isBad && wasBad && evt.state !== evt.old_state) {
      // jamming <-> interference: escalation, bump the existing alert's
      // state and peak rather than creating a second entry.
      const a = alerts.value.find(x => x.band === evt.band && !x.acked)
      if (a) {
        a.state = evt.state
        if (evt.max_db > a.peakDB) a.peakDB = evt.max_db
      }
    }
  }

  function ackAlert(band) {
    const a = alerts.value.find(x => x.band === band && !x.acked)
    if (a) {
      a.acked = true
      a.ackedAt = new Date().toISOString()
    }
  }

  function ackAll() {
    const now = new Date().toISOString()
    alerts.value.forEach(a => {
      if (!a.acked) {
        a.acked = true
        a.ackedAt = now
      }
    })
  }

  function connect() {
    if (es) return
    try {
      es = new EventSource('/api/spectrum/stream')
    } catch (e) {
      // Browsers in insecure / file contexts may throw. Fall back to a
      // reconnect loop rather than crashing the dashboard.
      enabled.value = false
      scheduleReconnect()
      return
    }
    es.onopen = () => {
      connected.value = true
    }
    es.onerror = () => {
      connected.value = false
      // Server returns 503 when rtl_power/dongle isn't available. Flag
      // the UI so the waterfall shows a "hardware not present" state
      // instead of endlessly retrying behind a 503.
      if (es && es.readyState === EventSource.CLOSED) {
        enabled.value = false
      }
      closeES()
      scheduleReconnect()
    }
    // Explicit event types from the backend — handler per type so we
    // don't pay the cost of a switch statement on every scan tick.
    es.addEventListener('scan', (msg) => {
      try { handleScan(JSON.parse(msg.data)) } catch {}
    })
    es.addEventListener('transition', (msg) => {
      try { handleTransition(JSON.parse(msg.data)) } catch {}
    })
  }

  function scheduleReconnect() {
    if (reconnectTimer) return
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null
      connect()
    }, 5000)
  }

  function closeES() {
    if (es) {
      try { es.close() } catch {}
      es = null
    }
  }

  function disconnect() {
    closeES()
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null }
    connected.value = false
  }

  return {
    bands,
    connected,
    enabled,
    alerts,
    activeAlerts,
    connect,
    disconnect,
    ackAlert,
    ackAll,
    WATERFALL_ROWS,
  }
})
