import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, authApi, pushApi, type LoginResponse, type User } from '@/lib/api'
import { endDevicePush } from '@/lib/push'
import { deviceTokenFor, forgetDevice, rememberDevice } from '@/lib/loginPin'
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

  // The login PIN step (after the password, and after TOTP when it is on).
  // setup is true when the account has no PIN yet and must choose one.
  const pinChallenge = ref<{ token: string; setup: boolean } | null>(null)

  function adoptSession(response: LoginResponse & { token: string; user: User }) {
    if (response.deviceToken) rememberDevice(response.user.email, response.deviceToken, response.deviceTokenExpiresAt)
    challengeToken.value = null
    pinChallenge.value = null
    token.value = response.token
    user.value = response.user
    api.setToken(response.token)
  }

  // Applies a sign-in result: a session (true) or the next step (false).
  function applyStep(response: LoginResponse): boolean {
    if (response.token && response.user) {
      adoptSession(response as LoginResponse & { token: string; user: User })
      return true
    }
    token.value = null
    user.value = null
    api.setToken(null)
    if (response.requiresTwoFactor && response.challengeToken) {
      challengeToken.value = response.challengeToken
      pinChallenge.value = null
      return false
    }
    if ((response.requiresPin || response.requiresPinSetup) && response.challengeToken) {
      challengeToken.value = null
      pinChallenge.value = { token: response.challengeToken, setup: !!response.requiresPinSetup }
      return false
    }
    throw new Error('Sign in did not complete. Please try again.')
  }

  async function login(email: string, password: string) {
    isLoading.value = true
    try {
      const deviceToken = deviceTokenFor(email)
      const response = await authApi.login(email, password, deviceToken)
      // A remembered device the server no longer accepts is dropped here.
      if (deviceToken && (response.requiresPin || response.requiresPinSetup)) forgetDevice(email)
      return applyStep(response)
    } finally {
      isLoading.value = false
    }
  }

  async function register(data: { name: string; email: string; password: string }) {
    isLoading.value = true
    try {
      return applyStep(await authApi.register(data))
    } finally {
      isLoading.value = false
    }
  }

  // Completes the TOTP step: true when signed in, false when the PIN follows.
  async function verifyChallenge(code: string) {
    if (!challengeToken.value) throw new Error('Sign in again to request a verification code.')
    return applyStep(await authApi.completeChallenge(challengeToken.value, code))
  }

  // Completes the PIN step (or chooses the first PIN).
  async function submitPin(pin: string, trustDevice: boolean) {
    const step = pinChallenge.value
    if (!step) throw new Error('Sign in again to enter your PIN.')
    const response = step.setup
      ? await authApi.setupPin(step.token, pin, trustDevice)
      : await authApi.verifyPin(step.token, pin, trustDevice)
    return applyStep(response)
  }

  function cancelSignIn() {
    challengeToken.value = null
    pinChallenge.value = null
  }

  // Ends web push in this browser for the account signed in with token: the
  // server is told while the session still authenticates, then the browser
  // subscription is dropped.
  function endPush(token: string | null) {
    const ended = endDevicePush(token ? endpoint => pushApi.unsubscribe(endpoint, token) : null)
      .catch(() => false /* No service worker, or push unavailable in this browser. */)
    // A hung server call must not block sign-out or account switching.
    return Promise.race([ended, new Promise<boolean>(resolve => setTimeout(() => resolve(false), 5000))])
  }

  function clearLocalSession(options: { keepLegacyRules?: boolean }) {
    challengeToken.value = null
    pinChallenge.value = null
    useReceivedInboxStore().reset()
    useInboxStore().closeCompose()
    useDomainsStore().reset()
    useSettingsStore().clearLocalSettings({ keepLegacyRules: !!options.keepLegacyRules })
    authError.value = null
    user.value = null
    token.value = null
    api.setToken(null)
  }

  // A user-initiated logout ends push in this browser, then revokes the
  // session. An involuntary one (expired or rejected session) passes
  // involuntary: it keeps the legacy browser-only mail rules for import and
  // leaves push alone, so an expired session never silently stops new-mail
  // notifications; signing in again keeps them working.
  function logout(options: { involuntary?: boolean } = {}) {
    const currentToken = api.getToken()
    const pushEnded = options.involuntary ? Promise.resolve() : endPush(currentToken)
    void pushEnded.finally(() => {
      if (currentToken) void authApi.logout(currentToken).catch(() => { /* Local logout remains possible offline. */ })
    })
    clearLocalSession({ keepLegacyRules: options.involuntary })
  }

  // Adopts a session issued outside the login form (invite acceptance). Any
  // other account signed in here is signed out first, and its push
  // subscription is ended before the new session is adopted: the new account
  // cannot subscribe this browser until then, so the old account's teardown
  // can never unsubscribe the new account's subscription.
  async function setSession(nextToken: string, nextUser: User) {
    const previous = api.getToken()
    if (previous && previous !== nextToken) {
      clearLocalSession({})
      await endPush(previous)
      void authApi.logout(previous).catch(() => { /* Local logout remains possible offline. */ })
    }
    challengeToken.value = null
    pinChallenge.value = null
    authError.value = null
    token.value = nextToken
    user.value = nextUser
    api.setToken(nextToken)
    isInitialized.value = true
  }

  // Starts the PIN step for a sign-in that began outside the login form
  // (invite acceptance, OAuth). Another account signed in here is signed out
  // first, as setSession does.
  async function beginPinStep(step: { token: string; setup: boolean }) {
    const previous = api.getToken()
    if (previous) {
      clearLocalSession({})
      await endPush(previous)
      void authApi.logout(previous).catch(() => { /* Local logout remains possible offline. */ })
    }
    challengeToken.value = null
    pinChallenge.value = step
    isInitialized.value = true
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
        logout({ involuntary: true })
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
    pinChallenge,
    submitPin,
    beginPinStep,
    cancelSignIn,
    register,
    logout,
    setSession,
    checkAuth,
    authError
  }
})
