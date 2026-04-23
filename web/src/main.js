import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import '@fontsource/jetbrains-mono/400.css'
import '@fontsource/jetbrains-mono/500.css'
import '@fontsource/jetbrains-mono/600.css'
import '@fontsource/jetbrains-mono/700.css'
import '@fontsource/oxanium/400.css'
import '@fontsource/oxanium/500.css'
import '@fontsource/oxanium/600.css'
import '@fontsource/oxanium/700.css'
import './style.css'
import { startBundleWatcher } from './bundleWatcher'

// Tag <html> with `shell-kiosk` when we're running on a dedicated
// kiosk device (hides cursor + scrollbars, bumps tap targets).
// Orthogonal from `shell=operator|engineer` which picks the role /
// nav density — a kiosk can show the engineer shell if an admin
// needs the dense surface, the chrome stays kiosk-grade.
//
// Triggers (any of):
//   • ?kiosk=1                (explicit, preferred)
//   • ?shell=kiosk            (legacy; still supported so existing
//                              field kits keep working until their
//                              autostart files are regenerated)
//   • User-agent contains CrKiosk / Chromium-Kiosk / KIOSK
//   • localStorage meshsat.kiosk == '1'  (sticky flag — see below)
//
// Sticky localStorage flag: once a kit has been recognised as a kiosk
// via URL or UA, we stamp `meshsat.kiosk=1` in localStorage. On
// subsequent loads (including bundleWatcher reloads that happen after
// a touch-driven Vue-router navigation stripped the query string —
// e.g. operator taps /compose → URL loses ?kiosk=1 → 4h freshness
// reload loads /compose unadorned → without the sticky flag the
// kit would start rendering cursor + scrollbars). Laptops browsing
// to the bridge from another origin have their own localStorage, so
// this cannot bleed across devices. Clear via
// `localStorage.removeItem('meshsat.kiosk')` in an engineer session.
try {
  const params = new URLSearchParams(window.location.search || '')
  const ua = navigator.userAgent || ''
  let isKiosk = params.get('kiosk') === '1' ||
                params.get('shell') === 'kiosk' ||
                /CrKiosk|Chromium-Kiosk|\bKIOSK\b/i.test(ua)
  if (!isKiosk) {
    try { isKiosk = localStorage.getItem('meshsat.kiosk') === '1' } catch {}
  } else {
    try { localStorage.setItem('meshsat.kiosk', '1') } catch {}
  }
  if (isKiosk) document.documentElement.classList.add('shell-kiosk')
} catch {}

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.mount('#app')

// Self-refreshing kiosk: poll / every 30 s; reload when the bundle hash
// changes. Independent 4 h freshness timer unconditionally reloads to
// clear accumulated renderer state (V8 heap fragmentation + listener
// leaks degrade main-thread scheduling over long uptimes). Override via
// ?bundleWatchMs=N + ?bundleFreshMs=N (0 on either disables).
// [MESHSAT-621 + MESHSAT-628]
try {
  const params = new URLSearchParams(window.location.search || '')
  const watchOverride = params.get('bundleWatchMs')
  const freshOverride = params.get('bundleFreshMs')
  const watchMs = watchOverride !== null ? Number(watchOverride) : 30_000
  const freshMs = freshOverride !== null ? Number(freshOverride) : 4 * 60 * 60 * 1000
  if (Number.isFinite(watchMs)) startBundleWatcher(watchMs, Number.isFinite(freshMs) ? freshMs : 0)
} catch {}
