<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { Trash2, RotateCw, UserPlus } from 'lucide-vue-next'
import Button from '@/components/common/Button.vue'
import Badge from '@/components/common/Badge.vue'
import Modal from '@/components/common/Modal.vue'
import { orgApi, type OrgIdentity, type OrgInvite, type OrgMember, type InviteStatus } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useDomainsStore } from '@/stores/domains'
import { memberAddress, visibleMembers, removedCount } from '@/lib/team'

const auth = useAuthStore()
const domains = useDomainsStore()
const isOwner = computed(() => auth.user?.role === 'owner')
// Mailbox users are loaded only to label the identities they own; they are
// managed per domain on the Mailboxes page, not here.
const people = ref<OrgMember[]>([])
const members = computed(() => people.value.filter(m => m.role !== 'mailbox'))
const invites = ref<OrgInvite[]>([])
const identities = ref<OrgIdentity[]>([])
const loading = ref(true)
const busy = ref('')
const error = ref('')
const notice = ref('')
const activeMembers = computed(() => members.value.filter(m => m.status === 'active'))
// Removed accounts stay listed for history but are hidden by default.
const showRemoved = ref(false)
const listedMembers = computed(() => visibleMembers(members.value, showRemoved.value))
const removedMembers = computed(() => removedCount(members.value))
const senderIdentities = computed(() => domains.identities.filter(i => !i.shared && i.canSend !== false))
const inviteBadge: Record<InviteStatus, 'warning' | 'success' | 'default' | 'error'> = { pending: 'warning', accepted: 'success', expired: 'default', revoked: 'error' }

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [m, i, ids] = await Promise.all([orgApi.members({ includeMailboxes: true }), orgApi.invites(), orgApi.identities(), domains.fetchIdentities()])
    people.value = m ?? []
    invites.value = i ?? []
    identities.value = ids ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not load your team.'
  } finally {
    loading.value = false
  }
}
onMounted(load)

async function run(key: string, action: () => Promise<void>, fallback: string) {
  busy.value = key
  error.value = ''
  try { await action() } catch (e) { error.value = e instanceof Error ? e.message : fallback } finally { busy.value = '' }
}

// ---- Invites ----
const inviteForm = ref({ email: '', role: 'member' as 'member' | 'admin', senderIdentityUuid: '' })
function sendInvite() {
  const f = inviteForm.value
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(f.email.trim())) { error.value = 'Enter a valid email address.'; return }
  return run('invite', async () => {
    const invite = await orgApi.invite({ email: f.email.trim(), role: isOwner.value ? f.role : 'member', senderIdentityUuid: f.senderIdentityUuid || undefined })
    invites.value = [invite, ...invites.value.filter(i => i.uuid !== invite.uuid)]
    inviteForm.value = { email: '', role: 'member', senderIdentityUuid: f.senderIdentityUuid }
    notice.value = `Invite sent to ${invite.email}.`
  }, 'Could not send the invite.')
}
const resendInvite = (invite: OrgInvite) => run(invite.uuid, async () => {
  const updated = await orgApi.resendInvite(invite.uuid)
  invites.value = invites.value.map(i => i.uuid === updated.uuid ? updated : i)
  notice.value = `Invite sent again to ${invite.email}.`
}, 'Could not resend the invite.')
const revokeInvite = (invite: OrgInvite) => confirm(`Revoke the invite for ${invite.email}?`) && run(invite.uuid, async () => {
  await orgApi.revokeInvite(invite.uuid)
  invites.value = invites.value.map(i => i.uuid === invite.uuid ? { ...i, status: 'revoked' } : i)
}, 'Could not revoke the invite.')

// ---- Members ----
const canRemove = (member: OrgMember) => member.status === 'active' && member.role !== 'owner' && member.uuid !== currentUuid.value && (isOwner.value || member.role === 'member')
const currentUuid = computed(() => members.value.find(m => m.email.toLowerCase() === auth.user?.email?.toLowerCase())?.uuid)
const changeRole = (member: OrgMember, role: string) => run(member.uuid, async () => {
  const updated = await orgApi.changeRole(member.uuid, role as 'admin' | 'member')
  people.value = people.value.map(m => m.uuid === updated.uuid ? updated : m)
}, 'Could not change the role.')
const removing = ref<OrgMember | null>(null)
const transferTo = ref('')
const removeTargets = computed(() => activeMembers.value.filter(m => m.uuid !== removing.value?.uuid))
function startRemove(member: OrgMember) { removing.value = member; transferTo.value = '' }
const confirmRemove = () => removing.value && run('remove', async () => {
  const member = removing.value!
  const result = await orgApi.removeMember(member.uuid, transferTo.value || undefined)
  removing.value = null
  notice.value = `${member.email} was removed. ${result.identitiesTransferred} identit${result.identitiesTransferred === 1 ? 'y' : 'ies'} transferred, ${result.identitiesDisabled} disabled.`
  await load()
}, 'Could not remove the member.')

