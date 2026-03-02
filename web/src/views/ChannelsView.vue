<script setup>
import { ref, computed, onMounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()
const loading = ref(true)
const editingIndex = ref(null)
const saving = ref(false)
const saved = ref(false)

const editForm = ref({
  name: '',
  role: 'DISABLED',
  psk: '',
  uplink_enabled: false,
  downlink_enabled: false
})

const channels = computed(() => {
  if (!store.config?.channels) return []
  return store.config.channels
})

function roleBadgeClass(role) {
  if (!role) return 'bg-gray-800 text-gray-500'
  const r = role.toUpperCase()
  if (r === 'PRIMARY') return 'bg-teal-900/30 text-teal-400'
  if (r === 'SECONDARY') return 'bg-blue-900/30 text-blue-400'
  return 'bg-gray-800 text-gray-500'
}

function encryptionInfo(ch) {
  const psk = ch.psk || ''
  // Base64 PSK lengths: empty=no encryption, 2 chars (1 byte)=default, 24 chars (16 bytes)=AES-128, 44 chars (32 bytes)=AES-256
  if (!psk || psk === '' || psk === 'AA==') {
    return { label: 'No Encryption', color: 'text-red-400 bg-red-900/20', icon: false }
  }
  // Approximate base64 length to raw byte count
  const rawLen = Math.floor((psk.length * 3) / 4)
  if (rawLen <= 1) {
    return { label: 'Default Key', color: 'text-amber-400 bg-amber-900/20', icon: false }
  }
  if (rawLen <= 16) {
    return { label: 'AES-128', color: 'text-emerald-400 bg-emerald-900/20', icon: false }
  }
  return { label: 'AES-256', color: 'text-emerald-400 bg-emerald-900/20', icon: true }
}

function isDisabled(ch) {
  const r = (ch.role || '').toUpperCase()
  return r === 'DISABLED' || r === ''
}

function startEdit(idx, ch) {
  editingIndex.value = idx
  editForm.value = {
    name: ch.name || '',
    role: (ch.role || 'DISABLED').toUpperCase(),
    psk: ch.psk || '',
    uplink_enabled: ch.uplink_enabled ?? false,
    downlink_enabled: ch.downlink_enabled ?? false
  }
}

function cancelEdit() {
  editingIndex.value = null
}

async function saveChannel(idx) {
  saving.value = true
  saved.value = false
  try {
    await store.setChannel({
      index: idx,
      name: editForm.value.name,
      role: editForm.value.role,
      psk: editForm.value.psk,
      uplink_enabled: editForm.value.uplink_enabled,
      downlink_enabled: editForm.value.downlink_enabled
    })
    editingIndex.value = null
    saved.value = true
    setTimeout(() => { saved.value = false }, 3000)
    await store.fetchConfig()
  } catch {
    // store sets error
  } finally {
    saving.value = false
  }
}

function generatePSK(bits) {
  const bytes = bits / 8
  const arr = new Uint8Array(bytes)
  crypto.getRandomValues(arr)
  // Convert to base64
  let binary = ''
  for (const b of arr) binary += String.fromCharCode(b)
  editForm.value.psk = btoa(binary)
}

function setDefaultPSK() {
  // 1-byte default key (0x01)
  editForm.value.psk = 'AQ=='
}

onMounted(async () => {
  loading.value = true
  await Promise.all([store.fetchConfig(), store.fetchStatus()])
  loading.value = false
})
</script>

<template>
  <div class="max-w-3xl mx-auto space-y-6">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-bold">Channels</h1>
      <button
        @click="store.fetchConfig()"
        class="px-3 py-1.5 text-sm rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors"
      >
        Refresh
      </button>
    </div>

    <!-- Connection banner -->
    <div v-if="store.status && !store.status.connected"
         class="bg-amber-900/20 border border-amber-800 rounded-lg p-3 text-amber-300 text-sm">
      Radio not connected. Connect a Meshtastic device to manage channels.
    </div>

    <!-- Error banner -->
    <div v-if="store.error" class="bg-red-900/30 border border-red-800 rounded-lg p-3 text-red-300 text-sm">
      {{ store.error }}
    </div>

    <!-- Saved confirmation -->
    <div v-if="saved" class="bg-teal-900/20 border border-teal-800 rounded-lg p-3 text-teal-300 text-sm">
      Channel configuration saved
    </div>

    <div v-if="loading" class="bg-gray-900 rounded-xl p-8 border border-gray-800 text-center text-gray-500">
      Loading configuration...
    </div>

    <div v-else-if="!channels.length" class="bg-gray-900 rounded-xl p-8 border border-gray-800 text-center text-gray-500">
      No channel data available. Ensure the radio is connected and the config endpoint is available.
    </div>

    <template v-else>
      <div v-for="(ch, idx) in channels" :key="idx"
           class="bg-gray-900 rounded-xl border border-gray-800 overflow-hidden">
        <!-- Channel header -->
        <div class="p-4 flex items-center justify-between cursor-pointer"
             @click="isDisabled(ch) && editingIndex !== idx ? startEdit(idx, ch) : null">
          <div class="flex items-center gap-3">
            <span class="w-8 h-8 rounded-lg bg-gray-800 flex items-center justify-center text-sm font-bold text-gray-400">
              {{ idx }}
            </span>
            <div>
              <div class="flex items-center gap-2">
                <span class="font-medium text-gray-200">{{ ch.name || `Channel ${idx}` }}</span>
                <span class="text-[10px] font-medium px-2 py-0.5 rounded-full"
                      :class="roleBadgeClass(ch.role)">
                  {{ (ch.role || 'DISABLED').toUpperCase() }}
                </span>
              </div>
              <div class="flex items-center gap-2 mt-1">
                <span class="text-[10px] font-medium px-2 py-0.5 rounded"
                      :class="encryptionInfo(ch).color">
                  {{ encryptionInfo(ch).icon ? '&#9632; ' : '' }}{{ encryptionInfo(ch).label }}
                </span>
                <span v-if="ch.uplink_enabled" class="text-[10px] text-gray-500">Uplink</span>
                <span v-if="ch.downlink_enabled" class="text-[10px] text-gray-500">Downlink</span>
              </div>
            </div>
          </div>
          <button
            v-if="editingIndex !== idx"
            @click.stop="startEdit(idx, ch)"
            class="px-3 py-1.5 text-xs rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors"
          >
            Edit
          </button>
        </div>

        <!-- Edit form -->
        <div v-if="editingIndex === idx" class="border-t border-gray-800 p-4 bg-gray-950/50">
          <div class="space-y-4 max-w-md">
            <div>
              <label class="block text-xs text-gray-400 mb-1">Name (max 11 chars)</label>
              <input v-model="editForm.name" type="text" maxlength="11"
                class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none" />
            </div>
            <div>
              <label class="block text-xs text-gray-400 mb-1">Role</label>
              <select v-model="editForm.role"
                class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none">
                <option value="PRIMARY">PRIMARY</option>
                <option value="SECONDARY">SECONDARY</option>
                <option value="DISABLED">DISABLED</option>
              </select>
            </div>
            <div>
              <label class="block text-xs text-gray-400 mb-1">PSK (base64)</label>
              <input v-model="editForm.psk" type="text"
                class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-sm text-gray-200 font-mono focus:border-teal-500 focus:outline-none" />
              <div class="flex gap-2 mt-2">
                <button @click="generatePSK(128)"
                  class="px-2 py-1 text-[10px] rounded bg-gray-800 text-gray-400 hover:text-teal-400 transition-colors">
                  AES-128
                </button>
                <button @click="generatePSK(256)"
                  class="px-2 py-1 text-[10px] rounded bg-gray-800 text-gray-400 hover:text-teal-400 transition-colors">
                  AES-256
                </button>
                <button @click="setDefaultPSK"
                  class="px-2 py-1 text-[10px] rounded bg-gray-800 text-gray-400 hover:text-amber-400 transition-colors">
                  Default
                </button>
              </div>
              <p class="text-[10px] text-gray-600 mt-1">AES-128 = 16 bytes, AES-256 = 32 bytes. Default = shared 1-byte key.</p>
            </div>
            <div class="flex items-center gap-6">
              <label class="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
                <input v-model="editForm.uplink_enabled" type="checkbox" class="accent-teal-500" />
                Uplink
              </label>
              <label class="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
                <input v-model="editForm.downlink_enabled" type="checkbox" class="accent-teal-500" />
                Downlink
              </label>
            </div>
            <div class="flex gap-2">
              <button @click="saveChannel(idx)" :disabled="saving"
                class="px-4 py-2 text-sm rounded-lg bg-teal-600 text-white hover:bg-teal-500 transition-colors disabled:opacity-50">
                {{ saving ? 'Saving...' : 'Save' }}
              </button>
              <button @click="cancelEdit"
                class="px-4 py-2 text-sm rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors">
                Cancel
              </button>
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
