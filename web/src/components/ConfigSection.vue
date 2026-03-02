<script setup>
import { ref } from 'vue'

const props = defineProps({
  title: String,
  fields: Array,     // [{ key, label, type, tip, options?, min?, max?, step? }]
  modelValue: Object, // the config object
  collapsed: { type: Boolean, default: true }
})

const emit = defineEmits(['update:modelValue'])

const open = ref(!props.collapsed)

function toggle() {
  open.value = !open.value
}

function updateField(key, value) {
  emit('update:modelValue', { ...props.modelValue, [key]: value })
}
</script>

<template>
  <div class="bg-gray-900 rounded-xl border border-gray-800 overflow-hidden">
    <button @click="toggle"
      class="w-full flex items-center justify-between p-4 text-left hover:bg-gray-800/50 transition-colors">
      <h3 class="text-sm font-semibold text-gray-200">{{ title }}</h3>
      <svg class="w-4 h-4 text-gray-500 transition-transform" :class="open ? 'rotate-180' : ''" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
        <path d="M6 9l6 6 6-6"/>
      </svg>
    </button>

    <div v-if="open" class="border-t border-gray-800 p-4 space-y-4">
      <div v-for="field in fields" :key="field.key">
        <!-- Checkbox -->
        <template v-if="field.type === 'checkbox'">
          <label class="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
            <input
              type="checkbox"
              :checked="modelValue?.[field.key]"
              @change="updateField(field.key, $event.target.checked)"
              class="accent-teal-500"
            />
            {{ field.label }}
          </label>
        </template>

        <!-- Select -->
        <template v-else-if="field.type === 'select'">
          <label class="block text-xs text-gray-400 mb-1">{{ field.label }}</label>
          <select
            :value="modelValue?.[field.key] ?? ''"
            @change="updateField(field.key, $event.target.value)"
            class="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none"
          >
            <option v-for="opt in field.options" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
        </template>

        <!-- Number -->
        <template v-else-if="field.type === 'number'">
          <label class="block text-xs text-gray-400 mb-1">{{ field.label }}</label>
          <input
            type="number"
            :value="modelValue?.[field.key] ?? ''"
            @input="updateField(field.key, Number($event.target.value))"
            :min="field.min"
            :max="field.max"
            :step="field.step ?? 1"
            class="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none"
          />
        </template>

        <!-- Text (default) -->
        <template v-else>
          <label class="block text-xs text-gray-400 mb-1">{{ field.label }}</label>
          <input
            type="text"
            :value="modelValue?.[field.key] ?? ''"
            @input="updateField(field.key, $event.target.value)"
            class="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-sm text-gray-200 focus:border-teal-500 focus:outline-none"
          />
        </template>

        <p v-if="field.tip" class="text-[10px] text-gray-600 mt-1">{{ field.tip }}</p>
      </div>
    </div>
  </div>
</template>