// ---- Identities ----
const transferIdentity = (identity: OrgIdentity, userUuid: string) => userUuid && userUuid !== identity.ownerUuid && confirm(`Give ${identity.email} to ${members.value.find(m => m.uuid === userUuid)?.email}? Existing mail stays with the current owner.`) && run(identity.uuid, async () => {
  const updated = await orgApi.transferIdentity(identity.uuid, userUuid)
  identities.value = identities.value.map(i => i.uuid === updated.uuid ? updated : i)
}, 'Could not transfer the identity.')
// The owner shown for an identity whose owner is not an active team member.
function otherOwner(identity: OrgIdentity) {
  const owner = people.value.find(m => m.uuid === identity.ownerUuid)
  if (owner?.role === 'mailbox' && owner.status !== 'disabled') return `${owner.email} (mailbox)`
  return `${memberAddress(identity.ownerEmail)} (removed)`
}
const formatDate = (value: string | null) => value ? new Date(value).toLocaleDateString() : 'Never'
</script>

<template>
  <div class="space-y-8" :aria-busy="loading">
    <p v-if="loading" role="status" class="text-sm text-gmail-gray">Loading your team…</p>
    <div v-if="error" role="alert" class="p-3 bg-red-50 text-red-700 rounded-lg text-sm">{{ error }}</div>
    <div v-if="notice" role="status" class="p-3 bg-blue-50 text-blue-800 rounded-lg text-sm flex gap-3"><span class="flex-1">{{ notice }}</span><button type="button" class="underline" @click="notice = ''">Dismiss</button></div>

    <section aria-labelledby="invite-title">
      <h3 id="invite-title" class="text-base font-medium mb-1">Invite someone</h3>
      <p class="text-sm text-gmail-gray mb-3">They get an email link that is valid for 7 days. Members see only their own mail and the shared mailboxes they join.</p>
      <form class="grid gap-3 sm:grid-cols-[2fr_1fr_2fr_auto] items-end" @submit.prevent="sendInvite">
        <label class="text-sm">Email<input v-model="inviteForm.email" type="email" autocomplete="off" class="mt-1 w-full border rounded p-2" /></label>
        <label class="text-sm">Role
          <select v-model="inviteForm.role" :disabled="!isOwner" class="mt-1 w-full border rounded p-2 bg-white">
            <option value="member">Member</option>
            <option value="admin">Admin</option>
          </select>
        </label>
        <label class="text-sm">Send from
          <select v-model="inviteForm.senderIdentityUuid" class="mt-1 w-full border rounded p-2 bg-white">
            <option value="">My default identity</option>
            <option v-for="identity in senderIdentities" :key="identity.uuid" :value="identity.uuid">{{ identity.email }}</option>
          </select>
        </label>
        <Button type="submit" :disabled="busy === 'invite'"><UserPlus class="w-4 h-4" />Invite</Button>
      </form>
    </section>

    <section aria-labelledby="members-title" class="pt-6 border-t border-gmail-border">
      <div class="flex items-center justify-between gap-3 mb-3">
        <div>
          <h3 id="members-title" class="text-base font-medium">Members</h3>
          <p class="text-xs text-gmail-gray">Mailbox users are managed per domain: <router-link to="/domains" class="text-gmail-blue hover:underline">Domains → Mailboxes</router-link>.</p>
        </div>
        <label v-if="removedMembers" class="text-sm text-gmail-gray flex items-center gap-2"><input v-model="showRemoved" type="checkbox" />Show removed ({{ removedMembers }})</label>
      </div>
      <div class="overflow-x-auto border border-gmail-border rounded-lg">
        <table class="w-full text-sm">
          <thead class="bg-gmail-lightGray text-left text-gmail-gray"><tr><th class="p-2 font-medium">Name</th><th class="p-2 font-medium">Role</th><th class="p-2 font-medium">Last sign-in</th><th class="p-2"><span class="sr-only">Actions</span></th></tr></thead>
          <tbody class="divide-y">
            <tr v-for="member in listedMembers" :key="member.uuid" :class="member.status !== 'active' ? 'text-gmail-gray' : ''">
              <td class="p-2"><span class="font-medium">{{ member.name }}</span><span class="block text-xs text-gmail-gray">{{ memberAddress(member.email) }}</span></td>
              <td class="p-2">
                <Badge v-if="member.status !== 'active'" size="sm">Removed</Badge>
                <select v-else-if="isOwner && member.role !== 'owner'" :value="member.role" :disabled="busy === member.uuid" :aria-label="`Role for ${member.email}`" class="border rounded p-1 bg-white" @change="changeRole(member, ($event.target as HTMLSelectElement).value)">
                  <option value="member">Member</option>
                  <option value="admin">Admin</option>
                </select>
                <span v-else class="capitalize">{{ member.role }}</span>
              </td>
              <td class="p-2">{{ formatDate(member.lastLoginAt) }}</td>
              <td class="p-2 text-right"><button v-if="canRemove(member)" type="button" class="p-1 rounded hover:bg-red-50" :aria-label="`Remove ${member.email}`" @click="startRemove(member)"><Trash2 class="w-4 h-4 text-red-600" /></button></td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section v-if="invites.length" aria-labelledby="invites-title" class="pt-6 border-t border-gmail-border">
      <h3 id="invites-title" class="text-base font-medium mb-3">Invites</h3>
      <ul class="divide-y border border-gmail-border rounded-lg">
        <li v-for="invite in invites" :key="invite.uuid" class="p-2 text-sm flex flex-wrap items-center gap-3">
          <span class="flex-1 min-w-0 break-all">{{ invite.email }} <span class="text-gmail-gray capitalize">· {{ invite.role }}</span></span>
          <Badge size="sm" :variant="inviteBadge[invite.status]" class="capitalize">{{ invite.status }}</Badge>
          <span class="text-xs text-gmail-gray">{{ invite.status === 'pending' ? `Expires ${formatDate(invite.expiresAt)}` : '' }}</span>
          <template v-if="invite.status === 'pending' || invite.status === 'expired'">
            <Button size="sm" variant="secondary" :disabled="busy === invite.uuid" @click="resendInvite(invite)"><RotateCw class="w-4 h-4" />Resend</Button>
            <Button v-if="invite.status === 'pending'" size="sm" variant="ghost" :disabled="busy === invite.uuid" @click="revokeInvite(invite)">Revoke</Button>
          </template>
        </li>
      </ul>
    </section>

    <section aria-labelledby="org-identities-title" class="pt-6 border-t border-gmail-border">
      <h3 id="org-identities-title" class="text-base font-medium mb-1">Identities</h3>
      <p class="text-sm text-gmail-gray mb-3">Giving an identity to someone else routes its future mail to them. Earlier mail stays with the previous owner.</p>
      <ul class="divide-y border border-gmail-border rounded-lg">
        <li v-for="identity in identities" :key="identity.uuid" class="p-2 text-sm flex flex-wrap items-center gap-3">
          <span class="flex-1 min-w-0 break-all">{{ identity.email }}
            <Badge v-if="identity.kind === 'shared'" size="sm" variant="info">Shared</Badge>
            <Badge v-if="identity.isCatchAll" size="sm" variant="warning">Catch-all</Badge>
            <Badge v-if="!identity.canReceive && !identity.canSend" size="sm">Disabled</Badge>
          </span>
          <span v-if="identity.kind === 'shared'" class="text-xs text-gmail-gray">Managed in Shared mailboxes</span>
          <span v-else-if="identity.mailboxPrimary" class="text-xs text-gmail-gray">Managed on the Mailboxes page</span>
          <label v-else class="text-xs text-gmail-gray flex items-center gap-2">Owner
            <select :value="identity.ownerUuid" :disabled="busy === identity.uuid" class="border rounded p-1 bg-white text-sm" @change="transferIdentity(identity, ($event.target as HTMLSelectElement).value) || (($event.target as HTMLSelectElement).value = identity.ownerUuid)">
              <option v-if="!activeMembers.some(m => m.uuid === identity.ownerUuid)" :value="identity.ownerUuid">{{ otherOwner(identity) }}</option>
              <option v-for="member in activeMembers" :key="member.uuid" :value="member.uuid">{{ member.email }}</option>
            </select>
          </label>
        </li>
      </ul>
    </section>

    <Modal :open="!!removing" :title="removing ? `Remove ${removing.email}` : ''" @close="removing = null">
      <div class="space-y-3 text-sm">
        <p>They are signed out everywhere and lose access immediately. Their mail is kept but no one can read it.</p>
        <label class="block">Their personal identities
          <select v-model="transferTo" class="mt-1 w-full border rounded p-2 bg-white">
            <option value="">Disable them</option>
            <option v-for="member in removeTargets" :key="member.uuid" :value="member.uuid">Give them to {{ member.email }}</option>
          </select>
        </label>
        <div class="flex justify-end gap-2 pt-2">
          <Button variant="secondary" @click="removing = null">Cancel</Button>
          <Button variant="danger" :disabled="busy === 'remove'" @click="confirmRemove">Remove</Button>
        </div>
      </div>
    </Modal>
  </div>
</template>
