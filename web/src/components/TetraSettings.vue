<script setup>
// Settings > TETRA: the radio on USB, the gateway's settings and a test
// send. The gateway sets itself up when the radio is plugged in; this tab
// shows what it found and changes what the defaults decide. [MESHSAT-1588]
import { ref, computed, onMounted, onUnmounted } from 'vue'
import api from '@/api/client'

const st = ref(null)
const loadError = ref('')
const form = ref({ mode: 'dmo', destination: '', prefix: '', port: '', acks: 'auto' })
const formLoaded = ref(false)
const saving = ref(false)
const saveMsg = ref('')
const sendTo = ref('')
const sendText = ref('')
const sending = ref(false)
const sendMsg = ref('')
let timer = null

const gw = computed(() => st.value?.gateway || null)
const connected = computed(() => gw.value?.state === 'connected')
const stateClass = computed(() => connected.value ? 'text-green-400' : (gw.value?.state === 'error' ? 'text-red-400' : 'text-amber-400'))
const textBytes = computed(() => {
  // What one SDS holds of this text: Latin-1 one byte a character, else
  // two (UTF-16), plus the 4-byte header, against the radio's limit.
  const t = sendText.value
  const latin = [...t].every(c => c.codePointAt(0) <= 0xff)
  return 4 + (latin ? [...t].length : [...t].reduce((n, c) => n + (c.codePointAt(0) > 0xffff ? 4 : 2), 0))
})
const parts = computed(() => {
  const max = gw.value?.max_text_bytes || 130
  return textBytes.value <= max ? 1 : Math.ceil(textBytes.value / (max - 8))
})

async function load() {
  try {
    st.value = await api.get('/tetra/status')
    loadError.value = ''
    if (!formLoaded.value && st.value?.gateway?.config) {
      const c = st.value.gateway.config
      form.value = { mode: c.mode || 'dmo', destination: c.destination || '', prefix: c.prefix || '', port: c.port || '', acks: c.acks || 'auto' }
      formLoaded.value = true
    }
  } catch (e) {
    loadError.value = e.message
  }
}

async function save() {
  saving.value = true
  saveMsg.value = ''
  try {
    const cfg = {
      mode: form.value.mode,
      destination: form.value.destination.trim(),
      prefix: form.value.prefix.trim(),
      port: form.value.port.trim(),
      acks: form.value.acks,
    }
    await api.put('/gateways/tetra', { enabled: true, config: cfg })
    saveMsg.value = 'Saved. The gateway restarted with the new settings.'
    formLoaded.value = false
    setTimeout(load, 1500)
  } catch (e) {
    saveMsg.value = e.message
  } finally {
    saving.value = false
  }
}

async function send() {
  if (!sendText.value.trim()) return
  sending.value = true
  sendMsg.value = ''
  try {
    const body = { text: sendText.value.trim() }
    if (sendTo.value.trim()) body.to = sendTo.value.trim()
    const r = await api.post('/tetra/send', body)
    sendMsg.value = `Queued as delivery ${r.delivery_id} to ${r.to}`
    sendText.value = ''
    for (let i = 0; i < 30; i++) {
      await new Promise(res => setTimeout(res, 1000))
      const d = await api.get(`/deliveries/${r.delivery_id}`)
      const del = d?.delivery || d
      if (del && !['queued', 'sending'].includes(del.status)) {
        sendMsg.value = `Delivery ${r.delivery_id} to ${r.to}: ${del.status}` + (del.last_error ? ` (${del.last_error})` : '')
        break
      }
    }
  } catch (e) {
    sendMsg.value = e.message
  } finally {
    sending.value = false
    load()
  }
}

function when(t) {
  return t ? new Date(t).toLocaleString() : '-'
}

