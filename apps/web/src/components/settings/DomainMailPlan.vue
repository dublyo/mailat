<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Users, CheckCircle } from 'lucide-vue-next'
import { identityApi, mailboxAdminApi, receivedInboxApi } from '@/lib/api'
import { MAIL_PLANS, domainInUse, includesMe, includesPrivate, localPartError, markMailPlanAnswered, planSteps, recalledMailPlan, rememberMailPlan, type MailPlan } from '@/lib/domainMailPlan'

// "Who reads mail on this domain?" for a verified domain nobody uses yet. It
// only calls the same endpoints as the buttons elsewhere on the domain page
// (enable receiving, add address, choose catch-all) and never publishes DNS.
export interface MailPlanResult { plan: MailPlan; done: string[]; error?: string }
// result: a setup the page keeps, so the summary survives the domain list reloading.
// ownerName: the From name for the new address (the signed-in admin's name).
const props = defineProps<{ domainId: number; domainUuid: string; domainName: string; receivingEnabled: boolean; ownerName?: string; result?: MailPlanResult | null }>()
const emit = defineEmits<{ (e: 'changed'): void; (e: 'finished', result: MailPlanResult): void; (e: 'close'): void; (e: 'inUse'): void }>()
const router = useRouter()

const plan = ref<MailPlan>(recalledMailPlan(props.domainName) || 'me')
const localPart = ref('hello')
const catchAll = ref(true)
const busy = ref(false)
const error = ref('')
const done = ref<string[]>([])
const localResult = ref<MailPlanResult | null>(null)
const summary = computed(() => props.result || localResult.value)
// Shown only once the organization-wide check says nobody gets mail here yet.
const ready = ref(false)
const helpId = `mail-plan-address-help-${props.domainUuid}`

onMounted(async () => {
  if (props.result) { ready.value = true; return }
  try {
    if (domainInUse(await mailboxAdminApi.list(props.domainUuid))) { emit('inUse'); return }
  } catch {
    // Unknown: offer the step, but never claim the catch-all without knowing it is free.
    catchAll.value = false
  }
  ready.value = true
})

const addressError = computed(() => includesMe(plan.value) ? localPartError(localPart.value) : '')
const steps = computed(() => planSteps(plan.value, { domain: props.domainName, localPart: localPart.value, catchAll: catchAll.value, receivingEnabled: props.receivingEnabled }))

