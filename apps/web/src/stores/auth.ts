import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, authApi, type User } from '@/lib/api'
import { useReceivedInboxStore } from './receivedInbox'
import { useInboxStore } from './inbox'
import { useDomainsStore } from './domains'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const token = ref<string | null>(null)
  const isLoading = ref(false)
  const challengeToken = ref<string | null>(null)
  const isInitialized = ref(false)

  const isAuthenticated = computed(() => !!user.value && !!token.value)

  async function login(email: string, password: string) {
    isLoading.value = true
    try {
      const response = await authApi.login(email, password)
      if (response.requiresTwoFactor) {
        challengeToken.value = response.challengeToken
        token.value = null
        user.value = null
        api.setToken(null)
        return false
      }
      challengeToken.value = null
      token.value = response.token
      user.value = response.user
      api.setToken(response.token)
      return true
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

  async function verifyChallenge(code: string) {
    if (!challengeToken.value) throw new Error('Sign in again to request a verification code.')
    const result = await authApi.completeChallenge(challengeToken.value, code)
    challengeToken.value = null
    token.value = result.token
    user.value = result.user
    api.setToken(result.token)
  }

  function logout() {
    const currentToken = api.getToken()
    if (currentToken) void authApi.logout(currentToken).catch(() => { /* Local logout remains possible offline. */ })
    challengeToken.value = null
    useReceivedInboxStore().reset()
    useInboxStore().closeCompose()
    useDomainsStore().reset()
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
    challengeToken,
    verifyChallenge,
    register,
    logout,
    checkAuth
  }
})
