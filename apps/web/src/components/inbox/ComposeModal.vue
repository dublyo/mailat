<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { format } from 'date-fns'
import {
  X, Minus, Maximize2, Minimize2, Bold, Italic, Underline,
  Link2, Image, Paperclip, MoreHorizontal, Trash2, Send
} from 'lucide-vue-next'
import { useInboxStore } from '@/stores/inbox'
import { useDomainsStore } from '@/stores/domains'
import { composeApi } from '@/lib/api'
import Spinner from '@/components/common/Spinner.vue'

const inboxStore = useInboxStore()
const domainsStore = useDomainsStore()

const isMinimized = ref(false)
const isFullscreen = ref(false)
const isSending = ref(false)
const error = ref('')

const to = ref('')
const cc = ref('')
const bcc = ref('')
const subject = ref('')
const body = ref('')
const showCcBcc = ref(false)
const selectedIdentityId = ref('')

const isOpen = computed(() => inboxStore.isComposeOpen)
const mode = computed(() => inboxStore.composeMode)
const replyToEmail = computed(() => inboxStore.replyToEmail)

// Find matching identity based on recipient email addresses
const findMatchingIdentity = (toEmails: string[], ccEmails?: string[]) => {
  const allRecipientEmails = [...toEmails, ...(ccEmails || [])].map(e => e.toLowerCase())

  // Check if any recipient email matches our identities
  for (const recipientEmail of allRecipientEmails) {
    const matchingIdentity = domainsStore.identities.find(
      i => i.email.toLowerCase() === recipientEmail
    )
    if (matchingIdentity) {
      return matchingIdentity
    }
  }

  // No match found, return default identity
  return domainsStore.identities.find(i => i.isDefault) || domainsStore.identities[0]
}

// Initialize form based on mode
watch(isOpen, async (open) => {
  if (!open) {
    resetForm()
    return
  }

  // Fetch identities if not loaded
  if (domainsStore.identities.length === 0) {
    await domainsStore.fetchIdentities()
  }

  if (replyToEmail.value && (mode.value === 'reply' || mode.value === 'replyAll')) {
    // First try to use identityId directly (for received emails, especially catch-all)
    if (replyToEmail.value.identityId) {
      const identityById = domainsStore.identities.find(
        i => Number(i.id) === replyToEmail.value!.identityId
      )
      if (identityById) {
        selectedIdentityId.value = String(identityById.id)
      }
    } else {
      // Fallback: Auto-select identity based on who the email was sent to
      const toEmails = replyToEmail.value.to.map(t => t.email)
      const ccEmails = replyToEmail.value.cc?.map(c => c.email)
      const matchingIdentity = findMatchingIdentity(toEmails, ccEmails)
      if (matchingIdentity) {
        selectedIdentityId.value = String(matchingIdentity.id)
      }
    }
  } else if (domainsStore.identities.length > 0) {
    // Default identity for new compose or forward
    const defaultIdentity = domainsStore.identities.find(i => i.isDefault) || domainsStore.identities[0]
    selectedIdentityId.value = String(defaultIdentity.id)
  }

  if (replyToEmail.value) {
    if (mode.value === 'reply') {
      to.value = replyToEmail.value.from.email
      subject.value = `Re: ${replyToEmail.value.subject}`
      body.value = buildQuotedBody()
    } else if (mode.value === 'replyAll') {
      const allRecipients = [
        replyToEmail.value.from.email,
        ...replyToEmail.value.to.map(t => t.email),
        ...(replyToEmail.value.cc?.map(c => c.email) || [])
      ].filter((email, index, self) => self.indexOf(email) === index)
      to.value = allRecipients.join(', ')
      subject.value = `Re: ${replyToEmail.value.subject}`
      body.value = buildQuotedBody()
    } else if (mode.value === 'forward') {
      subject.value = `Fwd: ${replyToEmail.value.subject}`
      body.value = buildForwardedBody()
    }
  }
})

const buildQuotedBody = () => {
  if (!replyToEmail.value) return ''
  const date = format(new Date(replyToEmail.value.receivedAt), 'EEE, MMM d, yyyy \'at\' h:mm a')
  const from = replyToEmail.value.from.name || replyToEmail.value.from.email
  return `\n\n\nOn ${date}, ${from} wrote:\n> ${replyToEmail.value.body.split('\n').join('\n> ')}`
}

