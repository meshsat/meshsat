<script setup>
import { ref, computed, onMounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import { formatTimestamp, formatRelativeTime } from '@/utils/format'

const store = useMeshsatStore()
const filterInterface = ref('')
const filterEventType = ref('')
const verifyResult = ref(null)
const verifying = ref(false)
const showSigner = ref(false)
const expandedEntry = ref(null)
const currentLimit = ref(100)
const loading = ref(false)

const filteredEntries = computed(() => {
  let entries = store.auditLog || []
  if (filterInterface.value) {
    entries = entries.filter(e => e.interface_id === filterInterface.value)
  }
  if (filterEventType.value) {
    entries = entries.filter(e => e.event_type === filterEventType.value)
  }
  return entries
})

const interfaceIds = computed(() => {
  const ids = new Set()
  for (const e of (store.auditLog || [])) {
    if (e.interface_id) ids.add(e.interface_id)
  }
  return [...ids].sort()
})

const eventTypes = computed(() => {
  const types = new Set()
  for (const e of (store.auditLog || [])) {
    if (e.event_type) types.add(e.event_type)
  }
  return [...types].sort()
})

async function doVerify() {
  verifying.value = true
  verifyResult.value = null
  try {
    verifyResult.value = await store.verifyAuditLog()
  } catch (e) {
    verifyResult.value = { verified: false, error: e.message }
  }
  verifying.value = false
}

async function loadMore() {
  currentLimit.value += 100
  loading.value = true
  await store.fetchAuditLog({ limit: currentLimit.value, interface_id: filterInterface.value || undefined })
  loading.value = false
}

async function refresh() {
  loading.value = true
  await store.fetchAuditLog({ limit: currentLimit.value, interface_id: filterInterface.value || undefined })
  loading.value = false
}

function eventColor(event_type) {
  if (event_type?.includes('deny') || event_type?.includes('drop')) return 'text-red-400 bg-red-400/10'
  if (event_type?.includes('forward') || event_type?.includes('deliver')) return 'text-emerald-400 bg-emerald-400/10'
  if (event_type?.includes('bind') || event_type?.includes('online')) return 'text-teal-400 bg-teal-400/10'
  if (event_type?.includes('unbind') || event_type?.includes('offline')) return 'text-amber-400 bg-amber-400/10'
  if (event_type?.includes('rule')) return 'text-blue-400 bg-blue-400/10'
  return 'text-gray-400 bg-gray-400/10'
}

function directionIcon(dir) {
  if (dir === 'ingress') return '\u2193'
  if (dir === 'egress') return '\u2191'
  return ''
}

function toggleExpand(id) {
  expandedEntry.value = expandedEntry.value === id ? null : id
}

function formatDetail(detail) {
  if (!detail) return ''
  try {
    const parsed = JSON.parse(detail)
    return JSON.stringify(parsed, null, 2)
  } catch {
    return detail
  }
}

onMounted(() => {
  store.fetchAuditLog()
  store.fetchAuditSigner()
})
</script>

<template>
  <div class="max-w-4xl mx-auto">
    <div class="flex items-center justify-between mb-4">
      <h2 class="text-lg font-semibold text-gray-200">Audit Log</h2>
      <div class="flex items-center gap-2">
        <button @click="showSigner = !showSigner"
          class="px-3 py-1.5 rounded text-xs bg-gray-800 text-gray-400 hover:text-gray-200 border border-gray-700">
          Signer
        </button>
        <button @click="doVerify" :disabled="verifying"
          class="px-3 py-1.5 rounded text-xs bg-teal-600 text-white hover:bg-teal-500 disabled:opacity-50">
          {{ verifying ? 'Verifying...' : 'Verify Chain' }}
        </button>
        <button @click="refresh" :disabled="loading"
          class="px-3 py-1.5 rounded text-xs bg-gray-800 text-gray-400 hover:text-gray-200 border border-gray-700 disabled:opacity-50">
          Refresh
        </button>
      </div>
    </div>

    <!-- Verify result banner -->
    <div v-if="verifyResult" class="mb-4 p-3 rounded-lg border text-sm"
      :class="verifyResult.verified ? 'bg-emerald-900/20 border-emerald-700 text-emerald-300' : 'bg-red-900/20 border-red-700 text-red-300'">
      <div class="font-medium">{{ verifyResult.verified ? 'Hash chain integrity verified' : 'Chain verification FAILED' }}</div>
      <div v-if="verifyResult.checked" class="text-xs mt-1 opacity-70">{{ verifyResult.valid }} of {{ verifyResult.checked }} entries valid</div>
      <div v-if="verifyResult.error" class="text-xs mt-1">{{ verifyResult.error }}</div>
      <div v-if="verifyResult.broken_at >= 0 && !verifyResult.verified" class="text-xs mt-1 text-red-400">Chain broken at entry #{{ verifyResult.broken_at }}</div>
    </div>

    <!-- Signer info -->
    <div v-if="showSigner && store.auditSigner" class="mb-4 p-3 rounded-lg bg-gray-800 border border-gray-700">
      <div class="text-[10px] text-gray-500 uppercase mb-1">Local Node Signer (Ed25519)</div>
      <code class="text-xs text-amber-400 break-all select-all block">{{ store.auditSigner.signer_id }}</code>
    </div>

    <!-- Filters -->
    <div class="flex items-center gap-3 mb-4">
      <select v-model="filterInterface" @change="refresh"
        class="px-2 py-1 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300">
        <option value="">All interfaces</option>
        <option v-for="id in interfaceIds" :key="id" :value="id">{{ id }}</option>
      </select>
      <select v-model="filterEventType"
        class="px-2 py-1 rounded bg-gray-900 border border-gray-700 text-xs text-gray-300">
        <option value="">All events</option>
        <option v-for="t in eventTypes" :key="t" :value="t">{{ t }}</option>
      </select>
      <span class="text-xs text-gray-500">{{ filteredEntries.length }} entries</span>
    </div>

    <!-- Entries -->
    <div v-if="filteredEntries.length === 0 && !loading" class="text-center text-gray-500 py-12 text-sm bg-gray-800/50 rounded-lg border border-gray-700">
      No audit events recorded yet.
    </div>

    <div class="space-y-1">
      <div v-for="entry in filteredEntries" :key="entry.id"
        class="bg-gray-800/40 rounded-lg p-3 border border-gray-700/50 hover:border-gray-600 transition-colors cursor-pointer"
        @click="toggleExpand(entry.id)">
        <div class="flex items-center gap-2 mb-1">
          <span class="text-[10px] font-mono px-1.5 py-px rounded" :class="eventColor(entry.event_type)">
            {{ entry.event_type }}
          </span>
          <span v-if="entry.direction" class="text-[10px] font-mono text-gray-500" :title="entry.direction">
            {{ directionIcon(entry.direction) }} {{ entry.direction }}
          </span>
          <span v-if="entry.interface_id" class="text-[10px] font-mono text-gray-500">{{ entry.interface_id }}</span>
          <span v-if="entry.seq_num != null" class="text-[10px] font-mono text-gray-600">#{{ entry.seq_num }}</span>
          <span class="flex-1" />
          <span class="text-[10px] text-gray-600" :title="formatTimestamp(entry.timestamp)">{{ formatRelativeTime(entry.timestamp) }}</span>
        </div>
        <div v-if="entry.detail && entry.detail !== '{}'" class="text-[11px] text-gray-400 font-mono truncate">
          {{ expandedEntry === entry.id ? '' : (typeof entry.detail === 'string' ? entry.detail : JSON.stringify(entry.detail)) }}
        </div>
        <!-- Expanded detail view -->
        <pre v-if="expandedEntry === entry.id && entry.detail && entry.detail !== '{}'"
          class="text-[11px] text-gray-400 font-mono mt-1 p-2 bg-gray-900/50 rounded whitespace-pre-wrap break-all">{{ formatDetail(entry.detail) }}</pre>
        <div class="flex items-center gap-3 mt-1 text-[9px] text-gray-600 font-mono">
          <span v-if="entry.hash" title="Hash chain entry">hash:{{ entry.hash.slice(0, 16) }}...</span>
          <span v-if="entry.prev_hash" title="Previous hash in chain">prev:{{ entry.prev_hash.slice(0, 12) }}...</span>
          <span v-if="entry.delivery_id != null" title="Delivery ID">dlv:{{ entry.delivery_id }}</span>
          <span v-if="entry.rule_id != null" title="Rule ID">rule:{{ entry.rule_id }}</span>
        </div>
      </div>
    </div>

    <!-- Load more -->
    <div v-if="filteredEntries.length >= currentLimit" class="mt-4 text-center">
      <button @click="loadMore" :disabled="loading"
        class="px-4 py-2 rounded text-xs bg-gray-800 text-gray-400 hover:text-gray-200 border border-gray-700 disabled:opacity-50">
        {{ loading ? 'Loading...' : 'Load more' }}
      </button>
    </div>
  </div>
</template>
