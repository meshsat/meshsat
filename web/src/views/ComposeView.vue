<script setup>
import { ref, computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import ContactPicker from '@/components/ContactPicker.vue'
import PrecedenceChips from '@/components/PrecedenceChips.vue'
import USMTFForm from '@/components/USMTFForm.vue'

// Compose view — the "money screen" per UX-AUDIT-AND-REDESIGN §9.2.
// Contact first, precedence second, body third, Send. Engineer Mode
// reveals per-bearer overrides + the strategy selector. [MESHSAT-551]

const store = useMeshsatStore()

const contact = ref(null)
const precedence = ref('Routine')
const strategy = ref('')  // empty → server picks (contact policy → precedence default → default)
const bearerOverride = ref('')  // single kind when Engineer wants to force one bearer
const text = ref('')

const sending = ref(false)
const result = ref(null)
const error = ref('')

// Trust-level hard-block on high-precedence sends [MESHSAT-560].
// Immediate / Flash / Override require trust_level >= 2 on the
// recipient. Below that we surface a confirmation modal so the
// operator either accepts the risk (degrading to Routine) or bails.
const highPrecedence = ['Immediate', 'Flash', 'Override']
const contactTrust = computed(() => Number(contact.value?.trust_level || 0))
const trustBlocked = computed(() =>
  contact.value &&
  highPrecedence.includes(precedence.value) &&
  contactTrust.value < 2)
const trustConfirm = ref(false)

// Bearer availability preview — enumerate kinds present on the picked
// contact so the operator sees upfront which channels the send can
// traverse. We colour-code by the same palette used elsewhere
// (mesh=green, sat=blue, sms=sky, aprs=amber).
const bearers = computed(() => {
  if (!contact.value) return []
  const seen = new Map()
  for (const a of (contact.value.addresses || [])) {
    const key = a.kind
    if (!seen.has(key)) seen.set(key, { kind: key, count: 0 })
    seen.get(key).count++
  }
  return Array.from(seen.values())
})

function bearerColour(kind) {
  switch (kind) {
    case 'mesh':        return 'text-emerald-400 border-emerald-500/40 bg-emerald-500/10'
    case 'iridium':
    case 'iridium_imt':
    case 'iridium_sbd': return 'text-tactical-iridium border-tactical-iridium/40 bg-tactical-iridium/10'
    case 'sms':
    case 'cellular':    return 'text-sky-400 border-sky-500/40 bg-sky-500/10'
    case 'aprs':        return 'text-amber-400 border-amber-500/40 bg-amber-500/10'
    case 'tak':         return 'text-violet-400 border-violet-500/40 bg-violet-500/10'
    case 'zigbee':      return 'text-fuchsia-400 border-fuchsia-500/40 bg-fuchsia-500/10'
    default:            return 'text-gray-400 border-gray-500/40 bg-gray-500/10'
  }
}

const canSend = computed(() => {
  return !!contact.value && text.value.trim().length > 0 && !sending.value
})

async function onSend() {
  if (!canSend.value) return
  // Trust gate: block high-precedence sends on unverified contacts.
  // [MESHSAT-560]
  if (trustBlocked.value && !trustConfirm.value) {
    trustConfirm.value = true
    return
  }
  sending.value = true
  error.value = ''
  result.value = null
  try {
    const payload = {
      contact_id: String(contact.value.id),
      text: text.value,
      precedence: precedence.value
    }
    // Engineer-mode force single bearer via strategy=PRIMARY_ONLY and
    // a contact-scoped filter isn't supported yet — for now pass the
    // strategy through and let the dispatcher pick. A follow-up
    // (MESHSAT-552) adds per-bearer tick rendering in Inbox.
    if (strategy.value) payload.strategy = strategy.value
    result.value = await store.sendToContact(payload)
    // Clear body on success; keep contact + precedence for quick resend.
    if (result.value?.queued) text.value = ''
  } catch (e) {
    error.value = e.message || 'Send failed'
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <div class="max-w-2xl mx-auto space-y-4">
    <h1 class="text-lg font-display font-semibold text-gray-200 tracking-wide">Compose</h1>

    <!-- 1. Contact picker -->
    <ContactPicker v-model="contact" />

    <!-- 2. Bearer availability preview -->
    <div v-if="contact">
      <div class="text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-1">Available bearers</div>
      <div class="flex flex-wrap gap-1.5">
        <span v-if="!bearers.length" class="text-xs text-amber-400">
          No addresses on this contact — add one before sending.
        </span>
        <span v-for="b in bearers" :key="b.kind"
          class="px-2 py-1 rounded border text-[10px] font-medium uppercase tracking-wide"
          :class="bearerColour(b.kind)">
          {{ b.kind }}<span v-if="b.count > 1" class="ml-1 opacity-70">×{{ b.count }}</span>
        </span>
      </div>
    </div>

    <!-- 3. Precedence -->
    <PrecedenceChips v-model="precedence" />

    <!-- 4. Engineer-only override row -->
    <div v-show="store.isEngineer" class="grid grid-cols-2 gap-3 border border-dashed border-tactical-border rounded p-3">
      <div>
        <label class="block text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-1">
          Strategy (override)
        </label>
        <select v-model="strategy" class="w-full px-2 py-1.5 rounded bg-tactical-surface border border-tactical-border text-xs">
          <option value="">Auto (policy)</option>
          <option value="PRIMARY_ONLY">Primary only</option>
          <option value="ANY_REACHABLE">Any reachable</option>
          <option value="ORDERED_FALLBACK">Ordered fallback</option>
          <option value="HEMB_BONDED">HeMB bonded</option>
          <option value="ALL_BEARERS">All bearers</option>
        </select>
      </div>
      <div>
        <label class="block text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-1">
          Force bearer
        </label>
        <select v-model="bearerOverride" class="w-full px-2 py-1.5 rounded bg-tactical-surface border border-tactical-border text-xs">
          <option value="">No override</option>
          <option v-for="b in bearers" :key="b.kind" :value="b.kind">{{ b.kind }}</option>
        </select>
      </div>
    </div>

    <!-- 4b. USMTF template (Engineer only — structured field forms). [MESHSAT-563] -->
    <USMTFForm v-show="store.isEngineer" v-model="text" />

    <!-- 5. Body -->
    <div>
      <label class="block text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-1">
        Message
      </label>
      <textarea v-model="text" rows="4"
        class="w-full px-3 py-2 rounded bg-tactical-surface border border-tactical-border text-sm focus:outline-none focus:border-tactical-iridium resize-y"
        placeholder="Type your message…"></textarea>
    </div>

    <!-- 6. Send + feedback -->
    <div class="flex items-center justify-between gap-3">
      <div class="text-xs text-gray-500">
        <span v-if="result?.queued" class="text-emerald-400">
          Queued on {{ result.per_bearer?.length || 0 }} bearer<span v-if="(result.per_bearer?.length || 0) !== 1">s</span>.
        </span>
        <span v-else-if="error" class="text-red-400">{{ error }}</span>
      </div>
      <button type="button" @click="onSend" :disabled="!canSend"
        class="px-5 py-3 rounded bg-tactical-iridium text-tactical-bg font-semibold text-sm min-h-[48px] disabled:opacity-40 disabled:cursor-not-allowed">
        <span v-if="sending">Sending…</span>
        <span v-else>Send</span>
      </button>
    </div>

    <!-- Trust-level hard-block modal [MESHSAT-560] -->
    <div v-if="trustConfirm" class="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-4"
      role="dialog" aria-modal="true" aria-labelledby="trust-modal-title">
      <div class="max-w-sm w-full bg-tactical-surface border border-amber-500/60 rounded p-4 space-y-3">
        <h3 id="trust-modal-title" class="text-sm font-semibold text-amber-400">
          Unverified contact
        </h3>
        <p class="text-xs text-gray-300">
          <span class="font-semibold">{{ contact?.display_name }}</span> is at trust level
          <span class="font-mono">{{ contactTrust }}/3</span>. High-priority precedence
          (<span class="font-mono">{{ precedence }}</span>) should only be sent to
          contacts verified in person or by a confirmed key exchange.
        </p>
        <p class="text-xs text-gray-400">
          Verify the contact on the People page, or continue and this message will
          drop to <span class="font-mono">Routine</span>.
        </p>
        <div class="flex justify-end gap-2 pt-1">
          <button type="button" @click="trustConfirm = false"
            class="px-3 py-2 rounded border border-tactical-border text-gray-400 text-xs min-h-[40px]">
            Cancel
          </button>
          <button type="button" @click="trustConfirm = false; precedence = 'Routine'; onSend()"
            class="px-3 py-2 rounded bg-amber-500 text-tactical-bg text-xs font-semibold min-h-[40px]">
            Send as Routine
          </button>
        </div>
      </div>
    </div>

    <!-- Per-bearer delivery breakdown -->
    <div v-if="result?.per_bearer?.length" class="border border-tactical-border rounded p-3">
      <div class="text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-2">
        Per-bearer delivery
      </div>
      <div class="space-y-1">
        <div v-for="b in result.per_bearer" :key="b.kind" class="flex items-center justify-between text-xs">
          <span class="font-mono">{{ b.kind }}</span>
          <span v-if="b.delivery_ids?.length" class="text-emerald-400">
            ✓ {{ b.delivery_ids.join(', ') }}
          </span>
          <span v-else-if="b.error" class="text-red-400">{{ b.error }}</span>
        </div>
      </div>
    </div>
  </div>
</template>
