<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import DOMPurify from 'dompurify'
import {
  Archive, Trash2, Mail, MailOpen, MoreVertical, RefreshCw,
  ChevronLeft, ChevronRight, Star, Filter, Search, X,
  Reply, ReplyAll, Forward, Printer, MoreHorizontal, ArrowLeft,
  Clock, Tag, FolderOpen, ChevronDown, Users
} from 'lucide-vue-next'
import AppLayout from '@/components/layout/AppLayout.vue'
import Spinner from '@/components/common/Spinner.vue'
import { useReceivedInboxStore } from '@/stores/receivedInbox'
import { useInboxStore } from '@/stores/inbox'
import { useAuthStore } from '@/stores/auth'
import { useDomainsStore } from '@/stores/domains'
import type { ReceivedEmail, Email, Identity } from '@/lib/api'

// Sanitize email HTML content to prevent XSS attacks
const sanitizedEmailHtml = computed(() => {
  if (!receivedInboxStore.currentEmail?.htmlBody) return ''
  return DOMPurify.sanitize(receivedInboxStore.currentEmail.htmlBody, {
    ALLOWED_TAGS: ['p', 'br', 'b', 'i', 'u', 'a', 'strong', 'em', 'ul', 'ol', 'li',
                   'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'blockquote', 'pre', 'code',
                   'img', 'table', 'tr', 'td', 'th', 'thead', 'tbody', 'div', 'span',
                   'hr', 'sup', 'sub', 'small', 'font', 'center'],
    ALLOWED_ATTR: ['href', 'src', 'alt', 'style', 'class', 'target', 'width', 'height',
                   'border', 'cellpadding', 'cellspacing', 'align', 'valign', 'bgcolor',
                   'color', 'size', 'face'],
    ALLOW_DATA_ATTR: false,
    ADD_ATTR: ['target'],
    FORCE_BODY: true
  })
})

const route = useRoute()
const router = useRouter()
const receivedInboxStore = useReceivedInboxStore()
const inboxStore = useInboxStore()
const authStore = useAuthStore()
const domainsStore = useDomainsStore()

// Helper to convert ReceivedEmail to Email format for ComposeModal
function convertToEmail(received: ReceivedEmail): Email {
  return {
    id: String(received.id),
    uuid: received.uuid,
    messageId: received.messageId,
    subject: received.subject,
    from: {
      name: received.fromName || '',
      email: received.fromEmail
    },
    to: (received.toEmails || []).map(email => ({ name: '', email })),
    cc: (received.ccEmails || []).map(email => ({ name: '', email })),
    body: received.textBody || '',
    htmlBody: received.htmlBody,
    snippet: received.snippet || '',
    folder: received.folder,
    isRead: received.isRead,
    isStarred: received.isStarred,
    hasAttachments: received.hasAttachments,
    receivedAt: received.receivedAt,
    createdAt: received.createdAt,
    // Include identityId so reply knows which identity to use
    identityId: received.identityId
  }
}

const searchInput = ref('')
const showFilters = ref(false)
const selectedEmail = ref<ReceivedEmail | null>(null)
const showMoreActions = ref(false)
const showIdentityDropdown = ref(false)

// Get current identity ID (0 = all identities / unified inbox)
const currentIdentityId = ref<number>(0)

// Available identities for filter dropdown
const identities = computed(() => domainsStore.identities || [])

const currentFolder = computed(() => {
  return (route.query.folder as string) || 'inbox'
})

// Current email index for navigation
const currentEmailIndex = computed(() => {
  if (!selectedEmail.value) return -1
  return receivedInboxStore.emails?.findIndex(e => e.uuid === selectedEmail.value?.uuid) ?? -1
})

const hasPreviousEmail = computed(() => currentEmailIndex.value > 0)
const hasNextEmail = computed(() => currentEmailIndex.value < (receivedInboxStore.emails?.length ?? 0) - 1)

