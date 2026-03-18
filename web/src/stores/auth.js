/**
 * Auth Store — OAuth2/OIDC session management + RBAC role tracking.
 *
 * Checks /auth/status on init to determine if auth is enabled.
 * When enabled, guards routes and provides user info, role, + logout.
 */
import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

export const useAuthStore = defineStore('auth', () => {
  const user = ref(null)
  const role = ref(null) // 'owner', 'operator', 'viewer'
  const authEnabled = ref(false)
  const loading = ref(true)
  const error = ref(null)

  const isAuthenticated = computed(() => !authEnabled.value || !!user.value)
  const displayName = computed(() => {
    if (!user.value) return ''
    return user.value.name || user.value.preferred_username || user.value.email || 'User'
  })

  // Role hierarchy check
  const roleRank = { viewer: 1, operator: 2, owner: 3 }
  function hasRole(minRole) {
    if (!authEnabled.value) return true // auth disabled = full access
    return (roleRank[role.value] || 0) >= (roleRank[minRole] || 0)
  }

  async function checkAuth() {
    loading.value = true
    error.value = null
    try {
      const res = await fetch('/auth/status')
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data = await res.json()

      authEnabled.value = data.enabled
      if (data.authenticated && data.user) {
        user.value = data.user
        // Fetch role from /auth/me (which includes role context)
        if (data.enabled) {
          try {
            const meRes = await fetch('/auth/me')
            if (meRes.ok) {
              const meData = await meRes.json()
              // Role comes from session context; default to 'owner' for OIDC users
              role.value = meData.role || 'owner'
            }
          } catch {
            role.value = 'owner' // default for OIDC session users
          }
        }
      } else {
        user.value = null
        role.value = null
      }
    } catch (e) {
      error.value = e.message
      authEnabled.value = false
      user.value = null
      role.value = null
    } finally {
      loading.value = false
    }
  }

  function login() {
    window.location.href = '/auth/login'
  }

  async function logout() {
    try {
      await fetch('/auth/logout', { method: 'POST' })
    } catch {
      // Ignore errors — cookie will be cleared by redirect
    }
    user.value = null
    role.value = null
    window.location.href = '/auth/login'
  }

  async function refreshSession() {
    try {
      const res = await fetch('/auth/refresh', { method: 'POST' })
      if (res.ok) {
        const data = await res.json()
        if (data.user) user.value = data.user
      }
    } catch {
      // Silent fail — session will expire naturally
    }
  }

  return {
    user,
    role,
    authEnabled,
    loading,
    error,
    isAuthenticated,
    displayName,
    hasRole,
    checkAuth,
    login,
    logout,
    refreshSession
  }
})
