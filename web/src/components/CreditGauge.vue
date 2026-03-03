<script setup>
import { computed } from 'vue'

const props = defineProps({
  used: { type: Number, default: 0 },
  budget: { type: Number, default: 0 },
  label: { type: String, default: 'Credits' }
})

const pct = computed(() => {
  if (props.budget <= 0) return 0
  return Math.min(100, Math.round((props.used / props.budget) * 100))
})

const color = computed(() => {
  if (pct.value >= 100) return 'bg-red-500'
  if (pct.value >= 90) return 'bg-red-400'
  if (pct.value >= 75) return 'bg-amber-400'
  return 'bg-emerald-400'
})
</script>

<template>
  <div class="flex items-center gap-2 text-xs">
    <span class="text-gray-400">{{ label }}:</span>
    <span v-if="budget > 0" class="text-gray-300">{{ used }}/{{ budget }}</span>
    <span v-else class="text-gray-500">{{ used }}</span>
    <div v-if="budget > 0" class="w-16 h-1.5 bg-gray-700 rounded-full overflow-hidden">
      <div :class="color" class="h-full rounded-full transition-all" :style="{ width: pct + '%' }"></div>
    </div>
  </div>
</template>
