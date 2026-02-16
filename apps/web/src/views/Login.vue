<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Mail, Eye, EyeOff } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { authApi } from '@/lib/api'
import Button from '@/components/common/Button.vue'
import Spinner from '@/components/common/Spinner.vue'

const router = useRouter()
const route = useRoute()
const authStore = useAuthStore()

const email = ref('')
const password = ref('')
const showPassword = ref(false)
const error = ref('')
const isLoading = ref(false)
const registrationOpen = ref(false)

onMounted(async () => {
  try {
    const res = await authApi.registerStatus()
    registrationOpen.value = res.open
  } catch {
    // ignore — just hide register link
  }
})

const handleSubmit = async () => {
  error.value = ''
  isLoading.value = true

  try {
    await authStore.login(email.value, password.value)
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
        <div class="flex items-center justify-center gap-2 mb-8">
          <div class="w-12 h-12 bg-gmail-red rounded-lg flex items-center justify-center">
            <Mail class="w-7 h-7 text-white" />
          </div>
          <span class="text-2xl font-medium text-gmail-gray">Mailat</span>
        </div>

        <h1 class="text-2xl font-normal text-center mb-2">Sign in</h1>
        <p class="text-gmail-gray text-center mb-8">to continue to Mailat</p>

        <div v-if="error" class="mb-4 p-3 bg-red-50 border border-red-200 rounded-lg text-red-700 text-sm">
          {{ error }}
        </div>

        <form @submit.prevent="handleSubmit" class="space-y-4">
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

          <div class="flex items-center justify-between text-sm">
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
            Sign in
          </Button>
        </form>

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
