<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, Plus, Upload, Download, AlertTriangle, ChevronRight, Check, X, Globe } from 'lucide-vue-next'
import AppLayout from '@/components/layout/AppLayout.vue'
import Button from '@/components/common/Button.vue'
import Badge from '@/components/common/Badge.vue'
import Modal from '@/components/common/Modal.vue'
import { mailboxAdminApi, domainApi, type DomainMailboxes, type MailboxImportRow, type DomainReceivingStatus } from '@/lib/api'
import { receivingProblem, receivingFixLink } from '@/lib/receiving'
import { useDomainsStore } from '@/stores/domains'
import {
  mailboxStatusLabel, mailboxStatusBadge, localPartError, nameError, passwordError, onReceivingDomain,
  mailboxImportTemplate, checkMailboxCsv, importSummary,
} from '@/lib/mailboxes'
import { CSV_MIME } from '@/lib/csv'

const route = useRoute()
const router = useRouter()
const domains = useDomainsStore()
// Two entry points: /domains/:uuid/mailboxes (one domain) and the sidebar's
// /mailboxes, which picks the domain with a switcher kept in ?domain=.
const fixedDomain = computed(() => (route.params.uuid as string | undefined) || '')
const allDomainsMode = computed(() => !fixedDomain.value)
// Mailboxes need an active domain that SES has verified for sending.
const readyDomains = computed(() => domains.domains.filter(d => d.status === 'active' && d.sesVerified))
const selectedDomain = ref((route.query.domain as string | undefined) || '')
const domainUuid = computed(() => fixedDomain.value || selectedDomain.value)
const noReadyDomains = ref(false)
const data = ref<DomainMailboxes | null>(null)
const loading = ref(true)
const error = ref('')
const notice = ref('')
const showRemoved = ref(false)
const domainName = computed(() => data.value?.domain.name ?? '')
// Live MX status: says exactly why mailboxes on this domain would get no mail.
const receiving = ref<DomainReceivingStatus | null>(null)
const receivingReason = computed(() => data.value ? receivingProblem(receiving.value, domainName.value, data.value.domain.receivingEnabled) : '')
const fixLink = computed(() => receivingFixLink(domainUuid.value))
let receivingVersion = 0
async function loadReceiving() {
  const uuid = domainUuid.value
  const version = ++receivingVersion
  receiving.value = null
  if (!uuid) return
  try {
    const result = await domainApi.receivingStatus(uuid)
    if (version === receivingVersion && result.domainUuid === domainUuid.value) receiving.value = result
  } catch {
    // Keep the receivingEnabled fallback; the domain card can re-check.
  }
}

async function load() {
  if (!domainUuid.value) { loading.value = false; return }
  loading.value = true
  error.value = ''
  try {
    data.value = await mailboxAdminApi.list(domainUuid.value, showRemoved.value)
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not load the mailboxes.'
  } finally {
    loading.value = false
  }
}
onMounted(async () => {
  // For the invite sender picker and the receiving-domain warning.
  domains.fetchIdentities()
  if (allDomainsMode.value) {
    if (!domains.domains.length) await domains.fetchDomains()
    const ready = readyDomains.value
    if (!ready.some(d => d.uuid === selectedDomain.value)) selectedDomain.value = ready[0]?.uuid ?? ''
    noReadyDomains.value = !ready.length
    if (selectedDomain.value && route.query.domain !== selectedDomain.value) router.replace({ query: { ...route.query, domain: selectedDomain.value } })
  } else if (!domains.domains.length) domains.fetchDomains()
  load()
  loadReceiving()
})
// Switching domains on /mailboxes keeps the choice in the URL so Back and
// links from the detail page land on the same list.
watch(selectedDomain, (uuid, previous) => {
  if (!allDomainsMode.value || !uuid || uuid === previous) return
  data.value = null
  notice.value = ''
  router.replace({ query: { ...route.query, domain: uuid } })
  load()
  loadReceiving()
})
const formatDate = (value: string | null) => value ? new Date(value).toLocaleDateString() : 'Never'

