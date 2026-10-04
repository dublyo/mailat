import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, receivedInboxApi, inboxSSE, type ReceivedEmail, type InboxCounts, type InboxListResponse, type InboxListOptions } from '@/lib/api'

export const useReceivedInboxStore = defineStore('receivedInbox', () => {
  const emails = ref<ReceivedEmail[]>([])
  const currentEmail = ref<ReceivedEmail | null>(null)
  const counts = ref<InboxCounts | null>(null)
  const isLoading = ref(false)
  const detailLoading = ref(false)
  const isMutating = ref(false)
  const error = ref<string | null>(null)
  const page = ref(1)
  const pageSize = ref(50)
  const total = ref(0)
  const totalPages = ref(0)
  const currentFolder = ref('inbox')
  const currentIdentityId = ref(0)
  const searchQuery = ref('')
  const selectedEmailUuids = ref<string[]>([])
  const sseConnected = ref(false)
  const notice = ref('')
  let filters: InboxListOptions = {}
  let ownerToken: string | null = null
  let listSequence = 0
  let detailSequence = 0
  let countSequence = 0
  let listController: AbortController | undefined
  let detailController: AbortController | undefined
  let activeListKey = ''
  let activeListPromise: Promise<void> | null = null
  let connected = false
  let refreshTimer: ReturnType<typeof setTimeout> | undefined
  let pollTimer: ReturnType<typeof setInterval> | undefined
  let syncGeneration = 0
  let refreshing = false
  let refreshPending = false
  // Cache is memory-only, short lived, and cleared on account changes and mutations.
  const listCache = new Map<string, { at: number; data: InboxListResponse }>()
  const detailCache = new Map<string, ReceivedEmail>()
  const countCache = new Map<number, { at: number; data: InboxCounts }>()
  const countRequests = new Map<number, Promise<InboxCounts>>()
  const unreadCount = computed(() => counts.value?.unread ?? 0)
  const hasMore = computed(() => page.value < totalPages.value)
  const allSelected = computed(() => emails.value.length > 0 && emails.value.every(e => selectedEmailUuids.value.includes(e.uuid)))
  const someSelected = computed(() => selectedEmailUuids.value.length > 0 && !allSelected.value)

  function ensureOwner() {
    const token = api.getToken()
    if (ownerToken !== token) {
      reset()
      ownerToken = token
    }
  }

  function invalidate() {
    listCache.clear()
    detailCache.clear()
    countCache.clear()
  }

  function applyList(data: InboxListResponse) {
    emails.value = data.emails || []
    total.value = data.total || 0
    totalPages.value = data.totalPages || 0
    selectedEmailUuids.value = selectedEmailUuids.value.filter(id => emails.value.some(e => e.uuid === id))
  }

  async function fetchEmails(identityId = currentIdentityId.value, options: InboxListOptions & { reset?: boolean; force?: boolean } = {}) {
    ensureOwner()
    const { reset: resetPage, force, ...nextFilters } = options
    filters = { ...filters, ...nextFilters }
    if (resetPage) {
      filters.page = 1
      selectedEmailUuids.value = []
    }
    currentIdentityId.value = identityId
    currentFolder.value = filters.folder || 'inbox'
    searchQuery.value = filters.search || ''
    page.value = filters.page || 1
    const requestOptions = { ...filters, page: page.value, pageSize: pageSize.value }
    const key = JSON.stringify([identityId, requestOptions])
    if (!force && key === activeListKey && activeListPromise) return activeListPromise
    const sequence = ++listSequence
    listController?.abort()
    activeListKey = ''
    activeListPromise = null
    listController = new AbortController()
    const cached = listCache.get(key)
    if (cached) applyList(cached.data)
    if (!force && cached && Date.now() - cached.at < 15000) {
      isLoading.value = false
      return
    }
    activeListKey = key
    isLoading.value = true
    error.value = null
    const token = ownerToken
    activeListPromise = (async () => {
      try {
        const result = await receivedInboxApi.list(identityId, requestOptions, listController!.signal)
        if (sequence !== listSequence || token !== api.getToken()) return
        listCache.set(key, { at: Date.now(), data: result })
        if (listCache.size > 20) listCache.delete(listCache.keys().next().value!)
        applyList(result)
      } catch (e) {
        if (sequence === listSequence) error.value = e instanceof Error ? e.message : 'Could not load mail. Try again.'
      } finally {
        if (sequence === listSequence) {
          isLoading.value = false
          activeListPromise = null
          activeListKey = ''
        }
      }
    })()
    return activeListPromise
  }

  async function fetchEmail(uuid: string) {
    ensureOwner()
    const sequence = ++detailSequence
    detailController?.abort()
    detailController = new AbortController()
    currentEmail.value = detailCache.get(uuid) || null
    detailLoading.value = !currentEmail.value
    error.value = null
    const token = ownerToken
    try {
      const result = await receivedInboxApi.get(uuid, detailController.signal)
      if (sequence !== detailSequence || token !== api.getToken()) return
      currentEmail.value = result
      detailCache.set(uuid, result)
      if (detailCache.size > 30) detailCache.delete(detailCache.keys().next().value!)
      return result
    } catch (e) {
      if (sequence === detailSequence) error.value = e instanceof Error ? e.message : 'Could not open this message.'
    } finally {
      if (sequence === detailSequence) detailLoading.value = false
    }
  }

  function closeEmail() {
    ++detailSequence
    detailController?.abort()
    currentEmail.value = null
    detailLoading.value = false
  }

  async function fetchCounts(identityId = currentIdentityId.value, force = false) {
    ensureOwner()
    const sequence = ++countSequence
    const token = ownerToken
    const cached = countCache.get(identityId)
    if (!force && cached && Date.now() - cached.at < 15000) {
      counts.value = cached.data
      return
    }
    // A post-event refresh must not reuse counts requested before that event.
    let request = force ? undefined : countRequests.get(identityId)
    try {
      if (!request) {
        request = receivedInboxApi.getCounts(identityId)
        countRequests.set(identityId, request)
      }
      const result = await request
      if (token !== api.getToken() || sequence !== countSequence) return
      countCache.set(identityId, { at: Date.now(), data: result })
      counts.value = result
    } catch {
      // A counts failure must not hide usable mail or fabricate zero counts.
    } finally {
      if (countRequests.get(identityId) === request) countRequests.delete(identityId)
    }
  }

  async function mutate(action: () => Promise<unknown>, uuids: string[], patch?: Partial<ReceivedEmail>, remove = false) {
    if (isMutating.value) return
    isMutating.value = true
    error.value = null
    try {
      await action()
      invalidate()
      if (remove) {
        emails.value = emails.value.filter(e => !uuids.includes(e.uuid))
        selectedEmailUuids.value = selectedEmailUuids.value.filter(id => !uuids.includes(id))
        if (currentEmail.value && uuids.includes(currentEmail.value.uuid)) closeEmail()
      } else {
        emails.value = emails.value.map(e => uuids.includes(e.uuid) ? { ...e, ...patch } : e)
        if (currentEmail.value && uuids.includes(currentEmail.value.uuid)) currentEmail.value = { ...currentEmail.value, ...patch }
      }
      if (remove && !emails.value.length && page.value > 1) filters.page = page.value - 1
      await Promise.all([fetchEmails(currentIdentityId.value, { force: true }), fetchCounts(currentIdentityId.value, true)])
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'The change could not be saved. Try again.'
      throw e
    } finally {
      isMutating.value = false
    }
  }

  const markAsRead = (uuids: string[], isRead: boolean) => mutate(() => receivedInboxApi.mark(uuids, isRead), uuids, { isRead })
  const starEmails = (uuids: string[], isStarred: boolean) => mutate(() => receivedInboxApi.star(uuids, isStarred), uuids, { isStarred })
  const moveEmails = (uuids: string[], folder: string) => mutate(() => receivedInboxApi.move(uuids, folder), uuids, undefined, true)
  const trashEmails = (uuids: string[], permanent = false) => mutate(() => receivedInboxApi.trash(uuids, permanent), uuids, undefined, true)

  function toggleSelect(uuid: string) {
    selectedEmailUuids.value = selectedEmailUuids.value.includes(uuid) ? selectedEmailUuids.value.filter(id => id !== uuid) : [...selectedEmailUuids.value, uuid]
  }
  function selectAll() { selectedEmailUuids.value = allSelected.value ? [] : emails.value.map(e => e.uuid) }
  function clearSelection() { selectedEmailUuids.value = [] }

  function canRefresh() {
    return (typeof document === 'undefined' || !document.hidden) && (typeof navigator === 'undefined' || navigator.onLine !== false)
  }
  function refreshSoon(delay = 300) {
    invalidate()
    refreshPending = true
    if (!connected || !canRefresh() || refreshing || refreshTimer) return
    refreshTimer = setTimeout(() => { void refreshMailbox() }, delay)
  }
  async function refreshMailbox() {
    refreshTimer = undefined
    if (!connected || !canRefresh()) return
    // Let a user action or initial load finish. A trailing refresh then covers
    // events that arrived after that request's database snapshot was taken.
    if (isMutating.value || isLoading.value) { refreshSoon(); return }
    const generation = syncGeneration
    refreshPending = false
    refreshing = true
    await Promise.all([fetchEmails(currentIdentityId.value, { force: true }), fetchCounts(currentIdentityId.value, true)])
    if (generation !== syncGeneration) return
    refreshing = false
    if (refreshPending) refreshSoon()
  }
  function resumeMailbox() { if (canRefresh()) refreshSoon(0) }
  function resumeConnection() {
    if (!connected) return
    disconnectSSE()
    connectSSE()
    resumeMailbox()
  }
  function connectSSE() {
    ensureOwner()
    if (connected) return
    connected = true
    const generation = ++syncGeneration
    const active = () => connected && generation === syncGeneration && ownerToken === api.getToken()
    const changed = () => { if (active()) refreshSoon() }
    inboxSSE.connect({
      // SSE has no replay log. Catch up after both first connect and reconnect,
      // including mail committed while a deployment interrupted the stream.
      onConnected: () => { if (active()) { sseConnected.value = true; refreshSoon() } },
      // Refresh the final folder and all counts, including DMARC Reports.
      // This path intentionally creates no Inbox arrival toast for filed mail.
      onNewEmail: changed,
      onEmailUpdate: changed,
      onEmailDeleted: changed,
      onCountsUpdate: changed,
      onError: () => { if (active()) sseConnected.value = false },
    })
    let pollTicks = 0
    pollTimer = setInterval(() => {
      pollTicks++
      if (canRefresh() && (!sseConnected.value || pollTicks % 4 === 0)) refreshSoon(0)
    }, 15000)
    // A healthy connection is not proof that no event was missed. Reconcile
    // periodically, and catch up immediately when a sleeping/mobile tab returns.
    if (typeof document !== 'undefined') document.addEventListener('visibilitychange', resumeMailbox)
    if (typeof window !== 'undefined') {
      window.addEventListener('focus', resumeMailbox)
      window.addEventListener('online', resumeConnection)
    }
  }
  function disconnectSSE() {
    ++syncGeneration
    inboxSSE.disconnect()
    connected = false
    sseConnected.value = false
    clearTimeout(refreshTimer)
    clearInterval(pollTimer)
    refreshTimer = undefined
    pollTimer = undefined
    refreshing = false
    refreshPending = false
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', resumeMailbox)
    if (typeof window !== 'undefined') {
      window.removeEventListener('focus', resumeMailbox)
      window.removeEventListener('online', resumeConnection)
    }
  }
  function reset() {
    ++listSequence
    ++countSequence
    closeEmail()
    listController?.abort()
    disconnectSSE()
    invalidate()
    countRequests.clear()
    activeListKey = ''
    activeListPromise = null
    emails.value = []
    counts.value = null
    page.value = 1
    total.value = 0
    totalPages.value = 0
    filters = {}
    currentFolder.value = 'inbox'
    currentIdentityId.value = 0
    searchQuery.value = ''
    selectedEmailUuids.value = []
    error.value = null
    notice.value = ''
    isLoading.value = false
  }
  return { emails, currentEmail, counts, isLoading, detailLoading, isMutating, error, page, pageSize, total, totalPages, currentFolder, currentIdentityId, searchQuery, selectedEmailUuids, sseConnected, notice, unreadCount, hasMore, allSelected, someSelected, fetchEmails, fetchEmail, closeEmail, fetchCounts, markAsRead, starEmails, moveEmails, trashEmails, toggleSelect, selectAll, clearSelection, connectSSE, disconnectSSE, invalidate, reset }
})
