<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Eye, EyeOff } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { api, authApi, oauthApi } from '@/lib/api'
import { consumeOAuthNonce, createOAuthNonce } from '@/lib/oauthNonce'
import Button from '@/components/common/Button.vue'
import Spinner from '@/components/common/Spinner.vue'

const router = useRouter()
const route = useRoute()
const authStore = useAuthStore()

// A password reset that could not sign in (two-factor on) lands here with ?email=.
const email = ref(typeof route.query.email === 'string' ? route.query.email : '')
const passwordReset = route.query.passwordReset === '1'
const password = ref('')
const showPassword = ref(false)
const verificationCode = ref('')
const error = ref('')
const isLoading = ref(false)
const registrationOpen = ref(false)
const providers = ref<string[]>([])
const retrying = ref(false)

const providerLabels: Record<string, string> = { google: 'Google', github: 'GitHub', microsoft: 'Microsoft' }

// The API redirects here with a fixed code only; provider text is never shown.
const oauthErrors: Record<string, string> = {
  invalid_state: 'That sign-in attempt expired or was started in another browser. Please try again.',
  provider_error: 'The sign-in provider could not complete the request. Please try again.',
  not_linked: 'This account is not connected to a Mailat user. Sign in with your password, then connect it in Settings → Security.',
  email_unverified: 'The provider has not verified this email address, so it cannot create the first account.',
  registration_closed: 'Registration is closed. Ask your administrator for an invite.',
  rate_limited: 'Too many sign-in attempts. Please wait a few minutes and try again.',
}

function startOAuth(provider: string) {
  window.location.assign(oauthApi.loginUrl(provider, createOAuthNonce()))
}

async function retrySession() {
  retrying.value = true
  try {
    await authStore.checkAuth()
    if (authStore.isAuthenticated) await router.replace(route.query.redirect as string || '/inbox')
  } finally {
    retrying.value = false
  }
}