// ---- Catch-all (Migadu "Catchall Recipients", one inbox per domain) ----
const catchAllEditing = ref(false)
const catchAllDraft = ref('')
const catchAllBusy = ref(false)
const catchAllError = ref('')
function editCatchAll() {
  catchAllDraft.value = data.value?.catchAll?.identityUuid ?? ''
  catchAllError.value = ''
  catchAllEditing.value = true
}
async function saveCatchAll() {
  if (!data.value || catchAllBusy.value) return
  const next = data.value.catchAllOptions.find(o => o.identityUuid === catchAllDraft.value)
  const message = next
    ? `Send mail for every ${domainName.value} address that has no mailbox to ${next.email}? The current catch-all stops getting it; mail already received stays where it is.`
    : `Remove the catch-all for ${domainName.value}? Mail to addresses with no mailbox will no longer be delivered.`
  if (!confirm(message)) return
  catchAllBusy.value = true
  catchAllError.value = ''
  try {
    await mailboxAdminApi.setCatchAll(domainUuid.value, catchAllDraft.value)
    catchAllEditing.value = false
    notice.value = next ? `${next.email} is now the catch-all for ${domainName.value}.` : `${domainName.value} no longer has a catch-all.`
    await load()
  } catch (e) {
    catchAllError.value = e instanceof Error ? e.message : 'Could not change the catch-all.'
  } finally {
    catchAllBusy.value = false
  }
}

// ---- New mailbox ----
const senderIdentities = computed(() => domains.identities.filter(i => !i.shared && i.kind !== 'shared' && i.canSend !== false))
const blankForm = () => ({ localPart: '', name: '', mode: 'invite' as 'invite' | 'password', inviteEmail: '', senderIdentityUuid: '', password: '', maySend: true, mayReceive: true })
const createOpen = ref(false)
const form = ref(blankForm())
const createError = ref('')
const creating = ref(false)
const inviteOnOwnDomain = computed(() => form.value.mode === 'invite' && onReceivingDomain(form.value.inviteEmail, domains.domains))

function openCreate() {
  form.value = blankForm()
  createError.value = ''
  createOpen.value = true
}

async function create() {
  const f = form.value
  const address = `${f.localPart.trim().toLowerCase()}@${domainName.value}`
  createError.value = localPartError(f.localPart) || nameError(f.name)
  if (!createError.value && f.mode === 'invite') {
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(f.inviteEmail.trim())) createError.value = 'Enter the address that should get the setup link.'
    else if (f.inviteEmail.trim().toLowerCase() === address) createError.value = 'The setup link must go to an address outside the new mailbox.'
  }
  if (!createError.value && f.mode === 'password') createError.value = passwordError(f.password)
  if (createError.value) return
  creating.value = true
  try {
    const access = f.mode === 'invite'
      ? { mode: 'invite' as const, inviteEmail: f.inviteEmail.trim(), senderIdentityUuid: f.senderIdentityUuid || undefined }
      : { mode: 'password' as const, password: f.password }
    const result = await mailboxAdminApi.create(domainUuid.value, { localPart: f.localPart.trim(), name: f.name.trim(), access, maySend: f.maySend, mayReceive: f.mayReceive })
    createOpen.value = false
    form.value = blankForm()
    const parts = [f.mode === 'invite'
      ? `${result.mailbox.address} was created. A setup link valid for 72 hours was sent to ${access.mode === 'invite' ? access.inviteEmail : ''}; mail to the address is kept for them meanwhile.`
      : `${result.mailbox.address} was created. Give the user the address and password through a secure channel.`]
    if (result.warnings?.includes('receiving_disabled')) parts.push('Receiving is off for this domain, so the mailbox gets no mail until you set it up.')
    notice.value = parts.join(' ')
    await load()
  } catch (e) {
    createError.value = e instanceof Error ? e.message : 'Could not create the mailbox.'
  } finally {
    creating.value = false
  }
}

// ---- CSV import ----
const importOpen = ref(false)
const importFile = ref('')
const importText = ref('')
const importError = ref('')
const importBusy = ref(false)
const dryRows = ref<MailboxImportRow[] | null>(null)
const resultRows = ref<MailboxImportRow[] | null>(null)
const skipErrors = ref(false)
const drySummary = computed(() => importSummary(dryRows.value ?? []))
const resultSummary = computed(() => importSummary(resultRows.value ?? []))
const canCommit = computed(() => !!dryRows.value && drySummary.value.ok > 0 && (drySummary.value.errors === 0 || skipErrors.value))

