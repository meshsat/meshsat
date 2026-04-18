<script setup>
import { computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

// Four-chip precedence selector. Operator-friendly labels on top
// (Emergency / Urgent / Normal / Low); STANAG 4406 names + ACP-127
// prosigns surface only in Engineer Mode. [MESHSAT-551]
//
// The wire value emitted is the canonical STANAG name accepted by
// POST /api/messages/send-to-contact (Flash / Immediate / Routine /
// Deferred). The send-to-contact handler already accepts the prosigns
// too, but we stick to the long names so UI readers don't have to
// decode.

const chips = [
  { label: 'Emergency', wire: 'Flash',    prosign: 'Z', color: 'text-red-400 border-red-500 bg-red-500/10' },
  { label: 'Urgent',    wire: 'Immediate', prosign: 'O', color: 'text-amber-400 border-amber-500 bg-amber-500/10' },
  { label: 'Normal',    wire: 'Routine',   prosign: 'R', color: 'text-tactical-iridium border-tactical-iridium bg-tactical-iridium/10' },
  { label: 'Low',       wire: 'Deferred',  prosign: 'M', color: 'text-gray-400 border-gray-500 bg-gray-500/10' }
]

const props = defineProps({
  modelValue: { type: String, default: 'Routine' }
})
const emit = defineEmits(['update:modelValue'])

const store = useMeshsatStore()

function pick(wire) { emit('update:modelValue', wire) }

const selected = computed(() => props.modelValue)
</script>

<template>
  <div>
    <label class="block text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-1">
      Precedence
    </label>
    <div class="grid grid-cols-4 gap-2" role="radiogroup" aria-label="Precedence">
      <button v-for="c in chips" :key="c.wire" type="button" @click="pick(c.wire)"
        role="radio" :aria-checked="selected === c.wire"
        class="px-2 py-2 rounded border text-xs font-medium transition-colors min-h-[48px] flex flex-col items-center justify-center gap-0.5"
        :class="selected === c.wire ? c.color : 'border-tactical-border text-gray-500 hover:text-gray-300 hover:bg-white/5'">
        <span class="text-xs font-semibold tracking-wide">{{ c.label }}</span>
        <span v-if="store.isEngineer" class="text-[9px] font-mono opacity-80">
          {{ c.wire }} · {{ c.prosign }}
        </span>
      </button>
    </div>
  </div>
</template>
