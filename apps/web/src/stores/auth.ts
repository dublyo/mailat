import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, authApi, type User } from '@/lib/api'
import { useReceivedInboxStore } from './receivedInbox'
import { useInboxStore } from './inbox'
import { useDomainsStore } from './domains'
import { useSettingsStore } from './settings'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const token = ref<string | null>(null)
  const isLoading = ref(false)
  const challengeToken = ref<string | null>(null)
  const isInitialized = ref(false)
  // Set when the session could not be checked (network or server error). The
  // token is kept so a retry can restore the session without signing in again.
  const authError = ref<string | null>(null)

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

  // An involuntary logout (expired or revoked session) keeps the legacy
  // browser-only mail rules for import; a user-initiated one clears them.
  function logout(options: { keepLegacyRules?: boolean } = {}) {
    const currentToken = api.getToken()
    if (currentToken) void authApi.logout(currentToken).catch(() => { /* Local logout remains possible offline. */ })
    challengeToken.value = null
    useReceivedInboxStore().reset()
    useInboxStore().closeCompose()
    useDomainsStore().reset()
    useSettingsStore().clearLocalSettings({ keepLegacyRules: !!options.keepLegacyRules })
    authError.value = null
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
      authError.value = null
    } catch (e) {
      // Only a rejected credential ends the session. An outage or 5xx must not
      // sign the user out of every tab.
      const status = (e as { status?: number }).status
      if (status === 401 || status === 403) {
        logout({ keepLegacyRules: true })
      } else {
        authError.value = 'Mailat could not reach the server to restore your session.'
      }
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
    checkAuth,
    authError
  }
})
