import { onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { useMeshsatStore } from '@/stores/meshsat'

// Engineer-mode keyboard shortcuts. [MESHSAT-558]
//
// Grammar (Gmail / Linear / GitHub style):
//   g c       →  /compose
//   g i       →  /inbox
//   g m       →  /map
//   g p       →  /people
//   g r       →  /radios
//   n         →  /compose (quick New)
//   /         →  focus the first search input (or fall back to no-op)
//   Esc       →  blur current focus
//
// Gating: only fires when store.isEngineer is true, so Operator Mode
// is never affected. Also suppressed while the event target is an
// editable element (input, textarea, contenteditable, select) so
// these keys don't fight with regular typing.
//
// Spec calls for `.ts`; the SPA is JS throughout so we land it as
// .js for consistency with the surrounding composables — rename
// when the project moves to TypeScript.

const PREFIX_TIMEOUT_MS = 800

function isEditable(target) {
  if (!target || target.nodeType !== 1) return false
  const tag = target.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return true
  if (target.isContentEditable) return true
  // Any ancestor contenteditable also counts.
  let n = target
  while (n && n !== document.body) {
    if (n.isContentEditable) return true
    n = n.parentElement
  }
  return false
}

export function useShortcuts() {
  const router = useRouter()
  const store = useMeshsatStore()

  let awaitingG = false
  let gTimer = null

  function clearG() {
    awaitingG = false
    if (gTimer) { clearTimeout(gTimer); gTimer = null }
  }

  function go(path) {
    clearG()
    router.push(path)
  }

  function focusFirstSearch() {
    const el = document.querySelector('input[type="search"], input[placeholder*="earch" i], input[placeholder*="ype" i]')
    if (el && typeof el.focus === 'function') el.focus()
  }

  function onKey(ev) {
    // Engineer-only — zero-conflict guarantee with Operator Mode.
    if (!store.isEngineer) return
    // Don't fight with modifier-keyed browser shortcuts.
    if (ev.ctrlKey || ev.metaKey || ev.altKey) return
    // Don't fire while typing into a field.
    if (isEditable(ev.target)) {
      // Exception: Esc should still blur the field so operators can
      // escape a modal even from an input.
      if (ev.key === 'Escape' && typeof ev.target.blur === 'function') ev.target.blur()
      return
    }

    const k = ev.key

    // Two-key go-to grammar.
    if (awaitingG) {
      switch (k) {
        case 'c': go('/compose'); ev.preventDefault(); return
        case 'i': go('/inbox');   ev.preventDefault(); return
        case 'm': go('/map');     ev.preventDefault(); return
        case 'p': go('/people');  ev.preventDefault(); return
        case 'r': go('/radios');  ev.preventDefault(); return
        default:  clearG(); return
      }
    }

    switch (k) {
      case 'g':
        awaitingG = true
        gTimer = setTimeout(clearG, PREFIX_TIMEOUT_MS)
        ev.preventDefault()
        return
      case 'n':
        router.push('/compose')
        ev.preventDefault()
        return
      case '/':
        focusFirstSearch()
        ev.preventDefault()
        return
      case 'Escape':
        if (document.activeElement && typeof document.activeElement.blur === 'function') {
          document.activeElement.blur()
        }
        return
    }
  }

  onMounted(() => document.addEventListener('keydown', onKey))
  onUnmounted(() => {
    document.removeEventListener('keydown', onKey)
    clearG()
  })
}
