<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const recentActivity = ref([])
const MAX_ACTIVITY = 15
const pulsingHop = ref(null)
let pulseTimeout = null

// Status helpers
const radioConnected = computed(() => store.status?.connected === true)
const nodeName = computed(() => store.status?.node_name || '—')
const nodeCount = computed(() => store.nodes?.length ?? 0)
const totalMessages = computed(() => store.messageStats?.total ?? 0)

const mqttGw = computed(() => store.gateways.find(g => g.type === 'mqtt'))
const iridiumGw = computed(() => store.gateways.find(g => g.type === 'iridium'))

// Hop status logic
const hops = computed(() => [
  {
    id: 'mesh',
    label: 'Mesh Nodes',
    icon: 'mesh',
    status: !radioConnected.value ? 'gray' : nodeCount.value > 0 ? 'green' : 'red',
    detail: radioConnected.value ? `${nodeCount.value} node${nodeCount.value !== 1 ? 's' : ''}` : 'Not connected'
  },
  {
    id: 'hal',
    label: 'HAL Radio',
    icon: 'radio',
    status: radioConnected.value ? 'green' : 'red',
    detail: radioConnected.value ? 'Connected' : 'Disconnected'
  },
  {
    id: 'meshsat',
    label: 'MeshSat',
    icon: 'server',
    status: 'green',
    detail: 'Running'
  }
])

const mqttHop = computed(() => ({
  id: 'mqtt',
  label: 'MQTT Broker',
  icon: 'mqtt',
  status: !mqttGw.value ? 'gray' : mqttGw.value.connected ? 'green' : 'red',
  detail: !mqttGw.value ? 'Not configured' : mqttGw.value.connected ? 'Connected' : 'Disconnected'
}))

const iridiumHop = computed(() => ({
  id: 'iridium',
  label: 'Iridium SBD',
  icon: 'satellite',
  status: !iridiumGw.value ? 'gray' : iridiumGw.value.connected ? 'green' : 'red',
  detail: !iridiumGw.value ? 'Not configured' : iridiumGw.value.connected ? 'Connected' : 'Disconnected'
}))

function statusColor(s) {
  if (s === 'green') return 'bg-emerald-400'
  if (s === 'red') return 'bg-red-400'
  return 'bg-gray-600'
}

