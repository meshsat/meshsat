// bundleWatcher.js — poll the bridge's / index.html every 30 s, compare the
// hashed asset filename embedded in the <script type=module> tag against the
// one we booted with.  If the hash changes, the bridge has been upgraded and
// we reload the page so the kiosk picks up the new bundle without a manual
// refresh.  Field-kit kiosks don't have a keyboard so this is the only way
// deployed SPA changes become visible.
//
// Why this shape rather than `<meta http-equiv=refresh>`:
//   • We ONLY reload when there is actually new code, not every N seconds —
//     preserves SSE subscriptions, scroll position, and half-typed input.
//   • We fetch just `/` (a few KB of HTML) not the full bundle, so the poll
//     cost is negligible.
//
// Override interval or disable entirely from the URL:
//   ?bundleWatchMs=60000   lengthen the poll interval
//   ?bundleWatchMs=0       disable the watcher
//
// [MESHSAT-621 — self-refreshing kiosk]

const DEFAULT_INTERVAL_MS = 30_000

function currentBundleHash() {
  // Vite emits <script type="module" crossorigin src="/assets/index-XXXXX.js">
  // and every build produces a different hash segment. We snapshot our own.
  const scripts = document.querySelectorAll('script[src*="/assets/index-"]')
  for (const s of scripts) {
    const m = s.getAttribute('src')?.match(/\/assets\/index-([^.]+)\.js/)
    if (m) return m[1]
  }
  return null
}

async function fetchRemoteBundleHash() {
  try {
    const res = await fetch('/?_bw=' + Date.now(), { cache: 'no-store' })
    if (!res.ok) return null
    const html = await res.text()
    const m = html.match(/\/assets\/index-([^."]+)\.js/)
    return m ? m[1] : null
  } catch {
    return null
  }
}

export function startBundleWatcher(intervalMs = DEFAULT_INTERVAL_MS) {
  const initial = currentBundleHash()
  if (!initial) {
    // Dev mode (no hashed asset) or inlined script — nothing to watch.
    return
  }
  if (intervalMs <= 0) return

  const tick = async () => {
    const remote = await fetchRemoteBundleHash()
    if (remote && remote !== initial) {
      // eslint-disable-next-line no-console
      console.log('[bundleWatcher] SPA bundle hash changed (%s → %s) — reloading', initial, remote)
      window.location.reload()
    }
  }
  // First check after one interval so we don't reload on initial load
  // if the server raced us.
  setInterval(tick, intervalMs)
}
