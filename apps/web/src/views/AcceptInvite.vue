<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { invitesApi, type InviteLookup } from '@/lib/api'
import { inviteTokenFromHash, clearFragment, completeInvite, inviteCopy, loginAfterReset } from '@/lib/invite'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const authStore = useAuthStore()
const token = inviteTokenFromHash(window.location.hash)
const invite = ref<InviteLookup | null>(null)
const loading = ref(!!token)
const busy = ref(false)
const error = ref(token ? '' : 'Open the complete invite link from your email.')
const name = ref('')
const password = ref('')
const confirmPassword = ref('')
const copy = computed(() => invite.value ? inviteCopy(invite.value) : null)

onMounted(async () => {
  if (!token) return
  try {
    invite.value = await invitesApi.lookup(token)
    name.value = inviteCopy(invite.value).initialName
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'This invite link is invalid or has expired.'
  } finally {
    loading.value = false
  }
})

async function accept() {
  if (busy.value || !invite.value || !copy.value) return
  if (copy.value.askName && name.value.trim().length < 2) { error.value = 'Enter your name.'; return }
  if (password.value.length < 8) { error.value = 'Use a password of at least 8 characters.'; return }
  if (password.value !== confirmPassword.value) { error.value = 'The passwords do not match.'; return }
  busy.value = true
  error.value = ''
  try {
    const user = await completeInvite({ token, name: copy.value.askName ? name.value.trim() : undefined, password: password.value }, {
      accept: invitesApi.accept,
      clearHash: () => clearFragment(),
      setSession: authStore.setSession,
    })
    // With two-factor sign-in on, a reset does not sign in: log in with the new password.
    await router.replace(user ? copy.value.after : loginAfterReset(invite.value.email))
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not accept the invite. Try again.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="min-h-screen flex items-center justify-center bg-gmail-lightGray p-4">
    <section class="w-full max-w-md bg-white rounded-2xl shadow-lg p-8" :aria-busy="loading || busy">
      <div class="flex items-center justify-center gap-3 mb-6">
        <img src="/logo.jpg" alt="Mailat" class="w-12 h-12 rounded-lg object-contain" />
        <span class="text-2xl font-medium text-gmail-gray">Mailat</span>
      </div>
      <p v-if="loading" role="status" class="text-center text-gmail-gray">Checking your invite…</p>
      <template v-else-if="invite && copy">
        <h1 class="text-2xl font-normal text-center mb-2 break-words">{{ copy.title }}</h1>
        <p class="text-gmail-gray text-center mb-6 break-words">{{ copy.intro }}</p>
        <p v-if="authStore.isAuthenticated && authStore.user?.email.toLowerCase() !== invite.email.toLowerCase()" role="status" class="mb-4 p-3 bg-yellow-50 rounded-lg text-sm text-yellow-800">You are signed in as {{ authStore.user?.email }}. Accepting signs you out of that account here.</p>
        <form class="space-y-4" @submit.prevent="accept">
          <label v-if="copy.askName" class="block text-sm font-medium text-gmail-gray">Your name
            <input v-model="name" autocomplete="name" maxlength="255" required class="mt-1 w-full px-4 py-3 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue focus:ring-1 focus:ring-gmail-blue" />
          </label>
          <label class="block text-sm font-medium text-gmail-gray">Choose a password
            <input v-model="password" type="password" autocomplete="new-password" minlength="8" maxlength="72" required class="mt-1 w-full px-4 py-3 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue focus:ring-1 focus:ring-gmail-blue" />
          </label>
          <label class="block text-sm font-medium text-gmail-gray">Confirm password
            <input v-model="confirmPassword" type="password" autocomplete="new-password" required class="mt-1 w-full px-4 py-3 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue focus:ring-1 focus:ring-gmail-blue" />
          </label>
          <p v-if="error" role="alert" class="p-3 bg-red-50 border border-red-200 rounded-lg text-red-700 text-sm">{{ error }}</p>
          <button type="submit" :disabled="busy" class="w-full rounded-lg bg-gmail-blue p-3 font-medium text-white disabled:opacity-60">{{ busy ? copy.busy : copy.submit }}</button>
        </form>
        <p v-if="!invite.purpose || invite.purpose === 'join'" class="mt-4 text-xs text-gmail-gray text-center">You can connect a Google, GitHub or Microsoft sign-in later in Settings.</p>
      </template>
      <template v-else>
        <h1 class="text-2xl font-normal text-center mb-4">Link unavailable</h1>
        <p role="alert" class="text-center text-red-700">{{ error }}</p>
        <p class="mt-4 text-center text-sm text-gmail-gray">Ask your administrator to send a new link.</p>
      </template>
    </section>
  </main>
</template>
