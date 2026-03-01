<script setup>
import { onMounted } from 'vue'
import { useMeshsatStore } from '@/stores/meshsat'

const store = useMeshsatStore()

function formatLastHeard(val) {
  if (!val) return '—'
  const ts = typeof val === 'number' && val < 1e12 ? val * 1000 : val
  const d = new Date(ts)
  if (isNaN(d.getTime())) return String(val)
  const diff = Math.floor((Date.now() - d.getTime()) / 1000)
  if (diff < 60) return `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return d.toLocaleDateString()
}

function signalClass(q) {
  if (!q) return 'text-gray-500'
  const u = q.toUpperCase()
  if (u === 'GOOD') return 'text-emerald-400'
  if (u === 'FAIR') return 'text-amber-400'
  return 'text-red-400'
}

async function handleReboot(node) {
  if (!confirm(`Reboot node ${node.name || node.id}?`)) return
  try {
    await store.adminReboot({ node_id: Number(node.id) || node.id, delay_secs: 5 })
  } catch { /* store error */ }
}

async function handleTraceroute(node) {
  try {
    await store.adminTraceroute({ node_id: Number(node.id) || node.id })
  } catch { /* store error */ }
}

onMounted(() => store.fetchNodes())
</script>

<template>
  <div class="max-w-5xl mx-auto space-y-6">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-bold">Mesh Nodes</h1>
      <button
        @click="store.fetchNodes()"
        class="px-3 py-1.5 text-sm rounded-lg bg-gray-800 text-gray-300 hover:text-white transition-colors"
      >
        Refresh
      </button>
    </div>

    <div v-if="!store.nodes.length" class="bg-gray-900 rounded-xl p-8 border border-gray-800 text-center text-gray-500">
      No nodes discovered
    </div>

    <div v-else class="bg-gray-900 rounded-xl border border-gray-800 overflow-x-auto">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-gray-800 text-left text-xs text-gray-500 uppercase">
            <th class="px-4 py-3">Node</th>
            <th class="px-4 py-3">HW</th>
            <th class="px-4 py-3 text-right">SNR</th>
            <th class="px-4 py-3 text-right">RSSI</th>
            <th class="px-4 py-3 text-center">Signal</th>
            <th class="px-4 py-3 text-right">Battery</th>
            <th class="px-4 py-3 text-right">Last Heard</th>
            <th class="px-4 py-3 text-right">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-800">
          <tr v-for="node in store.nodes" :key="node.id ?? node.node_id ?? node.num"
              class="hover:bg-gray-800/50 transition-colors">
            <td class="px-4 py-3">
              <div class="font-medium text-gray-200">{{ node.name ?? node.long_name ?? '—' }}</div>
              <div class="text-xs text-gray-500">{{ node.short_name ?? '' }} / {{ node.id ?? node.node_id ?? '' }}</div>
            </td>
            <td class="px-4 py-3 text-gray-400">{{ node.hw_model ?? node.hardware ?? '—' }}</td>
            <td class="px-4 py-3 text-right">
              <span v-if="node.snr != null" :class="Number(node.snr) >= 5 ? 'text-emerald-400' : Number(node.snr) >= 0 ? 'text-amber-400' : 'text-red-400'">
                {{ Number(node.snr).toFixed(1) }} dB
              </span>
              <span v-else class="text-gray-500">—</span>
            </td>
            <td class="px-4 py-3 text-right text-gray-400">
              {{ node.rssi != null ? `${node.rssi} dBm` : '—' }}
            </td>
            <td class="px-4 py-3 text-center">
              <span v-if="node.signal_quality"
                    :class="signalClass(node.signal_quality)"
                    class="text-xs font-medium px-2 py-0.5 rounded-full bg-gray-800">
                {{ node.signal_quality }}
              </span>
              <span v-else class="text-gray-500">—</span>
            </td>
            <td class="px-4 py-3 text-right text-gray-400">
              {{ node.battery != null ? `${Math.round(node.battery)}%` : '—' }}
            </td>
            <td class="px-4 py-3 text-right text-gray-500 text-xs">
              {{ formatLastHeard(node.last_heard ?? node.last_seen) }}
            </td>
            <td class="px-4 py-3 text-right">
              <div class="flex items-center justify-end gap-1">
                <button
                  @click="handleTraceroute(node)"
                  class="px-2 py-1 text-xs rounded bg-gray-800 text-gray-400 hover:text-teal-400 transition-colors"
                  title="Traceroute"
                >
                  Trace
                </button>
                <button
                  @click="handleReboot(node)"
                  class="px-2 py-1 text-xs rounded bg-gray-800 text-gray-400 hover:text-amber-400 transition-colors"
                  title="Reboot"
                >
                  Reboot
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
