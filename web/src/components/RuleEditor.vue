<script setup>
import { ref, computed, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()

const props = defineProps({
  rule: { type: Object, default: null },
  open: { type: Boolean, default: false }
})
const emit = defineEmits(['save', 'close'])

const form = ref(getDefault())

// Friendly picker state
const selectedChannels = ref([])
const selectedNodes = ref([])
const selectedPortnums = ref([])

const portnumOptions = [
  { value: 1, label: 'Text Message' },
  { value: 3, label: 'Position' },
  { value: 4, label: 'NodeInfo' },
  { value: 8, label: 'Waypoint' },
  { value: 67, label: 'Telemetry' },
  { value: 70, label: 'Traceroute' }
]

const channelOptions = computed(() => {
  const channels = store.config?.channels || {}
  return Array.from({ length: 8 }, (_, i) => {
    const ch = channels[i]
    return { index: i, name: ch?.name || `Ch ${i}` }
  })
})

const nodeOptions = computed(() => {
  return (store.nodes || []).map(n => ({
    num: n.num,
    id: n.user_id || `!${n.num?.toString(16)}`,
    name: n.long_name || n.short_name || 'Unknown'
  }))
})

function getDefault() {
  return {
    name: '', enabled: true, priority: 1,
    source_type: 'any', source_channels: '', source_nodes: '', source_portnums: '', source_keyword: '',
    dest_type: 'iridium',
    sat_priority: 1, sat_max_delay_sec: 0, sat_include_pos: false, sat_max_text_len: 320,
    position_precision: 32, rate_limit_per_min: 0, rate_limit_window: 60
  }
}

function parseJSON(val) {
  if (!val) return []
  try { return JSON.parse(val) } catch { return [] }
}

watch(() => props.rule, (r) => {
  if (r) {
    form.value = {
      ...r,
      source_channels: r.source_channels || '',
      source_nodes: r.source_nodes || '',
      source_portnums: r.source_portnums || '',
      source_keyword: r.source_keyword || ''
    }
    selectedChannels.value = parseJSON(r.source_channels)
    selectedNodes.value = parseJSON(r.source_nodes)
    selectedPortnums.value = parseJSON(r.source_portnums)
  } else {
    form.value = getDefault()
    selectedChannels.value = []
    selectedNodes.value = []
    selectedPortnums.value = []
  }
}, { immediate: true })

watch(() => props.open, (v) => {
  if (v) {
    store.fetchConfig()
    store.fetchNodes()
  }
})

function toggleChannel(idx) {
  const i = selectedChannels.value.indexOf(idx)
  if (i >= 0) selectedChannels.value.splice(i, 1)
  else selectedChannels.value.push(idx)
}

function toggleNode(nodeId) {
  const i = selectedNodes.value.indexOf(nodeId)
  if (i >= 0) selectedNodes.value.splice(i, 1)
  else selectedNodes.value.push(nodeId)
}

function togglePortnum(val) {
  const i = selectedPortnums.value.indexOf(val)
  if (i >= 0) selectedPortnums.value.splice(i, 1)
  else selectedPortnums.value.push(val)
}

function save() {
  const data = { ...form.value }

  // Convert picker selections back to JSON strings
  if (data.source_type === 'channel') {
    data.source_channels = selectedChannels.value.length ? JSON.stringify(selectedChannels.value) : null
  } else {
    data.source_channels = null
  }

  if (data.source_type === 'node') {
    data.source_nodes = selectedNodes.value.length ? JSON.stringify(selectedNodes.value) : null
  } else {
    data.source_nodes = null
  }

  if (data.source_type === 'portnum') {
    data.source_portnums = selectedPortnums.value.length ? JSON.stringify(selectedPortnums.value) : null
  } else {
    data.source_portnums = null
  }

  if (data.source_keyword === '') data.source_keyword = null

  emit('save', data)
}
</script>

<template>
  <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
    <div class="bg-gray-800 rounded-xl border border-gray-700 w-full max-w-lg max-h-[90vh] overflow-y-auto p-6">
      <h3 class="text-lg font-medium text-gray-200 mb-4">{{ rule ? 'Edit Rule' : 'New Rule' }}</h3>

      <div class="space-y-4">
        <div>
          <label class="block text-xs text-gray-400 mb-1">Name</label>
          <input v-model="form.name" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="Emergency to Satellite">
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-400 mb-1">Source Type</label>
            <select v-model="form.source_type" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <option value="any">Any message</option>
              <option value="channel">Channel</option>
              <option value="node">Node</option>
              <option value="portnum">Port Number</option>
            </select>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Destination</label>
            <select v-model="form.dest_type" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <option value="iridium">Iridium Satellite</option>
              <option value="mqtt">MQTT</option>
              <option value="both">Both</option>
            </select>
          </div>
        </div>

        <!-- Channel picker (multi-select badges) -->
        <div v-if="form.source_type === 'channel'">
          <label class="block text-xs text-gray-400 mb-2">Select Channels</label>
          <div class="flex flex-wrap gap-2">
            <button v-for="ch in channelOptions" :key="ch.index" @click="toggleChannel(ch.index)"
              class="px-2.5 py-1 rounded text-xs font-medium transition-colors border"
              :class="selectedChannels.includes(ch.index)
                ? 'bg-teal-600/20 text-teal-400 border-teal-600/30'
                : 'bg-gray-900 text-gray-500 border-gray-700 hover:border-gray-600'">
              {{ ch.index }}: {{ ch.name }}
            </button>
          </div>
        </div>

        <!-- Node picker (autocomplete with names) -->
        <div v-if="form.source_type === 'node'">
          <label class="block text-xs text-gray-400 mb-2">Select Nodes</label>
          <div v-if="nodeOptions.length === 0" class="text-xs text-gray-600">No nodes discovered yet</div>
          <div class="flex flex-wrap gap-2 max-h-32 overflow-y-auto">
            <button v-for="node in nodeOptions" :key="node.id" @click="toggleNode(node.id)"
              class="px-2.5 py-1 rounded text-xs font-medium transition-colors border"
              :class="selectedNodes.includes(node.id)
                ? 'bg-teal-600/20 text-teal-400 border-teal-600/30'
                : 'bg-gray-900 text-gray-500 border-gray-700 hover:border-gray-600'">
              {{ node.name }}
              <span class="text-[9px] text-gray-600 ml-1">{{ node.id }}</span>
            </button>
          </div>
        </div>

        <!-- Portnum picker (checkboxes with names) -->
        <div v-if="form.source_type === 'portnum'">
          <label class="block text-xs text-gray-400 mb-2">Select Port Numbers</label>
          <div class="grid grid-cols-2 gap-2">
            <label v-for="pn in portnumOptions" :key="pn.value"
              class="flex items-center gap-2 px-2.5 py-1.5 rounded text-xs transition-colors border cursor-pointer"
              :class="selectedPortnums.includes(pn.value)
                ? 'bg-teal-600/20 text-teal-400 border-teal-600/30'
                : 'bg-gray-900 text-gray-500 border-gray-700 hover:border-gray-600'">
              <input type="checkbox" :checked="selectedPortnums.includes(pn.value)" @change="togglePortnum(pn.value)"
                class="rounded bg-gray-900 border-gray-700 text-teal-500">
              {{ pn.label }}
              <span class="text-[9px] text-gray-600">#{{ pn.value }}</span>
            </label>
          </div>
        </div>

        <div>
          <label class="block text-xs text-gray-400 mb-1">Keyword filter (optional, case-insensitive)</label>
          <input v-model="form.source_keyword" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="emergency">
        </div>

        <div v-if="form.dest_type !== 'mqtt'" class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-400 mb-1">Satellite Priority</label>
            <select v-model.number="form.sat_priority" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <option :value="0">Critical (always send)</option>
              <option :value="1">Normal</option>
              <option :value="2">Low</option>
            </select>
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Max Text Length</label>
            <input v-model.number="form.sat_max_text_len" type="number" min="1" max="340" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-400 mb-1">Rate limit (msgs/window, 0=off)</label>
            <input v-model.number="form.rate_limit_per_min" type="number" min="0" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-400 mb-1">Window (seconds)</label>
            <input v-model.number="form.rate_limit_window" type="number" min="1" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>

        <div v-if="form.dest_type !== 'mqtt'" class="flex items-center gap-2">
          <input type="checkbox" v-model="form.sat_include_pos" id="sat_pos" class="rounded bg-gray-900 border-gray-700">
          <label for="sat_pos" class="text-xs text-gray-400">Include GPS position in satellite payload</label>
        </div>
      </div>

      <div class="flex gap-3 mt-6">
        <button @click="emit('close')" class="flex-1 py-2 rounded bg-gray-700 text-gray-300 text-sm hover:bg-gray-600">Cancel</button>
        <button @click="save" class="flex-1 py-2 rounded bg-teal-600 text-white text-sm font-medium hover:bg-teal-500">Save</button>
      </div>
    </div>
  </div>
</template>
