<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, Trash2, Plus, RotateCw, KeyRound, Mail, ShieldOff, PauseCircle, PlayCircle } from 'lucide-vue-next'
import AppLayout from '@/components/layout/AppLayout.vue'
import Button from '@/components/common/Button.vue'
import Badge from '@/components/common/Badge.vue'
import Modal from '@/components/common/Modal.vue'
import { mailboxAdminApi, orgApi, domainApi, type MailboxDetail, type OrgMember, type DomainReceivingStatus } from '@/lib/api'
import { receivesMailLabel, receivingFixLink } from '@/lib/receiving'
import { mailboxStatusLabel, mailboxStatusBadge, localPartError, nameError, passwordError, overviewRows } from '@/lib/mailboxes'

const route = useRoute()
const router = useRouter()
const domainUuid = route.params.uuid as string
const userUuid = route.params.userUuid as string
const listPath = `/mailboxes?domain=${encodeURIComponent(domainUuid)}`

const detail = ref<MailboxDetail | null>(null)
const loading = ref(true)
const busy = ref('')
const error = ref('')
const notice = ref('')

type Section = 'overview' | 'settings' | 'send-as' | 'access' | 'remove'
const sections: { id: Section; label: string }[] = [
  { id: 'overview', label: 'Overview' }, { id: 'settings', label: 'Settings' }, { id: 'send-as', label: 'Send-as' },
  { id: 'access', label: 'Access' }, { id: 'remove', label: 'Remove' },
]
const section = ref<Section>('overview')

const mailbox = computed(() => detail.value?.mailbox)
const domainName = computed(() => detail.value?.domain.name ?? '')
const status = computed(() => mailbox.value?.status)
const pending = computed(() => status.value === 'invited' || status.value === 'invite_expired')
const rows = computed(() => detail.value ? overviewRows(detail.value.overview) : [])
// Whether mail actually arrives: the mailbox switch plus the domain's live MX.
const receiving = ref<DomainReceivingStatus | null>(null)
const receivingFailed = ref(false)
const receivesMail = computed(() => {
  if (!detail.value) return null
  if (receivingFailed.value && detail.value.overview.mayReceive) return { value: 'Unknown — could not check the domain MX', ok: false }
  return receivesMailLabel(receiving.value, detail.value.overview.mayReceive)
})
const fixLink = receivingFixLink(domainUuid)
async function loadReceiving() {
  try {
    const result = await domainApi.receivingStatus(domainUuid)
    if (result.domainUuid === domainUuid) receiving.value = result
  } catch {
    receivingFailed.value = true
  }
}

const nameDraft = ref('')
const recoveryDraft = ref('')
const aliasDraft = ref('')

async function load() {
  error.value = ''
  try {
    detail.value = await mailboxAdminApi.get(userUuid)
    nameDraft.value = detail.value.mailbox.name
    recoveryDraft.value = detail.value.overview.recoveryEmail
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not load the mailbox.'
  } finally {
    loading.value = false
  }
}
onMounted(() => { load(); loadReceiving() })

async function run(key: string, action: () => Promise<string | void>, fallback: string) {
  if (busy.value) return
  busy.value = key
  error.value = ''
  notice.value = ''
  try {
    const message = await action()
    if (message) notice.value = message
    await load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : fallback
  } finally {
    busy.value = ''
  }
}
const formatDate = (value?: string | null) => value ? new Date(value).toLocaleString() : 'Never'

// ---- Settings ----
function saveName() {
  const err = nameError(nameDraft.value)
  if (err) { error.value = err; return }
  return run('name', async () => { await mailboxAdminApi.update(userUuid, { name: nameDraft.value.trim() }); return 'Name saved.' }, 'Could not save the name.')
}
const switchWarnings: Record<string, string> = {
  'mayReceive:false': 'Stop delivering to this mailbox? New mail to the address goes to the catch-all, or is not delivered when there is none.',
  'maySend:false': 'Stop this mailbox from sending? Compose, auto-replies and forward confirmations from it will fail.',
  'wildcardSender:true': 'Let this user send as any unused address on the domain? Other people\'s addresses and aliases stay blocked.',
}
function setSwitch(key: 'maySend' | 'mayReceive' | 'wildcardSender', event: Event) {
  const box = event.target as HTMLInputElement
  const value = box.checked
  const warning = switchWarnings[`${key}:${value}`]
  // A cancelled or failed change puts the box back; the saved value is shown after load().
  if (warning && !confirm(warning)) { box.checked = !value; return }
  return run(key, async () => { await mailboxAdminApi.update(userUuid, { [key]: value }) }, 'Could not save the change.')
    .then(() => { if (detail.value) box.checked = detail.value.overview[key] })
}

