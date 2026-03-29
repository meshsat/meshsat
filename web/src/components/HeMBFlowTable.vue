<script setup>
import { ref, computed } from 'vue'
import { useHeMBStore } from '../composables/useHeMBStore'
import { useHeMBSelection } from '../composables/useHeMBSelection'
import { useVirtualScroll } from '../composables/useVirtualScroll'
import HeMBGenerationDetail from './HeMBGenerationDetail.vue'

const store = useHeMBStore()
const selection = useHeMBSelection()

const filterType = ref('')
const scrollContainer = ref(null)

const eventIcons = {
  HEMB_SYMBOL_SENT: { icon: '\u2191', color: 'text-cyan-400 bg-cyan-400/10', label: 'SENT' },
  HEMB_SYMBOL_RECEIVED: { icon: '\u2193', color: 'text-teal-400 bg-teal-400/10', label: 'RECV' },
  HEMB_GENERATION_DECODED: { icon: '\u2713', color: 'text-emerald-400 bg-emerald-400/10', label: 'DECODED' },
  HEMB_GENERATION_FAILED: { icon: '\u2717', color: 'text-red-400 bg-red-400/10', label: 'FAILED' },
  HEMB_BEARER_DEGRADED: { icon: '\u26A0', color: 'text-amber-400 bg-amber-400/10', label: 'DEGRADED' },
  HEMB_BEARER_RECOVERED: { icon: '\u2191', color: 'text-emerald-400 bg-emerald-400/10', label: 'RECOVERED' },
  HEMB_STREAM_OPENED: { icon: '\u25B6', color: 'text-blue-400 bg-blue-400/10', label: 'OPENED' },
  HEMB_STREAM_CLOSED: { icon: '\u25A0', color: 'text-gray-400 bg-gray-400/10', label: 'CLOSED' },
  HEMB_BOND_STATS: { icon: '\u2593', color: 'text-purple-400 bg-purple-400/10', label: 'STATS' },
}

function getEventStyle(type) {
  return eventIcons[type] || { icon: '?', color: 'text-gray-500 bg-gray-500/10', label: type }
}

function parsePayload(event) {
  if (!event.payload) return {}
  if (typeof event.payload === 'string') {
    try { return JSON.parse(event.payload) } catch { return {} }
  }
  return event.payload
}

function formatTime(ts) {
  if (!ts) return ''
  const d = new Date(ts)
  return d.toLocaleTimeString('en-GB', { hour12: false, fractionalSecondDigits: 1 })
}

const filteredEvents = computed(() => {
  let evts = store.events.value
  if (filterType.value) {
    evts = evts.filter(e => e.type === filterType.value)
  }
  if (selection.filterActive.value) {
    evts = evts.filter(selection.eventFilter.value)
  }
  return evts
})

const { visibleItems, topPadding, bottomPadding, onScroll, visibleRange } = useVirtualScroll(filteredEvents, 36, scrollContainer)

const expandedRow = ref(null)
function toggleExpand(idx) {
  expandedRow.value = expandedRow.value === idx ? null : idx
}

function handleRowClick(event, idx) {
  selection.selectFromTable(event)
  toggleExpand(idx)
}
</script>

