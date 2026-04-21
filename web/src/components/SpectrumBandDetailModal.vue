<!--
  SpectrumBandDetailModal.vue
  ---------------------------
  Full-screen modal wrapper around SpectrumBandDetailView. The overlay
  covers ~95 × 95 % of the viewport, backdrop dims + blurs the page
  behind it. Click anywhere outside the panel, click the × button, or
  hit Escape to close.

  SpectrumBandDetailView is mounted inside with `modal=true`, which
  swaps its root sizing (fills the modal panel instead of the
  viewport) and hides its Back button — the modal owns the close UX.
-->
<script setup>
import { onMounted, onBeforeUnmount } from 'vue'
import SpectrumBandDetailView from '@/views/SpectrumBandDetailView.vue'

const props = defineProps({
  band: { type: String, required: true },
})
const emit = defineEmits(['close'])

function onKey(e) {
  if (e.key === 'Escape') emit('close')
}
onMounted(() => {
  document.addEventListener('keydown', onKey)
  // Lock background scroll so the page behind the modal doesn't
  // scroll under a finger drag on the touchscreen.
  document.body.style.overflow = 'hidden'
})
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKey)
  document.body.style.overflow = ''
})
</script>

<template>
  <teleport to="body">
    <div class="sbdm-backdrop" @click.self="$emit('close')">
      <div class="sbdm-panel" role="dialog" aria-modal="true" :aria-label="`${band} spectrum history`">
        <button class="sbdm-close" @click="$emit('close')" aria-label="Close">×</button>
        <SpectrumBandDetailView :band="band" :modal="true" @request-close="$emit('close')" />
      </div>
    </div>
  </teleport>
</template>

<style scoped>
.sbdm-backdrop {
  position: fixed; inset: 0;
  background: rgba(0, 0, 0, 0.78);
  backdrop-filter: blur(4px);
  -webkit-backdrop-filter: blur(4px);
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 2.5vh 2vw;
}
.sbdm-panel {
  position: relative;
  width: 96vw;
  height: 95vh;
  max-width: 96vw;
  max-height: 95vh;
  background: #020617;
  border: 1px solid #1e293b;
  border-radius: 10px;
  overflow: hidden;
  box-shadow: 0 20px 60px rgba(0, 0, 0, 0.6);
  display: flex;
  flex-direction: column;
}
.sbdm-close {
  position: absolute;
  top: 10px; right: 14px;
  width: 36px; height: 36px;
  background: rgba(15, 23, 42, 0.85);
  border: 1px solid #334155;
  border-radius: 6px;
  color: #e2e8f0;
  font-size: 22px; line-height: 1; font-weight: 500;
  cursor: pointer;
  z-index: 5;
  display: flex; align-items: center; justify-content: center;
  transition: background 0.15s ease, color 0.15s ease;
}
.sbdm-close:hover { background: #334155; color: white; }
.sbdm-close:focus-visible {
  outline: 2px solid #60a5fa;
  outline-offset: 2px;
}
</style>
