<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount, nextTick } from 'vue'
import { useEditor, EditorContent } from '@tiptap/vue-3'
import StarterKit from '@tiptap/starter-kit'
import ImageExtension from '@tiptap/extension-image'
import DOMPurify from 'dompurify'
import { X, Minus, Maximize2, Minimize2, Bold, Italic, Underline, Link2, Image, Paperclip, Trash2, Send, Save } from 'lucide-vue-next'
import { useInboxStore } from '@/stores/inbox'
import { useReceivedInboxStore } from '@/stores/receivedInbox'
import { useDomainsStore } from '@/stores/domains'
import { composeApi, receivedInboxApi, type ComposeAttachment, type ComposeRequest, type Identity, type SendResult } from '@/lib/api'
import { replyRecipients, replySender, prefixedSubject, escapeHtml, plainAddress, composeThreadHeaders, failedRetryPayload, retainSendAttempt, senderAllowed, senderSuggestions, senderHint, bodyWithSignature } from '@/lib/compose'
import { useAuthStore } from '@/stores/auth'
import { isOrgAdmin } from '@/lib/api'
import { renderMessageDocument, signatureHtml } from '@/lib/mailHtml'

const inboxStore = useInboxStore()
const mailbox = useReceivedInboxStore()
const domainsStore = useDomainsStore()
const isOpen = computed(() => inboxStore.isComposeOpen)
const identities = computed(() => domainsStore.identities.filter(i => i.canSend !== false))
const authStore = useAuthStore()
// Owners and admins may use any free address on the domain; members and
// mailbox users follow the send-as rule (identity, +tag, aliases, wildcard).
const restrictAlias = computed(() => !isOrgAdmin(authStore.user))
const selectedIdentity = computed(() => identities.value.find(i => Number(i.id) === Number(selectedIdentityId.value)))
const fromAllowed = (identity: Identity, from: string) => senderAllowed(identity, from, domainsStore.identities)
const aliasError = computed(() => restrictAlias.value && selectedIdentity.value && fromEmail.value.trim() && !fromAllowed(selectedIdentity.value, fromEmail.value)
  ? `Send as ${selectedIdentity.value.email}, a +tag of it${selectedIdentity.value.sendAliases?.length ? ', or one of your send-as addresses' : ''}.` : '')
const fromSuggestions = computed(() => senderSuggestions(selectedIdentity.value))
const fromHint = computed(() => restrictAlias.value ? senderHint(selectedIdentity.value) : "You can use an alias on this identity's verified domain.")
const to = ref('')
const cc = ref('')
const bcc = ref('')
const subject = ref('')
const body = ref('')
const html = ref('')
const selectedIdentityId = ref<number>(0)
const fromEmail = ref('')
const attachments = ref<ComposeAttachment[]>([])
const isMinimized = ref(false)
const isFullscreen = ref(false)
const isSending = ref(false)
const isSaving = ref(false)
const readingFiles = ref(false)
const initialized = ref(false)
const error = ref('')
const saveMessage = ref('')
const quoteNotice = ref('')
const showCcBcc = ref(false)
const draftId = ref('')
const draftVersion = ref<number | undefined>()
const sendState = ref<SendResult['status'] | 'network' | ''>('')
const submissionKey = ref('')
const submissionEmailId = ref('')
const attemptUncertain = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const recipientInput = ref<HTMLInputElement | null>(null)
let submissionPayload: ComposeRequest | null = null
let savePromise: Promise<boolean> | null = null
let saveTimer: ReturnType<typeof setTimeout> | undefined
const savedSnapshot = ref('')
// The body as compose opened it (signature and quote). The signature follows
// the identity only while the body still matches it.
const pristineHtml = ref('')
let quotedHtml = ''
let composeGeneration = 0
const locked = computed(() => isSending.value || !!submissionPayload)
const editor = useEditor({
  extensions: [StarterKit.configure({ link: { openOnClick: false } }), ImageExtension],
  content: '',
  editorProps: { attributes: { class: 'prose prose-sm max-w-none min-h-[180px] outline-none p-3', 'aria-label': 'Message body', role: 'textbox', 'aria-multiline': 'true' } },
  onUpdate: ({ editor }) => { body.value = editor.getText(); html.value = editor.getHTML() },
})
watch(locked, value => editor.value?.setEditable(!value))
const snapshot = computed(() => JSON.stringify([selectedIdentityId.value, fromEmail.value, to.value, cc.value, bcc.value, subject.value, html.value, attachments.value]))
const dirty = computed(() => initialized.value && snapshot.value !== savedSnapshot.value)
const hasContent = computed(() => !!(to.value || cc.value || bcc.value || subject.value || (body.value.trim() && html.value !== pristineHtml.value) || attachments.value.length))
const title = computed(() => inboxStore.composeMode === 'forward' ? 'Forward' : inboxStore.composeMode.startsWith('reply') ? 'Reply' : 'New message')
const sendLabel = computed(() => ['unknown', 'sending', 'network'].includes(sendState.value) ? 'Check send status' : sendState.value === 'failed' ? 'Retry send' : 'Send')

