<!--
  ApiLogin.vue
  ------------
  The one screen a kit with an API token shows a browser that has none:
  a token field over the page. Mounted once in App.vue; opens on the
  `meshsat:unauthorized` event the API client fires on a 401, hands the
  token to POST /api/auth/login, keeps it in localStorage on a 204 and
  reloads so every poll and event stream starts over with it. A kit with
  no token never answers 401 and never shows this; neither does the
  kiosk on the kit itself, which the Bridge trusts by address.
  [MESHSAT-1738]
-->
<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import api from '@/api/client'

const open = ref(false)
const token = ref('')
const busy = ref(false)
const refused = ref(false)
const failed = ref('')

function show() {
  open.value = true
  refused.value = false
}

async function submit() {
  if (busy.value || !token.value.trim()) return
  busy.value = true
  refused.value = false
  failed.value = ''
  try {
    const ok = await api.login(token.value)
    if (!ok) {
      refused.value = true
      return
    }
    open.value = false
    try { window.location.reload() } catch { /* jsdom */ }
  } catch (e) {
    failed.value = e?.message || 'no answer from the kit'
  } finally {
    busy.value = false
  }
}

onMounted(() => window.addEventListener('meshsat:unauthorized', show))
onUnmounted(() => window.removeEventListener('meshsat:unauthorized', show))
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center bg-gray-950/90 p-4" role="dialog" aria-modal="true" aria-labelledby="api-login-title" data-testid="api-login">
      <form class="w-full max-w-sm rounded border border-gray-700 bg-gray-900 p-5 space-y-4" @submit.prevent="submit">
        <div>
          <h2 id="api-login-title" class="text-base font-semibold text-gray-100">This kit asks for its API token</h2>
          <p class="mt-1 text-sm text-gray-400">
            The operator set a token on this Bridge. Paste it here; it stays in this browser.
          </p>
        </div>
        <label class="block">
          <span class="text-xs uppercase tracking-wide text-gray-500">Token</span>
          <input v-model="token" type="password" autocomplete="current-password" autofocus spellcheck="false"
                 class="mt-1 w-full px-3 py-2 rounded bg-gray-950 border border-gray-700 text-sm text-gray-200 font-mono"
                 data-testid="api-login-token" />
        </label>
        <p v-if="refused" class="text-sm text-red-400" data-testid="api-login-refused">That token was refused.</p>
        <p v-else-if="failed" class="text-sm text-amber-400">{{ failed }}</p>
        <button type="submit" :disabled="busy || !token.trim()"
                class="w-full px-3 py-2 rounded bg-gray-100 text-gray-900 text-sm font-semibold disabled:opacity-50"
                data-testid="api-login-submit">
          {{ busy ? 'Checking' : 'Unlock' }}
        </button>
      </form>
    </div>
  </Teleport>
</template>
