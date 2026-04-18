<script setup>
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

// Typeahead contact combobox. The backend `?q=` filter does not exist
// yet (see MESHSAT-551 notes) — we hydrate the full list on mount and
// filter client-side. Field-kit directories are small (<100 contacts);
// switch to a server-side search if that ever stops being true.
// [MESHSAT-551]

const props = defineProps({
  modelValue: { type: Object, default: null }  // { id, display_name, addresses }
})
const emit = defineEmits(['update:modelValue'])

const store = useMeshsatStore()
const query = ref('')
const open = ref(false)
const active = ref(0)

onMounted(() => {
  if (!store.contacts || store.contacts.length === 0) {
    store.fetchContacts()
  }
})

const matches = computed(() => {
  const q = query.value.trim().toLowerCase()
  const list = store.contacts || []
  if (!q) return list.slice(0, 12)
  return list.filter(c => {
    if ((c.display_name || '').toLowerCase().includes(q)) return true
    const addrs = c.addresses || []
    return addrs.some(a => (a.value || '').toLowerCase().includes(q))
  }).slice(0, 12)
})

function pick(c) {
  emit('update:modelValue', c)
  query.value = c.display_name
  open.value = false
}

function onFocus() { open.value = true }
function onKey(ev) {
  if (!open.value) open.value = true
  if (ev.key === 'ArrowDown') { active.value = Math.min(active.value + 1, matches.value.length - 1); ev.preventDefault() }
  else if (ev.key === 'ArrowUp') { active.value = Math.max(active.value - 1, 0); ev.preventDefault() }
  else if (ev.key === 'Enter') { if (matches.value[active.value]) pick(matches.value[active.value]); ev.preventDefault() }
  else if (ev.key === 'Escape') { open.value = false }
}

function onDocClick(ev) {
  const el = document.getElementById('contact-picker')
  if (el && !el.contains(ev.target)) open.value = false
}
onMounted(() => document.addEventListener('click', onDocClick))
onUnmounted(() => document.removeEventListener('click', onDocClick))

watch(() => props.modelValue, (c) => { if (c) query.value = c.display_name })
</script>

<template>
  <div id="contact-picker" class="relative">
    <label class="block text-[10px] font-medium text-gray-400 uppercase tracking-wide mb-1">
      To
    </label>
    <input type="text" v-model="query" @focus="onFocus" @keydown="onKey" autocomplete="off"
      class="w-full px-3 py-2 rounded bg-tactical-surface border border-tactical-border text-sm focus:outline-none focus:border-tactical-iridium min-h-[48px]"
      placeholder="Type a name or address…" />
    <ul v-show="open && matches.length" role="listbox"
      class="absolute left-0 right-0 mt-1 bg-tactical-surface border border-tactical-border rounded shadow-lg z-30 max-h-64 overflow-y-auto">
      <li v-for="(c, i) in matches" :key="c.id" @click="pick(c)"
        role="option" :aria-selected="i === active"
        class="px-3 py-2 cursor-pointer text-sm"
        :class="i === active ? 'bg-tactical-iridium/10 text-tactical-iridium' : 'text-gray-300 hover:bg-white/5'">
        <div class="font-medium">{{ c.display_name }}</div>
        <div v-if="c.addresses && c.addresses.length" class="text-[10px] text-gray-500 mt-0.5">
          {{ c.addresses.map(a => a.kind + ':' + a.value).join(' · ') }}
        </div>
      </li>
      <li v-if="!matches.length" class="px-3 py-2 text-sm text-gray-500">No contacts match.</li>
    </ul>
  </div>
</template>
