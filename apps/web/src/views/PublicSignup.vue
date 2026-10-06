<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { CheckCircle2, Mail } from 'lucide-vue-next'
import SignupCard from '@/components/forms/SignupCard.vue'
import { publicSignup, type PublicForm } from '@/lib/signupForms'
const route = useRoute()
const form = ref<PublicForm>(), error = ref(''), message = ref(''), loading = ref(true), busy = ref(false)
const confirmation = route.name === 'signup-confirm'
// Fragment tokens are never sent in HTTP URLs, server access logs or referrers.
const token = confirmation ? location.hash.slice(1) : ''
const previousTitle = document.title
const referrer = document.createElement('meta'); referrer.name = 'referrer'; referrer.content = 'no-referrer'
onMounted(async () => {
  document.head.appendChild(referrer)
  if (confirmation) { document.title = 'Confirm your subscription · Mailat'; loading.value = false; if (!token) error.value = 'Open the complete link from your confirmation email.'; return }
  try { form.value = await publicSignup<PublicForm>(`/${encodeURIComponent(String(route.params.uuid))}`); document.title = `${form.value.title} · Mailat` }
  catch (e) { error.value = e instanceof Error ? e.message : 'This form is not available.' }
  finally { loading.value = false }
})
onUnmounted(() => { referrer.remove(); document.title = previousTitle })
async function submit(data: { email: string; firstName: string; consent: boolean; website: string }) {
  if (!form.value || busy.value) return
  busy.value = true; error.value = ''
  try { const result = await publicSignup<{ message: string }>(`/${form.value.uuid}/submit`, { ...data, challenge: form.value.challenge }); message.value = result.message }
  catch (e) { error.value = e instanceof Error ? e.message : 'Please try again.' }
  finally { busy.value = false }
}
async function confirm() {
  if (!token || busy.value) return
  busy.value = true; error.value = ''
  try { const result = await publicSignup<{ message: string }>('/confirm', { token }); message.value = result.message; history.replaceState(null, '', location.pathname) }
  catch (e) { error.value = e instanceof Error ? e.message : 'Please try again.' }
  finally { busy.value = false }
}
</script>
<template>
  <main class="flex min-h-screen items-center justify-center bg-slate-50 p-4 sm:p-8">
    <p v-if="loading" role="status" class="text-gmail-gray">Getting your invitation ready…</p>
    <section v-else-if="confirmation" class="w-full max-w-lg rounded-2xl border border-gmail-border bg-white p-8 shadow-sm">
      <component :is="message ? CheckCircle2 : Mail" class="mb-5 h-10 w-10 text-gmail-blue" aria-hidden="true" />
      <h1 class="text-2xl font-semibold">{{ message ? 'All done!' : 'One more step to join us' }}</h1>
      <p v-if="message" role="status" class="mt-4 text-gmail-gray">{{ message }}</p>
      <template v-else>
        <p class="mt-4 leading-relaxed text-gmail-gray">Confirm that you’d like to receive the updates you signed up for. If you didn’t request them, simply close this page.</p>
        <p v-if="error" role="alert" class="mt-4 rounded-lg bg-red-50 p-3 text-red-700">{{ error }}</p>
        <button v-if="token" :disabled="busy" @click="confirm" class="mt-6 w-full rounded-lg bg-gmail-blue p-3 font-medium text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:opacity-60">{{ busy ? 'Confirming…' : 'Confirm my subscription' }}</button>
      </template>
    </section>
    <SignupCard v-else-if="form" :form="form" :busy="busy" :error="error" :message="message" @submit="submit" />
    <section v-else class="max-w-md rounded-2xl border bg-white p-8"><h1 class="text-xl font-semibold">This invitation isn’t available</h1><p role="alert" class="mt-3 text-gmail-gray">{{ error }}</p></section>
  </main>
</template>
