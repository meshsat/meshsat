import { ref, computed } from 'vue'
import api from '../api/client'

// Isolated HeMB store — lazy-loaded only when /hemb route is navigated to.
const events = ref([])
const topology = ref(null)
const stats = ref(null)
const sseHandle = ref(null)
const connected = ref(false)
const paused = ref(false)
const pendingQueue = ref([])

const MAX_EVENTS = 500

export function useHeMBStore() {
  function connectSSE(filters = {}) {
    closeSSE()
    const params = new URLSearchParams()
    if (filters.event_type) params.set('event_type', filters.event_type)
    if (filters.stream_id) params.set('stream_id', filters.stream_id)
    params.set('replay', '50')
    const qs = params.toString()
    const path = `/hemb/events${qs ? '?' + qs : ''}`

    sseHandle.value = api.sse(path, (event) => {
      if (event.type === 'hemb_connected') {
        connected.value = true
        return
      }
      if (paused.value) {
        pendingQueue.value.push(event)
        if (pendingQueue.value.length > MAX_EVENTS) {
          pendingQueue.value.shift()
        }
        return
      }
      events.value.unshift(event)
      if (events.value.length > MAX_EVENTS) {
        events.value.pop()
      }
    })
    connected.value = true
  }

  function closeSSE() {
    if (sseHandle.value) {
      sseHandle.value.close()
      sseHandle.value = null
    }
    connected.value = false
  }

  function togglePause() {
    if (paused.value) {
      // Resume: flush pending into main list.
      for (const evt of pendingQueue.value) {
        events.value.unshift(evt)
      }
      while (events.value.length > MAX_EVENTS) events.value.pop()
      pendingQueue.value = []
    }
    paused.value = !paused.value
  }

  async function fetchTopology() {
    try {
      topology.value = await api.get('/hemb/topology')
    } catch (e) {
      console.warn('HeMB topology fetch failed:', e.message)
    }
  }

  async function fetchStats() {
    try {
      stats.value = await api.get('/hemb/stats')
    } catch (e) {
      console.warn('HeMB stats fetch failed:', e.message)
    }
  }

  async function fetchHistory(limit = 100) {
    try {
      const data = await api.get('/hemb/events/history', { limit })
      if (data && data.events) {
        events.value = data.events.reverse()
      }
    } catch (e) {
      console.warn('HeMB history fetch failed:', e.message)
    }
  }

  const pendingCount = computed(() => pendingQueue.value.length)

  return {
    events,
    topology,
    stats,
    connected,
    paused,
    pendingCount,
    connectSSE,
    closeSSE,
    togglePause,
    fetchTopology,
    fetchStats,
    fetchHistory,
  }
}
