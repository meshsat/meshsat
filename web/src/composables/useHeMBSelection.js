import { ref, computed } from 'vue'

// Module-level singleton state — shared between HeMBBearerGraph and HeMBFlowTable.
const selectedNodeId = ref(null)
const selectedEdgeId = ref(null)
const selectedStreamId = ref(null)
const selectedBearerId = ref(null)
const highlightedRowId = ref(null)

export function useHeMBSelection() {
  function selectNode(node) {
    selectedNodeId.value = node.id || node
    selectedEdgeId.value = null
    selectedStreamId.value = null
    selectedBearerId.value = null
  }

  function selectEdge(edge) {
    selectedEdgeId.value = edge.id || edge
    selectedStreamId.value = edge.streamId ?? null
    selectedBearerId.value = edge.bearerId ?? null
    selectedNodeId.value = null
  }

  function selectFromTable(event) {
    const data = event.payload ? (typeof event.payload === 'string' ? JSON.parse(event.payload) : event.payload) : {}
    selectedStreamId.value = data.stream_id ?? null
    selectedBearerId.value = data.bearer_idx != null ? String(data.bearer_idx) : (data.bearer_type ?? null)
    selectedNodeId.value = null
    selectedEdgeId.value = null
  }

  function highlightFromTable(event) {
    highlightedRowId.value = event?.ts ?? null
  }

  function clear() {
    selectedNodeId.value = null
    selectedEdgeId.value = null
    selectedStreamId.value = null
    selectedBearerId.value = null
    highlightedRowId.value = null
  }

  function edgeOpacity(edgeId) {
    if (!selectedEdgeId.value && !selectedNodeId.value) return 1.0
    if (selectedEdgeId.value === edgeId) return 1.0
    return 0.2
  }

  function isNodeSelected(nodeId) {
    return selectedNodeId.value === nodeId
  }

  const filterActive = computed(() =>
    selectedNodeId.value != null ||
    selectedEdgeId.value != null ||
    selectedStreamId.value != null ||
    selectedBearerId.value != null
  )

  const eventFilter = computed(() => {
    return (event) => {
      if (!filterActive.value) return true
      const data = event.payload ? (typeof event.payload === 'string' ? JSON.parse(event.payload) : event.payload) : {}
      if (selectedStreamId.value != null && data.stream_id !== selectedStreamId.value) return false
      if (selectedBearerId.value && String(data.bearer_idx) !== selectedBearerId.value && data.bearer_type !== selectedBearerId.value) return false
      return true
    }
  })

  return {
    selectedNodeId,
    selectedEdgeId,
    selectedStreamId,
    selectedBearerId,
    highlightedRowId,
    filterActive,
    eventFilter,
    selectNode,
    selectEdge,
    selectFromTable,
    highlightFromTable,
    clear,
    edgeOpacity,
    isNodeSelected,
  }
}
