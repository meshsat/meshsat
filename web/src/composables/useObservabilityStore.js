import { ref, computed } from 'vue'
import api from '../api/client'

// Unified observability store — all bridge data sources for the Hubble view.
const interfaces = ref([])
const gateways = ref([])
const meshNodes = ref([])
const meshTopology = ref(null)
const reticulumIdentity = ref(null)
const reticulumPeers = ref([])
const accessRules = ref([])
const bondGroups = ref([])
const hembStats = ref(null)
const deliveryStats = ref([])
const bridgeStatus = ref(null)

const events = ref([])
const sseHandle = ref(null)
const hembSseHandle = ref(null)
const connected = ref(false)
const MAX_EVENTS = 500

// Selected node/edge for flow table filtering
const selectedNodeId = ref(null)

export function useObservabilityStore() {

  // ── Data fetchers ──

  async function fetchInterfaces() {
    try { interfaces.value = await api.get('/interfaces') } catch {}
  }

  async function fetchGateways() {
    try {
      const d = await api.get('/gateways')
      gateways.value = d?.gateways || d || []
    } catch {}
  }

  async function fetchMeshNodes() {
    try {
      const d = await api.get('/nodes')
      meshNodes.value = Array.isArray(d) ? d : []
    } catch {}
  }

  async function fetchMeshTopology() {
    try { meshTopology.value = await api.get('/topology') } catch {}
  }

  async function fetchReticulum() {
    try {
      const [id, peers] = await Promise.all([
        api.get('/routing/identity').catch(() => null),
        api.get('/routing/peers').catch(() => []),
      ])
      reticulumIdentity.value = id
      reticulumPeers.value = Array.isArray(peers) ? peers : []
    } catch {}
  }

  async function fetchAccessRules() {
    try { accessRules.value = await api.get('/access-rules') } catch {}
  }

  async function fetchBondGroups() {
    try { bondGroups.value = await api.get('/bond-groups') } catch {}
  }

  async function fetchHembStats() {
    try { hembStats.value = await api.get('/hemb/stats') } catch {}
  }

  async function fetchDeliveryStats() {
    try { deliveryStats.value = await api.get('/deliveries/stats') } catch {}
  }

  async function fetchBridgeStatus() {
    try { bridgeStatus.value = await api.get('/status') } catch {}
  }

  // ── SSE streams ──

  function connectSSE() {
    closeSSE()
    // Main bridge events
    sseHandle.value = api.sse('/events', (event) => {
      if (!event?.type || event.type === 'subscribed') return
      events.value.unshift({
        ts: new Date().toISOString(),
        type: event.type,
        source: 'bridge',
        payload: event.data || event,
      })
      while (events.value.length > MAX_EVENTS) events.value.pop()
    })
    // HeMB events
    hembSseHandle.value = api.sse('/hemb/events?replay=50', (event) => {
      if (!event?.type || event.type === 'hemb_connected') {
        connected.value = true
        return
      }
      events.value.unshift({
        ts: event.ts || new Date().toISOString(),
        type: event.type,
        source: 'hemb',
        payload: event.payload || event,
      })
      while (events.value.length > MAX_EVENTS) events.value.pop()
    })
    connected.value = true
  }

  function closeSSE() {
    sseHandle.value?.close?.()
    hembSseHandle.value?.close?.()
    sseHandle.value = null
    hembSseHandle.value = null
    connected.value = false
  }

  // ── Fetch all (initial + periodic) ──

  async function fetchAll() {
    await Promise.all([
      fetchBridgeStatus(),
      fetchInterfaces(),
      fetchGateways(),
      fetchMeshNodes(),
      fetchMeshTopology(),
      fetchReticulum(),
      fetchAccessRules(),
      fetchBondGroups(),
      fetchHembStats(),
      fetchDeliveryStats(),
    ])
  }

  // ── Computed graph data ──

  const interfaceNodes = computed(() => {
    return interfaces.value.map(iface => {
      const gw = gateways.value.find(g => g.instance_id === iface.id)
      return {
        id: iface.id,
        type: 'interface',
        channelType: iface.channel_type || iface.id.replace(/_\d+$/, ''),
        label: iface.label || iface.id,
        state: gw ? (gw.connected ? 'online' : 'offline') : iface.state,
        enabled: iface.enabled,
        messagesIn: gw?.messages_in || 0,
        messagesOut: gw?.messages_out || 0,
      }
    })
  })

  const meshNodeList = computed(() => {
    return meshNodes.value.map(n => ({
      id: n.node_id || n.id || '',
      type: 'mesh_node',
      label: n.long_name || n.label || n.node_id || '',
      hwModel: n.hw_model_name || '',
      snr: n.snr || 0,
      battery: n.battery || 0,
      online: n.online !== false,
      hops: n.hops_away,
    }))
  })

  const peerNodes = computed(() => {
    return reticulumPeers.value.map(p => ({
      id: p.address,
      type: 'rns_peer',
      label: p.address,
      direction: p.direction,
      connected: p.connected,
    }))
  })

  const ruleEdges = computed(() => {
    return accessRules.value
      .filter(r => r.enabled)
      .map(r => ({
        id: `rule-${r.id}`,
        source: r.interface_id,
        target: r.forward_to,
        label: r.name,
        matchCount: r.match_count,
        type: 'access_rule',
      }))
  })

  const onlineCount = computed(() =>
    interfaceNodes.value.filter(n => n.state === 'online').length
  )

  function selectNode(nodeId) {
    selectedNodeId.value = selectedNodeId.value === nodeId ? null : nodeId
  }

  const filteredEvents = computed(() => {
    if (!selectedNodeId.value) return events.value
    const id = selectedNodeId.value
    return events.value.filter(e => {
      const p = e.payload
      if (typeof p === 'object') {
        if (p.interface_id === id || p.source_iface === id) return true
        if (p.bearer_type === id || p.bearer_idx !== undefined) return true
        if (p.forward_to === id) return true
      }
      if (e.type?.includes(id)) return true
      return false
    })
  })

  return {
    // State
    interfaces, gateways, meshNodes, meshTopology,
    reticulumIdentity, reticulumPeers, accessRules,
    bondGroups, hembStats, deliveryStats, bridgeStatus,
    events, connected, selectedNodeId,
    // Computed
    interfaceNodes, meshNodeList, peerNodes, ruleEdges,
    onlineCount, filteredEvents,
    // Methods
    fetchAll, connectSSE, closeSSE, selectNode,
    fetchInterfaces, fetchGateways, fetchMeshNodes,
    fetchHembStats,
  }
}
