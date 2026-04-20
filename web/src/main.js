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
try {
  const params = new URLSearchParams(window.location.search || '')
  const ua = navigator.userAgent || ''
  const isKiosk = params.get('kiosk') === '1' ||
                  params.get('shell') === 'kiosk' ||
                  /CrKiosk|Chromium-Kiosk|\bKIOSK\b/i.test(ua)
  if (isKiosk) document.documentElement.classList.add('shell-kiosk')
} catch {}

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.mount('#app')

// Self-refreshing kiosk: poll / every 30 s; reload when the bundle hash
// changes.  Override via ?bundleWatchMs=N (0 disables).  [MESHSAT-621]
try {
  const params = new URLSearchParams(window.location.search || '')
  const override = params.get('bundleWatchMs')
  const ms = override !== null ? Number(override) : 30_000
  if (Number.isFinite(ms)) startBundleWatcher(ms)
} catch {}
