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

// Tag <html> with `shell-kiosk` when the page was loaded with the
// ?shell=kiosk URL param OR matches the kiosk UA. CSS rules under
// html.shell-kiosk hide the mouse cursor + scrollbars, which
// Chromium on Linux doesn't do via `pointer: coarse` even with a
// DSI touchscreen present.
try {
  const params = new URLSearchParams(window.location.search || '')
  const ua = navigator.userAgent || ''
  const isKiosk = params.get('shell') === 'kiosk' ||
                  /CrKiosk|Chromium-Kiosk|\bKIOSK\b/i.test(ua)
  if (isKiosk) document.documentElement.classList.add('shell-kiosk')
} catch {}

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.mount('#app')
