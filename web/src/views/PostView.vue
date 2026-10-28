<script setup>
// The Community Post's operator screen [MESHSAT-1733]: the posts waiting
// for approval, big buttons to send or refuse each, the recent ones, the
// three settings, the post's state (open, closed and when what it stored is
// purged, purge now) and the receipt printer as it last reported itself.
// Polls every 5 s; works under ?kiosk=1 (tap targets).
import { computed, onMounted, onUnmounted, ref } from 'vue'
import api from '@/api/client'

const config = ref(null)
const pending = ref([])
const recent = ref([])
const counts = ref({})
const error = ref('')
const busy = ref('')
const saving = ref(false)
const acting = ref('')
const notice = ref('')
let timer = null

const off = computed(() => config.value && !config.value.enabled)
const closed = computed(() => !!(config.value && config.value.closed))

// The printer line: off by configuration, on and heard, or on and silent.
const printerLine = computed(() => {
  const c = config.value
  if (!c) return ''
  if (!c.print) return 'Slips: off (MESHSAT_POST_PRINT).'
  const p = c.printer || {}
  const r = p.report || {}
  if (!p.heard) {
    return r.service
      ? `Slips: on, but the printer service was last heard ${ago(p.age_seconds)}; check meshsat-receipt-printer on the kit.`
      : 'Slips: on, but no printer service has reported to this Bridge; check meshsat-receipt-printer on the kit.'
  }
  const parts = [`Slips: on, printer service heard ${ago(p.age_seconds)}`]
  if (r.printer) parts.push(`printer ${r.printer}`)
  parts.push(`booth slips at #${r.count || 0}`)
  if (r.last_slip_at) parts.push(`last slip ${when(r.last_slip_at)}`)
  if (r.queued) parts.push(`${r.queued} waiting`)
  let line = parts.join(', ') + '.'
  if (r.last_error) line += ` Last error: ${r.last_error}`
  return line
})

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

// closePost, reopenPost and purgeNow are the three state actions. Closing
// and purging ask first: both are what the data notice promises the people
// who wrote here, and a purge cannot be undone.
async function closePost() {
  const days = config.value.purge_days || 30
  if (!confirm(`Close the post? The portal takes no new message; replies can still be picked up. Everything it stored is deleted ${days} days from now.`)) return
  await act('close', 'The post is closed.')
}

async function reopenPost() {
  await act('open', 'The post is open again; the scheduled purge is off.')
}

async function purgeNow() {
  if (!confirm('Delete every post, reply, board line and phone counter this post stored, now? This cannot be undone.')) return
  await act('purge', 'Purged.', { confirm: true })
}

async function act(action, done, body = null) {
  acting.value = action
  try {
    const res = await api.post(`/post/${action}`, body)
    const purged = res && res.purged
    notice.value = purged ? `${done} ${purged.messages} messages, ${purged.replies} replies, ${purged.board} board lines, ${purged.sessions} phones.` : done
    error.value = ''
    await load()
  } catch (e) {
    error.value = e.message || 'failed'
  } finally {
    acting.value = ''
  }
}

function when(t) {
  try { return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) } catch { return '' }
}

function day(t) {
  try { return new Date(t).toLocaleDateString([], { year: 'numeric', month: 'short', day: 'numeric' }) } catch { return '' }
}

function ago(seconds) {
  const s = Number(seconds) || 0
  if (s < 90) return `${s} s ago`
  if (s < 5400) return `${Math.round(s / 60)} min ago`
  return `${Math.round(s / 3600)} h ago`
}

onMounted(() => { load(); timer = setInterval(load, 5000) })
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div class="max-w-3xl mx-auto">
    <h1 class="text-lg font-semibold text-gray-200 mb-1">Community Post</h1>
    <p class="text-[12px] text-gray-500 mb-4">Messages people wrote on this kit's open WiFi. An approved one leaves over satellite as one text; a refused one never leaves.</p>

    <div v-if="error" class="bg-red-900/30 border border-red-700/50 text-red-200 text-[12px] rounded-lg p-3 mb-4">{{ error }}</div>
    <div v-if="notice" class="bg-gray-800/60 border border-gray-700/50 text-gray-200 text-[12px] rounded-lg p-3 mb-4 flex items-baseline justify-between gap-3">
      <span>{{ notice }}</span>
      <button class="text-gray-500 hover:text-gray-300 text-[11px]" @click="notice = ''">dismiss</button>
    </div>

    <div v-if="config && !off" class="space-y-4">
      <div v-if="closed" class="bg-amber-950/40 border border-amber-800/60 text-amber-100 text-[12px] rounded-lg p-3" data-testid="post-closed">
        This post is closed: the portal takes no new message or board line, replies can still be picked up.
        Everything it stored is deleted on <span class="font-semibold">{{ day(config.purge_at) }}</span><span v-if="config.purged_at"> (last purge {{ day(config.purged_at) }} {{ when(config.purged_at) }})</span>.
      </div>

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
        <h2 class="text-sm font-medium text-gray-300 mb-2">The post</h2>
        <p class="text-[12px] text-gray-400 mb-3" data-testid="post-state">
          <template v-if="closed">Closed since {{ day(config.closed_at) }} {{ when(config.closed_at) }}. Everything it stored is deleted on {{ day(config.purge_at) }} ({{ config.purge_days }} days after closing), or now with the button.</template>
          <template v-else>Open. Closing it stops new messages and board lines; what it stored is deleted {{ config.purge_days }} days later, or now with the button.</template>
        </p>
        <div class="grid grid-cols-2 gap-2">
          <button v-if="!closed" class="py-3 rounded-lg bg-gray-700 hover:bg-gray-600 text-gray-100 font-semibold disabled:opacity-50"
                  :disabled="acting !== ''" @click="closePost">Close the post</button>
          <button v-else class="py-3 rounded-lg bg-teal-700 hover:bg-teal-600 text-white font-semibold disabled:opacity-50"
                  :disabled="acting !== ''" @click="reopenPost">Reopen the post</button>
          <button class="py-3 rounded-lg bg-red-900/70 hover:bg-red-800 text-red-100 font-semibold disabled:opacity-50"
                  :disabled="acting !== ''" @click="purgeNow">Purge now</button>
        </div>
        <p class="text-[12px] text-gray-400 mt-3" data-testid="post-printer">{{ printerLine }}</p>
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
          <label class="block"><span class="text-gray-500">Operator's name (shown on the portal's About page and on the slips)</span>
            <input type="text" maxlength="80" v-model="config.operator" class="mt-1 w-full bg-gray-900 border border-gray-700 rounded px-2 py-2 text-gray-100"></label>
          <button class="py-2.5 px-4 rounded-lg bg-teal-700 hover:bg-teal-600 text-white font-semibold disabled:opacity-50" :disabled="saving" @click="saveSettings">Save</button>
          <div class="text-gray-500">Portal: <span class="font-mono">{{ config.portal_url }}</span> &middot; listener <span class="font-mono">{{ config.listen }}</span> &middot; bearer <span class="font-mono">{{ config.bearer }}</span> &middot; {{ config.max_chars }} characters, {{ config.max_bytes }} bytes, board {{ config.board_days }} days, purge {{ config.purge_days }} days after closing</div>
        </div>
      </section>
    </div>
  </div>
</template>
