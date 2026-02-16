<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import {
  Calendar,
  Clock,
  Zap,
  CalendarDays,
  AlertCircle,
  CheckCircle,
  Info
} from 'lucide-vue-next'
import { VueDatePicker } from '@vuepic/vue-datepicker'
import '@vuepic/vue-datepicker/dist/main.css'

const props = defineProps<{
  sendOption: 'now' | 'schedule'
  scheduledAt: Date | null
}>()

const emit = defineEmits<{
  'update:sendOption': [value: 'now' | 'schedule']
  'update:scheduledAt': [value: Date | null]
  'update:valid': [value: boolean]
}>()

const localSendOption = ref<'now' | 'schedule'>(props.sendOption)
const localScheduledAt = ref<Date | null>(props.scheduledAt)

// Sync with props
watch(() => props.sendOption, (val) => {
  localSendOption.value = val
})

watch(() => props.scheduledAt, (val) => {
  localScheduledAt.value = val
})

// Emit changes
watch(localSendOption, (val) => {
  emit('update:sendOption', val)
})

watch(localScheduledAt, (val) => {
  emit('update:scheduledAt', val)
})

const minDate = computed(() => {
  const now = new Date()
  now.setMinutes(now.getMinutes() + 5) // Minimum 5 minutes from now
  return now
})

const isValidSchedule = computed(() => {
  if (localSendOption.value === 'now') return true
  if (!localScheduledAt.value) return false
  return new Date(localScheduledAt.value) > new Date()
})

const isValid = computed(() => {
  return isValidSchedule.value
})

watch(isValid, (valid) => {
  emit('update:valid', valid)
}, { immediate: true })

const formatScheduledTime = (date: Date | null) => {
  if (!date) return ''
  return new Intl.DateTimeFormat('en-US', {
    weekday: 'long',
    year: 'numeric',
    month: 'long',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
    timeZoneName: 'short'
  }).format(new Date(date))
}

// Suggested send times
const suggestedTimes = [
  { label: 'Tomorrow 9 AM', getDate: () => {
    const d = new Date()
    d.setDate(d.getDate() + 1)
    d.setHours(9, 0, 0, 0)
    return d
  }},
  { label: 'Tomorrow 2 PM', getDate: () => {
    const d = new Date()
    d.setDate(d.getDate() + 1)
    d.setHours(14, 0, 0, 0)
    return d
  }},
  { label: 'Next Monday 10 AM', getDate: () => {
    const d = new Date()
    const daysUntilMonday = (8 - d.getDay()) % 7 || 7
    d.setDate(d.getDate() + daysUntilMonday)
    d.setHours(10, 0, 0, 0)
    return d
  }}
]

const applySuggestedTime = (getDate: () => Date) => {
  localScheduledAt.value = getDate()
}
</script>

