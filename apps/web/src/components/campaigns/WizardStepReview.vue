<script setup lang="ts">
import { ref, computed } from 'vue'
import DOMPurify from 'dompurify'
import {
  Eye,
  Mail,
  User,
  Users,
  FileText,
  Send,
  CheckCircle,
  AlertCircle,
  Monitor,
  Smartphone,
  Loader2
} from 'lucide-vue-next'
import type { Identity, ContactList } from '@/lib/api'

const props = defineProps<{
  name: string
  subject: string
  fromIdentityId: number | null
  replyTo: string
  selectedListUuid: string
  htmlContent: string
  textContent: string
  identities: Identity[]
  lists: ContactList[]
  onSendTest?: (email: string) => Promise<void>
}>()

const emit = defineEmits<{
  'send-test': [email: string]
}>()

const previewMode = ref<'desktop' | 'mobile'>('desktop')
const testEmail = ref('')
const sendingTest = ref(false)
const testSent = ref(false)
const testError = ref<string | null>(null)

const selectedIdentity = computed(() => {
  return props.identities.find(i => Number(i.id) === props.fromIdentityId)
})

const selectedList = computed(() => {
  return props.lists.find(l => l.uuid === props.selectedListUuid)
})

const totalRecipients = computed(() => {
  return selectedList.value?.contactCount || 0
})

const validationIssues = computed(() => {
  const issues: string[] = []
  if (!props.name || props.name.length < 3) {
    issues.push('Campaign name is too short')
  }
  if (!props.subject || props.subject.length < 5) {
    issues.push('Subject line is too short')
  }
  if (!props.fromIdentityId) {
    issues.push('No sender identity selected')
  }
  if (!props.selectedListUuid) {
    issues.push('No recipient list selected')
  }
  if (!props.textContent || props.textContent.trim().length < 10) {
    issues.push('Email content is too short')
  }
  return issues
})

const isValid = computed(() => validationIssues.value.length === 0)

// Sanitize campaign HTML content to prevent XSS attacks
const sanitizedHtmlContent = computed(() => {
  if (!props.htmlContent) return '<p class="text-gray-400">No content yet</p>'
  return DOMPurify.sanitize(props.htmlContent, {
    ALLOWED_TAGS: ['p', 'br', 'b', 'i', 'u', 'a', 'strong', 'em', 'ul', 'ol', 'li',
                   'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'blockquote', 'pre', 'code',
                   'img', 'table', 'tr', 'td', 'th', 'thead', 'tbody', 'div', 'span',
                   'hr', 'sup', 'sub', 'small', 'font', 'center', 'style'],
    ALLOWED_ATTR: ['href', 'src', 'alt', 'style', 'class', 'target', 'width', 'height',
                   'border', 'cellpadding', 'cellspacing', 'align', 'valign', 'bgcolor',
                   'color', 'size', 'face'],
    ALLOW_DATA_ATTR: false
  })
})

const sendTestEmail = async () => {
  if (!testEmail.value) return

  sendingTest.value = true
  testError.value = null
  testSent.value = false

  try {
    if (props.onSendTest) {
      await props.onSendTest(testEmail.value)
    } else {
      emit('send-test', testEmail.value)
    }
    testSent.value = true
    setTimeout(() => {
      testSent.value = false
    }, 3000)
  } catch (e) {
    testError.value = e instanceof Error ? e.message : 'Failed to send test email'
  } finally {
    sendingTest.value = false
  }
}
</script>

