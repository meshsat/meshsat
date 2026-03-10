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
  const sseConnected = ref(false)

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
      const items = data?.queue ?? data
      dlq.value = Array.isArray(items) ? items : []
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

  // Iridium signal history, credits, passes, locations
  const signalHistory = ref([])
  const gssHistory = ref([])
  const creditSummary = ref(null)
  const passes = ref([])
  const locations = ref([])

  async function fetchSignalHistory(params = {}) {
    try {
      const data = await api.get('/iridium/signal/history', params)
      signalHistory.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  async function fetchGSSHistory(params = {}) {
    try {
      const data = await api.get('/iridium/signal/history', { ...params, source: 'gss' })
      gssHistory.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  async function fetchCredits() {
    try {
      creditSummary.value = await api.get('/iridium/credits')
    } catch (e) { error.value = e.message }
  }

  async function setCreditBudget(daily, monthly) {
    error.value = null
    try {
      await api.post('/iridium/credits/budget', { daily_budget: daily, monthly_budget: monthly })
      await fetchCredits()
    } catch (e) { error.value = e.message; throw e }
  }

  async function fetchPasses(params = {}) {
    try {
      const data = await api.get('/iridium/passes', params)
      passes.value = data?.passes || []
      return data
    } catch (e) { error.value = e.message; return null }
  }

  async function refreshTLEs() {
    error.value = null
    try {
      return await api.post('/iridium/passes/refresh')
    } catch (e) { error.value = e.message; throw e }
  }

  async function fetchLocations() {
    try {
      const data = await api.get('/iridium/locations')
      locations.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  async function createLocation(loc) {
    error.value = null
    try {
      const r = await api.post('/iridium/locations', loc)
      await fetchLocations()
      return r
    } catch (e) { error.value = e.message; throw e }
  }

  async function deleteLocation(id) {
    error.value = null
    try {
      await api.del(`/iridium/locations/${id}`)
      await fetchLocations()
    } catch (e) { error.value = e.message; throw e }
  }

  async function enqueueIridiumMessage(message, priority = 1) {
    error.value = null
    try {
      return await api.post('/iridium/queue', { message, priority })
    } catch (e) { error.value = e.message; throw e }
  }

  async function cancelQueueItem(id) {
    error.value = null
    try {
      await api.post(`/iridium/queue/${id}/cancel`)
      await fetchDLQ()
    } catch (e) { error.value = e.message; throw e }
  }

  async function deleteQueueItem(id) {
    error.value = null
    try {
      await api.del(`/iridium/queue/${id}`)
      await fetchDLQ()
    } catch (e) { error.value = e.message; throw e }
  }

  async function setQueuePriority(id, priority) {
    error.value = null
    try {
      await api.post(`/iridium/queue/${id}/priority`, { priority })
      await fetchDLQ()
    } catch (e) { error.value = e.message; throw e }
  }

  // Location sources (GPS, Custom) + AUTO resolution
  const locationSources = ref(null) // { sources: [...], resolved: { source, lat, lon, ... }, iridium_passes: [...], iridium_centroid: {...} }

  async function fetchLocationSources() {
    try {
      locationSources.value = await api.get('/locations/resolved')
    } catch { /* location sources unavailable */ }
  }

  // Iridium geolocation history (satellite sub-points for multi-pass viz)
  const iridiumGeoHistory = ref(null)

  async function fetchIridiumGeoHistory(hours = 6) {
    try {
      iridiumGeoHistory.value = await api.get('/iridium/geolocation/history', { hours })
    } catch { /* iridium geo unavailable */ }
  }

  // SMS message history
  const smsMessages = ref([])

  async function fetchSMSMessages(params = {}) {
    try {
      smsMessages.value = await api.get('/cellular/sms', params)
    } catch { /* sms unavailable */ }
  }

  // Cell broadcast alerts
  const cellBroadcasts = ref([])

  async function fetchCellBroadcasts(params = {}) {
    try {
      cellBroadcasts.value = await api.get('/cellular/broadcasts', params)
    } catch { /* cbs unavailable */ }
  }

  async function ackCellBroadcast(id) {
    try {
      await api.post(`/cellular/broadcasts/${id}/ack`)
      await fetchCellBroadcasts({ limit: 20 })
    } catch { /* ack failed */ }
  }

  // Cell tower info
  const cellInfo = ref(null)

  async function fetchCellInfo() {
    try {
      cellInfo.value = await api.get('/cellular/info')
    } catch { /* cell info unavailable */ }
  }

  // SIM PIN unlock
  async function submitCellularPIN(pin) {
    error.value = null
    try {
      const result = await api.post('/cellular/pin', { pin })
      await fetchCellularStatus()
      return result
    } catch (e) { error.value = e.message; throw e }
  }

  // Astrocast LEO passes
  const astrocastPasses = ref([])

  async function fetchAstrocastPasses(params = {}) {
    try {
      const data = await api.get('/astrocast/passes', params)
      astrocastPasses.value = data?.passes || []
      return data
    } catch (e) { error.value = e.message; return null }
  }

  async function refreshAstrocastTLEs() {
    error.value = null
    try {
      return await api.post('/astrocast/passes/refresh')
    } catch (e) { error.value = e.message; throw e }
  }

  // Iridium scheduler status
  const schedulerStatus = ref(null)

  async function fetchSchedulerStatus() {
    try {
      schedulerStatus.value = await api.get('/iridium/scheduler')
    } catch { /* scheduler unavailable */ }
  }

  async function manualMailboxCheck() {
    error.value = null
    try {
      return await api.post('/iridium/mailbox/check')
    } catch (e) { error.value = e.message; throw e }
  }

  // Cellular modem
  const cellularSignal = ref(null)
  const cellularStatus = ref(null)
  const cellularSignalHistory = ref([])
  const cellularDataStatus = ref(null)
  const dyndnsStatus = ref(null)

  async function fetchCellularSignal() {
    try {
      cellularSignal.value = await api.get('/cellular/signal')
    } catch { /* cellular unavailable */ }
  }

  async function fetchCellularStatus() {
    try {
      cellularStatus.value = await api.get('/cellular/status')
    } catch { /* cellular unavailable */ }
  }

  async function fetchCellularSignalHistory(params = {}) {
    try {
      const data = await api.get('/cellular/signal/history', params)
      cellularSignalHistory.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  async function fetchCellularDataStatus() {
    try {
      cellularDataStatus.value = await api.get('/cellular/data/status')
    } catch { /* data status unavailable */ }
  }

  async function connectCellularData(apn) {
    error.value = null
    try {
      await api.post('/cellular/data/connect', { apn })
      await fetchCellularDataStatus()
    } catch (e) { error.value = e.message; throw e }
  }

  async function disconnectCellularData() {
    error.value = null
    try {
      await api.post('/cellular/data/disconnect')
      await fetchCellularDataStatus()
    } catch (e) { error.value = e.message; throw e }
  }

  async function fetchDynDNSStatus() {
    try {
      dyndnsStatus.value = await api.get('/cellular/dyndns/status')
    } catch { /* dyndns unavailable */ }
  }

  async function forceDynDNSUpdate() {
    error.value = null
    try {
      dyndnsStatus.value = await api.post('/cellular/dyndns/update')
    } catch (e) { error.value = e.message; throw e }
  }

  // Neighbor info
  const neighborInfo = ref([])
  async function fetchNeighborInfo() {
    try {
      const data = await api.get('/neighbors')
      neighborInfo.value = data?.neighbors || []
    } catch (e) { error.value = e.message }
  }

  // Range test
  const rangeTests = ref([])
  async function sendRangeTest(payload = {}) {
    error.value = null
    try { return await api.post('/range-test/send', payload) } catch (e) { error.value = e.message; throw e }
  }
  async function fetchRangeTests(params = {}) {
    try {
      const data = await api.get('/range-test', params)
      rangeTests.value = data?.range_tests || []
    } catch (e) { error.value = e.message }
  }

  // Position sharing
  async function sendPosition(payload) {
    error.value = null
    try { return await api.post('/position/send', payload) } catch (e) { error.value = e.message; throw e }
  }
  async function setFixedPosition(payload) {
    error.value = null
    try { return await api.post('/position/fixed', payload) } catch (e) { error.value = e.message; throw e }
  }
  async function removeFixedPosition() {
    error.value = null
    try { return await api.del('/position/fixed') } catch (e) { error.value = e.message; throw e }
  }

  // Store & Forward
  async function requestStoreForward(payload) {
    error.value = null
    try { return await api.post('/store-forward/request', payload) } catch (e) { error.value = e.message; throw e }
  }

  // Canned messages
  async function getCannedMessages() {
    error.value = null
    try { return await api.get('/canned-messages') } catch (e) { error.value = e.message; throw e }
  }
  async function setCannedMessages(messages) {
    error.value = null
    try { return await api.post('/canned-messages', { messages }) } catch (e) { error.value = e.message; throw e }
  }

  // Config section read
  async function fetchConfigSection(section) {
    error.value = null
    try { return await api.get(`/config/${section}`) } catch (e) { error.value = e.message; throw e }
  }
  async function fetchModuleConfigSection(section) {
    error.value = null
    try { return await api.get(`/config/module/${section}`) } catch (e) { error.value = e.message; throw e }
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
    sseConnected.value = true
  }

  function closeSSE() {
    if (sseHandle) {
      sseHandle.close()
      sseHandle = null
    }
    sseConnected.value = false
  }

  // SMS Contacts
  const smsContacts = ref([])

  async function fetchSMSContacts() {
    try {
      smsContacts.value = await api.get('/cellular/contacts')
    } catch (e) {
      error.value = e.message
    }
  }

  async function createSMSContact(payload) {
    error.value = null
    try {
      const res = await api.post('/cellular/contacts', payload)
      await fetchSMSContacts()
      return res
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function updateSMSContact(id, payload) {
    error.value = null
    try {
      await api.put(`/cellular/contacts/${id}`, payload)
      await fetchSMSContacts()
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function deleteSMSContact(id) {
    error.value = null
    try {
      await api.del(`/cellular/contacts/${id}`)
      await fetchSMSContacts()
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  async function sendSMS(to, text) {
    error.value = null
    try {
      return await api.post('/cellular/sms/send', { to, text })
    } catch (e) {
      error.value = e.message
      throw e
    }
  }

  // Delivery ledger (v0.2.0)
  const deliveries = ref([])
  const deliveryStats = ref([])

  async function fetchDeliveries(params = {}) {
    try {
      const data = await api.get('/deliveries', params)
      deliveries.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  async function fetchDeliveryStats() {
    try {
      const data = await api.get('/deliveries/stats')
      deliveryStats.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  async function cancelDelivery(id) {
    error.value = null
    try {
      await api.post(`/deliveries/${id}/cancel`)
      await fetchDeliveries()
      await fetchDeliveryStats()
    } catch (e) { error.value = e.message; throw e }
  }

  async function retryDelivery(id) {
    error.value = null
    try {
      await api.post(`/deliveries/${id}/retry`)
      await fetchDeliveries()
      await fetchDeliveryStats()
    } catch (e) { error.value = e.message; throw e }
  }

  async function fetchMessageDeliveries(msgRef) {
    try {
      return await api.get(`/deliveries/message/${msgRef}`)
    } catch (e) { error.value = e.message; return [] }
  }

  // Transport channels (v0.2.0)
  const transportChannels = ref([])

  async function fetchTransportChannels() {
    try {
      const data = await api.get('/transport/channels')
      transportChannels.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  // Webhook Log
  const webhookLog = ref([])

  async function fetchWebhookLog(limit = 100) {
    try {
      webhookLog.value = await api.get('/webhooks/log', { limit })
    } catch (e) {
      error.value = e.message
    }
  }

  // Interfaces (v0.3.0)
  const interfaces = ref([])
  const devices = ref([])
  const accessRules = ref([])
  const objectGroups = ref([])
  const failoverGroups = ref([])

  async function fetchInterfaces() {
    try {
      const data = await api.get('/interfaces')
      interfaces.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  async function createInterface(iface) {
    error.value = null
    try {
      const r = await api.post('/interfaces', iface)
      await fetchInterfaces()
      return r
    } catch (e) { error.value = e.message; throw e }
  }

  async function updateInterface(id, iface) {
    error.value = null
    try {
      const r = await api.put(`/interfaces/${id}`, iface)
      await fetchInterfaces()
      return r
    } catch (e) { error.value = e.message; throw e }
  }

  async function deleteInterface(id) {
    error.value = null
    try {
      await api.del(`/interfaces/${id}`)
      await fetchInterfaces()
    } catch (e) { error.value = e.message; throw e }
  }

  async function bindDevice(ifaceId, deviceId) {
    error.value = null
    try {
      const r = await api.post(`/interfaces/${ifaceId}/bind`, { device_id: deviceId })
      await fetchInterfaces()
      await fetchDevices()
      return r
    } catch (e) { error.value = e.message; throw e }
  }

  async function unbindDevice(ifaceId) {
    error.value = null
    try {
      const r = await api.post(`/interfaces/${ifaceId}/unbind`)
      await fetchInterfaces()
      await fetchDevices()
      return r
    } catch (e) { error.value = e.message; throw e }
  }

  async function generateEncryptionKey() {
    error.value = null
    try {
      return await api.post('/crypto/generate-key')
    } catch (e) { error.value = e.message; throw e }
  }

  // Devices (v0.3.0)
  async function fetchDevices() {
    try {
      const data = await api.get('/devices')
      devices.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  // Access Rules (v0.3.0)
  async function fetchAccessRules() {
    try {
      const data = await api.get('/access-rules')
      accessRules.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  async function createAccessRule(rule) {
    error.value = null
    try {
      const r = await api.post('/access-rules', rule)
      await fetchAccessRules()
      return r
    } catch (e) { error.value = e.message; throw e }
  }

  async function updateAccessRule(id, rule) {
    error.value = null
    try {
      const r = await api.put(`/access-rules/${id}`, rule)
      await fetchAccessRules()
      return r
    } catch (e) { error.value = e.message; throw e }
  }

  async function deleteAccessRule(id) {
    error.value = null
    try {
      await api.del(`/access-rules/${id}`)
      await fetchAccessRules()
    } catch (e) { error.value = e.message; throw e }
  }

  // Object Groups (v0.3.0)
  async function fetchObjectGroups() {
    try {
      const data = await api.get('/object-groups')
      objectGroups.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  async function createObjectGroup(group) {
    error.value = null
    try {
      const r = await api.post('/object-groups', group)
      await fetchObjectGroups()
      return r
    } catch (e) { error.value = e.message; throw e }
  }

  async function updateObjectGroup(id, group) {
    error.value = null
    try {
      const r = await api.put(`/object-groups/${id}`, group)
      await fetchObjectGroups()
      return r
    } catch (e) { error.value = e.message; throw e }
  }

  async function deleteObjectGroup(id) {
    error.value = null
    try {
      await api.del(`/object-groups/${id}`)
      await fetchObjectGroups()
    } catch (e) { error.value = e.message; throw e }
  }

  // Audit Log (v0.3.0)
  const auditLog = ref([])
  const auditSigner = ref(null)

  async function fetchAuditLog(params = {}) {
    try {
      const data = await api.get('/audit', params)
      auditLog.value = Array.isArray(data) ? data : (data?.entries || [])
    } catch (e) { error.value = e.message }
  }

  async function verifyAuditLog() {
    error.value = null
    try {
      return await api.get('/audit/verify')
    } catch (e) { error.value = e.message; throw e }
  }

  async function fetchAuditSigner() {
    try {
      auditSigner.value = await api.get('/audit/signer')
    } catch (e) { error.value = e.message }
  }

  // Config Export/Import (v0.3.0)
  async function exportConfig() {
    error.value = null
    try {
      return await api.get('/config/export')
    } catch (e) { error.value = e.message; throw e }
  }

  async function importConfig(yamlContent) {
    error.value = null
    try {
      return await api.post('/config/import', { content: yamlContent })
    } catch (e) { error.value = e.message; throw e }
  }

  // Transform validation (v0.3.0)
  async function validateTransforms(transforms, channelType) {
    error.value = null
    try {
      return await api.post('/crypto/validate-transforms', { transforms, channel_type: channelType })
    } catch (e) { error.value = e.message; throw e }
  }

  // Iridium geolocation trigger (v0.3.0)
  const iridiumGeolocation = ref(null)

  async function triggerIridiumGeolocation() {
    error.value = null
    try {
      iridiumGeolocation.value = await api.get('/iridium/geolocation')
      return iridiumGeolocation.value
    } catch (e) { error.value = e.message; throw e }
  }

  // Failover Groups (v0.3.0)
  async function fetchFailoverGroups() {
    try {
      const data = await api.get('/failover-groups')
      failoverGroups.value = Array.isArray(data) ? data : []
    } catch (e) { error.value = e.message }
  }

  async function createFailoverGroup(group) {
    error.value = null
    try {
      const r = await api.post('/failover-groups', group)
      await fetchFailoverGroups()
      return r
    } catch (e) { error.value = e.message; throw e }
  }

  async function deleteFailoverGroup(id) {
    error.value = null
    try {
      await api.del(`/failover-groups/${id}`)
      await fetchFailoverGroups()
    } catch (e) { error.value = e.message; throw e }
  }

  return {
    messages, messageStats, telemetry, positions, nodes, status, gateways, config, neighborInfo, rangeTests,
    iridiumSignal, signalHistory, gssHistory, creditSummary, passes, locations, schedulerStatus, astrocastPasses,
    locationSources, iridiumGeoHistory,
    cellularSignal, cellularStatus, cellularSignalHistory, cellularDataStatus, dyndnsStatus,
    smsMessages, cellBroadcasts, cellInfo,
    rules, presets, sosStatus, dlq,
    loading, error,
    fetchMessages, fetchMessageStats, sendMessage, purgeMessages,
    fetchTelemetry, fetchPositions, fetchNodes, removeNode, fetchStatus, fetchGateways,
    configureGateway, deleteGateway, startGateway, stopGateway, testGateway,
    adminReboot, adminFactoryReset, adminTraceroute,
    configRadio, configModule, sendWaypoint,
    fetchConfig, setChannel, fetchConfigSection, fetchModuleConfigSection,
    fetchNeighborInfo, sendRangeTest, fetchRangeTests,
    sendPosition, setFixedPosition, removeFixedPosition,
    requestStoreForward, getCannedMessages, setCannedMessages,
    fetchIridiumSignalFast, fetchIridiumSignal,
    fetchSignalHistory, fetchGSSHistory, fetchCredits, setCreditBudget, fetchSchedulerStatus,
    fetchPasses, refreshTLEs, fetchLocations, createLocation, deleteLocation, manualMailboxCheck, fetchAstrocastPasses, refreshAstrocastTLEs,
    fetchLocationSources, fetchIridiumGeoHistory,
    fetchCellularSignal, fetchCellularStatus, fetchCellularSignalHistory,
    fetchCellularDataStatus, connectCellularData, disconnectCellularData,
    fetchDynDNSStatus, forceDynDNSUpdate,
    fetchSMSMessages, fetchCellBroadcasts, ackCellBroadcast, fetchCellInfo, submitCellularPIN,
    enqueueIridiumMessage, cancelQueueItem, deleteQueueItem, setQueuePriority,
    fetchRules, createRule, updateRule, deleteRule, enableRule, disableRule, reorderRules, fetchRuleStats,
    fetchPresets, createPreset, updatePreset, deletePreset, sendPreset,
    activateSOS, cancelSOS, fetchSOSStatus,
    fetchDLQ,
    smsContacts, fetchSMSContacts, createSMSContact, updateSMSContact, deleteSMSContact, sendSMS,
    deliveries, deliveryStats, fetchDeliveries, fetchDeliveryStats, cancelDelivery, retryDelivery, fetchMessageDeliveries,
    transportChannels, fetchTransportChannels,
    webhookLog, fetchWebhookLog,
    iridiumGeolocation, triggerIridiumGeolocation,
    interfaces, fetchInterfaces, createInterface, updateInterface, deleteInterface, bindDevice, unbindDevice, generateEncryptionKey,
    devices, fetchDevices,
    accessRules, fetchAccessRules, createAccessRule, updateAccessRule, deleteAccessRule,
    objectGroups, fetchObjectGroups, createObjectGroup, updateObjectGroup, deleteObjectGroup,
    failoverGroups, fetchFailoverGroups, createFailoverGroup, deleteFailoverGroup,
    auditLog, auditSigner, fetchAuditLog, verifyAuditLog, fetchAuditSigner,
    exportConfig, importConfig, validateTransforms,
    sseConnected, connectSSE, closeSSE
  }
})