async function run() {
  if (busy.value || addressError.value) return
  // The choices are fixed for this run even if the form changes meanwhile.
  const chosen = plan.value
  const address = `${localPart.value.trim().toLowerCase()}@${props.domainName}`
  const wantCatchAll = catchAll.value
  error.value = ''
  if (chosen === 'sending') {
    rememberMailPlan(props.domainName, 'sending')
    markMailPlanAnswered(props.domainName)
    emit('close')
    return
  }
  busy.value = true
  done.value = []
  try {
    if (!props.receivingEnabled) {
      try {
        await receivedInboxApi.setupReceiving(props.domainId)
      } catch (e) {
        // Turned on meanwhile (another tab or admin): that step is done.
        if (!(e instanceof Error && /already enabled/i.test(e.message))) throw e
      }
      done.value.push(`Receiving is on for ${props.domainName}. Publish the MX record shown under Receiving to start getting mail.`)
    }
    if (includesMe(chosen)) {
      const identity = await identityApi.create({ displayName: props.ownerName?.trim() || props.domainName, email: address, domainId: props.domainUuid })
      done.value.push(`${address} now arrives in your Inbox.`)
      if (wantCatchAll) {
        await mailboxAdminApi.setCatchAll(props.domainUuid, identity.uuid)
        done.value.push(`Every other address on ${props.domainName} also comes to ${address} (catch-all).`)
      }
    }
    rememberMailPlan(props.domainName, chosen)
    markMailPlanAnswered(props.domainName)
    localResult.value = { plan: chosen, done: [...done.value] }
    emit('finished', localResult.value)
    emit('changed')
    if (includesPrivate(chosen)) void router.push({ path: `/domains/${props.domainUuid}/mailboxes`, query: { new: '1' } })
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Setup could not finish.'
    // Keep what was done and why it stopped: the page reloads the domain, and
    // without this the panel would disappear with the error.
    if (done.value.length) {
      markMailPlanAnswered(props.domainName)
      localResult.value = { plan: chosen, done: [...done.value], error: error.value }
      emit('finished', localResult.value)
      emit('changed')
    }
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section v-if="ready" :aria-label="`Who reads mail on ${domainName}`" class="mt-4 rounded-xl border border-blue-200 bg-blue-50/60 p-4 text-sm">
    <template v-if="!summary">
      <h4 class="flex items-center gap-2 font-semibold text-gray-900"><Users class="w-4 h-4 text-blue-600" aria-hidden="true" />Who reads mail on {{ domainName }}?</h4>
      <p class="mt-1 text-gray-600">You can change this later: add addresses, private logins or a catch-all at any time.</p>
      <fieldset :disabled="busy" class="mt-3 grid gap-2 sm:grid-cols-2">
        <legend class="sr-only">Who reads mail on {{ domainName }}</legend>
        <label v-for="option in MAIL_PLANS" :key="option.value"
          class="flex cursor-pointer gap-2 rounded-lg border bg-white p-3"
          :class="plan === option.value ? 'border-blue-500 ring-1 ring-blue-500' : 'border-gray-200 hover:border-gray-300'">
          <input type="radio" :name="`mail-plan-${domainUuid}`" :value="option.value" :checked="plan === option.value" @change="plan = option.value" class="mt-0.5" />
          <span><span class="block font-medium text-gray-900">{{ option.title }}</span><span class="block text-xs text-gray-600 mt-0.5">{{ option.detail }}</span></span>
        </label>
      </fieldset>

      <fieldset v-if="includesMe(plan)" :disabled="busy" class="mt-3 grid gap-2">
        <label class="block font-medium text-gray-800">Your address
          <span class="mt-1 flex items-center rounded-lg border border-gray-300 bg-white focus-within:ring-2 focus-within:ring-blue-500">
            <input v-model="localPart" autocomplete="off" maxlength="64" class="min-w-0 flex-1 rounded-l-lg p-2 outline-none"
              :aria-invalid="!!addressError" :aria-describedby="addressError ? helpId : undefined" />
            <span class="px-2 text-gray-500">@{{ domainName }}</span>
          </span>
        </label>
        <p v-if="addressError" :id="helpId" role="alert" class="text-xs text-red-700">{{ addressError }}</p>
        <label class="flex items-start gap-2 text-gray-700"><input v-model="catchAll" type="checkbox" class="mt-0.5" />
          <span>Also send me mail for any other address on {{ domainName }} (catch-all), such as sales@ or support@. Private logins still get their own mail.</span>
        </label>
      </fieldset>

      <div class="mt-3">
        <p class="font-medium text-gray-800">What happens</p>
        <ol class="mt-1 list-decimal pl-5 text-gray-700 space-y-0.5">
          <li v-for="step in steps" :key="step">{{ step }}</li>
        </ol>
      </div>
      <p v-if="error" role="alert" class="mt-3 text-red-700">{{ error }}</p>
      <div class="mt-4 flex flex-wrap gap-2">
        <button type="button" @click="run" :disabled="busy || !!addressError"
          class="rounded-lg bg-blue-600 px-3 py-2 font-medium text-white hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-50">
          {{ busy ? 'Setting up…' : plan === 'sending' ? 'Sending only' : plan === 'private' ? 'Continue to private logins' : 'Set up' }}
        </button>
        <button type="button" @click="emit('close')" :disabled="busy" class="rounded-lg border border-gray-300 bg-white px-3 py-2 font-medium text-gray-700 hover:bg-gray-50">Not now</button>
      </div>
    </template>
    <template v-else>
      <h4 v-if="!summary.error" class="flex items-center gap-2 font-semibold text-green-800"><CheckCircle class="w-4 h-4" aria-hidden="true" />{{ domainName }} is set up</h4>
      <h4 v-else class="font-semibold text-amber-800">{{ domainName }} is partly set up</h4>
      <ul class="mt-2 text-gray-700 space-y-1"><li v-for="item in summary.done" :key="item">✓ {{ item }}</li></ul>
      <p v-if="summary.error" role="alert" class="mt-2 text-red-700">Stopped: {{ summary.error }} Finish it from the Addresses tab or the Receiving section.</p>
      <p v-else-if="includesPrivate(summary.plan)" class="mt-2 text-gray-700">Opening Private logins…</p>
      <div class="mt-3 flex flex-wrap gap-2">
        <router-link v-if="includesMe(summary.plan) && !summary.error" :to="{ path: '/received', query: { domain: String(domainId) } }" class="rounded-lg bg-blue-600 px-3 py-2 font-medium text-white hover:bg-blue-700">Open {{ domainName }} inbox</router-link>
        <router-link :to="`/domains/${domainUuid}/mailboxes`" class="rounded-lg border border-gray-300 bg-white px-3 py-2 font-medium text-gray-700 hover:bg-gray-50">Private logins</router-link>
        <button type="button" @click="emit('close')" class="rounded-lg px-3 py-2 font-medium text-gray-600 hover:bg-gray-100">Close</button>
      </div>
    </template>
  </section>
</template>