<template>
  <div class="space-y-6">
    <div class="text-center mb-8">
      <div class="w-16 h-16 bg-amber-100 rounded-full flex items-center justify-center mx-auto mb-4">
        <Eye class="w-8 h-8 text-amber-600" />
      </div>
      <h3 class="text-xl font-semibold text-gray-900">Review Your Campaign</h3>
      <p class="text-gray-500 mt-1">Double-check everything before sending</p>
    </div>

    <!-- Validation Status -->
    <div
      :class="[
        'p-4 rounded-xl border-2',
        isValid
          ? 'bg-green-50 border-green-200'
          : 'bg-red-50 border-red-200'
      ]"
    >
      <div class="flex items-start gap-3">
        <div
          :class="[
            'w-10 h-10 rounded-full flex items-center justify-center flex-shrink-0',
            isValid ? 'bg-green-200' : 'bg-red-200'
          ]"
        >
          <CheckCircle v-if="isValid" class="w-5 h-5 text-green-700" />
          <AlertCircle v-else class="w-5 h-5 text-red-700" />
        </div>
        <div>
          <h4 :class="['font-semibold', isValid ? 'text-green-800' : 'text-red-800']">
            {{ isValid ? 'Campaign Ready!' : 'Issues Found' }}
          </h4>
          <p v-if="isValid" class="text-green-700 text-sm">
            Everything looks good. You can proceed to scheduling.
          </p>
          <ul v-else class="mt-2 space-y-1">
            <li v-for="issue in validationIssues" :key="issue" class="text-sm text-red-700 flex items-center gap-2">
              <span class="w-1.5 h-1.5 rounded-full bg-red-500"></span>
              {{ issue }}
            </li>
          </ul>
        </div>
      </div>
    </div>

    <!-- Campaign Summary Cards -->
    <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <!-- Campaign Details -->
      <div class="bg-white rounded-xl border border-gray-200 p-5">
        <div class="flex items-center gap-2 mb-4">
          <Mail class="w-5 h-5 text-blue-600" />
          <h4 class="font-semibold text-gray-900">Campaign Details</h4>
        </div>
        <dl class="space-y-3 text-sm">
          <div>
            <dt class="text-gray-500">Name</dt>
            <dd class="font-medium text-gray-900">{{ name || '(Not set)' }}</dd>
          </div>
          <div>
            <dt class="text-gray-500">Subject</dt>
            <dd class="font-medium text-gray-900">{{ subject || '(Not set)' }}</dd>
          </div>
        </dl>
      </div>

      <!-- Sender Info -->
      <div class="bg-white rounded-xl border border-gray-200 p-5">
        <div class="flex items-center gap-2 mb-4">
          <User class="w-5 h-5 text-purple-600" />
          <h4 class="font-semibold text-gray-900">Sender</h4>
        </div>
        <dl class="space-y-3 text-sm">
          <div>
            <dt class="text-gray-500">From</dt>
            <dd class="font-medium text-gray-900">
              <template v-if="selectedIdentity">
                {{ selectedIdentity.displayName }} &lt;{{ selectedIdentity.email }}&gt;
              </template>
              <span v-else class="text-gray-400">(Not selected)</span>
            </dd>
          </div>
          <div v-if="replyTo">
            <dt class="text-gray-500">Reply-To</dt>
            <dd class="font-medium text-gray-900">{{ replyTo }}</dd>
          </div>
        </dl>
      </div>

      <!-- Recipients -->
      <div class="bg-white rounded-xl border border-gray-200 p-5">
        <div class="flex items-center gap-2 mb-4">
          <Users class="w-5 h-5 text-green-600" />
          <h4 class="font-semibold text-gray-900">Recipients</h4>
        </div>
        <div class="space-y-2">
          <div class="text-2xl font-bold text-gray-900">
            {{ totalRecipients.toLocaleString() }}
            <span class="text-sm font-normal text-gray-500">contacts</span>
          </div>
          <div v-if="selectedList" class="text-sm text-gray-500">
            From list:
          </div>
          <div class="flex flex-wrap gap-1 mt-2">
            <span
              v-if="selectedList"
              class="px-2 py-1 bg-gray-100 rounded text-xs text-gray-700"
            >
              {{ selectedList.name }} ({{ selectedList.contactCount }})
            </span>
            <span v-else class="text-gray-400 text-sm">
              No list selected
            </span>
          </div>
        </div>
      </div>

      <!-- Content Stats -->
      <div class="bg-white rounded-xl border border-gray-200 p-5">
        <div class="flex items-center gap-2 mb-4">
          <FileText class="w-5 h-5 text-amber-600" />
          <h4 class="font-semibold text-gray-900">Content</h4>
        </div>
        <dl class="space-y-3 text-sm">
          <div>
            <dt class="text-gray-500">Characters</dt>
            <dd class="font-medium text-gray-900">{{ textContent.length.toLocaleString() }}</dd>
          </div>
          <div>
            <dt class="text-gray-500">Words</dt>
            <dd class="font-medium text-gray-900">
              {{ textContent.trim() ? textContent.trim().split(/\s+/).length.toLocaleString() : 0 }}
            </dd>
          </div>
        </dl>
      </div>
    </div>

    <!-- Email Preview -->
    <div class="bg-white rounded-xl border border-gray-200 overflow-hidden">
      <div class="flex items-center justify-between px-5 py-3 border-b border-gray-200 bg-gray-50">
        <h4 class="font-semibold text-gray-900">Email Preview</h4>
        <div class="flex items-center gap-1 bg-gray-200 rounded-lg p-1">
          <button
            @click="previewMode = 'desktop'"
            :class="[
              'p-1.5 rounded transition-all',
              previewMode === 'desktop' ? 'bg-white shadow text-gray-900' : 'text-gray-500 hover:text-gray-700'
            ]"
          >
            <Monitor class="w-4 h-4" />
          </button>
          <button
            @click="previewMode = 'mobile'"
            :class="[
              'p-1.5 rounded transition-all',
              previewMode === 'mobile' ? 'bg-white shadow text-gray-900' : 'text-gray-500 hover:text-gray-700'
            ]"
          >
            <Smartphone class="w-4 h-4" />
          </button>
        </div>
      </div>

      <div class="p-5 bg-gray-100 flex justify-center">
        <div
          :class="[
            'bg-white rounded-lg shadow-lg overflow-hidden transition-all duration-300',
            previewMode === 'desktop' ? 'w-full max-w-2xl' : 'w-80'
          ]"
        >
          <!-- Email Header -->
          <div class="px-4 py-3 border-b border-gray-100">
            <div class="text-sm">
              <div class="flex items-center gap-2 text-gray-500">
                <span class="font-medium text-gray-700">From:</span>
                {{ selectedIdentity?.displayName || 'Sender' }} &lt;{{ selectedIdentity?.email || 'email@example.com' }}&gt;
              </div>
              <div class="flex items-center gap-2 text-gray-500 mt-1">
                <span class="font-medium text-gray-700">Subject:</span>
                {{ subject || '(No subject)' }}
              </div>
            </div>
          </div>
          <!-- Email Body -->
          <div
            class="p-4 prose prose-sm max-w-none overflow-auto"
            :class="previewMode === 'mobile' ? 'max-h-80' : 'max-h-96'"
            v-html="sanitizedHtmlContent"
          />
        </div>
      </div>
    </div>

    <!-- Send Test Email -->
    <div class="bg-gradient-to-r from-blue-50 to-indigo-50 rounded-xl border border-blue-200 p-5">
      <div class="flex items-center gap-2 mb-3">
        <Send class="w-5 h-5 text-blue-600" />
        <h4 class="font-semibold text-gray-900">Send Test Email</h4>
      </div>
      <p class="text-sm text-gray-600 mb-4">
        Send a test email to yourself to see how it looks in your inbox.
      </p>
      <div class="flex gap-3">
        <input
          v-model="testEmail"
          type="email"
          placeholder="your@email.com"
          class="flex-1 px-4 py-2.5 border border-gray-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500/20 focus:border-blue-500 transition-all"
        />
        <button
          @click="sendTestEmail"
          :disabled="!testEmail || sendingTest || !isValid"
          class="px-6 py-2.5 bg-blue-600 text-white rounded-lg font-medium hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-all flex items-center gap-2"
        >
          <Loader2 v-if="sendingTest" class="w-4 h-4 animate-spin" />
          <Send v-else class="w-4 h-4" />
          Send Test
        </button>
      </div>

      <!-- Test Status -->
      <div v-if="testSent" class="mt-3 flex items-center gap-2 text-green-600 text-sm">
        <CheckCircle class="w-4 h-4" />
        Test email sent! Check your inbox.
      </div>
      <div v-if="testError" class="mt-3 flex items-center gap-2 text-red-600 text-sm">
        <AlertCircle class="w-4 h-4" />
        {{ testError }}
      </div>
    </div>
  </div>
</template>
