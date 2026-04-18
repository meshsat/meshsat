<script setup>
import { computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

// Contextual help marker. [MESHSAT-571]
//
// Renders a small "?" next to an Engineer-only field; hovering or
// focusing shows the concept explanation. Invisible in Operator
// mode so the simplified shell stays uncluttered.
//
// Intended usage:
//   <label>
//     Strategy <FieldHelp text="PRIMARY_ONLY picks the highest-rank bearer..." />
//   </label>
//
// Keeping the copy inline instead of reaching into i18n because
// these are long sentences tied to a specific field; the IQ-70 i18n
// layer (MESHSAT-557) is for short labels that change per mode.

const props = defineProps({
  text: { type: String, required: true }
})

const store = useMeshsatStore()
const show = computed(() => store.isEngineer)
</script>

<template>
  <span v-if="show" class="inline-flex items-center relative group align-middle ml-1">
    <span class="w-3.5 h-3.5 rounded-full border border-gray-500 text-gray-500 text-[9px] leading-[12px] text-center cursor-help">
      ?
    </span>
    <span
      class="hidden group-hover:block group-focus-within:block absolute bottom-full left-1/2 -translate-x-1/2 mb-1 w-64 max-w-[70vw] p-2 text-[11px] text-gray-200 bg-tactical-surface border border-tactical-border rounded shadow-lg z-30 pointer-events-none whitespace-normal">
      {{ text }}
    </span>
  </span>
</template>
