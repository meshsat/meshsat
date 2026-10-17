<script setup>
import { ref, computed, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import ContactPicker from '@/components/ContactPicker.vue'
import IntentStatus from '@/components/IntentStatus.vue'
import PrecedenceChips from '@/components/PrecedenceChips.vue'
import USMTFForm from '@/components/USMTFForm.vue'
import FieldHelp from '@/components/FieldHelp.vue'

// Compose view — the "money screen" per UX-AUDIT-AND-REDESIGN §9.2.
// Contact first, precedence second, body third, Send. Engineer Mode
// reveals per-bearer overrides + the strategy selector. [MESHSAT-551]

const store = useMeshsatStore()

const contact = ref(null)
const precedence = ref('Routine')
const strategy = ref('')  // empty → server picks (contact policy → precedence default → default)
const bearerOverride = ref('')  // single kind when Engineer wants to force one bearer
const text = ref('')

// No team broadcast here: the bridge has no team fan-out and answers a
// group_id with 400 (it used to be offered and fail). [MESHSAT-1584]

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
    const key = kindLabel(a.kind)
    if (!seen.has(key)) seen.set(key, { kind: key, count: 0 })
    seen.get(key).count++
  }
  return Array.from(seen.values())
})

// Directory kinds are upper case (MESHTASTIC, SMS, IRIDIUM_IMT, ...).
function kindLabel(kind) {
  const k = (kind || '').toLowerCase()
  if (k === 'meshtastic') return 'mesh'
  if (k === 'reticulum') return 'lxmf'
  return k
}

