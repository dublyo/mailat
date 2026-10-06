<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { Plus, Trash2, Users } from 'lucide-vue-next'
import Button from '@/components/common/Button.vue'
import Badge from '@/components/common/Badge.vue'
import Modal from '@/components/common/Modal.vue'
import { orgApi, sharedMailboxApi, isOrgAdmin, type OrgMember, type SharedMailbox, type SharedMailboxMember, type SharedMemberPermissions } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'
import { useDomainsStore } from '@/stores/domains'

const MEMBER_CAP = 50
const auth = useAuthStore()
const domains = useDomainsStore()
const admin = computed(() => isOrgAdmin(auth.user))
const mailboxes = ref<SharedMailbox[]>([])
const orgMembers = ref<OrgMember[]>([])
const loading = ref(true)
const busy = ref('')
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    mailboxes.value = (await sharedMailboxApi.list()) ?? []
    // Only admins can list the team, so only they can add new people.
    if (admin.value) orgMembers.value = ((await orgApi.members()) ?? []).filter(m => m.status === 'active')
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not load shared mailboxes.'
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
// Members see new shared identities in the sidebar and composer.
const refreshIdentities = () => domains.fetchIdentities()

const createOpen = ref(false)
const createForm = ref({ name: '', email: '', description: '' })
const createError = ref('')
async function create() {
  const f = createForm.value
  if (!f.name.trim() || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(f.email.trim())) { createError.value = 'Enter a name and a valid address on a receiving domain.'; return }
  busy.value = 'create'
  createError.value = ''
  try {
    await sharedMailboxApi.create({ name: f.name.trim(), email: f.email.trim(), description: f.description.trim() || undefined })
    createOpen.value = false
    createForm.value = { name: '', email: '', description: '' }
    await Promise.all([load(), refreshIdentities()])
  } catch (e) {
    createError.value = e instanceof Error ? e.message : 'Could not create the shared mailbox.'
  } finally {
    busy.value = ''
  }
}
function recreate(mailbox: SharedMailbox) {
  createForm.value = { name: mailbox.name, email: mailbox.email, description: mailbox.description || '' }
  createError.value = ''
  createOpen.value = true
}
const remove = (mailbox: SharedMailbox) => confirm(`Delete ${mailbox.email}? Every member's copy of its mail is deleted.`) && run(`mb-${mailbox.id}`, async () => {
  await sharedMailboxApi.delete(mailbox.id)
  await Promise.all([load(), refreshIdentities()])
}, 'Could not delete the shared mailbox.')

// ---- Members of one mailbox ----
const open = ref<SharedMailbox | null>(null)
const members = ref<SharedMailboxMember[]>([])
const membersError = ref('')
const addForm = ref({ userUuid: '', canRead: true, canSend: false, canManage: false })
const canManageOpen = computed(() => admin.value || !!open.value?.canManage)
const candidates = computed(() => orgMembers.value.filter(m => !members.value.some(x => x.userUuid === m.uuid)))
async function openMembers(mailbox: SharedMailbox) {
  open.value = mailbox
  members.value = []
  membersError.value = ''
  addForm.value = { userUuid: '', canRead: true, canSend: false, canManage: false }
  try { members.value = (await sharedMailboxApi.members(mailbox.id)) ?? [] } catch (e) { membersError.value = e instanceof Error ? e.message : 'Could not load members.' }
}
async function memberAction(key: string, action: () => Promise<void>) {
  busy.value = key
  membersError.value = ''
  try { await action() } catch (e) { membersError.value = e instanceof Error ? e.message : 'Could not update members.' } finally { busy.value = '' }
}
const addMember = () => open.value && memberAction('add', async () => {
  const f = addForm.value
  if (!f.userUuid || !(f.canRead || f.canSend)) { membersError.value = 'Pick a person and allow reading or sending.'; return }
  const added = await sharedMailboxApi.addMember(open.value!.id, f)
  members.value = [...members.value, added]
  addForm.value = { userUuid: '', canRead: true, canSend: false, canManage: false }
  await load()
})
const updateMember = (member: SharedMailboxMember, change: Partial<SharedMemberPermissions>) => open.value && memberAction(member.userUuid, async () => {
  const next = { canRead: member.canRead, canSend: member.canSend, canManage: member.canManage, ...change }
  const saved = await sharedMailboxApi.updateMember(open.value!.id, member.userUuid, next)
  members.value = members.value.map(m => m.userUuid === saved.userUuid ? saved : m)
})
const removeMember = (member: SharedMailboxMember) => open.value && confirm(`Remove ${member.email}? Their copies of this mailbox's mail are deleted.`) && memberAction(member.userUuid, async () => {
  await sharedMailboxApi.removeMember(open.value!.id, member.userUuid)
  members.value = members.value.filter(m => m.userUuid !== member.userUuid)
  await load()
})
</script>

