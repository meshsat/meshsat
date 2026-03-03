<script setup>
import { ref } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const sending = ref(null)

async function send(preset) {
  if (sending.value) return
  sending.value = preset.id
  try {
    await store.sendPreset(preset.id)
  } finally {
    sending.value = null
  }
}
</script>

<template>
  <div class="grid grid-cols-2 sm:grid-cols-4 gap-2">
    <button
      v-for="preset in store.presets"
      :key="preset.id"
      @click="send(preset)"
      :disabled="sending === preset.id"
      class="p-3 rounded-lg bg-gray-800 hover:bg-gray-700 border border-gray-700 text-left transition-colors disabled:opacity-50"
    >
      <div class="text-sm font-medium text-gray-200 truncate">{{ preset.name }}</div>
      <div class="text-xs text-gray-500 mt-1 truncate">{{ preset.text }}</div>
    </button>
  </div>
</template>
