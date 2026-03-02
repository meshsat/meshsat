import { createRouter, createWebHistory } from 'vue-router'
import DashboardView from '@/views/DashboardView.vue'

const routes = [
  { path: '/', name: 'dashboard', component: DashboardView },
  { path: '/messages', name: 'messages', component: () => import('@/views/MessagesView.vue') },
  { path: '/nodes', name: 'nodes', component: () => import('@/views/NodesView.vue') },
  { path: '/map', name: 'map', component: () => import('@/views/MapView.vue') },
  { path: '/telemetry', name: 'telemetry', component: () => import('@/views/TelemetryView.vue') },
  { path: '/config', name: 'config', component: () => import('@/views/ConfigView.vue') },
  { path: '/channels', name: 'channels', component: () => import('@/views/ChannelsView.vue') },
  { path: '/gateways', name: 'gateways', component: () => import('@/views/GatewaysView.vue') }
]

export default createRouter({
  history: createWebHistory(),
  routes
})