// ---- Send-as ----
function addAlias() {
  const err = localPartError(aliasDraft.value)
  if (err) { error.value = err; return }
  return run('alias', async () => {
    const alias = await mailboxAdminApi.addAlias(userUuid, aliasDraft.value.trim())
    aliasDraft.value = ''
    return `${alias.address} can now be used to send, and mail to it reaches this mailbox.`
  }, 'Could not add the alias.')
}
const removeAlias = (alias: { uuid: string; address: string }) => confirm(`Remove ${alias.address}? New mail to it goes to the catch-all.`) &&
  run(alias.uuid, async () => { await mailboxAdminApi.deleteAlias(userUuid, alias.uuid) }, 'Could not remove the alias.')

// ---- Access ----
function saveRecovery() {
  const value = recoveryDraft.value.trim()
  if (value && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)) { error.value = 'Enter a valid recovery email, or leave it empty.'; return }
  return run('recovery', async () => {
    await mailboxAdminApi.update(userUuid, { recoveryEmail: value })
    return value ? 'Recovery email saved. The previous address was told.' : 'Recovery email cleared.'
  }, 'Could not save the recovery email.')
}
const resendInvite = () => run('resend', async () => {
  const { invite } = await mailboxAdminApi.resendInvite(userUuid)
  return `A new setup link was sent; it works until ${formatDate(invite.expiresAt)}. The previous link no longer works.`
}, 'Could not resend the setup link.')
const resetTwoFactor = () => confirm('Turn off two-factor sign-in for this user? They are signed out everywhere and should set it up again.') &&
  run('2fa', async () => { await mailboxAdminApi.resetTwoFactor(userUuid); return 'Two-factor sign-in was turned off and every session ended.' }, 'Could not reset two-factor sign-in.')
const suspend = () => confirm('Suspend this mailbox? The user is signed out and cannot sign in, forwards pause and auto-replies stop. Mail keeps arriving.') &&
  run('suspend', async () => { await mailboxAdminApi.suspend(userUuid); return 'Mailbox suspended.' }, 'Could not suspend the mailbox.')
const reactivate = () => run('reactivate', async () => {
  await mailboxAdminApi.reactivate(userUuid)
  return 'Mailbox reactivated. Forwards stay paused until the user turns them back on.'
}, 'Could not reactivate the mailbox.')

const passwordMode = ref<'' | 'set' | 'link'>('')
const newPassword = ref('')
const confirmPassword = ref('')
const linkEmail = ref('')
const passwordError_ = ref('')
function openPassword(mode: 'set' | 'link') {
  passwordMode.value = mode
  newPassword.value = ''
  confirmPassword.value = ''
  linkEmail.value = ''
  passwordError_.value = ''
}
function closePassword() {
  passwordMode.value = ''
  newPassword.value = ''
  confirmPassword.value = ''
}
async function submitPassword() {
  passwordError_.value = ''
  if (passwordMode.value === 'set') {
    passwordError_.value = passwordError(newPassword.value) || (newPassword.value !== confirmPassword.value ? 'The passwords do not match.' : '')
    if (passwordError_.value) return
    const password = newPassword.value
    closePassword()
    await run('password', async () => {
      const result = await mailboxAdminApi.setPassword(userUuid, password)
      return `Password set. ${result.sessionsRevoked} session${result.sessionsRevoked === 1 ? '' : 's'} signed out. Give the user the new password through a secure channel.`
    }, 'Could not set the password.')
    return
  }
  const email = linkEmail.value.trim()
  if (!email && !detail.value?.overview.recoveryEmail) { passwordError_.value = 'There is no recovery email; enter where the link should go.'; return }
  if (email && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) { passwordError_.value = 'Enter a valid email address.'; return }
  closePassword()
  await run('password', async () => {
    const { invite } = await mailboxAdminApi.sendResetLink(userUuid, email || undefined)
    return `A reset link was sent to ${email || detail.value?.overview.recoveryEmail}; it works until ${formatDate(invite.expiresAt)}.`
  }, 'Could not send the reset link.')
}

