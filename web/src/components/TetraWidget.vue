<script setup>
// The TETRA card on the engineer dashboard: the radio, its counters, the
// last texts it carried, and a quick send. The card shell (drag, remove)
// is the dashboard's; the handle slot sits before the title. [MESHSAT-1588]
import { ref, computed, onMounted, onUnmounted } from 'vue'
import api from '@/api/client'
import GatewayRateSparkline from '@/components/GatewayRateSparkline.vue'

defineProps({ samples: { type: Array, default: () => [] } })

const st = ref(null)
const recent = ref([])
const text = ref('')
const sending = ref(false)
const sendMsg = ref('')
let timer = null

const gw = computed(() => st.value?.gateway || null)
const up = computed(() => gw.value?.state === 'connected')
const dotClass = computed(() => up.value ? 'bg-cyan-400' : (gw.value?.state === 'connecting' ? 'bg-amber-500 animate-pulse' : 'bg-gray-600'))
const model = computed(() => (gw.value?.radio?.model || '').split(' ')[0])

async function load() {
  try {
    st.value = await api.get('/tetra/status')
    const m = await api.get('/messages', { transport: 'tetra', limit: 4 })
    recent.value = m?.messages || []
  } catch { /* the card shows what it last had */ }
}

async function send() {
  const t = text.value.trim()
  if (!t) return
  sending.value = true
  sendMsg.value = ''
  try {
    const r = await api.post('/tetra/send', { text: t })
    sendMsg.value = `Queued to ${r.to.replace('tetra:', 'group ')}`
    text.value = ''
  } catch (e) {
    sendMsg.value = e.message
  } finally {
    sending.value = false
  }
}

function ago(t) {
  const s = Math.max(0, Math.round((Date.now() - new Date(t).getTime()) / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.round(s / 60)}m`
  return `${Math.round(s / 3600)}h`
}

onMounted(() => {
  load()
  timer = setInterval(load, 5000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <div data-testid="tetra-widget">
    <div class="flex items-center justify-between mb-3">
      <div class="flex items-center gap-2">
        <slot name="handle" />
        <span class="font-display font-semibold text-sm text-cyan-400 tracking-wide">TETRA</span>
        <span v-if="gw?.mode === 'dmo'" class="text-[9px] font-mono px-1.5 py-0.5 rounded bg-cyan-400/10 text-cyan-300" title="Direct Mode: radio to radio, no network">DMO</span>
        <span v-else-if="gw?.mode === 'tmo'" class="text-[9px] font-mono px-1.5 py-0.5 rounded bg-gray-700 text-gray-300" title="Network mode">TMO</span>
      </div>
      <div class="flex items-center gap-2">
        <span v-if="gw?.battery_pct >= 0" class="text-[10px] font-mono text-gray-500" title="Radio battery">{{ gw.battery_pct }}%</span>
        <GatewayRateSparkline :samples="samples" variant="tetra" />
        <span :class="dotClass" class="w-2 h-2 rounded-full" :title="gw?.state || 'no radio'" />
      </div>
    </div>

    <div v-if="st && !st.configured" class="text-[10px] text-gray-500 text-center py-4">
      No TETRA radio on USB. Plug one in and it sets itself up.
    </div>
    <template v-else-if="gw">
      <div class="grid grid-cols-4 gap-2 mb-3">
        <div class="text-center">
          <div class="text-[10px] text-gray-500">Group</div>
          <div class="text-sm font-mono text-cyan-400">{{ gw.group || '-' }}</div>
        </div>
        <div class="text-center">
          <div class="text-[10px] text-gray-500">RX</div>
          <div class="text-sm font-mono text-gray-300">{{ gw.messages_in }}</div>
        </div>
        <div class="text-center">
          <div class="text-[10px] text-gray-500">TX</div>
          <div class="text-sm font-mono text-gray-300">{{ gw.messages_out }}</div>
        </div>
        <div class="text-center">
          <div class="text-[10px] text-gray-500">Errors</div>
          <div class="text-sm font-mono" :class="gw.errors > 0 ? 'text-red-400' : 'text-gray-500'">{{ gw.errors }}</div>
        </div>
      </div>

      <div v-if="!up" class="text-[10px] text-amber-400 mb-2">Radio {{ gw.state }}<span v-if="gw.last_error" class="text-gray-500">: {{ gw.last_error }}</span></div>

      <div v-if="recent.length" class="space-y-1 mb-3">
        <div v-for="m in recent" :key="m.id" class="flex items-baseline gap-2 px-2 py-1 rounded bg-gray-800/50 text-[10px]">
          <span :class="m.direction === 'rx' ? 'text-cyan-400' : 'text-gray-500'" class="font-mono shrink-0">{{ m.direction === 'rx' ? 'in' : 'out' }}</span>
          <span class="text-gray-200 truncate flex-1">{{ m.decoded_text }}</span>
          <span class="text-gray-600 font-mono shrink-0">{{ ago(m.created_at) }}</span>
        </div>
      </div>
      <div v-else class="text-[10px] text-gray-600 text-center py-2 mb-2">No texts yet. Send one below or from a handheld on group {{ gw.group }}.</div>

      <form class="flex gap-2" @submit.prevent="send">
        <input v-model="text" type="text" :disabled="!up" :placeholder="up ? `Text to group ${gw.destination}` : 'Radio not connected'"
          class="flex-1 min-w-0 px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-[11px] text-gray-200 focus:border-cyan-500 focus:outline-none" data-testid="tetra-widget-text" />
        <button type="submit" :disabled="!up || sending || !text.trim()"
          class="px-3 py-1 rounded text-[10px] font-medium bg-cyan-400/10 text-cyan-300 border border-cyan-400/20 hover:bg-cyan-400/20 transition-colors disabled:opacity-40 disabled:cursor-not-allowed">Send</button>
      </form>
      <div class="mt-2 flex items-center justify-between text-[9px] text-gray-600">
        <span>{{ sendMsg || `${model}, ${gw.radio?.firmware || ''}, ${gw.max_text_bytes - 4} characters per SDS` }}</span>
        <router-link to="/settings?tab=tetra" class="text-cyan-400/70 hover:text-cyan-400 shrink-0">settings</router-link>
      </div>
    </template>
    <div v-else class="text-[10px] text-gray-600 text-center py-4">Loading</div>
  </div>
</template>