<template>
  <div class="space-y-6 max-w-2xl">
    <div class="text-center mb-8">
      <div class="w-16 h-16 bg-indigo-100 rounded-full flex items-center justify-center mx-auto mb-4">
        <Calendar class="w-8 h-8 text-indigo-600" />
      </div>
      <h3 class="text-xl font-semibold text-gray-900">Schedule Your Campaign</h3>
      <p class="text-gray-500 mt-1">Choose when to send your campaign</p>
    </div>

    <!-- Send Options -->
    <div class="space-y-4">
      <!-- Send Now Option -->
      <div
        @click="localSendOption = 'now'"
        :class="[
          'relative p-5 border-2 rounded-xl cursor-pointer transition-all',
          localSendOption === 'now'
            ? 'border-indigo-500 bg-indigo-50/50 ring-2 ring-indigo-500/20'
            : 'border-gray-200 hover:border-gray-300 bg-white'
        ]"
      >
        <div class="flex items-start gap-4">
          <div
            :class="[
              'w-12 h-12 rounded-xl flex items-center justify-center flex-shrink-0',
              localSendOption === 'now' ? 'bg-indigo-200' : 'bg-gray-100'
            ]"
          >
            <Zap :class="['w-6 h-6', localSendOption === 'now' ? 'text-indigo-700' : 'text-gray-500']" />
          </div>
          <div class="flex-1">
            <div class="flex items-center gap-2">
              <h4 class="font-semibold text-gray-900">Send Now</h4>
              <span v-if="localSendOption === 'now'" class="px-2 py-0.5 bg-indigo-100 text-indigo-700 text-xs font-medium rounded-full">
                Selected
              </span>
            </div>
            <p class="text-sm text-gray-500 mt-1">
              Your campaign will start sending immediately after you confirm.
              Emails will be queued and sent within a few minutes.
            </p>
          </div>
          <div
            :class="[
              'w-6 h-6 rounded-full border-2 flex items-center justify-center',
              localSendOption === 'now' ? 'border-indigo-500 bg-indigo-500' : 'border-gray-300'
            ]"
          >
            <CheckCircle v-if="localSendOption === 'now'" class="w-4 h-4 text-white" />
          </div>
        </div>
      </div>

      <!-- Schedule Option -->
      <div
        @click="localSendOption = 'schedule'"
        :class="[
          'relative p-5 border-2 rounded-xl cursor-pointer transition-all',
          localSendOption === 'schedule'
            ? 'border-indigo-500 bg-indigo-50/50 ring-2 ring-indigo-500/20'
            : 'border-gray-200 hover:border-gray-300 bg-white'
        ]"
      >
        <div class="flex items-start gap-4">
          <div
            :class="[
              'w-12 h-12 rounded-xl flex items-center justify-center flex-shrink-0',
              localSendOption === 'schedule' ? 'bg-indigo-200' : 'bg-gray-100'
            ]"
          >
            <CalendarDays :class="['w-6 h-6', localSendOption === 'schedule' ? 'text-indigo-700' : 'text-gray-500']" />
          </div>
          <div class="flex-1">
            <div class="flex items-center gap-2">
              <h4 class="font-semibold text-gray-900">Schedule for Later</h4>
              <span v-if="localSendOption === 'schedule'" class="px-2 py-0.5 bg-indigo-100 text-indigo-700 text-xs font-medium rounded-full">
                Selected
              </span>
            </div>
            <p class="text-sm text-gray-500 mt-1">
              Pick a specific date and time to send your campaign.
              Perfect for timing your emails for maximum engagement.
            </p>
          </div>
          <div
            :class="[
              'w-6 h-6 rounded-full border-2 flex items-center justify-center',
              localSendOption === 'schedule' ? 'border-indigo-500 bg-indigo-500' : 'border-gray-300'
            ]"
          >
            <CheckCircle v-if="localSendOption === 'schedule'" class="w-4 h-4 text-white" />
          </div>
        </div>

        <!-- Date/Time Picker -->
        <div v-if="localSendOption === 'schedule'" class="mt-5 pt-5 border-t border-indigo-200" @click.stop>
          <label class="block text-sm font-medium text-gray-700 mb-3">
            <Clock class="w-4 h-4 inline mr-1" />
            Select Date & Time
          </label>

          <VueDatePicker
            v-model="localScheduledAt"
            :min-date="minDate"
            :enable-time-picker="true"
            :is-24="false"
            :auto-apply="true"
            placeholder="Pick a date and time"
            class="schedule-picker"
          />

          <!-- Suggested Times -->
          <div class="mt-4">
            <p class="text-xs text-gray-500 mb-2">Quick options:</p>
            <div class="flex flex-wrap gap-2">
              <button
                v-for="time in suggestedTimes"
                :key="time.label"
                @click="applySuggestedTime(time.getDate)"
                class="px-3 py-1.5 text-xs bg-white border border-gray-200 hover:border-indigo-300 hover:bg-indigo-50 rounded-lg text-gray-600 transition-colors"
              >
                {{ time.label }}
              </button>
            </div>
          </div>

          <!-- Selected Time Display -->
          <div
            v-if="localScheduledAt"
            class="mt-4 p-3 bg-white rounded-lg border border-indigo-200"
          >
            <div class="flex items-center gap-2">
              <CheckCircle class="w-5 h-5 text-green-500" />
              <div>
                <p class="text-sm font-medium text-gray-900">Scheduled for:</p>
                <p class="text-sm text-indigo-600">{{ formatScheduledTime(localScheduledAt) }}</p>
              </div>
            </div>
          </div>

          <!-- Validation Error -->
          <div
            v-if="localSendOption === 'schedule' && !localScheduledAt"
            class="mt-3 flex items-center gap-2 text-amber-600 text-sm"
          >
            <AlertCircle class="w-4 h-4" />
            Please select a date and time
          </div>
        </div>
      </div>
    </div>

    <!-- Tips -->
    <div class="bg-blue-50 rounded-xl p-4 border border-blue-100">
      <div class="flex items-start gap-3">
        <Info class="w-5 h-5 text-blue-600 flex-shrink-0 mt-0.5" />
        <div>
          <h4 class="font-medium text-blue-900">Best times to send emails</h4>
          <ul class="mt-2 text-sm text-blue-800 space-y-1">
            <li>• <strong>Tuesday-Thursday</strong> tend to have highest open rates</li>
            <li>• <strong>9-11 AM</strong> is ideal for business emails</li>
            <li>• <strong>8-9 PM</strong> works well for consumer emails</li>
            <li>• Avoid weekends and holidays for B2B campaigns</li>
          </ul>
        </div>
      </div>
    </div>

    <!-- Confirmation Message -->
    <div
      v-if="isValid"
      class="p-4 bg-green-50 rounded-xl border border-green-200"
    >
      <div class="flex items-center gap-3">
        <CheckCircle class="w-6 h-6 text-green-600" />
        <div>
          <p class="font-medium text-green-800">
            {{ localSendOption === 'now' ? 'Ready to send immediately!' : 'Schedule is set!' }}
          </p>
          <p class="text-sm text-green-700">
            {{ localSendOption === 'now'
              ? 'Click "Send Now" to launch your campaign.'
              : `Your campaign will be sent on ${formatScheduledTime(localScheduledAt)}`
            }}
          </p>
        </div>
      </div>
    </div>
  </div>
</template>

<style>
/* VueDatePicker customization */
.schedule-picker .dp__input {
  @apply border-gray-300 rounded-lg py-3 px-4 focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20;
}

.dp__theme_light {
  --dp-primary-color: #6366f1;
  --dp-primary-text-color: #ffffff;
  --dp-border-radius: 12px;
}
</style>
