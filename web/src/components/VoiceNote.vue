<script setup>
import { ref, computed, onUnmounted } from 'vue'
import { SAMPLE_RATE, audioModeLabel, codec2Duration, decodeCodec2, formatClock, isCodec2, isOpus, loadCodec2 } from '@/utils/codec2'

// One voice note of an LXMF message. A Codec2 note (modes 0x03-0x09) is
// decoded by the vendored wasm the first time it is played and goes out
// through WebAudio as 8 kHz PCM; an Opus note (0x10-0x19) is the Ogg
// stream it is and plays in <audio>; 450 and 450PWB and anything else say
// so. The bytes arrive as `bytes`, or through `load` on the first play, so
// a page of notes downloads nothing until one is played; `size` gives the
// duration label before that, from the frame geometry. [MESHSAT-1735]
const props = defineProps({
  mode: { type: Number, required: true },
  size: { type: Number, default: 0 },
  bytes: { type: Uint8Array, default: null },
  // () => Promise<Uint8Array>: the note's bytes, fetched when first needed.
  load: { type: Function, default: null },
  // An Opus note's URL when the parent has one the browser may fetch itself.
  src: { type: String, default: '' }
})

const kind = computed(() => (isCodec2(props.mode) ? 'codec2' : isOpus(props.mode) ? 'opus' : 'unsupported'))
const label = computed(() => audioModeLabel(props.mode))
const sizeLabel = computed(() => (props.size >= 1024 ? `${(props.size / 1024).toFixed(1)} KiB` : `${props.size} B`))

// idle, loading, playing, paused, error
const status = ref('idle')
const error = ref('')
const position = ref(0)
const decoded = ref(0) // seconds, once decoded
const duration = computed(() => decoded.value || codec2Duration(props.mode, props.bytes ? props.bytes.length : props.size))
const clock = computed(() => `${status.value === 'idle' ? '' : formatClock(position.value) + ' / '}${formatClock(duration.value)}`)
const pct = computed(() => (duration.value ? Math.min(100, (position.value / duration.value) * 100) : 0))

// One AudioContext for every note on the page, made on the first play (a
// user gesture, so autoplay policy lets it run).
let shared = null
function audioContext() {
  if (!shared) shared = new (window.AudioContext || window.webkitAudioContext)()
  return shared
}

let buffer = null
let source = null
let startedAt = 0
let offset = 0
let ticker = null
let stopping = false

async function ensureBuffer() {
  if (buffer) return buffer
  const bytes = props.bytes || (props.load ? await props.load() : null)
  if (!bytes || !bytes.length) throw new Error('empty note')
  const m = await loadCodec2()
  const out = decodeCodec2(m, bytes, props.mode)
  if (!out.pcm.length) throw new Error('shorter than one frame')
  const ctx = audioContext()
  const buf = ctx.createBuffer(1, out.pcm.length, SAMPLE_RATE)
  buf.copyToChannel(out.pcm, 0)
  decoded.value = out.pcm.length / SAMPLE_RATE
  buffer = buf
  return buf
}

function tick() {
  clearInterval(ticker)
  ticker = setInterval(() => {
    if (status.value !== 'playing') return
    position.value = Math.min(duration.value, audioContext().currentTime - startedAt)
  }, 100)
}

function stopTicker() {
  clearInterval(ticker)
  ticker = null
}

async function play() {
  if (status.value === 'loading') return
  if (!buffer) {
    status.value = 'loading'
    error.value = ''
    try {
      await ensureBuffer()
    } catch (e) {
      status.value = 'error'
      error.value = e?.message || 'could not decode'
      return
    }
  }
  const ctx = audioContext()
  try { await ctx.resume() } catch { /* an engine that cannot resume still plays */ }
  const from = offset >= buffer.duration ? 0 : offset
  const node = ctx.createBufferSource()
  node.buffer = buffer
  node.connect(ctx.destination)
  node.onended = () => {
    if (node !== source) return
    source = null
    stopTicker()
    if (stopping) { stopping = false; return }
    offset = 0
    position.value = 0
    status.value = 'idle'
  }
  source = node
  node.start(0, from)
  startedAt = ctx.currentTime - from
  position.value = from
  status.value = 'playing'
  tick()
}

