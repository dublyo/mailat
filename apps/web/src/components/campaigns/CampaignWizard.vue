<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { X, Check, ChevronLeft, ChevronRight, Send, Calendar, Save } from 'lucide-vue-next'
import Button from '@/components/common/Button.vue'
import WizardStepInfo from './WizardStepInfo.vue'
import WizardStepRecipients from './WizardStepRecipients.vue'
import WizardStepContent from './WizardStepContent.vue'
import WizardStepReview from './WizardStepReview.vue'
import WizardStepSchedule from './WizardStepSchedule.vue'
import { useCampaignsStore } from '@/stores/campaigns'
import { useDomainsStore } from '@/stores/domains'
import { useAuthStore } from '@/stores/auth'
import { listApi } from '@/lib/api'
import type { Campaign, ContactList, Identity } from '@/lib/api'

const props = defineProps<{
  campaign?: Campaign | null
}>()

const emit = defineEmits<{
  close: []
  created: [campaign: Campaign]
  updated: [campaign: Campaign]
}>()

const campaignsStore = useCampaignsStore()
const domainsStore = useDomainsStore()
const authStore = useAuthStore()

const currentStep = ref(1)
const isSubmitting = ref(false)
const error = ref<string | null>(null)
const lists = ref<ContactList[]>([])
const saveWarnings = ref<string[]>([])

// Every save, send, schedule and test goes through persist(): the first create
// stores the UUID and later calls update it, so a retry after a failed send never
// creates (and sends) a second campaign. A duplicate arrives with an empty UUID.
const savedUuid = ref(props.campaign?.uuid || '')
let pendingSave: Promise<Campaign> | null = null
let testAttempt: { uuid: string; email: string; key: string } | null = null

const steps = [
  { number: 1, title: 'Campaign Info', short: 'Info' },
  { number: 2, title: 'Select Recipients', short: 'Recipients' },
  { number: 3, title: 'Email Content', short: 'Content' },
  { number: 4, title: 'Review', short: 'Review' },
  { number: 5, title: 'Schedule', short: 'Schedule' }
]

// Form state
const formData = ref({
  name: '',
  subject: '',
  fromIdentityId: null as number | null,
  replyTo: '',
  listId: null as number | null,  // Single list ID (backend expects integer)
  selectedListUuid: '' as string,  // Track selected list UUID for UI
  htmlContent: '',
  textContent: '',
  trackOpens: true,
  trackClicks: true,
  scheduledAt: null as Date | null,
  sendOption: 'now' as 'now' | 'schedule'
})

// Validation state per step
const stepValidation = ref({
  step1: false,
  step2: false,
  step3: false,
  step4: true,
  step5: true
})

// Computed
const isEditing = computed(() => !!props.campaign?.uuid)
const canManageSettings = computed(() => ['owner', 'admin'].includes(authStore.user?.role || ''))
const canGoNext = computed(() => {
  switch (currentStep.value) {
    case 1: return stepValidation.value.step1
    case 2: return stepValidation.value.step2
    case 3: return stepValidation.value.step3
    case 4: return stepValidation.value.step4
    case 5: return stepValidation.value.step5
    default: return false
  }
})

const canGoBack = computed(() => currentStep.value > 1)
const isLastStep = computed(() => currentStep.value === 5)

// Campaigns send only from can_send identities on active, SES-verified domains.
// The server enforces this too; without a domain list we fall back to canSend.
const sendableIdentities = computed<Identity[]>(() => {
  const domains = domainsStore.domains
  return domainsStore.identities.filter(identity => {
    if (identity.canSend === false) return false
    if (!domains.length) return true
    const domain = domains.find(d => String(d.id) === String(identity.domainId))
    return !!domain && domain.sesVerified && domain.status === 'active'
  })
})

const selectedIdentity = computed(() => {
  return domainsStore.identities.find(i => Number(i.id) === formData.value.fromIdentityId)
})

// Methods
const goToStep = (step: number) => {
  if (step < currentStep.value || canGoNext.value) {
    currentStep.value = step
  }
}

