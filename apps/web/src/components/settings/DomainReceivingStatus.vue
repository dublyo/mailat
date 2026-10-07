<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Check, Cloud, Copy, Inbox, RefreshCw } from 'lucide-vue-next'
import { api, domainApi, receivedInboxApi, type DomainReceivingStatus } from '@/lib/api'
import { RECEIVING_EXPLAINER, receivingBadge, mxCopyFields, receivingProblem } from '@/lib/receiving'

// Always-visible receiving row for a domain card. Receiving stays off until an
// owner or admin confirms it here; Mailat never publishes a root MX by itself.
const props = defineProps<{
  domainUuid: string
  domainName: string
  domainId: number
  receivingEnabled: boolean
  canManage: boolean
  canEnable: boolean
}>()
const emit = defineEmits<{ changed: [] }>()

const status = ref<DomainReceivingStatus | null>(null)
const busy = ref<'' | 'checking' | 'enabling' | 'cloudflare'>('')
const error = ref('')
const notice = ref('')
const confirmOpen = ref(false)
const cloudflareOpen = ref(false)
const cloudflareToken = ref('')
const copied = ref('')
let controller: AbortController | undefined
let requestVersion = 0

const badge = computed(() => busy.value === 'checking' && !status.value ? { label: 'Checking…', variant: 'default' as const } : receivingBadge(status.value?.mxStatus))
const badgeClass = computed(() => ({
  success: 'bg-green-50 text-green-700 border-green-200',
  warning: 'bg-amber-50 text-amber-800 border-amber-200',
  error: 'bg-red-50 text-red-700 border-red-200',
  default: 'bg-gray-50 text-gray-700 border-gray-200',
})[badge.value.variant])
const fields = computed(() => status.value ? mxCopyFields(status.value) : [])
const problem = computed(() => status.value && status.value.mxStatus !== 'not_enabled' ? receivingProblem(status.value, props.domainName) : '')
const checkedAt = computed(() => { const date = new Date(status.value?.checkedAt || ''); return Number.isNaN(date.getTime()) ? '' : date.toLocaleString() })

// keepMessages: a re-check right after an action keeps that action's outcome visible.
async function load(refresh = false, keepMessages = false) {
  controller?.abort()
  controller = new AbortController()
  const version = ++requestVersion
  const token = api.getToken()
  const domain = props.domainUuid
  busy.value = 'checking'
  if (!keepMessages) { error.value = ''; notice.value = '' }
  try {
    const result = await domainApi.receivingStatus(domain, refresh, controller.signal)
    // Discard answers for an older domain or a different signed-in account.
    if (version === requestVersion && token === api.getToken() && result.domainUuid === props.domainUuid) status.value = result
  } catch (e) {
    if (version === requestVersion && token === api.getToken()) error.value = e instanceof Error ? e.message : 'Could not check receiving. Try again.'
  } finally {
    if (version === requestVersion && token === api.getToken()) busy.value = ''
  }
}

async function enable() {
  if (!props.canManage || !props.canEnable || busy.value) return
  busy.value = 'enabling'
  error.value = ''
  notice.value = ''
  try {
    await receivedInboxApi.setupReceiving(props.domainId)
    confirmOpen.value = false
    notice.value = 'Receiving is on. Publish the MX record below; Mailat did not change your DNS.'
    emit('changed')
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not enable receiving.'
  } finally {
    busy.value = ''
  }
  await load(true, true)
}