onMounted(async () => {
  const oauthError = route.query.oauthError
  if (typeof oauthError === 'string') {
    error.value = oauthErrors[oauthError] || oauthErrors.provider_error
    const { oauthError: _removed, ...query } = route.query
    await router.replace({ path: route.path, query, hash: route.hash })
  }
  const params = new URLSearchParams(route.hash.replace(/^#/, ''))
  const challenge = params.get('challenge')
  const session = params.get('session')
  if (challenge || session) {
    await router.replace({ path: route.path, query: route.query, hash: '' })
    // Only adopt a result from a sign-in this tab started (login CSRF).
    if (!consumeOAuthNonce(params.get('nonce'))) {
      error.value = oauthErrors.invalid_state
    } else if (challenge) {
      authStore.challengeToken = challenge
    } else if (session) {
      api.setToken(session)
      await authStore.checkAuth()
      if (authStore.isAuthenticated) { await router.replace('/inbox'); return }
      error.value = 'Unable to complete sign in. Please try again.'
    }
  }
  try {
    const res = await authApi.registerStatus()
    registrationOpen.value = res.open
  } catch {
    // ignore — just hide register link
  }
  try {
    providers.value = (await oauthApi.providers())?.providers ?? []
  } catch {
    providers.value = []
  }
})

const handleSubmit = async () => {
  error.value = ''
  isLoading.value = true

  try {
    if (authStore.challengeToken) {
      await authStore.verifyChallenge(verificationCode.value.trim())
    } else if (!await authStore.login(email.value, password.value)) {
      password.value = ''
      return
    }
    const redirect = route.query.redirect as string || '/inbox'
    router.push(redirect)
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Login failed'
  } finally {
    isLoading.value = false
  }
}
</script>

<template>
  <div class="min-h-screen flex items-center justify-center bg-gmail-lightGray p-4">
    <div class="w-full max-w-md">
      <div class="bg-white rounded-2xl shadow-lg p-8">
        <!-- Logo -->
        <div class="flex items-center justify-center gap-3 mb-8">
          <img src="/logo.jpg" alt="Mailat" class="w-12 h-12 rounded-lg object-contain" />
          <span class="text-2xl font-medium text-gmail-gray">Mailat</span>
        </div>

        <h1 class="text-2xl font-normal text-center mb-2">Sign in</h1>
        <p class="text-gmail-gray text-center mb-8">to continue to Mailat</p>

        <div v-if="authStore.authError" class="mb-4 p-3 bg-yellow-50 border border-yellow-200 rounded-lg text-yellow-800 text-sm flex items-center justify-between gap-3" role="status">
          <span>{{ authStore.authError }}</span>
          <button type="button" class="font-medium text-gmail-blue hover:underline shrink-0" :disabled="retrying" @click="retrySession">
            {{ retrying ? 'Retrying…' : 'Retry' }}
          </button>
        </div>

        <div v-if="passwordReset && !error" class="mb-4 p-3 bg-blue-50 border border-blue-200 rounded-lg text-blue-800 text-sm" role="status">
          Your password was changed. Sign in with it and your two-factor code.
        </div>

        <div v-if="error" class="mb-4 p-3 bg-red-50 border border-red-200 rounded-lg text-red-700 text-sm" role="alert">
          {{ error }}
        </div>

        <form @submit.prevent="handleSubmit" class="space-y-4">
          <div v-if="!authStore.challengeToken">
            <label for="email" class="block text-sm font-medium text-gmail-gray mb-1">
              Email
            </label>
            <input
              id="email"
              v-model="email"
              type="email"
              required
              class="w-full px-4 py-3 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue focus:ring-1 focus:ring-gmail-blue"
              placeholder="you@example.com"
            />
          </div>

          <div v-if="!authStore.challengeToken">
            <label for="password" class="block text-sm font-medium text-gmail-gray mb-1">
              Password
            </label>
            <div class="relative">
              <input
                id="password"
                v-model="password"
                :type="showPassword ? 'text' : 'password'"
                required
                class="w-full px-4 py-3 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue focus:ring-1 focus:ring-gmail-blue pr-12"
                placeholder="Enter your password"
              />
              <button
                type="button"
                @click="showPassword = !showPassword"
                class="absolute right-3 top-1/2 -translate-y-1/2 text-gmail-gray hover:text-gmail-blue"
              >
                <component :is="showPassword ? EyeOff : Eye" class="w-5 h-5" />
              </button>
            </div>
          </div>

          <div v-if="authStore.challengeToken">
            <label for="verification-code" class="block text-sm font-medium text-gmail-gray mb-1">Verification code</label>
            <input id="verification-code" v-model="verificationCode" autocomplete="one-time-code" required autofocus
              class="w-full px-4 py-3 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue focus:ring-1 focus:ring-gmail-blue"
              placeholder="Authenticator or recovery code" />
            <p class="mt-2 text-sm text-gmail-gray">Enter the code from your authenticator app, or one unused recovery code.</p>
            <button type="button" class="mt-2 text-sm text-gmail-blue hover:underline" @click="authStore.challengeToken = null; verificationCode = ''; error = ''">Start sign in again</button>
          </div>

          <div v-if="!authStore.challengeToken" class="flex items-center justify-between text-sm">
            <label class="flex items-center gap-2 cursor-pointer">
              <input type="checkbox" class="gmail-checkbox" />
              <span class="text-gmail-gray">Remember me</span>
            </label>
            <router-link to="/forgot-password" class="text-gmail-blue hover:underline">
              Forgot password?
            </router-link>
          </div>

          <Button
            type="submit"
            :disabled="isLoading"
            :loading="isLoading"
            class="w-full"
          >
            {{ authStore.challengeToken ? 'Verify and sign in' : 'Sign in' }}
          </Button>
        </form>

        <div v-if="providers.length && !authStore.challengeToken" class="mt-6">
          <div class="flex items-center gap-3 text-xs text-gmail-gray mb-4">
            <span class="flex-1 border-t border-gmail-border"></span>or<span class="flex-1 border-t border-gmail-border"></span>
          </div>
          <div class="space-y-2">
            <a
              v-for="provider in providers"
              :key="provider"
              :href="oauthApi.loginUrl(provider)"
              @click.prevent="startOAuth(provider)"
              class="block w-full text-center px-4 py-3 border border-gmail-border rounded-lg text-sm font-medium hover:bg-gmail-lightGray"
            >
              Continue with {{ providerLabels[provider] || provider }}
            </a>
          </div>
        </div>

        <div v-if="registrationOpen" class="mt-6 text-center">
          <span class="text-gmail-gray">Don't have an account? </span>
          <router-link to="/register" class="text-gmail-blue hover:underline font-medium">
            Create account
          </router-link>
        </div>
      </div>
    </div>
  </div>
</template>