function requestBody(): ComposeRequest {
  const addresses = (value: string) => value.split(/[,;]/).map(plainAddress).filter(Boolean).map(email => ({ name: '', email }))
  const original = inboxStore.replyToEmail
  return {
    identityId: Number(selectedIdentityId.value), fromEmail: fromEmail.value.trim(),
    to: addresses(to.value), cc: addresses(cc.value), bcc: addresses(bcc.value),
    subject: subject.value, textBody: body.value, htmlBody: html.value,
    ...composeThreadHeaders(inboxStore.composeMode, original),
    attachments: attachments.value.map(({ size: _size, ...attachment }) => attachment),
  }
}

watch(isOpen, async open => {
  clearTimeout(saveTimer)
  const generation = ++composeGeneration
  if (!open) { initialized.value = false; return }
  initialized.value = false
  error.value = ''; saveMessage.value = ''; sendState.value = ''; quoteNotice.value = ''
  submissionPayload = null; submissionKey.value = ''; submissionEmailId.value = ''; attemptUncertain.value = false; draftId.value = ''; draftVersion.value = undefined
  to.value = ''; cc.value = ''; bcc.value = ''; subject.value = ''; body.value = ''; html.value = ''; attachments.value = []
  pristineHtml.value = ''; quotedHtml = ''
  isMinimized.value = false; showCcBcc.value = false
  await domainsStore.fetchIdentities()
  if (generation !== composeGeneration || !isOpen.value) return
  const original = inboxStore.replyToEmail
  const selected = original && inboxStore.composeMode !== 'forward' ? replySender(original, identities.value) : undefined
  const identity = selected?.identity || identities.value.find(i => i.isDefault) || identities.value[0]
  selectedIdentityId.value = identity ? Number(identity.id) : 0
  fromEmail.value = selected?.fromEmail || identity?.email || ''
  if (identity && restrictAlias.value && !fromAllowed(identity, fromEmail.value)) fromEmail.value = identity.email
  let content = ''
  if (original) {
    if (inboxStore.composeMode === 'draft') {
      draftId.value = original.uuid; draftVersion.value = original.draftVersion
      fromEmail.value = original.from.email
      to.value = original.to.map(a => a.email).join(', ')
      cc.value = original.cc?.map(a => a.email).join(', ') || ''
      bcc.value = original.bcc?.map(a => a.email).join(', ') || ''
      subject.value = original.subject
      content = original.htmlBody || `<p>${escapeHtml(original.body).replace(/\n/g, '<br>')}</p>`
    } else {
      const forward = inboxStore.composeMode === 'forward'
      if (!forward) {
        const recipients = replyRecipients(original, domainsStore.identities, inboxStore.composeMode === 'replyAll')
        to.value = recipients.to.join(', '); cc.value = recipients.cc.join(', ')
      }
      subject.value = prefixedSubject(original.subject, forward ? 'Fwd' : 'Re')
      // Quote through the remote-image policy so TipTap never loads trackers
      // in the app origin: blocked images become "[image: alt]" text.
      let originalHtml = `<p>${escapeHtml(original.body).replace(/\n/g, '<br>')}</p>`
      if (original.htmlBody) {
        const quoted = renderMessageDocument(original.htmlBody, { allowRemote: !!original.remoteImagesAllowed, mode: 'quote' })
        originalHtml = quoted.doc
        if (quoted.remoteCount > 0) quoteNotice.value = 'Remote images were left out. Show images in the message first to include them.'
      }
      const lead = forward ? `Forwarded message — From: ${original.from.email}` : `On ${new Date(original.receivedAt).toLocaleString()}, ${original.from.name || original.from.email} wrote:`
      quotedHtml = `<p>${escapeHtml(lead)}</p><blockquote>${originalHtml}</blockquote>`
    }
    if (inboxStore.composeMode === 'forward' || inboxStore.composeMode === 'draft') {
      attachments.value = (original.sourceAttachments || []).map(a => ({ blobId: a.uuid, name: a.filename, type: a.contentType, size: a.sizeBytes }))
    }
  }
  if (inboxStore.composeMode !== 'draft') content = bodyWithSignature(signatureHtml(identity), quotedHtml)
  await nextTick()
  setBody(content)
  if (!body.value && original) body.value = original.body || ''
  showCcBcc.value = !!(cc.value || bcc.value)
  savedSnapshot.value = snapshot.value
  initialized.value = true
  await nextTick()
  recipientInput.value?.focus()
}, { immediate: true })