function resetImport() {
  importFile.value = ''
  importText.value = '' // may hold passwords: never kept after the modal closes
  importError.value = ''
  dryRows.value = null
  resultRows.value = null
  skipErrors.value = false
}
function openImport() { resetImport(); importOpen.value = true }
function closeImport() {
  const created = resultSummary.value.created > 0
  importOpen.value = false
  resetImport()
  if (created) load()
}

function downloadTemplate() {
  const url = URL.createObjectURL(new Blob([mailboxImportTemplate()], { type: CSV_MIME }))
  const a = document.createElement('a')
  a.href = url
  a.download = `mailboxes-${domainName.value || 'template'}.csv`
  a.click()
  URL.revokeObjectURL(url)
}

async function pickFile(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  resetImport()
  importFile.value = file.name
  const text = await file.text()
  // A bad file is caught here, before anything is uploaded.
  const local = checkMailboxCsv(text, domainName.value)
  if ('error' in local) { importError.value = local.error; return }
  importText.value = text
  await runImport(true)
}

async function runImport(dryRun: boolean) {
  if (!importText.value || importBusy.value) return
  importBusy.value = true
  importError.value = ''
  try {
    const result = await mailboxAdminApi.importCsv(domainUuid.value, importText.value, dryRun)
    if (dryRun) dryRows.value = result.rows
    else { resultRows.value = result.rows; importText.value = '' }
  } catch (e) {
    importError.value = e instanceof Error ? e.message : 'Could not import the file.'
  } finally {
    importBusy.value = false
  }
}
</script>

