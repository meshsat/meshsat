import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true } },
  { path: '/', name: 'dashboard', component: () => import('@/views/DashboardView.vue') },
  { path: '/messages', name: 'messages', component: () => import('@/views/MessagesView.vue') },
  { path: '/nodes', name: 'nodes', component: () => import('@/views/NodesView.vue') },
  { path: '/bridge', name: 'bridge', component: () => import('@/views/BridgeView.vue') },
  { path: '/interfaces', name: 'interfaces', component: () => import('@/views/InterfacesView.vue') },
  { path: '/passes', name: 'passes', component: () => import('@/views/PassesView.vue') },
  { path: '/topology', name: 'topology', component: () => import('@/views/TopologyView.vue') },
  { path: '/map', name: 'map', component: () => import('@/views/MapView.vue') },
  { path: '/settings', name: 'settings', component: () => import('@/views/SettingsView.vue') },
  { path: '/keys', name: 'keys', component: () => import('@/views/ApiKeysView.vue'), meta: { minRole: 'owner' } },
  { path: '/audit', name: 'audit', component: () => import('@/views/AuditView.vue') },
  { path: '/help', name: 'help', component: () => import('@/views/HelpView.vue') },
  { path: '/about', name: 'about', component: () => import('@/views/AboutView.vue') },
  { path: '/:pathMatch(.*)*', redirect: '/' }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

// Auth guard — redirects to /login when auth is enabled and user is not authenticated.
// The auth store is lazily imported to avoid circular dependency issues.
let authStoreInstance = null

router.beforeEach(async (to) => {
  // Public routes are always accessible
  if (to.meta.public) return true

  // Lazy-load auth store
  if (!authStoreInstance) {
    const { useAuthStore } = await import('@/stores/auth')
    const { createPinia } = await import('pinia')
    // If pinia is already installed, useAuthStore() will work; otherwise it needs the app
    try {
      authStoreInstance = useAuthStore()
    } catch {
      // Pinia not yet installed — skip guard on first load (App.vue will handle it)
      return true
    }
  }

  // Wait for initial auth check to complete
  if (authStoreInstance.loading) {
    await authStoreInstance.checkAuth()
  }

  // If auth is disabled, allow everything
  if (!authStoreInstance.authEnabled) return true

  // If not authenticated, redirect to login
  if (!authStoreInstance.isAuthenticated) {
    return { name: 'login' }
  }

  return true
})

export default router
