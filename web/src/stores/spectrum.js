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

// After a user ACKs an alert for a band, suppress the modal popup for
// that band for this many milliseconds. Prevents the "flapping false
// positive" scenario where a noisy band (e.g. LoRa EU868 with real
// sensor traffic) flips clear->jamming->clear->jamming and each new
// transition defeats the ACK. The waterfall + CoT/hub relays still
// fire during the mute — only the modal is suppressed.
const ACK_MUTE_MS = 15 * 60 * 1000

const LS_POPUP_ENABLED = 'meshsat-spectrum-popup-enabled'
const LS_MUTED_BANDS = 'meshsat-spectrum-muted-bands'

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

  // popupEnabled is the master kill-switch for the sticky modal. When
  // false, alerts are still collected (so the waterfall highlights
  // jammed bands and CoT/hub relays still fire server-side) but the
  // modal stays hidden. Persisted so the preference survives reload.
  const popupEnabled = ref(loadPopupEnabled())

  // mutedUntil maps band name -> ms-epoch before which the modal will
  // not pop that band. Set on ACK to break the false-positive flap
  // loop; persisted because the flap is driven by the physical RF
  // environment and often persists across page reloads too.
  const mutedUntil = ref(loadMutedBands())

  // paused freezes the waterfall rolling buffer so the operator can
  // inspect a moment in time without new scans scrolling it away.
  // SSE stream still runs and transitions are still tracked (so alerts
  // + CoT/hub relay continue to work); only the rows ring is frozen.
  const paused = ref(false)
  function togglePause() { paused.value = !paused.value }
  function setPaused(v) { paused.value = !!v }

  function loadPopupEnabled() {
    try {
      const raw = localStorage.getItem(LS_POPUP_ENABLED)
      if (raw === null) return true
      return raw === 'true'
    } catch { return true }
  }
  function persistPopupEnabled() {
    try { localStorage.setItem(LS_POPUP_ENABLED, String(popupEnabled.value)) } catch {}
  }
  function loadMutedBands() {
    try {
      const raw = JSON.parse(localStorage.getItem(LS_MUTED_BANDS) || '{}')
      // drop stale entries whose mute already expired — no point holding them
      const now = Date.now()
      const cleaned = {}
      for (const [b, until] of Object.entries(raw)) {
        if (typeof until === 'number' && until > now) cleaned[b] = until
      }
      return cleaned
    } catch { return {} }
  }
  function persistMutedBands() {
    try { localStorage.setItem(LS_MUTED_BANDS, JSON.stringify(mutedUntil.value)) } catch {}
  }
  function bandMuted(band) {
    const until = mutedUntil.value[band]
    return typeof until === 'number' && until > Date.now()
  }

  // activeAlerts is what the modal renders — not acked, not muted,
  // and the global popup toggle is on.
  const activeAlerts = computed(() => {
    if (!popupEnabled.value) return []
    return alerts.value.filter(a => !a.acked && !bandMuted(a.band))
  })

  // Any non-acked alerts at all — for the widget's badge (we want the
  // widget to show a red state even if the modal is silenced).
  const anyActiveAlert = computed(() =>
    alerts.value.some(a => !a.acked)
  )

  function setPopupEnabled(v) {
    popupEnabled.value = !!v
    persistPopupEnabled()
  }
  function muteBand(band, ms = ACK_MUTE_MS) {
    mutedUntil.value = { ...mutedUntil.value, [band]: Date.now() + ms }
    persistMutedBands()
  }
  function unmuteBand(band) {
    const copy = { ...mutedUntil.value }
    delete copy[band]
    mutedUntil.value = copy
    persistMutedBands()
  }
  function unmuteAll() {
    mutedUntil.value = {}
    persistMutedBands()
  }

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
        calibrationStartedAt: null,
        calibrationDurationSec: 30,
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
    // calibration_started_at arrives on Phase 1 events only; clear on
    // Phase 2 (state != calibrating) so the UI stops showing the bar.
    if (evt.calibration_started_at) {
      b.calibrationStartedAt = new Date(evt.calibration_started_at)
      b.calibrationDurationSec = evt.calibration_duration_sec || 30
    } else if (evt.state !== 'calibrating') {
      b.calibrationStartedAt = null
      b.calibrationDurationSec = 0
    }
    // Paused: keep state/baseline fresh (so the alert badge is
    // accurate) but don't push the scan into the rows ring — freezes
    // the waterfall visualisation for inspection.
    if (paused.value) return
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
    // Mute so the next transition doesn't immediately re-pop — this
    // is the core fix for LoRa EU868 and similar bands where real
    // traffic flaps the state classifier across the 3σ threshold.
    muteBand(band)
  }

  function ackAll() {
    const now = new Date().toISOString()
    alerts.value.forEach(a => {
      if (!a.acked) {
        a.acked = true
        a.ackedAt = now
        muteBand(a.band)
      }
    })
  }

  // Seed the band list + current state from /api/spectrum/status so
  // the UI shows the 5 configured bands (in whatever state they are
  // in — typically "calibrating" right after a deploy restart) BEFORE
  // the SSE stream starts emitting scan events. Without this, the
  // waterfall sits on "No spectrum data" for the 2.5-min calibration
  // window after every container restart, which looks broken.
  async function seedFromStatus() {
    try {
      const resp = await fetch('/api/spectrum/status', { credentials: 'same-origin' })
      if (!resp.ok) {
        if (resp.status === 503) enabled.value = false
        return
      }
      const data = await resp.json()
      if (data && typeof data.enabled === 'boolean') enabled.value = data.enabled
      if (!Array.isArray(data?.bands)) return
      const next = { ...bands.value }
      for (const b of data.bands) {
        const existing = next[b.band] || { rows: [] }
        next[b.band] = {
          meta: existing.meta || {
            band: b.band,
            label: b.label,
            interfaceID: b.interface_id,
            freqLow: b.freq_low,
            freqHigh: b.freq_high,
            binSize: 0, // filled in by first scan event
          },
          rows: existing.rows || [],
          state: b.state || 'calibrating',
          baselineMean: b.baseline_mean || 0,
          baselineStd: b.baseline_std || 0,
          threshJamming: b.baseline_mean && b.baseline_std
            ? b.baseline_mean + 3 * b.baseline_std
            : 0,
          threshInterference: b.baseline_mean && b.baseline_std
            ? b.baseline_mean + 6 * b.baseline_std
            : 0,
          // Calibration progress fields come from the /api/spectrum/status
          // poll only — scan-event payloads don't carry them.
          // calibration_started_at arrives as an RFC3339 string or absent
          // (zero-valued, omitempty). We parse to a Date so the
          // countdown computation is cheap.
          calibrationStartedAt: b.calibration_started_at ? new Date(b.calibration_started_at) : null,
          calibrationDurationSec: b.calibration_duration_sec || 30,
        }
      }
      bands.value = next
    } catch {
      // network error — SSE reconnect loop will try again via schedule
    }
  }

  function connect() {
    if (es) return
    // Fire-and-forget the status seed alongside opening the SSE — both
    // are cheap and the fetch call resolves in <100ms on a local kit.
    seedFromStatus()
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
    anyActiveAlert,
    popupEnabled,
    mutedUntil,
    paused,
    togglePause,
    setPaused,
    connect,
    disconnect,
    ackAlert,
    ackAll,
    setPopupEnabled,
    muteBand,
    unmuteBand,
    unmuteAll,
    bandMuted,
    WATERFALL_ROWS,
    ACK_MUTE_MS,
  }
})
