<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { Plus, Trash2, Pencil } from 'lucide-vue-next'
import Button from '@/components/common/Button.vue'
import Badge from '@/components/common/Badge.vue'
import Modal from '@/components/common/Modal.vue'
import { autoRepliesApi, forwardsApi, type AutoReply, type AutoReplyInput, type EmailForward, type ForwardStatus } from '@/lib/api'
import { useDomainsStore } from '@/stores/domains'
import { escapeHtml } from '@/lib/compose'
import { forwardActions } from '@/lib/forwards'

const domains = useDomainsStore()
// Rules may cover your own identities and shared mailboxes you manage.
const ruleIdentities = computed(() => domains.identities.filter(i => !i.shared || i.canManage))
const identityLabel = (id: number | string) => domains.identities.find(i => Number(i.id) === Number(id))?.email || `Identity ${id}`

const autoReplies = ref<AutoReply[]>([])
const forwards = ref<EmailForward[]>([])
const loading = ref(true)
const error = ref('')
const notice = ref('')
const busy = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [replies, list] = await Promise.all([autoRepliesApi.list(), forwardsApi.list(), domains.fetchIdentities()])
    autoReplies.value = replies ?? []
    forwards.value = list ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not load vacation replies and forwards.'
  } finally {
    loading.value = false
  }
}
onMounted(load)

// ---- Auto-replies ----
const editorOpen = ref(false)
const editing = ref<AutoReply | null>(null)
const formError = ref('')
const blank = () => ({ name: 'Vacation reply', identityIds: [] as number[], start: '', end: '', subject: '', message: '', html: '', intervalDays: 7, replyOnce: false, exclude: '', active: true })
const form = ref(blank())

function localInput(value?: string) {
  if (!value) return ''
  const date = new Date(value)
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}
function openEditor(rule?: AutoReply) {
  editing.value = rule || null
  formError.value = ''
  form.value = rule
    ? { name: rule.name, identityIds: [...(rule.identityIds || [])], start: localInput(rule.startDate), end: localInput(rule.endDate), subject: rule.subject, message: rule.textContent || '', html: rule.textContent ? '' : rule.htmlContent, intervalDays: rule.replyIntervalDays || 7, replyOnce: rule.replyOnce, exclude: (rule.excludePatterns || []).join('\n'), active: rule.active }
    : blank()
  editorOpen.value = true
}
function payload(): AutoReplyInput {
  const f = form.value
  const text = f.message.trim()
  return {
    name: f.name.trim() || 'Vacation reply',
    identityIds: f.identityIds,
    startDate: f.start ? new Date(f.start).toISOString() : new Date().toISOString(),
    endDate: f.end ? new Date(f.end).toISOString() : undefined,
    subject: f.subject.trim(),
    // A plain-text message is sent as both parts; HTML from the API is kept as is.
    htmlContent: text ? text.split(/\n{2,}/).map(p => `<p>${escapeHtml(p).replace(/\n/g, '<br>')}</p>`).join('') : f.html,
    textContent: text || undefined,
    replyOnce: f.replyOnce,
    replyIntervalDays: Number(f.intervalDays),
    excludePatterns: f.exclude.split(/[\n,]/).map(p => p.trim()).filter(Boolean),
    active: f.active,
  }
}
async function saveRule() {
  const body = payload()
  if (!body.htmlContent.trim()) { formError.value = 'Write the reply message.'; return }
  if (body.endDate && new Date(body.endDate) <= new Date(body.startDate)) { formError.value = 'The end must be after the start.'; return }
  busy.value = 'rule'
  formError.value = ''
  try {
    const saved = editing.value ? await autoRepliesApi.update(editing.value.id, body) : await autoRepliesApi.create(body)
    autoReplies.value = editing.value ? autoReplies.value.map(r => r.id === saved.id ? saved : r) : [saved, ...autoReplies.value]
    editorOpen.value = false
    notice.value = 'Vacation reply saved.'
  } catch (e) {
    formError.value = e instanceof Error ? e.message : 'Could not save the vacation reply.'
  } finally {
    busy.value = ''
  }
}
async function toggleRule(rule: AutoReply) {
  busy.value = `rule-${rule.id}`
  try {
    const saved = await autoRepliesApi.update(rule.id, { active: !rule.active })
    autoReplies.value = autoReplies.value.map(r => r.id === saved.id ? saved : r)
  } catch (e) { error.value = e instanceof Error ? e.message : 'Could not update the vacation reply.' }
  finally { busy.value = '' }
}
async function deleteRule(rule: AutoReply) {
  if (!confirm(`Delete "${rule.name}"?`)) return
  busy.value = `rule-${rule.id}`
  try {
    await autoRepliesApi.delete(rule.id)
    autoReplies.value = autoReplies.value.filter(r => r.id !== rule.id)
  } catch (e) { error.value = e instanceof Error ? e.message : 'Could not delete the vacation reply.' }
  finally { busy.value = '' }
}
function ruleWindow(rule: AutoReply) {
  const start = new Date(rule.startDate).toLocaleString()
  return rule.endDate ? `${start} – ${new Date(rule.endDate).toLocaleString()}` : `From ${start}, no end`
}