<template>
  <AppLayout>
    <section class="flex-1 min-w-0 overflow-y-auto p-4 sm:p-6" :aria-busy="loading">
      <router-link v-if="!allDomainsMode" to="/domains" class="inline-flex items-center gap-1 text-sm text-gmail-gray hover:text-gmail-blue mb-4"><ArrowLeft class="w-4 h-4" />Domains</router-link>
      <header class="flex flex-wrap justify-between items-start gap-4 mb-4">
        <div>
          <h1 class="text-2xl font-medium break-all">Mailboxes{{ !allDomainsMode && domainName ? ` · ${domainName}` : '' }}</h1>
          <p v-if="allDomainsMode" class="text-sm text-gmail-gray mt-1">Each mailbox is a login that sees and sends only its own address. Mail to addresses without a mailbox goes to the domain's catch-all.</p>
          <label v-if="allDomainsMode && readyDomains.length" class="mt-3 flex flex-wrap items-center gap-2 text-sm">
            <Globe class="w-4 h-4 text-gmail-gray" aria-hidden="true" />
            <span class="font-medium">Domain</span>
            <select v-model="selectedDomain" class="border border-gmail-border rounded-lg px-2 py-1.5 bg-white min-w-48">
              <option v-for="domain in readyDomains" :key="domain.uuid" :value="domain.uuid">{{ domain.name }}</option>
            </select>
          </label>
          <div v-if="data" class="flex flex-wrap gap-2 mt-2">
            <Badge size="sm" :variant="data.domain.sesVerified ? 'success' : 'error'">{{ data.domain.sesVerified ? 'SES verified' : 'SES not verified' }}</Badge>
            <Badge size="sm" :variant="data.domain.receivingEnabled && !receivingReason ? 'success' : 'warning'">Receiving {{ !data.domain.receivingEnabled ? 'off' : receivingReason ? 'on, MX not ready' : 'on' }}</Badge>
          </div>
        </div>
        <div v-if="data" class="flex flex-wrap items-center gap-2">
          <Button size="sm" @click="openCreate"><Plus class="w-4 h-4" />New mailbox</Button>
          <Button size="sm" variant="secondary" @click="openImport"><Upload class="w-4 h-4" />Import CSV</Button>
          <label class="text-sm text-gmail-gray flex items-center gap-2 ml-2"><input v-model="showRemoved" type="checkbox" @change="load" />Show removed</label>
        </div>
      </header>

      <div v-if="receivingReason" role="status" class="mb-4 p-3 rounded-lg border border-amber-200 bg-amber-50 text-amber-900 text-sm flex flex-wrap items-center gap-2">
        <AlertTriangle class="w-4 h-4 shrink-0" />
        <span class="flex-1 min-w-0">{{ receivingReason }}</span>
        <router-link :to="fixLink" class="font-medium underline">Fix receiving</router-link>
      </div>
      <div v-if="allDomainsMode && noReadyDomains" role="status" class="mb-4 p-4 rounded-lg border border-gmail-border bg-gmail-lightGray text-sm">
        No domain is ready for mailboxes yet. Add a domain and verify it with SES, then come back here.
        <router-link to="/domains" class="ml-1 font-medium text-gmail-blue underline">Go to Domains</router-link>
      </div>
      <div v-if="error" role="alert" class="mb-4 p-3 bg-red-50 text-red-700 rounded-lg text-sm">{{ error }}</div>
      <div v-if="notice" role="status" class="mb-4 p-3 bg-blue-50 text-blue-800 rounded-lg text-sm flex gap-3"><span class="flex-1">{{ notice }}</span><button type="button" class="underline" @click="notice = ''">Dismiss</button></div>
      <p v-if="loading && !data" role="status" class="text-sm text-gmail-gray">Loading mailboxes…</p>

      <template v-if="data">
        <div class="text-sm text-gmail-gray mb-4 break-words">
          <div v-if="!catchAllEditing" class="flex flex-wrap items-center gap-2">
            <span v-if="data.catchAll">Catch-all: <strong class="text-gray-900">{{ data.catchAll.email }}</strong><span v-if="data.catchAll.ownerEmail !== data.catchAll.email"> (owned by {{ data.catchAll.ownerEmail }})</span> → gets mail for addresses with no mailbox.</span>
            <span v-else>No catch-all: mail to addresses with no mailbox is not delivered.</span>
            <Button size="sm" variant="secondary" :disabled="!data.catchAllOptions.length" @click="editCatchAll">{{ data.catchAll ? 'Change' : 'Set catch-all' }}</Button>
          </div>
          <form v-else class="flex flex-wrap items-center gap-2" @submit.prevent="saveCatchAll">
            <label class="flex flex-wrap items-center gap-2"><span class="font-medium text-gray-900">Catch-all inbox</span>
              <select v-model="catchAllDraft" class="border border-gmail-border rounded-lg px-2 py-1.5 bg-white min-w-56">
                <option value="">No catch-all (don't deliver)</option>
                <option v-for="option in data.catchAllOptions" :key="option.identityUuid" :value="option.identityUuid">{{ option.email }}{{ option.isMailbox ? ' (mailbox)' : '' }}</option>
              </select>
            </label>
            <Button size="sm" type="submit" :loading="catchAllBusy" :disabled="catchAllDraft === (data.catchAll?.identityUuid ?? '')">Save</Button>
            <Button size="sm" variant="secondary" @click="catchAllEditing = false">Cancel</Button>
            <span class="basis-full text-xs">Mail to any {{ domainName }} address without a mailbox, identity or alias goes to this inbox.</span>
            <p v-if="catchAllError" role="alert" class="basis-full text-red-700">{{ catchAllError }}</p>
          </form>
        </div>

        <div class="overflow-x-auto border border-gmail-border rounded-lg">
          <table class="w-full text-sm">
            <thead class="bg-gmail-lightGray text-left text-gmail-gray">
              <tr><th class="p-2 font-medium">Address</th><th class="p-2 font-medium">Name</th><th class="p-2 font-medium">Status</th><th class="p-2 font-medium">Send</th><th class="p-2 font-medium">Receive</th><th class="p-2 font-medium">Last sign-in</th><th class="p-2"><span class="sr-only">Open</span></th></tr>
            </thead>
            <tbody class="divide-y">
              <tr v-for="mailbox in data.mailboxes" :key="mailbox.userUuid" :class="mailbox.status === 'removed' ? 'text-gmail-gray' : ''">
                <td class="p-2 break-all"><router-link v-if="mailbox.status !== 'removed'" :to="`/domains/${domainUuid}/mailboxes/${mailbox.userUuid}`" class="font-medium text-gmail-blue hover:underline">{{ mailbox.address }}</router-link><span v-else class="font-medium">{{ mailbox.address }}</span> <Badge v-if="mailbox.isCatchAll" size="sm" variant="warning">Catch-all</Badge></td>
                <td class="p-2">{{ mailbox.name }}</td>
                <td class="p-2"><Badge size="sm" :variant="mailboxStatusBadge[mailbox.status]">{{ mailboxStatusLabel[mailbox.status] }}</Badge></td>
                <td class="p-2"><component :is="mailbox.maySend ? Check : X" :class="['w-4 h-4', mailbox.maySend ? 'text-green-600' : 'text-gray-400']" /><span class="sr-only">{{ mailbox.maySend ? 'Yes' : 'No' }}</span></td>
                <td class="p-2"><component :is="mailbox.mayReceive ? Check : X" :class="['w-4 h-4', mailbox.mayReceive ? 'text-green-600' : 'text-gray-400']" /><span class="sr-only">{{ mailbox.mayReceive ? 'Yes' : 'No' }}</span></td>
                <td class="p-2">{{ formatDate(mailbox.lastLoginAt) }}</td>
                <td class="p-2 text-right">
                  <router-link v-if="mailbox.status !== 'removed'" :to="`/domains/${domainUuid}/mailboxes/${mailbox.userUuid}`" class="inline-flex items-center gap-1 text-gmail-blue hover:underline" :aria-label="`Open ${mailbox.address}`">Open<ChevronRight class="w-4 h-4" /></router-link>
                </td>
              </tr>
              <tr v-if="!data.mailboxes.length"><td colspan="7" class="p-4 text-center text-gmail-gray">No mailboxes yet. Create one, or import a CSV file.</td></tr>
            </tbody>
          </table>
        </div>
      </template>
    </section>

    <Modal :open="createOpen" title="New mailbox" size="lg" @close="createOpen = false">
      <form class="space-y-4 text-sm" @submit.prevent="create">
        <label class="block">Address
          <span class="mt-1 flex items-stretch border rounded overflow-hidden focus-within:ring-1 focus-within:ring-gmail-blue">
            <input v-model="form.localPart" autocomplete="off" maxlength="64" class="flex-1 min-w-0 p-2 outline-none" placeholder="ibrahim" />
            <span class="px-2 flex items-center bg-gmail-lightGray text-gmail-gray break-all">@{{ domainName }}</span>
          </span>
        </label>
        <label class="block">Name<input v-model="form.name" maxlength="255" autocomplete="off" class="mt-1 w-full border rounded p-2" /></label>
        <fieldset class="space-y-2">
          <legend class="font-medium mb-1">Access</legend>
          <label class="flex items-center gap-2"><input v-model="form.mode" type="radio" value="invite" />Invite user to set own password</label>
          <div v-if="form.mode === 'invite'" class="pl-6 space-y-2">
            <label class="block">Send the setup link to<input v-model="form.inviteEmail" type="email" autocomplete="off" class="mt-1 w-full border rounded p-2" placeholder="their.personal@example.com" /></label>
            <p v-if="inviteOnOwnDomain" role="status" class="text-amber-800 bg-amber-50 rounded p-2 text-xs">That address is on one of your receiving domains. Make sure the person can already read it.</p>
            <label v-if="senderIdentities.length > 1" class="block">Send from
              <select v-model="form.senderIdentityUuid" class="mt-1 w-full border rounded p-2 bg-white">
                <option value="">My default identity</option>
                <option v-for="identity in senderIdentities" :key="identity.uuid" :value="identity.uuid">{{ identity.email }}</option>
              </select>
            </label>
            <p class="text-xs text-gmail-gray">The link is valid for 72 hours. Mail to the address is kept for the user meanwhile.</p>
          </div>
          <label class="flex items-center gap-2"><input v-model="form.mode" type="radio" value="password" />Set initial password</label>
          <div v-if="form.mode === 'password'" class="pl-6">
            <input v-model="form.password" type="password" autocomplete="new-password" maxlength="72" aria-label="Initial password" class="w-full border rounded p-2" placeholder="8 to 72 characters" />
            <p class="text-xs text-gmail-gray mt-1">Give the user the address and password through a secure channel.</p>
          </div>
        </fieldset>
        <div class="flex flex-wrap gap-4">
          <label class="flex items-center gap-2"><input v-model="form.maySend" type="checkbox" class="w-4 h-4" />May send</label>
          <label class="flex items-center gap-2"><input v-model="form.mayReceive" type="checkbox" class="w-4 h-4" />May receive</label>
        </div>
        <p v-if="receivingReason" role="status" class="text-amber-900 bg-amber-50 border border-amber-200 rounded p-2 text-xs">
          This domain cannot receive mail yet: {{ receivingReason }} You can still create the mailbox; it gets mail once that is fixed.
          <router-link :to="fixLink" class="font-medium underline">Fix receiving</router-link>
        </p>
        <p v-if="createError" role="alert" class="text-red-700">{{ createError }}</p>
        <div class="flex justify-end gap-2">
          <Button variant="secondary" @click="createOpen = false">Cancel</Button>
          <Button type="submit" :loading="creating">Create mailbox</Button>
        </div>
      </form>
    </Modal>

    <Modal :open="importOpen" title="Import mailboxes" size="full" @close="closeImport">
      <div class="space-y-4 text-sm">
        <p class="text-gmail-gray">A UTF-8 CSV file with a header row: <code>local_part</code> (or <code>address</code>), <code>name</code>, and exactly one of <code>invite_email</code> or <code>password</code> per row; <code>may_send</code> and <code>may_receive</code> are optional (true or false). At most 200 rows and 1 MiB.</p>
        <div class="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="secondary" @click="downloadTemplate"><Download class="w-4 h-4" />Download template</Button>
          <label class="inline-flex items-center gap-2 px-3 py-1.5 rounded-lg border border-gmail-border hover:bg-gmail-hover cursor-pointer font-medium text-gmail-gray">
            <Upload class="w-4 h-4" />Choose file
            <input type="file" accept=".csv,text/csv" class="sr-only" :disabled="importBusy" @change="pickFile" />
          </label>
          <span v-if="importFile" class="text-gmail-gray break-all">{{ importFile }}</span>
        </div>
        <p v-if="importBusy" role="status" class="text-gmail-gray">{{ resultRows || dryRows ? 'Creating mailboxes…' : 'Checking the file…' }}</p>
        <p v-if="importError" role="alert" class="p-3 bg-red-50 text-red-700 rounded-lg">{{ importError }}</p>

        <template v-if="resultRows">
          <p role="status" class="p-3 rounded-lg" :class="resultSummary.errors ? 'bg-amber-50 text-amber-900' : 'bg-green-50 text-green-800'">{{ resultSummary.created }} created, {{ resultSummary.errors }} not created.</p>
        </template>
        <template v-else-if="dryRows">
          <p role="status" class="p-3 rounded-lg" :class="drySummary.errors ? 'bg-amber-50 text-amber-900' : 'bg-green-50 text-green-800'">Dry run: {{ drySummary.ok }} ready, {{ drySummary.errors }} with errors. Nothing has been created yet.</p>
        </template>
        <div v-if="resultRows || dryRows" class="overflow-x-auto border border-gmail-border rounded-lg max-h-80 overflow-y-auto">
          <table class="w-full">
            <thead class="bg-gmail-lightGray text-left text-gmail-gray sticky top-0"><tr><th class="p-2 font-medium">Line</th><th class="p-2 font-medium">Address</th><th class="p-2 font-medium">Result</th></tr></thead>
            <tbody class="divide-y">
              <tr v-for="row in (resultRows ?? dryRows ?? [])" :key="row.line">
                <td class="p-2">{{ row.line }}</td>
                <td class="p-2 break-all">{{ row.address }}</td>
                <td class="p-2">
                  <Badge size="sm" :variant="row.result === 'error' ? 'error' : 'success'">{{ row.result === 'ok' ? 'Ready' : row.result === 'created' ? 'Created' : 'Error' }}</Badge>
                  <span v-if="row.message" class="ml-2 text-red-700">{{ row.message }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <label v-if="dryRows && !resultRows && drySummary.errors && drySummary.ok" class="flex items-center gap-2"><input v-model="skipErrors" type="checkbox" class="w-4 h-4" />Skip the {{ drySummary.errors }} row{{ drySummary.errors === 1 ? '' : 's' }} with errors and create the rest</label>
        <div class="flex justify-end gap-2">
          <Button variant="secondary" @click="closeImport">{{ resultRows ? 'Done' : 'Cancel' }}</Button>
          <Button v-if="!resultRows" :disabled="!canCommit" :loading="importBusy && !!dryRows" @click="runImport(false)">Create {{ drySummary.ok }} mailbox{{ drySummary.ok === 1 ? '' : 'es' }}</Button>
        </div>
      </div>
    </Modal>
  </AppLayout>
</template>
