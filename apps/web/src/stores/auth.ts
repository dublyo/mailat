import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, authApi, type User } from '@/lib/api'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const token = ref<string | null>(null)
  const isLoading = ref(false)
  const isInitialized = ref(false)

  const isAuthenticated = computed(() => !!user.value && !!token.value)

  async function login(email: string, password: string) {
    isLoading.value = true
    try {
      const response = await authApi.login(email, password)
      token.value = response.token
      user.value = response.user
      api.setToken(response.token)
    } finally {
      isLoading.value = false
    }
  }

  async function register(data: { name: string; email: string; password: string }) {
    isLoading.value = true
    try {
      const response = await authApi.register(data)
      token.value = response.token
      user.value = response.user
      api.setToken(response.token)
    } finally {
      isLoading.value = false
    }
  }

  function logout() {
    user.value = null
    token.value = null
    api.setToken(null)
  }

  async function checkAuth() {
    const storedToken = localStorage.getItem('token')
    if (!storedToken) {
      isInitialized.value = true
      return
    }

    token.value = storedToken
    api.setToken(storedToken)

    try {
      user.value = await authApi.me()
    } catch {
      logout()
    } finally {
      isInitialized.value = true
    }
  }

  return {
    user,
    token,
    isLoading,
    isInitialized,
    isAuthenticated,
    login,
    register,
    logout,
    checkAuth
  }
})
