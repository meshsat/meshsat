<script setup>
import { onMounted } from 'vue'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()

onMounted(() => {
  // If already authenticated, redirect to dashboard
  if (auth.isAuthenticated && !auth.loading) {
    window.location.href = '/'
  }
})
</script>

<template>
  <div class="min-h-screen bg-tactical-bg flex items-center justify-center">
    <div class="w-full max-w-sm px-6">
      <!-- Logo -->
      <div class="text-center mb-8">
        <img src="/logo-bg.png" alt="MeshSat" class="w-24 h-24 mx-auto mb-4 opacity-30" />
        <h1 class="font-display text-2xl font-bold text-gray-200 tracking-wide">MeshSat</h1>
        <p class="text-sm text-gray-500 mt-1">Multi-Channel Communications Hub</p>
      </div>

      <!-- Login card -->
      <div class="bg-tactical-surface border border-tactical-border rounded-lg p-6">
        <h2 class="text-sm font-medium text-gray-300 mb-4 text-center">Authentication Required</h2>

        <!-- Loading state -->
        <div v-if="auth.loading" class="text-center py-4">
          <div class="w-6 h-6 border-2 border-tactical-iridium/30 border-t-tactical-iridium rounded-full animate-spin mx-auto"></div>
          <p class="text-xs text-gray-500 mt-2">Checking authentication...</p>
        </div>

        <!-- Login button -->
        <div v-else>
          <button
            @click="auth.login()"
            class="w-full py-2.5 px-4 bg-tactical-iridium/20 hover:bg-tactical-iridium/30 border border-tactical-iridium/40 rounded text-sm font-medium text-tactical-iridium transition-colors"
          >
            Sign in with SSO
          </button>

          <p v-if="auth.error" class="text-xs text-red-400 mt-3 text-center">
            {{ auth.error }}
          </p>
        </div>
      </div>

      <p class="text-[10px] text-gray-600 text-center mt-6">
        Secured by OAuth2/OIDC
      </p>
    </div>
  </div>
</template>
