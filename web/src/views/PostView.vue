<script setup>
// The Community Post's operator screen [MESHSAT-1733]: the posts waiting
// for approval, big buttons to send or refuse each, the recent ones, and
// the three settings. Polls every 5 s; works under ?kiosk=1 (tap targets).
import { computed, onMounted, onUnmounted, ref } from 'vue'
import api from '@/api/client'

const config = ref(null)
const pending = ref([])
const recent = ref([])
const counts = ref({})
const error = ref('')
const busy = ref('')
const saving = ref(false)
let timer = null

const off = computed(() => config.value && !config.value.enabled)

async function load() {
  try {
    config.value = await api.get('/post/config')
    const q = await api.get('/post/queue')
    pending.value = q.pending || []
    recent.value = (q.recent || []).filter(p => p.status !== 'pending_approval')
    counts.value = q.counts || {}
    error.value = ''
  } catch (e) {
    error.value = e.status === 503 ? 'The Community Post is not configured on this Bridge (MESHSAT_POST_LISTEN).' : (e.message || 'failed')
  }
}

async function decide(code, action) {
  busy.value = code
  try {
    await api.post(`/post/${code}/${action}`)
    await load()
  } catch (e) {
    error.value = e.message || 'failed'
  } finally {
    busy.value = ''
  }
}

async function saveSettings() {
  saving.value = true
  try {
    config.value = await api.put('/post/config', {
      auto_approve: !!config.value.auto_approve,
      posts_per_hour: Number(config.value.posts_per_hour) || 0,
      posts_per_day: Number(config.value.posts_per_day) || 0,
      operator: config.value.operator || ''
    })
    error.value = ''
  } catch (e) {
    error.value = e.message || 'failed'
  } finally {
    saving.value = false
  }
}

function when(t) {
  try { return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) } catch { return '' }
}

onMounted(() => { load(); timer = setInterval(load, 5000) })
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div class="max-w-3xl mx-auto">
    <h1 class="text-lg font-semibold text-gray-200 mb-1">Community Post</h1>
    <p class="text-[12px] text-gray-500 mb-4">Messages people wrote on this kit's open WiFi. An approved one leaves over satellite as one text; a refused one never leaves.</p>

    <div v-if="error" class="bg-red-900/30 border border-red-700/50 text-red-200 text-[12px] rounded-lg p-3 mb-4">{{ error }}</div>

    <div v-if="config && !off" class="space-y-4">
      <section class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Waiting for you <span class="text-gray-500 font-normal">({{ pending.length }})</span></h2>
        <p v-if="pending.length === 0" class="text-[12px] text-gray-500">Nothing waiting.</p>
        <div v-for="p in pending" :key="p.code" class="border border-gray-700/60 rounded-lg p-3 mb-2"
             :class="p.class === 'emergency' ? 'bg-red-950/40 border-red-800/60' : 'bg-gray-900/40'">
          <div class="flex items-baseline justify-between gap-2 text-[12px] text-gray-400">
            <span class="font-mono text-gray-200 text-sm">{{ p.code }}</span>
            <span>{{ when(p.created_at) }}</span>
          </div>
          <div class="text-[12px] text-gray-400 mt-1">To <span class="font-mono text-gray-200">{{ p.recipient }}</span><span v-if="p.name"> &middot; from {{ p.name }}</span>
            <span v-if="p.class === 'emergency'" class="ml-2 px-1.5 py-0.5 rounded bg-red-800 text-red-100 font-semibold">EMERGENCY</span></div>
          <div class="text-sm text-gray-100 mt-2 break-words">{{ p.text }}</div>
          <div class="grid grid-cols-2 gap-2 mt-3">
            <button class="py-3 rounded-lg bg-emerald-700 hover:bg-emerald-600 text-white font-semibold disabled:opacity-50"
                    :disabled="busy === p.code" @click="decide(p.code, 'approve')">Send</button>
            <button class="py-3 rounded-lg bg-gray-700 hover:bg-gray-600 text-gray-100 font-semibold disabled:opacity-50"
                    :disabled="busy === p.code" @click="decide(p.code, 'refuse')">Refuse</button>
          </div>
        </div>
      </section>

      <section class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Recent</h2>
        <div class="text-[11px] text-gray-500 mb-2">
          <span v-for="(n, k) in counts" :key="k" class="mr-3">{{ k }}: {{ n }}</span>
        </div>
        <p v-if="recent.length === 0" class="text-[12px] text-gray-500">No posts yet.</p>
        <div v-for="p in recent" :key="p.code" class="flex items-baseline justify-between gap-2 text-[12px] py-1 border-b border-gray-800/60 last:border-0">
          <span class="font-mono text-gray-300">{{ p.code }}</span>
          <span class="text-gray-400 truncate flex-1">{{ p.recipient }} &middot; {{ p.text }}</span>
          <span class="text-gray-500 whitespace-nowrap">{{ p.status.replace('_', ' ') }}</span>
        </div>
      </section>

      <section class="bg-gray-800/40 rounded-lg border border-gray-700/50 p-4">
        <h2 class="text-sm font-medium text-gray-300 mb-2">Settings</h2>
        <div class="space-y-3 text-[12px]">
          <label class="flex items-center gap-3">
            <input type="checkbox" v-model="config.auto_approve" class="w-5 h-5">
            <span class="text-gray-300">Send every post at once (no approval queue)</span>
          </label>
          <div class="grid grid-cols-2 gap-3">
            <label class="block"><span class="text-gray-500">Posts per phone per hour (0 = no limit)</span>
              <input type="number" min="0" v-model="config.posts_per_hour" class="mt-1 w-full bg-gray-900 border border-gray-700 rounded px-2 py-2 text-gray-100"></label>
            <label class="block"><span class="text-gray-500">Posts per phone per day (0 = no limit)</span>
              <input type="number" min="0" v-model="config.posts_per_day" class="mt-1 w-full bg-gray-900 border border-gray-700 rounded px-2 py-2 text-gray-100"></label>
          </div>
          <label class="block"><span class="text-gray-500">Operator's name (shown on the portal's About page)</span>
            <input type="text" maxlength="80" v-model="config.operator" class="mt-1 w-full bg-gray-900 border border-gray-700 rounded px-2 py-2 text-gray-100"></label>
          <button class="py-2.5 px-4 rounded-lg bg-teal-700 hover:bg-teal-600 text-white font-semibold disabled:opacity-50" :disabled="saving" @click="saveSettings">Save</button>
          <div class="text-gray-500">Portal: <span class="font-mono">{{ config.portal_url }}</span> &middot; listener <span class="font-mono">{{ config.listen }}</span> &middot; bearer <span class="font-mono">{{ config.bearer }}</span> &middot; {{ config.max_chars }} characters, {{ config.max_bytes }} bytes, board {{ config.board_days }} days</div>
        </div>
      </section>
    </div>
  </div>
</template>
