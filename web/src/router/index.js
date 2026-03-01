import { createRouter, createWebHistory } from 'vue-router'
import MessagesView from '@/views/MessagesView.vue'

const routes = [
  { path: '/', name: 'messages', component: MessagesView },
  { path: '/nodes', name: 'nodes', component: () => import('@/views/NodesView.vue') },
  { path: '/map', name: 'map', component: () => import('@/views/MapView.vue') },
  { path: '/telemetry', name: 'telemetry', component: () => import('@/views/TelemetryView.vue') },
  { path: '/config', name: 'config', component: () => import('@/views/ConfigView.vue') },
  { path: '/gateways', name: 'gateways', component: () => import('@/views/GatewaysView.vue') }
]

export default createRouter({
  history: createWebHistory(),
  routes
})