const buildForwardedBody = () => {
  if (!replyToEmail.value) return ''
  const date = format(new Date(replyToEmail.value.receivedAt), 'EEE, MMM d, yyyy \'at\' h:mm a')
  const from = `${replyToEmail.value.from.name || ''} <${replyToEmail.value.from.email}>`
  const toList = replyToEmail.value.to.map(t => `${t.name || ''} <${t.email}>`).join(', ')
  return `\n\n\n---------- Forwarded message ---------\nFrom: ${from}\nDate: ${date}\nSubject: ${replyToEmail.value.subject}\nTo: ${toList}\n\n${replyToEmail.value.body}`
}

const resetForm = () => {
  to.value = ''
  cc.value = ''
  bcc.value = ''
  subject.value = ''
  body.value = ''
  showCcBcc.value = false
  isMinimized.value = false
  error.value = ''
}

const handleSend = async () => {
  if (!to.value.trim() || !selectedIdentityId.value) {
    error.value = 'Please fill in the recipient and select an identity'
    return
  }

  isSending.value = true
  error.value = ''

  try {
    const recipients = to.value.split(',').map(e => e.trim()).filter(Boolean)
    const ccRecipients = cc.value.split(',').map(e => e.trim()).filter(Boolean)
    const bccRecipients = bcc.value.split(',').map(e => e.trim()).filter(Boolean)

    await composeApi.send({
      identityId: selectedIdentityId.value,
      to: recipients,
      cc: ccRecipients.length > 0 ? ccRecipients : undefined,
      bcc: bccRecipients.length > 0 ? bccRecipients : undefined,
      subject: subject.value,
      body: body.value,
      // Use the actual Message-ID for proper email threading
      replyTo: replyToEmail.value?.messageId
    })

    inboxStore.closeCompose()
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to send email'
  } finally {
    isSending.value = false
  }
}

const handleDiscard = () => {
  if (body.value.trim() || subject.value.trim() || to.value.trim()) {
    if (confirm('Are you sure you want to discard this message?')) {
      inboxStore.closeCompose()
    }
  } else {
    inboxStore.closeCompose()
  }
}

const modalClasses = computed(() => {
  if (isFullscreen.value) return 'fixed inset-4 z-50'
  if (isMinimized.value) return 'fixed bottom-0 right-20 w-72 z-50'
  return 'fixed bottom-0 right-20 w-[560px] z-50'
})

const modalTitle = computed(() => {
  switch (mode.value) {
    case 'reply':
    case 'replyAll':
      return 'Reply'
    case 'forward':
      return 'Forward'
    default:
      return 'New Message'
  }
})
</script>