onMounted(async () => {
  receivedInboxStore.connectSSE()

  // Fetch identities for the filter dropdown
  await domainsStore.fetchIdentities()

  // Check if specific identity requested via query param
  const identityId = route.query.identity ? Number(route.query.identity) : 0
  currentIdentityId.value = identityId  // 0 = all identities (unified inbox)

  // Load emails (will fetch all if identityId is 0)
  await loadEmails()
})

onUnmounted(() => {
  receivedInboxStore.disconnectSSE()
})

watch(() => route.query.folder, async () => {
  await loadEmails()
})

watch(() => route.query.identity, async (newId) => {
  if (newId) {
    currentIdentityId.value = Number(newId)
    await loadEmails()
  }
})

async function loadEmails() {
  // currentIdentityId = 0 means all identities (unified inbox)
  await receivedInboxStore.fetchEmails(currentIdentityId.value, {
    folder: currentFolder.value,
    reset: true
  })
  await receivedInboxStore.fetchCounts(currentIdentityId.value)
}

// Handle identity filter change
async function changeIdentityFilter(identityId: number) {
  currentIdentityId.value = identityId
  showIdentityDropdown.value = false
  selectedEmail.value = null
  receivedInboxStore.currentEmail = null

  // Update URL query param
  const query = { ...route.query }
  if (identityId > 0) {
    query.identity = String(identityId)
  } else {
    delete query.identity
  }
  router.replace({ query })

  await loadEmails()
}

// Get identity by ID
function getIdentityById(id: number): Identity | undefined {
  return identities.value.find(i => Number(i.id) === id)
}

// Get display name for current filter
const currentFilterLabel = computed(() => {
  if (currentIdentityId.value === 0) {
    return 'All Identities'
  }
  const identity = getIdentityById(currentIdentityId.value)
  return identity?.email || 'Unknown'
})

async function selectEmail(email: ReceivedEmail) {
  selectedEmail.value = email
  await receivedInboxStore.fetchEmail(email.uuid)
  if (!email.isRead) {
    receivedInboxStore.markAsRead([email.uuid], true)
  }
}

function closeEmail() {
  selectedEmail.value = null
  receivedInboxStore.currentEmail = null
}

// Navigation functions
async function goToPreviousEmail() {
  if (hasPreviousEmail.value) {
    const prevEmail = receivedInboxStore.emails[currentEmailIndex.value - 1]
    await selectEmail(prevEmail)
  }
}

async function goToNextEmail() {
  if (hasNextEmail.value) {
    const nextEmail = receivedInboxStore.emails[currentEmailIndex.value + 1]
    await selectEmail(nextEmail)
  }
}

// Email actions
function handleReply() {
  if (!receivedInboxStore.currentEmail) return
  const email = convertToEmail(receivedInboxStore.currentEmail)
  inboxStore.openCompose('reply', email)
}

function handleReplyAll() {
  if (!receivedInboxStore.currentEmail) return
  const email = convertToEmail(receivedInboxStore.currentEmail)
  inboxStore.openCompose('replyAll', email)
}

function handleForward() {
  if (!receivedInboxStore.currentEmail) return
  const email = convertToEmail(receivedInboxStore.currentEmail)
  inboxStore.openCompose('forward', email)
}

async function handleArchiveEmail() {
  if (!selectedEmail.value) return
  await receivedInboxStore.moveEmails([selectedEmail.value.uuid], 'archive')
  goToNextEmail() || closeEmail()
}

async function handleDeleteEmail() {
  if (!selectedEmail.value) return
  const permanent = currentFolder.value === 'trash'
  await receivedInboxStore.trashEmails([selectedEmail.value.uuid], permanent)
  if (hasNextEmail.value) {
    goToNextEmail()
  } else if (hasPreviousEmail.value) {
    goToPreviousEmail()
  } else {
    closeEmail()
  }
}

async function handleMarkEmailUnread() {
  if (!selectedEmail.value) return
  await receivedInboxStore.markAsRead([selectedEmail.value.uuid], false)
  closeEmail()
}

