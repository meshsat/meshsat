<!--
  GatewayRateSparkline — tiny SVG sparkline for gateway widget headers.

  Consumes the client-side sliding window of /api/gateways samples
  maintained in stores/meshsat.js :: pushGatewayRateSample. Each bar is
  the delta msgs_in + msgs_out between consecutive samples, clamped so
  a single burst doesn't squash the rest.

  Props:
    - samples: array of { ts, inCount, outCount } ordered oldest → newest
    - variant: 'mesh' | 'aprs' | 'cellular' | 'iridium' | 'tak' | 'zigbee' | 'bond' (colour token)

  Empty state renders a dashed baseline so the widget has a consistent
  height regardless of traffic. [MESHSAT-686]
-->
<template>
  <div class="flex items-center gap-1.5 min-w-[64px]" :title="tooltip">
    <svg viewBox="0 0 60 16" preserveAspectRatio="none" class="w-16 h-4">
      <line x1="0" x2="60" y1="15" y2="15" stroke="#374151" stroke-width="0.5" stroke-dasharray="1 2" />
      <rect
        v-for="(bar, i) in bars"
        :key="'b' + i"
        :x="i * (60 / maxBars)"
        :y="14 - bar.h * 13"
        :width="Math.max(1, 60 / maxBars - 0.5)"
        :height="Math.max(0.5, bar.h * 13)"
        rx="0.5"
        :class="barClass"
      />
    </svg>
    <span class="text-[9px] font-mono text-gray-500 shrink-0">{{ rateLabel }}</span>
  </div>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  samples: { type: Array, default: () => [] },
  variant: { type: String, default: 'mesh' },
})

const maxBars = 15

const bars = computed(() => {
  const s = props.samples
  if (s.length < 2) return []
  // Compute per-interval deltas (msgs over the previous poll window).
  const deltas = []
  for (let i = 1; i < s.length; i++) {
    const dIn = Math.max(0, (s[i].inCount || 0) - (s[i - 1].inCount || 0))
    const dOut = Math.max(0, (s[i].outCount || 0) - (s[i - 1].outCount || 0))
    deltas.push(dIn + dOut)
  }
  // Keep only the last maxBars
  const tail = deltas.slice(-maxBars)
  // Normalise against the local max so bars are comparable, but keep a
  // floor of 1 to avoid division-by-zero and to leave flat-zero windows
  // readable as a row of empty tiles.
  const localMax = Math.max(1, ...tail)
  return tail.map((d) => ({ d, h: d / localMax }))
})

const rateLabel = computed(() => {
  if (bars.value.length === 0) return '—'
  const total = bars.value.reduce((a, b) => a + b.d, 0)
  // Each bar ≈ 4s poll interval; sum over N bars ≈ N*4s window.
  const seconds = bars.value.length * 4
  const perMin = (total * 60) / seconds
  if (perMin < 0.1) return '0/min'
  if (perMin < 10) return perMin.toFixed(1) + '/min'
  return Math.round(perMin) + '/min'
})

const tooltip = computed(() => {
  if (bars.value.length === 0) return 'No recent samples'
  const total = bars.value.reduce((a, b) => a + b.d, 0)
  const seconds = bars.value.length * 4
  return `${total} msg(s) over last ~${seconds}s`
})

const barClass = computed(() => {
  switch (props.variant) {
    case 'aprs':     return 'fill-purple-400/70'
    case 'cellular': return 'fill-amber-400/70'
    case 'iridium':  return 'fill-red-400/70'
    case 'tak':      return 'fill-blue-400/70'
    case 'zigbee':   return 'fill-pink-400/70'
    case 'bond':     return 'fill-teal-400/70'
    case 'mesh':
    default:         return 'fill-emerald-400/70'
  }
})
</script>
