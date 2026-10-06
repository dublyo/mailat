<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { Filter, Plus, Trash2, X } from 'lucide-vue-next'
import Button from '@/components/common/Button.vue'
import Modal from '@/components/common/Modal.vue'
import { useSettingsStore, type LocalFilter } from '@/stores/settings'
import { labelApi, type EmailLabel, type FilterCondition, type InboxFilter } from '@/lib/api'
import { FILTER_FIELDS, FILTER_OPERATORS, blockedSenderValue, conditionFromLegacyText, describeFilter } from '@/lib/mailRules'

// Server-backed filters and blocked senders (/inbox/filters), plus the opt-in
// import of rules that older versions kept only in this browser.
const store = useSettingsStore()
const labels = ref<EmailLabel[]>([])

interface FilterForm {
  name: string
  priority: number
  active: boolean
  conditionLogic: 'all' | 'any'
  conditions: FilterCondition[]
  actionFolder: string
  actionLabels: string[]
  actionStar: boolean
  actionMarkRead: boolean
  actionArchive: boolean
  actionTrash: boolean
}
const emptyForm = (): FilterForm => ({ name: '', priority: 0, active: true, conditionLogic: 'all', conditions: [{ field: 'from', operator: 'contains', value: '' }], actionFolder: '', actionLabels: [], actionStar: false, actionMarkRead: false, actionArchive: false, actionTrash: false })
const showFilterModal = ref(false)
const editingUuid = ref('')
const recreatingLocal = ref<LocalFilter | null>(null)
const form = ref<FilterForm>(emptyForm())
const formError = ref('')
const saving = ref(false)

const blockInput = ref('')
const blockFolder = ref<'spam' | 'trash'>('spam')
const blockError = ref('')
const blocking = ref(false)
const importing = ref(false)
const importNotice = ref('')

const local = computed(() => store.localRules)
const hasLocalRules = computed(() => local.value.blockedSenders.length > 0 || local.value.filters.length > 0)
const hasAction = computed(() => !!(form.value.actionFolder || form.value.actionLabels.length || form.value.actionStar || form.value.actionMarkRead || form.value.actionArchive || form.value.actionTrash))

onMounted(async () => {
  store.loadLocalMailRules()
  void store.fetchMailRules()
  try { labels.value = await labelApi.list() } catch { /* Labels are optional in the builder. */ }
})

function openCreate() {
  editingUuid.value = ''
  recreatingLocal.value = null
  form.value = emptyForm()
  formError.value = ''
  showFilterModal.value = true
}

function openEdit(filter: InboxFilter) {
  editingUuid.value = filter.uuid
  recreatingLocal.value = null
  form.value = {
    name: filter.name, priority: filter.priority, active: filter.active, conditionLogic: filter.conditionLogic,
    conditions: filter.conditions.map(c => ({ ...c })), actionFolder: filter.actionFolder || '', actionLabels: [...(filter.actionLabels || [])],
    actionStar: filter.actionStar, actionMarkRead: filter.actionMarkRead, actionArchive: filter.actionArchive, actionTrash: filter.actionTrash,
  }
  formError.value = ''
  showFilterModal.value = true
}

function openRecreate(rule: LocalFilter) {
  openCreate()
  recreatingLocal.value = rule
  form.value.name = String(rule.name || '').slice(0, 255)
  form.value.conditions = [conditionFromLegacyText(String(rule.conditions || ''))]
}

function addCondition() {
  if (form.value.conditions.length < 25) form.value.conditions.push({ field: 'subject', operator: 'contains', value: '' })
}

function setConditionField(condition: FilterCondition, field: FilterCondition['field']) {
  condition.field = field
  if (field === 'hasAttachment') { condition.operator = 'equals'; condition.value = 'true' }
}

async function saveFilter() {
  formError.value = ''
  if (!form.value.name.trim()) { formError.value = 'Give the filter a name.'; return }
  if (form.value.conditions.some(c => c.field !== 'hasAttachment' && !c.value.trim())) { formError.value = 'Fill in every condition or remove it.'; return }
  if (!hasAction.value) { formError.value = 'Choose at least one action.'; return }
  saving.value = true
  try {
    const saved = await store.saveFilter({ ...form.value, name: form.value.name.trim(), priority: Number(form.value.priority) || 0 }, editingUuid.value || undefined)
    if (!saved) { formError.value = store.rulesError || 'The filter was not saved.'; return }
    if (recreatingLocal.value) store.forgetLocalFilter(recreatingLocal.value.id)
    showFilterModal.value = false
  } finally {
    saving.value = false
  }
}

