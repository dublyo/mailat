<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Check, Copy, RefreshCw, Shield } from 'lucide-vue-next'
import { api, domainApi, type DomainDMARCStatus } from '@/lib/api'

const props = defineProps<{ domainUuid: string; domainName: string; refreshKey?: number; knownConfigured?: DomainDMARCStatus }>()
const status = ref<DomainDMARCStatus | null>(null)
const isChecking = ref(false)
const error = ref('')
const copyMessage = ref('')
let controller: AbortController | undefined
let requestVersion = 0

// Provider-confirmed writes can precede public DNS propagation. Remember them
// only to suppress duplicate manual suggestions; the server still decides writes.
const providerPolicy = computed(() => {
  const known = props.knownConfigured
  const hostname = `_dmarc.${props.domainName.toLowerCase().replace(/\.+$/, '')}`
  return known && known.hostname.toLowerCase().replace(/\.+$/, '') === hostname && (known.status === 'existing' || known.status === 'inherited') && known.value ? known : null
})
const pendingConfirmation = computed(() => !!providerPolicy.value && !isChecking.value && (!!error.value || status.value?.status === 'absent' || status.value?.status === 'unknown'))
const canCopy = computed(() => !providerPolicy.value && !isChecking.value && !error.value && status.value?.status === 'absent' && status.value.canCreate && !!status.value.suggestedValue && !!status.value.hostname)
const label = computed(() => {
  if (isChecking.value) return 'Checking DMARC…'
  if (pendingConfirmation.value) return 'Policy configured · awaiting DNS confirmation'
  if (error.value) return 'Could not check DMARC'
  switch (status.value?.status) {
    case 'absent': return status.value.canCreate ? 'Ready to add' : 'Needs review'
    case 'existing': return 'Existing policy preserved'
    case 'inherited': return 'Inherited policy preserved'
    case 'conflict': return 'Needs review'
    default: return 'Could not check DMARC'
  }
})
// Mailat never writes rua=, so the default policy (and many existing ones) send
// no aggregate reports. Shown only for a successful check, never for errors.
const needsReportingNote = computed(() => {
  if (isChecking.value || error.value || !status.value) return false
  const hasRua = (value: string) => /(^|;)\s*rua\s*=/i.test(value)
  // A provider-confirmed policy awaiting DNS propagation is judged by its own value.
  if (pendingConfirmation.value && providerPolicy.value) return !hasRua(providerPolicy.value.value)
  if (status.value.status === 'absent') return true
  if (status.value.status !== 'existing' && status.value.status !== 'inherited') return false
  return !hasRua(status.value.value || '')
})
const defaultPolicyTag = 'p=quarantine'
const ruaExample = 'rua=mailto:<address that delivers to Mailat>'
const checkedAt = computed(() => {
  const date = new Date(status.value?.checkedAt || '')
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString()
})

async function checkStatus() {
  controller?.abort()
  controller = new AbortController()
  const version = ++requestVersion
  const token = api.getToken()
  isChecking.value = true
  // A previous absence check must not leave a copyable default during a new check.
  status.value = null
  error.value = ''
  copyMessage.value = ''
  try {
    const result = await domainApi.inspectDMARC(props.domainUuid, controller.signal)
    if (version === requestVersion && token === api.getToken()) status.value = result
  } catch (e) {
    if (version === requestVersion && token === api.getToken()) error.value = e instanceof Error ? e.message : 'DMARC lookup failed. Try again.'
  } finally {
    if (version === requestVersion && token === api.getToken()) isChecking.value = false
  }
}

async function copyRecord(part: 'hostname' | 'value') {
  if (!canCopy.value || !status.value) return
  const version = requestVersion
  try {
    await navigator.clipboard.writeText(part === 'hostname' ? status.value.hostname : status.value.suggestedValue)
    if (version === requestVersion) copyMessage.value = part === 'hostname' ? 'Hostname copied' : 'Value copied'
  } catch {
    if (version === requestVersion) copyMessage.value = 'Could not copy. Select and copy the record text instead.'
  }
}

watch(() => [props.domainUuid, props.refreshKey], checkStatus, { immediate: true })
onBeforeUnmount(() => {
  requestVersion++
  controller?.abort()
})
</script>