// ---- Remove ----
const members = ref<OrgMember[]>([])
const transferTo = ref('')
async function loadMembers() {
  try { members.value = ((await orgApi.members()) ?? []).filter(m => m.status === 'active') } catch { members.value = [] }
}
function showSection(id: Section) {
  section.value = id
  if (id === 'remove' && !members.value.length) loadMembers()
}
async function remove() {
  if (!mailbox.value || busy.value || !confirm(`Remove ${mailbox.value.address}? This cannot be undone.`)) return
  busy.value = 'remove'
  error.value = ''
  try {
    await mailboxAdminApi.remove(userUuid, transferTo.value || undefined)
    await router.replace(listPath)
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not remove the mailbox.'
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <AppLayout>
    <section class="flex-1 min-w-0 overflow-y-auto p-4 sm:p-6" :aria-busy="loading || !!busy">
      <router-link :to="listPath" class="inline-flex items-center gap-1 text-sm text-gmail-gray hover:text-gmail-blue mb-4"><ArrowLeft class="w-4 h-4" />Mailboxes{{ domainName ? ` · ${domainName}` : '' }}</router-link>
      <p v-if="loading" role="status" class="text-sm text-gmail-gray">Loading the mailbox…</p>
      <div v-if="error" role="alert" class="mb-4 p-3 bg-red-50 text-red-700 rounded-lg text-sm">{{ error }}</div>
      <div v-if="notice" role="status" class="mb-4 p-3 bg-blue-50 text-blue-800 rounded-lg text-sm flex gap-3"><span class="flex-1">{{ notice }}</span><button type="button" class="underline" @click="notice = ''">Dismiss</button></div>

      <template v-if="detail && mailbox">
        <header class="mb-4">
          <h1 class="text-2xl font-medium break-all">{{ mailbox.address }}</h1>
          <p class="text-gmail-gray mt-1 flex flex-wrap items-center gap-2">{{ mailbox.name }}
            <Badge size="sm" :variant="mailboxStatusBadge[mailbox.status]">{{ mailboxStatusLabel[mailbox.status] }}</Badge>
            <Badge v-if="detail.overview.isCatchAll" size="sm" variant="warning">Catch-all</Badge>
          </p>
        </header>

        <nav class="flex flex-wrap gap-1 border-b border-gmail-border mb-6" aria-label="Mailbox sections">
          <button v-for="s in sections" :key="s.id" type="button" class="px-3 py-2 text-sm -mb-px border-b-2"
            :class="section === s.id ? (s.id === 'remove' ? 'border-red-600 text-red-700 font-medium' : 'border-gmail-blue text-gmail-blue font-medium') : 'border-transparent text-gmail-gray hover:text-gray-900'"
            :aria-current="section === s.id ? 'page' : undefined" @click="showSection(s.id)">{{ s.label }}</button>
        </nav>

        <!-- Overview -->
        <div v-if="section === 'overview'" class="max-w-xl">
          <table class="w-full text-sm border border-gmail-border rounded-lg overflow-hidden">
            <tbody class="divide-y">
              <tr v-for="row in rows" :key="row.label">
                <th scope="row" class="p-2 text-left font-medium bg-gmail-lightGray w-1/2">{{ row.label }}</th>
                <td class="p-2">
                  <button v-if="row.section" type="button" class="hover:underline" :class="row.value ? 'text-green-700' : 'text-gmail-gray'" @click="showSection(row.section as Section)">{{ row.value ? 'Yes' : 'No' }}</button>
                  <span v-else :class="row.value ? 'text-green-700' : 'text-gmail-gray'">{{ row.value ? 'Yes' : 'No' }}</span>
                </td>
              </tr>
              <tr v-if="receivesMail">
                <th scope="row" class="p-2 text-left font-medium bg-gmail-lightGray">Receives mail</th>
                <td class="p-2">
                  <span :class="receivesMail.ok ? 'text-green-700' : 'text-amber-800'">{{ receivesMail.value }}</span>
                  <router-link v-if="!receivesMail.ok && detail.overview.mayReceive && receiving" :to="fixLink" class="ml-2 text-gmail-blue underline">Fix receiving</router-link>
                </td>
              </tr>
              <tr>
                <th scope="row" class="p-2 text-left font-medium bg-gmail-lightGray">Send-as aliases</th>
                <td class="p-2"><button type="button" class="hover:underline" @click="showSection('send-as')">{{ detail.aliases.length }}</button></td>
              </tr>
              <tr>
                <th scope="row" class="p-2 text-left font-medium bg-gmail-lightGray">Last sign-in</th>
                <td class="p-2">{{ formatDate(mailbox.lastLoginAt) }}</td>
              </tr>
            </tbody>
          </table>
          <p class="text-xs text-gmail-gray mt-3">Forwarding and auto-replies are set by the user; only whether they are on is shown. The catch-all marker is read-only here.</p>
        </div>

        <!-- Settings -->
        <div v-else-if="section === 'settings'" class="max-w-xl space-y-6 text-sm">
          <form class="flex flex-wrap items-end gap-2" @submit.prevent="saveName">
            <label class="flex-1 min-w-[12rem]">Name<input v-model="nameDraft" maxlength="255" class="mt-1 w-full border rounded p-2" /></label>
            <Button type="submit" variant="secondary" :disabled="nameDraft.trim() === mailbox.name" :loading="busy === 'name'">Save</Button>
          </form>
          <div class="space-y-3">
            <label class="flex items-start gap-3"><input type="checkbox" class="w-4 h-4 mt-0.5" :checked="detail.overview.maySend" :disabled="!!busy" @change="setSwitch('maySend', $event)" />
              <span><span class="font-medium">May send</span><span class="block text-gmail-gray">Off blocks compose, auto-replies and forward confirmations from this address.</span></span></label>
            <label class="flex items-start gap-3"><input type="checkbox" class="w-4 h-4 mt-0.5" :checked="detail.overview.mayReceive" :disabled="!!busy" @change="setSwitch('mayReceive', $event)" />
              <span><span class="font-medium">May receive</span><span class="block text-gmail-gray">Off sends new mail for this address to the catch-all.</span></span></label>
          </div>
        </div>

        <!-- Send-as -->
        <div v-else-if="section === 'send-as'" class="max-w-xl space-y-6 text-sm">
          <section>
            <h2 class="text-base font-medium mb-1">Send-as aliases</h2>
            <p class="text-gmail-gray mb-3">The user can send as these addresses, and mail to them reaches this mailbox. {{ mailbox.address.split('@')[0] }}+anything@{{ domainName }} always works.</p>
            <ul v-if="detail.aliases.length" class="divide-y border border-gmail-border rounded-lg mb-3">
              <li v-for="alias in detail.aliases" :key="alias.uuid" class="p-2 flex items-center gap-3">
                <span class="flex-1 min-w-0 break-all">{{ alias.address }}</span>
                <button type="button" class="p-1 rounded hover:bg-red-50" :disabled="!!busy" :aria-label="`Remove ${alias.address}`" @click="removeAlias(alias)"><Trash2 class="w-4 h-4 text-red-600" /></button>
              </li>
            </ul>
            <p v-else class="text-gmail-gray mb-3">No aliases.</p>
            <form class="flex flex-wrap items-end gap-2" @submit.prevent="addAlias">
              <label class="flex-1 min-w-[12rem]">Add an alias
                <span class="mt-1 flex items-stretch border rounded overflow-hidden focus-within:ring-1 focus-within:ring-gmail-blue">
                  <input v-model="aliasDraft" autocomplete="off" maxlength="64" class="flex-1 min-w-0 p-2 outline-none" placeholder="sales" />
                  <span class="px-2 flex items-center bg-gmail-lightGray text-gmail-gray break-all">@{{ domainName }}</span>
                </span>
              </label>
              <Button type="submit" variant="secondary" :loading="busy === 'alias'"><Plus class="w-4 h-4" />Add</Button>
            </form>
          </section>
          <section class="pt-6 border-t border-gmail-border">
            <label class="flex items-start gap-3"><input type="checkbox" class="w-4 h-4 mt-0.5" :checked="detail.overview.wildcardSender" :disabled="!!busy" @change="setSwitch('wildcardSender', $event)" />
              <span><span class="font-medium">Wildcard sender</span><span class="block text-gmail-gray">Lets the user send as any unused address on {{ domainName }}. Other people's addresses and aliases stay blocked. Replies to those addresses go to the catch-all.</span></span></label>
            <p v-if="detail.overview.wildcardSender" role="status" class="mt-2 p-2 rounded bg-amber-50 text-amber-900 text-xs">Wildcard sending is on: this user can send as addresses like ceo@{{ domainName }} if no one has them.</p>
          </section>
        </div>

        <!-- Access -->
        <div v-else-if="section === 'access'" class="max-w-xl space-y-6 text-sm">
          <section>
            <p class="flex items-center gap-2">Status <Badge size="sm" :variant="mailboxStatusBadge[mailbox.status]">{{ mailboxStatusLabel[mailbox.status] }}</Badge></p>
            <p v-if="detail.invite" class="text-gmail-gray mt-2">
              {{ detail.invite.purpose === 'mailbox_setup' ? 'Setup link' : 'Password reset link' }}
              {{ detail.invite.status === 'expired' ? 'expired' : 'expires' }} {{ formatDate(detail.invite.expiresAt) }} · sent {{ detail.invite.sendCount }} time{{ detail.invite.sendCount === 1 ? '' : 's' }}
            </p>
          </section>
          <form class="flex flex-wrap items-end gap-2" @submit.prevent="saveRecovery">
            <label class="flex-1 min-w-[12rem]">Recovery email<input v-model="recoveryDraft" type="email" autocomplete="off" class="mt-1 w-full border rounded p-2" placeholder="Outside this mailbox" /></label>
            <Button type="submit" variant="secondary" :disabled="recoveryDraft.trim() === detail.overview.recoveryEmail" :loading="busy === 'recovery'">Save</Button>
            <span class="w-full text-xs text-gmail-gray">Reset links and password notices go here. The previous address is told when it changes.</span>
          </form>
          <section class="flex flex-wrap gap-2 pt-6 border-t border-gmail-border">
            <Button v-if="pending" variant="secondary" :loading="busy === 'resend'" @click="resendInvite"><RotateCw class="w-4 h-4" />Resend setup link</Button>
            <template v-if="status === 'active' || status === 'suspended'">
              <Button variant="secondary" :disabled="!!busy" @click="openPassword('set')"><KeyRound class="w-4 h-4" />Set new password</Button>
              <Button v-if="status === 'active'" variant="secondary" :disabled="!!busy" @click="openPassword('link')"><Mail class="w-4 h-4" />Send reset link</Button>
            </template>
            <Button v-if="detail.overview.twoFactor" variant="secondary" :loading="busy === '2fa'" @click="resetTwoFactor"><ShieldOff class="w-4 h-4" />Reset 2FA</Button>
            <Button v-if="status === 'active'" variant="secondary" :loading="busy === 'suspend'" @click="suspend"><PauseCircle class="w-4 h-4" />Suspend</Button>
            <Button v-if="status === 'suspended'" variant="secondary" :loading="busy === 'reactivate'" @click="reactivate"><PlayCircle class="w-4 h-4" />Reactivate</Button>
          </section>
          <p class="text-xs text-gmail-gray">Setting a password or resetting 2FA signs the user out everywhere. Two-factor sign-in stays on after a password change. Every action is recorded in the audit log.</p>
        </div>

        <!-- Remove -->
        <div v-else-if="section === 'remove'" class="max-w-xl space-y-4 text-sm">
          <div class="p-4 rounded-lg border border-red-200 bg-red-50 space-y-3">
            <h2 class="text-base font-medium text-red-800">Remove mailbox</h2>
            <p>The login is disabled and its links and aliases end. New mail to {{ mailbox.address }} goes to the catch-all. Mail already in the mailbox is kept but no one can read it.</p>
            <label class="block">The mailbox's identities
              <select v-model="transferTo" class="mt-1 w-full border rounded p-2 bg-white">
                <option value="">Disable them</option>
                <option v-for="m in members" :key="m.uuid" :value="m.uuid">Give them to {{ m.email }}</option>
              </select>
            </label>
            <p class="text-xs text-gmail-gray">Giving the identities to someone routes the address's future mail to them instead of the catch-all.</p>
            <Button variant="danger" :loading="busy === 'remove'" @click="remove"><Trash2 class="w-4 h-4" />Remove mailbox</Button>
          </div>
        </div>
      </template>
    </section>

    <Modal :open="!!passwordMode" :title="passwordMode === 'set' ? 'Set a new password' : 'Send a reset link'" @close="closePassword">
      <form class="space-y-3 text-sm" @submit.prevent="submitPassword">
        <template v-if="passwordMode === 'set'">
          <label class="block">New password<input v-model="newPassword" type="password" autocomplete="new-password" maxlength="72" class="mt-1 w-full border rounded p-2" /></label>
          <label class="block">Confirm password<input v-model="confirmPassword" type="password" autocomplete="new-password" maxlength="72" class="mt-1 w-full border rounded p-2" /></label>
          <p class="text-xs text-gmail-gray">Every session ends and any open reset link stops working. The recovery email is told.</p>
        </template>
        <template v-else>
          <label class="block">Send to<input v-model="linkEmail" type="email" autocomplete="off" class="mt-1 w-full border rounded p-2" :placeholder="detail?.overview.recoveryEmail || 'their.personal@example.com'" /></label>
          <p class="text-xs text-gmail-gray">{{ detail?.overview.recoveryEmail ? `Leave empty to use the recovery email ${detail.overview.recoveryEmail}. If you choose another address, the recovery email is told.` : 'There is no recovery email yet.' }} The link works for 72 hours.</p>
        </template>
        <p v-if="passwordError_" role="alert" class="text-red-700">{{ passwordError_ }}</p>
        <div class="flex justify-end gap-2">
          <Button variant="secondary" @click="closePassword">Cancel</Button>
          <Button type="submit">{{ passwordMode === 'set' ? 'Set password' : 'Send link' }}</Button>
        </div>
      </form>
    </Modal>
  </AppLayout>
</template>
