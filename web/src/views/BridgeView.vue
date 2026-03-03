<script setup>
import { ref, onMounted, computed } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'
import RuleCard from '@/components/RuleCard.vue'
import RuleEditor from '@/components/RuleEditor.vue'

const store = useMeshsatStore()
const editorOpen = ref(false)
const editingRule = ref(null)

const meshGw = computed(() => (store.gateways || []).find(g => g.type === 'mqtt'))
const iridiumGw = computed(() => (store.gateways || []).find(g => g.type === 'iridium'))

function openCreate() {
  editingRule.value = null
  editorOpen.value = true
}

function openEdit(rule) {
  editingRule.value = { ...rule }
  editorOpen.value = true
}

async function saveRule(data) {
  if (editingRule.value?.id) {
    await store.updateRule(editingRule.value.id, data)
  } else {
    await store.createRule(data)
  }
  editorOpen.value = false
}

async function toggleRule(rule) {
  if (rule.enabled) {
    await store.disableRule(rule.id)
  } else {
    await store.enableRule(rule.id)
  }
}

async function removeRule(rule) {
  if (confirm(`Delete rule "${rule.name}"?`)) {
    await store.deleteRule(rule.id)
  }
}

function gwStatusColor(gw) {
  if (!gw) return 'text-gray-500'
  return gw.connected ? 'text-emerald-400' : gw.enabled ? 'text-amber-400' : 'text-gray-500'
}

function gwStatusLabel(gw) {
  if (!gw) return 'Not configured'
  return gw.connected ? 'Connected' : gw.enabled ? 'Disconnected' : 'Disabled'
}

onMounted(() => {
  store.fetchRules()
  store.fetchGateways()
  store.fetchDLQ()
})
</script>

<template>
  <div class="max-w-4xl mx-auto">
    <h2 class="text-lg font-semibold text-gray-200 mb-4">Bridge</h2>

    <!-- Status panes -->
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 mb-6">
      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700">
        <div class="text-xs text-gray-500 mb-1">Mesh Radio</div>
        <div class="flex items-center gap-2">
          <span class="w-2 h-2 rounded-full" :class="store.status?.connected ? 'bg-emerald-400' : 'bg-red-400'"></span>
          <span class="text-sm text-gray-300">{{ store.status?.connected ? 'Connected' : 'Disconnected' }}</span>
        </div>
        <div class="text-xs text-gray-500 mt-1">{{ store.status?.num_nodes || 0 }} nodes</div>
      </div>

      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700">
        <div class="text-xs text-gray-500 mb-1">MQTT Gateway</div>
        <div class="flex items-center gap-2">
          <span class="w-2 h-2 rounded-full" :class="gwStatusColor(meshGw).replace('text-', 'bg-')"></span>
          <span class="text-sm text-gray-300">{{ gwStatusLabel(meshGw) }}</span>
        </div>
        <div v-if="meshGw" class="text-xs text-gray-500 mt-1">In: {{ meshGw.messages_in }} | Out: {{ meshGw.messages_out }}</div>
      </div>

      <div class="bg-gray-800 rounded-lg p-4 border border-gray-700">
        <div class="text-xs text-gray-500 mb-1">Iridium Satellite</div>
        <div class="flex items-center gap-2">
          <span class="w-2 h-2 rounded-full" :class="gwStatusColor(iridiumGw).replace('text-', 'bg-')"></span>
          <span class="text-sm text-gray-300">{{ gwStatusLabel(iridiumGw) }}</span>
        </div>
        <div v-if="iridiumGw" class="text-xs text-gray-500 mt-1">
          In: {{ iridiumGw.messages_in }} | Out: {{ iridiumGw.messages_out }}
          <span v-if="iridiumGw.dlq_pending > 0" class="text-amber-400"> | DLQ: {{ iridiumGw.dlq_pending }}</span>
        </div>
      </div>
    </div>

    <!-- Forwarding rules -->
    <div class="flex items-center justify-between mb-3">
      <h3 class="text-sm font-medium text-gray-300">Forwarding Rules</h3>
      <button @click="openCreate" class="px-3 py-1.5 rounded bg-teal-600 text-white text-xs font-medium hover:bg-teal-500">
        + New Rule
      </button>
    </div>

    <div v-if="store.rules.length === 0" class="text-center text-gray-500 py-8 text-sm bg-gray-800/50 rounded-lg border border-gray-700">
      No forwarding rules configured. Messages stay local until rules are added.
    </div>

    <div class="space-y-3 mb-6">
      <RuleCard v-for="rule in store.rules" :key="rule.id" :rule="rule"
        @toggle="toggleRule(rule)" @edit="openEdit(rule)" @delete="removeRule(rule)" />
    </div>

    <!-- DLQ -->
    <div v-if="store.dlq.length > 0">
      <h3 class="text-sm font-medium text-gray-300 mb-3">Dead-Letter Queue</h3>
      <div class="bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
        <table class="w-full text-xs">
          <thead class="bg-gray-900/50">
            <tr>
              <th class="px-3 py-2 text-left text-gray-500">ID</th>
              <th class="px-3 py-2 text-left text-gray-500">Priority</th>
              <th class="px-3 py-2 text-left text-gray-500">Retries</th>
              <th class="px-3 py-2 text-left text-gray-500">Status</th>
              <th class="px-3 py-2 text-left text-gray-500">Next Retry</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-700">
            <tr v-for="dl in store.dlq" :key="dl.id">
              <td class="px-3 py-2 text-gray-400">{{ dl.id }}</td>
              <td class="px-3 py-2">
                <span :class="dl.priority === 0 ? 'text-red-400' : dl.priority === 2 ? 'text-gray-500' : 'text-amber-400'">
                  {{ dl.priority === 0 ? 'Critical' : dl.priority === 2 ? 'Low' : 'Normal' }}
                </span>
              </td>
              <td class="px-3 py-2 text-gray-400">{{ dl.retries }}/{{ dl.max_retries }}</td>
              <td class="px-3 py-2 text-gray-400">{{ dl.status }}</td>
              <td class="px-3 py-2 text-gray-400">{{ new Date(dl.next_retry).toLocaleTimeString() }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Rule editor modal -->
    <RuleEditor :open="editorOpen" :rule="editingRule" @save="saveRule" @close="editorOpen = false" />
  </div>
</template>
