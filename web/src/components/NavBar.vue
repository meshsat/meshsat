<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { useMeshsatStore } from '@/stores/meshsat'
import { useLabel } from '@/i18n'

// IQ-70 five-item primary nav for Operator mode; full 16-item nav
// for Engineer mode. Mobile viewports (<768 px) get a bottom-tab
// bar so the primary actions stay in thumb reach (Fitts's law).
// [MESHSAT-550]

const route = useRoute()
const store = useMeshsatStore()
const { t, tooltip } = useLabel()

// Operator mode — 5 items in thumb reach. Compose and Inbox both
// sit on /messages today; MESHSAT-551 adds the split Compose view.
// Labels come from the i18n composable so Operator/Engineer swap
// terminology per MESHSAT-557.
const primary = computed(() => [
  { name: 'compose',  key: 'nav.compose', path: '/compose',   match: '/compose', icon: 'compose' },
  { name: 'inbox',    key: 'nav.inbox',   path: '/inbox',     match: '/inbox',   icon: 'inbox'   },
  { name: 'map',      key: 'nav.map',     path: '/map',       match: '/map',     icon: 'map'     },
  { name: 'people',   key: 'nav.people',  path: '/people',    match: '/people',  icon: 'people'  },
  { name: 'radios',   key: 'nav.radios',  path: '/radios',    match: '/radios',  icon: 'radios' }
])

// Everything else lives under the ⋮ More overflow menu in Operator
// mode. Engineer mode keeps the full flat tab strip it had before.
const overflow = computed(() => [
  { name: 'dashboard', key: 'nav.dashboard',  path: '/' },
  { name: 'bridge',    key: 'nav.bridge',     path: '/bridge' },
  { name: 'passes',    key: 'nav.passes',     path: '/passes' },
  { name: 'topology',  key: 'nav.topology',   path: '/topology' },
  { name: 'radio',     key: 'nav.meshtastic', path: '/radio' },
  { name: 'zigbee',    key: 'nav.zigbee',     path: '/zigbee' },
  { name: 'tak',       key: 'nav.tak',        path: '/tak' },
  { name: 'spectrum',  key: 'nav.spectrum',   path: '/spectrum' },
  { name: 'audit',     key: 'nav.audit',      path: '/audit' },
  { name: 'settings',  key: 'nav.settings',   path: '/settings' },
  { name: 'help',      key: 'nav.help',       path: '/help' },
  { name: 'about',     key: 'nav.about',      path: '/about' }
])

// Engineer mode: the 5 Operator primary routes FIRST (so the
// reshape is visible in both modes — Engineer is "reveal more", not
// "go back to legacy"), then the deeper admin/diagnostic surfaces.
// [fix: Engineer was pointing Comms/Peers/Interfaces at the legacy
// /messages /nodes /interfaces routes which bypassed the MESHSAT-551
// MESHSAT-552 MESHSAT-553 MESHSAT-554 reshape]
const engineerTabs = computed(() => [
  { name: 'compose',    key: 'nav.compose',    path: '/compose' },
  { name: 'inbox',      key: 'nav.inbox',      path: '/inbox' },
  { name: 'map',        key: 'nav.map',        path: '/map' },
  { name: 'people',     key: 'nav.people',     path: '/people' },
  { name: 'radios',     key: 'nav.radios',     path: '/radios' },
  { name: 'dashboard',  key: 'nav.dashboard',  path: '/' },
  { name: 'bridge',     key: 'nav.bridge',     path: '/bridge' },
  { name: 'interfaces', key: 'nav.interfaces', path: '/interfaces' },
  { name: 'passes',     key: 'nav.passes',     path: '/passes' },
  { name: 'topology',   key: 'nav.topology',   path: '/topology' },
  { name: 'comms',      key: 'nav.comms',      path: '/messages' },
  { name: 'nodes',      key: 'nav.nodes',      path: '/nodes' },
  { name: 'settings',   key: 'nav.settings',   path: '/settings' },
  { name: 'radio',      key: 'nav.meshtastic', path: '/radio' },
  { name: 'zigbee',     key: 'nav.zigbee',     path: '/zigbee' },
  { name: 'tak',        key: 'nav.tak',        path: '/tak' },
  { name: 'spectrum',   key: 'nav.spectrum',   path: '/spectrum' },
  { name: 'audit',      key: 'nav.audit',      path: '/audit' },
  { name: 'help',       key: 'nav.help',       path: '/help' },
  { name: 'about',      key: 'nav.about',      path: '/about' }
])

