<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Plus, ArrowLeft, ExternalLink, Copy, FileText } from 'lucide-vue-next'
import AppLayout from '@/components/layout/AppLayout.vue'
import Button from '@/components/common/Button.vue'
import SignupCard from '@/components/forms/SignupCard.vue'
import { identityApi, listApi, type ContactList, type Identity } from '@/lib/api'
import { deleteFormPrompt, signupFormsApi, type SignupForm, type SignupFormDraft, type SignupEntries } from '@/lib/signupForms'
const forms = ref<SignupForm[]>([]), lists = ref<ContactList[]>([]), identities = ref<Identity[]>([])
const loading = ref(true), saving = ref(false), deleting = ref(''), error = ref(''), notice = ref(''), editing = ref(false), id = ref('')
const entries = ref<SignupEntries>(), viewing = ref<SignupForm>(), entriesLoading = ref(false)
const empty = (): SignupFormDraft => ({ name: '', listId: '', identityId: '', title: 'Let’s stay in touch', description: 'A little inspiration, useful updates, and news worth opening.', consentText: 'I would like to receive email updates from this business. I can unsubscribe at any time.', buttonText: 'Count me in', privacyUrl: '', collectName: true, published: false })
const draft = ref<SignupFormDraft>(empty())
const selectedList = computed(() => lists.value.find(l => l.uuid === draft.value.listId))
const preview = computed(() => ({ ...draft.value, confirmationMode: selectedList.value?.confirmationMode || 'single' as const }))
const link = (uuid: string) => `${window.location.origin}/subscribe/${uuid}`
const embed = (uuid: string) => `<iframe src="${link(uuid)}" title="Email signup" width="100%" height="760" style="border:0;max-width:560px" loading="lazy" referrerpolicy="no-referrer"></iframe>`
function formData(f: SignupForm): SignupFormDraft { return { listId:f.listId,identityId:f.identityId,name:f.name,title:f.title,description:f.description,consentText:f.consentText,buttonText:f.buttonText,privacyUrl:f.privacyUrl,collectName:f.collectName,published:f.published } }
async function load() {
  loading.value = true; error.value = ''
  try { const data = await Promise.all([signupFormsApi.list(), listApi.list(), identityApi.list()]); forms.value = data[0] || []; lists.value = (data[1] || []).filter(l => !l.type || l.type === 'static'); identities.value = (data[2] || []).filter(i => i.canSend !== false) }
  catch (e) { error.value = e instanceof Error ? e.message : 'Could not load signup forms.' }
  finally { loading.value = false }
}
function edit(f?: SignupForm) { id.value = f?.uuid || ''; draft.value = f ? formData(f) : { ...empty(), listId: lists.value[0]?.uuid || '' }; editing.value = true; viewing.value = undefined; error.value = ''; notice.value = '' }
async function save() {
  saving.value = true; error.value = ''; notice.value = ''
  try { const result = id.value ? await signupFormsApi.update(id.value, draft.value) : await signupFormsApi.create(draft.value); id.value = result.uuid; notice.value = result.published ? 'Your form is live. Share the link or embed it below.' : 'Draft saved. Publish when you’re ready.'; await load() }
  catch (e) { error.value = e instanceof Error ? e.message : 'Could not save this form.' }
  finally { saving.value = false }
}
async function remove(f: SignupForm) {
  if (!confirm(deleteFormPrompt(f))) return
  deleting.value = f.uuid; error.value = ''; notice.value = ''
  try { await signupFormsApi.remove(f.uuid); notice.value = `Deleted "${f.name}".`; await load() }
  catch (e) { error.value = e instanceof Error ? e.message : 'Could not delete this form.' }
  finally { deleting.value = '' }
}
async function copy(value: string) { try { await navigator.clipboard.writeText(value); notice.value = 'Copied—ready to share!' } catch { error.value = 'Copy is unavailable here. Select and copy the text below.' } }
async function showSignups(f: SignupForm, page = 1) { viewing.value = f; entriesLoading.value = true; entries.value = undefined; error.value = ''; try { entries.value = await signupFormsApi.entries(f.uuid, page) } catch (e) { error.value = e instanceof Error ? e.message : 'Could not load signups.' } finally { entriesLoading.value = false } }
onMounted(load)
</script>
<template>
  <AppLayout>
    <div class="w-full overflow-y-auto p-5 sm:p-8">
      <header class="mb-6 flex flex-wrap items-start justify-between gap-4">
        <div><button v-if="editing || viewing" class="mb-3 flex items-center gap-2 text-sm text-gmail-blue" @click="editing = false; viewing = undefined; notice = ''; error = ''; load()"><ArrowLeft class="h-4 w-4" />All forms</button><h1 class="text-2xl font-semibold">{{ editing ? (id ? 'Edit signup form' : 'Create a signup form') : viewing ? viewing.name + ' · Signups' : 'Signup forms' }}</h1><p class="mt-2 text-gmail-gray">A friendly invitation to join your list. Share a page or embed it on your website.</p></div>
        <div v-if="!editing && !viewing" class="flex gap-3"><Button variant="secondary" :disabled="loading" @click="load">Refresh</Button><Button :disabled="loading || !lists.length" @click="edit()"><Plus class="h-4 w-4" />Create form</Button></div>
      </header>
      <p v-if="error" role="alert" class="mb-5 rounded-xl border border-red-200 bg-red-50 p-4 text-red-700">{{ error }}</p>
      <p v-if="notice" role="status" class="mb-5 rounded-xl border border-green-200 bg-green-50 p-4 text-green-800">{{ notice }}</p>
      <p v-if="loading && !editing" role="status" class="py-10 text-gmail-gray">Loading your forms…</p>
      <div v-else-if="editing" class="grid items-start gap-8 xl:grid-cols-2">
        <div>
          <form class="space-y-5 rounded-xl border border-gmail-border p-5" @submit.prevent="save">
            <label class="block text-sm font-medium">Internal name<input v-model="draft.name" required maxlength="100" class="form-field" placeholder="e.g. Blog newsletter" /></label>
            <label class="block text-sm font-medium">Contact list<select v-model="draft.listId" required :disabled="!!id" class="form-field"><option value="" disabled>Choose a list</option><option v-for="list in lists" :key="list.uuid" :value="list.uuid">{{ list.name }}</option></select></label>
            <p class="rounded-lg bg-blue-50 p-3 text-sm text-blue-900"><strong>{{ selectedList?.confirmationMode === 'double' ? 'Double opt-in' : 'Single opt-in' }}</strong> · {{ selectedList?.confirmationMode === 'double' ? 'New subscribers confirm by email before joining.' : 'New subscribers join after submitting this form.' }} Change this in <RouterLink to="/contacts" class="underline">Contacts → edit list</RouterLink>.</p>
            <label class="block text-sm font-medium">Confirmation sender<select v-model="draft.identityId" :required="draft.published && selectedList?.confirmationMode === 'double'" class="form-field"><option value="">Choose a sender (required for double opt-in)</option><option v-for="identity in identities" :key="identity.uuid" :value="identity.uuid">{{ identity.email }}</option></select></label>
            <label class="block text-sm font-medium">Headline<input v-model="draft.title" required maxlength="150" class="form-field" /></label>
            <label class="block text-sm font-medium">Introduction<textarea v-model="draft.description" maxlength="1000" rows="3" class="form-field" /></label>
            <label class="block text-sm font-medium">Consent wording<textarea v-model="draft.consentText" required minlength="10" maxlength="1000" rows="3" class="form-field" /><span class="mt-1 block text-xs font-normal text-gmail-gray">Name your business and explain what you’ll send and how often.</span></label>
            <label class="block text-sm font-medium">Button label<input v-model="draft.buttonText" required maxlength="60" class="form-field" /></label>
            <label class="block text-sm font-medium">Privacy policy URL <span class="font-normal text-gmail-gray">(optional)</span><input v-model="draft.privacyUrl" type="url" pattern="https://.*" maxlength="2048" placeholder="https://your-site.com/privacy" class="form-field" /></label>
            <label class="flex items-center gap-3 text-sm"><input v-model="draft.collectName" type="checkbox" class="h-4 w-4" />Include an optional name field</label>
            <label class="flex items-center gap-3 text-sm font-medium"><input v-model="draft.published" type="checkbox" class="h-4 w-4" />Published — accept public signups</label>
            <p class="text-xs text-gmail-gray">Unpublishing also disables outstanding confirmation links until the form is published again.</p>
            <Button type="submit" :loading="saving">{{ draft.published ? 'Save and publish' : 'Save draft' }}</Button>
          </form>
          <section v-if="id" class="mt-6 space-y-4 rounded-xl border border-gmail-border p-5" aria-label="Share your form">
            <h2 class="font-semibold">Share your invitation</h2><p v-if="!draft.published" class="text-sm text-gmail-gray">Save as published before sharing this form.</p>
            <label class="block text-sm">Hosted page<input :value="link(id)" readonly class="form-field" /></label><div class="flex gap-4"><button @click="copy(link(id))" class="flex items-center gap-2 text-sm text-gmail-blue"><Copy class="h-4 w-4" />Copy link</button><a :href="link(id)" target="_blank" rel="noopener noreferrer" class="flex items-center gap-2 text-sm text-gmail-blue">Open page<ExternalLink class="h-4 w-4" /></a></div>
            <label class="block text-sm">Website embed<textarea :value="embed(id)" readonly rows="4" class="form-field font-mono text-xs" /></label><button @click="copy(embed(id))" class="flex items-center gap-2 text-sm text-gmail-blue"><Copy class="h-4 w-4" />Copy embed code</button><p class="text-xs text-gmail-gray">Paste into an HTML block on your website. Adjust the iframe height for longer introductions. No API key needed.</p>
          </section>
        </div>
        <aside class="rounded-xl bg-slate-50 p-4 sm:p-6"><p class="mb-4 text-xs font-semibold uppercase tracking-wide text-gmail-gray">Live preview · no signups sent</p><SignupCard :form="preview" preview /></aside>
      </div>
      <section v-else-if="viewing" class="rounded-xl border border-gmail-border p-5">
        <p class="mb-4 text-sm text-gmail-gray">Signup history records the form request. Current unsubscribe and suppression settings always take priority over this history.</p>
        <p v-if="entriesLoading" role="status">Loading signups…</p>
        <template v-else-if="entries"><div class="overflow-x-auto"><table class="w-full text-left text-sm"><thead><tr class="border-b"><th class="p-3">Email</th><th class="p-3">Status</th><th class="p-3">Policy</th><th class="p-3">Requested</th></tr></thead><tbody><tr v-for="entry in entries.items" :key="entry.id" class="border-b"><td class="p-3">{{ entry.email }}</td><td class="p-3">{{ entry.status }}</td><td class="p-3">{{ entry.confirmationMode }}</td><td class="p-3">{{ new Date(entry.createdAt).toLocaleString() }}</td></tr></tbody></table></div><p v-if="!entries.items.length" class="py-10 text-center text-gmail-gray">Your next subscriber could be the first. Share your form to get started.</p><div class="mt-4 flex items-center justify-between gap-4"><Button variant="secondary" :disabled="entries.page <= 1" @click="showSignups(viewing, entries.page - 1)">Previous</Button><span class="text-sm">{{ entries.total }} signups · Page {{ entries.page }}</span><Button variant="secondary" :disabled="entries.page * entries.pageSize >= entries.total" @click="showSignups(viewing, entries.page + 1)">Next</Button></div></template>
      </section>
      <template v-else>
        <div v-if="!lists.length" class="rounded-xl border border-dashed border-gmail-border p-10 text-center"><h2 class="text-lg font-medium">First, give your subscribers a home</h2><p class="mt-2 text-gmail-gray">Create a contact list, then make its first signup form.</p><RouterLink to="/contacts" class="mt-4 inline-block text-gmail-blue underline">Go to contacts</RouterLink></div>
        <div v-else-if="!forms.length" class="rounded-xl border border-dashed border-gmail-border p-12 text-center"><FileText class="mx-auto mb-4 h-10 w-10 text-gmail-blue" /><h2 class="text-lg font-medium">Let’s grow your list</h2><p class="mt-2 text-gmail-gray">Create an invitation people can sign up to from anywhere.</p><Button class="mt-5" @click="edit()">Create your first form</Button></div>
        <div v-else class="grid gap-5 md:grid-cols-2 2xl:grid-cols-3"><article v-for="form in forms" :key="form.uuid" class="rounded-xl border border-gmail-border p-5"><div class="flex items-center justify-between gap-3"><h2 class="break-words font-semibold">{{ form.name }}</h2><span class="rounded-full px-2 py-1 text-xs" :class="form.published ? 'bg-green-50 text-green-700' : 'bg-gray-100 text-gray-600'">{{ form.published ? 'Published' : 'Draft' }}</span></div><p class="mt-2 text-sm text-gmail-gray">{{ form.listName }} · {{ form.confirmationMode === 'double' ? 'Double opt-in' : 'Single opt-in' }}</p><p class="mt-5 text-sm">{{ form.subscribed }} completed · {{ form.pending }} awaiting confirmation</p><div class="mt-5 flex flex-wrap gap-3"><Button variant="secondary" size="sm" @click="edit(form)">Edit & share</Button><Button variant="ghost" size="sm" @click="showSignups(form)">View signups</Button><Button variant="danger" size="sm" :loading="deleting === form.uuid" :disabled="!!deleting" @click="remove(form)">Delete</Button><a v-if="form.published" :href="link(form.uuid)" target="_blank" rel="noopener noreferrer" class="flex items-center gap-1 text-sm text-gmail-blue">Open<ExternalLink class="h-4 w-4" /></a></div></article></div>
      </template>
    </div>
  </AppLayout>
</template>
<style scoped>
.form-field { @apply mt-2 block w-full rounded-lg border border-gmail-border bg-white px-3 py-2.5 font-normal text-gray-900 focus:border-gmail-blue focus:outline-none focus:ring-2 focus:ring-blue-100 disabled:bg-gray-50; }
</style>
