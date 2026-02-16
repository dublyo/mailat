import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { inboxApi, type Email, type Folder } from '@/lib/api'

export const useInboxStore = defineStore('inbox', () => {
  // State
  const emails = ref<Email[]>([])
  const folders = ref<Folder[]>([
    { id: 'inbox', name: 'Inbox', type: 'system', unreadCount: 0, totalCount: 0 },
    { id: 'starred', name: 'Starred', type: 'system', unreadCount: 0, totalCount: 0 },
    { id: 'snoozed', name: 'Snoozed', type: 'system', unreadCount: 0, totalCount: 0 },
    { id: 'sent', name: 'Sent', type: 'system', unreadCount: 0, totalCount: 0 },
    { id: 'drafts', name: 'Drafts', type: 'system', unreadCount: 0, totalCount: 0 },
    { id: 'spam', name: 'Spam', type: 'system', unreadCount: 0, totalCount: 0 },
    { id: 'trash', name: 'Trash', type: 'system', unreadCount: 0, totalCount: 0 },
  ])
  const currentFolder = ref('inbox')
  const selectedEmails = ref<Set<string>>(new Set())
  const activeEmail = ref<Email | null>(null)
  const isLoading = ref(false)
  const totalEmails = ref(0)
  const page = ref(1)

  // Search state
  const searchQuery = ref('')
  const isSearching = ref(false)

  // Compose state
  const isComposeOpen = ref(false)
  const composeMode = ref<'new' | 'reply' | 'replyAll' | 'forward'>('new')
  const replyToEmail = ref<Email | null>(null)

  // Computed
  const hasSelection = computed(() => selectedEmails.value.size > 0)
  const allSelected = computed(
    () => emails.value.length > 0 && selectedEmails.value.size === emails.value.length
  )

  // Actions
  async function fetchEmails(folder?: string) {
    const targetFolder = folder || currentFolder.value
    isLoading.value = true
    try {
      const result = await inboxApi.list(targetFolder, page.value, 50)
      emails.value = (result?.emails ?? []).filter((e): e is Email => e != null)
      totalEmails.value = result?.total ?? 0
    } catch (e) {
      emails.value = []
      totalEmails.value = 0
    } finally {
      isLoading.value = false
    }
  }

  async function searchEmails(query: string) {
    if (!query.trim()) {
      isSearching.value = false
      await fetchEmails()
      return
    }

    isSearching.value = true
    isLoading.value = true
    try {
      const result = await inboxApi.search(query, page.value, 50)
      emails.value = (result?.emails ?? []).filter((e): e is Email => e != null)
      totalEmails.value = result?.total ?? 0
    } catch (e) {
      emails.value = []
      totalEmails.value = 0
    } finally {
      isLoading.value = false
    }
  }

  function setCurrentFolder(folder: string) {
    currentFolder.value = folder
    selectedEmails.value = new Set()
    activeEmail.value = null
    page.value = 1
    searchQuery.value = ''
    isSearching.value = false
  }

  function toggleEmailSelection(uuid: string) {
    const newSet = new Set(selectedEmails.value)
    if (newSet.has(uuid)) {
      newSet.delete(uuid)
    } else {
      newSet.add(uuid)
    }
    selectedEmails.value = newSet
  }

  function selectAll() {
    selectedEmails.value = new Set(emails.value.map(e => e.uuid))
  }

  function clearSelection() {
    selectedEmails.value = new Set()
  }

  function setActiveEmail(email: Email | null) {
    activeEmail.value = email
    if (email && !email.isRead) {
      markAsRead([email.uuid])
    }
  }

  async function markAsRead(uuids: string[]) {
    try {
      await inboxApi.markRead(uuids)
      emails.value = emails.value.map(e =>
        uuids.includes(e.uuid) ? { ...e, isRead: true } : e
      )
      if (activeEmail.value && uuids.includes(activeEmail.value.uuid)) {
        activeEmail.value = { ...activeEmail.value, isRead: true }
      }
    } catch (error) {
      console.error('Failed to mark as read:', error)
    }
  }

  async function markAsUnread(uuids: string[]) {
    try {
      await inboxApi.markUnread(uuids)
      emails.value = emails.value.map(e =>
        uuids.includes(e.uuid) ? { ...e, isRead: false } : e
      )
    } catch (error) {
      console.error('Failed to mark as unread:', error)
    }
  }

  async function toggleStar(uuid: string) {
    const email = emails.value.find(e => e.uuid === uuid)
    if (!email) return

    try {
      if (email.isStarred) {
        await inboxApi.unstar(uuid)
      } else {
        await inboxApi.star(uuid)
      }
      emails.value = emails.value.map(e =>
        e.uuid === uuid ? { ...e, isStarred: !e.isStarred } : e
      )
      if (activeEmail.value?.uuid === uuid) {
        activeEmail.value = { ...activeEmail.value, isStarred: !activeEmail.value.isStarred }
      }
    } catch (error) {
      console.error('Failed to toggle star:', error)
    }
  }

  async function moveToFolder(uuids: string[], folder: string) {
    try {
      await inboxApi.move(uuids, folder)
      emails.value = emails.value.filter(e => !uuids.includes(e.uuid))
      selectedEmails.value = new Set()
      if (activeEmail.value && uuids.includes(activeEmail.value.uuid)) {
        activeEmail.value = null
      }
    } catch (error) {
      console.error('Failed to move emails:', error)
    }
  }

  async function deleteEmails(uuids: string[]) {
    try {
      if (currentFolder.value === 'trash') {
        await inboxApi.delete(uuids)
      } else {
        await inboxApi.move(uuids, 'trash')
      }
      emails.value = emails.value.filter(e => !uuids.includes(e.uuid))
      selectedEmails.value = new Set()
      if (activeEmail.value && uuids.includes(activeEmail.value.uuid)) {
        activeEmail.value = null
      }
    } catch (error) {
      console.error('Failed to delete emails:', error)
    }
  }

  function openCompose(mode: 'new' | 'reply' | 'replyAll' | 'forward' = 'new', email?: Email) {
    composeMode.value = mode
    replyToEmail.value = email || null
    isComposeOpen.value = true
  }

  function closeCompose() {
    isComposeOpen.value = false
    composeMode.value = 'new'
    replyToEmail.value = null
  }

  return {
    // State
    emails,
    folders,
    currentFolder,
    selectedEmails,
    activeEmail,
    isLoading,
    totalEmails,
    page,
    searchQuery,
    isSearching,
    isComposeOpen,
    composeMode,
    replyToEmail,
    // Computed
    hasSelection,
    allSelected,
    // Actions
    fetchEmails,
    searchEmails,
    setCurrentFolder,
    toggleEmailSelection,
    selectAll,
    clearSelection,
    setActiveEmail,
    markAsRead,
    markAsUnread,
    toggleStar,
    moveToFolder,
    deleteEmails,
    openCompose,
    closeCompose
  }
})