function formatActivityTime(t) {
  if (!t) return ''
  return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function eventDescription(event) {
  const type = event?.type ?? ''
  const msg = event?.message ?? ''
  if (type === 'message' || type === 'text') return msg || 'New message received'
  if (type === 'node_update') return msg || 'Node data updated'
  if (type === 'position') return msg || 'Position update received'
  if (type === 'connected') return 'Radio connected'
  if (type === 'disconnected') return 'Radio disconnected'
  if (type === 'config_complete') return 'Config sync complete'
  return msg || type || 'Event'
}

function eventIcon(type) {
  if (type === 'message' || type === 'text') return 'M'
  if (type === 'node_update') return 'N'
  if (type === 'position') return 'P'
  if (type === 'connected') return '+'
  if (type === 'disconnected') return '-'
  return 'E'
}

function triggerPulse(hopId) {
  pulsingHop.value = hopId
  if (pulseTimeout) clearTimeout(pulseTimeout)
  pulseTimeout = setTimeout(() => { pulsingHop.value = null }, 500)
}

function handleSSEEvent(event) {
  const type = event?.type ?? ''
  recentActivity.value.unshift({
    time: new Date().toISOString(),
    type,
    description: eventDescription(event)
  })
  if (recentActivity.value.length > MAX_ACTIVITY) {
    recentActivity.value.length = MAX_ACTIVITY
  }

  // Pulse animation
  if (type === 'message' || type === 'text') {
    triggerPulse('meshsat')
  } else if (type === 'node_update' || type === 'position') {
    triggerPulse('hal')
  }
}

async function fetchAll() {
  await Promise.all([
    store.fetchStatus(),
    store.fetchNodes(),
    store.fetchMessageStats(),
    store.fetchGateways()
  ])
}

onMounted(() => {
  fetchAll()
  store.connectSSE(handleSSEEvent)
})

onUnmounted(() => {
  store.closeSSE()
  if (pulseTimeout) clearTimeout(pulseTimeout)
})
</script>

<template>
  <div class="max-w-5xl mx-auto space-y-6">
    <h1 class="text-2xl font-bold">Dashboard</h1>

    <!-- Status Cards -->
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
      <div class="bg-gray-900 rounded-xl p-4 border border-gray-800">
        <p class="text-xs text-gray-500">Radio Status</p>
        <div class="flex items-center gap-2 mt-1">
          <span class="w-2 h-2 rounded-full" :class="radioConnected ? 'bg-emerald-400' : 'bg-red-400'" />
          <span class="text-sm font-medium" :class="radioConnected ? 'text-emerald-400' : 'text-red-400'">
            {{ radioConnected ? 'Connected' : 'Disconnected' }}
          </span>
        </div>
        <p class="text-xs text-gray-500 mt-1">{{ nodeName }}</p>
      </div>

      <div class="bg-gray-900 rounded-xl p-4 border border-gray-800">
        <p class="text-xs text-gray-500">Nodes Online</p>
        <p class="text-2xl font-bold tabular-nums mt-1">{{ nodeCount }}</p>
      </div>

      <div class="bg-gray-900 rounded-xl p-4 border border-gray-800">
        <p class="text-xs text-gray-500">Messages (total)</p>
        <p class="text-2xl font-bold tabular-nums mt-1">{{ totalMessages }}</p>
      </div>

      <div class="bg-gray-900 rounded-xl p-4 border border-gray-800">
        <p class="text-xs text-gray-500">Gateways</p>
        <div class="flex items-center gap-3 mt-2">
          <div class="flex items-center gap-1.5">
            <span class="w-2 h-2 rounded-full" :class="statusColor(mqttHop.status)" />
            <span class="text-xs text-gray-400">MQTT</span>
          </div>
          <div class="flex items-center gap-1.5">
            <span class="w-2 h-2 rounded-full" :class="statusColor(iridiumHop.status)" />
            <span class="text-xs text-gray-400">Iridium</span>
          </div>
        </div>
      </div>
    </div>

    <!-- Message Flow Diagram -->
    <div class="bg-gray-900 rounded-xl p-5 border border-gray-800">
      <h2 class="text-sm font-semibold text-gray-300 mb-4">Message Flow</h2>

      <!-- Desktop: horizontal -->
      <div class="hidden md:flex items-start gap-0">
        <!-- Main chain: Mesh → HAL → MeshSat -->
        <template v-for="(hop, idx) in hops" :key="hop.id">
          <div class="flex flex-col items-center min-w-[100px]">
            <div class="w-16 h-16 rounded-xl border flex flex-col items-center justify-center transition-all"
                 :class="[
                   hop.status === 'green' ? 'border-emerald-800 bg-emerald-950/30' :
                   hop.status === 'red' ? 'border-red-800 bg-red-950/30' :
                   'border-gray-700 bg-gray-800/50',
                   pulsingHop === hop.id ? 'ring-2 ring-teal-400/50' : ''
                 ]">
              <svg class="w-5 h-5 text-gray-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                <template v-if="hop.icon === 'mesh'">
                  <circle cx="5" cy="6" r="2"/><circle cx="19" cy="6" r="2"/><circle cx="12" cy="18" r="2"/>
                  <path d="M7 6h10M6.5 7.5L11 17M17.5 7.5L13 17"/>
                </template>
                <template v-else-if="hop.icon === 'radio'">
                  <path d="M12 20v-6M8.5 14h7l1.5-9h-10.5z"/>
                  <path d="M7 4c2.8 2.8 2.8 5.2 0 8M17 4c-2.8 2.8-2.8 5.2 0 8"/>
                </template>
                <template v-else-if="hop.icon === 'server'">
                  <rect x="3" y="4" width="18" height="6" rx="1"/><rect x="3" y="14" width="18" height="6" rx="1"/>
                  <circle cx="7" cy="7" r="1" fill="currentColor"/><circle cx="7" cy="17" r="1" fill="currentColor"/>
                </template>
              </svg>
              <span class="w-2 h-2 rounded-full mt-1" :class="statusColor(hop.status)" />
            </div>
            <p class="text-xs text-gray-400 mt-2 text-center">{{ hop.label }}</p>
            <p class="text-[10px] text-gray-600 text-center">{{ hop.detail }}</p>
          </div>
          <!-- Arrow -->
          <div v-if="idx < hops.length - 1" class="flex items-center h-16 px-1">
            <svg class="w-8 h-4 text-gray-600" viewBox="0 0 32 16">
              <line x1="0" y1="8" x2="24" y2="8" stroke="currentColor" stroke-width="2"
                    :stroke-dasharray="pulsingHop === hops[idx+1]?.id ? 'none' : '4 3'" />
              <path d="M22 4l6 4-6 4" fill="none" stroke="currentColor" stroke-width="2"/>
            </svg>
          </div>
        </template>

        <!-- Branch: MeshSat → MQTT + Iridium -->
        <div class="flex flex-col items-start gap-3 ml-1">
          <!-- MQTT branch -->
          <div class="flex items-center gap-0">
            <div class="flex items-center h-10 px-1">
              <svg class="w-8 h-4 text-gray-600" viewBox="0 0 32 16">
                <line x1="0" y1="8" x2="24" y2="8" stroke="currentColor" stroke-width="2"
                      :stroke-dasharray="mqttHop.status === 'green' ? 'none' : '4 3'" />
                <path d="M22 4l6 4-6 4" fill="none" stroke="currentColor" stroke-width="2"/>
              </svg>
            </div>
            <div class="flex flex-col items-center min-w-[100px]">
              <div class="w-16 h-10 rounded-lg border flex items-center justify-center gap-2"
                   :class="mqttHop.status === 'green' ? 'border-emerald-800 bg-emerald-950/30' :
                           mqttHop.status === 'red' ? 'border-red-800 bg-red-950/30' :
                           'border-gray-700 bg-gray-800/50'">
                <svg class="w-4 h-4 text-gray-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                  <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>
                </svg>
                <span class="w-2 h-2 rounded-full" :class="statusColor(mqttHop.status)" />
              </div>
              <p class="text-xs text-gray-400 mt-1 text-center">{{ mqttHop.label }}</p>
              <p class="text-[10px] text-gray-600 text-center">{{ mqttHop.detail }}</p>
            </div>
          </div>

          <!-- Iridium branch -->
          <div class="flex items-center gap-0">
            <div class="flex items-center h-10 px-1">
              <svg class="w-8 h-4 text-gray-600" viewBox="0 0 32 16">
                <line x1="0" y1="8" x2="24" y2="8" stroke="currentColor" stroke-width="2"
                      :stroke-dasharray="iridiumHop.status === 'green' ? 'none' : '4 3'" />
                <path d="M22 4l6 4-6 4" fill="none" stroke="currentColor" stroke-width="2"/>
              </svg>
            </div>
            <div class="flex flex-col items-center min-w-[100px]">
              <div class="w-16 h-10 rounded-lg border flex items-center justify-center gap-2"
                   :class="iridiumHop.status === 'green' ? 'border-emerald-800 bg-emerald-950/30' :
                           iridiumHop.status === 'red' ? 'border-red-800 bg-red-950/30' :
                           'border-gray-700 bg-gray-800/50'">
                <svg class="w-4 h-4 text-gray-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                  <circle cx="12" cy="12" r="10"/>
                  <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>
                  <path d="M2 12h20"/>
                </svg>
                <span class="w-2 h-2 rounded-full" :class="statusColor(iridiumHop.status)" />
              </div>
              <p class="text-xs text-gray-400 mt-1 text-center">{{ iridiumHop.label }}</p>
              <p class="text-[10px] text-gray-600 text-center">{{ iridiumHop.detail }}</p>
            </div>
          </div>
        </div>
      </div>

      <!-- Mobile: vertical chain -->
      <div class="md:hidden space-y-2">
        <template v-for="(hop, idx) in hops" :key="'m-'+hop.id">
          <div class="flex items-center gap-3">
            <div class="w-10 h-10 rounded-lg border flex items-center justify-center shrink-0"
                 :class="hop.status === 'green' ? 'border-emerald-800 bg-emerald-950/30' :
                         hop.status === 'red' ? 'border-red-800 bg-red-950/30' :
                         'border-gray-700 bg-gray-800/50'">
              <span class="w-2 h-2 rounded-full" :class="statusColor(hop.status)" />
            </div>
            <div>
              <p class="text-xs font-medium text-gray-300">{{ hop.label }}</p>
              <p class="text-[10px] text-gray-500">{{ hop.detail }}</p>
            </div>
          </div>
          <div v-if="idx < hops.length - 1" class="flex justify-center">
            <svg class="w-4 h-6 text-gray-600" viewBox="0 0 16 24">
              <line x1="8" y1="0" x2="8" y2="18" stroke="currentColor" stroke-width="2" stroke-dasharray="4 3"/>
              <path d="M4 16l4 6 4-6" fill="none" stroke="currentColor" stroke-width="2"/>
            </svg>
          </div>
        </template>
        <!-- Branch arrows for mobile -->
        <div class="flex justify-center">
          <svg class="w-4 h-6 text-gray-600" viewBox="0 0 16 24">
            <line x1="8" y1="0" x2="8" y2="18" stroke="currentColor" stroke-width="2" stroke-dasharray="4 3"/>
            <path d="M4 16l4 6 4-6" fill="none" stroke="currentColor" stroke-width="2"/>
          </svg>
        </div>
        <!-- MQTT hop -->
        <div class="flex items-center gap-3 pl-4">
          <div class="w-10 h-10 rounded-lg border flex items-center justify-center shrink-0"
               :class="mqttHop.status === 'green' ? 'border-emerald-800 bg-emerald-950/30' :
                       mqttHop.status === 'red' ? 'border-red-800 bg-red-950/30' :
                       'border-gray-700 bg-gray-800/50'">
            <span class="w-2 h-2 rounded-full" :class="statusColor(mqttHop.status)" />
          </div>
          <div>
            <p class="text-xs font-medium text-gray-300">{{ mqttHop.label }}</p>
            <p class="text-[10px] text-gray-500">{{ mqttHop.detail }}</p>
          </div>
        </div>
        <!-- Iridium hop -->
        <div class="flex items-center gap-3 pl-4">
          <div class="w-10 h-10 rounded-lg border flex items-center justify-center shrink-0"
               :class="iridiumHop.status === 'green' ? 'border-emerald-800 bg-emerald-950/30' :
                       iridiumHop.status === 'red' ? 'border-red-800 bg-red-950/30' :
                       'border-gray-700 bg-gray-800/50'">
            <span class="w-2 h-2 rounded-full" :class="statusColor(iridiumHop.status)" />
          </div>
          <div>
            <p class="text-xs font-medium text-gray-300">{{ iridiumHop.label }}</p>
            <p class="text-[10px] text-gray-500">{{ iridiumHop.detail }}</p>
          </div>
        </div>
      </div>
    </div>

    <!-- Recent Activity Feed -->
    <div class="bg-gray-900 rounded-xl p-5 border border-gray-800">
      <h2 class="text-sm font-semibold text-gray-300 mb-3">Recent Activity</h2>
      <div v-if="!recentActivity.length" class="text-center text-gray-500 text-sm py-4">
        No activity yet. Events will appear here as they arrive.
      </div>
      <div v-else class="max-h-[300px] overflow-y-auto space-y-1">
        <div v-for="(item, idx) in recentActivity" :key="idx"
             class="flex items-start gap-3 px-3 py-2 rounded-lg hover:bg-gray-800/50 transition-colors">
          <span class="w-6 h-6 rounded flex items-center justify-center text-[10px] font-bold bg-gray-800 text-gray-400 shrink-0 mt-0.5">
            {{ eventIcon(item.type) }}
          </span>
          <div class="flex-1 min-w-0">
            <p class="text-sm text-gray-300 truncate">{{ item.description }}</p>
            <p class="text-[10px] text-gray-600">{{ formatActivityTime(item.time) }}</p>
          </div>
        </div>
      </div>
    </div>

    <!-- Quick Actions -->
    <div class="flex gap-3">
      <router-link to="/messages"
        class="px-4 py-2.5 text-sm font-medium rounded-lg bg-teal-600 text-white hover:bg-teal-500 transition-colors">
        Send Message
      </router-link>
      <router-link to="/nodes"
        class="px-4 py-2.5 text-sm font-medium rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors">
        View Nodes
      </router-link>
      <router-link to="/gateways"
        class="px-4 py-2.5 text-sm font-medium rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors">
        Gateways
      </router-link>
    </div>
  </div>
</template>