<template>
  <div class="flex flex-col h-full">
    <!-- Filter bar -->
    <div class="flex items-center gap-3 px-4 py-2 border-b border-gray-800 text-xs">
      <select v-model="filterType"
        class="bg-gray-800 text-gray-300 border border-gray-700 rounded px-2 py-1 text-xs">
        <option value="">All Events</option>
        <option v-for="(v, k) in eventIcons" :key="k" :value="k">{{ v.label }}</option>
      </select>

      <div v-if="selection.filterActive.value"
        class="flex items-center gap-1 px-2 py-0.5 bg-teal-900/30 border border-teal-700/50 rounded text-teal-400">
        <span>Filtered</span>
        <button @click="selection.clear()" class="ml-1 hover:text-white">&times;</button>
      </div>

      <span class="text-gray-500 ml-auto">{{ filteredEvents.length }} events</span>

      <button @click="store.togglePause()"
        class="px-2 py-0.5 rounded text-xs"
        :class="store.paused ? 'bg-amber-900/30 text-amber-400' : 'bg-gray-800 text-gray-400 hover:text-white'">
        {{ store.paused ? `Resume (${store.pendingCount})` : 'Pause' }}
      </button>
    </div>

    <!-- Table with virtual scroll -->
    <div ref="scrollContainer" class="flex-1 overflow-auto tactical-scroll" @scroll="onScroll">
      <table class="w-full text-xs">
        <thead class="sticky top-0 bg-gray-900/95 backdrop-blur-sm z-10">
          <tr class="text-gray-500 uppercase tracking-wider text-[10px]">
            <th class="px-3 py-1.5 text-left w-20">Time</th>
            <th class="px-2 py-1.5 text-left w-24">Event</th>
            <th class="px-2 py-1.5 text-left w-12">Stream</th>
            <th class="px-2 py-1.5 text-left w-12 hidden md:table-cell">Gen</th>
            <th class="px-2 py-1.5 text-left w-20">Bearer</th>
            <th class="px-2 py-1.5 text-left w-16 hidden lg:table-cell">Latency</th>
            <th class="px-2 py-1.5 text-left w-14 hidden lg:table-cell">Cost</th>
          </tr>
        </thead>
        <tbody>
          <!-- Virtual scroll top padding -->
          <tr v-if="topPadding > 0" :style="{ height: topPadding + 'px' }"><td colspan="7"></td></tr>
          <template v-for="(event, idx) in visibleItems" :key="event.ts + '-' + (visibleRange?.start + idx)">
            <tr class="border-t border-gray-800/50 hover:bg-white/[0.02] cursor-pointer"
              style="height: 36px"
              @click="handleRowClick(event, visibleRange?.start + idx)"
              @mouseenter="selection.highlightFromTable(event)"
              @mouseleave="selection.highlightFromTable(null)">
              <td class="px-3 py-1.5 font-mono text-gray-500">{{ formatTime(event.ts) }}</td>
              <td class="px-2 py-1.5">
                <span class="inline-flex items-center gap-1 px-1.5 py-px rounded text-[10px] font-mono"
                  :class="getEventStyle(event.type).color">
                  {{ getEventStyle(event.type).icon }}
                  {{ getEventStyle(event.type).label }}
                </span>
              </td>
              <td class="px-2 py-1.5 font-mono text-gray-300">
                S{{ parsePayload(event).stream_id ?? '-' }}
              </td>
              <td class="px-2 py-1.5 font-mono text-gray-400 hidden md:table-cell">
                G{{ parsePayload(event).gen_id ?? '-' }}
              </td>
              <td class="px-2 py-1.5 font-mono text-gray-400">
                {{ parsePayload(event).bearer_type || (parsePayload(event).bearer_idx ?? '') }}
              </td>
              <td class="px-2 py-1.5 font-mono hidden lg:table-cell"
                :class="(parsePayload(event).latency_ms ?? 0) > 5000 ? 'text-red-400' :
                        (parsePayload(event).latency_ms ?? 0) > 500 ? 'text-amber-400' : 'text-emerald-400'">
                {{ parsePayload(event).latency_ms != null ? parsePayload(event).latency_ms + 'ms' : '' }}
              </td>
              <td class="px-2 py-1.5 font-mono hidden lg:table-cell"
                :class="(parsePayload(event).cost_est ?? 0) > 0 ? 'text-orange-400' : 'text-emerald-400'">
                {{ (parsePayload(event).cost_est ?? 0) > 0 ? '$' + parsePayload(event).cost_est.toFixed(3) : 'free' }}
              </td>
            </tr>
            <!-- Expanded detail -->
            <tr v-if="expandedRow === (visibleRange?.start + idx)" class="bg-gray-800/30">
              <td colspan="7" class="px-4 py-3">
                <HeMBGenerationDetail :event="event" />
              </td>
            </tr>
          </template>
          <!-- Virtual scroll bottom padding -->
          <tr v-if="bottomPadding > 0" :style="{ height: bottomPadding + 'px' }"><td colspan="7"></td></tr>
          <tr v-if="filteredEvents.length === 0">
            <td colspan="7" class="px-4 py-8 text-center text-gray-600 text-sm">
              No HeMB events yet. Configure a bond group and send bonded traffic.
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