async function handleStarEmail() {
  if (!receivedInboxStore.currentEmail) return
  await receivedInboxStore.starEmails([receivedInboxStore.currentEmail.uuid], !receivedInboxStore.currentEmail.isStarred)
}

async function handleSnooze() {
  // TODO: Implement snooze functionality
  alert('Snooze feature coming soon!')
}

function handlePrint() {
  window.print()
}

// List toolbar actions
async function handleSearch() {
  if (!currentIdentityId.value) return
  await receivedInboxStore.fetchEmails(currentIdentityId.value, {
    search: searchInput.value,
    reset: true
  })
}

function clearSearch() {
  searchInput.value = ''
  loadEmails()
}

async function handleMarkRead() {
  if (!receivedInboxStore.selectedEmailUuids?.length) return
  await receivedInboxStore.markAsRead(receivedInboxStore.selectedEmailUuids, true)
  receivedInboxStore.clearSelection()
}

async function handleMarkUnread() {
  if (!receivedInboxStore.selectedEmailUuids?.length) return
  await receivedInboxStore.markAsRead(receivedInboxStore.selectedEmailUuids, false)
  receivedInboxStore.clearSelection()
}

async function handleStar() {
  if (!receivedInboxStore.selectedEmailUuids?.length) return
  await receivedInboxStore.starEmails(receivedInboxStore.selectedEmailUuids, true)
  receivedInboxStore.clearSelection()
}

async function handleArchive() {
  if (!receivedInboxStore.selectedEmailUuids?.length) return
  await receivedInboxStore.moveEmails(receivedInboxStore.selectedEmailUuids, 'archive')
  receivedInboxStore.clearSelection()
}

async function handleDelete() {
  if (!receivedInboxStore.selectedEmailUuids?.length) return
  const permanent = currentFolder.value === 'trash'
  await receivedInboxStore.trashEmails(receivedInboxStore.selectedEmailUuids, permanent)
  receivedInboxStore.clearSelection()
}

async function toggleEmailStar(email: ReceivedEmail) {
  await receivedInboxStore.starEmails([email.uuid], !email.isStarred)
}

function prevPage() {
  if (receivedInboxStore.page > 1) {
    receivedInboxStore.fetchEmails(currentIdentityId.value, {
      page: receivedInboxStore.page - 1
    })
  }
}

function nextPage() {
  if (receivedInboxStore.hasMore) {
    receivedInboxStore.fetchEmails(currentIdentityId.value, {
      page: receivedInboxStore.page + 1
    })
  }
}

function formatDate(dateStr: string): string {
  const date = new Date(dateStr)
  const now = new Date()
  const isToday = date.toDateString() === now.toDateString()

  if (isToday) {
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }

  const isThisYear = date.getFullYear() === now.getFullYear()
  if (isThisYear) {
    return date.toLocaleDateString([], { month: 'short', day: 'numeric' })
  }

  return date.toLocaleDateString([], { year: 'numeric', month: 'short', day: 'numeric' })
}

function formatFullDate(dateStr: string): string {
  const date = new Date(dateStr)
  return date.toLocaleDateString([], {
    weekday: 'short',
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit'
  })
}
</script>

