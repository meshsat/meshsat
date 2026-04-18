<script setup>
import { ref, shallowRef, onMounted, onUnmounted, nextTick } from 'vue'
import Keyboard from 'simple-keyboard'
import 'simple-keyboard/build/css/index.css'

// In-SPA on-screen keyboard for kiosk Chromium under labwc/wlroots,
// where the wayland virtual-keyboard protocol path is fragile
// (labwc issue #2926 / wlroots input-method crashes on reshow).
// Shipping the OSK inside the SPA sidesteps the compositor entirely.
// [MESHSAT-582]

const keyboardEl = ref(null)
const visible = ref(false)
const layoutName = ref('default')
const currentMode = ref('text') // 'text' | 'numeric'

let kb = null
let target = null
// Once any physical keyboard event arrives, we assume a USB keyboard
// is plugged in and suppress the OSK for the rest of the session.
// simple-keyboard synthesises DOM input via .value mutation, so real
// physical keydowns are the only source of keydown events on inputs.
let hasHardwareKeyboard = false
const coarsePointer = typeof window !== 'undefined' && window.matchMedia
  ? window.matchMedia('(pointer: coarse)').matches
  : false

const NUMERIC_MODES = new Set(['numeric', 'decimal', 'tel'])

function isEditable(el) {
  if (!el) return false
  if (el.isContentEditable) return true
  const tag = el.tagName
  if (tag === 'TEXTAREA') return true
  if (tag !== 'INPUT') return false
  // Skip buttons, checkboxes, files, etc. — only text-like inputs.
  const type = (el.getAttribute('type') || 'text').toLowerCase()
  return ['text', 'search', 'email', 'url', 'tel', 'password',
          'number', 'date', 'time', 'datetime-local', 'month', 'week']
    .includes(type)
}

function modeFor(el) {
  const hint = (el.getAttribute('inputmode') || '').toLowerCase()
  if (NUMERIC_MODES.has(hint)) return 'numeric'
  const type = (el.getAttribute('type') || '').toLowerCase()
  if (type === 'number' || type === 'tel') return 'numeric'
  return 'text'
}

function show(el) {
  if (hasHardwareKeyboard || !coarsePointer) return
  target = el
  const mode = modeFor(el)
  currentMode.value = mode
  layoutName.value = 'default'
  visible.value = true
  nextTick(() => {
    if (!kb) initKeyboard()
    else {
      kb.setOptions({ layout: layouts[mode] })
      kb.setInput(target.value || '')
    }
  })
}

function hide() {
  visible.value = false
  target = null
}

const layouts = {
  text: {
    default: [
      '1 2 3 4 5 6 7 8 9 0 {bksp}',
      'q w e r t y u i o p',
      'a s d f g h j k l',
      '{shift} z x c v b n m , .',
      '{space} {enter} {hide}'
    ],
    shift: [
      '! @ # $ % ^ & * ( ) {bksp}',
      'Q W E R T Y U I O P',
      'A S D F G H J K L',
      '{shift} Z X C V B N M ; :',
      '{space} {enter} {hide}'
    ]
  },
  numeric: {
    default: [
      '1 2 3',
      '4 5 6',
      '7 8 9',
      '. 0 {bksp}',
      '{enter} {hide}'
    ]
  }
}

function initKeyboard() {
  kb = new Keyboard(keyboardEl.value, {
    layout: layouts[currentMode.value],
    layoutName: layoutName.value,
    display: {
      '{bksp}': '⌫',
      '{enter}': '⏎',
      '{shift}': '⇧',
      '{space}': '␣',
      '{hide}': '▼'
    },
    onChange: input => {
      if (!target) return
      target.value = input
      target.dispatchEvent(new Event('input', { bubbles: true }))
    },
    onKeyPress: button => {
      if (button === '{shift}') {
        layoutName.value = layoutName.value === 'default' ? 'shift' : 'default'
        kb.setOptions({ layoutName: layoutName.value })
        return
      }
      if (button === '{enter}') {
        if (target && target.tagName !== 'TEXTAREA') {
          const form = target.form
          if (form) form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
        } else if (target) {
          const start = target.selectionStart ?? target.value.length
          target.value = target.value.slice(0, start) + '\n' + target.value.slice(start)
          kb.setInput(target.value)
          target.dispatchEvent(new Event('input', { bubbles: true }))
        }
        return
      }
      if (button === '{hide}') hide()
    }
  })
  if (target) kb.setInput(target.value || '')
}

function onFocusIn(ev) {
  const t = ev.target
  if (!isEditable(t)) return
  show(t)
}

function onFocusOut(ev) {
  // Give the OSK button tap a chance to re-focus before we tear down.
  setTimeout(() => {
    const active = document.activeElement
    if (!isEditable(active)) hide()
  }, 50)
}

function onPhysicalKey(ev) {
  // Any keydown on an editable target that wasn't synthesised by us
  // means a real keyboard is attached. simple-keyboard never fires
  // keydown — it mutates .value directly.
  if (ev.isTrusted && !hasHardwareKeyboard) {
    hasHardwareKeyboard = true
    hide()
  }
}

onMounted(() => {
  if (!coarsePointer) return
  document.addEventListener('focusin', onFocusIn)
  document.addEventListener('focusout', onFocusOut)
  document.addEventListener('keydown', onPhysicalKey, true)
})

onUnmounted(() => {
  document.removeEventListener('focusin', onFocusIn)
  document.removeEventListener('focusout', onFocusOut)
  document.removeEventListener('keydown', onPhysicalKey, true)
  if (kb) { kb.destroy(); kb = null }
})
</script>

<template>
  <div
    v-show="visible"
    class="fixed inset-x-0 bottom-0 z-[60] bg-tactical-surface/95 backdrop-blur border-t border-tactical-border p-2"
    @mousedown.prevent
    @touchstart.prevent>
    <div ref="keyboardEl" class="simple-keyboard"></div>
  </div>
</template>

<style>
/* Global (un-scoped) overrides so simple-keyboard's own class tree
   picks them up. Kiosk-friendly sizing + dark theme. */
.simple-keyboard.hg-theme-default {
  background: transparent;
  padding: 0;
}
.simple-keyboard .hg-button {
  background: rgb(31 41 55);
  color: rgb(229 231 235);
  border: 1px solid rgb(55 65 81);
  height: 48px;
  font-size: 16px;
}
.simple-keyboard .hg-button:active,
.simple-keyboard .hg-button.hg-activeButton {
  background: rgb(59 130 246);
}
</style>