function isActive(tab) {
  const target = tab.match || tab.path
  if (target === '/') return route.path === '/'
  return route.path.startsWith(target)
}

const moreOpen = ref(false)
const moreBtn = ref(null)
const moreStyle = ref({})

// Compute dropdown position relative to the anchor button. Using
// `position: fixed` + viewport coords so the dropdown escapes the
// parent <nav>'s `overflow-x-auto` clip context (setting overflow-x
// forces overflow-y to "auto" in CSS, which was previously clipping
// the absolute-positioned panel off-screen).
function positionDropdown() {
  const el = moreBtn.value
  if (!el) return
  const r = el.getBoundingClientRect()
  moreStyle.value = {
    position: 'fixed',
    top:   (r.bottom + 4) + 'px',
    right: (window.innerWidth - r.right) + 'px'
  }
}
function toggleMore() {
  moreOpen.value = !moreOpen.value
  if (moreOpen.value) positionDropdown()
}
function closeMore() { moreOpen.value = false }

// Close More on any outside click.
function onDocClick(ev) {
  if (!moreOpen.value) return
  const btn = moreBtn.value
  const menu = document.getElementById('meshsat-nav-more-menu')
  if (btn && btn.contains(ev.target)) return
  if (menu && menu.contains(ev.target)) return
  closeMore()
}
onMounted(() => {
  document.addEventListener('click', onDocClick)
  window.addEventListener('resize', positionDropdown)
  window.addEventListener('scroll', positionDropdown, true)
})
onUnmounted(() => {
  document.removeEventListener('click', onDocClick)
  window.removeEventListener('resize', positionDropdown)
  window.removeEventListener('scroll', positionDropdown, true)
})

const viewportWidth  = ref(typeof window !== 'undefined' ? window.innerWidth  : 1024)
const viewportHeight = ref(typeof window !== 'undefined' ? window.innerHeight :  768)
function onResize() {
  viewportWidth.value  = window.innerWidth
  viewportHeight.value = window.innerHeight
}
onMounted(() => window.addEventListener('resize', onResize))
onUnmounted(() => window.removeEventListener('resize', onResize))

// Bottom tab bar — shown in Operator mode on any viewport that's
// narrower than ~iPad-portrait OR shorter than the Pi Touch Display
// 2's 480 px landscape height. Catches the 7" Pi panel (800×480)
// which would otherwise fall through the `md:` breakpoint.
// [fix: Pi 7" compat]
const showBottomBar = computed(() =>
  store.isOperator &&
  (viewportWidth.value <= 820 || viewportHeight.value <= 520))