<template>
  <div class="space-y-6" :aria-busy="loading">
    <div class="flex items-start justify-between gap-4">
      <p class="text-sm text-gmail-gray">Mail to a shared address is copied to every member who can read it. Each member has their own read state; members who can send may reply as the shared address.</p>
      <Button v-if="admin" size="sm" variant="secondary" @click="createOpen = true; createError = ''"><Plus class="w-4 h-4" />New shared mailbox</Button>
    </div>
    <p v-if="loading" role="status" class="text-sm text-gmail-gray">Loading…</p>
    <div v-if="error" role="alert" class="p-3 bg-red-50 text-red-700 rounded-lg text-sm">{{ error }}</div>
    <ul v-if="mailboxes.length" class="divide-y border border-gmail-border rounded-lg">
      <li v-for="mailbox in mailboxes" :key="mailbox.id" class="p-3 text-sm flex flex-wrap items-center gap-3">
        <div class="flex-1 min-w-0">
          <p class="font-medium">{{ mailbox.name }} <Badge v-if="!mailbox.active" size="sm" variant="warning">Not active</Badge></p>
          <p class="text-gmail-gray break-all">{{ mailbox.email }} · {{ mailbox.memberCount }} member{{ mailbox.memberCount === 1 ? '' : 's' }}</p>
          <p v-if="!admin" class="text-xs text-gmail-gray">You can {{ [mailbox.canRead && 'read', mailbox.canSend && 'send', mailbox.canManage && 'manage members'].filter(Boolean).join(', ') || 'view' }}.</p>
        </div>
        <template v-if="mailbox.active">
          <Button v-if="admin || mailbox.canManage" size="sm" variant="secondary" @click="openMembers(mailbox)"><Users class="w-4 h-4" />Members</Button>
        </template>
        <Button v-else-if="admin" size="sm" variant="secondary" @click="recreate(mailbox)">Recreate</Button>
        <button v-if="admin" type="button" class="p-1 rounded hover:bg-red-50" :disabled="busy === `mb-${mailbox.id}`" :aria-label="`Delete ${mailbox.email}`" @click="remove(mailbox)"><Trash2 class="w-4 h-4 text-red-600" /></button>
      </li>
    </ul>
    <p v-else-if="!loading" class="text-sm text-gmail-gray">{{ admin ? 'No shared mailboxes yet.' : 'You are not a member of any shared mailbox.' }}</p>

    <Modal :open="createOpen" title="New shared mailbox" @close="createOpen = false">
      <form class="space-y-3 text-sm" @submit.prevent="create">
        <label class="block">Name<input v-model="createForm.name" maxlength="255" class="mt-1 w-full border rounded p-2" placeholder="Support" /></label>
        <label class="block">Address<input v-model="createForm.email" type="email" class="mt-1 w-full border rounded p-2" placeholder="support@yourdomain.com" /></label>
        <p class="text-xs text-gmail-gray">The domain must be verified and receiving. You become its first member.</p>
        <label class="block">Description (optional)<input v-model="createForm.description" maxlength="500" class="mt-1 w-full border rounded p-2" /></label>
        <p v-if="createError" role="alert" class="text-red-700">{{ createError }}</p>
        <div class="flex justify-end gap-2">
          <Button variant="secondary" @click="createOpen = false">Cancel</Button>
          <Button type="submit" :disabled="busy === 'create'">Create</Button>
        </div>
      </form>
    </Modal>

    <Modal :open="!!open" :title="open ? `Members of ${open.email}` : ''" size="lg" @close="open = null">
      <div class="space-y-4 text-sm">
        <p v-if="membersError" role="alert" class="text-red-700">{{ membersError }}</p>
        <table class="w-full">
          <thead class="text-left text-gmail-gray"><tr><th class="py-1 font-medium">Member</th><th class="font-medium">Read</th><th class="font-medium">Send</th><th class="font-medium">Manage</th><th><span class="sr-only">Remove</span></th></tr></thead>
          <tbody class="divide-y">
            <tr v-for="member in members" :key="member.userUuid">
              <td class="py-2 pr-2"><span class="font-medium">{{ member.name }}</span><span class="block text-xs text-gmail-gray break-all">{{ member.email }}</span></td>
              <td v-for="perm in (['canRead', 'canSend', 'canManage'] as const)" :key="perm"><input type="checkbox" class="w-4 h-4" :checked="member[perm]" :disabled="!canManageOpen || busy === member.userUuid" :aria-label="`${perm} for ${member.email}`" @change="updateMember(member, { [perm]: !member[perm] })" /></td>
              <td class="text-right"><button v-if="canManageOpen" type="button" class="p-1 rounded hover:bg-red-50" :disabled="busy === member.userUuid" :aria-label="`Remove ${member.email}`" @click="removeMember(member)"><Trash2 class="w-4 h-4 text-red-600" /></button></td>
            </tr>
          </tbody>
        </table>
        <form v-if="admin" class="flex flex-wrap items-end gap-3 pt-3 border-t" @submit.prevent="addMember">
          <label class="flex-1 min-w-[12rem]">Add
            <select v-model="addForm.userUuid" :disabled="members.length >= MEMBER_CAP" class="mt-1 w-full border rounded p-2 bg-white">
              <option value="" disabled>Choose a team member</option>
              <option v-for="person in candidates" :key="person.uuid" :value="person.uuid">{{ person.name }} ({{ person.email }})</option>
            </select>
          </label>
          <label class="flex items-center gap-1"><input v-model="addForm.canRead" type="checkbox" class="w-4 h-4" />Read</label>
          <label class="flex items-center gap-1"><input v-model="addForm.canSend" type="checkbox" class="w-4 h-4" />Send</label>
          <label class="flex items-center gap-1"><input v-model="addForm.canManage" type="checkbox" class="w-4 h-4" />Manage</label>
          <Button type="submit" size="sm" :disabled="busy === 'add' || members.length >= MEMBER_CAP">Add</Button>
          <p v-if="members.length >= MEMBER_CAP" class="w-full text-xs text-gmail-gray">A shared mailbox can have at most {{ MEMBER_CAP }} members.</p>
        </form>
        <p v-else-if="canManageOpen" class="text-xs text-gmail-gray pt-3 border-t">Ask an organization admin to add new people.</p>
      </div>
    </Modal>
  </div>
</template>
