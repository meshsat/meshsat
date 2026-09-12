import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  { path: '/', name: 'dashboard', component: () => import('@/views/DashboardView.vue') },
  { path: '/compose', name: 'compose', component: () => import('@/views/ComposeView.vue') },
  { path: '/inbox', name: 'inbox', component: () => import('@/views/InboxView.vue') },
  { path: '/people', name: 'people', component: () => import('@/views/PeopleView.vue') },
  { path: '/radios', name: 'radios', component: () => import('@/views/RadiosView.vue') },
  { path: '/pair/scan', name: 'pair-scan', component: () => import('@/views/PairScanView.vue') },
  { path: '/messages', name: 'messages', component: () => import('@/views/MessagesView.vue') },
  { path: '/nodes', name: 'nodes', component: () => import('@/views/NodesView.vue') },
  { path: '/bridge', name: 'bridge', component: () => import('@/views/BridgeView.vue') },
  { path: '/interfaces', name: 'interfaces', component: () => import('@/views/InterfacesView.vue') },
  { path: '/passes', name: 'passes', component: () => import('@/views/PassesView.vue') },
  { path: '/topology', name: 'topology', component: () => import('@/views/TopologyView.vue') },
  { path: '/map', name: 'map', component: () => import('@/views/MapView.vue') },
  { path: '/settings', name: 'settings', component: () => import('@/views/SettingsView.vue') },
  { path: '/radio', name: 'radio', component: () => import('@/views/RadioConfigView.vue') },
  { path: '/tak', name: 'tak', component: () => import('@/views/TakView.vue') },
  { path: '/ttc', name: 'ttc', component: () => import('@/views/TtcView.vue') },
  { path: '/spectrum', name: 'spectrum', component: () => import('@/views/SpectrumView.vue') },
  { path: '/spectrum/:band', name: 'spectrum-band', component: () => import('@/views/SpectrumBandDetailView.vue'), props: true },
  { path: '/zigbee', name: 'zigbee', component: () => import('@/views/ZigBeeDevicesView.vue') },
  { path: '/zigbee/:addr', name: 'zigbee-device', component: () => import('@/views/ZigBeeDeviceDetailView.vue'), props: true },
  { path: '/audit', name: 'audit', component: () => import('@/views/AuditView.vue') },
  { path: '/help', name: 'help', component: () => import('@/views/HelpView.vue') },
  { path: '/about', name: 'about', component: () => import('@/views/AboutView.vue') },
  { path: '/:pathMatch(.*)*', redirect: '/' }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

// Remember which screen a kiosk was left on, and come back to it. [MESHSAT-993]
//
// Chromium on a field kit is launched with a fixed URL, so every relaunch — the
// nightly recycle, a crash respawn, the soft-launch watcher — returns to that
// URL's path. A kit tapped into TTC MODE therefore dropped back to the operator
// dashboard minutes after the morning power-on, in front of whoever was at the
// stand. The bundle-watcher reloads never had this problem because they reload
// the current document; only a process relaunch loses the route.
//
// Only the PATH is remembered. The shell (operator or engineer) still comes
// from the launch URL on every load, which MESHSAT-659 requires: a kit must come
// back to the role its autostart names, whatever anyone tapped.
const KIOSK_ROUTE_KEY = 'meshsat.kioskRoute'
// Long enough to cover a night switched off, short enough that a kit found in a
// drawer months later starts at its configured screen.
const KIOSK_ROUTE_MAX_AGE_MS = 36 * 60 * 60 * 1000

function isKioskSession () {
  try {
    if (document.documentElement.classList.contains('shell-kiosk')) return true
    return localStorage.getItem('meshsat.kiosk') === '1'
  } catch {
    return false
  }
}

let restoreChecked = false

router.beforeEach((to) => {
  // Only the first navigation of the session can restore, and only from the
  // landing path: once anyone has navigated, their choice wins.
  if (restoreChecked) return true
  restoreChecked = true
  if (!isKioskSession() || to.path !== '/') return true

  let saved
  try {
    saved = JSON.parse(localStorage.getItem(KIOSK_ROUTE_KEY) || 'null')
  } catch {
    return true
  }
  if (!saved || typeof saved.path !== 'string' || saved.path === '/') return true
  if (!saved.at || Date.now() - saved.at > KIOSK_ROUTE_MAX_AGE_MS) return true
  if (!saved.path.startsWith('/') || saved.path.startsWith('//')) return true

  // Keep the query: it carries kiosk=1 and shell=, which must survive.
  return { path: saved.path, query: to.query, replace: true }
})

router.afterEach((to) => {
  if (!isKioskSession()) return
  try {
    localStorage.setItem(KIOSK_ROUTE_KEY, JSON.stringify({ path: to.path, at: Date.now() }))
  } catch { /* private mode, or storage full: the kit just loses the memory */ }
})

export default router
