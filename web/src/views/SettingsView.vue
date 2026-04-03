<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import ConfigSection from '@/components/ConfigSection.vue'
import api from '@/api/client'

const store = useMeshsatStore()
const activeTab = ref('radio')
const tabs = [
  { id: 'radio', label: 'Radio' },
  { id: 'channels', label: 'Channels' },
  { id: 'position', label: 'Position' },
  { id: 'canned', label: 'Canned Msg' },
  { id: 'mqtt', label: 'MQTT' },
  { id: 'device_mqtt', label: 'Device MQTT' },
  { id: 'iridium', label: 'Iridium' },
  { id: 'astrocast', label: 'Astrocast' },
  { id: 'cellular', label: 'Cellular' },
  { id: 'zigbee', label: 'ZigBee' },
  { id: 'store_forward', label: 'S&F' },
  { id: 'range_test', label: 'Range Test' },
  { id: 'deadman', label: 'Dead Man' },
  { id: 'tak', label: 'TAK' },
  { id: 'credentials', label: 'Credentials' },
  { id: 'routing', label: 'Routing' },
  { id: 'config_mgmt', label: 'Export/Import' },
  { id: 'about', label: 'About' }
]

// Radio config
const radioSection = ref('lora')
const radioConfig = ref({})
const radioEditing = ref(false)
const radioJSON = ref('')

const radioRefreshing = ref(false)

// Protobuf field name mappings for Meshtastic Config
const configSectionMap = {
  lora: 'config_6', device: 'config_1', position: 'config_2',
  power: 'config_3', bluetooth: 'config_7',
}
const configFieldLabels = {
  lora: {
    '1': 'Use Preset', '2': 'Modem Preset', '3': 'Bandwidth (Hz)', '4': 'Spread Factor',
    '5': 'Coding Rate', '6': 'Frequency Offset', '7': 'Region', '8': 'Hop Limit',
    '9': 'TX Enabled', '10': 'TX Power (dBm)', '11': 'Channel Number', '12': 'Override Duty Cycle',
    '13': 'SX126x RX Boosted Gain', '14': 'Override Frequency (MHz)', '15': 'PA Fan Disabled',
    '17': 'Ignore MQTT', '18': 'Config OK to MQTT',
  },
  device: {
    '1': 'Role', '2': 'Serial Enabled', '3': 'Debug Log Enabled',
    '5': 'Button GPIO', '6': 'Buzzer GPIO', '7': 'Rebroadcast Mode',
    '8': 'Node Info Broadcast (s)', '9': 'Double Tap as Button',
    '10': 'Is Managed', '12': 'Disable Triple Click', '13': 'Timezone',
    '14': 'LED Heartbeat Disabled',
  },
  position: {
    '1': 'Broadcast Interval (s)', '2': 'Smart Broadcast', '3': 'Fixed Position',
    '4': 'GPS Enabled', '5': 'GPS Update Interval (s)', '6': 'GPS Attempt Time (s)',
    '7': 'Position Flags', '8': 'RX GPIO', '9': 'TX GPIO',
    '10': 'Smart Min Distance (m)', '11': 'Smart Min Interval (s)',
    '12': 'GPS Enable GPIO', '13': 'GPS Mode',
  },
  power: {
    '1': 'Power Saving', '2': 'Shutdown After (s)', '3': 'ADC Multiplier',
    '4': 'Wait Bluetooth (s)', '5': 'Super Deep Sleep (s)', '6': 'Light Sleep (s)',
    '7': 'Min Wake (s)', '8': 'Battery INA Address',
  },
  bluetooth: {
    '1': 'Enabled', '2': 'Mode', '3': 'Fixed PIN',
  },
}

const currentSectionData = computed(() => {
  if (!store.config) return []
  const configKey = configSectionMap[radioSection.value]
  const data = store.config[configKey]
  if (!data || typeof data !== 'object') return []
  const labels = configFieldLabels[radioSection.value] || {}
  return Object.entries(data)
    .sort(([a], [b]) => parseInt(a) - parseInt(b))
    .map(([k, v]) => ({
      key: k,
      label: labels[k] || `Field ${k}`,
      value: typeof v === 'object' ? JSON.stringify(v) : v,
      isBool: v === true || v === false || v === 0 || v === 1,
    }))
})

// Credentials
const credFile = ref(null)
const credFileName = ref('')
const credProvider = ref('')
const credName = ref('')
const credUploading = ref(false)
const credUploadResult = ref('')

function onCredFileSelected(e) {
  const f = e.target.files[0]
  if (f) {
    credFile.value = f
    credFileName.value = f.name
    credUploadResult.value = ''
  }
}

async function doUploadCred() {
  if (!credFile.value || !credProvider.value) return
  credUploading.value = true
  credUploadResult.value = ''
  try {
    const result = await store.uploadCredential(credFile.value, credProvider.value, credName.value || credProvider.value)
    credUploadResult.value = `Uploaded: ${result.cred_type} (${result.subject || result.fingerprint?.substring(0, 16) || 'ok'})`
    credFile.value = null
    credFileName.value = ''
    credProvider.value = ''
    credName.value = ''
  } catch (e) {
    credUploadResult.value = ''
  }
  credUploading.value = false
}

function credExpiryClass(c) {
  if (!c.cert_not_after) return 'bg-gray-700 text-gray-400'
  const days = Math.floor((new Date(c.cert_not_after) - Date.now()) / 86400000)
  if (days <= 0) return 'bg-red-900 text-red-300'
  if (days <= 30) return 'bg-amber-900 text-amber-300'
  return 'bg-emerald-900 text-emerald-300'
}

function credExpiryLabel(c) {
  if (!c.cert_not_after) return 'no expiry'
  const days = Math.floor((new Date(c.cert_not_after) - Date.now()) / 86400000)
  if (days <= 0) return 'EXPIRED'
  return `${days}d left`
}

// Dead Man's Switch
const deadmanSaving = ref(false)
const deadmanLocalEnabled = ref(false)
const deadmanLocalTimeout = ref(240)

async function loadDeadman() {
  await store.fetchDeadmanConfig()
  deadmanLocalEnabled.value = store.deadmanEnabled
  deadmanLocalTimeout.value = store.deadmanTimeout
}

async function saveDeadman() {
  deadmanSaving.value = true
  try {
    await store.setDeadmanConfig(deadmanLocalEnabled.value, deadmanLocalTimeout.value)
  } catch {}
  deadmanSaving.value = false
}

const deadmanLastActivity = computed(() => {
  const cfg = store.deadmanConfig
  if (!cfg || !cfg.last_activity) return null
  const elapsed = Math.floor(Date.now() / 1000 - cfg.last_activity)
  if (elapsed < 60) return `${elapsed}s ago`
  if (elapsed < 3600) return `${Math.floor(elapsed / 60)}m ago`
  return `${Math.floor(elapsed / 3600)}h ${Math.floor((elapsed % 3600) / 60)}m ago`
})

// TAK gateway
const takForm = ref({ tak_host: '', tak_port: 8087, tak_ssl: false, protocol: 'xml', cert_file: '', key_file: '', ca_file: '', callsign_prefix: 'MESHSAT', cot_stale_seconds: 300, coalesce_seconds: 30 })
const takEnabled = ref(false)
const takGw = computed(() => (store.gateways || []).find(g => g.type === 'tak'))

function loadTAK() {
  if (takGw.value?.config) {
    try {
      const c = typeof takGw.value.config === 'string' ? JSON.parse(takGw.value.config) : takGw.value.config
      Object.assign(takForm.value, c)
      takEnabled.value = takGw.value.enabled
    } catch {}
  }
}

async function saveTAK() {
  await store.configureGateway('tak', takEnabled.value, takForm.value)
}

// Config export/import
const exportedConfig = ref('')
const importText = ref('')
const importResult = ref(null)
const exporting = ref(false)
const importing = ref(false)

// Factory reset
const factoryResetConfirm = ref(false)
const factoryResetNodeId = ref('')
const factoryResetResult = ref('')

async function doFactoryReset() {
  if (!factoryResetNodeId.value) return
  factoryResetResult.value = ''
  try {
    const res = await store.adminFactoryReset({ node_id: parseInt(factoryResetNodeId.value) })
    factoryResetResult.value = res?.status || 'factory reset command sent'
    factoryResetConfirm.value = false
    factoryResetNodeId.value = ''
  } catch (e) {
    factoryResetResult.value = e.message || 'factory reset failed'
  }
}

async function doExportConfig() {
  exporting.value = true
  exportedConfig.value = ''
  try {
    const data = await store.exportConfig()
    exportedConfig.value = typeof data === 'string' ? data : JSON.stringify(data, null, 2)
  } catch { /* store error */ }
  exporting.value = false
}

