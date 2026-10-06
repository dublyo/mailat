<script setup lang="ts">
import { ref } from 'vue'
import { Mail, CheckCircle2 } from 'lucide-vue-next'
import type { PublicForm } from '@/lib/signupForms'
const props = defineProps<{ form: Pick<PublicForm, 'title' | 'description' | 'consentText' | 'buttonText' | 'privacyUrl' | 'collectName' | 'confirmationMode'>; preview?: boolean; busy?: boolean; message?: string; error?: string }>()
const emit = defineEmits<{ submit: [data: { email: string; firstName: string; consent: boolean; website: string }] }>()
const email = ref(''), firstName = ref(''), consent = ref(false), website = ref('')
function submit() { if (!props.preview && !props.busy) emit('submit', { email: email.value, firstName: firstName.value, consent: consent.value, website: website.value }) }
</script>
<template>
  <section class="w-full max-w-lg rounded-2xl border border-gmail-border bg-white p-6 shadow-sm sm:p-8" :aria-label="form.title || 'Email signup'">
    <div class="mb-5 flex h-12 w-12 items-center justify-center rounded-2xl bg-blue-50 text-gmail-blue"><Mail class="h-6 w-6" aria-hidden="true" /></div>
    <template v-if="message">
      <CheckCircle2 class="mb-4 h-8 w-8 text-green-600" aria-hidden="true" />
      <h1 class="text-2xl font-semibold text-gray-900">Thanks for joining us!</h1>
      <p role="status" class="mt-4 leading-relaxed text-gmail-gray">{{ message }}</p>
    </template>
    <template v-else>
      <h1 class="break-words text-2xl font-semibold leading-tight text-gray-900">{{ form.title || 'Let’s stay in touch' }}</h1>
      <p v-if="form.description" class="mt-3 whitespace-pre-line break-words leading-relaxed text-gmail-gray">{{ form.description }}</p>
      <form class="mt-6 space-y-5" @submit.prevent="submit">
        <label v-if="form.collectName" class="block text-sm font-medium text-gray-800">Your name <span class="font-normal text-gmail-gray">(optional)</span>
          <input v-model="firstName" name="given-name" autocomplete="given-name" maxlength="100" class="mt-2 w-full rounded-lg border border-gmail-border px-3 py-3 focus:border-gmail-blue focus:outline-none focus:ring-2 focus:ring-blue-100" />
        </label>
        <label class="block text-sm font-medium text-gray-800">Email address
          <input v-model="email" name="email" type="email" autocomplete="email" required maxlength="255" placeholder="you@example.com" class="mt-2 w-full rounded-lg border border-gmail-border px-3 py-3 focus:border-gmail-blue focus:outline-none focus:ring-2 focus:ring-blue-100" />
        </label>
        <div class="hidden" aria-hidden="true"><label>Website<input v-model="website" name="website" tabindex="-1" autocomplete="off" /></label></div>
        <label class="flex cursor-pointer items-start gap-3 text-sm leading-relaxed text-gray-700">
          <input v-model="consent" type="checkbox" required class="mt-1 h-4 w-4 shrink-0 accent-blue-600" />
          <span class="break-words">{{ form.consentText || 'I would like to receive these email updates.' }}</span>
        </label>
        <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700">{{ error }}</p>
        <button type="submit" :disabled="busy || preview" class="w-full rounded-lg bg-gmail-blue px-4 py-3 font-medium text-white hover:bg-blue-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:opacity-60">{{ busy ? 'Just a moment…' : form.buttonText || 'Subscribe' }}</button>
        <p class="text-center text-xs leading-relaxed text-gmail-gray">{{ form.confirmationMode === 'double' ? 'We’ll email you a link to confirm your subscription.' : 'Subscribe to the updates described above.' }} You can unsubscribe later.</p>
        <a v-if="form.privacyUrl" :href="form.privacyUrl" target="_blank" rel="noopener noreferrer" class="block text-center text-xs text-gmail-blue underline">Privacy policy</a>
      </form>
    </template>
    <p class="mt-6 text-center text-xs text-gray-400">Powered by Mailat</p>
  </section>
</template>
