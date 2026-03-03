import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  { path: '/', name: 'dashboard', component: () => import('@/views/DashboardView.vue') },
  { path: '/messages', name: 'messages', component: () => import('@/views/MessagesView.vue') },
  { path: '/nodes', name: 'nodes', component: () => import('@/views/NodesView.vue') },
  { path: '/bridge', name: 'bridge', component: () => import('@/views/BridgeView.vue') },
  { path: '/map', name: 'map', component: () => import('@/views/MapView.vue') },
  { path: '/settings', name: 'settings', component: () => import('@/views/SettingsView.vue') }
]

export default createRouter({
  history: createWebHistory(),
  routes
})
