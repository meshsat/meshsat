import { ref, computed, onMounted, onUnmounted } from 'vue'

// Generic virtual scroll composable for fixed-height rows.
// Renders only visible rows + overscan, uses padding divs for scroll height.
export function useVirtualScroll(items, rowHeight = 36, containerRef = null) {
  const scrollTop = ref(0)
  const containerHeight = ref(600)
  const overscan = 5

  const visibleRange = computed(() => {
    const start = Math.max(0, Math.floor(scrollTop.value / rowHeight) - overscan)
    const visible = Math.ceil(containerHeight.value / rowHeight) + overscan * 2
    const end = Math.min(items.value.length, start + visible)
    return { start, end }
  })

  const visibleItems = computed(() =>
    items.value.slice(visibleRange.value.start, visibleRange.value.end)
  )

  const topPadding = computed(() => visibleRange.value.start * rowHeight)
  const bottomPadding = computed(() =>
    Math.max(0, (items.value.length - visibleRange.value.end) * rowHeight)
  )

  function onScroll(e) {
    scrollTop.value = e.target.scrollTop
  }

  let resizeObserver = null

  onMounted(() => {
    if (containerRef?.value) {
      containerHeight.value = containerRef.value.clientHeight
      resizeObserver = new ResizeObserver((entries) => {
        for (const entry of entries) {
          containerHeight.value = entry.contentRect.height
        }
      })
      resizeObserver.observe(containerRef.value)
    }
  })

  onUnmounted(() => {
    if (resizeObserver) {
      resizeObserver.disconnect()
    }
  })

  return { visibleItems, topPadding, bottomPadding, onScroll, visibleRange }
}