<template>
  <AppLayout>
    <div class="flex h-full bg-gray-50">
      <!-- Main content -->
      <div class="flex-1 flex flex-col">
        <!-- Toolbar -->
        <div class="flex items-center gap-2 px-4 py-2 border-b border-gray-200 bg-white shadow-sm">
          <!-- Back button when email is selected -->
          <button
            v-if="selectedEmail"
            @click="closeEmail"
            class="p-2 hover:bg-gray-100 rounded-full mr-2"
            title="Back to inbox"
          >
            <ArrowLeft class="w-5 h-5 text-gray-600" />
          </button>

          <!-- Select all (only when no email selected) -->
          <div v-if="!selectedEmail" class="flex items-center gap-1">
            <input
              type="checkbox"
              :checked="receivedInboxStore.allSelected"
              :indeterminate="receivedInboxStore.someSelected"
              @change="receivedInboxStore.selectAll()"
              class="w-4 h-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500"
            />
          </div>

          <!-- Actions when emails selected in list -->
          <template v-if="!selectedEmail && receivedInboxStore.selectedEmailUuids?.length > 0">
            <button @click="handleArchive" class="p-2 hover:bg-gray-100 rounded-full" title="Archive">
              <Archive class="w-5 h-5 text-gray-600" />
            </button>
            <button @click="handleDelete" class="p-2 hover:bg-gray-100 rounded-full" title="Delete">
              <Trash2 class="w-5 h-5 text-gray-600" />
            </button>
            <button @click="handleMarkRead" class="p-2 hover:bg-gray-100 rounded-full" title="Mark as read">
              <MailOpen class="w-5 h-5 text-gray-600" />
            </button>
            <button @click="handleMarkUnread" class="p-2 hover:bg-gray-100 rounded-full" title="Mark as unread">
              <Mail class="w-5 h-5 text-gray-600" />
            </button>
            <button @click="handleStar" class="p-2 hover:bg-gray-100 rounded-full" title="Star">
              <Star class="w-5 h-5 text-gray-600" />
            </button>
          </template>

          <!-- Email view actions -->
          <template v-else-if="selectedEmail && receivedInboxStore.currentEmail">
            <button @click="handleArchiveEmail" class="p-2 hover:bg-gray-100 rounded-full" title="Archive">
              <Archive class="w-5 h-5 text-gray-600" />
            </button>
            <button @click="handleDeleteEmail" class="p-2 hover:bg-gray-100 rounded-full" title="Delete">
              <Trash2 class="w-5 h-5 text-gray-600" />
            </button>
            <button @click="handleMarkEmailUnread" class="p-2 hover:bg-gray-100 rounded-full" title="Mark as unread">
              <Mail class="w-5 h-5 text-gray-600" />
            </button>
            <button @click="handleSnooze" class="p-2 hover:bg-gray-100 rounded-full" title="Snooze">
              <Clock class="w-5 h-5 text-gray-600" />
            </button>
            <div class="w-px h-6 bg-gray-300 mx-1"></div>
            <button @click="handleStarEmail" class="p-2 hover:bg-gray-100 rounded-full" title="Star">
              <Star :class="['w-5 h-5', receivedInboxStore.currentEmail.isStarred ? 'fill-yellow-400 text-yellow-400' : 'text-gray-600']" />
            </button>
          </template>

          <!-- Default actions (refresh) -->
          <template v-else>
            <button @click="loadEmails" class="p-2 hover:bg-gray-100 rounded-full" title="Refresh">
              <RefreshCw class="w-5 h-5 text-gray-600" />
            </button>
          </template>

          <!-- Identity Filter Dropdown -->
          <div class="relative" v-if="!selectedEmail">
            <button
              @click="showIdentityDropdown = !showIdentityDropdown"
              class="flex items-center gap-2 px-3 py-1.5 text-sm border border-gray-300 rounded-lg hover:bg-gray-50 bg-white min-w-[180px]"
            >
              <Users class="w-4 h-4 text-gray-500" />
              <span
                v-if="currentIdentityId > 0 && getIdentityById(currentIdentityId)?.color"
                class="w-2.5 h-2.5 rounded-full flex-shrink-0"
                :style="{ backgroundColor: getIdentityById(currentIdentityId)?.color }"
              ></span>
              <span class="truncate flex-1 text-left">{{ currentFilterLabel }}</span>
              <ChevronDown class="w-4 h-4 text-gray-400 flex-shrink-0" />
            </button>
            <!-- Dropdown -->
            <div
              v-if="showIdentityDropdown"
              class="absolute top-full left-0 mt-1 w-64 bg-white border border-gray-200 rounded-lg shadow-lg z-20"
            >
              <!-- All Identities option -->
              <button
                @click="changeIdentityFilter(0)"
                :class="[
                  'w-full px-3 py-2.5 text-left text-sm flex items-center gap-2 hover:bg-gray-50',
                  currentIdentityId === 0 ? 'bg-blue-50 text-blue-700' : ''
                ]"
              >
                <Users class="w-4 h-4 text-gray-400" />
                <span class="font-medium">All Identities</span>
                <span class="ml-auto text-xs text-gray-500">Unified inbox</span>
              </button>
              <div class="border-t border-gray-100"></div>
              <!-- Individual identities -->
              <button
                v-for="identity in identities"
                :key="identity.id"
                @click="changeIdentityFilter(Number(identity.id))"
                :class="[
                  'w-full px-3 py-2.5 text-left text-sm flex items-center gap-2 hover:bg-gray-50',
                  currentIdentityId === Number(identity.id) ? 'bg-blue-50 text-blue-700' : ''
                ]"
              >
                <span
                  class="w-3 h-3 rounded-full flex-shrink-0"
                  :style="{ backgroundColor: identity.color || '#9CA3AF' }"
                ></span>
                <div class="flex-1 min-w-0">
                  <div class="font-medium truncate">{{ identity.displayName }}</div>
                  <div class="text-xs text-gray-500 truncate">{{ identity.email }}</div>
                </div>
              </button>
            </div>
            <!-- Click outside to close -->
            <div
              v-if="showIdentityDropdown"
              class="fixed inset-0 z-10"
              @click="showIdentityDropdown = false"
            ></div>
          </div>

          <!-- Search -->
          <div class="flex-1 max-w-xl mx-4">
            <div class="relative">
              <Search class="absolute left-3 top-1/2 -translate-y-1/2 w-5 h-5 text-gray-400" />
              <input
                v-model="searchInput"
                @keyup.enter="handleSearch"
                type="text"
                placeholder="Search emails..."
                class="w-full pl-10 pr-10 py-2 border border-gray-300 rounded-full focus:ring-2 focus:ring-blue-500 focus:border-blue-500 bg-gray-50 focus:bg-white transition-colors"
              />
              <button
                v-if="searchInput"
                @click="clearSearch"
                class="absolute right-3 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600"
              >
                <X class="w-5 h-5" />
              </button>
            </div>
          </div>

          <!-- Filters -->
          <button
            @click="showFilters = !showFilters"
            :class="[
              'p-2 rounded-full',
              showFilters ? 'bg-blue-100 text-blue-600' : 'hover:bg-gray-100 text-gray-600'
            ]"
            title="Filters"
          >
            <Filter class="w-5 h-5" />
          </button>

          <!-- Pagination / Navigation -->
          <div class="flex items-center gap-1 text-sm text-gray-600">
            <template v-if="selectedEmail">
              <span class="px-2">
                {{ currentEmailIndex + 1 }} of {{ receivedInboxStore.emails?.length ?? 0 }}
              </span>
            </template>
            <template v-else>
              <span class="px-2">
                {{ (receivedInboxStore.page - 1) * receivedInboxStore.pageSize + 1 }}-{{ Math.min(receivedInboxStore.page * receivedInboxStore.pageSize, receivedInboxStore.total) }}
                of {{ receivedInboxStore.total }}
              </span>
            </template>
            <button
              @click="selectedEmail ? goToPreviousEmail() : prevPage()"
              :disabled="selectedEmail ? !hasPreviousEmail : receivedInboxStore.page === 1"
              class="p-1.5 hover:bg-gray-100 rounded-full disabled:opacity-30 disabled:cursor-not-allowed"
            >
              <ChevronLeft class="w-5 h-5" />
            </button>
            <button
              @click="selectedEmail ? goToNextEmail() : nextPage()"
              :disabled="selectedEmail ? !hasNextEmail : !receivedInboxStore.hasMore"
              class="p-1.5 hover:bg-gray-100 rounded-full disabled:opacity-30 disabled:cursor-not-allowed"
            >
              <ChevronRight class="w-5 h-5" />
            </button>
          </div>
        </div>

        <!-- Email list / view -->
        <div class="flex-1 flex overflow-hidden">
          <!-- Email list -->
          <div
            :class="[
              'overflow-y-auto bg-white transition-all duration-200',
              selectedEmail ? 'w-80 border-r border-gray-200 hidden lg:block' : 'flex-1'
            ]"
          >
            <!-- Loading -->
            <div v-if="receivedInboxStore.isLoading && !receivedInboxStore.emails?.length" class="flex items-center justify-center h-64">
              <Spinner size="lg" />
            </div>

            <!-- Empty state -->
            <div
              v-else-if="!receivedInboxStore.emails?.length"
              class="flex flex-col items-center justify-center h-full text-gray-500 py-16"
            >
              <div class="w-24 h-24 rounded-full bg-gray-100 flex items-center justify-center mb-6">
                <Mail class="w-12 h-12 text-gray-400" />
              </div>
              <p class="text-xl font-medium text-gray-700">No emails in {{ currentFolder }}</p>
              <p class="text-sm text-gray-500 mt-2">Emails sent to your identity will appear here</p>
            </div>

            <!-- Email rows -->
            <ul v-else class="divide-y divide-gray-100">
              <li
                v-for="email in receivedInboxStore.emails"
                :key="email.uuid"
                @click="selectEmail(email)"
                :class="[
                  'flex items-center gap-3 px-3 py-3 cursor-pointer transition-all duration-100',
                  email.isRead ? 'bg-white' : 'bg-blue-50/60',
                  selectedEmail?.uuid === email.uuid ? 'bg-blue-100 border-l-4 border-l-blue-600' : 'hover:bg-gray-50 border-l-4 border-l-transparent',
                  receivedInboxStore.selectedEmailUuids.includes(email.uuid) ? 'bg-blue-50' : ''
                ]"
              >
                <!-- Checkbox (only show when no email selected) -->
                <input
                  v-if="!selectedEmail"
                  type="checkbox"
                  :checked="receivedInboxStore.selectedEmailUuids.includes(email.uuid)"
                  @click.stop="receivedInboxStore.toggleSelect(email.uuid)"
                  class="w-4 h-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500 flex-shrink-0"
                />

                <!-- Star -->
                <button
                  @click.stop="toggleEmailStar(email)"
                  class="p-1 hover:bg-gray-200 rounded-full transition-colors flex-shrink-0"
                >
                  <Star
                    :class="[
                      'w-4 h-4 transition-colors',
                      email.isStarred ? 'fill-yellow-400 text-yellow-400' : 'text-gray-300 hover:text-gray-400'
                    ]"
                  />
                </button>

                <!-- Identity color dot (shows which identity received this email) -->
                <span
                  v-if="email.identityColor || currentIdentityId === 0"
                  class="w-2.5 h-2.5 rounded-full flex-shrink-0"
                  :style="{ backgroundColor: email.identityColor || '#9CA3AF' }"
                  :title="email.identityEmail || 'Unknown identity'"
                ></span>

                <!-- Avatar -->
                <div
                  :class="[
                    'w-9 h-9 rounded-full flex items-center justify-center text-white font-medium text-sm flex-shrink-0',
                    email.isRead ? 'bg-gray-400' : 'bg-blue-600'
                  ]"
                >
                  {{ (email.fromName || email.fromEmail).charAt(0).toUpperCase() }}
                </div>

                <!-- Content -->
                <div class="flex-1 min-w-0">
                  <div class="flex items-center justify-between gap-2">
                    <span
                      :class="[
                        'truncate text-sm',
                        email.isRead ? 'font-normal text-gray-600' : 'font-semibold text-gray-900'
                      ]"
                    >
                      {{ email.fromName || email.fromEmail }}
                    </span>
                    <span
                      :class="[
                        'text-xs whitespace-nowrap flex-shrink-0',
                        email.isRead ? 'text-gray-500' : 'text-blue-600 font-medium'
                      ]"
                    >
                      {{ formatDate(email.receivedAt) }}
                    </span>
                  </div>
                  <div class="flex items-center gap-1">
                    <span
                      :class="[
                        'text-sm truncate',
                        email.isRead ? 'text-gray-600' : 'font-medium text-gray-900'
                      ]"
                    >
                      {{ email.subject || '(no subject)' }}
                    </span>
                    <span v-if="email.hasAttachments" class="text-gray-400 flex-shrink-0">
                      <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15.172 7l-6.586 6.586a2 2 0 102.828 2.828l6.414-6.586a4 4 0 00-5.656-5.656l-6.415 6.585a6 6 0 108.486 8.486L20.5 13" />
                      </svg>
                    </span>
                  </div>
                  <div class="text-xs text-gray-500 truncate mt-0.5">
                    {{ email.snippet }}
                  </div>
                </div>
              </li>
            </ul>
          </div>

          <!-- Email view -->
          <div v-if="selectedEmail" class="flex-1 flex flex-col overflow-hidden bg-white">
            <!-- Loading state -->
            <div v-if="receivedInboxStore.isLoading && !receivedInboxStore.currentEmail" class="flex-1 flex items-center justify-center">
              <Spinner size="lg" />
            </div>

            <template v-else-if="receivedInboxStore.currentEmail">
              <!-- Email header -->
              <div class="px-6 py-4 border-b border-gray-200">
                <!-- Subject -->
                <h1 class="text-xl font-normal text-gray-900 mb-4">
                  {{ receivedInboxStore.currentEmail.subject || '(no subject)' }}
                </h1>

                <!-- Sender info -->
                <div class="flex items-start gap-4">
                  <div class="w-10 h-10 rounded-full bg-blue-600 flex items-center justify-center text-white font-semibold flex-shrink-0">
                    {{ (receivedInboxStore.currentEmail.fromName || receivedInboxStore.currentEmail.fromEmail).charAt(0).toUpperCase() }}
                  </div>

                  <div class="flex-1 min-w-0">
                    <div class="flex items-center gap-2 flex-wrap">
                      <span class="font-semibold text-gray-900">
                        {{ receivedInboxStore.currentEmail.fromName || receivedInboxStore.currentEmail.fromEmail }}
                      </span>
                      <span class="text-sm text-gray-500">
                        &lt;{{ receivedInboxStore.currentEmail.fromEmail }}&gt;
                      </span>
                    </div>
                    <div class="text-sm text-gray-500 mt-0.5">
                      to {{ receivedInboxStore.currentEmail.toEmails?.join(', ') }}
                      <span v-if="receivedInboxStore.currentEmail.ccEmails?.length">
                        , cc: {{ receivedInboxStore.currentEmail.ccEmails.join(', ') }}
                      </span>
                    </div>
                  </div>

                  <div class="flex items-center gap-2 flex-shrink-0">
                    <span class="text-sm text-gray-500">
                      {{ formatFullDate(receivedInboxStore.currentEmail.receivedAt) }}
                    </span>
                    <button @click="handleStarEmail" class="p-1.5 hover:bg-gray-100 rounded-full">
                      <Star :class="['w-5 h-5', receivedInboxStore.currentEmail.isStarred ? 'fill-yellow-400 text-yellow-400' : 'text-gray-400']" />
                    </button>
                    <button @click="handleReply" class="p-1.5 hover:bg-gray-100 rounded-full" title="Reply">
                      <Reply class="w-5 h-5 text-gray-500" />
                    </button>
                    <div class="relative">
                      <button @click="showMoreActions = !showMoreActions" class="p-1.5 hover:bg-gray-100 rounded-full">
                        <MoreVertical class="w-5 h-5 text-gray-500" />
                      </button>
                      <!-- Dropdown -->
                      <div
                        v-if="showMoreActions"
                        class="absolute right-0 top-full mt-1 w-48 bg-white rounded-lg shadow-lg border border-gray-200 py-1 z-10"
                        @click="showMoreActions = false"
                      >
                        <button @click="handleReplyAll" class="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 flex items-center gap-3">
                          <ReplyAll class="w-4 h-4 text-gray-500" />
                          Reply all
                        </button>
                        <button @click="handleForward" class="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 flex items-center gap-3">
                          <Forward class="w-4 h-4 text-gray-500" />
                          Forward
                        </button>
                        <hr class="my-1 border-gray-200" />
                        <button @click="handlePrint" class="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 flex items-center gap-3">
                          <Printer class="w-4 h-4 text-gray-500" />
                          Print
                        </button>
                        <button @click="handleDeleteEmail" class="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 flex items-center gap-3 text-red-600">
                          <Trash2 class="w-4 h-4" />
                          Delete
                        </button>
                      </div>
                    </div>
                  </div>
                </div>
              </div>

              <!-- Email body -->
              <div class="flex-1 overflow-y-auto">
                <div class="px-6 py-4 max-w-4xl">
                  <div
                    v-if="sanitizedEmailHtml"
                    v-html="sanitizedEmailHtml"
                    class="prose prose-sm max-w-none prose-headings:text-gray-900 prose-p:text-gray-700 prose-a:text-blue-600"
                  ></div>
                  <div
                    v-else-if="receivedInboxStore.currentEmail.textBody"
                    class="whitespace-pre-wrap font-sans text-gray-800 leading-relaxed text-sm"
                  >{{ receivedInboxStore.currentEmail.textBody }}</div>
                  <div v-else class="text-gray-500 italic">
                    No message content available
                  </div>
                </div>

                <!-- Attachments -->
                <div
                  v-if="receivedInboxStore.currentEmail.attachments?.length"
                  class="px-6 py-4 border-t border-gray-100"
                >
                  <h3 class="text-sm font-medium text-gray-700 mb-3">
                    {{ receivedInboxStore.currentEmail.attachments?.length }} Attachment{{ receivedInboxStore.currentEmail.attachments?.length > 1 ? 's' : '' }}
                  </h3>
                  <div class="flex flex-wrap gap-2">
                    <a
                      v-for="att in receivedInboxStore.currentEmail.attachments"
                      :key="att.uuid"
                      :href="att.downloadUrl"
                      target="_blank"
                      class="flex items-center gap-2 px-3 py-2 bg-gray-100 border border-gray-200 rounded-lg hover:bg-gray-200 transition-colors text-sm"
                    >
                      <svg class="w-4 h-4 text-gray-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15.172 7l-6.586 6.586a2 2 0 102.828 2.828l6.414-6.586a4 4 0 00-5.656-5.656l-6.415 6.585a6 6 0 108.486 8.486L20.5 13" />
                      </svg>
                      <span class="font-medium">{{ att.filename }}</span>
                      <span class="text-gray-500">({{ Math.round(att.sizeBytes / 1024) }} KB)</span>
                    </a>
                  </div>
                </div>
              </div>

              <!-- Reply box -->
              <div class="px-6 py-4 border-t border-gray-200 bg-gray-50">
                <div class="flex gap-2">
                  <button
                    @click="handleReply"
                    class="flex items-center gap-2 px-4 py-2 bg-white border border-gray-300 rounded-full hover:bg-gray-50 transition-colors text-sm font-medium"
                  >
                    <Reply class="w-4 h-4" />
                    Reply
                  </button>
                  <button
                    @click="handleForward"
                    class="flex items-center gap-2 px-4 py-2 bg-white border border-gray-300 rounded-full hover:bg-gray-50 transition-colors text-sm font-medium"
                  >
                    <Forward class="w-4 h-4" />
                    Forward
                  </button>
                </div>
              </div>
            </template>
          </div>
        </div>
      </div>
    </div>
  </AppLayout>
</template>
