<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { forwardsApi } from '@/lib/api'
import { forwardVerificationFromHash, clearFragment } from '@/lib/invite'

// Opened by the forward's destination, who usually has no Mailat account.
const link = forwardVerificationFromHash(window.location.hash)
const state = ref<'working' | 'verified' | 'failed'>(link ? 'working' : 'failed')
const message = ref(link ? '' : 'Open the complete confirmation link from the email.')

onMounted(async () => {
  if (!link) return
  try {
    await forwardsApi.verify(link.uuid, link.token)
    state.value = 'verified'
  } catch (e) {
    state.value = 'failed'
    message.value = e instanceof Error ? e.message : 'This confirmation link is invalid or has expired.'
  } finally {
    // The token is single use either way; keep it out of history.
    clearFragment()
  }
})
</script>

<template>
  <main class="min-h-screen flex items-center justify-center bg-gmail-lightGray p-4">
    <section class="w-full max-w-md bg-white rounded-2xl shadow-lg p-8 text-center" :aria-busy="state === 'working'">
      <div class="flex items-center justify-center gap-3 mb-6">
        <img src="/logo.jpg" alt="Mailat" class="w-12 h-12 rounded-lg object-contain" />
        <span class="text-2xl font-medium text-gmail-gray">Mailat</span>
      </div>
      <p v-if="state === 'working'" role="status" class="text-gmail-gray">Confirming…</p>
      <template v-else-if="state === 'verified'">
        <h1 class="text-2xl font-normal mb-2">Forwarding confirmed</h1>
        <p role="status" class="text-gmail-gray">Mail will now be forwarded to this address. You can close this page.</p>
      </template>
      <template v-else>
        <h1 class="text-2xl font-normal mb-2">Could not confirm forwarding</h1>
        <p role="alert" class="text-red-700">{{ message }}</p>
        <p class="mt-4 text-sm text-gmail-gray">Ask the sender to resend the confirmation from their Mailat settings.</p>
      </template>
    </section>
  </main>
</template>
