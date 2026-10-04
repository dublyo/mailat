<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { RefreshCw, Send } from 'lucide-vue-next'
import { api, domainApi, type DomainSendingReadiness } from '@/lib/api'

const props = defineProps<{ domainUuid: string; domainName: string; canSetup: boolean }>()
const status = ref<DomainSendingReadiness | null>(null)
const busy = ref<'checking' | 'setting-up' | ''>('')
const error = ref('')
let requestVersion = 0
let controller: AbortController | undefined
const ready = computed(() => !!status.value?.storageReady && !!status.value?.feedbackReady)
const headline = computed(() => {
  if (busy.value === 'setting-up') return 'Setting up sending resources…'
  if (busy.value) return 'Checking sending resources…'
  if (error.value) return 'Could not check sending resources'
  if (ready.value) return 'Attachments and delivery feedback ready'
  if (status.value?.feedbackConfigured && status.value.subscriptionStatus === 'pending') return 'Awaiting delivery subscription confirmation'
  return 'Sending setup needs attention'
})
const checkedAt = computed(() => { const date = new Date(status.value?.checkedAt || ''); return Number.isNaN(date.getTime()) ? '' : date.toLocaleString() })
async function load(setup = false) {
  if (setup && (!props.canSetup || busy.value)) return
  controller?.abort()
  controller = new AbortController()
  const version = ++requestVersion
  const token = api.getToken()
  const domain = props.domainUuid
  busy.value = setup ? 'setting-up' : 'checking'
  status.value = null
  error.value = ''
  try {
    const result = setup ? await domainApi.setupSending(domain) : await domainApi.sendingStatus(domain, controller.signal)
    // HTTP 200 can mean storage-only progress. Read the readiness fields before
    // declaring completion, and discard results from an older domain/account.
    if (version === requestVersion && token === api.getToken() && result.domainUuid === props.domainUuid) status.value = result
  } catch (e) {
    if (version === requestVersion && token === api.getToken()) error.value = e instanceof Error ? e.message : 'Sending setup could not be checked. Try again.'
  } finally {
    if (version === requestVersion && token === api.getToken()) busy.value = ''
  }
}
watch(() => props.domainUuid, () => load(), { immediate: true })
onBeforeUnmount(() => { requestVersion++; controller?.abort() })
</script>

<template>
  <section :aria-label="`Sending resources for ${domainName}`" :aria-busy="!!busy" class="rounded-xl border border-gray-200 bg-white p-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h4 class="flex items-center gap-2 text-sm font-semibold text-gray-900"><Send class="h-4 w-4 text-blue-600" aria-hidden="true" />Sending resources</h4>
      <button type="button" @click="load()" :disabled="!!busy" :aria-label="`Refresh sending resources for ${domainName}`" class="inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-xs font-medium text-blue-700 hover:bg-blue-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-blue-500 disabled:opacity-60"><RefreshCw :class="['h-3.5 w-3.5', { 'animate-spin': busy }]" aria-hidden="true" />Refresh status</button>
    </div>
    <p class="mt-2 text-sm text-gray-600">Private attachment storage and delivery notifications for outgoing email.</p>
    <p role="status" aria-live="polite" class="mt-3 text-sm font-medium" :class="ready && !busy ? 'text-green-700' : 'text-amber-700'">{{ headline }}</p>
    <p v-if="error" role="alert" class="mt-2 text-sm text-red-700">{{ error }}</p>
    <template v-else-if="status && !busy">
      <dl class="mt-3 grid gap-2 text-sm sm:grid-cols-3">
        <div class="rounded-lg bg-gray-50 p-3"><dt class="text-gray-500">Attachments</dt><dd class="mt-1 font-medium" :class="status.storageReady ? 'text-green-700' : 'text-amber-700'">{{ status.storageReady ? 'Storage ready' : 'Setup required' }}</dd></div>
        <div class="rounded-lg bg-gray-50 p-3"><dt class="text-gray-500">SES feedback</dt><dd class="mt-1 font-medium" :class="status.feedbackConfigured ? 'text-green-700' : 'text-amber-700'">{{ status.feedbackConfigured ? 'Configured' : 'Setup required' }}</dd></div>
        <div class="rounded-lg bg-gray-50 p-3"><dt class="text-gray-500">Delivery subscription</dt><dd class="mt-1 font-medium" :class="status.subscriptionStatus === 'active' ? 'text-green-700' : 'text-amber-700'">{{ status.subscriptionStatus === 'active' ? 'Confirmed' : status.subscriptionStatus === 'pending' ? 'Awaiting confirmation' : 'Not configured' }}</dd></div>
      </dl>
      <p v-if="status.reason" class="mt-3 text-sm text-gray-600">{{ status.reason }}</p>
      <p v-if="checkedAt" class="mt-2 text-xs text-gray-500">Checked {{ checkedAt }}</p>
    </template>
    <button v-if="!ready" type="button" @click="load(true)" :disabled="!!busy || !canSetup" aria-label="Set up sending resources" class="mt-4 rounded-lg bg-blue-600 px-3 py-2 text-sm font-medium text-white hover:bg-blue-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-blue-500 disabled:cursor-not-allowed disabled:opacity-50">{{ busy === 'setting-up' ? 'Setting up…' : 'Set up sending resources' }}</button>
    <p v-if="!canSetup" class="mt-2 text-xs text-gray-500">Verify this SES domain before setting up sending resources.</p>
    <p class="mt-3 text-xs text-gray-500">Receiving is configured separately. This setup preserves your root MX records and existing inbox provider.</p>
  </section>
</template>
