import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  { path: '/', name: 'dashboard', component: () => import('@/views/DashboardView.vue') },
  { path: '/messages', name: 'messages', component: () => import('@/views/MessagesView.vue') },
  { path: '/nodes', name: 'nodes', component: () => import('@/views/NodesView.vue') },
  { path: '/bridge', name: 'bridge', component: () => import('@/views/BridgeView.vue') },
  { path: '/interfaces', name: 'interfaces', component: () => import('@/views/InterfacesView.vue') },
  { path: '/passes', name: 'passes', component: () => import('@/views/PassesView.vue') },
  { path: '/topology', name: 'topology', component: () => import('@/views/TopologyView.vue') },
  { path: '/map', name: 'map', component: () => import('@/views/MapView.vue') },
  { path: '/settings', name: 'settings', component: () => import('@/views/SettingsView.vue') },
  { path: '/radio', name: 'radio', component: () => import('@/views/RadioConfigView.vue') },
  { path: '/audit', name: 'audit', component: () => import('@/views/AuditView.vue') },
  { path: '/help', name: 'help', component: () => import('@/views/HelpView.vue') },
  { path: '/about', name: 'about', component: () => import('@/views/AboutView.vue') },
  { path: '/:pathMatch(.*)*', redirect: '/' }
]

export default createRouter({
  history: createWebHistory(),
  routes
})