// At ≤lg OR short viewports we hide text labels and show icons only
// (header nav) to keep the nav strip inside one row on 800 px wide.
const compactLabels = computed(() =>
  viewportWidth.value < 1024 || viewportHeight.value <= 520)
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
        :aria-label="t(tab.key)"
        :title="tooltip(tab.key) ? t(tab.key) + ' — ' + tooltip(tab.key) : t(tab.key)">
        <!-- Minimal inline icons; no assets needed. -->
        <svg v-if="tab.icon === 'compose'" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 20h4L20 8l-4-4L4 16v4z"/><path d="M14 6l4 4"/></svg>
        <svg v-else-if="tab.icon === 'inbox'" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M3 7l9 6 9-6"/><rect x="3" y="5" width="18" height="14" rx="2"/></svg>
        <svg v-else-if="tab.icon === 'map'" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 21s-7-7.5-7-12a7 7 0 0 1 14 0c0 4.5-7 12-7 12z"/><circle cx="12" cy="9" r="2.5"/></svg>
        <svg v-else-if="tab.icon === 'people'" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="9" cy="8" r="3.5"/><path d="M2 20c0-3.5 3-6 7-6s7 2.5 7 6"/><circle cx="17" cy="7" r="2.5"/><path d="M17 12c3 0 5 2 5 5"/></svg>
        <svg v-else-if="tab.icon === 'radios'" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 10a9 9 0 0 1 16 0"/><path d="M7 13a5 5 0 0 1 10 0"/><circle cx="12" cy="16" r="1.5"/></svg>
        <span :class="{ 'hidden lg:inline': compactLabels }">{{ t(tab.key) }}</span>
      </router-link>

      <!-- ⋮ More -->
      <button ref="moreBtn" type="button" @click.stop="toggleMore"
        class="px-2 py-1.5 rounded text-xs font-medium text-gray-500 hover:text-gray-300 hover:bg-white/5"
        :class="{ 'bg-white/5 text-gray-300': moreOpen }"
        aria-label="More" :aria-expanded="moreOpen">
        <svg class="w-4 h-4" viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/></svg>
      </button>

      <!-- More dropdown — teleported to <body> so it escapes the
           <nav>'s overflow-x-auto clip rect. [MESHSAT-550 fix] -->
      <Teleport to="body">
        <div v-show="moreOpen" id="meshsat-nav-more-menu"
          :style="moreStyle"
          class="w-44 bg-tactical-surface border border-tactical-border rounded shadow-lg z-[9999] py-1">
          <router-link v-for="tab in overflow" :key="tab.name" :to="tab.path" @click="closeMore"
            :title="tooltip(tab.key) ? t(tab.key) + ' — ' + tooltip(tab.key) : t(tab.key)"
            class="block px-3 py-1.5 text-xs"
            :class="isActive(tab)
              ? 'bg-tactical-iridium/10 text-tactical-iridium'
              : 'text-gray-400 hover:text-gray-200 hover:bg-white/5'">
            {{ t(tab.key) }}
          </router-link>
        </div>
      </Teleport>
    </div>

    <!-- Engineer mode — flat tab strip, labels from engineer dict. -->
    <div v-else class="flex items-center gap-0.5">
      <router-link v-for="tab in engineerTabs" :key="tab.name" :to="tab.path"
        class="px-3 py-1.5 rounded text-xs font-medium whitespace-nowrap transition-colors"
        :class="isActive(tab)
          ? 'bg-tactical-iridium/10 text-tactical-iridium'
          : 'text-gray-500 hover:text-gray-300 hover:bg-white/5'">
        {{ t(tab.key) }}
      </router-link>
    </div>
  </nav>

  <!-- Bottom tab bar, Operator only on narrow / short viewports.
       (No `md:hidden` — the Pi 7" at 800×480 is above md=768 and
       was missing this bar entirely.) [fix: Pi 7" compat]
       Teleported to <body> so `position: fixed; bottom: 0` resolves
       to the viewport. If we leave it inside <header> (which has
       `backdrop-blur` = backdrop-filter), that ancestor becomes the
       containing block for fixed descendants and the bar renders at
       y=-8 covering the header. Verified live on tesseract
       2026-04-19. [MESHSAT-549] -->
  <Teleport to="body">
  <nav v-show="showBottomBar"
    class="fixed bottom-0 inset-x-0 z-40 bg-tactical-surface/95 backdrop-blur border-t border-tactical-border flex items-center justify-around h-14"
    aria-label="Primary navigation">
    <router-link v-for="tab in primary" :key="'b-'+tab.name" :to="tab.path"
      class="flex flex-col items-center justify-center gap-0.5 flex-1 h-full text-[10px]"
      :class="isActive(tab)
        ? 'text-tactical-iridium'
        : 'text-gray-500'"
      :aria-label="t(tab.key)">
      <svg v-if="tab.icon === 'compose'" class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 20h4L20 8l-4-4L4 16v4z"/><path d="M14 6l4 4"/></svg>
      <svg v-else-if="tab.icon === 'inbox'" class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M3 7l9 6 9-6"/><rect x="3" y="5" width="18" height="14" rx="2"/></svg>
      <svg v-else-if="tab.icon === 'map'" class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 21s-7-7.5-7-12a7 7 0 0 1 14 0c0 4.5-7 12-7 12z"/><circle cx="12" cy="9" r="2.5"/></svg>
      <svg v-else-if="tab.icon === 'people'" class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="9" cy="8" r="3.5"/><path d="M2 20c0-3.5 3-6 7-6s7 2.5 7 6"/><circle cx="17" cy="7" r="2.5"/><path d="M17 12c3 0 5 2 5 5"/></svg>
      <svg v-else-if="tab.icon === 'radios'" class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 10a9 9 0 0 1 16 0"/><path d="M7 13a5 5 0 0 1 10 0"/><circle cx="12" cy="16" r="1.5"/></svg>
      <span>{{ t(tab.key) }}</span>
    </router-link>
  </nav>
  </Teleport>
</template>

<style scoped>
.no-scrollbar::-webkit-scrollbar { display: none; }
.no-scrollbar { -ms-overflow-style: none; scrollbar-width: none; }
</style>
