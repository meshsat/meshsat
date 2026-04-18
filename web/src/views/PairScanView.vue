<script setup>
import { ref, computed } from 'vue'
import api from '@/api/client'

// Browser-as-remote-control pair/scan wizard. [MESHSAT-605]
//
// Runs on the remote device (laptop / phone browser) pointed at the
// bridge it wants to pair with. Wizard steps:
//   1. Operator enters the 6-digit PIN shown on the bridge touch
//      display.
//   2. Operator pastes / types the 32-byte pairing key (shown on
//      the same display; too long to type comfortably, but manual
//      entry is the only path in pre-QR flows — once MESHSAT-600
//      lands the Android camera + a web camera scanner will skip
//      this step).
//   3. Wizard generates an Ed25519 keypair client-side (WebCrypto),
//      HKDFs the shared secret, HMACs the pubkey, posts to
//      /api/v2/pair/claim, and on success stores the client_id +
//      keypair in localStorage under meshsat.pair.
//
// Keypair stays in localStorage (can be upgraded to IndexedDB +
// non-extractable CryptoKey in a follow-up). For now we treat
// paired browsers as lower-trust than Android + CLI.

const step = ref(1)
const pin = ref('')
const pairingKey = ref('')
const name = ref('')
const error = ref('')
const busy = ref(false)
const result = ref(null)

const canAdvance = computed(() => {
  if (step.value === 1) return /^\d{6}$/.test(pin.value)
  if (step.value === 2) return /^[0-9a-fA-F]{64}$/.test(pairingKey.value.trim())
  return false
})

// --- WebCrypto helpers --------------------------------------------------

async function generateEd25519() {
  // Ed25519 in WebCrypto is rolling out; fall back to a JS
  // implementation via noble if unsupported. For the MVP assume
  // modern browser support (Chromium >= 120, Firefox >= 129).
  const kp = await crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify'])
  const pubRaw = new Uint8Array(await crypto.subtle.exportKey('raw', kp.publicKey))
  // Raw export of an Ed25519 private key isn't universally supported
  // (Firefox only does PKCS8). Store jwk/pkcs8 under the hood.
  const privPKCS8 = new Uint8Array(await crypto.subtle.exportKey('pkcs8', kp.privateKey))
  return { pubRaw, privPKCS8 }
}

function toHex(u8) { return Array.from(u8).map(b => b.toString(16).padStart(2, '0')).join('') }

async function hkdf(pairingKeyHex, pinStr) {
  const pk = hexToBytes(pairingKeyHex)
  const info = new TextEncoder().encode('meshsat-pair-v1')
  const salt = new TextEncoder().encode(pinStr)
  const key = await crypto.subtle.importKey('raw', pk, { name: 'HKDF' }, false, ['deriveBits'])
  const bits = await crypto.subtle.deriveBits({ name: 'HKDF', hash: 'SHA-256', salt, info }, key, 256)
  return new Uint8Array(bits)
}

async function hmacSha256(key, data) {
  const k = await crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign'])
  const sig = await crypto.subtle.sign('HMAC', k, data)
  return new Uint8Array(sig)
}

function hexToBytes(h) {
  const out = new Uint8Array(h.length / 2)
  for (let i = 0; i < out.length; i++) out[i] = parseInt(h.substr(i*2, 2), 16)
  return out
}

// --- Claim flow ---------------------------------------------------------

async function doClaim() {
  busy.value = true
  error.value = ''
  try {
    const kp = await generateEd25519()
    const secret = await hkdf(pairingKey.value.trim(), pin.value)
    const mac = await hmacSha256(secret, kp.pubRaw)
    const req = {
      pin: pin.value,
      public_key: toHex(kp.pubRaw),
      hmac: toHex(mac),
      name: name.value || 'Browser',
      kind: 'browser'
    }
    const resp = await api.post('/v2/pair/claim', req)
    // Persist keypair + client_id in localStorage so the SPA can
    // mint JWTs locally next boot. (Follow-up: move to IndexedDB +
    // non-extractable keys per-device.)
    localStorage.setItem('meshsat.pair', JSON.stringify({
      client_id:   resp.client_id,
      public_key:  toHex(kp.pubRaw),
      private_pkcs8: toHex(kp.privPKCS8),
      claimed_at:  new Date().toISOString()
    }))
    result.value = resp
    step.value = 3
  } catch (e) {
    error.value = e?.message || 'Claim failed'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="max-w-md mx-auto space-y-4">
    <h1 class="text-lg font-display font-semibold text-gray-200 tracking-wide">Pair this device</h1>

    <!-- Step 1 -->
    <div v-if="step === 1" class="space-y-3 bg-tactical-surface border border-tactical-border rounded p-4">
      <p class="text-xs text-gray-400">
        Enter the 6-digit PIN shown on the bridge's touch display (Settings → Devices → Arm pair mode).
      </p>
      <input v-model="pin" type="text" inputmode="numeric" maxlength="6"
        placeholder="••••••"
        class="w-full px-3 py-3 rounded bg-tactical-bg border border-tactical-border text-center text-3xl font-mono tracking-widest" />
      <div class="flex justify-end">
        <button @click="step = 2" :disabled="!canAdvance"
          class="px-4 py-2 rounded bg-tactical-iridium text-tactical-bg font-semibold text-sm disabled:opacity-40 min-h-[40px]">
          Next
        </button>
      </div>
    </div>

    <!-- Step 2 -->
    <div v-if="step === 2" class="space-y-3 bg-tactical-surface border border-tactical-border rounded p-4">
      <p class="text-xs text-gray-400">
        Paste the 64-character pairing key shown on the same display.
      </p>
      <textarea v-model="pairingKey" rows="3"
        placeholder="0123456789abcdef…"
        class="w-full px-3 py-2 rounded bg-tactical-bg border border-tactical-border text-xs font-mono resize-none"></textarea>
      <label class="block text-[10px] text-gray-500">Name this device (optional)</label>
      <input v-model="name" type="text" placeholder="Operator Chromium"
        class="w-full px-3 py-2 rounded bg-tactical-bg border border-tactical-border text-sm min-h-[40px]" />
      <div v-if="error" class="text-xs text-red-400">{{ error }}</div>
      <div class="flex justify-between">
        <button @click="step = 1"
          class="px-3 py-2 rounded border border-tactical-border text-gray-400 text-xs min-h-[40px]">
          Back
        </button>
        <button @click="doClaim" :disabled="!canAdvance || busy"
          class="px-4 py-2 rounded bg-tactical-iridium text-tactical-bg font-semibold text-sm disabled:opacity-40 min-h-[40px]">
          <span v-if="busy">Claiming…</span><span v-else>Claim identity</span>
        </button>
      </div>
    </div>

    <!-- Step 3 -->
    <div v-if="step === 3" class="space-y-3 bg-tactical-surface border border-emerald-500/50 rounded p-4">
      <h2 class="text-sm font-semibold text-emerald-400">Paired</h2>
      <div class="text-[10px] text-gray-500">Client ID</div>
      <div class="text-xs font-mono break-all text-gray-200">{{ result?.client_id }}</div>
      <div class="text-[10px] text-gray-500">Keypair stored in this browser's localStorage. Back up or clear it from Settings → Devices.</div>
      <router-link to="/" class="inline-block px-4 py-2 rounded bg-tactical-iridium text-tactical-bg font-semibold text-sm min-h-[40px]">
        Open MeshSat
      </router-link>
    </div>
  </div>
</template>