<template>
  <Teleport to="body">
    <div v-if="isOpen" :class="modalClasses">
      <div
        :class="[
          'bg-white rounded-t-lg shadow-xl flex flex-col',
          isMinimized ? 'h-12' : isFullscreen ? 'h-full' : 'h-[500px]'
        ]"
      >
        <!-- Header -->
        <div
          class="flex items-center justify-between px-3 py-2 bg-gmail-gray rounded-t-lg cursor-pointer"
          @click="isMinimized && (isMinimized = false)"
        >
          <span class="text-sm font-medium text-white truncate">
            {{ modalTitle }}
          </span>
          <div class="flex items-center gap-1">
            <button
              @click.stop="isMinimized = !isMinimized"
              class="p-1 hover:bg-gray-600 rounded"
            >
              <Minus class="w-4 h-4 text-white" />
            </button>
            <button
              @click.stop="isFullscreen = !isFullscreen; isMinimized = false"
              class="p-1 hover:bg-gray-600 rounded"
            >
              <component
                :is="isFullscreen ? Minimize2 : Maximize2"
                class="w-4 h-4 text-white"
              />
            </button>
            <button
              @click.stop="handleDiscard"
              class="p-1 hover:bg-gray-600 rounded"
            >
              <X class="w-4 h-4 text-white" />
            </button>
          </div>
        </div>

        <template v-if="!isMinimized">
          <!-- Error -->
          <div v-if="error" class="px-3 py-2 bg-red-50 text-red-700 text-sm">
            {{ error }}
          </div>

          <!-- Identity selector -->
          <div class="px-3 py-2 border-b border-gmail-border">
            <div class="flex items-center gap-2">
              <span class="text-sm text-gmail-gray w-12">From</span>
              <div class="flex items-center gap-2 flex-1">
                <span
                  v-if="domainsStore.identities.find(i => String(i.id) === selectedIdentityId)?.color"
                  class="w-3 h-3 rounded-full flex-shrink-0"
                  :style="{ backgroundColor: domainsStore.identities.find(i => String(i.id) === selectedIdentityId)?.color }"
                ></span>
                <select
                  v-model="selectedIdentityId"
                  class="flex-1 outline-none text-sm bg-transparent"
                >
                  <option
                    v-for="identity in domainsStore.identities"
                    :key="identity.id"
                    :value="identity.id"
                  >
                    {{ identity.displayName }} &lt;{{ identity.email }}&gt;
                  </option>
                </select>
              </div>
            </div>
          </div>

          <!-- Recipients -->
          <div class="px-3 py-2 border-b border-gmail-border">
            <div class="flex items-center gap-2">
              <span class="text-sm text-gmail-gray w-12">To</span>
              <input
                v-model="to"
                type="text"
                class="flex-1 outline-none text-sm"
                placeholder="Recipients"
              />
              <button
                @click="showCcBcc = !showCcBcc"
                class="text-sm text-gmail-gray hover:text-gmail-blue"
              >
                Cc/Bcc
              </button>
            </div>

            <template v-if="showCcBcc">
              <div class="flex items-center gap-2 mt-2">
                <span class="text-sm text-gmail-gray w-12">Cc</span>
                <input
                  v-model="cc"
                  type="text"
                  class="flex-1 outline-none text-sm"
                />
              </div>
              <div class="flex items-center gap-2 mt-2">
                <span class="text-sm text-gmail-gray w-12">Bcc</span>
                <input
                  v-model="bcc"
                  type="text"
                  class="flex-1 outline-none text-sm"
                />
              </div>
            </template>
          </div>

          <!-- Subject -->
          <div class="px-3 py-2 border-b border-gmail-border">
            <input
              v-model="subject"
              type="text"
              placeholder="Subject"
              class="w-full outline-none text-sm"
            />
          </div>

          <!-- Body -->
          <div class="flex-1 p-3 overflow-y-auto">
            <textarea
              v-model="body"
              class="w-full h-full outline-none resize-none text-sm"
              placeholder="Compose email"
            />
          </div>

          <!-- Toolbar -->
          <div class="flex items-center justify-between px-3 py-2 border-t border-gmail-border">
            <div class="flex items-center gap-1">
              <button
                @click="handleSend"
                :disabled="isSending || !to.trim()"
                class="flex items-center gap-2 px-6 py-2 bg-gmail-blue text-white rounded-full hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <Spinner v-if="isSending" size="sm" class="border-white border-t-transparent" />
                <Send v-else class="w-4 h-4" />
                <span>Send</span>
              </button>

              <div class="flex items-center gap-0.5 ml-2">
                <button class="p-2 hover:bg-gmail-hover rounded" title="Bold">
                  <Bold class="w-4 h-4 text-gmail-gray" />
                </button>
                <button class="p-2 hover:bg-gmail-hover rounded" title="Italic">
                  <Italic class="w-4 h-4 text-gmail-gray" />
                </button>
                <button class="p-2 hover:bg-gmail-hover rounded" title="Underline">
                  <Underline class="w-4 h-4 text-gmail-gray" />
                </button>
                <button class="p-2 hover:bg-gmail-hover rounded" title="Insert link">
                  <Link2 class="w-4 h-4 text-gmail-gray" />
                </button>
                <button class="p-2 hover:bg-gmail-hover rounded" title="Insert image">
                  <Image class="w-4 h-4 text-gmail-gray" />
                </button>
                <button class="p-2 hover:bg-gmail-hover rounded" title="Attach files">
                  <Paperclip class="w-4 h-4 text-gmail-gray" />
                </button>
                <button class="p-2 hover:bg-gmail-hover rounded" title="More options">
                  <MoreHorizontal class="w-4 h-4 text-gmail-gray" />
                </button>
              </div>
            </div>

            <button
              @click="handleDiscard"
              class="p-2 hover:bg-gmail-hover rounded"
              title="Discard draft"
            >
              <Trash2 class="w-4 h-4 text-gmail-gray" />
            </button>
          </div>
        </template>
      </div>
    </div>
  </Teleport>
</template>