watch(snapshot, () => {
  if (!initialized.value || !isOpen.value || locked.value) return
  saveMessage.value = dirty.value ? 'Unsaved changes' : 'Draft saved'
  clearTimeout(saveTimer)
  if (dirty.value && hasContent.value) saveTimer = setTimeout(() => { void saveDraft() }, 1500)
})

async function saveDraft(): Promise<boolean> {
  if (savePromise) { await savePromise; return dirty.value ? saveDraft() : true }
  if (readingFiles.value) return false
  if (!hasContent.value || !dirty.value || locked.value) return true
  if (!selectedIdentityId.value) { error.value = 'Add a sending identity in Domains before saving.'; return false }
  const generation = composeGeneration
  const saved = snapshot.value
  const payload = requestBody()
  isSaving.value = true; error.value = ''; saveMessage.value = 'Saving draft…'
  savePromise = (async () => {
    try {
      const result = draftId.value ? await composeApi.updateDraft(draftId.value, { ...payload, version: draftVersion.value }) : await composeApi.saveDraft(payload)
      if (generation !== composeGeneration) return false
      draftId.value = result.id; draftVersion.value = result.version
      savedSnapshot.value = saved
      saveMessage.value = snapshot.value === saved ? 'Draft saved' : 'Unsaved changes'
      mailbox.invalidate()
      void mailbox.fetchCounts(mailbox.currentIdentityId, true)
      if (mailbox.currentFolder === 'drafts') void mailbox.fetchEmails(mailbox.currentIdentityId, { force: true })
      return true
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Could not save your draft. Your message is still here.'
      saveMessage.value = 'Draft not saved'
      return false
    } finally { isSaving.value = false; savePromise = null }
  })()
  return savePromise
}

