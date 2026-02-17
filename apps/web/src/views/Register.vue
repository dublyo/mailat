<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { Eye, EyeOff, Check, X } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { authApi } from '@/lib/api'
import Button from '@/components/common/Button.vue'

const router = useRouter()
const authStore = useAuthStore()

const name = ref('')
const email = ref('')
const password = ref('')
const confirmPassword = ref('')
const showPassword = ref(false)
const error = ref('')
const isLoading = ref(false)
const registrationClosed = ref(false)
const checkingStatus = ref(true)

onMounted(async () => {
  try {
    const res = await authApi.registerStatus()
    if (!res.open) {
      registrationClosed.value = true
    }
  } catch {
    // If check fails, allow form to show — backend will reject anyway
  } finally {
    checkingStatus.value = false
  }
})

const passwordRequirements = computed(() => [
  { label: 'At least 8 characters', met: password.value.length >= 8 },
  { label: 'Contains uppercase letter', met: /[A-Z]/.test(password.value) },
  { label: 'Contains lowercase letter', met: /[a-z]/.test(password.value) },
  { label: 'Contains number', met: /\d/.test(password.value) },
])

const allRequirementsMet = computed(() => passwordRequirements.value.every(req => req.met))
const passwordsMatch = computed(() => password.value === confirmPassword.value && confirmPassword.value.length > 0)

const handleSubmit = async () => {
  error.value = ''

  if (!allRequirementsMet.value) {
    error.value = 'Please meet all password requirements'
    return
  }

  if (!passwordsMatch.value) {
    error.value = 'Passwords do not match'
    return
  }

  isLoading.value = true

  try {
    await authStore.register({ name: name.value, email: email.value, password: password.value })
    router.push('/inbox')
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Registration failed'
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

        <!-- Loading state -->
        <div v-if="checkingStatus" class="text-center py-8">
          <p class="text-gmail-gray">Loading...</p>
        </div>

        <!-- Registration closed -->
        <div v-else-if="registrationClosed" class="text-center">
          <h1 class="text-2xl font-normal mb-2">Registration Closed</h1>
          <p class="text-gmail-gray mb-6">
            Registration is closed. Only the admin can invite new users.
          </p>
          <router-link
            to="/login"
            class="inline-block px-6 py-3 bg-gmail-blue text-white rounded-lg font-medium hover:bg-blue-600 transition-colors"
          >
            Sign in instead
          </router-link>
        </div>

        <!-- Registration form -->
        <template v-else>
        <h1 class="text-2xl font-normal text-center mb-2">Create account</h1>
        <p class="text-gmail-gray text-center mb-8">Set up your admin account</p>

        <div v-if="error" class="mb-4 p-3 bg-red-50 border border-red-200 rounded-lg text-red-700 text-sm">
          {{ error }}
        </div>

        <form @submit.prevent="handleSubmit" class="space-y-4">
          <div>
            <label for="name" class="block text-sm font-medium text-gmail-gray mb-1">
              Full name
            </label>
            <input
              id="name"
              v-model="name"
              type="text"
              required
              class="w-full px-4 py-3 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue focus:ring-1 focus:ring-gmail-blue"
              placeholder="John Doe"
            />
          </div>

          <div>
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

          <div>
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
                placeholder="Create a password"
              />
              <button
                type="button"
                @click="showPassword = !showPassword"
                class="absolute right-3 top-1/2 -translate-y-1/2 text-gmail-gray hover:text-gmail-blue"
              >
                <component :is="showPassword ? EyeOff : Eye" class="w-5 h-5" />
              </button>
            </div>

            <!-- Password requirements -->
            <div v-if="password.length > 0" class="mt-2 space-y-1">
              <div
                v-for="req in passwordRequirements"
                :key="req.label"
                :class="['flex items-center gap-2 text-xs', req.met ? 'text-green-600' : 'text-gmail-gray']"
              >
                <component :is="req.met ? Check : X" class="w-3.5 h-3.5" />
                <span>{{ req.label }}</span>
              </div>
            </div>
          </div>

          <div>
            <label for="confirmPassword" class="block text-sm font-medium text-gmail-gray mb-1">
              Confirm password
            </label>
            <input
              id="confirmPassword"
              v-model="confirmPassword"
              type="password"
              required
              :class="[
                'w-full px-4 py-3 border rounded-lg focus:outline-none focus:ring-1',
                confirmPassword.length > 0
                  ? passwordsMatch
                    ? 'border-green-500 focus:border-green-500 focus:ring-green-500'
                    : 'border-red-500 focus:border-red-500 focus:ring-red-500'
                  : 'border-gmail-border focus:border-gmail-blue focus:ring-gmail-blue'
              ]"
              placeholder="Confirm your password"
            />
            <p v-if="confirmPassword.length > 0 && !passwordsMatch" class="mt-1 text-xs text-red-500">
              Passwords do not match
            </p>
          </div>

          <Button
            type="submit"
            :disabled="isLoading || !allRequirementsMet || !passwordsMatch"
            :loading="isLoading"
            class="w-full"
          >
            Create account
          </Button>
        </form>

        <div class="mt-6 text-center">
          <span class="text-gmail-gray">Already have an account? </span>
          <router-link to="/login" class="text-gmail-blue hover:underline font-medium">
            Sign in
          </router-link>
        </div>
        </template>
      </div>
    </div>
  </div>
</template>
