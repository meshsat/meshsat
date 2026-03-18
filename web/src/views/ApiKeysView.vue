<script setup>
import { ref, onMounted } from 'vue'
import api from '@/api/client'

const keys = ref([])
const loading = ref(true)
const creating = ref(false)
const error = ref(null)
const showCreated = ref(null) // holds the plaintext key after creation

// Create form
const newLabel = ref('')
const newRole = ref('operator')
const newDeviceId = ref('')

async function loadKeys() {
  loading.value = true
  error.value = null
  try {
    keys.value = await api.get('/auth/keys') || []
  } catch (e) {
    error.value = e.message
    keys.value = []
  } finally {
    loading.value = false
  }
}

async function createKey() {
  if (!newLabel.value.trim()) return
  creating.value = true
  error.value = null
  try {
    const body = {
      label: newLabel.value.trim(),
      role: newRole.value,
    }
    if (newDeviceId.value) {
      body.device_id = parseInt(newDeviceId.value, 10)
    }
    const result = await api.post('/auth/keys', body)
    showCreated.value = result.key
    newLabel.value = ''
    newDeviceId.value = ''
    await loadKeys()
  } catch (e) {
    error.value = e.message
  } finally {
    creating.value = false
  }
}

async function revokeKey(id) {
  if (!confirm('Revoke this API key? This cannot be undone.')) return
  try {
    await api.del(`/auth/keys/${id}`)
    await loadKeys()
  } catch (e) {
    error.value = e.message
  }
}

function copyKey() {
  if (showCreated.value) {
    navigator.clipboard.writeText(showCreated.value)
  }
}

function formatDate(ts) {
  if (!ts) return '--'
  try {
    return new Date(ts).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric', hour: '2-digit', minute: '2-digit' })
  } catch {
    return ts
  }
}

const roleBadge = {
  owner: 'bg-red-500/20 text-red-400 border-red-500/30',
  operator: 'bg-amber-500/20 text-amber-400 border-amber-500/30',
  viewer: 'bg-sky-500/20 text-sky-400 border-sky-500/30',
}

onMounted(loadKeys)
</script>

