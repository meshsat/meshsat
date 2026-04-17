<!--
  JammingAlertModal.vue
  ---------------------
  Sticky-until-ACK modal for RTL-SDR jamming detection. Mounted once in
  App.vue so it surfaces on any route — the user asked for this because
  EW detection is time-critical and must not be silently missed while
  the operator is on a different tab.

  Behaviour:
    * Shown whenever store.activeAlerts is non-empty.
    * Listing one row per non-acknowledged alert, with state + band +
      freq + power + started-at + cleared-at (or "ongoing").
    * Each row has an ACK button; a footer ACK ALL button is available
      when >1 alert.
    * Acked alerts are removed from activeAlerts and therefore the
      modal — but if a new transition fires (e.g. band flaps back to
      jamming), a fresh alert appears and the modal returns.
    * No auto-dismiss: the user must explicitly ACK.
-->
<script setup>
import { computed } from 'vue'
import { useSpectrumStore } from '@/stores/spectrum'

const store = useSpectrumStore()

const open = computed(() => store.activeAlerts.length > 0)
const alerts = computed(() => store.activeAlerts)

function fmtFreqRange(low, high) {
  const lowMHz = (low / 1e6).toFixed(2)
  const highMHz = (high / 1e6).toFixed(2)
  return `${lowMHz}-${highMHz} MHz`
}

function fmtTs(iso) {
  if (!iso) return '—'
  try {
    return new Date(iso).toLocaleTimeString()
  } catch {
    return iso
  }
}

function durationSince(iso) {
  if (!iso) return ''
  try {
    const ms = Date.now() - new Date(iso).getTime()
    const s = Math.max(0, Math.floor(ms / 1000))
    if (s < 60) return `${s}s`
    if (s < 3600) return `${Math.floor(s / 60)}m${s % 60}s`
    return `${Math.floor(s / 3600)}h${Math.floor((s % 3600) / 60)}m`
  } catch { return '' }
}
</script>

<template>
  <Teleport to="body">
    <transition name="alert-fade">
      <div v-if="open" class="alert-backdrop" role="alertdialog" aria-modal="true">
        <div class="alert-card">
          <div class="alert-header">
            <div class="alert-badge">RF ALERT</div>
            <h2>RTL-SDR jamming detected</h2>
            <p class="alert-sub">Operator acknowledgement required. TAK/CoT and hub relays have been sent.</p>
          </div>
          <div class="alert-list">
            <div v-for="a in alerts" :key="a.band + a.startedAt"
                 class="alert-row" :class="'state-' + a.state">
              <div class="alert-state">{{ a.state }}</div>
              <div class="alert-meta">
                <div class="label">{{ a.label }} <span class="band">({{ a.band }})</span></div>
                <div class="sub">
                  <span>iface: {{ a.interfaceID }}</span>
                  <span>{{ fmtFreqRange(a.freqLow, a.freqHigh) }}</span>
                  <span>peak: {{ a.peakDB?.toFixed?.(1) }} dB</span>
                  <span>Δbase: {{ (a.powerDB - a.baselineDB)?.toFixed?.(1) }} dB</span>
                </div>
                <div class="timing">
                  <span>started {{ fmtTs(a.startedAt) }} ({{ durationSince(a.startedAt) }} ago)</span>
                  <span v-if="a.clearedAt" class="recovered">
                    recovered at {{ fmtTs(a.clearedAt) }}
                  </span>
                  <span v-else class="ongoing">still active</span>
                </div>
              </div>
              <button type="button" class="ack-btn" @click="store.ackAlert(a.band)">
                ACK
              </button>
            </div>
          </div>
          <div class="alert-footer">
            <button v-if="alerts.length > 1" type="button" class="ack-all" @click="store.ackAll">
              ACK ALL {{ alerts.length }}
            </button>
            <span class="footer-hint">Press ACK to dismiss once you have reviewed the event.</span>
          </div>
        </div>
      </div>
    </transition>
  </Teleport>
</template>

<style scoped>
.alert-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(15, 23, 42, 0.78);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 9999;
  padding: 20px;
}
.alert-card {
  background: #0b1220;
  border: 2px solid #dc2626;
  border-radius: 8px;
  color: #e2e8f0;
  max-width: 680px;
  width: 100%;
  box-shadow: 0 0 32px rgba(220, 38, 38, 0.6);
  animation: pulse-shadow 1.6s infinite alternate;
}
@keyframes pulse-shadow {
  from { box-shadow: 0 0 20px rgba(220, 38, 38, 0.35); }
  to   { box-shadow: 0 0 48px rgba(220, 38, 38, 0.85); }
}
.alert-header {
  padding: 14px 18px;
  border-bottom: 1px solid #1e293b;
}
.alert-badge {
  display: inline-block;
  padding: 2px 8px;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.1em;
  background: #dc2626;
  color: white;
  border-radius: 3px;
  margin-bottom: 6px;
}
.alert-header h2 {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
}
.alert-sub {
  margin: 4px 0 0;
  font-size: 12px;
  color: #94a3b8;
}
.alert-list {
  max-height: 50vh;
  overflow-y: auto;
  padding: 4px 0;
}
.alert-row {
  display: grid;
  grid-template-columns: 90px 1fr auto;
  gap: 10px;
  align-items: center;
  padding: 10px 18px;
  border-bottom: 1px solid #1e293b;
}
.alert-row:last-child { border-bottom: none; }
.alert-state {
  font-size: 11px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  padding: 4px 6px;
  border-radius: 3px;
  text-align: center;
}
.alert-row.state-jamming .alert-state { background: #dc2626; color: white; }
.alert-row.state-interference .alert-state { background: #f59e0b; color: #0b0b0b; }
.alert-meta .label { font-size: 13px; font-weight: 600; }
.alert-meta .label .band { color: #94a3b8; font-weight: normal; font-family: monospace; }
.alert-meta .sub {
  font-size: 11px;
  color: #94a3b8;
  margin-top: 3px;
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}
.alert-meta .timing {
  font-size: 11px;
  color: #64748b;
  margin-top: 3px;
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}
.alert-meta .ongoing { color: #dc2626; font-weight: 600; }
.alert-meta .recovered { color: #10b981; }
.ack-btn {
  padding: 8px 18px;
  border-radius: 4px;
  background: #1e40af;
  color: white;
  border: none;
  font-weight: 700;
  letter-spacing: 0.05em;
  cursor: pointer;
}
.ack-btn:hover { background: #2563eb; }
.alert-footer {
  padding: 12px 18px;
  border-top: 1px solid #1e293b;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.ack-all {
  padding: 8px 14px;
  border-radius: 4px;
  background: #dc2626;
  color: white;
  border: none;
  font-weight: 700;
  cursor: pointer;
  letter-spacing: 0.05em;
}
.ack-all:hover { background: #b91c1c; }
.footer-hint { font-size: 11px; color: #64748b; }

.alert-fade-enter-active, .alert-fade-leave-active {
  transition: opacity 180ms ease;
}
.alert-fade-enter-from, .alert-fade-leave-to { opacity: 0; }
</style>