<template>
  <section :aria-label="`DMARC for ${domainName}`" :aria-busy="isChecking" class="rounded-xl border border-gray-200 bg-white p-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h4 class="flex items-center gap-2 text-sm font-semibold text-gray-900">
        <Shield class="h-4 w-4 text-blue-600" aria-hidden="true" />
        DMARC
      </h4>
      <button type="button" @click="checkStatus" :disabled="isChecking" :aria-label="`Refresh DMARC status for ${domainName}`" class="inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-xs font-medium text-blue-700 hover:bg-blue-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-blue-500 disabled:cursor-wait disabled:opacity-60">
        <RefreshCw :class="['h-3.5 w-3.5', { 'animate-spin': isChecking }]" aria-hidden="true" />
        {{ isChecking ? 'Checking…' : 'Refresh status' }}
      </button>
    </div>
    <p role="status" aria-live="polite" aria-atomic="true" class="mt-2 text-sm font-medium" :class="!isChecking && status?.verified ? 'text-green-700' : error || status?.status === 'conflict' || status?.status === 'unknown' ? 'text-amber-700' : 'text-gray-700'">{{ label }}</p>
    <p v-if="error" role="alert" class="mt-2 text-sm text-red-700">{{ error }} No record is suggested until the check succeeds.</p>
    <template v-else-if="status && !isChecking">
      <p v-if="status.reason && !pendingConfirmation" class="mt-2 text-sm text-gray-600">{{ status.reason }}</p>
      <div v-if="status.value" class="mt-3 rounded-lg bg-gray-50 p-3">
        <p class="text-xs font-medium text-gray-500">{{ status.status === 'inherited' ? 'Inherited from' : 'Policy at' }}</p>
        <p class="mt-1 break-all font-mono text-xs text-gray-700">{{ status.policyHostname || status.hostname }}</p>
        <p class="mt-2 break-all font-mono text-xs text-gray-800">{{ status.value }}</p>
        <p v-if="status.policy" class="mt-2 text-xs text-gray-600">Effective policy: {{ status.policy }}</p>
      </div>
      <div v-if="canCopy" class="mt-3 rounded-lg border border-blue-100 bg-blue-50 p-3">
        <p class="text-xs font-medium text-blue-800">Add one TXT record</p>
        <div class="mt-2 flex items-start justify-between gap-2">
          <p class="min-w-0 break-all font-mono text-xs text-gray-800">{{ status.hostname }}</p>
          <button type="button" @click="copyRecord('hostname')" class="shrink-0 rounded p-1 text-blue-700 hover:bg-blue-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-blue-500" aria-label="Copy DMARC hostname"><Copy class="h-4 w-4" aria-hidden="true" /></button>
        </div>
        <div class="mt-2 flex items-start justify-between gap-2">
          <p class="min-w-0 break-all font-mono text-xs text-gray-800">{{ status.suggestedValue }}</p>
          <button type="button" @click="copyRecord('value')" class="shrink-0 rounded p-1 text-blue-700 hover:bg-blue-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-blue-500" aria-label="Copy DMARC value"><Check v-if="copyMessage === 'Value copied'" class="h-4 w-4" aria-hidden="true" /><Copy v-else class="h-4 w-4" aria-hidden="true" /></button>
        </div>
        <p class="mt-3 text-xs text-blue-800">Quarantine applies to all senders using this domain. Confirm that every sending service authenticates correctly before publishing. Root MX and SPF records stay unchanged.</p>
        <p class="mt-2 text-xs text-blue-800">Cloudflare setup checks again before adding this policy. For manual DNS, refresh this status just before adding it; do not create a second DMARC policy.</p>
      </div>
      <p v-if="status.status === 'existing' || status.status === 'inherited'" class="mt-3 text-xs text-gray-600">Mailat keeps this policy and its reporting settings unchanged. All sending providers share this policy; no second record is needed.</p>
      <p v-if="!pendingConfirmation && (status.status === 'conflict' || status.status === 'unknown' || (status.status === 'absent' && !status.canCreate))" class="mt-3 text-xs text-amber-700">Automatic creation is paused. Review the existing DNS settings or refresh after the lookup issue is resolved.</p>
      <div v-if="needsReportingNote" data-testid="dmarc-rua-note" class="mt-3 rounded-lg border border-gray-200 bg-gray-50 p-3">
        <p class="text-xs text-gray-600"><span class="font-medium text-gray-700">DMARC reports:</span> the default policy Mailat adds (<code class="font-mono">{{ defaultPolicyTag }}</code>) has no reporting address, so no aggregate reports are sent. To see reports in <strong class="font-medium">DMARC Reports</strong>, enable receiving on a Mailat domain and add <code class="break-all font-mono">{{ ruaExample }}</code> to your DMARC record. A rua on a different domain also needs an authorization record at that domain.</p>
      </div>
      <p v-if="checkedAt" class="mt-3 text-xs text-gray-500">Checked {{ checkedAt }}</p>
    </template>
    <div v-if="pendingConfirmation && providerPolicy" class="mt-3 rounded-lg bg-blue-50 p-3">
      <p class="text-xs text-blue-800">Sending setup already confirmed this policy. The current public DNS check has not confirmed it. Do not add another record; refresh to check propagation or review the existing DNS settings.</p>
      <p class="mt-2 break-all font-mono text-xs text-gray-700">{{ providerPolicy.policyHostname || providerPolicy.hostname }}</p>
      <p class="mt-2 break-all font-mono text-xs text-gray-800">{{ providerPolicy.value }}</p>
    </div>
    <p v-if="copyMessage" role="status" class="mt-2 text-xs text-gray-700">{{ copyMessage }}</p>
    <p class="mt-3 text-xs text-gray-500">DMARC is managed separately from the three SES sending checks and excluded from bulk DNS downloads, which cannot check for an existing policy.</p>
  </section>
</template>
