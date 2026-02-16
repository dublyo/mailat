<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { X, Check, ChevronLeft, ChevronRight, Send, Calendar, Save } from 'lucide-vue-next'
import Button from '@/components/common/Button.vue'
import WizardStepInfo from './WizardStepInfo.vue'
import WizardStepRecipients from './WizardStepRecipients.vue'
import WizardStepContent from './WizardStepContent.vue'
import WizardStepReview from './WizardStepReview.vue'
import WizardStepSchedule from './WizardStepSchedule.vue'
import { useCampaignsStore } from '@/stores/campaigns'
import { useContactsStore } from '@/stores/contacts'
import { useDomainsStore } from '@/stores/domains'
import { listApi, composeApi } from '@/lib/api'
import type { Campaign, ContactList } from '@/lib/api'

const props = defineProps<{
  campaign?: Campaign | null
}>()

const emit = defineEmits<{
  close: []
  created: [campaign: Campaign]
  updated: [campaign: Campaign]
}>()

const campaignsStore = useCampaignsStore()
const contactsStore = useContactsStore()
const domainsStore = useDomainsStore()

const currentStep = ref(1)
const isSubmitting = ref(false)
const error = ref<string | null>(null)
const lists = ref<ContactList[]>([])

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
const isEditing = computed(() => !!props.campaign)
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

const estimatedRecipients = computed(() => {
  const selectedList = lists.value.find(list => list.uuid === formData.value.selectedListUuid)
  return selectedList?.contactCount || 0
})

const selectedIdentity = computed(() => {
  return domainsStore.identities.find(i => Number(i.id) === formData.value.fromIdentityId)
})

const selectedLists = computed(() => {
  const selectedList = lists.value.find(list => list.uuid === formData.value.selectedListUuid)
  return selectedList ? [selectedList] : []
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

const saveDraft = async () => {
  isSubmitting.value = true
  error.value = null
  try {
    const identity = selectedIdentity.value
    if (!identity) {
      error.value = 'Please select a sender identity'
      isSubmitting.value = false
      return
    }

    if (!formData.value.listId) {
      error.value = 'Please select a recipient list'
      isSubmitting.value = false
      return
    }

    const data = {
      name: formData.value.name,
      subject: formData.value.subject,
      fromName: identity.displayName,
      fromEmail: identity.email,
      replyTo: formData.value.replyTo || undefined,
      listId: formData.value.listId,
      htmlContent: formData.value.htmlContent,
      textContent: formData.value.textContent || undefined
    }

    if (isEditing.value && props.campaign) {
      const updated = await campaignsStore.updateCampaign(props.campaign.uuid, data)
      emit('updated', updated)
    } else {
      const created = await campaignsStore.createCampaign(data)
      emit('created', created)
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to save campaign'
  } finally {
    isSubmitting.value = false
  }
}

const scheduleCampaign = async () => {
  if (!formData.value.scheduledAt) return
  isSubmitting.value = true
  error.value = null
  try {
    const identity = selectedIdentity.value
    if (!identity) {
      error.value = 'Please select a sender identity'
      isSubmitting.value = false
      return
    }

    if (!formData.value.listId) {
      error.value = 'Please select a recipient list'
      isSubmitting.value = false
      return
    }

    let campaign: Campaign
    const data = {
      name: formData.value.name,
      subject: formData.value.subject,
      fromName: identity.displayName,
      fromEmail: identity.email,
      replyTo: formData.value.replyTo || undefined,
      listId: formData.value.listId,
      htmlContent: formData.value.htmlContent,
      textContent: formData.value.textContent || undefined
    }

    if (isEditing.value && props.campaign) {
      campaign = await campaignsStore.updateCampaign(props.campaign.uuid, data)
    } else {
      campaign = await campaignsStore.createCampaign(data)
    }

    const scheduledAtISO = formData.value.scheduledAt.toISOString()
    await campaignsStore.scheduleCampaign(campaign.uuid, scheduledAtISO)
    const updatedCampaign = { ...campaign, status: 'scheduled' as const, scheduledAt: scheduledAtISO }
    if (isEditing.value) {
      emit('updated', updatedCampaign)
    } else {
      emit('created', updatedCampaign)
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to schedule campaign'
  } finally {
    isSubmitting.value = false
  }
}

const sendNow = async () => {
  isSubmitting.value = true
  error.value = null
  try {
    const identity = selectedIdentity.value
    if (!identity) {
      error.value = 'Please select a sender identity'
      isSubmitting.value = false
      return
    }

    if (!formData.value.listId) {
      error.value = 'Please select a recipient list'
      isSubmitting.value = false
      return
    }

    let campaign: Campaign
    const data = {
      name: formData.value.name,
      subject: formData.value.subject,
      fromName: identity.displayName,
      fromEmail: identity.email,
      replyTo: formData.value.replyTo || undefined,
      listId: formData.value.listId,
      htmlContent: formData.value.htmlContent,
      textContent: formData.value.textContent || undefined
    }

    if (isEditing.value && props.campaign) {
      campaign = await campaignsStore.updateCampaign(props.campaign.uuid, data)
    } else {
      campaign = await campaignsStore.createCampaign(data)
    }

    await campaignsStore.sendCampaign(campaign.uuid)
    const updatedCampaign = { ...campaign, status: 'sending' as const }
    if (isEditing.value) {
      emit('updated', updatedCampaign)
    } else {
      emit('created', updatedCampaign)
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to send campaign'
  } finally {
    isSubmitting.value = false
  }
}

const sendTestEmail = async (email: string) => {
  const identity = selectedIdentity.value
  if (!identity) {
    error.value = 'Please select a sender identity'
    return
  }

  try {
    await composeApi.send({
      identityId: identity.id,
      to: [email],
      subject: `[TEST] ${formData.value.subject}`,
      body: formData.value.textContent || '',
      htmlBody: formData.value.htmlContent || undefined
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to send test email'
    throw e
  }
}

// Load data on mount
onMounted(async () => {
  try {
    const [listsResponse] = await Promise.all([
      listApi.list(),
      domainsStore.fetchIdentities()
    ])
    lists.value = listsResponse || []
  } catch (e) {
    console.error('Failed to load data:', e)
  }

  // If editing, populate form
  if (props.campaign) {
    formData.value.name = props.campaign.name
    formData.value.subject = props.campaign.subject
    formData.value.fromIdentityId = props.campaign.fromIdentityId || null
    formData.value.replyTo = props.campaign.replyTo || ''
    // Handle listId from campaign (could be single or first from array)
    if (props.campaign.listIds && props.campaign.listIds.length > 0) {
      const firstListUuid = props.campaign.listIds[0]
      formData.value.selectedListUuid = firstListUuid
      // Try to find the list to get its numeric ID
      const matchedList = lists.value.find(l => l.uuid === firstListUuid)
      if (matchedList) {
        formData.value.listId = typeof matchedList.id === 'string' ? parseInt(matchedList.id, 10) : matchedList.id
      }
    }
    formData.value.htmlContent = props.campaign.htmlBody || ''
    formData.value.textContent = props.campaign.textBody || ''
  }

  // Set default identity
  if (!formData.value.fromIdentityId && domainsStore.identities.length > 0) {
    const defaultIdentity = domainsStore.identities.find(i => i.isDefault) || domainsStore.identities[0]
    formData.value.fromIdentityId = Number(defaultIdentity.id)
  }
})

// Validation is now handled by child components emitting update:valid events
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
          :identities="domainsStore.identities"
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
