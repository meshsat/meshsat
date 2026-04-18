<script setup>
import { computed, ref } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import TrustDots from '@/components/TrustDots.vue'
import SIDCPicker from '@/components/SIDCPicker.vue'

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

// SIDC picker — edits the directory metadata on the contact via
// PUT /api/contacts/{id}. Local draft so we can show a "Save" state
// and only push on confirm.
const sidcDraft = ref(props.contact?.sidc || '')
const sidcDirty = computed(() => sidcDraft.value !== (props.contact?.sidc || ''))
const sidcBusy = ref(false)
async function saveSIDC() {
  sidcBusy.value = true
  try {
    await store.updateContact(props.contact.id, {
      display_name: props.contact.display_name,
      notes: props.contact.notes || '',
      sidc: sidcDraft.value
    })
  } finally {
    sidcBusy.value = false
  }
}

// QR verification flow [MESHSAT-560]. We don't ship the camera
// scanner here — that's MESHSAT-600 on Android + a future web-shell
// story. For the MVP we accept a pasted QR URL / raw JSON, verify
// the signature server-side, and bump trust_level to 3 on match.
const verifyOpen = ref(false)
const verifyInput = ref('')
const verifyError = ref('')
const verifyBusy = ref(false)
function openVerify() { verifyOpen.value = true; verifyInput.value = ''; verifyError.value = '' }
async function confirmVerify() {
  verifyError.value = ''
  if (!verifyInput.value.trim()) { verifyError.value = 'Paste the QR URL from the other device.'; return }
  verifyBusy.value = true
  try {
    // Server parses + verifies signature. If the card's display_name
    // matches the contact's display_name we treat it as proof of
    // in-person verification and bump trust to 3.
    const imported = await store.importContactQR(verifyInput.value.trim())
    if (!imported || imported.display_name !== props.contact.display_name) {
      verifyError.value = "QR card's display name doesn't match this contact."
      return
    }
    await store.verifyContact(props.contact.id, 3, 'qr-in-person')
    verifyOpen.value = false
  } catch (e) {
    verifyError.value = e.message || 'Verification failed.'
  } finally {
    verifyBusy.value = false
  }
}

// My-QR export [MESHSAT-561]. Fetches the signed card and drops it
// into a shareable <textarea> + meshsat:// link. Operators show this
// face-to-face so the remote side can scan / paste.
const myQROpen = ref(false)
const myQR = ref(null)
async function showMyQR() {
  myQROpen.value = true
  myQR.value = null
  try { myQR.value = await store.fetchContactQR(props.contact.id) } catch {}
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

      <!-- Symbol (MIL-STD-2525D SIDC) -->
      <section>
        <SIDCPicker v-model="sidcDraft" />
        <div v-if="sidcDirty" class="mt-2 flex justify-end">
          <button type="button" @click="saveSIDC" :disabled="sidcBusy"
            class="px-3 py-1.5 rounded bg-tactical-iridium text-tactical-bg text-xs font-semibold min-h-[36px]">
            <span v-if="sidcBusy">Saving…</span><span v-else>Save symbol</span>
          </button>
        </div>
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

    <footer class="flex flex-wrap items-center justify-between gap-2 p-3 border-t border-tactical-border">
      <div class="flex gap-2">
        <button type="button" @click="openVerify"
          class="px-3 py-2 rounded border border-tactical-iridium text-tactical-iridium text-xs font-semibold min-h-[40px]">
          Verify in person
        </button>
        <button type="button" @click="showMyQR"
          class="px-3 py-2 rounded border border-tactical-border text-gray-400 text-xs font-semibold min-h-[40px]">
          My QR
        </button>
      </div>
      <button v-show="store.isEngineer" type="button" @click="onDelete" :disabled="busy"
        class="px-3 py-2 rounded border border-red-500/50 text-red-400 text-xs font-semibold min-h-[40px]">
        Delete
      </button>
    </footer>

    <!-- Verify modal -->
    <div v-if="verifyOpen" class="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-4">
      <div class="max-w-md w-full bg-tactical-surface border border-tactical-border rounded p-4 space-y-3">
        <h3 class="text-sm font-semibold text-tactical-iridium">Verify {{ contact.display_name }}</h3>
        <p class="text-xs text-gray-400">
          Ask them to show their QR card, scan it, and paste the
          <span class="font-mono">meshsat://contact/…</span> URL below. Successful
          signature verification bumps trust level to 3.
        </p>
        <textarea v-model="verifyInput" rows="4"
          class="w-full px-3 py-2 rounded bg-tactical-bg border border-tactical-border text-xs font-mono resize-y"
          placeholder="meshsat://contact/…"></textarea>
        <div v-if="verifyError" class="text-xs text-red-400">{{ verifyError }}</div>
        <div class="flex justify-end gap-2">
          <button type="button" @click="verifyOpen = false"
            class="px-3 py-2 rounded border border-tactical-border text-gray-400 text-xs min-h-[40px]">
            Cancel
          </button>
          <button type="button" @click="confirmVerify" :disabled="verifyBusy"
            class="px-3 py-2 rounded bg-tactical-iridium text-tactical-bg text-xs font-semibold min-h-[40px]">
            <span v-if="verifyBusy">Verifying…</span><span v-else>Verify</span>
          </button>
        </div>
      </div>
    </div>

    <!-- My QR modal -->
    <div v-if="myQROpen" class="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-4">
      <div class="max-w-md w-full bg-tactical-surface border border-tactical-border rounded p-4 space-y-3">
        <h3 class="text-sm font-semibold text-gray-200">{{ contact.display_name }} — QR card</h3>
        <div v-if="myQR">
          <textarea readonly :value="myQR.url" rows="4"
            class="w-full px-3 py-2 rounded bg-tactical-bg border border-tactical-border text-xs font-mono resize-none break-all"></textarea>
          <p class="text-[10px] text-gray-500 mt-1">
            Signed by <span class="font-mono">{{ (myQR.signer || '').slice(0, 16) }}…</span>
            — paste into the other device's Verify dialog, or render via any QR generator.
          </p>
        </div>
        <div v-else class="text-xs text-gray-500">Generating…</div>
        <div class="flex justify-end">
          <button type="button" @click="myQROpen = false"
            class="px-3 py-2 rounded border border-tactical-border text-gray-400 text-xs min-h-[40px]">
            Close
          </button>
        </div>
      </div>
    </div>
  </aside>
</template>
