<script setup lang="ts">
import { computed } from 'vue'
import { Star, Paperclip } from 'lucide-vue-next'
import { format, isToday, isThisYear } from 'date-fns'
import type { Email } from '@/lib/api'

interface Props {
  email: Email
  selected: boolean
}

const props = defineProps<Props>()

const emit = defineEmits<{
  select: []
  click: []
  star: []
}>()

const formattedDate = computed(() => {
  const date = new Date(props.email.receivedAt)
  if (isToday(date)) {
    return format(date, 'h:mm a')
  }
  if (isThisYear(date)) {
    return format(date, 'MMM d')
  }
  return format(date, 'MMM d, yyyy')
})

const senderDisplay = computed(() => {
  return props.email.from.name || props.email.from.email
})
</script>

<template>
  <li
    :class="[
      'email-row',
      email.isRead ? 'read' : 'unread',
      { 'selected': selected }
    ]"
    @click="emit('click')"
  >
    <!-- Checkbox -->
    <div class="px-2" @click.stop="emit('select')">
      <input
        type="checkbox"
        :checked="selected"
        class="gmail-checkbox"
        @change="emit('select')"
      />
    </div>

    <!-- Star -->
    <button
      @click.stop="emit('star')"
      class="p-1 hover:bg-gmail-hover rounded"
    >
      <Star
        :class="[
          'w-5 h-5',
          email.isStarred ? 'text-yellow-500 fill-yellow-500' : 'text-gmail-gray'
        ]"
      />
    </button>

    <!-- Sender -->
    <div class="w-48 truncate px-2 text-sm">
      {{ senderDisplay }}
    </div>

    <!-- Subject and snippet -->
    <div class="flex-1 flex items-center gap-2 truncate px-2">
      <span :class="['text-sm', { 'font-semibold': !email.isRead }]">
        {{ email.subject || '(no subject)' }}
      </span>
      <span class="text-sm text-gmail-gray truncate">
        - {{ email.snippet }}
      </span>
    </div>

    <!-- Attachment indicator -->
    <Paperclip
      v-if="email.hasAttachments"
      class="w-4 h-4 text-gmail-gray shrink-0"
    />

    <!-- Date -->
    <div class="w-20 text-right text-sm text-gmail-gray px-2">
      {{ formattedDate }}
    </div>
  </li>
</template>