function pause() {
  if (!source) return
  stopping = true
  offset = Math.min(buffer.duration, audioContext().currentTime - startedAt)
  position.value = offset
  status.value = 'paused'
  source.stop()
}

function toggle() {
  if (status.value === 'playing') pause()
  else play()
}

// Opus: the browser's own player; the bytes are fetched on the first tap
// when the parent gave no URL.
const opusSrc = ref(props.src)
const audioEl = ref(null)
let objectURL = ''
async function loadOpus() {
  if (opusSrc.value || !props.load) return
  status.value = 'loading'
  error.value = ''
  try {
    const bytes = await props.load()
    objectURL = URL.createObjectURL(new Blob([bytes], { type: 'audio/ogg' }))
    opusSrc.value = objectURL
    status.value = 'idle'
    requestAnimationFrame(() => audioEl.value?.play().catch(() => {}))
  } catch (e) {
    status.value = 'error'
    error.value = e?.message || 'could not load'
  }
}

onUnmounted(() => {
  stopping = true
  try { source?.stop() } catch { /* already ended */ }
  stopTicker()
  if (objectURL) URL.revokeObjectURL(objectURL)
})
</script>

<template>
  <div class="flex items-center gap-2 mt-1.5 text-[11px] min-w-0" data-role="voice-note" :title="`${label}, ${sizeLabel}`">
    <template v-if="kind === 'codec2'">
      <button type="button" @click="toggle" :disabled="status === 'loading'"
        class="flex-shrink-0 w-7 h-7 rounded-full border border-gray-600 bg-gray-800 hover:bg-gray-700 text-gray-200 flex items-center justify-center disabled:opacity-50 transition-colors"
        :aria-label="status === 'playing' ? 'Pause' : 'Play'" data-role="voice-toggle">
        <svg v-if="status === 'loading'" class="w-3.5 h-3.5 animate-spin" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" d="M12 3a9 9 0 1 0 9 9" /></svg>
        <svg v-else-if="status === 'playing'" class="w-3 h-3" viewBox="0 0 24 24" fill="currentColor"><rect x="6" y="5" width="4" height="14" rx="1" /><rect x="14" y="5" width="4" height="14" rx="1" /></svg>
        <svg v-else class="w-3 h-3 ml-0.5" viewBox="0 0 24 24" fill="currentColor"><path d="M7 5.5v13a1 1 0 0 0 1.5.86l11-6.5a1 1 0 0 0 0-1.72l-11-6.5A1 1 0 0 0 7 5.5z" /></svg>
      </button>
      <div class="flex-1 min-w-[72px] h-1 rounded bg-gray-700 overflow-hidden" role="progressbar" :aria-valuenow="Math.round(pct)" aria-valuemin="0" aria-valuemax="100">
        <div class="h-full bg-gray-300 transition-[width] duration-100" :style="{ width: pct + '%' }"></div>
      </div>
      <span class="font-mono tabular-nums text-gray-400 flex-shrink-0" data-role="voice-clock">{{ clock }}</span>
      <span class="text-gray-600 flex-shrink-0">{{ label }}</span>
      <span v-if="status === 'error'" class="text-red-400 truncate" data-role="voice-error">{{ error }}</span>
    </template>

    <template v-else-if="kind === 'opus'">
      <audio v-if="opusSrc" ref="audioEl" controls preload="metadata" :src="opusSrc" class="h-8 max-w-full min-w-0"></audio>
      <button v-else type="button" @click="loadOpus" :disabled="status === 'loading'"
        class="px-2 py-1 rounded border border-gray-600 bg-gray-800 hover:bg-gray-700 text-gray-200 disabled:opacity-50 transition-colors">
        {{ status === 'loading' ? 'Loading' : 'Play' }}
      </button>
      <span class="text-gray-600 flex-shrink-0">{{ label }}, {{ sizeLabel }}</span>
      <span v-if="status === 'error'" class="text-red-400 truncate" data-role="voice-error">{{ error }}</span>
    </template>

    <span v-else class="text-gray-500" data-role="voice-unsupported">{{ label }}, {{ sizeLabel }}: not supported here</span>
  </div>
</template>
