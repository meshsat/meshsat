<script setup>
import { ref } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()
const sidebarOpen = ref(false)

const navItems = [
  { path: '/', label: 'Dashboard', icon: 'D' },
  { path: '/messages', label: 'Messages', icon: 'M' },
  { path: '/nodes', label: 'Nodes', icon: 'N' },
  { path: '/map', label: 'Map', icon: 'P' },
  { path: '/telemetry', label: 'Telemetry', icon: 'T' },
  { path: '/channels', label: 'Channels', icon: 'H' },
  { path: '/config', label: 'Config', icon: 'C' },
  { path: '/gateways', label: 'Gateways', icon: 'G' }
]
</script>

<template>
  <div class="min-h-screen bg-gray-950 text-gray-100 flex">
    <!-- Mobile sidebar toggle -->
    <button
      class="fixed top-4 left-4 z-50 lg:hidden p-2 rounded-lg bg-gray-800 text-gray-300 hover:text-white"
      @click="sidebarOpen = !sidebarOpen"
    >
      <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 6h16M4 12h16M4 18h16" />
      </svg>
    </button>

    <!-- Sidebar -->
    <aside
      :class="[
        'fixed inset-y-0 left-0 z-40 w-56 bg-gray-900 border-r border-gray-800 transform transition-transform lg:relative lg:translate-x-0',
        sidebarOpen ? 'translate-x-0' : '-translate-x-full'
      ]"
    >
      <div class="p-5 border-b border-gray-800">
        <h1 class="text-lg font-bold text-teal-400">MeshSat</h1>
        <p class="text-xs text-gray-500 mt-0.5">Meshtastic Gateway</p>
      </div>
      <nav class="p-3 space-y-1">
        <router-link
          v-for="item in navItems"
          :key="item.path"
          :to="item.path"
          class="flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium transition-colors"
          :class="route.path === item.path
            ? 'bg-teal-500/15 text-teal-400'
            : 'text-gray-400 hover:text-gray-200 hover:bg-gray-800'"
          @click="sidebarOpen = false"
        >
          <span class="w-6 h-6 rounded flex items-center justify-center text-xs font-bold bg-gray-800">
            {{ item.icon }}
          </span>
          {{ item.label }}
        </router-link>
      </nav>
    </aside>

    <!-- Sidebar backdrop (mobile) -->
    <div
      v-if="sidebarOpen"
      class="fixed inset-0 z-30 bg-black/50 lg:hidden"
      @click="sidebarOpen = false"
    />

    <!-- Main content -->
    <main class="flex-1 min-w-0 p-4 sm:p-6 lg:p-8">
      <router-view />
    </main>
  </div>
</template>
