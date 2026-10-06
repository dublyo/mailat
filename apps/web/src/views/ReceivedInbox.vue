<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Archive, Trash2, Mail, MailOpen, RefreshCw, ChevronLeft, ChevronRight, Star, Filter, X, Reply, ReplyAll, Forward, ArrowLeft, Inbox, AlertTriangle, Paperclip, FileText, ImageOff, Ban } from 'lucide-vue-next'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useReceivedInboxStore } from '@/stores/receivedInbox'
import { useInboxStore } from '@/stores/inbox'
import { useDomainsStore } from '@/stores/domains'
import { useSettingsStore } from '@/stores/settings'
import { api, trustedSendersApi, type ReceivedEmail, type Email, type InboxListOptions, type ReceivedEmailAttachment } from '@/lib/api'
import { renderMessageDocument } from '@/lib/mailHtml'

const route = useRoute()
const router = useRouter()
const mailbox = useReceivedInboxStore()
const composer = useInboxStore()
const domains = useDomainsStore()
const settingsStore = useSettingsStore()
const showFilters = ref(false)
const selectedUuid = ref('')
const inlineUrls = ref<Record<string, string>>({})
const downloading = ref('')
let inlineSequence = 0
// Messages whose remote images the user chose to show, in memory only.
const revealed = ref(new Set<string>())
const trusting = ref(false)
const filterForm = ref({ identity: '', domain: '', read: '', starred: '', attachments: '', sender: '', after: '', before: '' })
const folder = computed(() => String(route.query.folder || route.params.folder || 'inbox'))
const folderTitle = computed(() => ({ 'dmarc-reports': 'DMARC Reports', all: 'All Mail', inbox: 'Inbox', starred: 'Starred', sent: 'Sent', drafts: 'Drafts', outbox: 'Outbox', archive: 'Archive', spam: 'Spam', trash: 'Trash' })[folder.value] || folder.value)
const identityId = computed(() => Number(route.query.identity) || 0)
const current = computed(() => mailbox.currentEmail)
const selectedIndex = computed(() => mailbox.emails.findIndex(e => e.uuid === selectedUuid.value))
const actionIds = computed(() => selectedUuid.value ? [selectedUuid.value] : mailbox.selectedEmailUuids)
const allActionStarred = computed(() => actionIds.value.length > 0 && actionIds.value.every(id => (current.value?.uuid === id ? current.value : mailbox.emails.find(e => e.uuid === id))?.isStarred))
const queryOptions = computed<InboxListOptions>(() => ({
  folder: folder.value, domainId: Number(route.query.domain) || undefined,
  search: String(route.query.q || ''), sender: String(route.query.sender || ''),
  isRead: route.query.read === 'read' ? true : route.query.read === 'unread' ? false : undefined,
  isStarred: route.query.starred === 'true' ? true : undefined,
  hasAttachments: route.query.attachments === 'true' ? true : route.query.attachments === 'false' ? false : undefined,
  dateFrom: String(route.query.after || ''), dateTo: String(route.query.before || ''),
  page: Number(route.query.page) || 1,
}))
const chips = computed(() => Object.entries(route.query).filter(([key, value]) => ['q', 'identity', 'domain', 'read', 'starred', 'attachments', 'sender', 'after', 'before'].includes(key) && value).map(([key, value]) => ({ key, label: key === 'identity' ? domains.identities.find(i => String(i.id) === value)?.email || String(value) : key === 'domain' ? domains.domains.find(d => String(d.id) === value)?.name || String(value) : key === 'q' ? `Search: ${value}` : key === 'starred' ? 'Starred' : key === 'attachments' ? (value === 'true' ? 'With attachments' : 'Without attachments') : `${key}: ${value}` })))
const range = computed(() => mailbox.total ? `${(mailbox.page - 1) * mailbox.pageSize + 1}–${Math.min(mailbox.page * mailbox.pageSize, mailbox.total)} of ${mailbox.total}` : '0 messages')
const remoteAllowed = computed(() => !!current.value && (current.value.remoteImages === 'allowed' || revealed.value.has(current.value.uuid)))
const rendered = computed(() => {
  if (!current.value?.htmlBody?.trim()) return { doc: '', remoteCount: 0 }
  const cidUrls: Record<string, string> = {}
  for (const attachment of current.value.attachments || []) {
    if (attachment.contentId && inlineUrls.value[attachment.uuid]) cidUrls[attachment.contentId.replace(/[<>]/g, '')] = inlineUrls.value[attachment.uuid]
  }
  return renderMessageDocument(current.value.htmlBody, { inlineUrls: cidUrls, allowRemote: remoteAllowed.value, mode: 'view' })
})
const sanitizedHtml = computed(() => rendered.value.doc)
const showRemoteBanner = computed(() => !remoteAllowed.value && rendered.value.remoteCount > 0)
// Only DMARC-authenticated senders outside Spam can be trusted for images.
const canTrustSender = computed(() => !!current.value && (current.value.dmarcVerdict || '').toUpperCase() === 'PASS' && current.value.folder !== 'spam' && !!current.value.fromEmail)
const canBlockSender = computed(() => !!current.value?.fromEmail && current.value.direction !== 'outbound' && !['sent', 'drafts', 'outbox'].includes(current.value.folder))
function showImages() {
  if (current.value) revealed.value = new Set([...revealed.value, current.value.uuid])
}
async function trustSender() {
  const email = current.value
  if (!email || trusting.value) return
  trusting.value = true
  try {
    await trustedSendersApi.add(email.fromEmail.toLowerCase())
    email.remoteImages = 'allowed'
    email.trustedSender = true
    mailbox.notice = `Images from ${email.fromEmail} will be shown automatically.`
  } catch (e) { mailbox.error = e instanceof Error ? e.message : 'Could not trust this sender.' }
  finally { trusting.value = false }
}
async function blockSender() {
  const email = current.value
  if (!email || !canBlockSender.value || mailbox.isMutating) return
  if (!confirm(`Block ${email.fromEmail}? Future messages from this address go to Spam.`)) return
  const blocked = await settingsStore.blockSender(email.fromEmail)
  if (!blocked) { mailbox.error = settingsStore.rulesError || 'Could not block this sender.'; return }
  if (email.folder !== 'spam') await perform('spam')
  mailbox.notice = `${email.fromEmail} is blocked. Manage blocked senders in Settings.`
}
watch(() => current.value?.uuid, async () => {
  const sequence = ++inlineSequence
  Object.values(inlineUrls.value).forEach(URL.revokeObjectURL)
  inlineUrls.value = {}
  await Promise.all((current.value?.attachments || []).filter(a => a.isInline && a.contentId && a.downloadUrl).map(async attachment => {
    try {
      const blob = await api.download(attachment.downloadUrl!)
      if (sequence === inlineSequence) inlineUrls.value[attachment.uuid] = URL.createObjectURL(blob)
    } catch { /* The attachment remains available for an explicit retry/download. */ }
  }))
})
async function downloadAttachment(attachment: ReceivedEmailAttachment) {
  if (!attachment.downloadUrl) { mailbox.error = 'Attachment is not available for download.'; return }
  downloading.value = attachment.uuid
  try {
    const blob = await api.download(attachment.downloadUrl)
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url; anchor.download = attachment.filename; anchor.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch (e) { mailbox.error = e instanceof Error ? e.message : 'Attachment download failed.' }
  finally { downloading.value = '' }
}

async function load(force = false) {
  await Promise.all([mailbox.fetchEmails(identityId.value, { ...queryOptions.value, force }), mailbox.fetchCounts(identityId.value, force)])
}
// ?message=<uuid> (a push notification click) opens that message once; the
// parameter is dropped so later navigation does not reopen it.
let pendingMessage = ''
watch(() => route.fullPath, () => {
  const deepLink = typeof route.query.message === 'string' ? route.query.message : ''
  if (deepLink) {
    pendingMessage = deepLink
    const { message: _message, ...query } = route.query
    void router.replace({ path: route.path, query })
    return
  }
  closeEmail()
  mailbox.clearSelection()
  for (const key of Object.keys(filterForm.value) as (keyof typeof filterForm.value)[]) filterForm.value[key] = String(route.query[key] || '')
  void load()
  if (pendingMessage) {
    const uuid = pendingMessage
    pendingMessage = ''
    void openEmail({ uuid } as ReceivedEmail)
  }
}, { immediate: true })
// A message deleted in another tab or device closes here too.
watch(() => mailbox.currentEmail, (email) => {
  if (!email && selectedUuid.value && !mailbox.detailLoading && !mailbox.error) selectedUuid.value = ''
})
function showNewMail() {
  mailbox.newMailPill = false
  void router.replace({ query: { ...route.query, page: undefined } })
  if (mailbox.page <= 1) void load(true)
}
onMounted(() => {
  void Promise.all([domains.fetchIdentities(), domains.fetchDomains()])
  mailbox.connectSSE()
})
onUnmounted(() => { ++inlineSequence; Object.values(inlineUrls.value).forEach(URL.revokeObjectURL); mailbox.disconnectSSE(); mailbox.closeEmail() })

function updateQuery(values: Record<string, string | undefined>) {
  const query = { ...route.query, ...values, page: undefined }
  for (const key of Object.keys(query)) if (query[key as keyof typeof query] === '') delete query[key as keyof typeof query]
  void router.replace({ path: '/received', query })
}
function clearFilters() { void router.replace({ path: '/received', query: folder.value === 'inbox' ? {} : { folder: folder.value } }) }
function applyFilters() { updateQuery(filterForm.value); showFilters.value = false }
function closeEmail() { selectedUuid.value = ''; mailbox.closeEmail() }
function convert(email: ReceivedEmail): Email {
  return { id: String(email.id), uuid: email.uuid, messageId: email.messageId, subject: email.subject,
    from: { name: email.fromName, email: email.fromEmail }, to: (email.toEmails || []).map(email => ({ email })),
    cc: (email.ccEmails || []).map(email => ({ email })), bcc: (email.bccEmails || []).map(email => ({ email })),
    body: email.textBody || '', htmlBody: email.htmlBody, snippet: email.snippet || '', folder: email.folder,
    isRead: email.isRead, isStarred: email.isStarred, hasAttachments: email.hasAttachments,
    receivedAt: email.receivedAt, createdAt: email.createdAt, identityId: email.identityId,
    replyToAddress: email.replyTo, inReplyTo: email.inReplyTo, references: email.references, envelopeRecipients: email.envelopeRecipients,
    draftVersion: email.draftVersion ?? email.version, sourceAttachments: email.attachments,
    remoteImagesAllowed: email.remoteImages === 'allowed' || revealed.value.has(email.uuid) }
}
async function openEmail(email: ReceivedEmail) {
  selectedUuid.value = email.uuid
  const detail = await mailbox.fetchEmail(email.uuid)
  if (!detail || selectedUuid.value !== email.uuid) return
  if (detail.folder === 'drafts') {
    if (composer.isComposeOpen) { mailbox.notice = 'Close or save your current composition before opening another draft.'; return }
    composer.openCompose('draft', convert(detail))
  } else if (!detail.isRead) {
    try { await mailbox.markAsRead([detail.uuid], true) } catch { /* Store exposes the error without hiding the message. */ }
  }
}
function compose(mode: 'reply' | 'replyAll' | 'forward' | 'draft') {
  if (!current.value) return
  if (composer.isComposeOpen) { mailbox.notice = 'Close or save your current composition first.'; return }
  composer.openCompose(mode, convert(current.value))
}
async function perform(action: 'read' | 'unread' | 'star' | 'unstar' | 'archive' | 'spam' | 'restore' | 'trash' | 'dmarc-reports') {
  const ids = [...actionIds.value]
  if (!ids.length || mailbox.isMutating) return
  const permanent = action === 'trash' && folder.value === 'trash'
  if (permanent && !confirm(`Permanently delete ${ids.length} message${ids.length === 1 ? '' : 's'}? This cannot be undone.`)) return
  const index = selectedIndex.value
  const next = mailbox.emails[index + 1] || mailbox.emails[index - 1]
  try {
    if (action === 'read' || action === 'unread') await mailbox.markAsRead(ids, action === 'read')
    else if (action === 'star' || action === 'unstar') await mailbox.starEmails(ids, action === 'star')
    else if (action === 'trash') await mailbox.trashEmails(ids, permanent)
    else await mailbox.moveEmails(ids, action === 'restore' ? 'inbox' : action)
    mailbox.clearSelection()
    if (mailbox.page !== (Number(route.query.page) || 1)) void router.replace({ query: { ...route.query, page: String(mailbox.page) } })
    if (['archive', 'spam', 'restore', 'trash', 'dmarc-reports'].includes(action)) {
      if (selectedUuid.value && next && mailbox.emails.some(e => e.uuid === next.uuid)) await openEmail(next)
      else closeEmail()
      mailbox.notice = permanent ? 'Messages permanently deleted.' : action === 'restore' ? 'Messages restored to Inbox.' : `Messages moved to ${action === 'trash' ? 'Trash' : action === 'dmarc-reports' ? 'DMARC Reports' : action}.`
    } else if (action === 'unread') closeEmail()
  } catch { /* Keep selection so a failed operation is easy to retry. */ }
}
async function toggleStar(email: ReceivedEmail) { try { await mailbox.starEmails([email.uuid], !email.isStarred) } catch {} }
function navigate(direction: number) {
  if (selectedUuid.value) {
    const email = mailbox.emails[selectedIndex.value + direction]
    if (email) void openEmail(email)
  } else void router.replace({ query: { ...route.query, page: String(mailbox.page + direction) } })
}
function formatDate(value: string, full = false) {
  return new Intl.DateTimeFormat(undefined, full ? { dateStyle: 'medium', timeStyle: 'short' } : { month: 'short', day: 'numeric' }).format(new Date(value))
}
</script>

<template>
  <AppLayout>
    <section class="flex-1 min-w-0 flex flex-col h-full bg-white" :aria-busy="mailbox.isLoading || mailbox.isMutating" aria-label="Email mailbox">
      <div class="flex flex-wrap items-center gap-2 px-3 py-2 border-b bg-white">
        <button v-if="selectedUuid" @click="closeEmail" class="mail-action" title="Back to message list"><ArrowLeft class="w-5 h-5" /></button>
        <label v-else class="flex items-center gap-2 p-2 text-xs"><input type="checkbox" :checked="mailbox.allSelected" :indeterminate="mailbox.someSelected" @change="mailbox.selectAll" aria-label="Select all messages on this page" class="w-4 h-4" /><span v-if="mailbox.selectedEmailUuids.length">{{ mailbox.selectedEmailUuids.length }} selected</span></label>
        <h1 v-if="!actionIds.length" class="text-base font-medium capitalize mr-1">{{ folderTitle }}</h1>
        <div v-if="actionIds.length" class="flex items-center gap-1 flex-wrap">
          <button v-if="!['inbox', 'sent', 'drafts', 'outbox'].includes(folder)" @click="perform('restore')" :disabled="mailbox.isMutating" class="mail-action" title="Move to Inbox" aria-label="Move to Inbox"><Inbox class="w-4 h-4" /></button>
          <button v-if="!['archive', 'drafts', 'outbox'].includes(folder)" @click="perform('archive')" :disabled="mailbox.isMutating" class="mail-action" title="Archive"><Archive class="w-4 h-4" /></button>
          <button v-if="!['dmarc-reports', 'sent', 'drafts', 'outbox'].includes(folder)" @click="perform('dmarc-reports')" :disabled="mailbox.isMutating" class="mail-action" title="Move to DMARC Reports" aria-label="Move to DMARC Reports"><FileText class="w-4 h-4" /></button>
          <button @click="perform('trash')" :disabled="mailbox.isMutating" class="mail-action" :title="folder === 'trash' ? 'Delete permanently' : 'Move to Trash'"><Trash2 class="w-4 h-4" /></button>
          <button @click="perform('read')" :disabled="mailbox.isMutating" class="mail-action" title="Mark as read"><MailOpen class="w-4 h-4" /></button>
          <button @click="perform('unread')" :disabled="mailbox.isMutating" class="mail-action" title="Mark as unread"><Mail class="w-4 h-4" /></button>
          <button @click="perform(allActionStarred ? 'unstar' : 'star')" :disabled="mailbox.isMutating" class="mail-action" :title="allActionStarred ? 'Remove star' : 'Star'"><Star :class="['w-4 h-4', allActionStarred ? 'fill-yellow-400 text-yellow-500' : '']" /></button>
          <button v-if="folder !== 'spam'" @click="perform('spam')" :disabled="mailbox.isMutating" class="mail-action" title="Move to Spam"><AlertTriangle class="w-4 h-4" /></button>
          <button v-if="selectedUuid && canBlockSender" @click="blockSender" :disabled="mailbox.isMutating" class="mail-action" title="Block sender" aria-label="Block sender"><Ban class="w-4 h-4" /></button>
        </div>
        <button v-else @click="load(true)" :disabled="mailbox.isLoading" class="mail-action" title="Refresh messages"><RefreshCw :class="['w-4 h-4', mailbox.isLoading ? 'animate-spin' : '']" /></button>
        <div class="flex-1" />
        <button v-if="!selectedUuid" @click="showFilters = !showFilters" :aria-expanded="showFilters" aria-controls="mail-filters" class="flex items-center gap-2 px-3 py-2 border rounded-lg text-sm hover:bg-gray-50"><Filter class="w-4 h-4" />Filters<span v-if="chips.length" class="rounded-full bg-blue-100 text-blue-700 px-1.5 text-xs">{{ chips.length }}</span></button>
        <div class="flex items-center text-xs text-gray-500 gap-1"><span class="hidden sm:inline mr-1">{{ selectedUuid ? `${selectedIndex + 1} of ${mailbox.emails.length}` : range }}</span><button @click="navigate(-1)" :disabled="selectedUuid ? selectedIndex <= 0 : mailbox.page <= 1" class="mail-action" aria-label="Previous"><ChevronLeft class="w-4 h-4" /></button><button @click="navigate(1)" :disabled="selectedUuid ? selectedIndex >= mailbox.emails.length - 1 : !mailbox.hasMore" class="mail-action" aria-label="Next"><ChevronRight class="w-4 h-4" /></button></div>
      </div>
      <form v-if="showFilters && !selectedUuid" id="mail-filters" @submit.prevent="applyFilters" class="p-4 border-b bg-gray-50 grid grid-cols-2 lg:grid-cols-4 gap-3 text-sm">
        <label>Identity<select v-model="filterForm.identity" class="mail-filter"><option value="">All identities</option><option v-for="identity in domains.identities" :key="identity.id" :value="String(identity.id)">{{ identity.email }}</option></select></label>
        <label>Domain<select v-model="filterForm.domain" class="mail-filter"><option value="">All domains</option><option v-for="domain in domains.domains" :key="domain.id" :value="String(domain.id)">{{ domain.name }}</option></select></label>
        <label>Read status<select v-model="filterForm.read" class="mail-filter"><option value="">Any</option><option value="unread">Unread</option><option value="read">Read</option></select></label>
        <label>Attachments<select v-model="filterForm.attachments" class="mail-filter"><option value="">Any</option><option value="true">Has attachments</option><option value="false">No attachments</option></select></label>
        <label>Sender<input v-model="filterForm.sender" placeholder="name@example.com" class="mail-filter" /></label>
        <label>From date<input v-model="filterForm.after" type="date" class="mail-filter" /></label>
        <label>Through date<input v-model="filterForm.before" type="date" class="mail-filter" /></label>
        <label class="flex items-center gap-2 self-center"><input v-model="filterForm.starred" type="checkbox" true-value="true" false-value="" />Starred only</label>
        <div class="col-span-2 lg:col-span-4 flex gap-3"><button type="submit" class="px-4 py-2 rounded-full bg-gmail-blue text-white">Apply filters</button><button type="button" @click="clearFilters" class="px-3 py-2 text-gray-600">Clear all</button></div>
      </form>
      <div v-if="chips.length && !selectedUuid" class="flex flex-wrap gap-2 px-3 py-2 border-b" aria-label="Active filters"><button v-for="chip in chips" :key="chip.key" @click="updateQuery({ [chip.key]: undefined })" class="inline-flex items-center gap-1 rounded-full bg-blue-50 text-blue-700 text-xs px-3 py-1" :aria-label="`Remove ${chip.label}`">{{ chip.label }}<X class="w-3 h-3" /></button><button v-if="route.query.q && folder !== 'all'" @click="updateQuery({ folder: 'all' })" class="text-xs text-blue-600 underline">Search All Mail</button><button @click="clearFilters" class="text-xs text-gray-500 underline">Clear all</button></div>
      <div v-if="mailbox.error" role="alert" class="px-4 py-3 text-sm bg-red-50 text-red-700 flex gap-3"><span class="flex-1">{{ mailbox.error }}</span><button @click="selectedUuid ? mailbox.fetchEmail(selectedUuid) : load(true)" class="underline">Retry</button></div>
      <div v-if="mailbox.notice" role="status" class="px-4 py-2 text-sm bg-blue-50 text-blue-800 flex gap-3"><span class="flex-1">{{ mailbox.notice }}</span><button @click="mailbox.notice = ''" aria-label="Dismiss message"><X class="w-4 h-4" /></button></div>
      <div v-if="mailbox.newMailPill && !selectedUuid" class="flex justify-center py-2 border-b"><button type="button" class="rounded-full bg-blue-600 text-white text-sm px-4 py-1 shadow" @click="showNewMail">New messages · Show</button></div>
      <div v-if="mailbox.isLoading" class="h-0.5 bg-blue-100 overflow-hidden"><div class="w-1/3 h-full bg-blue-500 animate-pulse" /></div>
      <div class="flex-1 flex min-h-0 overflow-hidden">
        <div :class="['overflow-y-auto min-w-0', selectedUuid ? 'hidden lg:block w-80 shrink-0 border-r' : 'flex-1']">
          <div v-if="mailbox.isLoading && !mailbox.emails.length" class="p-4 space-y-4" role="status" aria-label="Loading messages"><div v-for="n in 8" :key="n" class="h-12 bg-gray-100 rounded animate-pulse" /></div>
          <div v-else-if="!mailbox.emails.length" class="h-full min-h-60 flex flex-col items-center justify-center p-6 text-center"><Mail class="w-12 h-12 text-gray-300 mb-4" /><h2 class="text-lg font-medium">{{ chips.length ? 'No messages match these filters' : `No messages in ${folderTitle}` }}</h2><p class="text-sm text-gray-500 mt-2">{{ chips.length ? 'Try a different search or clear your filters.' : folder === 'dmarc-reports' ? 'Reports appear here when your domain’s existing DMARC policy requests reports and Mailat receives them. Automatic organization does not change your DNS or enable receiving.' : folder === 'drafts' ? 'Saved drafts appear here when you compose a message.' : folder === 'sent' ? 'Messages accepted by SES are saved here.' : 'Mail for all your identities appears together here.' }}</p><button v-if="chips.length" @click="clearFilters" class="text-blue-600 text-sm mt-4">Clear filters</button></div>
          <ul v-else class="divide-y divide-gray-100">
            <li v-for="email in mailbox.emails" :key="email.uuid" :class="['group flex items-center gap-2 px-3 py-3 sm:py-2.5 border-l-4', selectedUuid === email.uuid ? 'bg-blue-100 border-blue-500' : email.isRead ? 'bg-white border-transparent hover:bg-gray-50' : 'bg-blue-50/60 border-transparent']">
              <input v-if="!selectedUuid" type="checkbox" :checked="mailbox.selectedEmailUuids.includes(email.uuid)" @change="mailbox.toggleSelect(email.uuid)" :aria-label="`Select ${email.subject || 'message'}`" class="w-4 h-4 shrink-0" />
              <button @click="toggleStar(email)" :disabled="mailbox.isMutating" class="p-1 shrink-0" :aria-label="email.isStarred ? 'Remove star' : 'Star message'"><Star :class="['w-4 h-4', email.isStarred ? 'fill-yellow-400 text-yellow-500' : 'text-gray-300']" /></button>
              <button @click="openEmail(email)" class="flex-1 min-w-0 text-left focus-visible:outline-blue-500 rounded">
                <div class="flex items-center gap-2 leading-5">
                  <span :class="['truncate flex-1 text-sm', !email.isRead ? 'font-semibold' : 'text-gray-600']">{{ ['sent', 'drafts', 'outbox'].includes(folder) ? `To: ${(email.toEmails || []).join(', ') || '(no recipients)'}` : email.fromName || email.fromEmail }}</span>
                  <span v-if="identityId === 0" class="hidden sm:inline-flex items-center gap-1 max-w-[30%] text-[10px] text-gray-500 rounded bg-gray-100 px-1.5 leading-4" :title="email.identityEmail"><span class="w-1.5 h-1.5 rounded-full shrink-0" :style="{ backgroundColor: email.identityColor || '#9ca3af' }" /><span class="truncate">{{ email.identityEmail }}</span></span>
                  <span class="text-xs text-gray-500 shrink-0">{{ formatDate(email.receivedAt || email.createdAt) }}</span>
                </div>
                <div class="flex gap-2 items-center mt-0.5 leading-5 min-w-0">
                  <span :class="['text-sm truncate', !selectedUuid ? 'sm:max-w-[55%] sm:shrink-0' : '', !email.isRead ? 'font-medium' : 'text-gray-700']">{{ email.subject || '(no subject)' }}</span>
                  <span v-if="folder !== 'dmarc-reports' && email.folder === 'dmarc-reports'" class="text-[10px] text-gray-600 rounded bg-gray-100 px-1.5 shrink-0">DMARC Reports</span>
                  <Paperclip v-if="email.hasAttachments" class="w-3 h-3 shrink-0 text-gray-400" />
                  <span v-if="email.sendStatus && email.sendStatus !== 'received'" class="text-[10px] text-gray-500 shrink-0">{{ email.sendStatus }}</span>
                  <span v-if="!selectedUuid" class="hidden sm:block text-xs text-gray-500 truncate min-w-0">— {{ email.snippet || (email.hasAttachments ? 'Content is in the attachment.' : '') }}</span>
                </div>
                <p class="sm:hidden text-xs text-gray-500 truncate mt-1">{{ email.snippet || (email.hasAttachments ? 'Content is in the attachment.' : '') }}</p>
                <p v-if="identityId === 0" class="sm:hidden text-[10px] text-gray-400 truncate mt-1"><span class="inline-block w-1.5 h-1.5 rounded-full mr-1" :style="{ backgroundColor: email.identityColor || '#9ca3af' }" />{{ email.identityEmail }}</p>
              </button>
            </li>
          </ul>
        </div>
        <article v-if="selectedUuid" class="flex-1 min-w-0 flex flex-col overflow-hidden">
          <div v-if="mailbox.detailLoading && !current" role="status" class="p-6 animate-pulse space-y-4"><div class="h-7 w-2/3 bg-gray-100 rounded" /><div class="h-40 bg-gray-100 rounded" /></div>
          <template v-else-if="current">
            <header class="px-4 sm:px-6 py-4 border-b break-words"><h2 class="text-xl mb-3">{{ current.subject || '(no subject)' }}</h2><div class="flex flex-wrap items-start justify-between gap-2 text-sm"><div class="min-w-0"><p class="font-medium break-all">{{ current.fromName }} &lt;{{ current.fromEmail }}&gt;</p><p class="text-gray-500 break-all mt-1">To: {{ current.toEmails?.join(', ') }}</p><p v-if="current.ccEmails?.length" class="text-gray-500 break-all">Cc: {{ current.ccEmails.join(', ') }}</p><p v-if="current.replyTo" class="text-gray-500 break-all">Reply to: {{ current.replyTo }}</p></div><time class="text-xs text-gray-500">{{ formatDate(current.receivedAt || current.createdAt, true) }}</time></div><p v-if="current.sendStatus !== 'received' && (current.sendStatus || current.deliveryStatus)" class="text-xs text-gray-500 mt-3">Send: {{ current.sendStatus || 'accepted' }}<span v-if="current.deliveryStatus"> · Delivery: {{ current.deliveryStatus }}</span></p></header>
            <div class="flex-1 min-h-0 overflow-y-auto">
              <div v-if="showRemoteBanner" role="status" class="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 sm:px-6 py-2 text-sm bg-gray-50 border-b">
                <span class="flex items-center gap-2 text-gray-700"><ImageOff class="w-4 h-4 shrink-0" />Remote images are hidden to protect your privacy.</span>
                <button @click="showImages" class="text-blue-600 hover:underline">Show images</button>
                <button v-if="canTrustSender" @click="trustSender" :disabled="trusting" class="text-blue-600 hover:underline break-all disabled:opacity-50">Always show from {{ current.fromEmail }}</button>
              </div>
              <iframe v-if="sanitizedHtml" :srcdoc="sanitizedHtml" sandbox="allow-popups allow-popups-to-escape-sandbox" title="Email message" class="w-full min-h-[50vh] border-0 bg-white" referrerpolicy="no-referrer" />
              <div v-else class="p-4 sm:p-6 text-sm whitespace-pre-wrap break-words leading-relaxed">{{ current.textBody?.trim() ? current.textBody : current.attachments?.length ? 'This message has no text body. Its content is in the attachment below.' : 'This message has no text body.' }}</div>
              <div v-if="current.attachments?.length" class="p-4 border-t"><h3 class="text-sm font-medium mb-2">Attachments</h3><div class="flex flex-wrap gap-2"><button v-for="attachment in current.attachments" :key="attachment.uuid" @click="downloadAttachment(attachment)" :disabled="downloading === attachment.uuid" class="max-w-full flex items-center gap-2 text-sm border rounded-lg p-2 hover:bg-gray-50"><Paperclip class="w-4 h-4 shrink-0" /><span class="truncate">{{ attachment.filename }}</span><span class="text-xs text-gray-500 shrink-0">{{ downloading === attachment.uuid ? 'Downloading…' : `${Math.ceil(attachment.sizeBytes / 1024)} KB` }}</span></button></div></div>
            </div>
            <footer class="flex flex-wrap gap-2 px-4 py-3 border-t bg-gray-50"><template v-if="current.folder !== 'drafts'"><button @click="compose('reply')" class="mail-reply"><Reply class="w-4 h-4" />Reply</button><button @click="compose('replyAll')" class="mail-reply"><ReplyAll class="w-4 h-4" />Reply all</button><button @click="compose('forward')" class="mail-reply"><Forward class="w-4 h-4" />Forward</button></template><button v-else @click="compose('draft')" class="mail-reply"><FileText class="w-4 h-4" />Edit draft</button></footer>
          </template>
        </article>
      </div>
      <p class="sm:hidden text-xs text-gray-500 py-1 px-3 border-t">{{ range }}</p>
    </section>
  </AppLayout>
</template>

<style scoped>
.mail-action { @apply p-2 rounded-full hover:bg-gray-100 text-gray-600 disabled:opacity-30 disabled:cursor-not-allowed focus-visible:outline-blue-500; }
.mail-filter { @apply block w-full min-w-0 mt-1 px-2 py-2 rounded-lg border border-gray-300 bg-white; }
.mail-reply { @apply inline-flex items-center gap-2 px-4 py-2 rounded-full border bg-white text-sm hover:bg-gray-100; }
</style>
