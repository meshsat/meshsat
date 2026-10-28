<script>
import api from '@/api/client'

// Module scope, shared by every message on the page: the list is read once
// per message id, and at most four reads are in flight so a long inbox does
// not flood the kit. A failed read is forgotten so the next mount retries.
const lists = new Map() // message id -> Promise<[attachments]>
let inFlight = 0
const waiting = []
function slot() {
  if (inFlight < 4) { inFlight++; return Promise.resolve() }
  return new Promise(resolve => waiting.push(resolve))
}
function release() {
  const next = waiting.shift()
  if (next) next()
  else inFlight--
}

function listFor(id) {
  const key = String(id)
  if (!lists.has(key)) {
    const p = slot()
      .then(() => api.get(`/lxmf/messages/${encodeURIComponent(key)}/attachments`))
      .then(r => r?.attachments || [])
      .finally(release)
      .catch(err => { lists.delete(key); throw err })
    lists.set(key, p)
  }
  return lists.get(key)
}

async function bytesOf(att) {
  const res = await fetch(att.url)
  if (!res.ok) throw new Error(`HTTP ${res.status}`)
  return new Uint8Array(await res.arrayBuffer())
}
</script>

<script setup>
import { ref, watch } from 'vue'
import VoiceNote from '@/components/VoiceNote.vue'
import PhotoNote from '@/components/PhotoNote.vue'

// The voice note and photo an LXMF message carries, under its text: the
// list from GET /api/lxmf/messages/{id}/attachments (cached above), a photo
// fetched by the browser from its URL, a Codec2 note's bytes only when it
// is played. A message with nothing attached renders nothing. [MESHSAT-1735]
const props = defineProps({
  messageId: { type: [String, Number], required: true }
})

const items = ref([])
watch(() => props.messageId, async id => {
  items.value = []
  try {
    const list = await listFor(id)
    if (String(props.messageId) === String(id)) items.value = list
  } catch {
    // Nothing is shown; the next mount reads again.
  }
}, { immediate: true })
</script>

<template>
  <div v-if="items.length" class="flex flex-col gap-1" data-role="lxmf-attachments">
    <template v-for="att in items" :key="att.index">
      <PhotoNote v-if="att.kind === 'image'" :format="att.format" :size="att.size" :src="att.url" />
      <VoiceNote v-else-if="att.kind === 'audio'" :mode="att.mode" :size="att.size" :src="att.mode >= 0x10 && att.mode <= 0x19 ? att.url : ''" :load="() => bytesOf(att)" />
    </template>
  </div>
</template>
