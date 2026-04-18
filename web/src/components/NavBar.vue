<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { useMeshsatStore } from '@/stores/meshsat'

// IQ-70 five-item primary nav for Operator mode; full 16-item nav
// for Engineer mode. Mobile viewports (<768 px) get a bottom-tab
// bar so the primary actions stay in thumb reach (Fitts's law).
// [MESHSAT-550]

const route = useRoute()
const store = useMeshsatStore()

// Operator mode — 5 items in thumb reach. Compose and Inbox both
// sit on /messages today; MESHSAT-551 adds the split Compose view.
const primary = [
  { name: 'compose',  label: 'Compose',  path: '/messages?tab=compose', match: '/messages', icon: 'compose' },
  { name: 'inbox',    label: 'Inbox',    path: '/messages',             match: '/messages', icon: 'inbox'   },
  { name: 'map',      label: 'Map',      path: '/map',                  match: '/map',      icon: 'map'     },
  { name: 'people',   label: 'People',   path: '/nodes',                match: '/nodes',    icon: 'people'  },
  { name: 'radios',   label: 'Radios',   path: '/interfaces',           match: '/interfaces', icon: 'radios' }
]

// Everything else lives under the ⋮ More overflow menu in Operator
// mode. Engineer mode keeps the full flat tab strip it had before.
const overflow = [
  { name: 'dashboard', label: 'Dashboard', path: '/' },
  { name: 'bridge',    label: 'Bridge',    path: '/bridge' },
  { name: 'passes',    label: 'Passes',    path: '/passes' },
  { name: 'topology',  label: 'Topology',  path: '/topology' },
  { name: 'radio',     label: 'Meshtastic', path: '/radio' },
  { name: 'zigbee',    label: 'ZigBee',    path: '/zigbee' },
  { name: 'tak',       label: 'TAK',       path: '/tak' },
  { name: 'spectrum',  label: 'Spectrum',  path: '/spectrum' },
  { name: 'audit',     label: 'Audit',     path: '/audit' },
  { name: 'settings',  label: 'Settings',  path: '/settings' },
  { name: 'help',      label: 'Help',      path: '/help' },
  { name: 'about',     label: 'About',     path: '/about' }
]

const engineerTabs = [
  { name: 'dashboard',  label: 'Dashboard',  path: '/' },
  { name: 'comms',      label: 'Comms',      path: '/messages' },
  { name: 'nodes',      label: 'Peers',      path: '/nodes' },
  { name: 'bridge',     label: 'Bridge',     path: '/bridge' },
  { name: 'interfaces', label: 'Interfaces', path: '/interfaces' },
  { name: 'passes',     label: 'Passes',     path: '/passes' },
  { name: 'topology',   label: 'Topology',   path: '/topology' },
  { name: 'map',        label: 'Map',        path: '/map' },
  { name: 'settings',   label: 'Settings',   path: '/settings' },
  { name: 'radio',      label: 'Meshtastic', path: '/radio' },
  { name: 'zigbee',     label: 'ZigBee',     path: '/zigbee' },
  { name: 'tak',        label: 'TAK',        path: '/tak' },
  { name: 'spectrum',   label: 'Spectrum',   path: '/spectrum' },
  { name: 'audit',      label: 'Audit',      path: '/audit' },
  { name: 'help',       label: 'Help',       path: '/help' },
  { name: 'about',      label: 'About',      path: '/about' }
]

function isActive(tab) {
  const target = tab.match || tab.path
  if (target === '/') return route.path === '/'
  return route.path.startsWith(target)
}

const moreOpen = ref(false)
function toggleMore() { moreOpen.value = !moreOpen.value }
function closeMore() { moreOpen.value = false }

// Close More on any outside click.
function onDocClick(ev) {
  if (!moreOpen.value) return
  const el = document.getElementById('meshsat-nav-more')
  if (el && !el.contains(ev.target)) closeMore()
}
onMounted(() => document.addEventListener('click', onDocClick))
onUnmounted(() => document.removeEventListener('click', onDocClick))

const viewportWidth = ref(typeof window !== 'undefined' ? window.innerWidth : 1024)
function onResize() { viewportWidth.value = window.innerWidth }
onMounted(() => window.addEventListener('resize', onResize))
onUnmounted(() => window.removeEventListener('resize', onResize))

// Bottom tab bar is only shown in Operator mode on phone viewports.
const showBottomBar = computed(() => store.isOperator && viewportWidth.value < 768)

// At ≤lg we hide text labels and show icons only (header nav).
const compactLabels = computed(() => viewportWidth.value < 1024)
</script>

