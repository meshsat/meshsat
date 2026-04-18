import { computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import operator from './operator.en.json'
import engineer from './engineer.en.json'

// IQ-70 label composable. [MESHSAT-557]
//
// Usage:
//   const { t, tooltip } = useLabel()
//   t('nav.inbox')         → "Inbox" (operator) / "Comms" (engineer)
//   tooltip('nav.inbox')   → "" (operator) / "Comms" (engineer, for
//                            hover-reveal on an already-operator-
//                            labelled surface).
//
// Keys that are missing from the active dictionary fall back to the
// other dictionary, and finally to the literal key. That lets views
// migrate incrementally without throwing on an untranslated string.

const dicts = { operator, engineer }

function pick(mode, key) {
  const primary = dicts[mode] || dicts.operator
  if (primary[key] !== undefined) return primary[key]
  const fallback = mode === 'operator' ? dicts.engineer : dicts.operator
  if (fallback[key] !== undefined) return fallback[key]
  return key
}

export function useLabel() {
  const store = useMeshsatStore()
  function t(key) {
    return pick(store.shellMode, key)
  }
  // Engineer term for hover-reveal. Returns "" when the two
  // dictionaries agree so we don't render a noise-tooltip.
  function tooltip(key) {
    if (!store.isOperator) return ''
    const eng = dicts.engineer[key]
    const op  = dicts.operator[key]
    if (!eng || eng === op) return ''
    return eng
  }
  return {
    t,
    tooltip,
    isOperator: computed(() => store.isOperator),
    isEngineer: computed(() => store.isEngineer)
  }
}

// Plain (non-reactive) lookups for components that don't want to
// import the composable — e.g. static label maps.
export function label(mode, key) { return pick(mode, key) }
