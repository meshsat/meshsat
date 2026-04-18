<script setup>
import { computed, ref } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import TrustDots from '@/components/TrustDots.vue'

// Right-hand detail pane for the People view. Shows addresses,
// groups, policy, and (Engineer only) per-address keys + a
// "Verify in person" action that flips to QR rescan. [MESHSAT-553]
//
// Per-contact groups/policy are not yet surfaced on /api/contacts.
// The Phase 1 directory schema has the join tables
// (directory_group_members, directory_dispatch_policy); the bridge
// REST layer exposes them on the /api/directory/* path once the
// S1-05 Hub endpoints are in. Until then we render a "not wired
// yet" placeholder so the right pane still makes visual sense.

const props = defineProps({
  contact: { type: Object, required: true }
})
const emit = defineEmits(['close', 'verify'])

const store = useMeshsatStore()

const trustLevel = computed(() => Number(props.contact?.trust_level || 0))

const addresses = computed(() => props.contact?.addresses || [])

function bearerColour(kind) {
  const k = (kind || '').toLowerCase()
  if (k.startsWith('mesh'))                                 return 'text-blue-400'
  if (k === 'sms' || k.startsWith('cellular'))              return 'text-emerald-400'
  if (k.startsWith('iridium') || k === 'sbd' || k === 'imt')return 'text-amber-400'
  if (k === 'aprs' || k.startsWith('ax25'))                 return 'text-teal-400'
  if (k.startsWith('reticulum') || k.startsWith('rns'))     return 'text-violet-400'
  return 'text-gray-400'
}

const busy = ref(false)
async function onDelete() {
  if (!confirm(`Delete contact "${props.contact.display_name}"? This cannot be undone.`)) return
  busy.value = true
  try {
    await store.deleteContact(props.contact.id)
    emit('close')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <aside class="flex flex-col h-full bg-tactical-surface border border-tactical-border rounded">
    <header class="flex items-start justify-between gap-2 p-3 border-b border-tactical-border">
      <div class="min-w-0">
        <div class="flex items-center gap-2">
          <TrustDots :level="trustLevel" />
          <h2 class="text-sm font-semibold text-gray-200 truncate">{{ contact.display_name }}</h2>
        </div>
        <div v-if="contact.notes" class="text-[10px] text-gray-500 mt-1 truncate">{{ contact.notes }}</div>
      </div>
      <button type="button" @click="emit('close')"
        class="text-gray-500 hover:text-gray-300 p-1" aria-label="Close">
        <svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 18L18 6M6 6l12 12"/></svg>
      </button>
    </header>

    <div class="flex-1 overflow-y-auto p-3 space-y-4 text-sm">
      <!-- Addresses -->
      <section>
        <div class="text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-1">Addresses</div>
        <div v-if="!addresses.length" class="text-xs text-gray-500">None.</div>
        <ul v-else class="space-y-1">
          <li v-for="a in addresses" :key="a.id" class="flex items-center justify-between gap-2 px-2 py-1 rounded bg-tactical-bg/40">
            <span class="text-[10px] font-mono uppercase" :class="bearerColour(a.type || a.kind)">
              {{ (a.type || a.kind || '?').toLowerCase() }}
            </span>
            <span class="text-xs font-mono truncate" :title="a.address || a.value">{{ a.address || a.value }}</span>
            <span v-if="a.label" class="text-[10px] text-gray-500">{{ a.label }}</span>
          </li>
        </ul>
      </section>

      <!-- Groups + policy placeholder until S1-05 surfaces them -->
      <section>
        <div class="text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-1">Groups & policy</div>
        <div class="text-xs text-gray-500">
          Not wired on the bridge REST layer yet — arrives with MESHSAT-538/540.
        </div>
      </section>

      <!-- Keys: Engineer only -->
      <section v-show="store.isEngineer">
        <div class="text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-1">Keys (Engineer)</div>
        <div class="text-xs text-gray-500">
          Per-contact AES key management lands with MESHSAT-537.
          Key inventory (/api/keys) is today still keyed per-channel
          rather than per-contact.
        </div>
      </section>
    </div>

    <footer class="flex items-center justify-between gap-2 p-3 border-t border-tactical-border">
      <button type="button" @click="emit('verify', contact)"
        class="px-3 py-2 rounded border border-tactical-iridium text-tactical-iridium text-xs font-semibold min-h-[40px]">
        Verify in person
      </button>
      <button v-show="store.isEngineer" type="button" @click="onDelete" :disabled="busy"
        class="px-3 py-2 rounded border border-red-500/50 text-red-400 text-xs font-semibold min-h-[40px]">
        Delete
      </button>
    </footer>
  </aside>
</template>
