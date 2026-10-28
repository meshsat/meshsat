<script setup>
// The relayed public warnings in force at this kit, as a banner
// (MESHSAT-1734). On the operator dashboard every warning in force gets its
// headline and the label line beneath, so the operator reads what the mesh,
// the APRS bulletin and the Post portal are saying. On the booth screen
// (`compact`) it is one line: the newest warning's headline, the label
// after it, and a count when more are in force. Nothing renders when none
// is; the words are the relay's own (warnings.headline, warnings.LabelFor).
import { computed, onMounted, onUnmounted } from 'vue'
import { useWarningsStore, headline, label } from '@/stores/warnings'

const props = defineProps({
  compact: { type: Boolean, default: false }
})

const store = useWarningsStore()

const live = computed(() => store.inForce)
const first = computed(() => live.value[0] || null)
const more = computed(() => Math.max(0, live.value.length - 1))

// A test warning is amber, a real one red: the same split the SMS credit
// banner uses for degraded against failed, so the panel's colours keep one
// meaning. Extreme and Severe are not told apart here; the word is.
function tone(w) {
  return w.test
    ? 'bg-amber-950/80 text-amber-100 border-amber-800/60'
    : 'bg-red-950/80 text-red-100 border-red-800/60'
}

function dot(w) {
  return w.test ? 'bg-amber-400' : 'bg-red-400 animate-pulse'
}

onMounted(() => store.start())
onUnmounted(() => store.stop())
</script>

<template>
  <!-- booth screen: one line, nothing else -->
  <div v-if="compact && first" data-testid="warning-banner" role="status"
    class="shrink-0 px-4 py-1 text-xs font-mono flex items-center gap-2 border-b overflow-hidden whitespace-nowrap"
    :class="tone(first)" :title="first.text ? headline(first) + ': ' + first.text : headline(first)">
    <span class="inline-block w-1.5 h-1.5 rounded-full shrink-0" :class="dot(first)" />
    <span class="font-semibold shrink-0" data-role="headline">{{ headline(first) }}</span>
    <span class="truncate opacity-70" data-role="label">{{ label(first) }}</span>
    <span v-if="more" class="shrink-0 opacity-70" data-role="more">+{{ more }} more</span>
  </div>

  <!-- the dashboard: every warning in force, headline and label -->
  <div v-else-if="!compact && live.length" data-testid="warning-banner" role="status" class="mb-2 space-y-1.5">
    <router-link v-for="w in live" :key="w.id + '/' + w.msg_type" to="/warnings"
      class="block rounded-lg border px-3 py-2 transition-colors hover:brightness-110"
      :class="tone(w)" :title="w.text || headline(w)">
      <div class="flex items-center gap-2 min-w-0">
        <svg class="w-4 h-4 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round" aria-hidden="true">
          <path d="M12 3 2.5 20h19L12 3z"/><path d="M12 9v5"/><circle cx="12" cy="17" r=".6" fill="currentColor"/>
        </svg>
        <span class="font-mono font-semibold text-sm tracking-wide truncate" data-role="headline">{{ headline(w) }}</span>
        <span v-if="w.text" class="hidden sm:inline text-sm truncate opacity-90" data-role="text">{{ w.text }}</span>
      </div>
      <div class="text-[11px] opacity-70 mt-0.5 pl-6 leading-tight" data-role="label">{{ label(w) }}</div>
    </router-link>
  </div>
</template>
