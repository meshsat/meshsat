<script setup>
import { ref, computed, watch } from 'vue'
import { templates, templateList } from '@/composables/usmtfTemplates'

// USMTF form picker + field editor. [MESHSAT-563]
//
// Drops into Compose — pick a template, fill the fields, the
// composable flattens the values to a slash-delimited wire string
// the caller passes through as the message body. Slash format was
// chosen because SALUTE / MEDEVAC / SITREP all have deterministic
// field orderings and survive any bearer (SMS, mesh, APRS). The
// XML path for TAK-bound traffic is future work (MESHSAT-569).

const props = defineProps({
  modelValue: { type: String, default: '' }  // wire string
})
const emit = defineEmits(['update:modelValue', 'picked'])

const selected = ref('')
const values = ref({})

watch(selected, (id) => {
  values.value = {}
  if (id) {
    emit('picked', id)
    update()
  }
})

function update() {
  if (!selected.value) {
    emit('update:modelValue', '')
    return
  }
  const t = templates[selected.value]
  const wire = t.toWire(values.value || {})
  emit('update:modelValue', wire)
}

const currentTemplate = computed(() => selected.value ? templates[selected.value] : null)
</script>

<template>
  <div class="space-y-3 border border-dashed border-tactical-border rounded p-3">
    <div class="flex items-center justify-between gap-2">
      <label class="block text-[10px] font-medium text-gray-400 uppercase tracking-wide">
        USMTF template
      </label>
      <select v-model="selected"
        class="px-2 py-1.5 rounded bg-tactical-surface border border-tactical-border text-xs min-h-[36px]">
        <option value="">— none —</option>
        <option v-for="t in templateList()" :key="t.id" :value="t.id">{{ t.name }}</option>
      </select>
    </div>

    <div v-if="currentTemplate" class="space-y-2">
      <div v-for="f in currentTemplate.fields" :key="f.key" class="space-y-0.5">
        <label class="block text-[10px] text-gray-500">
          {{ f.label }}<span v-if="f.required" class="text-amber-400 ml-0.5">*</span>
        </label>
        <input type="text" v-model="values[f.key]" @input="update"
          :placeholder="f.placeholder"
          class="w-full px-2 py-1.5 rounded bg-tactical-bg border border-tactical-border text-xs min-h-[36px]" />
      </div>
      <div class="text-[9px] text-gray-500 font-mono break-all">
        Wire: {{ modelValue || '(empty)' }}
      </div>
    </div>
  </div>
</template>
