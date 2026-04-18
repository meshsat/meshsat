<script setup>
import { computed, ref } from 'vue'
import ms from 'milsymbol'

// STANAG 4677 reduced symbol picker. [MESHSAT-559]
//
// We expose a curated shortlist rather than the full MIL-STD-2525D
// tree — the operator in the field picks a handful of common
// entities (friendly/hostile infantry, medical, HQ, UAV, vehicle,
// sensor). Engineers who need the full tree hand-edit the SIDC in
// Engineer Mode or through the REST API.
//
// SIDC format per MIL-STD-2525D: 20 characters; for the reduced
// set we synthesise a short-form code (10-character 2525C-style)
// which milsymbol accepts and upgrades.

const props = defineProps({
  modelValue: { type: String, default: '' }
})
const emit = defineEmits(['update:modelValue'])

// Reduced set. SIDC is the wire value; label/hint is UI copy.
const symbols = [
  { sidc: 'SFGPUCI---*****', label: 'Infantry',       hint: 'Friendly · ground · infantry' },
  { sidc: 'SFGPUCIM--*****', label: 'Med. Infantry',  hint: 'Friendly · medical personnel' },
  { sidc: 'SFGPUH----*****', label: 'HQ',             hint: 'Friendly · headquarters' },
  { sidc: 'SFAPMFQ---*****', label: 'UAV',            hint: 'Friendly · air · UAV' },
  { sidc: 'SFGPEV----*****', label: 'Vehicle',        hint: 'Friendly · ground · vehicle' },
  { sidc: 'SFGPES----*****', label: 'Sensor',         hint: 'Friendly · ground · sensor' },
  { sidc: 'SFGPUCIZ--*****', label: 'Weapons',        hint: 'Friendly · ground · weapons' },
  { sidc: 'SHGPUCI---*****', label: 'Hostile',        hint: 'Hostile · ground · infantry' },
  { sidc: 'SNGPUCI---*****', label: 'Neutral',        hint: 'Neutral · ground · infantry' },
  { sidc: 'SUGPUCI---*****', label: 'Unknown',        hint: 'Unknown · ground · infantry' }
]

function renderIcon(sidc, size = 24) {
  try {
    return new ms.Symbol(sidc, { size }).asSVG()
  } catch {
    return ''
  }
}

const selected = computed(() => props.modelValue || '')

const open = ref(false)
function toggle() { open.value = !open.value }
function pick(sidc) {
  emit('update:modelValue', sidc)
  open.value = false
}
function clear() {
  emit('update:modelValue', '')
  open.value = false
}

const selectedSymbol = computed(() => symbols.find(s => s.sidc === selected.value))
</script>

<template>
  <div>
    <label class="block text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-1">
      Symbol (MIL-STD-2525D)
    </label>
    <button type="button" @click="toggle"
      class="w-full flex items-center gap-2 px-3 py-2 rounded bg-tactical-surface border border-tactical-border text-sm min-h-[48px]">
      <span class="w-6 h-6 flex items-center justify-center" v-html="selected ? renderIcon(selected, 22) : ''" />
      <span class="flex-1 text-left">
        <template v-if="selectedSymbol">{{ selectedSymbol.label }}</template>
        <template v-else-if="selected" class="font-mono">{{ selected }}</template>
        <template v-else><span class="text-gray-500">Pick a symbol…</span></template>
      </span>
      <svg class="w-3 h-3 text-gray-500" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
        <path d="M6 9l6 6 6-6"/>
      </svg>
    </button>

    <div v-show="open" class="mt-2 grid grid-cols-5 gap-2 p-2 bg-tactical-surface border border-tactical-border rounded">
      <button v-for="s in symbols" :key="s.sidc" type="button" @click="pick(s.sidc)"
        class="flex flex-col items-center justify-center gap-1 p-2 rounded hover:bg-white/5 min-h-[72px]"
        :class="s.sidc === selected ? 'bg-tactical-iridium/10' : ''"
        :title="s.hint">
        <span class="w-8 h-8 flex items-center justify-center" v-html="renderIcon(s.sidc, 28)" />
        <span class="text-[9px] text-gray-400 text-center leading-tight">{{ s.label }}</span>
      </button>
      <button type="button" @click="clear"
        class="flex flex-col items-center justify-center gap-1 p-2 rounded hover:bg-white/5 min-h-[72px] text-gray-500">
        <svg class="w-6 h-6" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 18L18 6M6 6l12 12"/></svg>
        <span class="text-[9px]">None</span>
      </button>
    </div>
  </div>
</template>
