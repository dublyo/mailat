<script setup lang="ts">
import { computed } from 'vue'
import { format } from 'date-fns'
import DOMPurify from 'dompurify'
import {
  ArrowLeft, Archive, Trash2, Star, Reply, ReplyAll, Forward,
  MoreVertical, Printer, ExternalLink, Download, Paperclip
} from 'lucide-vue-next'
import { useInboxStore } from '@/stores/inbox'
import Avatar from '@/components/common/Avatar.vue'
import Button from '@/components/common/Button.vue'

const inboxStore = useInboxStore()

const email = computed(() => inboxStore.activeEmail)

const sanitizedHtml = computed(() => {
  if (!email.value?.htmlBody) return ''
  return DOMPurify.sanitize(email.value.htmlBody, {
    ALLOWED_TAGS: ['p', 'br', 'b', 'i', 'u', 'a', 'strong', 'em', 'ul', 'ol', 'li', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'blockquote', 'pre', 'code', 'img', 'table', 'tr', 'td', 'th', 'thead', 'tbody', 'div', 'span'],
    ALLOWED_ATTR: ['href', 'src', 'alt', 'style', 'class', 'target']
  })
})

const formattedDate = computed(() => {
  if (!email.value) return ''
  return format(new Date(email.value.receivedAt), 'MMM d, yyyy, h:mm a')
})