function downloadConfig() {
  if (!exportedConfig.value) return
  const blob = new Blob([exportedConfig.value], { type: 'text/yaml' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `meshsat-config-${new Date().toISOString().slice(0, 10)}.yaml`
  a.click()
  URL.revokeObjectURL(url)
}

async function doImportConfig() {
  if (!importText.value.trim()) return
  importing.value = true
  importResult.value = null
  try {
    importResult.value = await store.importConfig(importText.value)
  } catch (e) {
    importResult.value = { error: e.message }
  }
  importing.value = false
}

async function refreshRadioSection() {
  radioRefreshing.value = true
  try {
    await store.fetchConfigSection(radioSection.value)
    setTimeout(() => store.fetchConfig(), 1500) // wait for device response
  } catch {}
  radioRefreshing.value = false
}

async function saveRadioConfig() {
  try {
    const data = JSON.parse(radioJSON.value)
    await store.configRadio({ section: radioSection.value, ...data })
    radioEditing.value = false
    store.fetchConfig()
  } catch (e) {
    store.error = e.message
  }
}

// Position sharing
const posForm = ref({ latitude: 0, longitude: 0, altitude: 0 })
const positionSending = ref(false)

async function doSendPosition() {
  positionSending.value = true
  try { await store.sendPosition(posForm.value) } catch {}
  positionSending.value = false
}

async function doSetFixedPosition() {
  try { await store.setFixedPosition(posForm.value) } catch {}
}

async function doRemoveFixedPosition() {
  try { await store.removeFixedPosition() } catch {}
}

// Canned messages
const cannedText = ref('')
const cannedLoading = ref(false)

async function loadCannedMessages() {
  cannedLoading.value = true
  try {
    const data = await store.getCannedMessages()
    if (data && data.messages) cannedText.value = data.messages
  } catch {}
  cannedLoading.value = false
}

async function saveCannedMessages() {
  try { await store.setCannedMessages(cannedText.value) } catch {}
}

// Device MQTT module
const deviceMqttForm = ref({ enabled: false, address: '', username: '', password: '', encryption_enabled: false, json_enabled: false, tls_enabled: false, root: '' })
const deviceMqttEditing = ref(false)

function loadDeviceMqtt() {
  const raw = store.config?.['module_1']
  if (raw && typeof raw === 'object') {
    deviceMqttForm.value = {
      enabled: !!raw['1'], address: raw['2'] || '', username: raw['3'] || '',
      password: raw['4'] || '', encryption_enabled: !!raw['5'],
      json_enabled: !!raw['6'], tls_enabled: !!raw['7'], root: raw['8'] || '',
    }
  }
}

async function saveDeviceMqtt() {
  const f = deviceMqttForm.value
  const data = {}
  data['1'] = f.enabled
  if (f.address) data['2'] = f.address
  if (f.username) data['3'] = f.username
  if (f.password) data['4'] = f.password
  data['5'] = f.encryption_enabled
  data['6'] = f.json_enabled
  data['7'] = f.tls_enabled
  if (f.root) data['8'] = f.root
  try {
    await store.configModule({ section: 'mqtt', config: data })
    deviceMqttEditing.value = false
  } catch (e) {
    store.error = e.message
  }
}

async function refreshDeviceMqtt() {
  try {
    await store.fetchModuleConfigSection('mqtt')
    setTimeout(() => { store.fetchConfig(); loadDeviceMqtt() }, 1500)
  } catch {}
}

// Store & Forward
const sfForm = ref({ node_id: 0, window: 3600 })

async function doRequestSF() {
  try { await store.requestStoreForward(sfForm.value) } catch {}
}

// Range Test
const rtForm = ref({ text: '', to: 0 })
const rtSending = ref(false)

async function doSendRangeTest() {
  rtSending.value = true
  try {
    await store.sendRangeTest(rtForm.value)
    await store.fetchRangeTests()
  } catch {}
  rtSending.value = false
}

// Channels
const editingChannel = ref(null)
const channelForm = ref({})

const channels = computed(() => {
  if (!store.config?.channels) return []
  return Object.entries(store.config.channels).map(([idx, ch]) => ({ index: parseInt(idx), ...ch }))
})

function editChannel(ch) {
  editingChannel.value = ch.index
  channelForm.value = { index: ch.index, name: ch.name || '', psk: ch.psk || '', role: ch.role || 'SECONDARY', uplink_enabled: ch.uplink_enabled || false, downlink_enabled: ch.downlink_enabled || false }
}

async function saveChannel() {
  await store.setChannel(channelForm.value)
  editingChannel.value = null
  store.fetchConfig()
}

// MQTT gateway
const mqttForm = ref({ broker_url: '', username: '', password: '', client_id: 'meshsat', topic_prefix: 'msh/US', channel_name: 'LongFast', qos: 0, use_tls: false, keep_alive: 60 })
const mqttEnabled = ref(false)

const mqttGw = computed(() => (store.gateways || []).find(g => g.type === 'mqtt'))

function loadMQTT() {
  if (mqttGw.value?.config) {
    try {
      const c = typeof mqttGw.value.config === 'string' ? JSON.parse(mqttGw.value.config) : mqttGw.value.config
      Object.assign(mqttForm.value, c)
      mqttEnabled.value = mqttGw.value.enabled
    } catch {}
  }
}

async function saveMQTT() {
  await store.configureGateway('mqtt', mqttEnabled.value, mqttForm.value)
}

// Iridium gateway
const iridiumForm = ref({
  forward_all: false, forward_portnums: [1], compression: 'compact', auto_receive: true,
  mailbox_mode: 'ring_alert_only', poll_interval: 1800, max_text_length: 320, include_position: true,
  dlq_max_retries: 0, dlq_retry_base_secs: 120, min_signal_bars: 1,
  daily_budget: 0, monthly_budget: 0, critical_reserve: 20,
  min_elev_deg: 5,
  expiry_policy: { critical_max_retries: 0, normal_max_retries: 0, low_max_retries: 0 }
})
const checkingMailbox = ref(false)

async function doCheckMailbox() {
  checkingMailbox.value = true
  try { await store.manualMailboxCheck() } catch {}
  checkingMailbox.value = false
}
const iridiumEnabled = ref(false)

const iridiumGw = computed(() => (store.gateways || []).find(g => g.type === 'iridium'))

function loadIridium() {
  if (iridiumGw.value?.config) {
    try {
      const c = typeof iridiumGw.value.config === 'string' ? JSON.parse(iridiumGw.value.config) : iridiumGw.value.config
      Object.assign(iridiumForm.value, c)
      // Ensure expiry_policy exists with defaults (backward compat with configs saved before this feature)
      if (!iridiumForm.value.expiry_policy || typeof iridiumForm.value.expiry_policy !== 'object') {
        iridiumForm.value.expiry_policy = { critical_max_retries: 0, normal_max_retries: 0, low_max_retries: 0 }
      }
      iridiumEnabled.value = iridiumGw.value.enabled
    } catch {}
  }
}

async function saveIridium() {
  await store.configureGateway('iridium', iridiumEnabled.value, iridiumForm.value)
}

// Credit budget
const budgetForm = ref({ daily: 0, monthly: 0 })

async function loadBudget() {
  await store.fetchCredits()
  if (store.creditSummary) {
    budgetForm.value.daily = store.creditSummary.daily_budget || 0
    budgetForm.value.monthly = store.creditSummary.monthly_budget || 0
  }
}

async function saveBudget() {
  await store.setCreditBudget(budgetForm.value.daily, budgetForm.value.monthly)
}

// Astrocast gateway
const astrocastForm = ref({
  max_uplink_bytes: 160, poll_interval_sec: 300, fragment_enabled: true,
  geoloc_enabled: false, power_mode: 'balanced'
})
const astrocastEnabled = ref(false)

const astrocastGw = computed(() => (store.gateways || []).find(g => g.type === 'astrocast'))

function loadAstrocast() {
  if (astrocastGw.value?.config) {
    try {
      const c = typeof astrocastGw.value.config === 'string' ? JSON.parse(astrocastGw.value.config) : astrocastGw.value.config
      Object.assign(astrocastForm.value, c)
      astrocastEnabled.value = astrocastGw.value.enabled
    } catch {}
  }
}

async function saveAstrocast() {
  await store.configureGateway('astrocast', astrocastEnabled.value, astrocastForm.value)
}

const astrocastTesting = ref(false)
const astrocastTestResult = ref('')

async function doTestAstrocast() {
  astrocastTesting.value = true
  astrocastTestResult.value = ''
  try {
    await store.testGateway('astrocast')
    astrocastTestResult.value = 'ok'
  } catch (e) {
    astrocastTestResult.value = e.message || 'failed'
  }
  astrocastTesting.value = false
}

// Cellular gateway
const cellularForm = ref({
  sms_destinations: '', allowed_senders: '', sms_prefix: 'MESHSAT', max_segments: 3,
  apn: '', auto_connect: false, auto_reconnect: false, apn_failover_list: '',
  webhook_url: '', webhook_headers: '', inbound_webhook_enabled: false, inbound_webhook_secret: '',
  dyndns_provider: 'none', dyndns_domain: '', dyndns_token: '', dyndns_zone_id: '', dyndns_interval: 300
})
const cellularEnabled = ref(false)

// SMS Contact management
const showContactForm = ref(false)
const editingContact = ref(null)
const contactForm = ref({ name: '', phone: '', notes: '', auto_fwd: false })

function editContact(c) {
  editingContact.value = c.id
  contactForm.value = { name: c.name, phone: c.phone, notes: c.notes || '', auto_fwd: !!c.auto_fwd }
  showContactForm.value = true
}

function cancelContact() {
  showContactForm.value = false
  editingContact.value = null
  contactForm.value = { name: '', phone: '', notes: '', auto_fwd: false }
}

async function saveContact() {
  if (!contactForm.value.name || !contactForm.value.phone) return
  try {
    if (editingContact.value) {
      await store.updateSMSContact(editingContact.value, contactForm.value)
    } else {
      await store.createSMSContact(contactForm.value)
    }
    cancelContact()
  } catch { /* store error */ }
}

async function deleteContact(id) {
  if (!confirm('Delete this contact?')) return
  try { await store.deleteSMSContact(id) } catch { /* store error */ }
}

// SIM Card management
const showSimForm = ref(false)
const editingSim = ref(null)
const simForm = ref({ iccid: '', label: '', phone: '', pin: '', notes: '' })
const simReadingICCID = ref(false)

function editSim(s) {
  editingSim.value = s.id
  simForm.value = { iccid: s.iccid, label: s.label, phone: s.phone || '', pin: s.pin || '', notes: s.notes || '' }
  showSimForm.value = true
}

function cancelSim() {
  showSimForm.value = false
  editingSim.value = null
  simForm.value = { iccid: '', label: '', phone: '', pin: '', notes: '' }
}

async function readCurrentICCID() {
  simReadingICCID.value = true
  try {
    const data = await store.readCurrentICCID()
    if (data?.iccid) {
      simForm.value.iccid = data.iccid
      if (!simForm.value.label) simForm.value.label = 'SIM ' + data.iccid.slice(-4)
    }
  } catch { /* ignore */ }
  simReadingICCID.value = false
}

async function saveSim() {
  if (!simForm.value.iccid) return
  try {
    if (editingSim.value) {
      await store.updateSIMCard(editingSim.value, simForm.value)
    } else {
      await store.createSIMCard(simForm.value)
    }
    cancelSim()
  } catch { /* store error */ }
}

async function deleteSim(id) {
  if (!confirm('Delete this SIM card?')) return
  try { await store.deleteSIMCard(id) } catch { /* store error */ }
}

// SIM PIN unlock
const settingsPinInput = ref('')
const settingsPinUnlocking = ref(false)
const settingsPinError = ref('')
async function unlockSettingsPIN() {
  settingsPinUnlocking.value = true
  settingsPinError.value = ''
  try {
    await store.submitCellularPIN(settingsPinInput.value)
    settingsPinInput.value = ''
    await store.fetchCellularStatus()
  } catch (e) {
    settingsPinError.value = e.message || 'PIN unlock failed'
  }
  settingsPinUnlocking.value = false
}

const cellularGw = computed(() => (store.gateways || []).find(g => g.type === 'cellular'))

function loadCellular() {
  if (cellularGw.value?.config) {
    try {
      const c = typeof cellularGw.value.config === 'string' ? JSON.parse(cellularGw.value.config) : cellularGw.value.config
      // Convert array fields to comma-separated strings for form inputs
      if (Array.isArray(c.apn_failover_list)) {
        c.apn_failover_list = c.apn_failover_list.join(', ')
      }
      // Flatten nested dyndns object to form fields
      if (c.dyndns && typeof c.dyndns === 'object') {
        cellularForm.value.dyndns_provider = c.dyndns.provider || 'none'
        cellularForm.value.dyndns_domain = c.dyndns.domain || ''
        cellularForm.value.dyndns_token = c.dyndns.token || ''
        cellularForm.value.dyndns_zone_id = c.dyndns.zone_id || ''
        cellularForm.value.dyndns_interval = c.dyndns.interval || 300
        delete c.dyndns
      }
      Object.assign(cellularForm.value, c)
      cellularEnabled.value = cellularGw.value.enabled
    } catch {}
  }
}

async function saveCellular() {
  const cfg = { ...cellularForm.value }
  // Convert comma-separated APN failover list to array
  if (typeof cfg.apn_failover_list === 'string') {
    cfg.apn_failover_list = cfg.apn_failover_list.split(',').map(s => s.trim()).filter(Boolean)
  }
  // Rebuild nested dyndns object from flat form fields
  const provider = cfg.dyndns_provider || 'none'
  cfg.dyndns = {
    enabled: provider !== 'none',
    provider: provider === 'none' ? '' : provider,
    domain: cfg.dyndns_domain || '',
    token: cfg.dyndns_token || '',
    zone_id: cfg.dyndns_zone_id || '',
    interval: cfg.dyndns_interval || 300,
  }
  delete cfg.dyndns_provider
  delete cfg.dyndns_domain
  delete cfg.dyndns_token
  delete cfg.dyndns_zone_id
  delete cfg.dyndns_interval
  await store.configureGateway('cellular', cellularEnabled.value, cfg)
}

// ZigBee gateway
const zigbeeForm = ref({
  serial_port: 'auto', inbound_channel: 0, inbound_dest: '',
  forward_all: false, default_dst_addr: 65535, default_dst_ep: 1, default_cluster: 6
})
const zigbeeEnabled = ref(false)
const zigbeeStatus = ref(null)
const zigbeeDevices = ref([])

const zigbeeGw = computed(() => (store.gateways || []).find(g => g.type === 'zigbee'))

function loadZigBee() {
  if (zigbeeGw.value?.config) {
    try {
      const c = typeof zigbeeGw.value.config === 'string' ? JSON.parse(zigbeeGw.value.config) : zigbeeGw.value.config
      Object.assign(zigbeeForm.value, c)
      zigbeeEnabled.value = zigbeeGw.value.enabled
    } catch {}
  }
}

async function saveZigBee() {
  await store.configureGateway('zigbee', zigbeeEnabled.value, zigbeeForm.value)
}

async function fetchZigBeeStatus() {
  try {
    const resp = await fetch('/api/zigbee/status')
    zigbeeStatus.value = await resp.json()
  } catch {}
}

async function fetchZigBeeDevices() {
  try {
    const resp = await fetch('/api/zigbee/devices')
    const data = await resp.json()
    zigbeeDevices.value = data.devices || []
  } catch {}
}

// Routing config + peers + flood control
const routingForm = ref({ listen_port: 4242, announce_interval: 300, listen_addr: '' })
const routingWarning = ref('')
const newPeerAddr = ref('')
const routingPeers = ref([])

async function loadRoutingConfig() {
  try {
    const data = await api.get('/routing/config')
    if (data) Object.assign(routingForm.value, data)
  } catch {}
}
async function saveRoutingConfig() {
  routingWarning.value = ''
  try {
    const data = await api.put('/routing/config', routingForm.value)
    if (data?.warning) routingWarning.value = data.warning
    if (data) Object.assign(routingForm.value, data)
  } catch (e) { routingWarning.value = e.message }
}
async function fetchPeers() {
  try {
    const data = await api.get('/routing/peers')
    routingPeers.value = Array.isArray(data) ? data : []
  } catch {}
}
async function addPeer() {
  if (!newPeerAddr.value) return
  try {
    await api.post('/routing/peers', { address: newPeerAddr.value })
    newPeerAddr.value = ''
    await fetchPeers()
  } catch (e) { routingWarning.value = e.message }
}
async function removePeer(addr) {
  try {
    await api.del(`/routing/peers/${encodeURIComponent(addr)}`)
    await fetchPeers()
  } catch (e) { routingWarning.value = e.message }
}

// Hub connection
const hubForm = ref({ url: '', bridge_id: '', username: '', password: '', has_password: false, tls_ca: '', tls_insecure: false })
const hubWarning = ref('')
const restarting = ref(false)

async function restartBridge() {
  restarting.value = true
  try {
    await api.post('/system/restart')
  } catch {}
}

async function loadHubConfig() {
  try {
    const data = await api.get('/routing/hub')
    if (data) Object.assign(hubForm.value, data)
  } catch {}
}
async function saveHubConfig() {
  hubWarning.value = ''
  try {
    const data = await api.put('/routing/hub', hubForm.value)
    if (data?.warning) hubWarning.value = data.warning
    hubForm.value.password = ''
    if (data) { hubForm.value.has_password = data.has_password }
  } catch (e) { hubWarning.value = e.message }
}

async function toggleFloodable(iface) {
  const enabling = !iface.floodable
  if (enabling && iface.cost > 0) {
    const ok = confirm(
      `Enable flooding on ${iface.id} ($${iface.cost}/msg)?\n\n` +
      'Path discovery requests and announce broadcasts will be sent over this paid interface. ' +
      'Every node in the network can trigger a message. This may incur significant costs.'
    )
    if (!ok) { await store.fetchRoutingInterfaces(); return }
  }
  await store.setFloodable(iface.id, enabling)
}

// Signal polling
let signalTimer = null

onMounted(async () => {
  store.fetchConfig()
  await store.fetchGateways()
  store.fetchIridiumSignalFast()
  signalTimer = setInterval(() => store.fetchIridiumSignalFast(), 10000)
  store.fetchCellularStatus()
  store.fetchCellularSignal()
  store.fetchSMSContacts()
  store.fetchSIMCards()
  loadMQTT(); loadIridium(); loadBudget(); loadAstrocast(); loadCellular(); loadZigBee(); loadDeadman(); loadDeviceMqtt(); loadTAK()
  store.fetchCredentials()
  store.fetchRoutingInterfaces()
  loadRoutingConfig(); fetchPeers(); loadHubConfig()
  fetchZigBeeStatus(); fetchZigBeeDevices()
  store.fetchRangeTests()
})

onUnmounted(() => { if (signalTimer) clearInterval(signalTimer) })
</script>

<template>
  <div class="max-w-3xl mx-auto">
    <h2 class="text-lg font-semibold text-gray-200 mb-4">Settings</h2>

    <!-- Tab bar -->
    <div class="flex gap-1 mb-6 overflow-x-auto pb-1">
      <button v-for="tab in tabs" :key="tab.id" @click="activeTab = tab.id"
        class="px-4 py-2 rounded-lg text-xs font-medium whitespace-nowrap transition-colors"
        :class="activeTab === tab.id ? 'bg-teal-600 text-white' : 'bg-gray-800 text-gray-400 hover:text-gray-200'">
        {{ tab.label }}
      </button>
    </div>

    <!-- Radio Config -->
    <div v-if="activeTab === 'radio'">
      <div v-if="!store.config" class="text-sm text-gray-500">Loading radio config...</div>
      <div v-else>
        <div class="flex gap-2 mb-4">
          <select v-model="radioSection" class="px-3 py-2 rounded bg-gray-800 border border-gray-700 text-sm text-gray-200">
            <option value="lora">LoRa Radio</option>
            <option value="device">Device</option>
            <option value="position">Position</option>
            <option value="power">Power</option>
            <option value="bluetooth">Bluetooth</option>
          </select>
          <button @click="refreshRadioSection" :disabled="radioRefreshing" class="px-3 py-2 rounded bg-gray-800 border border-gray-700 text-xs text-gray-400 hover:text-teal-400 disabled:opacity-40">
            {{ radioRefreshing ? 'Refreshing...' : 'Refresh' }}
          </button>
          <button @click="radioEditing = !radioEditing" class="px-3 py-2 rounded bg-gray-800 border border-gray-700 text-xs text-gray-400 hover:text-teal-400">
            {{ radioEditing ? 'Cancel' : 'Edit JSON' }}
          </button>
        </div>
        <div v-if="radioEditing">
          <textarea v-model="radioJSON" rows="8" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" placeholder="{ ... }"></textarea>
          <button @click="saveRadioConfig" class="mt-2 px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Apply</button>
        </div>
        <div v-else-if="currentSectionData.length > 0" class="bg-gray-900 rounded-lg border border-gray-700 overflow-hidden">
          <div v-for="(field, i) in currentSectionData" :key="field.key"
            class="flex items-center px-4 py-2 text-sm" :class="i % 2 === 0 ? 'bg-gray-900' : 'bg-gray-800/50'">
            <span class="w-1/2 text-gray-400 truncate">{{ field.label }}</span>
            <span v-if="field.value === true || field.value === 1" class="text-emerald-400">enabled</span>
            <span v-else-if="field.value === false || field.value === 0" class="text-gray-600">disabled</span>
            <span v-else class="text-gray-200 font-mono text-xs truncate">{{ field.value }}</span>
          </div>
        </div>
        <div v-else class="bg-gray-900 rounded-lg p-4 text-xs text-gray-500">
          No data for this section. Try clicking Refresh to request it from the device.
        </div>
      </div>
    </div>

    <!-- Channels -->
    <div v-if="activeTab === 'channels'">
      <div v-for="ch in channels" :key="ch.index" class="bg-gray-800 rounded-lg p-4 border border-gray-700 mb-3">
        <div class="flex items-center justify-between mb-2">
          <div class="flex items-center gap-2">
            <span class="w-6 h-6 rounded bg-gray-700 flex items-center justify-center text-xs font-bold text-gray-400">{{ ch.index }}</span>
            <span class="text-sm text-gray-200">{{ ch.name || 'Unnamed' }}</span>
            <span class="px-1.5 py-0.5 rounded text-[10px] font-medium" :class="ch.role === 'PRIMARY' ? 'bg-teal-500/20 text-teal-400' : ch.role === 'DISABLED' ? 'bg-gray-600 text-gray-500' : 'bg-gray-700 text-gray-400'">
              {{ ch.role || 'SECONDARY' }}
            </span>
          </div>
          <button @click="editChannel(ch)" class="text-xs text-gray-400 hover:text-teal-400">Edit</button>
        </div>
        <div v-if="editingChannel === ch.index" class="mt-3 space-y-3">
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="block text-xs text-gray-500 mb-1">Name</label>
              <input v-model="channelForm.name" maxlength="11" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            </div>
            <div>
              <label class="block text-xs text-gray-500 mb-1">Role</label>
              <select v-model="channelForm.role" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
                <option>PRIMARY</option><option>SECONDARY</option><option>DISABLED</option>
              </select>
            </div>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">PSK (base64)</label>
            <input v-model="channelForm.psk" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono">
          </div>
          <div class="flex gap-4">
            <label class="flex items-center gap-1 text-xs text-gray-400"><input type="checkbox" v-model="channelForm.uplink_enabled" class="rounded bg-gray-900 border-gray-700"> Uplink</label>
            <label class="flex items-center gap-1 text-xs text-gray-400"><input type="checkbox" v-model="channelForm.downlink_enabled" class="rounded bg-gray-900 border-gray-700"> Downlink</label>
          </div>
          <div class="flex gap-2">
            <button @click="editingChannel = null" class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs">Cancel</button>
            <button @click="saveChannel" class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs">Save</button>
          </div>
        </div>
      </div>
    </div>

    <!-- MQTT -->
    <div v-if="activeTab === 'mqtt'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-gray-200">MQTT Gateway</span>
          <span v-if="mqttGw" class="text-xs" :class="mqttGw.connected ? 'text-emerald-400' : 'text-gray-500'">
            {{ mqttGw.connected ? 'Connected' : 'Disconnected' }}
          </span>
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Broker URL</label>
          <input v-model="mqttForm.broker_url" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="tcp://mosquitto:1883">
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Username</label>
            <input v-model="mqttForm.username" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Password</label>
            <input v-model="mqttForm.password" type="password" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Topic Prefix</label>
            <input v-model="mqttForm.topic_prefix" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Channel Name</label>
            <input v-model="mqttForm.channel_name" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="flex items-center gap-2">
          <input type="checkbox" v-model="mqttEnabled" id="mqtt_en" class="rounded bg-gray-900 border-gray-700">
          <label for="mqtt_en" class="text-xs text-gray-400">Enable MQTT gateway</label>
        </div>
        <button @click="saveMQTT" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save MQTT Config</button>
      </div>
    </div>

    <!-- Iridium -->
    <div v-if="activeTab === 'iridium'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-gray-200">Iridium Satellite</span>
          <span class="text-xs" :class="iridiumGw?.connected ? 'text-emerald-400' : 'text-gray-500'">
            {{ iridiumGw?.connected ? 'Connected' : 'Disconnected' }}
          </span>
        </div>

        <!-- Mailbox Polling Mode -->
        <div>
          <label class="block text-xs text-gray-500 mb-1">Mailbox Polling Mode</label>
          <select v-model="iridiumForm.mailbox_mode" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <option value="ring_alert_only">Ring Alert Only (no periodic polling, saves credits)</option>
            <option value="scheduled">Scheduled (pass-aware periodic polling)</option>
            <option value="off">Off (no mailbox checking)</option>
          </select>
          <p class="text-[10px] text-gray-600 mt-1">
            <template v-if="iridiumForm.mailbox_mode === 'ring_alert_only'">Waits for Iridium ring alerts and satellite pass events. Zero credit waste.</template>
            <template v-else-if="iridiumForm.mailbox_mode === 'scheduled'">Periodically checks mailbox using pass-aware scheduling. Costs 1 credit per empty check.</template>
            <template v-else>No mailbox checking at all. Inbound messages will not be received.</template>
          </p>
        </div>

        <!-- Poll interval (only shown for scheduled mode) -->
        <div v-if="iridiumForm.mailbox_mode === 'scheduled'" class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Idle Poll Interval (sec)</label>
            <input v-model.number="iridiumForm.idle_poll_sec" type="number" min="60" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Active Poll Interval (sec)</label>
            <input v-model.number="iridiumForm.active_poll_sec" type="number" min="10" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>

        <!-- Check Mailbox Now -->
        <div class="flex items-center gap-3">
          <button @click="doCheckMailbox" :disabled="checkingMailbox || !iridiumGw?.connected"
            class="px-3 py-1.5 rounded bg-gray-700 border border-gray-600 text-xs text-gray-300 hover:text-teal-400 hover:border-teal-600/30 transition-colors disabled:opacity-40 disabled:cursor-not-allowed">
            {{ checkingMailbox ? 'Checking...' : 'Check Mailbox Now' }}
          </button>
          <span class="text-[10px] text-gray-600">Triggers a one-shot SBDIX session (costs 1 credit if mailbox is empty)</span>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Max Text Length</label>
            <input v-model.number="iridiumForm.max_text_length" type="number" min="1" max="340" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Min Signal Bars</label>
            <input v-model.number="iridiumForm.min_signal_bars" type="number" min="0" max="5" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Min Elevation (°)</label>
            <input v-model.number="iridiumForm.min_elev_deg" type="number" min="0" max="90" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <p class="text-[10px] text-gray-600 mt-0.5">Pass scheduler min elevation (5=clear sky, 40=urban, 60=canyon)</p>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Critical Reserve (%)</label>
            <input v-model.number="iridiumForm.critical_reserve" type="number" min="0" max="100" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Daily Budget (0=unlimited)</label>
            <input v-model.number="iridiumForm.daily_budget" type="number" min="0" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Monthly Budget (0=unlimited)</label>
            <input v-model.number="iridiumForm.monthly_budget" type="number" min="0" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="flex flex-wrap gap-4">
          <label class="flex items-center gap-1 text-xs text-gray-400"><input type="checkbox" v-model="iridiumForm.auto_receive" class="rounded bg-gray-900 border-gray-700"> Auto-receive</label>
          <label class="flex items-center gap-1 text-xs text-gray-400"><input type="checkbox" v-model="iridiumForm.include_position" class="rounded bg-gray-900 border-gray-700"> Include position</label>
          <label class="flex items-center gap-1 text-xs text-gray-400"><input type="checkbox" v-model="iridiumForm.forward_all" class="rounded bg-gray-900 border-gray-700"> Forward all</label>
        </div>

        <!-- Per-Priority Message Expiration -->
        <div class="border-t border-gray-700 pt-3 mt-1">
          <h4 class="text-xs font-medium text-gray-300 mb-2">Message Expiration by Priority</h4>
          <p class="text-[10px] text-gray-600 mb-3">Configure how many retry attempts before a queued message expires. 0 or "Never expire" means infinite retries.</p>
          <div class="space-y-2">
            <div v-for="p in [{key: 'critical_max_retries', label: 'Critical (P0)', color: 'text-red-400'}, {key: 'normal_max_retries', label: 'Normal (P1)', color: 'text-gray-300'}, {key: 'low_max_retries', label: 'Low (P2)', color: 'text-gray-500'}]" :key="p.key"
              class="flex items-center gap-3">
              <span class="text-xs w-24" :class="p.color">{{ p.label }}</span>
              <label class="flex items-center gap-1 text-xs text-gray-400">
                <input type="checkbox" :checked="iridiumForm.expiry_policy[p.key] === 0"
                  @change="iridiumForm.expiry_policy[p.key] = $event.target.checked ? 0 : 10"
                  class="rounded bg-gray-900 border-gray-700">
                Never expire
              </label>
              <input v-if="iridiumForm.expiry_policy[p.key] > 0"
                v-model.number="iridiumForm.expiry_policy[p.key]" type="number" min="1" max="999"
                class="w-20 px-2 py-1 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <span v-if="iridiumForm.expiry_policy[p.key] > 0" class="text-[10px] text-gray-600">retries</span>
            </div>
          </div>
        </div>

        <div class="flex items-center gap-2">
          <input type="checkbox" v-model="iridiumEnabled" id="iridium_en" class="rounded bg-gray-900 border-gray-700">
          <label for="iridium_en" class="text-xs text-gray-400">Enable Iridium gateway</label>
        </div>
        <button @click="saveIridium" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save Iridium Config</button>
      </div>

      <!-- Credit Budget (dedicated API) -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mt-4">
        <h4 class="text-sm font-medium text-gray-200">Credit Budget</h4>
        <p class="text-xs text-gray-500">Set daily and monthly SBD credit limits. Zero means unlimited.</p>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Daily Limit</label>
            <input v-model.number="budgetForm.daily" type="number" min="0" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Monthly Limit</label>
            <input v-model.number="budgetForm.monthly" type="number" min="0" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div v-if="store.creditSummary" class="text-xs text-gray-500">
          Used today: {{ store.creditSummary.today }} | This month: {{ store.creditSummary.month }} | All time: {{ store.creditSummary.all_time }}
        </div>
        <button @click="saveBudget" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save Budget</button>
      </div>
    </div>

    <!-- Astrocast -->
    <div v-if="activeTab === 'astrocast'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-gray-200">Astrocast Astronode S</span>
          <span class="text-xs" :class="astrocastGw?.connected ? 'text-emerald-400' : 'text-gray-500'">
            {{ astrocastGw?.connected ? 'Connected' : 'Disconnected' }}
          </span>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Max Uplink Bytes</label>
            <input v-model.number="astrocastForm.max_uplink_bytes" type="number" min="1" max="160" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <p class="text-[10px] text-gray-600 mt-0.5">Astronode S max payload is 160 bytes</p>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Poll Interval (sec)</label>
            <input v-model.number="astrocastForm.poll_interval_sec" type="number" min="60" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <p class="text-[10px] text-gray-600 mt-0.5">How often to check for downlink messages</p>
          </div>
        </div>

        <div>
          <label class="block text-xs text-gray-500 mb-1">Power Mode</label>
          <select v-model="astrocastForm.power_mode" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <option value="low_power">Low Power (minimal polling, battery saving)</option>
            <option value="balanced">Balanced (default)</option>
            <option value="performance">Performance (aggressive polling)</option>
          </select>
        </div>

        <div class="flex flex-wrap gap-4">
          <label class="flex items-center gap-1 text-xs text-gray-400">
            <input type="checkbox" v-model="astrocastForm.fragment_enabled" class="rounded bg-gray-900 border-gray-700">
            Auto-fragment messages >160 bytes
          </label>
          <label class="flex items-center gap-1 text-xs text-gray-400">
            <input type="checkbox" v-model="astrocastForm.geoloc_enabled" class="rounded bg-gray-900 border-gray-700">
            Include geolocation in uplinks
          </label>
        </div>

        <div class="flex items-center gap-2">
          <input type="checkbox" v-model="astrocastEnabled" id="astrocast_en" class="rounded bg-gray-900 border-gray-700">
          <label for="astrocast_en" class="text-xs text-gray-400">Enable Astrocast gateway</label>
        </div>
        <button @click="saveAstrocast" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save Astrocast Config</button>

        <!-- Gateway controls -->
        <div class="flex items-center gap-2 mt-3 pt-3 border-t border-gray-700">
          <button @click="store.startGateway('astrocast')" :disabled="astrocastGw?.connected"
            class="px-3 py-1.5 rounded bg-emerald-600/20 text-emerald-400 text-xs border border-emerald-600/30 hover:bg-emerald-600/30 disabled:opacity-40">
            Start
          </button>
          <button @click="store.stopGateway('astrocast')" :disabled="!astrocastGw?.connected"
            class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs hover:bg-gray-600 disabled:opacity-40">
            Stop
          </button>
          <button @click="doTestAstrocast"
            class="px-3 py-1.5 rounded bg-blue-600/20 text-blue-400 text-xs border border-blue-600/30 hover:bg-blue-600/30">
            {{ astrocastTesting ? 'Testing...' : 'Test Connection' }}
          </button>
          <span v-if="astrocastTestResult" class="text-[10px] ml-1"
            :class="astrocastTestResult === 'ok' ? 'text-emerald-400' : 'text-red-400'">
            {{ astrocastTestResult === 'ok' ? 'Connected' : astrocastTestResult }}
          </span>
        </div>
      </div>
    </div>

    <!-- Cellular -->
    <div v-if="activeTab === 'cellular'">
      <!-- Modem Status (read-only) -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mb-4">
        <h4 class="text-sm font-medium text-gray-200">Modem Status</h4>
        <div class="space-y-2 text-[11px]">
          <div class="flex justify-between">
            <span class="text-gray-500">IMEI</span>
            <span class="text-gray-300 font-mono">{{ store.cellularStatus?.imei || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Model</span>
            <span class="text-gray-300">{{ store.cellularStatus?.model || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Operator</span>
            <span class="text-gray-300">{{ store.cellularStatus?.operator || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Network Type</span>
            <span class="text-gray-300">{{ store.cellularStatus?.network_type || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">SIM State</span>
            <span class="text-gray-300">{{ store.cellularStatus?.sim_state || 'N/A' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Registration</span>
            <span class="text-gray-300">{{ store.cellularStatus?.registration || 'N/A' }}</span>
          </div>
          <div v-if="store.cellularStatus?.iccid" class="flex justify-between">
            <span class="text-gray-500">ICCID</span>
            <span class="text-gray-300 font-mono text-[10px]">{{ store.cellularStatus.iccid }}</span>
          </div>
          <div v-if="store.cellularStatus?.phone_number" class="flex justify-between">
            <span class="text-gray-500">Phone</span>
            <span class="text-gray-300 font-mono">{{ store.cellularStatus.phone_number }}</span>
          </div>
          <div v-if="store.cellularStatus?.sim_label" class="flex justify-between">
            <span class="text-gray-500">SIM Card</span>
            <span class="text-sky-400">{{ store.cellularStatus.sim_label }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-gray-500">Signal</span>
            <span class="text-gray-300">{{ store.cellularSignal?.bars ?? 'N/A' }}/5 bars ({{ store.cellularSignal?.dbm ?? 'N/A' }} dBm)</span>
          </div>
        </div>
      </div>

      <!-- SIM PIN Unlock -->
      <div v-if="store.cellularStatus?.sim_state === 'PIN_REQUIRED'" class="bg-amber-900/20 rounded-lg p-4 border border-amber-700/40 space-y-3 mb-4">
        <h4 class="text-sm font-medium text-amber-400">SIM PIN Required</h4>
        <p class="text-xs text-gray-400">The SIM card requires a PIN to unlock. Enter the 4-8 digit PIN below.</p>
        <div class="flex items-center gap-2">
          <input type="password" v-model="settingsPinInput" maxlength="8" placeholder="SIM PIN"
            class="flex-1 px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" />
          <button @click="unlockSettingsPIN" :disabled="settingsPinUnlocking"
            class="px-4 py-2 rounded bg-amber-600 text-white text-sm hover:bg-amber-500 disabled:opacity-50">
            {{ settingsPinUnlocking ? 'Unlocking...' : 'Unlock SIM' }}
          </button>
        </div>
        <div v-if="settingsPinError" class="text-xs text-red-400">{{ settingsPinError }}</div>
      </div>

      <!-- SMS Configuration -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mb-4">
        <h4 class="text-sm font-medium text-gray-200">SMS Configuration</h4>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Destination Numbers (comma-separated)</label>
          <input v-model="cellularForm.sms_destinations" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="+1234567890,+0987654321">
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Allowed Senders (comma-separated, empty = all)</label>
          <input v-model="cellularForm.allowed_senders" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="+1234567890">
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">SMS Prefix</label>
            <input v-model="cellularForm.sms_prefix" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Max Segments</label>
            <input v-model.number="cellularForm.max_segments" type="number" min="1" max="10" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
      </div>

      <!-- Data Connection -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mb-4">
        <h4 class="text-sm font-medium text-gray-200">Data Connection</h4>
        <div>
          <label class="block text-xs text-gray-500 mb-1">APN</label>
          <input v-model="cellularForm.apn" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="internet">
        </div>
        <div class="flex gap-4">
          <label class="flex items-center gap-1 text-xs text-gray-400">
            <input type="checkbox" v-model="cellularForm.auto_connect" class="rounded bg-gray-900 border-gray-700"> Auto-connect on boot
          </label>
          <label class="flex items-center gap-1 text-xs text-gray-400">
            <input type="checkbox" v-model="cellularForm.auto_reconnect" class="rounded bg-gray-900 border-gray-700"> Auto-reconnect on drop
          </label>
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">APN Failover List (comma-separated, tried in order if primary fails)</label>
          <input v-model="cellularForm.apn_failover_list" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="internet,data,broadband">
        </div>
        <div class="flex gap-2">
          <button @click="store.connectCellularData(cellularForm.apn)" class="px-3 py-1.5 rounded bg-emerald-600 text-white text-xs hover:bg-emerald-500">Connect</button>
          <button @click="store.disconnectCellularData()" class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs hover:bg-gray-600">Disconnect</button>
        </div>
      </div>

      <!-- Webhooks -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mb-4">
        <h4 class="text-sm font-medium text-gray-200">Webhooks</h4>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Outbound URL</label>
          <input v-model="cellularForm.webhook_url" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="https://example.com/webhook">
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Outbound Headers (JSON)</label>
          <input v-model="cellularForm.webhook_headers" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" placeholder='{"Authorization": "Bearer ..."}'>
        </div>
        <div class="flex gap-4">
          <label class="flex items-center gap-1 text-xs text-gray-400"><input type="checkbox" v-model="cellularForm.inbound_webhook_enabled" class="rounded bg-gray-900 border-gray-700"> Inbound webhook</label>
        </div>
        <div v-if="cellularForm.inbound_webhook_enabled">
          <label class="block text-xs text-gray-500 mb-1">Inbound Secret</label>
          <input v-model="cellularForm.inbound_webhook_secret" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono">
        </div>
      </div>

      <!-- DynDNS -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mb-4">
        <h4 class="text-sm font-medium text-gray-200">Dynamic DNS</h4>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Provider</label>
            <select v-model="cellularForm.dyndns_provider" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <option value="none">None</option>
              <option value="duckdns">DuckDNS</option>
              <option value="noip">No-IP</option>
              <option value="dynu">Dynu</option>
              <option value="cloudflare">Cloudflare</option>
              <option value="custom">Custom</option>
            </select>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Update Interval (sec)</label>
            <input v-model.number="cellularForm.dyndns_interval" type="number" min="60" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div v-if="cellularForm.dyndns_provider !== 'none'">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Domain</label>
            <input v-model="cellularForm.dyndns_domain" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" :placeholder="cellularForm.dyndns_provider === 'cloudflare' ? 'meshsat.example.com' : 'mydevice.duckdns.org'">
          </div>
          <div v-if="cellularForm.dyndns_provider === 'cloudflare'" class="mt-3">
            <label class="block text-xs text-gray-500 mb-1">Zone ID</label>
            <input v-model="cellularForm.dyndns_zone_id" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="Cloudflare Zone ID (from domain overview)">
          </div>
          <div class="mt-3">
            <label class="block text-xs text-gray-500 mb-1">{{ cellularForm.dyndns_provider === 'cloudflare' ? 'API Token' : 'Token / Credentials' }}</label>
            <input v-model="cellularForm.dyndns_token" type="password" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
      </div>

      <!-- Enable + Save -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center gap-2">
          <input type="checkbox" v-model="cellularEnabled" id="cellular_en" class="rounded bg-gray-900 border-gray-700">
          <label for="cellular_en" class="text-xs text-gray-400">Enable Cellular gateway</label>
        </div>
        <button @click="saveCellular" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save Cellular Config</button>
      </div>

      <!-- SIM Cards -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mt-4">
        <div class="flex items-center justify-between">
          <h4 class="text-sm font-medium text-gray-200">SIM Cards</h4>
          <button @click="showSimForm = true" class="px-3 py-1 rounded bg-teal-600 text-white text-xs hover:bg-teal-500">+ Add</button>
        </div>

        <!-- Current SIM indicator -->
        <div v-if="store.cellularStatus?.iccid" class="flex items-center gap-2 px-2 py-1.5 rounded bg-sky-900/20 border border-sky-800/30">
          <span class="w-1.5 h-1.5 rounded-full bg-sky-400" />
          <span class="text-[10px] text-sky-400">Current SIM:</span>
          <span class="text-[10px] text-gray-300 font-mono">{{ store.cellularStatus.iccid }}</span>
          <span v-if="store.cellularStatus.sim_label" class="text-[10px] text-sky-300">({{ store.cellularStatus.sim_label }})</span>
        </div>

        <!-- Add/Edit form -->
        <div v-if="showSimForm" class="bg-gray-900 rounded p-3 border border-gray-700 space-y-2">
          <div>
            <label class="block text-[10px] text-gray-500 mb-1">ICCID</label>
            <div class="flex gap-2">
              <input v-model="simForm.iccid" class="flex-1 px-2 py-1.5 rounded bg-gray-800 border border-gray-700 text-xs text-gray-200 font-mono" placeholder="8931..." :disabled="!!editingSim">
              <button v-if="!editingSim" @click="readCurrentICCID" :disabled="simReadingICCID"
                class="px-2 py-1.5 rounded bg-gray-700 text-gray-300 text-[10px] hover:bg-gray-600 disabled:opacity-40 whitespace-nowrap">
                {{ simReadingICCID ? 'Reading...' : 'Read from Modem' }}
              </button>
            </div>
          </div>
          <div class="grid grid-cols-2 gap-2">
            <div>
              <label class="block text-[10px] text-gray-500 mb-1">Label</label>
              <input v-model="simForm.label" class="w-full px-2 py-1.5 rounded bg-gray-800 border border-gray-700 text-xs text-gray-200" placeholder="My SIM">
            </div>
            <div>
              <label class="block text-[10px] text-gray-500 mb-1">Phone Number</label>
              <input v-model="simForm.phone" class="w-full px-2 py-1.5 rounded bg-gray-800 border border-gray-700 text-xs text-gray-200 font-mono" placeholder="+31612345678">
            </div>
          </div>
          <div class="grid grid-cols-2 gap-2">
            <div>
              <label class="block text-[10px] text-gray-500 mb-1">PIN Code</label>
              <input v-model="simForm.pin" type="password" class="w-full px-2 py-1.5 rounded bg-gray-800 border border-gray-700 text-xs text-gray-200 font-mono" placeholder="1234">
            </div>
            <div>
              <label class="block text-[10px] text-gray-500 mb-1">Notes</label>
              <input v-model="simForm.notes" class="w-full px-2 py-1.5 rounded bg-gray-800 border border-gray-700 text-xs text-gray-200" placeholder="Optional">
            </div>
          </div>
          <div class="text-[9px] text-gray-600">PIN is stored locally for auto-unlock when this SIM is inserted.</div>
          <div class="flex gap-2">
            <button @click="saveSim" :disabled="!simForm.iccid" class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs hover:bg-teal-500 disabled:opacity-40">
              {{ editingSim ? 'Update' : 'Add' }}
            </button>
            <button @click="cancelSim" class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs hover:bg-gray-600">Cancel</button>
          </div>
        </div>

        <!-- SIM card list -->
        <div v-if="(store.simCards || []).length === 0 && !showSimForm" class="text-xs text-gray-500 py-2">No SIM cards saved yet. Add one to remember its settings.</div>
        <div v-for="s in store.simCards" :key="s.id" class="flex items-center justify-between py-1.5 border-b border-gray-700 last:border-0">
          <div class="flex items-center gap-2">
            <span class="w-1.5 h-1.5 rounded-full" :class="store.cellularStatus?.iccid === s.iccid ? 'bg-sky-400' : 'bg-gray-600'" />
            <span class="text-sm text-gray-200">{{ s.label || 'Unnamed' }}</span>
            <span class="text-[10px] text-gray-500 font-mono">{{ s.iccid.slice(0, 6) }}...{{ s.iccid.slice(-4) }}</span>
            <span v-if="s.phone" class="text-xs text-gray-400 font-mono">{{ s.phone }}</span>
            <span v-if="s.pin" class="px-1 py-0.5 rounded text-[9px] bg-amber-900/30 text-amber-400">PIN</span>
            <span v-if="store.cellularStatus?.iccid === s.iccid" class="px-1.5 py-0.5 rounded text-[9px] bg-sky-900/30 text-sky-300">active</span>
          </div>
          <div class="flex items-center gap-1">
            <span v-if="s.last_seen" class="text-[9px] text-gray-600 mr-2">{{ new Date(s.last_seen).toLocaleDateString() }}</span>
            <button @click="editSim(s)" class="px-2 py-1 rounded bg-gray-700 text-gray-300 text-[10px] hover:bg-gray-600">Edit</button>
            <button @click="deleteSim(s.id)" class="px-2 py-1 rounded bg-gray-700 text-red-400 text-[10px] hover:bg-gray-600">Del</button>
          </div>
        </div>
      </div>

      <!-- SMS Contacts -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mt-4">
        <div class="flex items-center justify-between">
          <h4 class="text-sm font-medium text-gray-200">SMS Contacts</h4>
          <button @click="showContactForm = true" class="px-3 py-1 rounded bg-teal-600 text-white text-xs hover:bg-teal-500">+ Add</button>
        </div>

        <!-- Add/Edit form -->
        <div v-if="showContactForm" class="bg-gray-900 rounded p-3 border border-gray-700 space-y-2">
          <div class="grid grid-cols-2 gap-2">
            <div>
              <label class="block text-[10px] text-gray-500 mb-1">Name</label>
              <input v-model="contactForm.name" class="w-full px-2 py-1.5 rounded bg-gray-800 border border-gray-700 text-xs text-gray-200" placeholder="Alice">
            </div>
            <div>
              <label class="block text-[10px] text-gray-500 mb-1">Phone</label>
              <input v-model="contactForm.phone" class="w-full px-2 py-1.5 rounded bg-gray-800 border border-gray-700 text-xs text-gray-200 font-mono" placeholder="+1234567890">
            </div>
          </div>
          <div>
            <label class="block text-[10px] text-gray-500 mb-1">Notes</label>
            <input v-model="contactForm.notes" class="w-full px-2 py-1.5 rounded bg-gray-800 border border-gray-700 text-xs text-gray-200" placeholder="Optional notes">
          </div>
          <label class="flex items-center gap-1 text-xs text-gray-400">
            <input type="checkbox" v-model="contactForm.auto_fwd" class="rounded bg-gray-800 border-gray-600"> Auto-forward SMS from this contact to mesh
          </label>
          <div class="flex gap-2">
            <button @click="saveContact" class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs hover:bg-teal-500">
              {{ editingContact ? 'Update' : 'Add' }}
            </button>
            <button @click="cancelContact" class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs hover:bg-gray-600">Cancel</button>
          </div>
        </div>

        <!-- Contact list -->
        <div v-if="(store.smsContacts || []).length === 0 && !showContactForm" class="text-xs text-gray-500 py-2">No contacts yet.</div>
        <div v-for="c in store.smsContacts" :key="c.id" class="flex items-center justify-between py-1.5 border-b border-gray-700 last:border-0">
          <div class="flex items-center gap-2">
            <span class="text-sm text-gray-200">{{ c.name }}</span>
            <span class="text-xs text-gray-500 font-mono">{{ c.phone }}</span>
            <span v-if="c.auto_fwd" class="px-1.5 py-0.5 rounded text-[9px] bg-teal-900 text-teal-300">auto-fwd</span>
            <span v-if="c.notes" class="text-[10px] text-gray-600">{{ c.notes }}</span>
          </div>
          <div class="flex items-center gap-1">
            <button @click="editContact(c)" class="px-2 py-1 rounded bg-gray-700 text-gray-300 text-[10px] hover:bg-gray-600">Edit</button>
            <button @click="deleteContact(c.id)" class="px-2 py-1 rounded bg-gray-700 text-red-400 text-[10px] hover:bg-gray-600">Del</button>
          </div>
        </div>
      </div>
    </div>

    <!-- ZigBee -->
    <div v-if="activeTab === 'zigbee'" class="space-y-4">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-gray-200">ZigBee 3.0 Coordinator</span>
          <span class="text-xs" :class="zigbeeStatus?.connected ? 'text-emerald-400' : 'text-gray-500'">
            {{ zigbeeStatus?.connected ? 'Connected' : 'Disconnected' }}
          </span>
        </div>

        <div v-if="zigbeeStatus?.firmware" class="text-[11px] text-gray-500">
          Firmware: {{ zigbeeStatus.firmware }}
          <span v-if="zigbeeStatus?.uptime" class="ml-3">Uptime: {{ zigbeeStatus.uptime }}</span>
        </div>

        <div>
          <label class="block text-xs text-gray-500 mb-1">Serial Port</label>
          <input v-model="zigbeeForm.serial_port" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="auto">
          <p class="text-[10px] text-gray-600 mt-0.5">"auto" scans USB ports for CC2652P/CC2531 coordinator dongles</p>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Default Dest Address</label>
            <input v-model.number="zigbeeForm.default_dst_addr" type="number" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <p class="text-[10px] text-gray-600 mt-0.5">65535 = broadcast (0xFFFF)</p>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Default Endpoint</label>
            <input v-model.number="zigbeeForm.default_dst_ep" type="number" min="1" max="240" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Default Cluster ID</label>
            <input v-model.number="zigbeeForm.default_cluster" type="number" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            <p class="text-[10px] text-gray-600 mt-0.5">6 = On/Off, 8 = Level Control</p>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Inbound Mesh Channel</label>
            <input v-model.number="zigbeeForm.inbound_channel" type="number" min="0" max="7" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>

        <div class="flex flex-wrap gap-4">
          <label class="flex items-center gap-1 text-xs text-gray-400">
            <input type="checkbox" v-model="zigbeeForm.forward_all" class="rounded bg-gray-900 border-gray-700">
            Forward all mesh messages to ZigBee
          </label>
        </div>

        <div class="flex items-center gap-2">
          <input type="checkbox" v-model="zigbeeEnabled" id="zigbee_en" class="rounded bg-gray-900 border-gray-700">
          <label for="zigbee_en" class="text-xs text-gray-400">Enable ZigBee gateway</label>
        </div>
        <button @click="saveZigBee" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save ZigBee Config</button>
      </div>

      <!-- Paired Devices -->
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium text-gray-200">Paired Devices ({{ zigbeeDevices.length }})</span>
          <button @click="fetchZigBeeDevices" class="text-xs text-teal-400 hover:text-teal-300">Refresh</button>
        </div>
        <div v-if="zigbeeDevices.length === 0" class="text-xs text-gray-500 py-2">
          No devices paired yet. Pair a ZigBee device by putting it in pairing mode.
        </div>
        <div v-else class="divide-y divide-gray-700/50">
          <div v-for="dev in zigbeeDevices" :key="dev.short_addr" class="py-2 flex items-center justify-between text-xs">
            <div>
              <span class="text-gray-200 font-mono">0x{{ dev.short_addr.toString(16).padStart(4, '0').toUpperCase() }}</span>
              <span v-if="dev.ieee_addr" class="text-gray-500 ml-2 font-mono">{{ dev.ieee_addr }}</span>
            </div>
            <div class="flex items-center gap-3">
              <span class="text-gray-500">EP {{ dev.endpoint }}</span>
              <span :class="dev.lqi > 150 ? 'text-emerald-400' : dev.lqi > 80 ? 'text-amber-400' : 'text-red-400'">
                LQI {{ dev.lqi }}
              </span>
              <span class="text-gray-600" v-if="dev.last_seen">
                {{ new Date(dev.last_seen).toLocaleTimeString() }}
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Position -->
    <div v-if="activeTab === 'position'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <h4 class="text-sm font-medium text-gray-200">Share Position</h4>
        <p class="text-xs text-gray-500">Broadcast MeshSat's location as a position packet to the mesh.</p>
        <div class="grid grid-cols-3 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Latitude</label>
            <input v-model.number="posForm.latitude" type="number" step="0.000001" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Longitude</label>
            <input v-model.number="posForm.longitude" type="number" step="0.000001" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Altitude (m)</label>
            <input v-model.number="posForm.altitude" type="number" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="flex gap-2">
          <button @click="doSendPosition" :disabled="positionSending" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500 disabled:opacity-40">
            {{ positionSending ? 'Sending...' : 'Send Position' }}
          </button>
        </div>
      </div>
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3 mt-4">
        <h4 class="text-sm font-medium text-gray-200">Fixed Position</h4>
        <p class="text-xs text-gray-500">Set a fixed GPS position on the device (disables GPS module, uses this position).</p>
        <div class="flex gap-2">
          <button @click="doSetFixedPosition" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Set Fixed Position</button>
          <button @click="doRemoveFixedPosition" class="px-4 py-2 rounded bg-gray-700 text-gray-300 text-sm hover:bg-gray-600">Remove Fixed</button>
        </div>
      </div>
    </div>

    <!-- Canned Messages -->
    <div v-if="activeTab === 'canned'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <h4 class="text-sm font-medium text-gray-200">Canned Messages</h4>
        <p class="text-xs text-gray-500">Configure quick-send messages on the device. Separate messages with pipe (|) characters.</p>
        <button @click="loadCannedMessages" :disabled="cannedLoading" class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs hover:text-teal-400 disabled:opacity-40">
          {{ cannedLoading ? 'Requesting...' : 'Request from Device' }}
        </button>
        <div>
          <label class="block text-xs text-gray-500 mb-1">Messages (pipe-separated)</label>
          <textarea v-model="cannedText" rows="4" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" placeholder="OK|Help|SOS|Returning to base|Position report"></textarea>
        </div>
        <button @click="saveCannedMessages" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save to Device</button>
      </div>
    </div>

    <!-- Device MQTT Module -->
    <div v-if="activeTab === 'device_mqtt'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-gray-200">Device Built-in MQTT</span>
          <button @click="refreshDeviceMqtt" class="text-xs text-gray-400 hover:text-teal-400">Refresh from Device</button>
        </div>
        <p class="text-xs text-gray-500">Configure the Meshtastic device's built-in MQTT module. This is separate from MeshSat's MQTT gateway.</p>
        <div v-if="!deviceMqttEditing">
          <div class="bg-gray-900 rounded-lg border border-gray-700 overflow-hidden">
            <div class="flex items-center px-4 py-2 bg-gray-900">
              <span class="w-1/2 text-gray-400 text-sm">Enabled</span>
              <span :class="deviceMqttForm.enabled ? 'text-emerald-400' : 'text-gray-600'" class="text-sm">{{ deviceMqttForm.enabled ? 'Yes' : 'No' }}</span>
            </div>
            <div class="flex items-center px-4 py-2 bg-gray-800/50">
              <span class="w-1/2 text-gray-400 text-sm">Broker Address</span>
              <span class="text-gray-200 text-sm font-mono">{{ deviceMqttForm.address || '—' }}</span>
            </div>
            <div class="flex items-center px-4 py-2 bg-gray-900">
              <span class="w-1/2 text-gray-400 text-sm">Username</span>
              <span class="text-gray-200 text-sm font-mono">{{ deviceMqttForm.username || '—' }}</span>
            </div>
            <div class="flex items-center px-4 py-2 bg-gray-800/50">
              <span class="w-1/2 text-gray-400 text-sm">Root Topic</span>
              <span class="text-gray-200 text-sm font-mono">{{ deviceMqttForm.root || '—' }}</span>
            </div>
            <div class="flex items-center px-4 py-2 bg-gray-900">
              <span class="w-1/2 text-gray-400 text-sm">Encryption</span>
              <span :class="deviceMqttForm.encryption_enabled ? 'text-emerald-400' : 'text-gray-600'" class="text-sm">{{ deviceMqttForm.encryption_enabled ? 'enabled' : 'disabled' }}</span>
            </div>
            <div class="flex items-center px-4 py-2 bg-gray-800/50">
              <span class="w-1/2 text-gray-400 text-sm">JSON Output</span>
              <span :class="deviceMqttForm.json_enabled ? 'text-emerald-400' : 'text-gray-600'" class="text-sm">{{ deviceMqttForm.json_enabled ? 'enabled' : 'disabled' }}</span>
            </div>
            <div class="flex items-center px-4 py-2 bg-gray-900">
              <span class="w-1/2 text-gray-400 text-sm">TLS</span>
              <span :class="deviceMqttForm.tls_enabled ? 'text-emerald-400' : 'text-gray-600'" class="text-sm">{{ deviceMqttForm.tls_enabled ? 'enabled' : 'disabled' }}</span>
            </div>
          </div>
          <button @click="loadDeviceMqtt(); deviceMqttEditing = true" class="mt-3 px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs hover:text-teal-400">Edit</button>
        </div>
        <div v-else class="space-y-3">
          <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
            <label class="flex items-center gap-2 text-sm text-gray-300">
              <input type="checkbox" v-model="deviceMqttForm.enabled" class="rounded bg-gray-900 border-gray-700">
              Enabled
            </label>
            <label class="flex items-center gap-2 text-sm text-gray-300">
              <input type="checkbox" v-model="deviceMqttForm.tls_enabled" class="rounded bg-gray-900 border-gray-700">
              TLS
            </label>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Broker Address</label>
            <input v-model="deviceMqttForm.address" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" placeholder="mqtt.meshtastic.org">
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label class="block text-xs text-gray-500 mb-1">Username</label>
              <input v-model="deviceMqttForm.username" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            </div>
            <div>
              <label class="block text-xs text-gray-500 mb-1">Password</label>
              <input v-model="deviceMqttForm.password" type="password" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
            </div>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Root Topic</label>
            <input v-model="deviceMqttForm.root" class="w-full px-2 py-1.5 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" placeholder="msh/US">
          </div>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
            <label class="flex items-center gap-2 text-sm text-gray-300">
              <input type="checkbox" v-model="deviceMqttForm.encryption_enabled" class="rounded bg-gray-900 border-gray-700">
              Encryption Enabled
            </label>
            <label class="flex items-center gap-2 text-sm text-gray-300">
              <input type="checkbox" v-model="deviceMqttForm.json_enabled" class="rounded bg-gray-900 border-gray-700">
              JSON Output
            </label>
          </div>
          <div class="flex gap-2">
            <button @click="deviceMqttEditing = false" class="px-3 py-1.5 rounded bg-gray-700 text-gray-300 text-xs">Cancel</button>
            <button @click="saveDeviceMqtt" class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs">Apply to Device</button>
          </div>
        </div>
      </div>
    </div>

    <!-- Store & Forward -->
    <div v-if="activeTab === 'store_forward'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <h4 class="text-sm font-medium text-gray-200">Store & Forward</h4>
        <p class="text-xs text-gray-500">Request missed messages from a Store & Forward server node. The S&F node must have the store_forward module enabled.</p>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">S&F Server Node ID (decimal)</label>
            <input v-model.number="sfForm.node_id" type="number" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="e.g. 1234567890">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">History Window (seconds)</label>
            <input v-model.number="sfForm.window" type="number" min="60" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <button @click="doRequestSF" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Request History</button>
        <p class="text-[10px] text-gray-600">Responses will appear as messages in the Messages view via SSE events.</p>
      </div>
    </div>

    <!-- Range Test -->
    <div v-if="activeTab === 'range_test'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <h4 class="text-sm font-medium text-gray-200">Range Test</h4>
        <p class="text-xs text-gray-500">Send a range test packet. Receiving nodes with Range Test enabled will log it.</p>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Text (optional)</label>
            <input v-model="rtForm.text" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="auto-generated if empty">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">To Node (0 = broadcast)</label>
            <input v-model.number="rtForm.to" type="number" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <button @click="doSendRangeTest" :disabled="rtSending" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500 disabled:opacity-40">
          {{ rtSending ? 'Sending...' : 'Send Range Test' }}
        </button>
      </div>
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 mt-4">
        <h4 class="text-sm font-medium text-gray-200 mb-3">Range Test History</h4>
        <div v-if="store.rangeTests.length === 0" class="text-xs text-gray-500">No range test results yet.</div>
        <div v-else class="space-y-2">
          <div v-for="rt in store.rangeTests" :key="rt.id" class="flex items-center justify-between text-xs bg-gray-900 rounded px-3 py-2">
            <div>
              <span class="text-gray-400">{{ rt.from_node }}</span>
              <span class="text-gray-600 mx-1">&rarr;</span>
              <span class="text-gray-400">{{ rt.to_node || 'broadcast' }}</span>
            </div>
            <div class="flex items-center gap-3">
              <span class="text-gray-500">SNR {{ rt.rx_snr?.toFixed(1) }}</span>
              <span class="text-gray-500">RSSI {{ rt.rx_rssi }}</span>
              <span class="text-gray-600">{{ new Date(rt.created_at).toLocaleTimeString() }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Dead Man's Switch -->
    <div v-if="activeTab === 'deadman'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-4">
        <div>
          <h3 class="text-sm font-semibold text-gray-200 mb-1">Dead Man's Switch</h3>
          <p class="text-xs text-gray-500">Auto-send SOS if no activity for a configured period. When triggered, sends SOS with last GPS position to all transports.</p>
        </div>

        <!-- Enable toggle -->
        <div class="flex items-center justify-between">
          <label for="deadman_en" class="text-sm text-gray-300">Enable Dead Man's Switch</label>
          <div class="flex items-center gap-2">
            <input type="checkbox" v-model="deadmanLocalEnabled" id="deadman_en" class="rounded bg-gray-900 border-gray-700">
          </div>
        </div>

        <!-- Timeout -->
        <div class="flex items-center justify-between">
          <label for="deadman_timeout" class="text-sm text-gray-300">Timeout (minutes)</label>
          <input v-model.number="deadmanLocalTimeout" id="deadman_timeout" type="number" min="1" max="10080"
            class="w-24 px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono text-right" />
        </div>

        <!-- Status -->
        <div v-if="store.deadmanConfig" class="space-y-2 pt-2 border-t border-gray-700">
          <div class="flex items-center justify-between">
            <span class="text-xs text-gray-500">Status</span>
            <span class="text-xs font-mono"
              :class="store.deadmanConfig.triggered ? 'text-red-400' : store.deadmanConfig.enabled ? 'text-emerald-400' : 'text-gray-500'">
              {{ store.deadmanConfig.triggered ? 'TRIGGERED' : store.deadmanConfig.enabled ? 'Armed' : 'Disabled' }}
            </span>
          </div>
          <div v-if="deadmanLastActivity" class="flex items-center justify-between">
            <span class="text-xs text-gray-500">Last activity</span>
            <span class="text-xs font-mono text-gray-300">{{ deadmanLastActivity }}</span>
          </div>
        </div>

        <!-- Warning -->
        <div class="bg-amber-900/10 border border-amber-700/30 rounded-lg p-3">
          <p class="text-xs text-amber-400/80">When triggered, sends SOS with last GPS position to all transports. The switch resets on any user activity.</p>
        </div>

        <!-- Save button -->
        <button @click="saveDeadman" :disabled="deadmanSaving"
          class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500 disabled:opacity-40">
          {{ deadmanSaving ? 'Saving...' : 'Save' }}
        </button>
      </div>
    </div>

    <!-- TAK -->
    <div v-if="activeTab === 'tak'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-gray-200">TAK Gateway (CoT over TCP)</span>
          <span v-if="takGw" class="text-xs" :class="takGw.connected ? 'text-emerald-400' : 'text-gray-500'">
            {{ takGw.connected ? 'Connected' : 'Disconnected' }}
          </span>
        </div>
        <div class="grid grid-cols-3 gap-3">
          <div class="col-span-2">
            <label class="block text-xs text-gray-500 mb-1">Host</label>
            <input v-model="takForm.tak_host" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="tak-server.local">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Port</label>
            <input v-model.number="takForm.tak_port" type="number" min="1" max="65535" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div class="flex items-center gap-2">
            <input type="checkbox" v-model="takForm.tak_ssl" id="tak_ssl" class="rounded bg-gray-900 border-gray-700">
            <label for="tak_ssl" class="text-xs text-gray-400">Use TLS/SSL</label>
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Protocol</label>
            <select v-model="takForm.protocol" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
              <option value="xml">XML (CoT v2.0)</option>
              <option value="protobuf">Protobuf (TAK Protocol v1)</option>
            </select>
          </div>
        </div>
        <div v-if="takForm.tak_ssl" class="space-y-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Certificate File (PEM)</label>
            <input v-model="takForm.cert_file" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" placeholder="/path/to/cert.pem">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Key File (PEM)</label>
            <input v-model="takForm.key_file" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" placeholder="/path/to/key.pem">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">CA File (optional)</label>
            <input v-model="takForm.ca_file" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200 font-mono" placeholder="/path/to/ca.pem">
          </div>
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-xs text-gray-500 mb-1">Callsign Prefix</label>
            <input v-model="takForm.callsign_prefix" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200" placeholder="MESHSAT">
          </div>
          <div>
            <label class="block text-xs text-gray-500 mb-1">Coalesce (s)</label>
            <input v-model.number="takForm.coalesce_seconds" type="number" min="1" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
          </div>
        </div>
        <div>
          <label class="block text-xs text-gray-500 mb-1">CoT Stale Seconds</label>
          <input v-model.number="takForm.cot_stale_seconds" type="number" min="1" class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-sm text-gray-200">
        </div>
        <div class="flex items-center gap-2">
          <input type="checkbox" v-model="takEnabled" id="tak_en" class="rounded bg-gray-900 border-gray-700">
          <label for="tak_en" class="text-xs text-gray-400">Enable TAK gateway</label>
        </div>
        <button @click="saveTAK" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">Save TAK Config</button>
        <p class="text-xs text-gray-500">Connects to an OpenTAK Server via TCP/TLS. Forwards mesh positions, SOS, telemetry and chat as CoT XML events. Callsign format: PREFIX-XXXX (last 4 hex of node ID).</p>
      </div>
    </div>

    <!-- Credentials -->
    <div v-if="activeTab === 'credentials'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-4">
        <div>
          <h3 class="text-sm font-semibold text-gray-200 mb-1">TLS Certificates & Provider Credentials</h3>
          <p class="text-xs text-gray-500">Upload ZIP or PEM files from providers (Cloudloop, Astrocast, etc.). Certificates are encrypted at rest.</p>
        </div>

        <!-- Upload -->
        <div class="border border-dashed border-gray-600 rounded-lg p-4 text-center">
          <input type="file" ref="credFileInput" accept=".zip,.pem,.crt,.key,.cer" @change="onCredFileSelected" class="hidden">
          <button @click="$refs.credFileInput.click()" class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500">
            Select ZIP or PEM File
          </button>
          <p v-if="credFileName" class="text-xs text-gray-400 mt-2">{{ credFileName }}</p>
          <div v-if="credFile" class="mt-3 flex items-center gap-2 justify-center">
            <select v-model="credProvider" class="px-2 py-1 rounded bg-gray-900 border border-gray-700 text-xs text-gray-200">
              <option value="">Select provider...</option>
              <option value="cloudloop_mqtt">Cloudloop MQTT</option>
              <option value="cloudloop_api">Cloudloop API</option>
              <option value="rockblock">RockBLOCK</option>
              <option value="astrocast">Astrocast</option>
              <option value="globalstar">Globalstar</option>
              <option value="hub_mqtt">Hub MQTT</option>
              <option value="tak">TAK</option>
              <option value="custom">Custom</option>
            </select>
            <input v-model="credName" placeholder="Label" class="px-2 py-1 rounded bg-gray-900 border border-gray-700 text-xs text-gray-200 w-32">
            <button @click="doUploadCred" :disabled="credUploading || !credProvider"
              class="px-3 py-1 rounded bg-emerald-600 text-white text-xs hover:bg-emerald-500 disabled:opacity-40">
              {{ credUploading ? 'Uploading...' : 'Upload' }}
            </button>
          </div>
          <p v-if="credUploadResult" class="text-xs text-emerald-400 mt-2">{{ credUploadResult }}</p>
        </div>

        <!-- Credential list -->
        <div v-if="store.credentials.length > 0">
          <h4 class="text-xs font-medium text-gray-400 mb-2">Stored Credentials</h4>
          <div class="space-y-2">
            <div v-for="c in store.credentials" :key="c.id"
              class="flex items-center justify-between bg-gray-900 rounded px-3 py-2 border border-gray-700">
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2">
                  <span class="text-xs font-medium text-gray-200">{{ c.name }}</span>
                  <span class="text-[10px] px-1.5 py-0.5 rounded bg-gray-700 text-gray-400">{{ c.provider }}</span>
                  <span class="text-[10px] px-1.5 py-0.5 rounded" :class="credExpiryClass(c)">{{ credExpiryLabel(c) }}</span>
                  <span v-if="c.source === 'hub'" class="text-[10px] px-1.5 py-0.5 rounded bg-blue-900 text-blue-300">Hub</span>
                </div>
                <div class="text-[10px] text-gray-500 mt-0.5">
                  {{ c.cred_type }} | v{{ c.version }}
                  <span v-if="c.cert_subject"> | {{ c.cert_subject }}</span>
                  <span v-if="c.cert_fingerprint"> | {{ c.cert_fingerprint.substring(0, 16) }}...</span>
                </div>
              </div>
              <div class="flex gap-1 ml-2">
                <button v-if="!c.applied" @click="store.applyCredential(c.id)"
                  class="px-2 py-1 rounded bg-teal-700 text-white text-[10px] hover:bg-teal-600">Apply</button>
                <span v-else class="text-[10px] text-emerald-400 px-2 py-1">Active</span>
                <button @click="store.deleteCredential(c.id)"
                  class="px-2 py-1 rounded bg-red-900 text-red-300 text-[10px] hover:bg-red-800">Delete</button>
              </div>
            </div>
          </div>
        </div>
        <p v-else class="text-xs text-gray-500 text-center py-4">No credentials stored. Upload a certificate ZIP or PEM file above.</p>
      </div>
    </div>

    <!-- Routing / Reticulum -->
    <div v-if="activeTab === 'routing'">
      <div class="space-y-4">

        <!-- TCP Interface Config -->
        <div class="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <h3 class="text-sm font-medium text-gray-200 mb-3">TCP Interface</h3>
          <div class="grid grid-cols-2 gap-3 mb-3">
            <div>
              <label class="text-[10px] text-gray-500 block mb-1">Listen Port</label>
              <input type="number" v-model.number="routingForm.listen_port" min="1024" max="65535"
                class="w-full bg-gray-900 border border-gray-700 rounded px-2 py-1 text-xs text-gray-200">
            </div>
            <div>
              <label class="text-[10px] text-gray-500 block mb-1">Announce Interval (sec)</label>
              <input type="number" v-model.number="routingForm.announce_interval" min="30" max="3600"
                class="w-full bg-gray-900 border border-gray-700 rounded px-2 py-1 text-xs text-gray-200">
            </div>
          </div>
          <div class="flex items-center gap-2">
            <button @click="saveRoutingConfig" class="px-3 py-1 bg-teal-700 text-white text-xs rounded hover:bg-teal-600">Save</button>
            <span v-if="routingForm.listen_addr" class="text-[10px] text-gray-500">Currently listening on {{ routingForm.listen_addr }}</span>
          </div>
          <p v-if="routingWarning" class="mt-2 text-[10px] text-amber-400 bg-amber-900/20 rounded px-2 py-1.5 border border-amber-800/40">{{ routingWarning }}</p>
        </div>

        <!-- TCP Peers -->
        <div class="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <h3 class="text-sm font-medium text-gray-200 mb-3">TCP Peers</h3>
          <div class="flex gap-2 mb-3">
            <input type="text" v-model="newPeerAddr" placeholder="host:port (e.g. 192.168.1.10:4242)"
              @keyup.enter="addPeer"
              class="flex-1 bg-gray-900 border border-gray-700 rounded px-2 py-1 text-xs text-gray-200 placeholder-gray-600">
            <button @click="addPeer" :disabled="!newPeerAddr"
              class="px-3 py-1 bg-teal-700 text-white text-xs rounded hover:bg-teal-600 disabled:opacity-40">Add</button>
          </div>
          <div v-if="routingPeers.length === 0" class="text-xs text-gray-500 text-center py-2">No peers configured. Add a remote bridge address above.</div>
          <div v-else class="space-y-1.5">
            <div v-for="peer in routingPeers" :key="peer.address"
              class="flex items-center justify-between bg-gray-900 rounded px-3 py-1.5 border border-gray-700">
              <div class="flex items-center gap-2">
                <span class="text-xs font-mono text-gray-200">{{ peer.address }}</span>
                <span class="text-[10px] px-1.5 py-0.5 rounded"
                  :class="peer.connected ? 'bg-green-900/40 text-green-400' : 'bg-gray-700 text-gray-500'">
                  {{ peer.connected ? 'connected' : 'disconnected' }}
                </span>
                <span class="text-[10px] text-gray-600">{{ peer.direction }}</span>
              </div>
              <button v-if="peer.dynamic" @click="removePeer(peer.address)"
                class="px-2 py-0.5 rounded bg-red-900 text-red-300 text-[10px] hover:bg-red-800">Remove</button>
              <span v-else class="text-[10px] text-gray-600">env</span>
            </div>
          </div>
        </div>

        <!-- Hub Connection -->
        <div class="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <h3 class="text-sm font-medium text-gray-200 mb-1">Hub Connection</h3>
          <p class="text-xs text-gray-500 mb-3">Paste the MQTT credentials from the Hub Fleet page to connect this bridge via WSS (port 443).</p>
          <div class="space-y-2 mb-3">
            <div>
              <label class="text-[10px] text-gray-500 block mb-1">MQTT URL</label>
              <input type="text" v-model="hubForm.url" placeholder="wss://hub.meshsat.net/mqtt"
                class="w-full bg-gray-900 border border-gray-700 rounded px-2 py-1 text-xs text-gray-200 font-mono placeholder-gray-600">
              <span class="text-[9px] text-gray-600 mt-0.5 block">Supports tcp://, ssl://, ws://, wss:// schemes</span>
            </div>
            <div class="grid grid-cols-2 gap-2">
              <div>
                <label class="text-[10px] text-gray-500 block mb-1">Bridge ID</label>
                <input type="text" v-model="hubForm.bridge_id" placeholder="rocket01"
                  class="w-full bg-gray-900 border border-gray-700 rounded px-2 py-1 text-xs text-gray-200 font-mono placeholder-gray-600">
              </div>
              <div>
                <label class="text-[10px] text-gray-500 block mb-1">Username</label>
                <input type="text" v-model="hubForm.username" placeholder="rocket01"
                  class="w-full bg-gray-900 border border-gray-700 rounded px-2 py-1 text-xs text-gray-200 font-mono placeholder-gray-600">
              </div>
            </div>
            <div>
              <label class="text-[10px] text-gray-500 block mb-1">Password</label>
              <input type="password" v-model="hubForm.password" placeholder="paste from Fleet page (shown only once)"
                class="w-full bg-gray-900 border border-gray-700 rounded px-2 py-1 text-xs text-gray-200 font-mono placeholder-gray-600">
            </div>
            <div>
              <label class="text-[10px] text-gray-500 block mb-1">Client Certificate PEM <span class="text-gray-600">(from Fleet > Issue TLS Certificate)</span></label>
              <textarea v-model="hubForm.tls_cert_pem" rows="3" placeholder="-----BEGIN CERTIFICATE-----"
                class="w-full bg-gray-900 border border-gray-700 rounded px-2 py-1 text-xs text-gray-200 font-mono placeholder-gray-600 resize-y"></textarea>
            </div>
            <div>
              <label class="text-[10px] text-gray-500 block mb-1">Client Private Key PEM</label>
              <textarea v-model="hubForm.tls_key_pem" rows="3" placeholder="-----BEGIN EC PRIVATE KEY-----"
                class="w-full bg-gray-900 border border-gray-700 rounded px-2 py-1 text-xs text-gray-200 font-mono placeholder-gray-600 resize-y"></textarea>
            </div>
            <span v-if="hubForm.has_cert" class="text-[10px] text-emerald-400">mTLS certificate configured</span>
          </div>
          <div class="flex items-center gap-2">
            <button @click="saveHubConfig" class="px-3 py-1 bg-teal-700 text-white text-xs rounded hover:bg-teal-600">Save</button>
            <span v-if="hubForm.has_password" class="text-[10px] text-emerald-400">credentials saved</span>
            <span v-else class="text-[10px] text-gray-500">not configured</span>
          </div>
          <div v-if="hubWarning" class="mt-2 flex items-center gap-2 text-[10px] text-amber-400 bg-amber-900/20 rounded px-2 py-1.5 border border-amber-800/40">
            <span class="flex-1">{{ hubWarning }}</span>
            <button v-if="!restarting" @click="restartBridge"
              class="shrink-0 px-2 py-0.5 bg-amber-700 text-white rounded hover:bg-amber-600">Restart Now</button>
            <span v-else class="shrink-0 text-amber-300">Restarting...</span>
          </div>
        </div>

        <!-- Flood Control -->
        <div class="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <h3 class="text-sm font-medium text-gray-200 mb-1">Flood Control</h3>
          <p class="text-xs text-gray-500 mb-3">Paid transports excluded from path discovery by default.</p>
          <div v-if="store.routingInterfaces.length === 0" class="text-xs text-gray-500 text-center py-2">No Reticulum interfaces registered.</div>
          <div v-else class="space-y-1.5">
            <div v-for="iface in store.routingInterfaces" :key="iface.id"
              class="flex items-center justify-between bg-gray-900 rounded px-3 py-1.5 border border-gray-700">
              <div class="flex items-center gap-3">
                <span class="text-xs font-mono text-gray-200">{{ iface.id }}</span>
                <span class="text-[10px] px-1.5 py-0.5 rounded"
                  :class="iface.online ? 'bg-green-900/40 text-green-400' : 'bg-gray-700 text-gray-500'">
                  {{ iface.online ? 'online' : 'offline' }}
                </span>
                <span v-if="iface.cost > 0" class="text-[10px] px-1.5 py-0.5 rounded bg-amber-900/40 text-amber-400">${{ iface.cost }}/msg</span>
                <span v-else class="text-[10px] px-1.5 py-0.5 rounded bg-gray-700 text-gray-400">free</span>
              </div>
              <label class="flex items-center gap-2 cursor-pointer">
                <span class="text-[10px] text-gray-500">flood</span>
                <input type="checkbox" :checked="iface.floodable" @change="toggleFloodable(iface)"
                  class="rounded bg-gray-900 border-gray-700">
              </label>
            </div>
          </div>
        </div>

        <div class="bg-gray-800/50 rounded-lg p-3 border border-gray-700/50">
          <p class="text-[10px] text-gray-500 leading-relaxed">
            <strong class="text-gray-400">Floodable</strong> interfaces receive path discovery requests and announce broadcasts. Enabling on paid transports means every path request generates a message on that interface. <strong class="text-gray-400">Directed sends</strong> (known routes) always use the best interface regardless of this setting.
          </p>
        </div>
      </div>
    </div>

    <!-- Config Export/Import -->
    <div v-if="activeTab === 'config_mgmt'">
      <div class="space-y-4">
        <!-- Export -->
        <div class="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <h3 class="text-sm font-medium text-gray-200 mb-3">Export Running Config</h3>
          <p class="text-xs text-gray-500 mb-3">Download the current interface configuration as YAML (Cisco-style running-config format).</p>
          <div class="flex gap-2 mb-3">
            <button @click="doExportConfig" :disabled="exporting"
              class="px-4 py-2 rounded bg-teal-600 text-white text-sm hover:bg-teal-500 disabled:opacity-50">
              {{ exporting ? 'Exporting...' : 'Export Config' }}
            </button>
            <button v-if="exportedConfig" @click="downloadConfig"
              class="px-4 py-2 rounded bg-gray-700 text-gray-300 text-sm hover:bg-gray-600">
              Download YAML
            </button>
          </div>
          <div v-if="exportedConfig">
            <pre class="text-[11px] font-mono text-gray-400 whitespace-pre-wrap break-all bg-gray-900 rounded p-3 max-h-[400px] overflow-y-auto select-all border border-gray-700">{{ exportedConfig }}</pre>
          </div>
        </div>

        <!-- Import -->
        <div class="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <h3 class="text-sm font-medium text-gray-200 mb-3">Import Config</h3>
          <p class="text-xs text-gray-500 mb-3">Paste a YAML config to import. This will merge/overwrite current interface and rule configuration.</p>
          <textarea v-model="importText" rows="8" placeholder="Paste YAML config here..."
            class="w-full px-3 py-2 rounded bg-gray-900 border border-gray-700 text-xs text-gray-200 font-mono mb-3"></textarea>
          <button @click="doImportConfig" :disabled="importing || !importText.trim()"
            class="px-4 py-2 rounded bg-amber-600 text-white text-sm hover:bg-amber-500 disabled:opacity-50">
            {{ importing ? 'Importing...' : 'Import Config' }}
          </button>
          <div v-if="importResult" class="mt-3 p-3 rounded text-sm"
            :class="importResult.error ? 'bg-red-900/20 border border-red-700 text-red-300' : 'bg-emerald-900/20 border border-emerald-700 text-emerald-300'">
            {{ importResult.error || importResult.message || 'Config imported successfully' }}
          </div>
        </div>
      </div>
    </div>

    <!-- About -->
    <div v-if="activeTab === 'about'">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700 space-y-3">
        <div class="flex items-center justify-between">
          <span class="text-xs text-gray-500">Version</span>
          <span class="text-sm text-gray-300">0.2.0</span>
        </div>
        <div class="flex items-center justify-between">
          <span class="text-xs text-gray-500">Mode</span>
          <span class="text-sm text-gray-300">{{ store.status?.transport || 'unknown' }}</span>
        </div>
        <div class="flex items-center justify-between">
          <span class="text-xs text-gray-500">Radio Connected</span>
          <span class="text-sm" :class="store.status?.connected ? 'text-emerald-400' : 'text-red-400'">{{ store.status?.connected ? 'Yes' : 'No' }}</span>
        </div>
        <div class="flex items-center justify-between">
          <span class="text-xs text-gray-500">Nodes</span>
          <span class="text-sm text-gray-300">{{ store.status?.num_nodes || 0 }}</span>
        </div>
      </div>

      <!-- Factory Reset (Meshtastic node) -->
      <div class="bg-red-900/10 rounded-lg p-4 border border-red-800/30 mt-4">
        <h4 class="text-sm font-medium text-red-400 mb-2">Factory Reset</h4>
        <p class="text-xs text-gray-500 mb-3">Send a factory reset command to the connected Meshtastic node. This will erase all settings on the radio and reset it to defaults.</p>
        <div v-if="!factoryResetConfirm" class="flex items-center gap-2">
          <button @click="factoryResetConfirm = true"
            class="px-4 py-2 rounded bg-red-900/30 text-red-400 text-xs border border-red-700/40 hover:bg-red-900/50">
            Factory Reset Node
          </button>
        </div>
        <div v-else class="space-y-2">
          <p class="text-xs text-red-300">Are you sure? This cannot be undone. Enter the node ID to confirm.</p>
          <div class="flex items-center gap-2">
            <input v-model="factoryResetNodeId" placeholder="Node ID (decimal)" type="number"
              class="flex-1 px-3 py-2 rounded bg-gray-900 border border-red-700/40 text-sm text-gray-200 font-mono" />
            <button @click="doFactoryReset" :disabled="!factoryResetNodeId"
              class="px-4 py-2 rounded bg-red-600 text-white text-xs hover:bg-red-500 disabled:opacity-40">
              Confirm Reset
            </button>
            <button @click="factoryResetConfirm = false"
              class="px-4 py-2 rounded bg-gray-700 text-gray-300 text-xs hover:bg-gray-600">
              Cancel
            </button>
          </div>
          <div v-if="factoryResetResult" class="text-xs" :class="factoryResetResult.includes('sent') ? 'text-amber-400' : 'text-red-400'">
            {{ factoryResetResult }}
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
