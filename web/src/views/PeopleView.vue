<script setup>
import { ref, computed, onMounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import TrustDots from '@/components/TrustDots.vue'
import ContactDetail from '@/components/ContactDetail.vue'

// People view — directory browser with trust dots, per-bearer filter
// chips, search, and a right-hand detail pane. [MESHSAT-553]
//
// The backend `/api/contacts` is lean on metadata today (no team /
// role / trust_level surfaced per contact). We map what's
// available and let the UI gracefully show N/A where the backend
// hasn't wired the field yet. Team/role become available when the
// Hub directory sync lands the richer Contact object (MESHSAT-540).

const store = useMeshsatStore()
const query = ref('')
const filterKind = ref('')
const selected = ref(null)

onMounted(() => {
  store.fetchContacts()
})

const list = computed(() => store.contacts || [])

const bearerKinds = computed(() => {
  const set = new Set()
  for (const c of list.value) {
    for (const a of (c.addresses || [])) {
      set.add((a.type || a.kind || '').toLowerCase())
    }
  }
  set.delete('')
  return Array.from(set).sort()
})

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  return list.value.filter(c => {
    // Kind filter
    if (filterKind.value) {
      const hit = (c.addresses || []).some(a =>
        (a.type || a.kind || '').toLowerCase() === filterKind.value)
      if (!hit) return false
    }
    // Search query (name / address / notes)
    if (q) {
      if ((c.display_name || '').toLowerCase().includes(q)) return true
      if ((c.notes || '').toLowerCase().includes(q)) return true
      return (c.addresses || []).some(a =>
        (a.address || a.value || '').toLowerCase().includes(q))
    }
    return true
  })
})

function select(c) { selected.value = c }

function onVerify(c) {
  // QR rescan flow lives in MESHSAT-560 (S4-02). Until it lands,
  // surface a toast-style hint so operators know the flow exists.
  alert(`QR rescan flow for "${c.display_name}" — wired in MESHSAT-560 (S4-02).`)
}

// Directory sync status footer — best-effort from what the store
// already tracks. Hub connection sits under store.status.hub (wired
// by main.go's status endpoint); lastSync is future MESHSAT-540 work.
const hubStatus = computed(() => {
  const s = store.status?.hub
  if (!s) return { state: 'unknown', label: '—' }
  if (s.connected) return { state: 'connected', label: 'Hub connected' }
  return { state: 'disconnected', label: 'Hub disconnected' }
})

const kindColour = (k) => {
  if (k.startsWith('mesh')) return 'text-blue-400 border-blue-500/40'
  if (k === 'sms' || k.startsWith('cellular')) return 'text-emerald-400 border-emerald-500/40'
  if (k.startsWith('iridium') || k === 'sbd' || k === 'imt') return 'text-amber-400 border-amber-500/40'
  if (k === 'aprs' || k.startsWith('ax25')) return 'text-teal-400 border-teal-500/40'
  if (k.startsWith('reticulum') || k.startsWith('rns')) return 'text-violet-400 border-violet-500/40'
  return 'text-gray-400 border-gray-500/40'
}
</script>

<template>
  <div class="max-w-5xl mx-auto space-y-3">
    <header class="flex items-center justify-between">
      <h1 class="text-lg font-display font-semibold text-gray-200 tracking-wide">People</h1>
      <div class="text-xs text-gray-500">{{ filtered.length }} / {{ list.length }}</div>
    </header>

    <!-- Search + filters -->
    <div class="flex flex-wrap items-center gap-2">
      <input type="text" v-model="query" placeholder="Search name, address, or note…"
        class="flex-1 min-w-[200px] px-3 py-2 rounded bg-tactical-surface border border-tactical-border text-sm focus:outline-none focus:border-tactical-iridium min-h-[40px]" />
      <div class="flex flex-wrap gap-1.5">
        <button type="button" @click="filterKind = ''"
          class="px-2 py-1 rounded border text-[10px] font-medium uppercase tracking-wide"
          :class="filterKind === '' ? 'border-tactical-iridium text-tactical-iridium bg-tactical-iridium/10' : 'border-tactical-border text-gray-500 hover:text-gray-300'">
          All
        </button>
        <button v-for="k in bearerKinds" :key="k" type="button" @click="filterKind = k"
          class="px-2 py-1 rounded border text-[10px] font-medium uppercase tracking-wide"
          :class="filterKind === k ? kindColour(k) + ' bg-white/5' : 'border-tactical-border text-gray-500 hover:text-gray-300'">
          {{ k }}
        </button>
      </div>
    </div>

    <!-- Two-column layout: list + detail -->
    <div class="grid grid-cols-1 lg:grid-cols-[1fr_24rem] gap-3 min-h-[400px]">
      <!-- Contact list -->
      <ul class="bg-tactical-surface border border-tactical-border rounded divide-y divide-tactical-border overflow-hidden">
        <li v-if="!filtered.length" class="px-3 py-4 text-sm text-gray-500">
          No contacts match.
        </li>
        <li v-for="c in filtered" :key="c.id" @click="select(c)"
          role="button" tabindex="0" @keyup.enter="select(c)"
          class="flex items-center gap-3 px-3 py-2.5 cursor-pointer min-h-[48px] transition-colors"
          :class="selected && selected.id === c.id ? 'bg-tactical-iridium/10' : 'hover:bg-white/5'">
          <TrustDots :level="Number(c.trust_level || 0)" />
          <div class="min-w-0 flex-1">
            <div class="text-sm text-gray-200 truncate">{{ c.display_name }}</div>
            <div v-if="c.addresses && c.addresses.length" class="text-[10px] text-gray-500 truncate">
              {{ c.addresses.map(a => a.type || a.kind).join(' · ') }}
            </div>
          </div>
          <span class="text-[10px] text-gray-600 tabular-nums">
            {{ (c.addresses || []).length }}
          </span>
        </li>
      </ul>

      <!-- Detail pane (visible when a contact is selected, or on lg+). -->
      <div v-show="selected" class="lg:block">
        <ContactDetail v-if="selected" :contact="selected" @close="selected = null" @verify="onVerify" />
      </div>
      <div v-show="!selected" class="hidden lg:flex items-center justify-center bg-tactical-surface border border-dashed border-tactical-border rounded text-xs text-gray-500 p-6">
        Select a contact to see addresses, groups, and trust level.
      </div>
    </div>

    <!-- Sync status footer -->
    <footer class="flex items-center justify-between text-[10px] text-gray-500 px-1">
      <div class="flex items-center gap-2">
        <span class="w-1.5 h-1.5 rounded-full"
          :class="hubStatus.state === 'connected' ? 'bg-emerald-400' : hubStatus.state === 'disconnected' ? 'bg-red-400' : 'bg-gray-600'" />
        <span>{{ hubStatus.label }}</span>
      </div>
      <div>
        Directory snapshot sync: MESHSAT-540 (pending)
      </div>
    </footer>
  </div>
</template>
