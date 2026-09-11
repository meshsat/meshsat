<script setup>
// Kiosk poster interlude. An untouched panel keeps its live page (on the
// TTC screen the route drawing and its attract replay) and shows the
// MeshSat poster as an eye-catcher: after IDLE_MS without a touch the
// poster comes up for SHOW_MS, goes away by itself, and returns every
// EVERY_MS while nobody touches the panel. Any touch or a `meshsat:wake`
// event (the TTC screen fires it when a relayed message lands) drops the
// poster at once and restarts the idle clock. Mounted only in the kiosk
// shell (html.shell-kiosk). Owner rulings 11 Sep 2026: the bears poster;
// interlude rather than a lid, so passers-by see the live relay.
// URL overrides, sticky for the tab: ?saverMs=<idle> (0 disables),
// ?saverShowMs=<poster duration>, ?saverEveryMs=<repeat period>.
// [MESHSAT-826]
import { ref, onMounted, onUnmounted } from 'vue'

function setting(key, def) {
  let v = def
  try {
    const q = new URLSearchParams(window.location.search || '').get(key)
    if (q !== null && q !== '') {
      v = Math.max(0, Number(q) || 0)
      try { sessionStorage.setItem('meshsat.' + key, String(v)) } catch {}
    } else {
      const s = sessionStorage.getItem('meshsat.' + key)
      if (s !== null) v = Math.max(0, Number(s) || 0)
    }
  } catch {}
  return v
}
const IDLE_MS = setting('saverMs', 180_000)
const SHOW_MS = setting('saverShowMs', 20_000)
const EVERY_MS = setting('saverEveryMs', 240_000)

const shown = ref(false)
let timer = 0

function schedule(ms, fn) {
  if (timer) clearTimeout(timer)
  timer = 0
  if (ms > 0) timer = setTimeout(fn, ms)
}
function show() {
  shown.value = true
  schedule(SHOW_MS, hide)
}
function hide() {
  shown.value = false
  // Next appearance one period after this one started.
  schedule(Math.max(1000, EVERY_MS - SHOW_MS), show)
}
// Idle clock from zero: touch, key, wheel, or a live message.
function wake() {
  shown.value = false
  if (IDLE_MS > 0) schedule(IDLE_MS, show)
  else schedule(0, null)
}
// Activity on the page only restarts the idle clock. While the poster is
// up the poster's own handlers take the touch, so the tap that wakes the
// panel never reaches a route row or a button underneath: the poster
// stays mounted through pointerdown and disappears on pointerup, so the
// browser resolves the whole gesture against the poster.
function activity() {
  if (!shown.value) wake()
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
  wake()
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
.saver-leave-active { transition: opacity 400ms ease; }
.saver-enter-from, .saver-leave-to { opacity: 0; }
</style>
