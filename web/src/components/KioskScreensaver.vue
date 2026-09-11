<script setup>
// Kiosk screensaver: after SAVER_MS without a touch the panel shows the
// MeshSat poster full screen; any touch brings the page back, and so
// does a `meshsat:wake` event, which the TTC screen fires when a live
// message lands so visitors see the relay instead of the poster.
// Mounted only in the kiosk shell (html.shell-kiosk). `?saverMs=N`
// overrides the delay, `?saverMs=0` disables it (sticky for the tab,
// like the other kiosk flags). Owner ruling 11 Sep 2026: 30 s, the
// bears poster. [MESHSAT-826]
import { ref, onMounted, onUnmounted } from 'vue'

const DEFAULT_MS = 30_000
let saverMs = DEFAULT_MS
try {
  const q = new URLSearchParams(window.location.search || '').get('saverMs')
  if (q !== null && q !== '') {
    saverMs = Math.max(0, Number(q) || 0)
    try { sessionStorage.setItem('meshsat.saverMs', String(saverMs)) } catch {}
  } else {
    const s = sessionStorage.getItem('meshsat.saverMs')
    if (s !== null) saverMs = Math.max(0, Number(s) || 0)
  }
} catch {}

const shown = ref(false)
let timer = 0

function arm() {
  if (timer) clearTimeout(timer)
  timer = 0
  if (saverMs > 0) timer = setTimeout(() => { shown.value = true }, saverMs)
}
function wake() {
  shown.value = false
  arm()
}
// Activity on the page only re-arms the timer. While the poster is up
// the poster's own handlers take the touch, so the tap that wakes the
// panel never reaches a route row or a button underneath: the poster
// stays mounted through pointerdown and disappears on pointerup, so
// the browser resolves the whole gesture against the poster.
function activity() {
  if (!shown.value) arm()
}
function swallow(ev) {
  ev.preventDefault()
  ev.stopPropagation()
}
function dismiss(ev) {
  swallow(ev)
  wake()
}

const ACTIVITY = ['pointerdown', 'keydown', 'wheel', 'touchstart']
onMounted(() => {
  for (const t of ACTIVITY) window.addEventListener(t, activity, { capture: true, passive: true })
  window.addEventListener('meshsat:wake', wake)
  arm()
})
onUnmounted(() => {
  for (const t of ACTIVITY) window.removeEventListener(t, activity, { capture: true })
  window.removeEventListener('meshsat:wake', wake)
  if (timer) clearTimeout(timer)
})
</script>

<template>
  <Transition name="saver">
    <div v-if="shown" class="saver fixed inset-0 z-[200] bg-black select-none" role="presentation"
         @pointerdown="swallow" @touchstart="swallow" @pointerup="dismiss" @click="dismiss" @keydown="dismiss">
      <img src="/screensaver-bears.png" alt="" class="w-full h-full object-contain" draggable="false" />
    </div>
  </Transition>
</template>

<style scoped>
.saver { touch-action: none; cursor: none; }
.saver-enter-active { transition: opacity 600ms ease; }
.saver-leave-active { transition: opacity 150ms ease; }
.saver-enter-from, .saver-leave-to { opacity: 0; }
</style>