async function removeFilter(filter: InboxFilter) {
  if (confirm(`Delete the filter "${filter.name}"?`)) await store.deleteFilter(filter.uuid)
}

async function blockSender() {
  blockError.value = ''
  const value = blockedSenderValue(blockInput.value)
  if (!value) { blockError.value = 'Enter an email address (name@example.com) or a domain (@example.com).'; return }
  blocking.value = true
  try {
    if (await store.blockSender(value, blockFolder.value)) blockInput.value = ''
    else blockError.value = store.rulesError || 'The sender was not blocked.'
  } finally {
    blocking.value = false
  }
}

function blockedValue(rule: InboxFilter) {
  return rule.conditions[0]?.value || rule.name
}

async function importLocal() {
  importing.value = true
  importNotice.value = ''
  try {
    const { imported, failed } = await store.importLocalMailRules()
    if (!failed.length) importNotice.value = `${imported} blocked sender${imported === 1 ? '' : 's'} imported.`
  } finally {
    importing.value = false
  }
}

function discardLocal() {
  if (confirm('Discard the rules saved in this browser? They were never applied and cannot be recovered.')) store.discardLocalMailRules()
}
</script>

<template>
  <div class="space-y-6">
    <div v-if="store.rulesError" role="alert" class="p-3 bg-red-50 text-red-700 rounded-lg text-sm flex gap-3">
      <span class="flex-1">{{ store.rulesError }}</span>
      <button class="underline" @click="store.fetchMailRules()">Retry</button>
    </div>

    <section v-if="hasLocalRules" class="p-4 border border-amber-300 bg-amber-50 rounded-lg text-sm" aria-labelledby="local-rules-title">
      <h3 id="local-rules-title" class="font-medium text-amber-900">Rules saved in this browser only</h3>
      <p class="mt-1 text-amber-900">{{ local.blockedSenders.length }} blocked sender{{ local.blockedSenders.length === 1 ? '' : 's' }} and {{ local.filters.length }} filter{{ local.filters.length === 1 ? ' was' : 's were' }} saved in this browser only and never applied.</p>
      <p class="mt-1 text-xs text-amber-800">Import only if they are yours: anyone who used this browser may have created them.</p>
      <ul v-if="local.filters.length" class="mt-3 space-y-2">
        <li v-for="rule in local.filters" :key="rule.id" class="flex items-center gap-3 bg-white rounded p-2 border border-amber-200">
          <span class="flex-1 min-w-0 break-words"><span class="font-medium">{{ rule.name }}</span><span class="block text-xs text-gmail-gray">{{ rule.conditions }} → {{ rule.actions }}</span></span>
          <button class="text-gmail-blue hover:underline shrink-0" @click="openRecreate(rule)">Recreate</button>
        </li>
      </ul>
      <div class="mt-3 flex flex-wrap gap-3">
        <Button v-if="local.blockedSenders.length" :disabled="importing" @click="importLocal">{{ importing ? 'Importing…' : 'Import blocked senders' }}</Button>
        <Button variant="secondary" :disabled="importing" @click="discardLocal">Discard</Button>
      </div>
    </section>
    <p v-if="importNotice" role="status" class="text-sm text-green-700">{{ importNotice }}</p>

    <div class="flex items-center justify-between gap-3">
      <p class="text-gmail-gray text-sm">Filters run on new mail as it arrives. Higher priority runs first; when several filters move a message, the last one wins.</p>
      <Button class="shrink-0" @click="openCreate"><Plus class="w-4 h-4" />Create Filter</Button>
    </div>

    <div class="border border-gmail-border rounded-lg overflow-hidden" :aria-busy="store.rulesLoading">
      <div class="bg-gmail-lightGray px-4 py-3 border-b border-gmail-border">
        <span class="text-sm font-medium">Filters ({{ store.filters.length }})</span>
      </div>
      <div v-if="store.rulesLoading && !store.filters.length" role="status" class="p-6 text-sm text-gmail-gray">Loading filters…</div>
      <div v-else-if="store.filters.length === 0" class="p-8 text-center text-gmail-gray">
        <Filter class="w-12 h-12 mx-auto mb-3 opacity-50" />
        <p>No filters created yet</p>
      </div>
      <ul v-else class="divide-y divide-gmail-border">
        <li v-for="filter in store.filters" :key="filter.uuid" class="p-4 flex items-center gap-3">
          <label class="shrink-0" :title="filter.active ? 'Active' : 'Paused'">
            <input type="checkbox" :checked="filter.active" class="w-4 h-4 rounded border-gmail-border text-gmail-blue" :aria-label="`${filter.active ? 'Pause' : 'Activate'} ${filter.name}`" @change="store.setFilterActive(filter, ($event.target as HTMLInputElement).checked)" />
          </label>
          <div class="flex-1 min-w-0">
            <p :class="['font-medium text-sm', filter.active ? '' : 'text-gmail-gray']">{{ filter.name }}<span class="ml-2 text-xs font-normal text-gmail-gray">priority {{ filter.priority }}</span></p>
            <p class="text-xs text-gmail-gray break-words">{{ describeFilter(filter) }}</p>
          </div>
          <button class="text-gmail-blue text-sm hover:underline" @click="openEdit(filter)">Edit</button>
          <button class="text-gmail-red text-sm hover:underline" @click="removeFilter(filter)">Delete</button>
        </li>
      </ul>
    </div>

    <section class="pt-6 border-t border-gmail-border" aria-labelledby="blocked-senders-title">
      <h3 id="blocked-senders-title" class="text-sm font-medium text-gmail-gray mb-1">Blocked Senders ({{ store.blockedSenders.length }})</h3>
      <p class="text-xs text-gmail-gray mb-4">Blocked senders always run after your filters, so no filter can bring their mail back to the Inbox. Existing mail is not moved.</p>
      <form class="flex flex-wrap items-start gap-2 mb-4" @submit.prevent="blockSender">
        <div class="flex-1 min-w-[12rem]">
          <label for="block-sender-input" class="sr-only">Address or @domain to block</label>
          <input id="block-sender-input" v-model="blockInput" type="text" autocomplete="off" placeholder="spam@example.com or @example.com" class="w-full px-3 py-2 border border-gmail-border rounded-lg text-sm focus:outline-none focus:border-gmail-blue" />
          <p class="text-xs text-gmail-gray mt-1">@example.com doesn't cover subdomains.</p>
        </div>
        <label class="text-sm"><span class="sr-only">Destination</span>
          <select v-model="blockFolder" class="px-3 py-2 border border-gmail-border rounded-lg text-sm bg-white">
            <option value="spam">Send to Spam</option>
            <option value="trash">Send to Trash</option>
          </select>
        </label>
        <Button type="submit" variant="secondary" :disabled="blocking || !blockInput.trim()">Block</Button>
      </form>
      <p v-if="blockError" role="alert" class="text-sm text-red-700 mb-3">{{ blockError }}</p>
      <div v-if="store.blockedSenders.length === 0" class="text-center text-gmail-gray py-4">
        <p class="text-sm">No blocked senders</p>
      </div>
      <ul v-else class="space-y-2">
        <li v-for="rule in store.blockedSenders" :key="rule.uuid" class="flex items-center justify-between gap-3 p-3 bg-gmail-lightGray rounded-lg">
          <span class="text-sm break-all">{{ blockedValue(rule) }}<span class="ml-2 text-xs text-gmail-gray">→ {{ rule.actionFolder === 'trash' ? 'Trash' : 'Spam' }}</span></span>
          <button class="text-gmail-red text-sm hover:underline shrink-0" @click="store.unblockSender(rule.uuid)">Unblock</button>
        </li>
      </ul>
    </section>

    <Modal :open="showFilterModal" size="xl" :title="editingUuid ? 'Edit Filter' : 'Create Filter'" @close="showFilterModal = false">
      <form class="space-y-4" @submit.prevent="saveFilter">
        <p v-if="recreatingLocal" class="text-xs bg-amber-50 text-amber-900 rounded p-2">Recreating "{{ recreatingLocal.conditions }} → {{ recreatingLocal.actions }}". Review the conditions and actions before saving.</p>
        <div class="grid grid-cols-3 gap-3">
          <label class="col-span-2 text-sm font-medium">Name
            <input v-model="form.name" type="text" maxlength="255" required placeholder="e.g., Newsletters" class="mt-1 w-full px-3 py-2 border border-gmail-border rounded-lg font-normal focus:outline-none focus:border-gmail-blue" />
          </label>
          <label class="text-sm font-medium">Priority
            <input v-model.number="form.priority" type="number" min="-10000" max="10000" class="mt-1 w-full px-3 py-2 border border-gmail-border rounded-lg font-normal focus:outline-none focus:border-gmail-blue" />
          </label>
        </div>

        <fieldset>
          <legend class="text-sm font-medium mb-2">When a message matches
            <select v-model="form.conditionLogic" class="mx-1 px-2 py-1 border border-gmail-border rounded bg-white text-sm" aria-label="Condition logic">
              <option value="all">all</option>
              <option value="any">any</option>
            </select>
            of these conditions
          </legend>
          <div v-for="(condition, index) in form.conditions" :key="index" class="flex flex-wrap items-center gap-2 mb-2">
            <select :value="condition.field" class="px-2 py-2 border border-gmail-border rounded-lg bg-white text-sm" aria-label="Field" @change="setConditionField(condition, ($event.target as HTMLSelectElement).value as FilterCondition['field'])">
              <option v-for="field in FILTER_FIELDS" :key="field.value" :value="field.value">{{ field.label }}</option>
            </select>
            <template v-if="condition.field === 'hasAttachment'">
              <select v-model="condition.value" class="px-2 py-2 border border-gmail-border rounded-lg bg-white text-sm" aria-label="Attachment">
                <option value="true">yes</option>
                <option value="false">no</option>
              </select>
            </template>
            <template v-else>
              <select v-model="condition.operator" class="px-2 py-2 border border-gmail-border rounded-lg bg-white text-sm" aria-label="Operator">
                <option v-for="operator in FILTER_OPERATORS" :key="operator.value" :value="operator.value">{{ operator.label }}</option>
              </select>
              <input v-model="condition.value" type="text" maxlength="1000" class="flex-1 min-w-[8rem] px-3 py-2 border border-gmail-border rounded-lg text-sm focus:outline-none focus:border-gmail-blue" aria-label="Value" />
            </template>
            <button v-if="form.conditions.length > 1" type="button" class="p-2 text-gmail-gray hover:text-gmail-red" aria-label="Remove condition" @click="form.conditions.splice(index, 1)"><X class="w-4 h-4" /></button>
          </div>
          <button type="button" class="text-sm text-gmail-blue hover:underline" @click="addCondition"><Plus class="w-3 h-3 inline" /> Add condition</button>
        </fieldset>

        <fieldset class="space-y-2">
          <legend class="text-sm font-medium mb-2">Then</legend>
          <label class="flex items-center gap-2 text-sm">Move to
            <select v-model="form.actionFolder" class="px-2 py-1 border border-gmail-border rounded bg-white text-sm">
              <option value="">(don't move)</option>
              <option value="inbox">Inbox</option>
              <option value="archive">Archive</option>
              <option value="spam">Spam</option>
              <option value="trash">Trash</option>
              <option value="dmarc-reports">DMARC Reports</option>
            </select>
          </label>
          <div v-if="labels.length" class="flex flex-wrap gap-2 text-sm">
            <span>Apply labels:</span>
            <label v-for="label in labels" :key="label.uuid" class="flex items-center gap-1"><input v-model="form.actionLabels" type="checkbox" :value="label.name" class="rounded border-gmail-border" />{{ label.name }}</label>
          </div>
          <div class="flex flex-wrap gap-4 text-sm">
            <label class="flex items-center gap-2"><input v-model="form.actionStar" type="checkbox" class="rounded border-gmail-border" />Star</label>
            <label class="flex items-center gap-2"><input v-model="form.actionMarkRead" type="checkbox" class="rounded border-gmail-border" />Mark as read</label>
            <label class="flex items-center gap-2"><input v-model="form.actionArchive" type="checkbox" class="rounded border-gmail-border" />Archive</label>
            <label class="flex items-center gap-2"><input v-model="form.actionTrash" type="checkbox" class="rounded border-gmail-border" /><Trash2 class="w-3 h-3" />Trash</label>
          </div>
        </fieldset>

        <label class="flex items-center gap-2 text-sm"><input v-model="form.active" type="checkbox" class="rounded border-gmail-border" />Active</label>
        <p v-if="formError" role="alert" class="text-sm text-red-700">{{ formError }}</p>
        <div class="flex justify-end gap-3 pt-2">
          <Button type="button" variant="secondary" @click="showFilterModal = false">Cancel</Button>
          <Button type="submit" :disabled="saving">{{ saving ? 'Saving…' : editingUuid ? 'Save Filter' : 'Create Filter' }}</Button>
        </div>
      </form>
    </Modal>
  </div>
</template>