// ---- Forwards ----
const forwardForm = ref({ identityUuid: '', forwardTo: '', keepCopy: true })
const forwardError = ref('')
const selectedForwardIdentity = computed(() => domains.identities.find(i => i.uuid === forwardForm.value.identityUuid))
const statusBadge: Record<ForwardStatus, { label: string; variant: 'success' | 'warning' | 'error' | 'default' }> = {
  pending: { label: 'Awaiting confirmation', variant: 'warning' },
  active: { label: 'Active', variant: 'success' },
  paused: { label: 'Paused', variant: 'default' },
  suspended: { label: 'Suspended', variant: 'error' },
}
async function createForward() {
  const f = forwardForm.value
  if (!f.identityUuid || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(f.forwardTo.trim())) { forwardError.value = 'Choose an identity and enter a valid destination address.'; return }
  busy.value = 'forward'
  forwardError.value = ''
  try {
    // Shared mailboxes always keep their members' copies.
    const created = await forwardsApi.create({ identityUuid: f.identityUuid, forwardTo: f.forwardTo.trim(), keepCopy: selectedForwardIdentity.value?.shared ? true : f.keepCopy })
    forwards.value = [created, ...forwards.value]
    forwardForm.value = { identityUuid: '', forwardTo: '', keepCopy: true }
    notice.value = `We sent a confirmation link to ${created.forwardTo}. Forwarding starts once it is opened.`
  } catch (e) {
    forwardError.value = e instanceof Error ? e.message : 'Could not create the forward.'
  } finally {
    busy.value = ''
  }
}
async function forwardAction(forward: EmailForward, action: 'pause' | 'resume' | 'resend' | 'delete') {
  if (action === 'delete' && !confirm(`Stop forwarding to ${forward.forwardTo}?`)) return
  busy.value = forward.uuid
  error.value = ''
  try {
    if (action === 'delete') {
      await forwardsApi.delete(forward.uuid)
      forwards.value = forwards.value.filter(f => f.uuid !== forward.uuid)
    } else if (action === 'resend') {
      const saved = await forwardsApi.resendVerification(forward.uuid)
      if (saved?.uuid) forwards.value = forwards.value.map(f => f.uuid === saved.uuid ? saved : f)
      notice.value = `A new confirmation link was sent to ${forward.forwardTo}. Forwarding starts once it is confirmed.`
    } else {
      const saved = await forwardsApi.update(forward.uuid, { active: action === 'resume' })
      forwards.value = forwards.value.map(f => f.uuid === saved.uuid ? saved : f)
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not update the forward.'
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <div class="space-y-8" :aria-busy="loading">
    <p v-if="loading" role="status" class="text-sm text-gmail-gray">Loading…</p>
    <div v-if="error" role="alert" class="p-3 bg-red-50 text-red-700 rounded-lg text-sm">{{ error }}</div>
    <div v-if="notice" role="status" class="p-3 bg-blue-50 text-blue-800 rounded-lg text-sm flex gap-3"><span class="flex-1">{{ notice }}</span><button type="button" class="underline" @click="notice = ''">Dismiss</button></div>

    <section aria-labelledby="auto-replies-title">
      <div class="flex items-center justify-between mb-2">
        <h3 id="auto-replies-title" class="text-base font-medium">Vacation replies</h3>
        <Button size="sm" variant="secondary" @click="openEditor()"><Plus class="w-4 h-4" />New reply</Button>
      </div>
      <p class="text-sm text-gmail-gray mb-4">Replies go to the original sender. Mail from lists or automated senders is not answered.</p>
      <ul v-if="autoReplies.length" class="divide-y border border-gmail-border rounded-lg">
        <li v-for="rule in autoReplies" :key="rule.id" class="p-3 text-sm flex items-start gap-3">
          <div class="flex-1 min-w-0">
            <p class="font-medium flex items-center gap-2">{{ rule.name }}<Badge size="sm" :variant="rule.active ? 'success' : 'default'">{{ rule.active ? 'On' : 'Off' }}</Badge></p>
            <p class="text-gmail-gray">{{ ruleWindow(rule) }} · {{ rule.identityIds?.length ? rule.identityIds.map(identityLabel).join(', ') : 'All your identities' }}</p>
            <p class="text-gmail-gray">{{ rule.replyOnce ? 'Once per sender' : `At most once every ${rule.replyIntervalDays} day${rule.replyIntervalDays === 1 ? '' : 's'} per sender` }} · {{ rule.replyCount }} sent</p>
            <p v-if="rule.lastError" class="text-red-700">Last problem: {{ rule.lastError }}</p>
          </div>
          <label class="flex items-center gap-1 text-xs"><input type="checkbox" :checked="rule.active" :disabled="busy === `rule-${rule.id}`" class="w-4 h-4" @change="toggleRule(rule)" />Active</label>
          <button type="button" class="p-1 rounded hover:bg-gray-100" :aria-label="`Edit ${rule.name}`" @click="openEditor(rule)"><Pencil class="w-4 h-4" /></button>
          <button type="button" class="p-1 rounded hover:bg-red-50" :aria-label="`Delete ${rule.name}`" :disabled="busy === `rule-${rule.id}`" @click="deleteRule(rule)"><Trash2 class="w-4 h-4 text-red-600" /></button>
        </li>
      </ul>
      <p v-else-if="!loading" class="text-sm text-gmail-gray">No vacation replies.</p>
    </section>

    <section aria-labelledby="forwards-title" class="pt-6 border-t border-gmail-border">
      <h3 id="forwards-title" class="text-base font-medium mb-2">Forwarding</h3>
      <p class="text-sm text-gmail-gray mb-4">Forwarded mail arrives as "Sender via Mailat" from your identity, and replies go to the original sender. The destination must confirm first.</p>
      <form class="grid gap-3 sm:grid-cols-[1fr_1fr_auto] items-end p-3 border border-gmail-border rounded-lg" @submit.prevent="createForward">
        <label class="text-sm">Forward mail for
          <select v-model="forwardForm.identityUuid" class="mt-1 w-full border rounded p-2 bg-white">
            <option value="" disabled>Choose an identity</option>
            <option v-for="identity in ruleIdentities" :key="identity.uuid" :value="identity.uuid">{{ identity.email }}{{ identity.shared ? ' (shared)' : '' }}</option>
          </select>
        </label>
        <label class="text-sm">To
          <input v-model="forwardForm.forwardTo" type="email" autocomplete="email" placeholder="you@example.com" class="mt-1 w-full border rounded p-2" />
        </label>
        <Button type="submit" :disabled="busy === 'forward'">Add forward</Button>
        <label class="text-sm flex items-center gap-2 sm:col-span-3">
          <input v-model="forwardForm.keepCopy" type="checkbox" class="w-4 h-4" :disabled="selectedForwardIdentity?.shared" />
          Keep a copy in Mailat<span v-if="selectedForwardIdentity?.shared" class="text-gmail-gray">(always on for shared mailboxes)</span><span v-else class="text-gmail-gray">(otherwise forwarded mail is archived)</span>
        </label>
        <p v-if="forwardError" role="alert" class="text-sm text-red-700 sm:col-span-3">{{ forwardError }}</p>
      </form>
      <ul v-if="forwards.length" class="mt-4 divide-y border border-gmail-border rounded-lg">
        <li v-for="forward in forwards" :key="forward.uuid" class="p-3 text-sm flex flex-wrap items-start gap-3">
          <div class="flex-1 min-w-0">
            <p class="font-medium break-all">{{ forward.identityEmail }} → {{ forward.forwardTo }}</p>
            <p class="text-gmail-gray flex flex-wrap items-center gap-2">
              <Badge size="sm" :variant="statusBadge[forward.status]?.variant || 'default'">{{ statusBadge[forward.status]?.label || forward.status }}</Badge>
              <span>{{ forward.keepCopy ? 'Keeps a copy' : 'Archives the copy' }}</span>
              <span v-if="forward.forwardCount">· {{ forward.forwardCount }} forwarded</span>
            </p>
            <p v-if="forward.lastError" class="text-red-700">{{ forward.lastError }}</p>
          </div>
          <div class="flex gap-2">
            <Button v-for="item in forwardActions(forward)" :key="item.action" size="sm" variant="secondary" :disabled="busy === forward.uuid" @click="forwardAction(forward, item.action)">{{ item.label }}</Button>
            <Button size="sm" variant="ghost" :disabled="busy === forward.uuid" @click="forwardAction(forward, 'delete')"><Trash2 class="w-4 h-4 text-red-600" />Delete</Button>
          </div>
        </li>
      </ul>
    </section>

    <Modal :open="editorOpen" :title="editing ? 'Edit vacation reply' : 'New vacation reply'" size="lg" @close="editorOpen = false">
      <form class="space-y-3 text-sm" @submit.prevent="saveRule">
        <label class="block">Name<input v-model="form.name" maxlength="255" class="mt-1 w-full border rounded p-2" /></label>
        <fieldset>
          <legend>Reply for</legend>
          <p class="text-xs text-gmail-gray">None selected means all your personal identities.</p>
          <label v-for="identity in ruleIdentities" :key="identity.uuid" class="flex items-center gap-2 mt-1">
            <input v-model="form.identityIds" type="checkbox" :value="Number(identity.id)" class="w-4 h-4" />{{ identity.email }}{{ identity.shared ? ' (shared)' : '' }}
          </label>
        </fieldset>
        <div class="grid grid-cols-2 gap-3">
          <label>Starts<input v-model="form.start" type="datetime-local" class="mt-1 w-full border rounded p-2" /></label>
          <label>Ends (optional)<input v-model="form.end" type="datetime-local" class="mt-1 w-full border rounded p-2" /></label>
        </div>
        <label class="block">Subject<input v-model="form.subject" maxlength="500" placeholder="Re: original subject" class="mt-1 w-full border rounded p-2" /></label>
        <label class="block">Message<textarea v-model="form.message" rows="6" class="mt-1 w-full border rounded p-2" :placeholder="form.html ? 'This reply has HTML content from the API; typing here replaces it.' : 'I am away until…'" /></label>
        <div class="grid grid-cols-2 gap-3 items-end">
          <label>Reply to the same sender at most every<span class="flex items-center gap-2 mt-1"><input v-model.number="form.intervalDays" type="number" min="1" max="30" :disabled="form.replyOnce" class="w-20 border rounded p-2" />days</span></label>
          <label class="flex items-center gap-2"><input v-model="form.replyOnce" type="checkbox" class="w-4 h-4" />Only once per sender</label>
        </div>
        <label class="block">Never reply to (one per line, * matches anything)<textarea v-model="form.exclude" rows="3" class="mt-1 w-full border rounded p-2 font-mono text-xs" placeholder="*@example.com" /></label>
        <label class="flex items-center gap-2"><input v-model="form.active" type="checkbox" class="w-4 h-4" />Active</label>
        <p v-if="formError" role="alert" class="text-red-700">{{ formError }}</p>
        <div class="flex justify-end gap-2 pt-2">
          <Button variant="secondary" @click="editorOpen = false">Cancel</Button>
          <Button type="submit" :disabled="busy === 'rule'">{{ busy === 'rule' ? 'Saving…' : 'Save' }}</Button>
        </div>
      </form>
    </Modal>
  </div>
</template>
