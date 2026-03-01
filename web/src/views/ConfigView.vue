<script setup>
import { ref } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()

// Radio config
const radioSection = ref('lora')
const radioJSON = ref('{}')
const radioLoading = ref(false)
const radioSaved = ref(false)

// Module config
const moduleSection = ref('mqtt')
const moduleJSON = ref('{}')
const moduleLoading = ref(false)
const moduleSaved = ref(false)

// Waypoint
const wpName = ref('')
const wpDesc = ref('')
const wpLat = ref('')
const wpLon = ref('')
const wpIcon = ref(0)
const wpExpire = ref('')
const wpLoading = ref(false)
const wpSent = ref(false)

const RADIO_SECTIONS = ['lora', 'bluetooth', 'device', 'display', 'network', 'position', 'power']
const MODULE_SECTIONS = ['mqtt', 'serial', 'external_notification', 'store_forward', 'range_test', 'telemetry', 'canned_message']

function isValidJSON(s) {
  try { JSON.parse(s); return true } catch { return false }
}

async function handleRadio() {
  if (!isValidJSON(radioJSON.value)) return
  radioLoading.value = true
  try {
    await store.configRadio({ section: radioSection.value, config: JSON.parse(radioJSON.value) })
    radioSaved.value = true
    setTimeout(() => { radioSaved.value = false }, 3000)
  } catch { /* store error */ } finally {
    radioLoading.value = false
  }
}

async function handleModule() {
  if (!isValidJSON(moduleJSON.value)) return
  moduleLoading.value = true
  try {
    await store.configModule({ section: moduleSection.value, config: JSON.parse(moduleJSON.value) })
    moduleSaved.value = true
    setTimeout(() => { moduleSaved.value = false }, 3000)
  } catch { /* store error */ } finally {
    moduleLoading.value = false
  }
}

async function handleWaypoint() {
  if (!wpName.value.trim() || !wpLat.value || !wpLon.value) return
  wpLoading.value = true
  wpSent.value = false
  try {
    const payload = {
      name: wpName.value.trim(),
      description: wpDesc.value.trim() || undefined,
      latitude: Number(wpLat.value),
      longitude: Number(wpLon.value),
      icon: Number(wpIcon.value) || 0
    }
    if (wpExpire.value) payload.expire = Math.floor(new Date(wpExpire.value).getTime() / 1000)
    await store.sendWaypoint(payload)
    wpName.value = ''; wpDesc.value = ''; wpLat.value = ''; wpLon.value = ''
    wpSent.value = true
    setTimeout(() => { wpSent.value = false }, 3000)
  } catch { /* store error */ } finally {
    wpLoading.value = false
  }
}
</script>