<template>
  <div class="max-w-3xl mx-auto">
    <h1 class="text-lg font-display font-semibold text-gray-200 mb-4">API Keys</h1>

    <!-- Created key banner (shown once) -->
    <div v-if="showCreated" class="mb-4 p-3 bg-emerald-900/30 border border-emerald-600/40 rounded-lg">
      <div class="flex items-center justify-between mb-1">
        <span class="text-xs font-medium text-emerald-400">Key created — copy it now, it won't be shown again</span>
        <button @click="showCreated = null" class="text-gray-500 hover:text-gray-300 text-xs">Dismiss</button>
      </div>
      <div class="flex items-center gap-2">
        <code class="flex-1 text-xs font-mono text-emerald-300 bg-black/30 px-2 py-1.5 rounded select-all break-all">{{ showCreated }}</code>
        <button @click="copyKey" class="px-2 py-1.5 text-xs bg-emerald-700/30 hover:bg-emerald-700/50 text-emerald-300 rounded transition-colors">
          Copy
        </button>
      </div>
    </div>

    <!-- Create form -->
    <div class="mb-6 p-4 bg-tactical-surface border border-tactical-border rounded-lg">
      <h2 class="text-sm font-medium text-gray-300 mb-3">Create API Key</h2>
      <div class="flex flex-wrap gap-3 items-end">
        <div class="flex-1 min-w-[160px]">
          <label class="block text-[10px] text-gray-500 uppercase tracking-wider mb-1">Label</label>
          <input v-model="newLabel" type="text" placeholder="e.g. Field Device 1"
            class="w-full px-2.5 py-1.5 bg-black/20 border border-tactical-border rounded text-sm text-gray-200 placeholder-gray-600 focus:border-tactical-iridium/40 focus:outline-none" />
        </div>
        <div class="w-32">
          <label class="block text-[10px] text-gray-500 uppercase tracking-wider mb-1">Role</label>
          <select v-model="newRole"
            class="w-full px-2.5 py-1.5 bg-black/20 border border-tactical-border rounded text-sm text-gray-200 focus:border-tactical-iridium/40 focus:outline-none">
            <option value="viewer">Viewer</option>
            <option value="operator">Operator</option>
            <option value="owner">Owner</option>
          </select>
        </div>
        <div class="w-28">
          <label class="block text-[10px] text-gray-500 uppercase tracking-wider mb-1">Device ID</label>
          <input v-model="newDeviceId" type="text" placeholder="Optional"
            class="w-full px-2.5 py-1.5 bg-black/20 border border-tactical-border rounded text-sm text-gray-200 placeholder-gray-600 focus:border-tactical-iridium/40 focus:outline-none" />
        </div>
        <button @click="createKey" :disabled="creating || !newLabel.trim()"
          class="px-4 py-1.5 bg-tactical-iridium/20 hover:bg-tactical-iridium/30 border border-tactical-iridium/40 text-tactical-iridium text-sm font-medium rounded transition-colors disabled:opacity-40 disabled:cursor-not-allowed">
          {{ creating ? 'Creating...' : 'Create' }}
        </button>
      </div>
    </div>

    <!-- Error -->
    <p v-if="error" class="text-xs text-red-400 mb-3">{{ error }}</p>

    <!-- Keys table -->
    <div class="bg-tactical-surface border border-tactical-border rounded-lg overflow-hidden">
      <div v-if="loading" class="p-8 text-center text-gray-500 text-sm">Loading...</div>
      <div v-else-if="!keys || keys.length === 0" class="p-8 text-center text-gray-600 text-sm">
        No API keys yet. Create one above.
      </div>
      <table v-else class="w-full text-sm">
        <thead>
          <tr class="border-b border-tactical-border text-[10px] text-gray-500 uppercase tracking-wider">
            <th class="text-left px-3 py-2 font-medium">Label</th>
            <th class="text-left px-3 py-2 font-medium">Prefix</th>
            <th class="text-left px-3 py-2 font-medium">Role</th>
            <th class="text-left px-3 py-2 font-medium hidden sm:table-cell">Last Used</th>
            <th class="text-left px-3 py-2 font-medium hidden sm:table-cell">Created</th>
            <th class="text-right px-3 py-2 font-medium"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="key in keys" :key="key.id" class="border-b border-tactical-border/50 hover:bg-white/[0.02]">
            <td class="px-3 py-2 text-gray-200">{{ key.label }}</td>
            <td class="px-3 py-2 font-mono text-xs text-gray-400">{{ key.key_prefix }}...</td>
            <td class="px-3 py-2">
              <span class="px-1.5 py-0.5 text-[10px] font-medium rounded border" :class="roleBadge[key.role] || 'text-gray-400'">
                {{ key.role }}
              </span>
            </td>
            <td class="px-3 py-2 text-gray-500 text-xs hidden sm:table-cell">{{ formatDate(key.last_used) }}</td>
            <td class="px-3 py-2 text-gray-500 text-xs hidden sm:table-cell">{{ formatDate(key.created_at) }}</td>
            <td class="px-3 py-2 text-right">
              <button @click="revokeKey(key.id)"
                class="text-[10px] text-red-400/60 hover:text-red-400 transition-colors">
                Revoke
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Help text -->
    <p class="mt-4 text-[10px] text-gray-600 leading-relaxed">
      API keys provide programmatic access to the MeshSat Hub API. Use them in the
      <code class="text-gray-500">Authorization: Bearer meshsat_...</code> header.
      Keys are shown once at creation — store them securely.
      Roles: <strong class="text-gray-500">viewer</strong> (read-only),
      <strong class="text-gray-500">operator</strong> (read-write),
      <strong class="text-gray-500">owner</strong> (full admin).
    </p>
  </div>
</template>