function bearerColour(kind) {
  switch (kindLabel(kind)) {
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

// One Idempotency-Key per message: a send whose answer never came (the
// network dropped it) is retried under the same key, so the bridge returns
// the first submission instead of sending twice; any answer, or a changed
// message, starts a new key. crypto.randomUUID needs a secure context and
// the kits serve plain http, so the key is built from getRandomValues.
// [MESHSAT-1584]
let pendingKey = ''
let pendingFor = ''
function newKey() {
  const b = new Uint8Array(16)
  crypto.getRandomValues(b)
  return 'spa-' + Array.from(b, x => x.toString(16).padStart(2, '0')).join('')
}

// A definitive answer from the bridge: it refused the request before
// anything was written (a 4xx: invalid, conflicting, too long, no route),
// or its own "no bearer took it" (503 with its per-bearer body). Anything
// else (no answer, a 500, or a 502/503/504 from something between) leaves
// the first send unresolved: it may have been queued, so the key stays and
// Send again retries the same message without sending it twice (review
// B10-R1). [MESHSAT-1584]
function definitive(e) {
  if (!e.status) return false
  if (e.status >= 400 && e.status < 500) return true
  return e.status === 503 && !!e.body && Array.isArray(e.body.per_bearer)
}

function refusalText(e) {
  switch (e.status) {
    case 413: return 'Too long for every route this contact has.'
    case 422: return 'No route of this contact can carry this message.'
    case 503: if (definitive(e)) return 'No bearer took the message.'
  }
  if (!definitive(e)) {
    const why = e.status ? `HTTP ${e.status}` : 'no answer'
    return `Not known whether it was queued (${why}). Send again to retry the same message: it is not sent twice.`
  }
  return e.message || 'Send failed'
}

// The latest delivery intents to the picked contact (empty with delivery
// intents off). Each answer belongs to the contact and the request that
// asked: a slow answer for an earlier contact, or its error, never lands
// under the one picked now (review B10-R2). [MESHSAT-1584]
const recent = ref([])
const recentFor = ref('')
const openRecent = ref('')
let recentGen = 0
async function refreshRecent() {
  const gen = ++recentGen
  const id = contact.value?.id ? String(contact.value.id) : ''
  if (!id) { recent.value = []; recentFor.value = ''; return }
  let list = []
  try { list = (await store.fetchIntents(id, 5)) || [] } catch { list = [] }
  if (gen !== recentGen || id !== String(contact.value?.id || '')) return
  recent.value = list.filter(r => r.contact_id === id)
  recentFor.value = id
}
watch(contact, () => { openRecent.value = ''; recent.value = []; recentFor.value = ''; refreshRecent() })
const recentOthers = computed(() => {
  const shown = new Set(result.value?.intent_ids || [])
  if (recentFor.value !== String(contact.value?.id || '')) return []
  return recent.value.filter(r => !shown.has(r.id))
})
function labelTone(o) {
  if (['delivered_handset', 'delivered_device', 'accepted_destination_node', 'acked_station'].includes(o)) return 'text-emerald-400'
  if (o === 'may_have_been_sent') return 'text-amber-400'
  if (o === 'failed' || o === 'expired') return 'text-red-400'
  if (o === 'pending') return 'text-sky-400'
  return 'text-gray-300'
}

const canSend = computed(() => {
  if (sending.value) return false
  if (!text.value.trim()) return false
  return !!contact.value
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
      text: text.value,
      precedence: precedence.value
    }
    payload.contact_id = String(contact.value.id)
    // Engineer-mode force single bearer via strategy=PRIMARY_ONLY and
    // a contact-scoped filter isn't supported yet — for now pass the
    // strategy through and let the dispatcher pick. A follow-up
    // (MESHSAT-552) adds per-bearer tick rendering in Inbox.
    if (strategy.value) payload.strategy = strategy.value
    const fingerprint = JSON.stringify(payload)
    if (!pendingKey || fingerprint !== pendingFor) { pendingKey = newKey(); pendingFor = fingerprint }
    result.value = await store.sendToContact(payload, pendingKey)
    pendingKey = ''
    // Clear body on success; keep contact + precedence for quick resend.
    if (result.value?.queued) text.value = ''
    refreshRecent()
  } catch (e) {
    // A definitive refusal ends this message: the next Send is a new one.
    // An unresolved answer keeps the key for the retry.
    if (definitive(e)) pendingKey = ''
    if (e.body?.per_bearer) result.value = e.body
    error.value = refusalText(e)
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <div class="max-w-2xl mx-auto space-y-4">
    <h1 class="text-lg font-display font-semibold text-gray-200 tracking-wide">Compose</h1>

    <!-- 1. Contact picker (directory contacts) -->
    <ContactPicker v-model="contact" source="directory" />

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
          <FieldHelp text="PRIMARY_ONLY queues on the highest-ranked bearer only. ALL_BEARERS queues one delivery per bearer kind. ORDERED_FALLBACK tries the bearers one after another and needs delivery intents (MESHSAT_DELIVERY_INTENTS); without them the bridge refuses it. Empty = use the contact's stored policy." />
        </label>
        <select v-model="strategy" class="w-full px-2 py-1.5 rounded bg-tactical-surface border border-tactical-border text-xs">
          <option value="">Auto (policy)</option>
          <option value="PRIMARY_ONLY">Primary only</option>
          <option value="ALL_BEARERS">All bearers</option>
          <option value="ORDERED_FALLBACK">Ordered fallback (delivery intents)</option>
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
      <textarea v-model="text" rows="4" enterkeyhint="send"
        class="w-full px-3 py-2 rounded bg-tactical-surface border border-tactical-border text-sm focus:outline-none focus:border-tactical-iridium resize-y"
        placeholder="Type your message…"></textarea>
    </div>

    <!-- 6. Send + feedback -->
    <div class="flex items-center justify-between gap-3">
      <div class="text-xs text-gray-500">
        <span v-if="result?.queued && result.intent_ids?.length" class="text-gray-300">
          <span v-if="result.existing">Already sent; its status is below.</span><span v-else>Sent to the delivery queue; its status is below.</span>
        </span>
        <span v-else-if="result?.queued" class="text-gray-300">
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

    <!-- Delivery intents: the status of each recipient's message [MESHSAT-1584] -->
    <div v-if="result?.intent_ids?.length" class="space-y-2">
      <div class="text-[10px] font-medium text-gray-400 uppercase tracking-wide">Delivery status</div>
      <IntentStatus v-for="id in result.intent_ids" :key="id" :intent-id="id" @closed="refreshRecent" />
    </div>

    <!-- Per-bearer breakdown: queued is not delivered, so no tick -->
    <div v-else-if="result?.per_bearer?.length" class="border border-tactical-border rounded p-3">
      <div class="text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-2">
        Per bearer
      </div>
      <div class="space-y-1">
        <div v-for="b in result.per_bearer" :key="b.kind" class="flex items-center justify-between gap-2 text-xs">
          <span class="font-mono">{{ kindLabel(b.kind) }}</span>
          <span v-if="b.delivery_ids?.length" class="text-gray-300">
            queued #{{ b.delivery_ids.join(', #') }}
          </span>
          <span v-else-if="b.error" class="text-red-400 text-right">{{ b.error }}</span>
        </div>
      </div>
    </div>

    <!-- Recent messages to this contact [MESHSAT-1584] -->
    <div v-if="recentOthers.length" class="border border-tactical-border rounded p-3">
      <div class="text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-1">
        Recent to {{ contact?.display_name }}
      </div>
      <ul class="divide-y divide-tactical-border">
        <li v-for="r in recentOthers" :key="r.id" class="py-1">
          <button type="button" @click="openRecent = openRecent === r.id ? '' : r.id"
            class="w-full flex items-center justify-between gap-3 text-left text-xs min-h-[36px]">
            <span class="truncate text-gray-400">“{{ r.text }}”</span>
            <span class="shrink-0" :class="labelTone(r.outcome)">{{ r.label }}</span>
          </button>
          <IntentStatus v-if="openRecent === r.id" :intent-id="r.id" :initial="r" class="mt-1" />
        </li>
      </ul>
    </div>
  </div>
</template>