const formatFileSize = (bytes: number) => {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

const goBack = () => {
  inboxStore.setActiveEmail(null)
}

const handleArchive = async () => {
  if (!email.value) return
  await inboxStore.moveToFolder([email.value.uuid], 'archive')
}

const handleDelete = async () => {
  if (!email.value) return
  await inboxStore.deleteEmails([email.value.uuid])
}

const handleToggleStar = async () => {
  if (!email.value) return
  await inboxStore.toggleStar(email.value.uuid)
}

const handleReply = () => {
  if (!email.value) return
  inboxStore.openCompose('reply', email.value)
}

const handleReplyAll = () => {
  if (!email.value) return
  inboxStore.openCompose('replyAll', email.value)
}

const handleForward = () => {
  if (!email.value) return
  inboxStore.openCompose('forward', email.value)
}
</script>

<template>
  <div v-if="!email" class="flex-1 flex items-center justify-center bg-gmail-lightGray">
    <div class="text-center text-gmail-gray">
      <Inbox class="w-16 h-16 mx-auto mb-4 opacity-50" />
      <p class="text-lg">Select an email to read</p>
    </div>
  </div>

  <div v-else class="flex-1 flex flex-col">
    <!-- Toolbar -->
    <div class="flex items-center gap-2 px-4 py-2 border-b border-gmail-border">
      <button
        @click="goBack"
        class="p-2 hover:bg-gmail-hover rounded-full"
        title="Back to inbox"
      >
        <ArrowLeft class="w-5 h-5 text-gmail-gray" />
      </button>

      <button
        @click="handleArchive"
        class="p-2 hover:bg-gmail-hover rounded-full"
        title="Archive"
      >
        <Archive class="w-5 h-5 text-gmail-gray" />
      </button>

      <button
        @click="handleDelete"
        class="p-2 hover:bg-gmail-hover rounded-full"
        title="Delete"
      >
        <Trash2 class="w-5 h-5 text-gmail-gray" />
      </button>

      <div class="w-px h-6 bg-gmail-border mx-2" />

      <button
        @click="handleToggleStar"
        class="p-2 hover:bg-gmail-hover rounded-full"
        :title="email.isStarred ? 'Unstar' : 'Star'"
      >
        <Star
          :class="[
            'w-5 h-5',
            email.isStarred ? 'text-yellow-500 fill-yellow-500' : 'text-gmail-gray'
          ]"
        />
      </button>

      <div class="flex-1" />

      <button class="p-2 hover:bg-gmail-hover rounded-full" title="Print">
        <Printer class="w-5 h-5 text-gmail-gray" />
      </button>
      <button class="p-2 hover:bg-gmail-hover rounded-full" title="Open in new window">
        <ExternalLink class="w-5 h-5 text-gmail-gray" />
      </button>
      <button class="p-2 hover:bg-gmail-hover rounded-full" title="More">
        <MoreVertical class="w-5 h-5 text-gmail-gray" />
      </button>
    </div>

    <!-- Email content -->
    <div class="flex-1 overflow-y-auto p-6">
      <!-- Subject -->
      <h1 class="text-2xl font-normal text-gray-900 mb-6">
        {{ email.subject || '(no subject)' }}
      </h1>

      <!-- Sender info -->
      <div class="flex items-start gap-4 mb-6">
        <Avatar :name="email.from.name" :email="email.from.email" size="lg" />

        <div class="flex-1 min-w-0">
          <div class="flex items-center gap-2 flex-wrap">
            <span class="font-medium">
              {{ email.from.name || email.from.email }}
            </span>
            <span class="text-gmail-gray text-sm">
              &lt;{{ email.from.email }}&gt;
            </span>
          </div>

          <div class="text-sm text-gmail-gray">
            to
            <template v-for="(recipient, index) in email.to" :key="recipient.email">
              {{ recipient.name || recipient.email }}<template v-if="index < email.to.length - 1">, </template>
            </template>
            <template v-if="email.cc && email.cc.length > 0">
              , cc:
              <template v-for="(recipient, index) in email.cc" :key="recipient.email">
                {{ recipient.name || recipient.email }}<template v-if="index < (email.cc?.length || 0) - 1">, </template>
              </template>
            </template>
          </div>
        </div>

        <div class="text-sm text-gmail-gray shrink-0">
          {{ formattedDate }}
        </div>
      </div>

      <!-- Email body -->
      <div class="prose max-w-none mb-8">
        <div
          v-if="sanitizedHtml"
          v-html="sanitizedHtml"
          class="email-html-content"
        />
        <pre
          v-else
          class="whitespace-pre-wrap font-sans text-sm leading-relaxed"
        >{{ email.body }}</pre>
      </div>

      <!-- Attachments -->
      <div
        v-if="email.attachments && email.attachments.length > 0"
        class="border-t border-gmail-border pt-4 mt-6"
      >
        <div class="flex items-center gap-2 text-sm text-gmail-gray mb-3">
          <Paperclip class="w-4 h-4" />
          <span>{{ email.attachments.length }} attachment(s)</span>
        </div>
        <div class="flex flex-wrap gap-2">
          <a
            v-for="attachment in email.attachments"
            :key="attachment.id"
            :href="attachment.url"
            :download="attachment.filename"
            class="flex items-center gap-2 px-3 py-2 bg-gmail-lightGray rounded-lg hover:bg-gmail-hover transition-colors"
          >
            <Download class="w-4 h-4 text-gmail-gray" />
            <div>
              <div class="text-sm font-medium truncate max-w-[200px]">
                {{ attachment.filename }}
              </div>
              <div class="text-xs text-gmail-gray">
                {{ formatFileSize(attachment.size) }}
              </div>
            </div>
          </a>
        </div>
      </div>
    </div>

    <!-- Reply actions -->
    <div class="border-t border-gmail-border p-4 flex items-center gap-3">
      <Button variant="secondary" @click="handleReply">
        <Reply class="w-4 h-4" />
        Reply
      </Button>
      <Button variant="secondary" @click="handleReplyAll">
        <ReplyAll class="w-4 h-4" />
        Reply all
      </Button>
      <Button variant="secondary" @click="handleForward">
        <Forward class="w-4 h-4" />
        Forward
      </Button>
    </div>
  </div>
</template>

<script lang="ts">
import { Inbox } from 'lucide-vue-next'
export default {
  components: { Inbox }
}
</script>