<template>
  <div class="max-w-3xl mx-auto space-y-8">
    <h1 class="text-2xl font-bold">Configuration</h1>

    <!-- Radio Config -->
    <div class="bg-gray-900 rounded-xl p-5 border border-gray-800">
      <h2 class="text-lg font-semibold mb-4">Radio Configuration</h2>
      <div class="space-y-4 max-w-lg">
        <div>
          <label class="block text-sm text-gray-400 mb-1">Section</label>
          <select v-model="radioSection"
                  class="w-full px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 focus:outline-none focus:ring-2 focus:ring-teal-500">
            <option v-for="s in RADIO_SECTIONS" :key="s" :value="s">{{ s }}</option>
          </select>
        </div>
        <div>
          <label class="block text-sm text-gray-400 mb-1">Config (JSON)</label>
          <textarea v-model="radioJSON" rows="4"
                    class="w-full px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 font-mono focus:outline-none focus:ring-2 focus:ring-teal-500"
                    :class="{ 'border-red-500': radioJSON && !isValidJSON(radioJSON) }" />
          <p v-if="radioJSON && !isValidJSON(radioJSON)" class="text-xs text-red-400 mt-1">Invalid JSON</p>
        </div>
        <button @click="handleRadio" :disabled="!isValidJSON(radioJSON) || radioLoading"
                class="px-4 py-2 text-sm font-medium rounded-lg bg-teal-600 text-white hover:bg-teal-500 disabled:opacity-50 transition-colors">
          {{ radioLoading ? 'Applying...' : 'Apply' }}
        </button>
        <p v-if="radioSaved" class="text-xs text-teal-400">Radio config applied</p>
      </div>
    </div>

    <!-- Module Config -->
    <div class="bg-gray-900 rounded-xl p-5 border border-gray-800">
      <h2 class="text-lg font-semibold mb-4">Module Configuration</h2>
      <div class="space-y-4 max-w-lg">
        <div>
          <label class="block text-sm text-gray-400 mb-1">Module</label>
          <select v-model="moduleSection"
                  class="w-full px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 focus:outline-none focus:ring-2 focus:ring-teal-500">
            <option v-for="s in MODULE_SECTIONS" :key="s" :value="s">{{ s }}</option>
          </select>
        </div>
        <div>
          <label class="block text-sm text-gray-400 mb-1">Config (JSON)</label>
          <textarea v-model="moduleJSON" rows="4"
                    class="w-full px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 font-mono focus:outline-none focus:ring-2 focus:ring-teal-500"
                    :class="{ 'border-red-500': moduleJSON && !isValidJSON(moduleJSON) }" />
          <p v-if="moduleJSON && !isValidJSON(moduleJSON)" class="text-xs text-red-400 mt-1">Invalid JSON</p>
        </div>
        <button @click="handleModule" :disabled="!isValidJSON(moduleJSON) || moduleLoading"
                class="px-4 py-2 text-sm font-medium rounded-lg bg-teal-600 text-white hover:bg-teal-500 disabled:opacity-50 transition-colors">
          {{ moduleLoading ? 'Applying...' : 'Apply' }}
        </button>
        <p v-if="moduleSaved" class="text-xs text-teal-400">Module config applied</p>
      </div>
    </div>

    <!-- Waypoints -->
    <div class="bg-gray-900 rounded-xl p-5 border border-gray-800">
      <h2 class="text-lg font-semibold mb-4">Send Waypoint</h2>
      <div class="space-y-4 max-w-lg">
        <input v-model="wpName" type="text" placeholder="Name"
               class="w-full px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-teal-500" />
        <input v-model="wpDesc" type="text" placeholder="Description (optional)"
               class="w-full px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-teal-500" />
        <div class="grid grid-cols-2 gap-4">
          <input v-model="wpLat" type="number" step="0.000001" placeholder="Latitude"
                 class="px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 font-mono focus:outline-none focus:ring-2 focus:ring-teal-500" />
          <input v-model="wpLon" type="number" step="0.000001" placeholder="Longitude"
                 class="px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 font-mono focus:outline-none focus:ring-2 focus:ring-teal-500" />
        </div>
        <div class="grid grid-cols-2 gap-4">
          <input v-model.number="wpIcon" type="number" min="0" max="255" placeholder="Icon (0-255)"
                 class="px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-teal-500" />
          <input v-model="wpExpire" type="datetime-local"
                 class="px-3 py-2 text-sm rounded-lg border border-gray-700 bg-gray-800 text-gray-100 focus:outline-none focus:ring-2 focus:ring-teal-500" />
        </div>
        <button @click="handleWaypoint" :disabled="!wpName.trim() || !wpLat || !wpLon || wpLoading"
                class="px-4 py-2 text-sm font-medium rounded-lg bg-teal-600 text-white hover:bg-teal-500 disabled:opacity-50 transition-colors">
          {{ wpLoading ? 'Sending...' : 'Send Waypoint' }}
        </button>
        <p v-if="wpSent" class="text-xs text-teal-400">Waypoint sent</p>
      </div>
    </div>
  </div>
</template>
