import { defineStore } from 'pinia'
import { ref } from 'vue'
import api from '@/api/client'

export const useMeshsatStore = defineStore('meshsat', () => {
  const messages = ref([])
  const messageStats = ref(null)
  const telemetry = ref([])
  const positions = ref([])
  const nodes = ref([])
  const status = ref(null)
  const gateways = ref([])
  const loading = ref(false)
  const error = ref(null)

  let sseHandle = null

  async function fetchMessages(params = {}) {
    try {
      const data = await api.get('/messages', params)
      const list = Array.isArray(data) ? data : (data.messages || data.items || [])
      if (params.offset && params.offset > 0) {
        // Append for "load more"
        const existingIds = new Set(messages.value.map(m => m.id))
        const newMsgs = list.filter(m => !existingIds.has(m.id))
        messages.value = [...messages.value, ...newMsgs]
      } else {
        messages.value = list
      }
      return list
    } catch (e) {
      error.value = e.message
      return []
    }
  }

  async function fetchMessageStats() {
    try {
      messageStats.value = await api.get('/messages/stats')
    } catch (e) {
      error.value = e.message
    }
  }

  async function sendMessage(payload) {
    error.value = null
    try {
      return await api.post('/messages/send', payload)
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function fetchTelemetry(params = {}) {
    try {
      const data = await api.get('/telemetry', params)
      const list = Array.isArray(data) ? data : (data.telemetry || data.items || [])
      telemetry.value = list
      return list
    } catch (e) {
      error.value = e.message
      return []
    }
  }

  async function fetchPositions(params = {}) {
    try {
      const data = await api.get('/positions', params)
      const list = Array.isArray(data) ? data : (data.positions || data.items || [])
      positions.value = list
      return list
    } catch (e) {
      error.value = e.message
      return []
    }
  }

  async function fetchNodes() {
    try {
      const data = await api.get('/nodes')
      const list = Array.isArray(data) ? data : (data.nodes || data.items || [])
      nodes.value = list
      return list
    } catch (e) {
      error.value = e.message
      return []
    }
  }

  async function removeNode(nodeNum) {
    error.value = null
    try {
      await api.del(`/nodes/${nodeNum}`)
      await fetchNodes()
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function purgeMessages(before) {
    error.value = null
    try {
      const result = await api.del(`/messages?before=${encodeURIComponent(before)}`)
      await fetchMessages()
      await fetchMessageStats()
      return result
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function fetchStatus() {
    try {
      status.value = await api.get('/status')
    } catch (e) {
      error.value = e.message
    }
  }

  async function fetchGateways() {
    try {
      const data = await api.get('/gateways')
      const list = Array.isArray(data) ? data : (data.gateways || data.items || [])
      gateways.value = list
    } catch (e) {
      error.value = e.message
    }
  }

  async function configureGateway(gwType, enabled, config) {
    error.value = null
    try {
      const result = await api.put(`/gateways/${gwType}`, { enabled, config })
      await fetchGateways()
      return result
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function deleteGateway(gwType) {
    error.value = null
    try {
      const result = await api.del(`/gateways/${gwType}`)
      await fetchGateways()
      return result
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function startGateway(gwType) {
    error.value = null
    try {
      const result = await api.post(`/gateways/${gwType}/start`)
      await fetchGateways()
      return result
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function stopGateway(gwType) {
    error.value = null
    try {
      const result = await api.post(`/gateways/${gwType}/stop`)
      await fetchGateways()
      return result
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function testGateway(gwType) {
    error.value = null
    try {
      return await api.post(`/gateways/${gwType}/test`)
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function adminReboot(payload) {
    error.value = null
    try {
      return await api.post('/admin/reboot', payload)
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function adminFactoryReset(payload) {
    error.value = null
    try {
      return await api.post('/admin/factory_reset', payload)
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function adminTraceroute(payload) {
    error.value = null
    try {
      return await api.post('/admin/traceroute', payload)
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function configRadio(payload) {
    error.value = null
    try {
      return await api.post('/config/radio', payload)
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function configModule(payload) {
    error.value = null
    try {
      return await api.post('/config/module', payload)
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function sendWaypoint(payload) {
    error.value = null
    try {
      return await api.post('/waypoints', payload)
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  // Forwarding rules
  const rules = ref([])
  async function fetchRules() {
    try {
      const data = await api.get('/rules')
      rules.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }
  async function createRule(rule) {
    error.value = null
    try { const r = await api.post('/rules', rule); await fetchRules(); return r } catch (e) { error.value = e.message; throw e }
  }
  async function updateRule(id, rule) {
    error.value = null
    try { const r = await api.put(`/rules/${id}`, rule); await fetchRules(); return r } catch (e) { error.value = e.message; throw e }
  }
  async function deleteRule(id) {
    error.value = null
    try { await api.del(`/rules/${id}`); await fetchRules() } catch (e) { error.value = e.message; throw e }
  }
  async function enableRule(id) {
    try { await api.post(`/rules/${id}/enable`); await fetchRules() } catch (e) { error.value = e.message }
  }
  async function disableRule(id) {
    try { await api.post(`/rules/${id}/disable`); await fetchRules() } catch (e) { error.value = e.message }
  }
  async function reorderRules(ids) {
    try { await api.post('/rules/reorder', { rule_ids: ids }); await fetchRules() } catch (e) { error.value = e.message }
  }
  async function fetchRuleStats(id) {
    try { return await api.get(`/rules/${id}/stats`) } catch (e) { error.value = e.message; return null }
  }

  // Preset messages
  const presets = ref([])
  async function fetchPresets() {
    try {
      const data = await api.get('/presets')
      presets.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }
  async function createPreset(preset) {
    error.value = null
    try { const r = await api.post('/presets', preset); await fetchPresets(); return r } catch (e) { error.value = e.message; throw e }
  }
  async function updatePreset(id, preset) {
    error.value = null
    try { const r = await api.put(`/presets/${id}`, preset); await fetchPresets(); return r } catch (e) { error.value = e.message; throw e }
  }
  async function deletePreset(id) {
    error.value = null
    try { await api.del(`/presets/${id}`); await fetchPresets() } catch (e) { error.value = e.message; throw e }
  }
  async function sendPreset(id) {
    error.value = null
    try { return await api.post(`/presets/${id}/send`) } catch (e) { error.value = e.message; throw e }
  }

  // SOS
  const sosStatus = ref({ active: false })
  async function activateSOS() {
    error.value = null
    try { sosStatus.value = await api.post('/sos/activate'); return sosStatus.value } catch (e) { error.value = e.message; throw e }
  }
  async function cancelSOS() {
    error.value = null
    try { sosStatus.value = await api.post('/sos/cancel'); return sosStatus.value } catch (e) { error.value = e.message; throw e }
  }
  async function fetchSOSStatus() {
    try { sosStatus.value = await api.get('/sos/status') } catch (e) { error.value = e.message }
  }

  // Iridium DLQ
  const dlq = ref([])
  async function fetchDLQ() {
    try {
      const data = await api.get('/iridium/queue')
      dlq.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  const iridiumSignal = ref(null) // { bars: 0-5, assessment: string, timestamp: string }

  async function fetchIridiumSignalFast() {
    try {
      iridiumSignal.value = await api.get('/iridium/signal/fast')
    } catch {
      // Signal unavailable — keep last known value
    }
  }

  async function fetchIridiumSignal() {
    try {
      iridiumSignal.value = await api.get('/iridium/signal')
    } catch {
      // Signal unavailable — keep last known value
    }
  }

  const config = ref(null)

  async function fetchConfig() {
    try {
      config.value = await api.get('/config')
    } catch (e) {
      error.value = e.message
    }
  }

  async function setChannel(payload) {
    error.value = null
    try {
      return await api.post('/channels', payload)
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  function connectSSE(onEvent) {
    closeSSE()
    sseHandle = api.sse('/events', (event) => {
      if (onEvent) onEvent(event)
    })
  }

  function closeSSE() {
    if (sseHandle) {
      sseHandle.close()
      sseHandle = null
    }
  }

  return {
    messages, messageStats, telemetry, positions, nodes, status, gateways, config,
    iridiumSignal, rules, presets, sosStatus, dlq,
    loading, error,
    fetchMessages, fetchMessageStats, sendMessage, purgeMessages,
    fetchTelemetry, fetchPositions, fetchNodes, removeNode, fetchStatus, fetchGateways,
    configureGateway, deleteGateway, startGateway, stopGateway, testGateway,
    adminReboot, adminFactoryReset, adminTraceroute,
    configRadio, configModule, sendWaypoint,
    fetchConfig, setChannel,
    fetchIridiumSignalFast, fetchIridiumSignal,
    fetchRules, createRule, updateRule, deleteRule, enableRule, disableRule, reorderRules, fetchRuleStats,
    fetchPresets, createPreset, updatePreset, deletePreset, sendPreset,
    activateSOS, cancelSOS, fetchSOSStatus,
    fetchDLQ,
    connectSSE, closeSSE
  }
})
