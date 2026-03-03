<script setup>
import { ref, watch } from 'vue'

const props = defineProps({
  rule: { type: Object, default: null },
  open: { type: Boolean, default: false }
})
const emit = defineEmits(['save', 'close'])

const form = ref(getDefault())

function getDefault() {
  return {
    name: '', enabled: true, priority: 1,
    source_type: 'any', source_channels: '', source_nodes: '', source_portnums: '', source_keyword: '',
    dest_type: 'iridium',
    sat_priority: 1, sat_max_delay_sec: 0, sat_include_pos: false, sat_max_text_len: 320,
    position_precision: 32, rate_limit_per_min: 0, rate_limit_window: 60
  }
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
  } else {
    form.value = getDefault()
  }
}, { immediate: true })

function save() {
  const data = { ...form.value }
  if (data.source_channels === '') data.source_channels = null
  if (data.source_nodes === '') data.source_nodes = null
  if (data.source_portnums === '') data.source_portnums = null
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

        <div v-if="form.source_type === 'channel'">
          <label class="block text-xs text-gray-400 mb-1">Channels (JSON array, e.g. [0,2])</label>
          <input v-model="form.source_channels" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="[0]">
        </div>
        <div v-if="form.source_type === 'node'">
          <label class="block text-xs text-gray-400 mb-1">Nodes (JSON array, e.g. ["!abcd1234"])</label>
          <input v-model="form.source_nodes" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder='["!abcd1234"]'>
        </div>
        <div v-if="form.source_type === 'portnum'">
          <label class="block text-xs text-gray-400 mb-1">Portnums (JSON array: 1=Text, 3=Position, 67=Telemetry)</label>
          <input v-model="form.source_portnums" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="[1,3]">
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