const nextStep = () => {
  if (canGoNext.value && currentStep.value < 5) {
    currentStep.value++
  }
}

const prevStep = () => {
  if (currentStep.value > 1) {
    currentStep.value--
  }
}

const updateValidation = (step: number, isValid: boolean) => {
  const key = `step${step}` as keyof typeof stepValidation.value
  stepValidation.value[key] = isValid
}

const senderName = (identity: Identity) => {
  // An edited campaign keeps its display name while the sender is unchanged.
  const original = props.campaign
  if (original?.fromName && original.fromEmail?.toLowerCase() === identity.email.toLowerCase()) return original.fromName
  return identity.displayName?.trim() || identity.email.split('@')[0]
}

async function saveOnce(): Promise<Campaign> {
  const identity = selectedIdentity.value
  if (!identity) throw new Error('Please select a sender identity')
  if (!formData.value.listId) throw new Error('Please select a recipient list')
  const f = formData.value
  const base = {
    name: f.name,
    subject: f.subject,
    fromName: senderName(identity),
    fromEmail: identity.email,
    listId: f.listId!,
    htmlContent: f.htmlContent,
    trackOpens: f.trackOpens,
    trackClicks: f.trackClicks
  }
  let saved: Campaign
  if (savedUuid.value) {
    saved = await campaignsStore.updateCampaign(savedUuid.value, { ...base, replyTo: f.replyTo || null, textContent: f.textContent || null })
  } else {
    saved = await campaignsStore.createCampaign({ ...base, replyTo: f.replyTo || undefined, textContent: f.textContent || undefined })
    savedUuid.value = saved.uuid
  }
  saveWarnings.value = saved.warnings ?? []
  return saved
}

// Concurrent callers share one in-flight save, so a double click cannot create twice.
function persist(): Promise<Campaign> {
  if (!pendingSave) pendingSave = saveOnce().finally(() => { pendingSave = null })
  return pendingSave
}

// Returns the saved UUID; the review step uses it for the audience estimate.
const ensureSaved = async () => (await persist()).uuid

const finish = (campaign: Campaign) => {
  if (isEditing.value) emit('updated', campaign)
  else emit('created', campaign)
}

const submit = async (fallback: string, action: () => Promise<Campaign>) => {
  isSubmitting.value = true
  error.value = null
  try {
    finish(await action())
  } catch (e) {
    // Server 400s are validation messages (sender, feedback, postal address, audience); show them as-is.
    error.value = e instanceof Error ? e.message : fallback
  } finally {
    isSubmitting.value = false
  }
}

const saveDraft = () => submit('Failed to save campaign', persist)

const scheduleCampaign = () => {
  const when = formData.value.scheduledAt
  if (!when) return
  return submit('Failed to schedule campaign', async () => {
    await persist()
    return campaignsStore.scheduleCampaign(savedUuid.value, when.toISOString())
  })
}

const sendNow = () => submit('Failed to send campaign', async () => {
  await persist()
  return campaignsStore.sendCampaign(savedUuid.value)
})

// Test sends go through the campaign endpoint (same renderer, footer and headers).
// The idempotency key is reused only when retrying the same address after a
// request that got no answer, so a lost response never sends twice.
const sendTestEmail = async (email: string) => {
  error.value = null
  try {
    const uuid = (await persist()).uuid
    if (!testAttempt || testAttempt.uuid !== uuid || testAttempt.email !== email) {
      testAttempt = { uuid, email, key: crypto.randomUUID() }
    }
    const result = await campaignsStore.sendTestCampaign(uuid, [email], testAttempt.key)
    testAttempt = null
    const failed = result.results?.find(r => r.status !== 'sent')
    if (failed) {
      throw new Error(failed.error || (failed.status === 'unknown'
        ? 'The test send outcome is uncertain. Check your inbox before retrying.'
        : 'Test send failed'))
    }
    if (result.status === 'sending') throw new Error('The test send is still in progress. Check your inbox before retrying.')
  } catch (e) {
    const status = (e as { status?: number }).status
    if (status && status < 500) testAttempt = null
    throw e
  }
}

