<script setup>
// Settings > Media Hopping: the switch (off by default) and, while it
// watches or gates, each link's state in the hop set, its last probe, and
// what each OOB peer reported about its own links over STATUS-NET (shown,
// never acted on). Off changes nothing about routing; watch only records;
// on lets routes skip links the hop set takes out of use. [MESHSAT-1650]
import { ref, computed, onMounted, onUnmounted } from 'vue'
import api from '@/api/client'

const st = ref(null)
const mode = ref('off')
const chosen = ref('off')
const loadError = ref('')
const saving = ref(false)
const saveMsg = ref('')
// edited: the operator picked a position not saved yet. The refresh never
// overwrites it; a save or Cancel settles it (R49-S2).
const edited = ref(false)
let timer = null
let stopped = false

const modes = [
  { id: 'off', label: 'Off', text: 'Nothing runs. Messages are routed exactly as before media hopping. This is the default.' },
  { id: 'watch', label: 'Watch', text: 'The Bridge keeps track of every link from what it sees and shows it below. Routing does not change.' },
  { id: 'on', label: 'On', text: 'Messages skip a link the hop set has taken out of use, and use it again once it is back.' }
]

const links = computed(() => st.value?.links || [])
const peers = computed(() => st.value?.peers || [])
const cfg = computed(() => st.value?.config || {})

async function load() {
  try {
    const [m, s] = await Promise.all([api.get('/hopset/mode'), api.get('/hopset')])
    mode.value = m.mode
    if (!saving.value && !edited.value) chosen.value = m.mode
    st.value = s
    loadError.value = ''
  } catch (e) {
    loadError.value = e.message
  }
}

function pick(id) {
  chosen.value = id
  edited.value = id !== mode.value
  saveMsg.value = ''
}

function cancel() {
  chosen.value = mode.value
  edited.value = false
}

async function save() {
  if (chosen.value === mode.value) return
  if (chosen.value === 'on' && !confirm('Turn media hopping on? Messages will skip links the hop set takes out of use.')) return
  saving.value = true
  saveMsg.value = ''
  try {
    const r = await api.put('/hopset/mode', { mode: chosen.value })
    mode.value = r.mode
    chosen.value = r.mode
    edited.value = false
    saveMsg.value = `Media hopping is ${r.mode} now.`
    await load()
  } catch (e) {
    saveMsg.value = e.message
  } finally {
    saving.value = false
  }
}

function mins(s) {
  return s >= 60 ? `${Math.round(s / 60)} min` : `${s} s`
}

function stateClass(l) {
  if (l.down || !l.usable) return 'text-red-400'
  if (l.state === 'suspect' || l.retained) return 'text-amber-400'
  return 'text-green-400'
}

function signs(l) {
  if (!l.signs?.length) return '-'
  return l.signs.map(s => s.source + (s.key && s.key !== '-' ? ':' + s.key : '')).join(', ')
}

function when(t) {
  return t ? new Date(t).toLocaleTimeString() : '-'
}

// ago is how long ago t was, in whole minutes ("now" under one).
function ago(t) {
  if (!t) return '-'
  const m = Math.floor((Date.now() - new Date(t).getTime()) / 60000)
  return m < 1 ? 'now' : `${m} min ago`
}

function peerStateClass(state) {
  if (state === 'excluded' || state === 'down' || state === 'recovering') return 'text-red-400'
  if (state === 'suspect' || state === 'retained') return 'text-amber-400'
  return 'text-green-400'
}

// The status refreshes every 5 s, one load after the other.
function schedule() {
  if (stopped) return
  timer = setTimeout(async () => {
    await load()
    schedule()
  }, 5000)
}

onMounted(async () => {
  await load()
  schedule()
})
onUnmounted(() => {
  stopped = true
  clearTimeout(timer)
})
</script>

