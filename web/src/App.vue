<script setup>
import { onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { useMeshsatStore } from '@/stores/meshsat'
import ConnectivityBanner from '@/components/ConnectivityBanner.vue'

const route = useRoute()
const store = useMeshsatStore()

const tabs = [
  { name: 'comms', label: 'Comms', path: '/', icon: 'M8 10h.01M12 10h.01M16 10h.01M9 16H5a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v8a2 2 0 01-2 2h-5l-5 5v-5z' },
  { name: 'nodes', label: 'Nodes', path: '/nodes', icon: 'M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0z' },
  { name: 'bridge', label: 'Bridge', path: '/bridge', icon: 'M13 10V3L4 14h7v7l9-11h-7z' },
  { name: 'map', label: 'Map', path: '/map', icon: 'M9 20l-5.447-2.724A1 1 0 013 16.382V5.618a1 1 0 011.447-.894L9 7m0 13l6-3m-6 3V7m6 10l4.553 2.276A1 1 0 0021 18.382V7.618a1 1 0 00-.553-.894L15 4m0 13V4m0 0L9 7' },
  { name: 'settings', label: 'Settings', path: '/settings', icon: 'M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.066 2.573c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.573 1.066c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.066-2.573c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z M15 12a3 3 0 11-6 0 3 3 0 016 0z' }
]

function isActive(tab) {
  if (tab.path === '/') return route.path === '/'
  return route.path.startsWith(tab.path)
}

let pollTimer = null
onMounted(() => {
  store.fetchStatus()
  store.fetchGateways()
  store.fetchIridiumSignalFast()
  pollTimer = setInterval(() => {
    store.fetchStatus()
    store.fetchIridiumSignalFast()
  }, 10000)
})
onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<template>
  <div class="min-h-screen bg-gray-950 text-gray-100 flex flex-col">
    <!-- Connectivity Banner -->
    <ConnectivityBanner />

    <!-- Main content -->
    <main class="flex-1 pb-16 lg:pb-0 lg:pl-20">
      <div class="p-4 sm:p-6">
        <router-view />
      </div>
    </main>

    <!-- Bottom tab bar (mobile) -->
    <nav class="fixed bottom-0 left-0 right-0 bg-gray-900 border-t border-gray-800 z-40 lg:hidden">
      <div class="flex justify-around">
        <router-link v-for="tab in tabs" :key="tab.name" :to="tab.path"
          class="flex flex-col items-center py-2 px-3 text-xs transition-colors"
          :class="isActive(tab) ? 'text-teal-400' : 'text-gray-500 hover:text-gray-300'">
          <svg class="w-5 h-5 mb-1" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1.5">
            <path stroke-linecap="round" stroke-linejoin="round" :d="tab.icon" />
          </svg>
          <span>{{ tab.label }}</span>
        </router-link>
      </div>
    </nav>

    <!-- Sidebar (desktop) -->
    <nav class="hidden lg:flex fixed left-0 top-0 bottom-0 w-20 bg-gray-900 border-r border-gray-800 flex-col items-center py-6 z-40">
      <img src="/logo.png" alt="MeshSat" class="w-12 h-12 mb-6 opacity-90" />
      <div class="flex-1 flex flex-col gap-1">
        <router-link v-for="tab in tabs" :key="tab.name" :to="tab.path"
          class="flex flex-col items-center py-3 px-2 rounded-lg text-xs transition-colors"
          :class="isActive(tab) ? 'text-teal-400 bg-gray-800' : 'text-gray-500 hover:text-gray-300 hover:bg-gray-800/50'">
          <svg class="w-5 h-5 mb-1" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1.5">
            <path stroke-linecap="round" stroke-linejoin="round" :d="tab.icon" />
          </svg>
          <span>{{ tab.label }}</span>
        </router-link>
      </div>
    </nav>
  </div>
</template>
