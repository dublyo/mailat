import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import {
  receivedInboxApi,
  labelApi,
  inboxSSE,
  type ReceivedEmail,
  type InboxCounts,
  type EmailLabel
} from '@/lib/api'

export const useReceivedInboxStore = defineStore('receivedInbox', () => {
  // State
  const emails = ref<ReceivedEmail[]>([])
  const currentEmail = ref<ReceivedEmail | null>(null)
  const counts = ref<InboxCounts | null>(null)
  const labels = ref<EmailLabel[]>([])
  const isLoading = ref(false)
  const error = ref<string | null>(null)

  // Pagination
  const page = ref(1)
  const pageSize = ref(50)
  const total = ref(0)
  const totalPages = ref(0)

  // Filters
  const currentFolder = ref('inbox')
  const currentIdentityId = ref<number | null>(null)
  const searchQuery = ref('')
  const selectedLabels = ref<string[]>([])

  // Selection
  const selectedEmailUuids = ref<string[]>([])

  // SSE connection status
  const sseConnected = ref(false)

  // Computed
  const unreadCount = computed(() => counts.value?.unread ?? 0)
  const hasMore = computed(() => page.value < totalPages.value)
  const allSelected = computed(() =>
    (emails.value?.length ?? 0) > 0 && (selectedEmailUuids.value?.length ?? 0) === (emails.value?.length ?? 0)
  )
  const someSelected = computed(() =>
    (selectedEmailUuids.value?.length ?? 0) > 0 && (selectedEmailUuids.value?.length ?? 0) < (emails.value?.length ?? 0)
  )

  // Actions
  // identityId = 0 means fetch from all identities (unified inbox)
  async function fetchEmails(identityId: number, options?: {
    folder?: string
    page?: number
    search?: string
    labels?: string[]
    reset?: boolean
  }) {
    isLoading.value = true
    error.value = null

    try {
      currentIdentityId.value = identityId
      if (options?.folder) currentFolder.value = options.folder
      if (options?.page) page.value = options.page
      if (options?.search !== undefined) searchQuery.value = options.search
      if (options?.labels) selectedLabels.value = options.labels
      if (options?.reset) {
        page.value = 1
        selectedEmailUuids.value = []
      }

      const result = await receivedInboxApi.list(identityId, {
        folder: currentFolder.value,
        search: searchQuery.value || undefined,
        labels: selectedLabels.value.length ? selectedLabels.value : undefined,
        page: page.value,
        pageSize: pageSize.value
      })

      emails.value = result.emails
      total.value = result.total
      totalPages.value = result.totalPages
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch emails'
      emails.value = []
    } finally {
      isLoading.value = false
    }
  }

  async function fetchEmail(uuid: string) {
    isLoading.value = true
    error.value = null

    try {
      currentEmail.value = await receivedInboxApi.get(uuid)

      // Update email in list as read
      const index = emails.value.findIndex(e => e.uuid === uuid)
      if (index !== -1) {
        emails.value[index] = { ...emails.value[index], isRead: true }
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch email'
      currentEmail.value = null
    } finally {
      isLoading.value = false
    }
  }

  // identityId = 0 means fetch counts across all identities
  async function fetchCounts(identityId: number) {
    try {
      counts.value = await receivedInboxApi.getCounts(identityId)
    } catch (e) {
      console.error('Failed to fetch counts:', e)
    }
  }

  async function fetchLabels() {
    try {
      labels.value = await labelApi.list()
    } catch (e) {
      console.error('Failed to fetch labels:', e)
    }
  }

  async function markAsRead(uuids: string[], isRead: boolean) {
    try {
      await receivedInboxApi.mark(uuids, isRead)

      // Update local state
      for (const uuid of uuids) {
        const index = emails.value.findIndex(e => e.uuid === uuid)
        if (index !== -1) {
          emails.value[index] = { ...emails.value[index], isRead }
        }
        if (currentEmail.value?.uuid === uuid) {
          currentEmail.value = { ...currentEmail.value, isRead }
        }
      }

      // Refresh counts
      if (currentIdentityId.value) {
        await fetchCounts(currentIdentityId.value)
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to update emails'
      throw e
    }
  }

  async function starEmails(uuids: string[], isStarred: boolean) {
    try {
      await receivedInboxApi.star(uuids, isStarred)

      // Update local state
      for (const uuid of uuids) {
        const index = emails.value.findIndex(e => e.uuid === uuid)
        if (index !== -1) {
          emails.value[index] = { ...emails.value[index], isStarred }
        }
        if (currentEmail.value?.uuid === uuid) {
          currentEmail.value = { ...currentEmail.value, isStarred }
        }
      }

      // Refresh counts
      if (currentIdentityId.value) {
        await fetchCounts(currentIdentityId.value)
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to update emails'
      throw e
    }
  }

  async function moveEmails(uuids: string[], folder: string) {
    try {
      await receivedInboxApi.move(uuids, folder)

      // Remove from current view if moving to different folder
      if (folder !== currentFolder.value) {
        emails.value = emails.value.filter(e => !uuids.includes(e.uuid))
        selectedEmailUuids.value = selectedEmailUuids.value.filter(u => !uuids.includes(u))
      }

      // Refresh counts
      if (currentIdentityId.value) {
        await fetchCounts(currentIdentityId.value)
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to move emails'
      throw e
    }
  }

  async function trashEmails(uuids: string[], permanent = false) {
    try {
      await receivedInboxApi.trash(uuids, permanent)

      // Remove from current view
      emails.value = emails.value.filter(e => !uuids.includes(e.uuid))
      selectedEmailUuids.value = selectedEmailUuids.value.filter(u => !uuids.includes(u))

      if (currentEmail.value && uuids.includes(currentEmail.value.uuid)) {
        currentEmail.value = null
      }

      // Refresh counts
      if (currentIdentityId.value) {
        await fetchCounts(currentIdentityId.value)
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to delete emails'
      throw e
    }
  }

  async function createLabel(name: string, color?: string) {
    try {
      const label = await labelApi.create({ name, color })
      labels.value.push(label)
      return label
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to create label'
      throw e
    }
  }

  async function deleteLabel(uuid: string) {
    try {
      await labelApi.delete(uuid)
      labels.value = labels.value.filter(l => l.uuid !== uuid)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to delete label'
      throw e
    }
  }

  // Selection
  function toggleSelect(uuid: string) {
    const index = selectedEmailUuids.value.indexOf(uuid)
    if (index === -1) {
      selectedEmailUuids.value.push(uuid)
    } else {
      selectedEmailUuids.value.splice(index, 1)
    }
  }

  function selectAll() {
    if (allSelected.value) {
      selectedEmailUuids.value = []
    } else {
      selectedEmailUuids.value = emails.value.map(e => e.uuid)
    }
  }

  function clearSelection() {
    selectedEmailUuids.value = []
  }

  // SSE
  function connectSSE() {
    inboxSSE.connect({
      onConnected: () => {
        sseConnected.value = true
        console.log('SSE connected')
      },
      onNewEmail: (email) => {
        // Add to top of list if in current folder
        if (email.identityId === currentIdentityId.value) {
          if (
            (currentFolder.value === 'inbox' && email.folder === 'inbox') ||
            currentFolder.value === 'all'
          ) {
            emails.value = [email, ...emails.value]
            total.value++
          }

          // Update counts
          if (counts.value) {
            counts.value.inbox++
            counts.value.unread++
          }
        }
      },
      onEmailUpdate: (data) => {
        const index = emails.value.findIndex(e => e.uuid === data.uuid)
        if (index !== -1) {
          emails.value[index] = { ...emails.value[index], ...data.updates }
        }
        if (currentEmail.value?.uuid === data.uuid) {
          currentEmail.value = { ...currentEmail.value, ...data.updates } as ReceivedEmail
        }
      },
      onEmailDeleted: (data) => {
        emails.value = emails.value.filter(e => !data.uuids.includes(e.uuid))
        selectedEmailUuids.value = selectedEmailUuids.value.filter(u => !data.uuids.includes(u))
        if (currentEmail.value && data.uuids.includes(currentEmail.value.uuid)) {
          currentEmail.value = null
        }
      },
      onCountsUpdate: (data) => {
        if (data.identityId === currentIdentityId.value) {
          counts.value = data
        }
      },
      onError: () => {
        sseConnected.value = false
      }
    })
  }

  function disconnectSSE() {
    inboxSSE.disconnect()
    sseConnected.value = false
  }

  // Reset
  function reset() {
    emails.value = []
    currentEmail.value = null
    counts.value = null
    page.value = 1
    total.value = 0
    totalPages.value = 0
    currentFolder.value = 'inbox'
    currentIdentityId.value = null
    searchQuery.value = ''
    selectedLabels.value = []
    selectedEmailUuids.value = []
    error.value = null
  }

  return {
    // State
    emails,
    currentEmail,
    counts,
    labels,
    isLoading,
    error,
    page,
    pageSize,
    total,
    totalPages,
    currentFolder,
    currentIdentityId,
    searchQuery,
    selectedLabels,
    selectedEmailUuids,
    sseConnected,

    // Computed
    unreadCount,
    hasMore,
    allSelected,
    someSelected,

    // Actions
    fetchEmails,
    fetchEmail,
    fetchCounts,
    fetchLabels,
    markAsRead,
    starEmails,
    moveEmails,
    trashEmails,
    createLabel,
    deleteLabel,
    toggleSelect,
    selectAll,
    clearSelection,
    connectSSE,
    disconnectSSE,
    reset
  }
})
