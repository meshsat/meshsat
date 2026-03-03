<script setup>
import { ref } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const confirming = ref(false)
const sending = ref(false)

async function activate() {
  if (store.sosStatus?.active) return
  confirming.value = true
}

async function confirm() {
  sending.value = true
  try {
    await store.activateSOS()
  } finally {
    sending.value = false
    confirming.value = false
  }
}

async function cancel() {
  await store.cancelSOS()
}
</script>

<template>
  <div>
    <!-- SOS Button -->
    <button v-if="!store.sosStatus?.active && !confirming"
      @click="activate"
      class="w-full py-3 rounded-lg bg-red-600 hover:bg-red-500 text-white font-bold text-sm tracking-wider transition-colors">
      SOS
    </button>

    <!-- Confirmation dialog -->
    <div v-if="confirming && !store.sosStatus?.active" class="bg-red-900/50 border border-red-700 rounded-lg p-4">
      <p class="text-sm text-red-200 mb-3">This will send an emergency alert with your GPS position via all available transports. Continue?</p>
      <div class="flex gap-2">
        <button @click="confirming = false" class="flex-1 py-2 rounded bg-gray-700 text-gray-300 text-sm hover:bg-gray-600">Cancel</button>
        <button @click="confirm" :disabled="sending"
          class="flex-1 py-2 rounded bg-red-600 text-white text-sm font-bold hover:bg-red-500 disabled:opacity-50">
          {{ sending ? 'Sending...' : 'Send SOS' }}
        </button>
      </div>
    </div>

    <!-- Active SOS -->
    <div v-if="store.sosStatus?.active" class="bg-red-900/50 border border-red-600 rounded-lg p-4 animate-pulse">
      <div class="flex items-center justify-between">
        <div>
          <div class="text-red-400 font-bold text-sm">SOS ACTIVE</div>
          <div class="text-xs text-red-300 mt-1">Sends: {{ store.sosStatus.sends || 0 }}/3</div>
        </div>
        <button @click="cancel" class="px-4 py-2 rounded bg-gray-700 text-gray-300 text-sm hover:bg-gray-600">Cancel</button>
      </div>
    </div>
  </div>
</template>
