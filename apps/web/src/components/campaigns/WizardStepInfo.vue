<script setup lang="ts">
import { computed, watch } from 'vue'
import { Mail, User, Reply, Sparkles } from 'lucide-vue-next'
import type { Identity } from '@/lib/api'

const props = defineProps<{
  name: string
  subject: string
  fromIdentityId: number | null
  replyTo: string
  identities: Identity[]
}>()

const emit = defineEmits<{
  'update:name': [value: string]
  'update:subject': [value: string]
  'update:fromIdentityId': [value: number | null]
  'update:replyTo': [value: string]
  'update:valid': [value: boolean]
}>()

const subjectLength = computed(() => props.subject.length)
const subjectLengthColor = computed(() => {
  if (subjectLength.value <= 50) return 'text-green-600'
  if (subjectLength.value <= 100) return 'text-yellow-600'
  return 'text-red-600'
})

const isValid = computed(() => {
  return props.name.trim().length >= 3 &&
    props.subject.trim().length >= 5 &&
    props.fromIdentityId !== null
})

watch(isValid, (valid) => {
  emit('update:valid', valid)
}, { immediate: true })

// Subject line suggestions
const subjectSuggestions = [
  'Your weekly update is here!',
  'Don\'t miss out - Special offer inside',
  'News you\'ll want to read',
  'Quick update from our team'
]
</script>

<template>
  <div class="space-y-6 max-w-2xl">
    <div class="text-center mb-8">
      <div class="w-16 h-16 bg-blue-100 rounded-full flex items-center justify-center mx-auto mb-4">
        <Mail class="w-8 h-8 text-gmail-blue" />
      </div>
      <h3 class="text-xl font-semibold text-gray-900">Campaign Details</h3>
      <p class="text-gray-500 mt-1">Set up the basic information for your campaign</p>
    </div>

    <!-- Campaign Name -->
    <div>
      <label class="block text-sm font-medium text-gray-700 mb-2">
        Campaign Name <span class="text-red-500">*</span>
      </label>
      <input
        :value="name"
        @input="emit('update:name', ($event.target as HTMLInputElement).value)"
        type="text"
        placeholder="e.g., January Newsletter, Product Launch"
        class="w-full px-4 py-3 border border-gray-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-gmail-blue/20 focus:border-gmail-blue transition-all"
        :class="{ 'border-red-300': name.length > 0 && name.length < 3 }"
      />
      <p class="text-sm text-gray-500 mt-1.5">
        Internal name to identify this campaign (not visible to recipients)
      </p>
    </div>

    <!-- Subject Line -->
    <div>
      <label class="block text-sm font-medium text-gray-700 mb-2">
        Subject Line <span class="text-red-500">*</span>
      </label>
      <div class="relative">
        <input
          :value="subject"
          @input="emit('update:subject', ($event.target as HTMLInputElement).value)"
          type="text"
          placeholder="e.g., Your weekly update is here!"
          maxlength="150"
          class="w-full px-4 py-3 border border-gray-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-gmail-blue/20 focus:border-gmail-blue transition-all pr-16"
          :class="{ 'border-red-300': subject.length > 0 && subject.length < 5 }"
        />
        <span :class="['absolute right-3 top-1/2 -translate-y-1/2 text-sm', subjectLengthColor]">
          {{ subjectLength }}/150
        </span>
      </div>
      <div class="flex items-center justify-between mt-1.5">
        <p class="text-sm text-gray-500">
          Keep under 50 characters for best open rates
        </p>
        <div v-if="subjectLength <= 50" class="flex items-center gap-1 text-sm text-green-600">
          <Sparkles class="w-3.5 h-3.5" />
          <span>Great length!</span>
        </div>
      </div>

      <!-- Subject suggestions -->
      <div class="mt-3">
        <p class="text-xs text-gray-400 mb-2">Need inspiration?</p>
        <div class="flex flex-wrap gap-2">
          <button
            v-for="suggestion in subjectSuggestions"
            :key="suggestion"
            @click="emit('update:subject', suggestion)"
            class="px-3 py-1.5 text-xs bg-gray-100 hover:bg-gray-200 rounded-full text-gray-600 transition-colors"
          >
            {{ suggestion }}
          </button>
        </div>
      </div>
    </div>

    <!-- From Identity -->
    <div>
      <label class="block text-sm font-medium text-gray-700 mb-2">
        <div class="flex items-center gap-2">
          <User class="w-4 h-4" />
          From <span class="text-red-500">*</span>
        </div>
      </label>
      <select
        :value="fromIdentityId"
        @change="emit('update:fromIdentityId', Number(($event.target as HTMLSelectElement).value) || null)"
        class="w-full px-4 py-3 border border-gray-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-gmail-blue/20 focus:border-gmail-blue transition-all bg-white appearance-none cursor-pointer"
        :class="{ 'border-red-300 text-gray-400': !fromIdentityId }"
      >
        <option :value="null" disabled>Select sender identity</option>
        <option v-for="identity in identities" :key="identity.id" :value="Number(identity.id)">
          {{ identity.displayName }} &lt;{{ identity.email }}&gt;
        </option>
      </select>
      <p class="text-sm text-gray-500 mt-1.5">
        Recipients will see this as the sender
      </p>
    </div>

    <!-- Reply-To (Optional) -->
    <div>
      <label class="block text-sm font-medium text-gray-700 mb-2">
        <div class="flex items-center gap-2">
          <Reply class="w-4 h-4" />
          Reply-To <span class="text-gray-400 font-normal">(optional)</span>
        </div>
      </label>
      <input
        :value="replyTo"
        @input="emit('update:replyTo', ($event.target as HTMLInputElement).value)"
        type="email"
        placeholder="replies@example.com"
        class="w-full px-4 py-3 border border-gray-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-gmail-blue/20 focus:border-gmail-blue transition-all"
      />
      <p class="text-sm text-gray-500 mt-1.5">
        Where replies will be sent (leave empty to use the From address)
      </p>
    </div>
  </div>
</template>