async function addToCloudflare() {
  const token = cloudflareToken.value.trim()
  if (!props.canManage || busy.value) return
  if (!token) { error.value = 'Enter your Cloudflare API token.'; return }
  busy.value = 'cloudflare'
  error.value = ''
  notice.value = ''
  try {
    const { results } = await domainApi.addDNSToCloudflare(props.domainUuid, token, undefined, 'receiving-mx')
    const mx = results.find(r => r.type === 'MX')
    if (mx?.success) {
      notice.value = mx.status === 'preserved' ? 'Cloudflare already has this MX record.' : 'MX record added to Cloudflare. Public DNS can take a few minutes; use Re-check.'
      cloudflareOpen.value = false
    } else {
      error.value = mx?.reason || mx?.error || 'Cloudflare did not add the MX record.'
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not add the MX record to Cloudflare.'
  } finally {
    cloudflareToken.value = '' // never kept after the attempt
    busy.value = ''
  }
  await load(true, true)
}

async function copy(label: string, value: string) {
  try {
    await navigator.clipboard.writeText(value)
    copied.value = label
    setTimeout(() => { if (copied.value === label) copied.value = '' }, 2000)
  } catch {
    error.value = 'Could not copy. Select the text instead.'
  }
}

watch(() => props.domainUuid, () => { status.value = null; confirmOpen.value = false; cloudflareOpen.value = false; load() }, { immediate: true })
watch(() => props.receivingEnabled, () => load(true, true))
onBeforeUnmount(() => { requestVersion++; controller?.abort() })
</script>

<template>
  <section :id="`receiving-${domainUuid}`" :aria-label="`Receiving (MX) for ${domainName}`" :aria-busy="!!busy" class="mt-4 rounded-xl border border-gray-200 bg-white p-4 text-sm">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h4 class="flex items-center gap-2 font-semibold text-gray-900">
        <Inbox class="h-4 w-4 text-blue-600" aria-hidden="true" />Receiving (MX)
        <span role="status" class="rounded-full border px-2 py-0.5 text-xs font-medium" :class="badgeClass">{{ badge.label }}</span>
      </h4>
      <button type="button" @click="load(true)" :disabled="!!busy" :aria-label="`Re-check MX for ${domainName}`" class="inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-xs font-medium text-blue-700 hover:bg-blue-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-blue-500 disabled:opacity-60"><RefreshCw :class="['h-3.5 w-3.5', { 'animate-spin': busy === 'checking' }]" aria-hidden="true" />Re-check</button>
    </div>
    <p class="mt-2 text-gray-600">{{ RECEIVING_EXPLAINER }}</p>
    <p v-if="problem" class="mt-2 text-amber-800">{{ problem }}</p>
    <p v-if="error" role="alert" class="mt-2 text-red-700">{{ error }}</p>
    <p v-if="notice" role="status" class="mt-2 text-green-700">{{ notice }}</p>

    <dl v-if="status" class="mt-3 grid gap-2 sm:grid-cols-4">
      <div v-for="field in fields" :key="field.label" class="flex min-w-0 items-start justify-between gap-2 rounded-lg bg-gray-50 p-2">
        <div class="min-w-0"><dt class="text-xs text-gray-500">{{ field.label }}</dt><dd class="mt-0.5 break-all font-mono text-gray-900">{{ field.value }}</dd></div>
        <button type="button" @click="copy(field.label, field.value)" :aria-label="`Copy ${field.label}`" class="shrink-0 rounded p-1 hover:bg-gray-200"><Check v-if="copied === field.label" class="h-4 w-4 text-green-600" aria-hidden="true" /><Copy v-else class="h-4 w-4 text-gray-400" aria-hidden="true" /></button>
      </div>
    </dl>
    <p v-if="status && status.existingMx.length && status.mxStatus !== 'published'" class="mt-2 text-xs text-gray-600">Current public MX: <span class="font-mono">{{ status.existingMx.join(', ') }}</span></p>
    <p v-if="status?.mxStatus === 'conflict'" class="mt-2 text-xs text-gray-600">Mailat never replaces another provider's MX. If this domain's mail should come to Mailat, change the record in your DNS yourself.</p>

    <div v-if="canManage" class="mt-3 flex flex-wrap gap-2">
      <button v-if="status && !status.enabled && !confirmOpen" type="button" @click="confirmOpen = true; error = ''" :disabled="!!busy || !canEnable" :aria-label="`Enable receiving for ${domainName}`" class="rounded-lg bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-50">Enable receiving</button>
      <button v-if="status?.enabled && status.mxStatus === 'missing' && !cloudflareOpen" type="button" @click="cloudflareOpen = true; error = ''" :disabled="!!busy" aria-label="Add MX to Cloudflare" class="inline-flex items-center gap-1.5 rounded-lg border border-orange-200 bg-orange-50 px-3 py-1.5 text-sm font-medium text-orange-800 hover:bg-orange-100 disabled:opacity-50"><Cloud class="h-4 w-4" aria-hidden="true" />Add MX to Cloudflare</button>
    </div>
    <p v-if="canManage && status && !status.enabled && !canEnable" class="mt-2 text-xs text-gray-500">Verify the domain for sending before enabling receiving.</p>

    <div v-if="canManage && confirmOpen" class="mt-3 rounded-lg border border-blue-200 bg-blue-50 p-3" aria-label="Confirm enabling receiving">
      <p class="font-semibold text-gray-900">Receiving is a separate choice</p>
      <p class="mt-2 text-gray-700">Pointing the root MX for {{ domainName }} to SES routes incoming mail to Mailat. It does not send a copy to your existing inbox provider.</p>
      <p class="mt-2 text-gray-700">To keep that provider, retain its root MX records and use its forwarding feature to a separately configured Mailat receiving address, or add a receiving subdomain to Mailat.</p>
      <p class="mt-2 text-gray-600">Enabling creates the SES receiving setup only. Mailat does not change your DNS: publish the MX record above yourself, or use Add MX to Cloudflare afterwards.</p>
      <p v-if="status?.existingMx.length" class="mt-2 text-amber-800">Mail for {{ domainName }} currently goes to {{ status.existingMx.join(', ') }}.</p>
      <div class="mt-3 flex gap-2">
        <button type="button" @click="enable" :disabled="!!busy" aria-label="Confirm enable receiving" class="rounded-lg bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50">{{ busy === 'enabling' ? 'Enabling…' : 'Enable receiving' }}</button>
        <button type="button" @click="confirmOpen = false" :disabled="busy === 'enabling'" class="rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-sm">Cancel</button>
      </div>
    </div>

    <form v-if="canManage && cloudflareOpen && status?.enabled && status.mxStatus === 'missing'" class="mt-3 rounded-lg border border-orange-200 bg-orange-50 p-3" @submit.prevent="addToCloudflare" aria-label="Add MX to Cloudflare">
      <label class="block text-gray-800">Cloudflare API token
        <input :value="cloudflareToken" @input="cloudflareToken = ($event.target as HTMLInputElement).value" type="password" autocomplete="off" class="mt-1 w-full rounded border border-gray-300 p-2 font-mono" placeholder="Token with Zone › DNS › Edit" />
      </label>
      <p class="mt-1 text-xs text-gray-600">The same token as Set up sending. It is used once and not stored. Only this MX is added, and only if the zone has no other root MX.</p>
      <div class="mt-2 flex gap-2">
        <button type="submit" :disabled="!!busy" aria-label="Add the MX record" class="rounded-lg bg-orange-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-orange-700 disabled:opacity-50">{{ busy === 'cloudflare' ? 'Adding…' : 'Add MX' }}</button>
        <button type="button" @click="cloudflareOpen = false; cloudflareToken = ''" :disabled="busy === 'cloudflare'" class="rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-sm">Cancel</button>
      </div>
    </form>
    <p v-if="checkedAt" class="mt-2 text-xs text-gray-500">Checked {{ checkedAt }}</p>
  </section>
</template>
