<script setup lang="ts">
import { ref } from 'vue'
const token = location.hash.slice(1), busy = ref(false), done = ref(false), error = ref('')
async function unsubscribe() {
  if (!token || busy.value) return
  busy.value = true; error.value = ''
  try {
    const res = await fetch(`${import.meta.env.VITE_API_URL || ''}/api/v1/unsubscribe/${encodeURIComponent(token)}`, { method: 'POST', credentials: 'omit', referrerPolicy: 'no-referrer', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body: 'List-Unsubscribe=One-Click' })
    const data = await res.json(); if (!res.ok) throw new Error(data.message || 'Please try again.')
    done.value = true; history.replaceState(null, '', location.pathname)
  } catch (e) { error.value = e instanceof Error ? e.message : 'Please try again.' }
  finally { busy.value = false }
}
</script>
<template>
  <main class="flex min-h-screen items-center justify-center bg-slate-50 p-4"><section class="w-full max-w-lg rounded-2xl border border-gmail-border bg-white p-8 shadow-sm">
    <h1 class="text-2xl font-semibold">{{ done ? 'You’re unsubscribed' : 'Want to leave the list?' }}</h1>
    <p v-if="done" role="status" class="mt-4 text-gmail-gray">Your unsubscribe request has been saved. Take care!</p>
    <template v-else><p class="mt-4 text-gmail-gray">Confirm below to stop marketing emails from this organization.</p><p v-if="error || !token" role="alert" class="mt-4 text-red-700">{{ error || 'Open the complete unsubscribe link from your email.' }}</p><button v-if="token" :disabled="busy" @click="unsubscribe" class="mt-6 w-full rounded-lg bg-gmail-blue p-3 font-medium text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-60">{{ busy ? 'Saving…' : 'Unsubscribe' }}</button></template>
  </section></main>
</template>
