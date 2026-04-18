<script setup>
import { ref, onMounted, onUnmounted, nextTick } from 'vue'
import Keyboard from 'simple-keyboard'
import 'simple-keyboard/build/css/index.css'

// On-screen touch keyboard. [MESHSAT-582]
//
// Shows when a text input / textarea is focused AND the device has
// touchscreen capability AND the viewport width is below 1024 px
// (kiosk tablets, Pi Touch Display 2). A connected USB keyboard
// lifts the show threshold so field operators with both don't get
// a popup every time they tap a field.
//
// Mount at the App level so the keyboard is available on every
// route. Lazy singleton — we build the simple-keyboard instance on
// the first real focus to avoid blocking initial paint.

const visible = ref(false)
const kbEl = ref(null)
let kb = null
let target = null

function isEditable(el) {
  if (!el) return false
  const t = el.tagName
  return t === 'INPUT' || t === 'TEXTAREA'
}

function onFocusIn(ev) {
  if (!isEditable(ev.target)) return
  // Don't pop up when the user already has a hardware keyboard (best
  // heuristic: keyboardevent has been seen on this document recently).
  // sessionStorage flag set by onAnyKey() below.
  if (sessionStorage.getItem('meshsat.hwkb') === '1') return
  // Kiosk or touch viewport only.
  const isTouch = (navigator.maxTouchPoints || 0) > 0
  const narrow = window.innerWidth < 1024
  if (!(isTouch && narrow)) return

  target = ev.target
  visible.value = true
  nextTick(ensureKeyboard)
}

function onFocusOut() {
  // Small delay so tapping the keyboard itself doesn't drop focus.
  setTimeout(() => {
    if (document.activeElement && isEditable(document.activeElement)) return
    visible.value = false
    target = null
  }, 100)
}

function onAnyKey() {
  // Hardware keyboard detected — remember and hide.
  sessionStorage.setItem('meshsat.hwkb', '1')
  visible.value = false
}

function ensureKeyboard() {
  if (kb || !kbEl.value) return
  kb = new Keyboard(kbEl.value, {
    onChange: (input) => { if (target) { target.value = input; target.dispatchEvent(new Event('input', { bubbles: true })) } },
    onKeyPress: (btn) => {
      if (btn === '{enter}' && target) target.form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      if (btn === '{bksp}' && target) target.dispatchEvent(new KeyboardEvent('keydown', { key: 'Backspace' }))
    }
  })
}

onMounted(() => {
  document.addEventListener('focusin',  onFocusIn)
  document.addEventListener('focusout', onFocusOut)
  document.addEventListener('keydown',  onAnyKey, { once: true })
})
onUnmounted(() => {
  document.removeEventListener('focusin',  onFocusIn)
  document.removeEventListener('focusout', onFocusOut)
})
</script>

<template>
  <div v-show="visible"
    class="fixed inset-x-0 bottom-0 z-[60] bg-tactical-surface border-t border-tactical-border p-2">
    <div ref="kbEl" class="simple-keyboard"></div>
  </div>
</template>

<style>
/* simple-keyboard picks up its own CSS; the overrides below nudge
   contrast up to match the tactical palette. */
.simple-keyboard { background: transparent !important; }
.simple-keyboard .hg-button { min-height: 44px; }
</style>