onMounted(() => {
  load()
  timer = setInterval(load, 5000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <div class="space-y-4">
    <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
      <h4 class="text-sm font-medium text-gray-200">TETRA radio</h4>
      <div v-if="loadError" class="text-[11px] text-red-400">{{ loadError }}</div>
      <div v-else-if="!st" class="text-[11px] text-gray-500">Loading</div>
      <div v-else-if="!st.configured" class="text-[11px] text-gray-400">
        No TETRA radio on this Bridge. Plug a Motorola TETRA handheld (MTP3000 series) into a USB port with its data cable:
        the Bridge finds it, puts it in Direct Mode and creates the link tetra_0 by itself.
      </div>
      <div v-else class="space-y-2 text-[11px]" data-testid="tetra-status">
        <div class="flex justify-between"><span class="text-gray-500">State</span>
          <span :class="stateClass">{{ gw.state }}<span v-if="gw.last_error" class="text-gray-500"> ({{ gw.last_error }})</span></span></div>
        <div class="flex justify-between"><span class="text-gray-500">Radio</span>
          <span class="text-gray-300">{{ gw.radio?.model || '-' }}</span></div>
        <div class="flex justify-between"><span class="text-gray-500">Firmware</span>
          <span class="text-gray-300 font-mono">{{ gw.radio?.firmware || '-' }}</span></div>
        <div class="flex justify-between"><span class="text-gray-500">Mode</span>
          <span class="text-gray-300">{{ gw.mode === 'dmo' ? 'Direct Mode (DMO)' : (gw.mode === 'tmo' ? 'Network (TMO)' : gw.mode) }}</span></div>
        <div class="flex justify-between"><span class="text-gray-500">Selected group / own number</span>
          <span class="text-gray-300 font-mono">{{ gw.group || '-' }} / {{ gw.self || '-' }}</span></div>
        <div class="flex justify-between"><span class="text-gray-500">Unaddressed texts go to</span>
          <span class="text-gray-300 font-mono">{{ gw.destination || '-' }}</span></div>
        <div class="flex justify-between"><span class="text-gray-500">Battery</span>
          <span class="text-gray-300">{{ gw.battery_pct >= 0 ? gw.battery_pct + '%' : 'unknown' }}</span></div>
        <div class="flex justify-between"><span class="text-gray-500">One SDS holds</span>
          <span class="text-gray-300">{{ gw.max_text_bytes }} bytes ({{ gw.max_text_bytes - 4 }} Latin-1 characters)</span></div>
        <div class="flex justify-between"><span class="text-gray-500">Port</span>
          <span class="text-gray-300 font-mono">{{ gw.port || '-' }}</span></div>
        <div class="flex justify-between"><span class="text-gray-500">SDS in / out</span>
          <span class="text-gray-300">{{ gw.messages_in }} / {{ gw.messages_out }}</span></div>
        <div class="flex justify-between"><span class="text-gray-500">Errors / too long / not text</span>
          <span class="text-gray-300">{{ gw.errors }} / {{ gw.refused_too_long }} / {{ gw.non_text_in }}</span></div>
        <div class="flex justify-between"><span class="text-gray-500">Last in / last out</span>
          <span class="text-gray-300">{{ when(gw.last_in) }} / {{ when(gw.last_out) }}</span></div>
        <div class="flex justify-between"><span class="text-gray-500">MeshSat Bridges on the group</span>
          <span class="text-gray-300" data-testid="tetra-peers">{{ gw.peers }}<span v-if="gw.last_peer_seen" class="text-gray-500"> (last heard {{ when(gw.last_peer_seen) }})</span></span></div>
        <div class="flex justify-between"><span class="text-gray-500">Confirmed / not confirmed / no peer to confirm</span>
          <span class="text-gray-300">{{ gw.acked }} / {{ gw.unacked }} / {{ gw.unconfirmed }}<span v-if="gw.repeats" class="text-gray-500">, {{ gw.repeats }} repeats not delivered twice</span></span></div>
        <p class="text-gray-500">{{ gw.waits_for_acks
          ? 'A MeshSat Bridge listens on this group: every text waits for its confirmation, and one it did not confirm is sent again.'
          : 'No MeshSat Bridge has answered on this group: texts go once, unconfirmed, as to any handheld.' }}</p>
        <p class="text-gray-500">While the Bridge runs, incoming texts come here instead of to the radio's screen; the screen gets them back when the gateway stops.</p>
      </div>
    </div>

    <div v-if="st?.configured" class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
      <h4 class="text-sm font-medium text-gray-200">Settings</h4>
      <div class="grid grid-cols-1 md:grid-cols-2 gap-3 text-[11px]">
        <label class="space-y-1">
          <span class="text-gray-400">Radio mode at start</span>
          <select v-model="form.mode" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" data-testid="tetra-mode">
            <option value="dmo">Direct Mode: radio to radio, no network (default)</option>
            <option value="tmo">Network mode (TMO)</option>
            <option value="keep">Leave the radio as it is</option>
          </select>
        </label>
        <label class="space-y-1">
          <span class="text-gray-400">Default destination (group or radio number)</span>
          <input v-model="form.destination" type="text" inputmode="numeric" :placeholder="'empty: the radio\'s selected group' + (gw?.group ? ' (' + gw.group + ')' : '')" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" data-testid="tetra-destination" />
        </label>
        <label class="space-y-1">
          <span class="text-gray-400">Prefix for texts relayed from the mesh</span>
          <input v-model="form.prefix" type="text" maxlength="20" placeholder="empty: none" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" />
        </label>
        <label class="space-y-1">
          <span class="text-gray-400">Delivery confirmation between MeshSat Bridges</span>
          <select v-model="form.acks" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" data-testid="tetra-acks">
            <option value="auto">When a MeshSat Bridge is on the group (default)</option>
            <option value="on">Always wait for a confirmation</option>
            <option value="off">Off: send once, never confirm</option>
          </select>
        </label>
        <label class="space-y-1">
          <span class="text-gray-400">Data port</span>
          <input v-model="form.port" type="text" placeholder="empty: the radio found on USB" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" />
        </label>
      </div>
      <div class="flex items-center gap-3">
        <button :disabled="saving" @click="save" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500 disabled:opacity-40" data-testid="tetra-save">Save TETRA settings</button>
        <span v-if="saveMsg" class="text-[11px] text-amber-300">{{ saveMsg }}</span>
      </div>
    </div>

    <div v-if="st?.configured" class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
      <h4 class="text-sm font-medium text-gray-200">Send a text</h4>
      <p class="text-[11px] text-gray-500">Goes through the delivery ledger like any other message. A text longer than one SDS goes as numbered parts, and a MeshSat kit on the other end joins them again.</p>
      <div class="grid grid-cols-1 md:grid-cols-4 gap-3">
        <input v-model="sendTo" type="text" inputmode="numeric" :placeholder="'to: ' + (gw?.destination || 'group')" class="px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" />
        <input v-model="sendText" type="text" placeholder="Text" class="md:col-span-3 px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" data-testid="tetra-text" @keyup.enter="send" />
      </div>
      <div class="flex items-center gap-3">
        <button :disabled="sending || !connected || !sendText.trim()" @click="send" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500 disabled:opacity-40" data-testid="tetra-send">Send over TETRA</button>
        <span v-if="sendText" class="text-[11px] text-gray-500">{{ textBytes }} bytes{{ parts > 1 ? ', about ' + parts + ' SDS' : ', one SDS' }}</span>
        <span v-if="sendMsg" class="text-[11px] text-amber-300" data-testid="tetra-send-msg">{{ sendMsg }}</span>
      </div>
    </div>
  </div>
</template>