async function closeCompose() {
  clearTimeout(saveTimer)
  if (submissionPayload || !hasContent.value || await saveDraft()) inboxStore.closeCompose()
}
async function discard() {
  if (!confirm('Permanently discard this draft?')) return
  clearTimeout(saveTimer)
  if (savePromise) await savePromise
  try {
    if (draftId.value && !submissionPayload) await composeApi.deleteDraft(draftId.value)
    mailbox.invalidate(); inboxStore.closeCompose()
    void mailbox.fetchCounts(mailbox.currentIdentityId, true)
    if (mailbox.currentFolder === 'drafts') void mailbox.fetchEmails(mailbox.currentIdentityId, { force: true })
  } catch (e) { error.value = e instanceof Error ? e.message : 'Could not discard draft' }
}
async function send() {
  if (isSending.value || readingFiles.value) return
  isSending.value = true; error.value = ''
  clearTimeout(saveTimer)
  try {
    if (savePromise) await savePromise
    if (sendState.value === 'failed') {
      if (!confirm('The previous send failed. Start a new send attempt?')) return
      try {
        if (!submissionEmailId.value || !submissionPayload) throw new Error('The failed attempt is not available. Review its saved copy in Outbox.')
        const savedAttempt = await receivedInboxApi.get(submissionEmailId.value)
        submissionPayload = failedRetryPayload(submissionPayload, savedAttempt)
        attachments.value = (savedAttempt.attachments || []).map(a => ({ blobId: a.uuid, name: a.filename, type: a.contentType, size: a.sizeBytes }))
        submissionKey.value = crypto.randomUUID()
        submissionEmailId.value = ''; attemptUncertain.value = false; sendState.value = ''
        draftId.value = ''; draftVersion.value = undefined
      } catch (e) {
        error.value = e instanceof Error ? e.message : 'Could not load the failed attempt. Your original send key is preserved.'
        return
      }
    }
    if (!submissionPayload) {
      const payload = requestBody()
      const recipients = [...payload.to, ...(payload.cc || []), ...(payload.bcc || [])]
      if (!payload.identityId || !payload.to.length || recipients.some(a => !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(a.email))) {
        error.value = 'Select a sender and enter valid recipient email addresses separated by commas.'; return
      }
      if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(payload.fromEmail || '')) { error.value = 'Enter a valid From address on your verified domain.'; return }
      if (aliasError.value) { error.value = aliasError.value; return }
      submissionPayload = { ...payload, draftId: draftId.value || undefined, draftVersion: draftVersion.value }
      submissionKey.value = crypto.randomUUID()
    }
    const result = await composeApi.send(submissionPayload, submissionKey.value)
    sendState.value = result.status
    submissionEmailId.value = result.emailId
    if (result.status === 'unknown' || result.status === 'sending') attemptUncertain.value = true
    mailbox.invalidate()
    void mailbox.fetchEmails(mailbox.currentIdentityId, { force: true })
    void mailbox.fetchCounts(mailbox.currentIdentityId, true)
    // A status check can observe a later delivery event. These are completed
    // attempts too; preserve their outcome in Sent instead of offering another send.
    if (['sent', 'delivered', 'bounced', 'complained'].includes(result.status)) {
      mailbox.notice = result.status === 'delivered'
        ? 'Message delivered. The saved copy is in Sent.'
        : result.status === 'bounced' || result.status === 'complained'
          ? `The recorded send was ${result.status}. Review its saved copy in Sent.`
          : 'Message accepted by SES. The saved copy is in Sent; delivery is tracked separately.'
      inboxStore.closeCompose()
    } else {
      error.value = result.sendError || (result.status === 'failed' ? 'Send failed. The message is preserved in Outbox.' : 'Send confirmation is pending. The message is preserved in Outbox. Check status before trying a new send.')
    }
  } catch (e) {
    const status = (e as { status?: number }).status
    if (retainSendAttempt(status, attemptUncertain.value)) {
      attemptUncertain.value = true
      sendState.value = 'network'
      error.value = `${e instanceof Error ? e.message : 'Connection lost'}. Check send status to safely retry this same submission.`
    } else {
      submissionPayload = null; submissionKey.value = ''; submissionEmailId.value = ''; sendState.value = ''
      error.value = e instanceof Error ? e.message : 'Check the message details and try again.'
    }
  } finally { isSending.value = false }
}

