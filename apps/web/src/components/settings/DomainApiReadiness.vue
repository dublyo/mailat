<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { CheckCircle, Circle, AlertCircle, RefreshCw, Copy } from 'lucide-vue-next'
import { api, domainApi, identityApi, type DomainReadiness, type DomainReadinessItem } from '@/lib/api'
import { ASK_ADMIN, needsAdmin, readinessActions, readinessBadge, readinessCopy, readinessSummary, type ReadinessAction } from '@/lib/domainReadiness'

// refreshKey changes when the card's domain or identities change elsewhere
// (verify, add identity), so the checklist re-reads without a manual refresh.
const props = defineProps<{ domainUuid: string; domainName: string; canManage: boolean; refreshKey?: string }>()
const emit = defineEmits<{ (e: 'action', action: ReadinessAction): void; (e: 'changed'): void }>()
const status = ref<DomainReadiness | null>(null)
const busy = ref('')
const error = ref('')
const copied = ref('')
let requestVersion = 0
let controller: AbortController | undefined

const summary = computed(() => readinessSummary(status.value))
const badgeClass: Record<string, string> = {
  success: 'bg-green-50 text-green-700', warning: 'bg-amber-50 text-amber-700', error: 'bg-red-50 text-red-700', info: 'bg-blue-50 text-blue-700', default: 'bg-gray-100 text-gray-600',
}

async function load() {
  controller?.abort()
  controller = new AbortController()
  const version = ++requestVersion
  const token = api.getToken()
  const domain = props.domainUuid
  busy.value = busy.value || 'checking'
  error.value = ''
  try {
    const result = await domainApi.readiness(domain, controller.signal)
    if (version === requestVersion && token === api.getToken() && result.domainUuid === props.domainUuid) status.value = result
  } catch (e) {
    if (version === requestVersion && token === api.getToken()) error.value = e instanceof Error ? e.message : 'The checklist could not be loaded. Try again.'
  } finally {
    if (version === requestVersion && token === api.getToken()) busy.value = ''
  }
}

async function run(action: ReadinessAction) {
  if (busy.value) return
  if (action.kind !== 'setup_sending' && action.kind !== 'create_identity') {
    emit('action', action)
    return
  }
  busy.value = action.kind
  error.value = ''
  try {
    if (action.kind === 'setup_sending') await domainApi.setupSending(props.domainUuid)
    else await identityApi.create({ displayName: props.domainName, email: action.address || '', domainId: props.domainUuid })
    emit('changed')
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'That did not work. Try again.'
    busy.value = ''
    return
  }
  busy.value = 'checking'
  await load()
}

async function copy(value: string) {
  try {
    await navigator.clipboard.writeText(value)
    copied.value = value
  } catch {
    copied.value = ''
  }
}

function actionsFor(item: DomainReadinessItem) {
  return readinessActions(item, { canManage: props.canManage, suggestedIdentity: status.value?.suggestedIdentity || '' })
}

watch(() => [props.domainUuid, props.refreshKey], () => load(), { immediate: true })
onBeforeUnmount(() => { requestVersion++; controller?.abort() })
</script>

<template>
  <section :id="`readiness-${domainUuid}`" :aria-label="`API sending readiness for ${domainName}`" :aria-busy="!!busy" class="mt-4 rounded-xl border border-gray-200 bg-white p-4 text-sm">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h4 class="font-semibold text-gray-900">API sending readiness</h4>
      <button type="button" @click="load()" :disabled="!!busy" :aria-label="`Refresh API sending readiness for ${domainName}`" class="inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-xs font-medium text-blue-700 hover:bg-blue-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-blue-500 disabled:opacity-60"><RefreshCw :class="['h-3.5 w-3.5', { 'animate-spin': busy === 'checking' }]" aria-hidden="true" />Re-check</button>
    </div>
    <p role="status" aria-live="polite" class="mt-1 font-medium" :class="status?.ready ? 'text-green-700' : 'text-gray-700'">{{ busy === 'checking' && !status ? 'Checking…' : summary }}</p>
    <p v-if="error" role="alert" class="mt-2 text-red-700">{{ error }}</p>
    <ul v-if="status" class="mt-3 space-y-2">
      <li v-for="item in status.items" :key="item.key" :data-item="item.key" class="rounded-lg bg-gray-50 p-3">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="flex items-center gap-2 font-medium text-gray-900">
            <CheckCircle v-if="item.status === 'ok'" class="h-4 w-4 text-green-600" aria-hidden="true" />
            <AlertCircle v-else-if="item.status === 'attention'" class="h-4 w-4 text-red-600" aria-hidden="true" />
            <Circle v-else class="h-4 w-4 text-gray-400" aria-hidden="true" />
            {{ item.label }}
          </span>
          <span class="rounded-full px-2 py-0.5 text-xs font-medium" :class="badgeClass[readinessBadge(item.status).variant]">{{ readinessBadge(item.status).label }}</span>
        </div>
        <p v-if="item.detail" class="mt-1 text-gray-600">{{ item.detail }}</p>
        <div v-if="readinessCopy(item, status.domain)" class="mt-2 flex items-center gap-2 break-all">
          <span class="text-xs text-gray-500">{{ readinessCopy(item, status.domain)?.label }}</span>
          <code class="rounded bg-white px-2 py-1 font-mono text-xs">{{ item.value }}</code>
          <button type="button" @click="copy(item.value)" :aria-label="`Copy ${item.label} value`" class="rounded p-1 hover:bg-gray-200"><Copy class="h-3.5 w-3.5 text-gray-500" aria-hidden="true" /></button>
          <span v-if="copied === item.value" class="text-xs text-green-700">Copied</span>
        </div>
        <div v-if="actionsFor(item).length" class="mt-2 flex flex-wrap gap-2">
          <button v-for="action in actionsFor(item)" :key="action.kind" type="button" @click="run(action)" :disabled="!!busy"
            class="rounded-lg px-3 py-1.5 text-xs font-medium focus-visible:outline focus-visible:outline-2 focus-visible:outline-blue-500 disabled:cursor-not-allowed disabled:opacity-50"
            :class="action.kind === 'setup_sending' || action.kind === 'create_identity' || action.kind === 'verify' ? 'bg-blue-600 text-white hover:bg-blue-700' : 'border border-gray-300 bg-white text-gray-700 hover:bg-gray-100'">
            {{ busy === action.kind ? 'Working…' : action.label }}
          </button>
        </div>
        <p v-if="needsAdmin(item, canManage)" class="mt-2 text-xs text-gray-500">{{ ASK_ADMIN }}</p>
      </li>
    </ul>
  </section>
</template>
