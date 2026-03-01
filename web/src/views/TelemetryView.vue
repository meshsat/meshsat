<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import { Line } from 'vue-chartjs'
import { Chart, CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Legend, Filler } from 'chart.js'

Chart.register(CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Legend, Filler)

const store = useMeshsatStore()
const selectedNode = ref('')
const timeRange = ref('24h')

const TIME_RANGES = [
  { key: '1h', hours: 1 },
  { key: '6h', hours: 6 },
  { key: '24h', hours: 24 },
  { key: '7d', hours: 168 }
]

const chartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  interaction: { mode: 'index', intersect: false },
  plugins: {
    legend: { labels: { color: '#9ca3af', font: { size: 11 } } },
    tooltip: { backgroundColor: '#1f2937', titleColor: '#e5e7eb', bodyColor: '#d1d5db' }
  },
  scales: {
    x: { ticks: { color: '#6b7280', maxTicksLimit: 8, font: { size: 10 } }, grid: { color: '#1f2937' } },
    y: { ticks: { color: '#6b7280', font: { size: 10 } }, grid: { color: '#1f2937' } }
  },
  elements: { point: { radius: 0 }, line: { tension: 0.3, borderWidth: 2 } }
}

const nodeOptions = computed(() =>
  store.nodes.map(n => ({
    id: String(n.id ?? n.node_id ?? n.num ?? ''),
    name: n.name ?? n.long_name ?? n.short_name ?? String(n.id ?? '')
  }))
)

function formatLabel(val) {
  if (!val) return ''
  const ts = typeof val === 'number' && val < 1e12 ? val * 1000 : val
  return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

const labels = computed(() =>
  store.telemetry.map(t => formatLabel(t.timestamp ?? t.time))
)

const batteryChart = computed(() => ({
  labels: labels.value,
  datasets: [
    {
      label: 'Battery %',
      data: store.telemetry.map(t => t.battery ?? null),
      borderColor: '#34d399',
      backgroundColor: 'rgba(52, 211, 153, 0.1)',
      fill: true
    },
    {
      label: 'Voltage',
      data: store.telemetry.map(t => t.voltage ?? null),
      borderColor: '#22d3ee',
      yAxisID: 'y1'
    }
  ]
}))

const batteryOptions = computed(() => ({
  ...chartOptions,
  scales: {
    ...chartOptions.scales,
    y: { ...chartOptions.scales.y, title: { display: true, text: 'Battery %', color: '#6b7280' } },
    y1: { position: 'right', ticks: { color: '#6b7280', font: { size: 10 } }, grid: { drawOnChartArea: false }, title: { display: true, text: 'Voltage', color: '#6b7280' } }
  }
}))

const channelChart = computed(() => ({
  labels: labels.value,
  datasets: [
    {
      label: 'Channel Util %',
      data: store.telemetry.map(t => t.channel_util ?? null),
      borderColor: '#a855f7',
      backgroundColor: 'rgba(168, 85, 247, 0.1)',
      fill: true
    },
    {
      label: 'Air Util TX %',
      data: store.telemetry.map(t => t.air_util_tx ?? null),
      borderColor: '#818cf8'
    }
  ]
}))

const envChart = computed(() => ({
  labels: labels.value,
  datasets: [
    {
      label: 'Temperature (C)',
      data: store.telemetry.map(t => t.temperature ?? null),
      borderColor: '#fb923c'
    },
    {
      label: 'Humidity %',
      data: store.telemetry.map(t => t.humidity ?? null),
      borderColor: '#60a5fa'
    }
  ]
}))

const hasBattery = computed(() => store.telemetry.some(t => t.battery != null))
const hasChannel = computed(() => store.telemetry.some(t => t.channel_util != null))
const hasEnv = computed(() => store.telemetry.some(t => t.temperature != null || t.humidity != null))

async function fetchData() {
  if (!selectedNode.value) return
  const r = TIME_RANGES.find(x => x.key === timeRange.value)
  const since = new Date(Date.now() - (r?.hours ?? 24) * 3600 * 1000).toISOString()
  await store.fetchTelemetry({ node: selectedNode.value, since, limit: 500 })
}

watch([selectedNode, timeRange], fetchData)

onMounted(async () => {
  await store.fetchNodes()
  if (nodeOptions.value.length) {
    selectedNode.value = nodeOptions.value[0].id
  }
})
</script>

<template>
  <div class="max-w-4xl mx-auto space-y-6">
    <h1 class="text-2xl font-bold">Telemetry</h1>

    <div class="flex flex-col sm:flex-row gap-3">
      <select
        v-model="selectedNode"
        class="px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 focus:outline-none focus:ring-2 focus:ring-teal-500"
      >
        <option value="" disabled>Select Node</option>
        <option v-for="n in nodeOptions" :key="n.id" :value="n.id">{{ n.name }}</option>
      </select>
      <div class="flex gap-1">
        <button
          v-for="r in TIME_RANGES" :key="r.key"
          class="px-3 py-1.5 text-xs font-medium rounded-lg transition-colors"
          :class="timeRange === r.key ? 'bg-teal-500/15 text-teal-400' : 'text-gray-400 hover:bg-gray-800'"
          @click="timeRange = r.key"
        >{{ r.key }}</button>
      </div>
    </div>

    <div v-if="!selectedNode" class="bg-gray-900 rounded-xl p-8 border border-gray-800 text-center text-gray-500">
      Select a node to view telemetry
    </div>

    <template v-else-if="store.telemetry.length">
      <div v-if="hasBattery" class="bg-gray-900 rounded-xl p-5 border border-gray-800">
        <h2 class="text-sm font-semibold text-gray-300 mb-3">Battery & Voltage</h2>
        <div class="h-56"><Line :data="batteryChart" :options="batteryOptions" /></div>
      </div>

      <div v-if="hasChannel" class="bg-gray-900 rounded-xl p-5 border border-gray-800">
        <h2 class="text-sm font-semibold text-gray-300 mb-3">Channel Utilization</h2>
        <div class="h-56"><Line :data="channelChart" :options="chartOptions" /></div>
      </div>

      <div v-if="hasEnv" class="bg-gray-900 rounded-xl p-5 border border-gray-800">
        <h2 class="text-sm font-semibold text-gray-300 mb-3">Environment</h2>
        <div class="h-56"><Line :data="envChart" :options="chartOptions" /></div>
      </div>
    </template>

    <div v-else class="bg-gray-900 rounded-xl p-8 border border-gray-800 text-center text-gray-500">
      No telemetry data for selected node and time range
    </div>
  </div>
</template>
