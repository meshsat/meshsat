<script setup>
defineProps({
  status: { type: String, default: 'received' }
})

const statusMap = {
  received: { icon: 'M5 13l4 4L19 7', color: 'text-gray-400', label: 'Received' },
  queued: { icon: 'M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z', color: 'text-amber-400', label: 'Queued' },
  sending: { icon: 'M13 10V3L4 14h7v7l9-11h-7z', color: 'text-blue-400', label: 'Sending' },
  sent: { icon: 'M5 13l4 4L19 7', color: 'text-emerald-400', label: 'Sent' },
  delivered: { icon: 'M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z', color: 'text-emerald-400', label: 'Delivered' },
  retry: { icon: 'M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15', color: 'text-amber-400', label: 'Retry' },
  failed: { icon: 'M6 18L18 6M6 6l12 12', color: 'text-red-400', label: 'Failed' },
  dead: { icon: 'M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636', color: 'text-red-500', label: 'Dead' },
  cancelled: { icon: 'M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z', color: 'text-gray-500', label: 'Cancelled' },
  // Legacy statuses (backwards compat)
  mesh_sent: { icon: 'M5 13l4 4L19 7', color: 'text-amber-400', label: 'Sent to mesh' },
  mesh_delivered: { icon: 'M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z', color: 'text-emerald-400', label: 'Delivered' },
  bridging: { icon: 'M13 10V3L4 14h7v7l9-11h-7z', color: 'text-blue-400', label: 'Bridging' },
  sat_queued: { icon: 'M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z', color: 'text-blue-400', label: 'Satellite queued' },
  sat_sent: { icon: 'M5 13l4 4L19 7', color: 'text-blue-400', label: 'Satellite sent' },
  confirmed: { icon: 'M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z', color: 'text-emerald-400', label: 'Confirmed' },
}
</script>

<template>
  <span :class="(statusMap[status] || statusMap.received).color" :title="(statusMap[status] || statusMap.received).label"
    class="inline-flex items-center gap-1">
    <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="2">
      <path stroke-linecap="round" stroke-linejoin="round" :d="(statusMap[status] || statusMap.received).icon" />
    </svg>
    <span class="text-[10px]">{{ (statusMap[status] || statusMap.received).label }}</span>
  </span>
</template>