// Load data on mount
onMounted(async () => {
  try {
    const [listsResponse] = await Promise.all([
      listApi.list(),
      domainsStore.fetchIdentities(),
      domainsStore.fetchDomains()
    ])
    lists.value = listsResponse || []
  } catch (e) {
    console.error('Failed to load data:', e)
  }

  // Edit and duplicate: map the flat API campaign onto the form.
  const c = props.campaign
  if (c) {
    formData.value.name = c.name
    formData.value.subject = c.subject
    formData.value.replyTo = c.replyTo || ''
    formData.value.htmlContent = c.htmlContent || ''
    formData.value.textContent = c.textContent || ''
    formData.value.trackOpens = c.trackOpens !== false
    formData.value.trackClicks = c.trackClicks !== false
    if (c.listId) {
      formData.value.listId = Number(c.listId)
      formData.value.selectedListUuid = lists.value.find(l => Number(l.id) === Number(c.listId))?.uuid || ''
    }
    const fromEmail = (c.fromEmail || '').toLowerCase()
    const identity = sendableIdentities.value.find(i => i.email.toLowerCase() === fromEmail)
    formData.value.fromIdentityId = identity ? Number(identity.id) : null
  }

  // New campaigns default to the default sendable identity; an edit never switches sender silently.
  if (!c && !formData.value.fromIdentityId && sendableIdentities.value.length > 0) {
    const defaultIdentity = sendableIdentities.value.find(i => i.isDefault) || sendableIdentities.value[0]
    formData.value.fromIdentityId = Number(defaultIdentity.id)
  }
})
</script>