<template>
  <!-- Top nav strip: Engineer gets the full list; Operator gets 5 + More. -->
  <nav class="flex-1 flex items-center overflow-x-auto no-scrollbar mx-2 lg:mx-6">
    <!-- Operator mode — 5 items + More -->
    <div v-if="store.isOperator" class="flex items-center gap-0.5">
      <router-link v-for="tab in primary" :key="tab.name" :to="tab.path"
        class="px-3 py-1.5 rounded text-xs font-medium whitespace-nowrap transition-colors flex items-center gap-1.5 min-h-[32px]"
        :class="isActive(tab)
          ? 'bg-tactical-iridium/10 text-tactical-iridium'
          : 'text-gray-500 hover:text-gray-300 hover:bg-white/5'"
        :aria-label="tab.label" :title="tab.label">
        <!-- Minimal inline icons; no assets needed. -->
        <svg v-if="tab.icon === 'compose'" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 20h4L20 8l-4-4L4 16v4z"/><path d="M14 6l4 4"/></svg>
        <svg v-else-if="tab.icon === 'inbox'" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M3 7l9 6 9-6"/><rect x="3" y="5" width="18" height="14" rx="2"/></svg>
        <svg v-else-if="tab.icon === 'map'" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 21s-7-7.5-7-12a7 7 0 0 1 14 0c0 4.5-7 12-7 12z"/><circle cx="12" cy="9" r="2.5"/></svg>
        <svg v-else-if="tab.icon === 'people'" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="9" cy="8" r="3.5"/><path d="M2 20c0-3.5 3-6 7-6s7 2.5 7 6"/><circle cx="17" cy="7" r="2.5"/><path d="M17 12c3 0 5 2 5 5"/></svg>
        <svg v-else-if="tab.icon === 'radios'" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 10a9 9 0 0 1 16 0"/><path d="M7 13a5 5 0 0 1 10 0"/><circle cx="12" cy="16" r="1.5"/></svg>
        <span :class="{ 'hidden lg:inline': compactLabels }">{{ tab.label }}</span>
      </router-link>

      <!-- ⋮ More -->
      <div id="meshsat-nav-more" class="relative">
        <button type="button" @click.stop="toggleMore"
          class="px-2 py-1.5 rounded text-xs font-medium text-gray-500 hover:text-gray-300 hover:bg-white/5"
          :class="{ 'bg-white/5 text-gray-300': moreOpen }"
          aria-label="More" :aria-expanded="moreOpen">
          <svg class="w-4 h-4" viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/></svg>
        </button>
        <div v-show="moreOpen"
          class="absolute right-0 mt-1 w-44 bg-tactical-surface border border-tactical-border rounded shadow-lg z-50 py-1">
          <router-link v-for="tab in overflow" :key="tab.name" :to="tab.path" @click="closeMore"
            class="block px-3 py-1.5 text-xs"
            :class="isActive(tab)
              ? 'bg-tactical-iridium/10 text-tactical-iridium'
              : 'text-gray-400 hover:text-gray-200 hover:bg-white/5'">
            {{ tab.label }}
          </router-link>
        </div>
      </div>
    </div>

    <!-- Engineer mode — original flat tab strip, untouched. -->
    <div v-else class="flex items-center gap-0.5">
      <router-link v-for="tab in engineerTabs" :key="tab.name" :to="tab.path"
        class="px-3 py-1.5 rounded text-xs font-medium whitespace-nowrap transition-colors"
        :class="isActive(tab)
          ? 'bg-tactical-iridium/10 text-tactical-iridium'
          : 'text-gray-500 hover:text-gray-300 hover:bg-white/5'">
        {{ tab.label }}
      </router-link>
    </div>
  </nav>

  <!-- Bottom tab bar, phone viewport, Operator only — thumb-reach primary actions. -->
  <nav v-show="showBottomBar"
    class="fixed bottom-0 inset-x-0 z-40 bg-tactical-surface/95 backdrop-blur border-t border-tactical-border flex items-center justify-around h-14 md:hidden"
    aria-label="Primary navigation">
    <router-link v-for="tab in primary" :key="'b-'+tab.name" :to="tab.path"
      class="flex flex-col items-center justify-center gap-0.5 flex-1 h-full text-[10px]"
      :class="isActive(tab)
        ? 'text-tactical-iridium'
        : 'text-gray-500'"
      :aria-label="tab.label">
      <svg v-if="tab.icon === 'compose'" class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 20h4L20 8l-4-4L4 16v4z"/><path d="M14 6l4 4"/></svg>
      <svg v-else-if="tab.icon === 'inbox'" class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M3 7l9 6 9-6"/><rect x="3" y="5" width="18" height="14" rx="2"/></svg>
      <svg v-else-if="tab.icon === 'map'" class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 21s-7-7.5-7-12a7 7 0 0 1 14 0c0 4.5-7 12-7 12z"/><circle cx="12" cy="9" r="2.5"/></svg>
      <svg v-else-if="tab.icon === 'people'" class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="9" cy="8" r="3.5"/><path d="M2 20c0-3.5 3-6 7-6s7 2.5 7 6"/><circle cx="17" cy="7" r="2.5"/><path d="M17 12c3 0 5 2 5 5"/></svg>
      <svg v-else-if="tab.icon === 'radios'" class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 10a9 9 0 0 1 16 0"/><path d="M7 13a5 5 0 0 1 10 0"/><circle cx="12" cy="16" r="1.5"/></svg>
      <span>{{ tab.label }}</span>
    </router-link>
  </nav>
</template>

<style scoped>
.no-scrollbar::-webkit-scrollbar { display: none; }
.no-scrollbar { -ms-overflow-style: none; scrollbar-width: none; }
</style>
