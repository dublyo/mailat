<script setup lang="ts">
import { ref } from 'vue'
import { forwardsApi } from '@/lib/api'
import { forwardConfirmation, clearFragment } from '@/lib/invite'

// Opened by the forward's destination, who usually has no Mailat account.
// The token leaves the address bar at once but is only sent on a click, so a
// mail scanner or link preview opening this page confirms nothing.
const link = forwardConfirmation(window.location.hash, { verify: forwardsApi.verify, clearHash: () => clearFragment() })
const state = ref<'ready' | 'working' | 'verified' | 'failed'>(link.valid ? 'ready' : 'failed')
const message = ref(link.valid ? '' : 'Open the complete confirmation link from the email.')

async function confirm() {
  if (state.value !== 'ready') return
  state.value = 'working'
  try {
    await link.confirm()
    state.value = 'verified'
  } catch (e) {
    state.value = 'failed'
    message.value = e instanceof Error ? e.message : 'This confirmation link is invalid or has expired.'
  }
}
</script>

<template>
  <main class="min-h-screen flex items-center justify-center bg-gmail-lightGray p-4">
    <section class="w-full max-w-md bg-white rounded-2xl shadow-lg p-8 text-center" :aria-busy="state === 'working'">
      <div class="flex items-center justify-center gap-3 mb-6">
        <img src="/logo.jpg" alt="Mailat" class="w-12 h-12 rounded-lg object-contain" />
        <span class="text-2xl font-medium text-gmail-gray">Mailat</span>
      </div>
      <template v-if="state === 'ready' || state === 'working'">
        <h1 class="text-2xl font-normal mb-2">Confirm forwarding</h1>
        <p class="text-gmail-gray">The email that brought you here names the Mailat address that wants to forward its incoming mail to you. Confirm only if you expect that mail.</p>
        <button type="button" class="mt-6 px-5 py-2 rounded-lg bg-gmail-blue text-white font-medium disabled:opacity-60" :disabled="state === 'working'" @click="confirm">
          {{ state === 'working' ? 'Confirming…' : 'Confirm forwarding' }}
        </button>
        <p class="mt-4 text-sm text-gmail-gray">If you did not expect this, close this page. Nothing is forwarded unless you confirm.</p>
      </template>
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
