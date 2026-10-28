<script setup>
import { ref, onMounted, onUnmounted } from 'vue'

// One photo of an LXMF message (FIELD_IMAGE, webp from every client). The
// browser fetches `src` itself; when the parent gives `load` instead (an
// API behind a token), the bytes are fetched on mount and shown through an
// object URL. A tap toggles between a thumbnail and the full width.
// [MESHSAT-1735]
const props = defineProps({
  format: { type: String, default: 'webp' },
  size: { type: Number, default: 0 },
  src: { type: String, default: '' },
  // () => Promise<Blob|Uint8Array>
  load: { type: Function, default: null }
})

const url = ref(props.src)
const big = ref(false)
const failed = ref(false)
let objectURL = ''

const sizeLabel = props.size >= 1024 ? `${(props.size / 1024).toFixed(1)} KiB` : `${props.size} B`

onMounted(async () => {
  if (url.value || !props.load) return
  try {
    const data = await props.load()
    const blob = data instanceof Blob ? data : new Blob([data], { type: `image/${props.format}` })
    objectURL = URL.createObjectURL(blob)
    url.value = objectURL
  } catch {
    failed.value = true
  }
})

onUnmounted(() => { if (objectURL) URL.revokeObjectURL(objectURL) })
</script>

<template>
  <figure class="mt-1.5" data-role="photo-note">
    <img v-if="url && !failed" :src="url" :alt="`photo, ${format}, ${sizeLabel}`" loading="lazy" decoding="async"
      class="rounded border border-gray-700/60 cursor-zoom-in" :class="big ? 'max-w-full cursor-zoom-out' : 'max-h-48 max-w-full'"
      @click="big = !big" @error="failed = true" />
    <figcaption class="text-[10px] text-gray-600 mt-0.5">
      <span v-if="failed" class="text-red-400">photo could not be loaded</span>
      <span v-else>{{ format }}, {{ sizeLabel }}</span>
    </figcaption>
  </figure>
</template>