<template>
  <div class="space-y-4">
    <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
      <h4 class="text-sm font-medium text-gray-200">Media hopping</h4>
      <p class="text-[11px] text-gray-500">
        The hop set judges each link from evidence that does not depend on one contact: the radio refusing to send,
        failures to several destinations, a busy link gone quiet, a jammed band. It takes a link out of use on two
        such signs and brings it back slowly. It never takes the last free link, and never leaves fewer than two.
      </p>
      <div v-if="loadError" class="text-[11px] text-red-400">{{ loadError }}</div>
      <div class="space-y-2" data-testid="hop-modes">
        <label v-for="m in modes" :key="m.id" class="flex items-start gap-2 text-[11px] cursor-pointer">
          <input type="radio" name="hop-mode" :value="m.id" :checked="chosen === m.id" @change="pick(m.id)" class="mt-0.5" :data-testid="'hop-mode-' + m.id" />
          <span><span class="text-gray-200">{{ m.label }}</span><span v-if="m.id === mode" class="text-teal-400"> (now)</span>
            <span class="block text-gray-500">{{ m.text }}</span></span>
        </label>
      </div>
      <div class="flex items-center gap-3">
        <button :disabled="saving || chosen === mode" @click="save" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500 disabled:opacity-40" data-testid="hop-save">Save</button>
        <button v-if="edited" :disabled="saving" @click="cancel" class="px-4 py-2 rounded bg-gray-700 text-gray-200 text-sm hover:bg-gray-600 disabled:opacity-40" data-testid="hop-cancel">Cancel</button>
        <span v-if="edited" class="text-[11px] text-gray-400" data-testid="hop-unsaved">Not saved yet: still {{ mode }}.</span>
        <span v-if="saveMsg" class="text-[11px] text-amber-300" data-testid="hop-save-msg">{{ saveMsg }}</span>
      </div>
      <p class="text-[11px] text-gray-500">Takes effect at once and stays after a restart. Turning it off forgets what the hop set had seen.</p>
    </div>

    <div v-if="st && mode !== 'off'" class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3" data-testid="hop-status">
      <h4 class="text-sm font-medium text-gray-200">Links {{ mode === 'watch' ? '(watching: routing unchanged)' : '(routing on it)' }}</h4>
      <div v-if="!links.length" class="text-[11px] text-gray-500">No links the hop set covers yet.</div>
      <div v-else class="overflow-x-auto">
        <table class="w-full text-[11px]">
          <thead>
            <tr class="text-left text-gray-500">
              <th class="py-1 pr-3">Link</th><th class="pr-3">State</th><th class="pr-3">In use</th><th class="pr-3">Signs</th>
              <th class="pr-3">Back after</th><th class="pr-3">Repeats</th><th class="pr-3">Last heard</th><th>Last probe</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="l in links" :key="l.interface_id" class="border-t border-gray-700" :data-testid="'hop-link-' + l.interface_id">
              <td class="py-1 pr-3 font-mono text-gray-300">{{ l.interface_id }}<span class="text-gray-500"> {{ l.paid ? 'paid' : 'free' }}</span></td>
              <td class="pr-3" :class="stateClass(l)">{{ l.state }}<span v-if="l.reason" class="text-gray-500"> ({{ l.reason }})</span></td>
              <td class="pr-3 text-gray-300">{{ l.deaf ? 'no, deaf' : l.absent ? 'no, absent' : l.usable ? (l.retained ? 'yes, kept by the floor' : 'yes') : 'no' }}</td>
              <td class="pr-3 text-gray-400 font-mono">{{ signs(l) }}</td>
              <td class="pr-3 text-gray-300">{{ l.readmit_after || '-' }}</td>
              <td class="pr-3 text-gray-300">{{ l.flaps }}</td>
              <td class="pr-3 text-gray-300">{{ when(l.last_positive) }}</td>
              <td class="text-gray-300" :data-testid="'hop-probe-' + l.interface_id">{{ when(l.last_probe) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="text-[11px] text-gray-500" data-testid="hop-config">
        Two signs within {{ mins(cfg.trigger_window_s) }} take a link out after {{ cfg.drop_dwell_s }} s. It comes back
        {{ mins(cfg.readmit_dwell_s) }} after it is heard again, longer each time it fails again within the hour, up to
        {{ mins(cfg.readmit_max_s) }}. A link that usually hears traffic every minute and hears nothing for
        {{ mins(cfg.silence_after_s) }} has a sign against it. A probe is a scheduled time-sync request answered on
        the link it went out on: free links only, no extra airtime.
      </p>
    </div>

    <div v-if="st && mode !== 'off'" class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3" data-testid="hop-peers">
      <h4 class="text-sm font-medium text-gray-200">Peers (what they report about their own links)</h4>
      <p class="text-[11px] text-gray-500">
        Each OOB peer's hop state, as it last told us over STATUS-NET. Shown only: it never takes one of our links out of use.
      </p>
      <div v-if="!peers.length" class="text-[11px] text-gray-500" data-testid="hop-peers-none">No peer has reported yet.</div>
      <div v-for="p in peers" :key="p.peer_id" class="text-[11px] space-y-1" :data-testid="'hop-peer-' + p.peer_id">
        <div class="text-gray-300">
          <span class="font-mono">{{ p.alias || ('peer ' + p.peer_id) }}</span>
          <span class="text-gray-500"> on {{ p.bearer }}, {{ ago(p.heard_at) }}, in its {{ p.via }}</span>
          <span v-if="p.cut" class="text-amber-400"> (cut to fit the bearer: more links than shown)</span>
        </div>
        <div class="flex flex-wrap gap-x-3 gap-y-1">
          <span v-for="l in p.links" :key="l.interface_id" class="font-mono">
            <span class="text-gray-400">{{ l.interface_id }}</span> <span :class="peerStateClass(l.state)">{{ l.state }}</span>
          </span>
          <span v-if="!p.links.length" class="text-gray-500">no links</span>
        </div>
      </div>
    </div>
  </div>
</template>