<template>
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
    <div class="bg-white rounded-xl shadow-2xl w-full max-w-4xl mx-4 max-h-[90vh] flex flex-col overflow-hidden">
      <!-- Header -->
      <div class="flex items-center justify-between px-6 py-4 border-b bg-gradient-to-r from-gmail-blue to-blue-600">
        <div>
          <h2 class="text-xl font-semibold text-white">
            {{ isEditing ? 'Edit Campaign' : 'Create New Campaign' }}
          </h2>
          <p class="text-blue-100 text-sm mt-0.5">Step {{ currentStep }} of 5</p>
        </div>
        <button
          @click="emit('close')"
          class="p-2 hover:bg-white/20 rounded-lg transition-colors"
        >
          <X class="w-5 h-5 text-white" />
        </button>
      </div>

      <!-- Step Indicator -->
      <div class="px-6 py-4 bg-gray-50 border-b">
        <div class="flex items-center justify-between">
          <template v-for="(step, index) in steps" :key="step.number">
            <button
              @click="goToStep(step.number)"
              :disabled="step.number > currentStep && !canGoNext"
              class="flex items-center gap-2 group"
              :class="{
                'cursor-pointer': step.number <= currentStep || canGoNext,
                'cursor-not-allowed opacity-50': step.number > currentStep && !canGoNext
              }"
            >
              <div
                class="w-8 h-8 rounded-full flex items-center justify-center text-sm font-medium transition-all"
                :class="{
                  'bg-gmail-blue text-white': currentStep === step.number,
                  'bg-green-500 text-white': step.number < currentStep,
                  'bg-gray-200 text-gray-500': step.number > currentStep
                }"
              >
                <Check v-if="step.number < currentStep" class="w-4 h-4" />
                <span v-else>{{ step.number }}</span>
              </div>
              <span
                class="text-sm font-medium hidden sm:block"
                :class="{
                  'text-gmail-blue': currentStep === step.number,
                  'text-green-600': step.number < currentStep,
                  'text-gray-400': step.number > currentStep
                }"
              >
                {{ step.short }}
              </span>
            </button>
            <div
              v-if="index < steps.length - 1"
              class="flex-1 h-0.5 mx-2"
              :class="{
                'bg-green-500': step.number < currentStep,
                'bg-gray-200': step.number >= currentStep
              }"
            />
          </template>
        </div>
      </div>

      <!-- Error Alert -->
      <div v-if="error" class="mx-6 mt-4 p-4 bg-red-50 border border-red-200 rounded-lg">
        <p class="text-red-700 text-sm">{{ error }}</p>
      </div>

      <!-- Step Content -->
      <div class="flex-1 overflow-y-auto p-6">
        <WizardStepInfo
          v-if="currentStep === 1"
          v-model:name="formData.name"
          v-model:subject="formData.subject"
          v-model:fromIdentityId="formData.fromIdentityId"
          v-model:replyTo="formData.replyTo"
          v-model:trackOpens="formData.trackOpens"
          v-model:trackClicks="formData.trackClicks"
          :identities="sendableIdentities"
          @update:valid="(v) => updateValidation(1, v)"
        />

        <WizardStepRecipients
          v-if="currentStep === 2"
          v-model:selectedListId="formData.listId"
          v-model:selectedListUuid="formData.selectedListUuid"
          @update:valid="(v) => updateValidation(2, v)"
        />

        <WizardStepContent
          v-if="currentStep === 3"
          v-model:htmlContent="formData.htmlContent"
          v-model:textContent="formData.textContent"
          @update:valid="(v) => updateValidation(3, v)"
        />

        <WizardStepReview
          v-if="currentStep === 4"
          :name="formData.name"
          :subject="formData.subject"
          :fromIdentityId="formData.fromIdentityId"
          :replyTo="formData.replyTo"
          :selectedListUuid="formData.selectedListUuid"
          :htmlContent="formData.htmlContent"
          :textContent="formData.textContent"
          :identities="domainsStore.identities"
          :lists="lists"
          :trackOpens="formData.trackOpens"
          :trackClicks="formData.trackClicks"
          :campaignUuid="savedUuid"
          :warnings="saveWarnings"
          :canManageSettings="canManageSettings"
          :ensureSaved="ensureSaved"
          :onSendTest="sendTestEmail"
        />

        <WizardStepSchedule
          v-if="currentStep === 5"
          v-model:sendOption="formData.sendOption"
          v-model:scheduledAt="formData.scheduledAt"
          @update:valid="(v) => updateValidation(5, v)"
        />
      </div>

      <!-- Footer -->
      <div class="flex items-center justify-between px-6 py-4 border-t bg-gray-50">
        <div class="flex items-center gap-2">
          <Button
            v-if="canGoBack"
            variant="secondary"
            @click="prevStep"
            :disabled="isSubmitting"
          >
            <ChevronLeft class="w-4 h-4" />
            Back
          </Button>
          <Button
            v-else
            variant="secondary"
            @click="emit('close')"
            :disabled="isSubmitting"
          >
            Cancel
          </Button>
        </div>

        <div class="flex items-center gap-2">
          <!-- Save Draft (always available) -->
          <Button
            v-if="currentStep >= 3 && stepValidation.step1 && stepValidation.step3"
            variant="secondary"
            @click="saveDraft"
            :disabled="isSubmitting"
            :loading="isSubmitting"
          >
            <Save class="w-4 h-4" />
            Save Draft
          </Button>

          <!-- Next Step -->
          <Button
            v-if="!isLastStep"
            @click="nextStep"
            :disabled="!canGoNext || isSubmitting"
          >
            Next
            <ChevronRight class="w-4 h-4" />
          </Button>

          <!-- Final Actions -->
          <template v-if="isLastStep">
            <Button
              v-if="formData.sendOption === 'schedule' && formData.scheduledAt"
              @click="scheduleCampaign"
              :disabled="isSubmitting"
              :loading="isSubmitting"
              class="bg-amber-500 hover:bg-amber-600"
            >
              <Calendar class="w-4 h-4" />
              Schedule Campaign
            </Button>
            <Button
              v-if="formData.sendOption === 'now'"
              @click="sendNow"
              :disabled="isSubmitting"
              :loading="isSubmitting"
              class="bg-green-600 hover:bg-green-700"
            >
              <Send class="w-4 h-4" />
              Send Now
            </Button>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>