async function addFiles(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files || [])
  const total = files.reduce((sum, f) => sum + f.size, attachments.value.reduce((sum, a) => sum + (a.size || 0), 0))
  if (total > 10 * 1024 * 1024) { error.value = 'Attachments must total 10 MB or less.'; input.value = ''; return }
  readingFiles.value = true
  try {
    for (const file of files) {
      const content = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader()
        reader.onload = () => resolve(String(reader.result).split(',')[1])
        reader.onerror = () => reject(new Error(`Could not read ${file.name}`))
        reader.readAsDataURL(file)
      })
      attachments.value.push({ name: file.name, type: file.type || 'application/octet-stream', content, size: file.size })
    }
  } catch (e) { error.value = e instanceof Error ? e.message : 'Could not attach file' }
  finally { readingFiles.value = false; input.value = '' }
}
function setBody(content: string) {
  content = DOMPurify.sanitize(content)
  editor.value?.commands.setContent(content, { emitUpdate: false })
  html.value = editor.value?.getHTML() || content
  body.value = editor.value?.getText() || ''
  pristineHtml.value = html.value
}
function identityChanged() {
  fromEmail.value = selectedIdentity.value?.email || ''
  // Swap the signature only while the body is still as compose opened it.
  if (inboxStore.composeMode !== 'draft' && html.value === pristineHtml.value) setBody(bodyWithSignature(signatureHtml(selectedIdentity.value), quotedHtml))
}
function insertLink() {
  const url = prompt('Link URL (https://…)')
  if (url && /^https?:\/\//i.test(url)) editor.value?.chain().focus().extendMarkRange('link').setLink({ href: url }).run()
}
function insertImage() {
  const url = prompt('Image URL (https://…)')
  if (url && /^https:\/\//i.test(url)) editor.value?.chain().focus().setImage({ src: url }).run()
}
function beforeUnload(event: BeforeUnloadEvent) { if (isOpen.value && (dirty.value || isSaving.value)) { event.preventDefault(); event.returnValue = '' } }
window.addEventListener('beforeunload', beforeUnload)
onBeforeUnmount(() => { clearTimeout(saveTimer); editor.value?.destroy(); window.removeEventListener('beforeunload', beforeUnload) })
</script>

<template>
  <Teleport to="body">
    <section v-if="isOpen" role="dialog" aria-label="Compose email" :aria-busy="isSending || isSaving" :class="['fixed z-50 bg-white shadow-2xl border border-gray-200 rounded-t-xl flex flex-col max-w-full', isMinimized ? 'bottom-0 right-0 sm:right-6 w-72' : isFullscreen ? 'inset-2 sm:inset-6' : 'inset-x-0 bottom-0 h-[min(680px,100dvh)] sm:left-auto sm:right-6 sm:w-[640px]']">
      <header class="flex items-center justify-between px-4 py-2 bg-gmail-gray text-white rounded-t-xl">
        <button @click="isMinimized = false" class="text-sm font-medium">{{ title }}</button>
        <div class="flex gap-1">
          <button @click="isMinimized = !isMinimized" class="p-2 rounded hover:bg-gray-600" aria-label="Minimize composer"><Minus class="w-4 h-4" /></button>
          <button @click="isFullscreen = !isFullscreen; isMinimized = false" class="p-2 rounded hover:bg-gray-600" aria-label="Toggle composer size"><component :is="isFullscreen ? Minimize2 : Maximize2" class="w-4 h-4" /></button>
          <button @click="closeCompose" :disabled="isSending" class="p-2 rounded hover:bg-gray-600" aria-label="Save draft and close"><X class="w-4 h-4" /></button>
        </div>
      </header>
      <template v-if="!isMinimized">
        <p v-if="!initialized" role="status" class="px-4 py-2 text-sm text-gray-500">Loading sender identities…</p>
        <p v-if="error" role="alert" class="bg-red-50 text-red-700 text-sm px-4 py-3">{{ error }}</p>
        <p v-if="quoteNotice" role="status" class="bg-gray-50 text-gray-700 text-xs px-4 py-2 flex gap-2"><span class="flex-1">{{ quoteNotice }}</span><button type="button" @click="quoteNotice = ''" aria-label="Dismiss notice"><X class="w-3 h-3" /></button></p>
        <p v-if="!identities.length && initialized" class="px-4 py-3 text-sm bg-amber-50">Add a sending identity on a verified domain in <RouterLink to="/domains" class="underline">Domains</RouterLink>.</p>
        <fieldset :disabled="locked" class="min-h-0 flex-1 flex flex-col disabled:opacity-70">
          <div class="p-3 border-b space-y-2">
            <label class="flex items-center gap-3 text-sm"><span class="w-12 shrink-0 text-gray-500">Identity</span><select v-model="selectedIdentityId" @change="identityChanged" class="flex-1 min-w-0 bg-white border rounded p-1"><option v-for="identity in identities" :key="identity.id" :value="Number(identity.id)">{{ identity.shared ? identity.sharedMailboxName || identity.displayName : identity.displayName }} &lt;{{ identity.email }}&gt;{{ identity.shared ? ' (shared)' : '' }}</option></select></label>
            <label class="flex items-center gap-3 text-sm"><span class="w-12 shrink-0 text-gray-500">From</span><input v-model="fromEmail" type="email" list="compose-from-options" class="flex-1 min-w-0 p-1 border-b" aria-label="Sender email or alias" :aria-invalid="!!aliasError" aria-describedby="compose-from-hint" /></label>
            <datalist id="compose-from-options"><option v-for="address in fromSuggestions" :key="address" :value="address" /></datalist>
            <p id="compose-from-hint" :class="['text-xs pl-16', aliasError ? 'text-red-700' : 'text-gray-500']">{{ aliasError || fromHint }}</p>
          </div>
          <div class="px-3 py-2 border-b space-y-2">
            <div class="flex items-center gap-3 text-sm"><label for="compose-to" class="w-12 shrink-0 text-gray-500">To</label><input id="compose-to" ref="recipientInput" v-model="to" class="flex-1 min-w-0 outline-none" placeholder="Recipients, separated by commas" /><button type="button" @click="showCcBcc = !showCcBcc" class="text-blue-600">Cc/Bcc</button></div>
            <template v-if="showCcBcc"><label class="flex items-center gap-3 text-sm"><span class="w-12 text-gray-500">Cc</span><input v-model="cc" class="flex-1 min-w-0 outline-none" /></label><label class="flex items-center gap-3 text-sm"><span class="w-12 text-gray-500">Bcc</span><input v-model="bcc" class="flex-1 min-w-0 outline-none" /></label></template>
          </div>
          <label class="px-3 py-2 border-b text-sm"><span class="sr-only">Subject</span><input v-model="subject" placeholder="Subject" class="w-full outline-none" /></label>
          <div class="flex-1 min-h-0 overflow-auto"><EditorContent :editor="editor" /></div>
          <div v-if="attachments.length || readingFiles" class="px-3 py-2 border-t max-h-28 overflow-auto">
            <p v-if="readingFiles" role="status" class="text-xs text-gray-500">Reading attachments…</p>
            <div v-for="(attachment, index) in attachments" :key="index" class="flex items-center gap-2 text-xs py-1"><Paperclip class="w-3 h-3 shrink-0" /><span class="truncate">{{ attachment.name }}</span><span class="text-gray-500">{{ Math.ceil((attachment.size || 0) / 1024) }} KB</span><button @click="attachments.splice(index, 1)" class="ml-auto p-1" :aria-label="`Remove ${attachment.name}`"><X class="w-3 h-3" /></button></div>
          </div>
          <div class="flex gap-1 border-t px-3 py-1">
            <button type="button" @click="editor?.chain().focus().toggleBold().run()" title="Bold" class="p-2 hover:bg-gray-100 rounded"><Bold class="w-4 h-4" /></button>
            <button type="button" @click="editor?.chain().focus().toggleItalic().run()" title="Italic" class="p-2 hover:bg-gray-100 rounded"><Italic class="w-4 h-4" /></button>
            <button type="button" @click="editor?.chain().focus().toggleUnderline().run()" title="Underline" class="p-2 hover:bg-gray-100 rounded"><Underline class="w-4 h-4" /></button>
            <button type="button" @click="insertLink" title="Insert link" class="p-2 hover:bg-gray-100 rounded"><Link2 class="w-4 h-4" /></button>
            <button type="button" @click="insertImage" title="Insert image URL" class="p-2 hover:bg-gray-100 rounded"><Image class="w-4 h-4" /></button>
            <button type="button" @click="fileInput?.click()" title="Attach files" class="p-2 hover:bg-gray-100 rounded"><Paperclip class="w-4 h-4" /></button>
            <input ref="fileInput" type="file" multiple class="hidden" @change="addFiles" />
          </div>
        </fieldset>
        <footer class="flex flex-wrap items-center gap-2 p-3 border-t">
          <button @click="send" :disabled="isSending || readingFiles || !initialized" class="flex items-center gap-2 bg-gmail-blue text-white px-4 py-2 rounded-full disabled:opacity-50"><Send class="w-4 h-4" />{{ isSending ? 'Sending…' : sendLabel }}</button>
          <button v-if="!submissionPayload" @click="saveDraft" :disabled="isSaving || !dirty || readingFiles" class="p-2 rounded hover:bg-gray-100 disabled:opacity-40" title="Save draft"><Save class="w-4 h-4" /></button>
          <span role="status" class="text-xs text-gray-500 flex-1">{{ saveMessage }}</span>
          <button v-if="!submissionPayload" @click="discard" :disabled="isSending" class="p-2 rounded hover:bg-gray-100" title="Discard draft"><Trash2 class="w-4 h-4" /></button>
        </footer>
      </template>
    </section>
  </Teleport>
</template>
