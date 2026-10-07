<script setup lang="ts">
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { useEditor, EditorContent } from '@tiptap/vue-3'
import StarterKit from '@tiptap/starter-kit'
import ImageExtension from '@tiptap/extension-image'
import { Bold, Italic, Link2 } from 'lucide-vue-next'
import Button from '@/components/common/Button.vue'
import { useDomainsStore } from '@/stores/domains'
import { signatureHtml } from '@/lib/mailHtml'

const domainsStore = useDomainsStore()
// Signatures belong to the caller's personal identities; shared mailboxes have none.
const personal = computed(() => domainsStore.identities.filter(i => !i.shared && i.kind !== 'shared'))
const selectedUuid = ref('')
const selected = computed(() => personal.value.find(i => i.uuid === selectedUuid.value))
const loading = ref(true)
const saving = ref(false)
const notice = ref('')
const error = ref('')
const savedHtml = ref('')
const currentHtml = ref('')

const editor = useEditor({
  extensions: [StarterKit.configure({ link: { openOnClick: false } }), ImageExtension],
  content: '',
  editorProps: { attributes: { class: 'prose prose-sm max-w-none min-h-[140px] outline-none p-3', 'aria-label': 'Signature', role: 'textbox', 'aria-multiline': 'true' } },
  onUpdate: ({ editor }) => { currentHtml.value = editor.getHTML() },
})
const dirty = computed(() => currentHtml.value !== savedHtml.value)

function load() {
  notice.value = ''; error.value = ''
  editor.value?.commands.setContent(signatureHtml(selected.value), { emitUpdate: false })
  currentHtml.value = savedHtml.value = editor.value?.getHTML() || ''
}
watch([selectedUuid, editor], load)
watch(personal, list => {
  if (!list.some(i => i.uuid === selectedUuid.value)) selectedUuid.value = (list.find(i => i.isDefault) || list[0])?.uuid || ''
}, { immediate: true })

onMounted(async () => {
  try { await domainsStore.fetchIdentities() } finally { loading.value = false }
})
onBeforeUnmount(() => editor.value?.destroy())

function insertLink() {
  const url = prompt('Link URL (https://…)')
  if (url && /^https?:\/\//i.test(url)) editor.value?.chain().focus().extendMarkRange('link').setLink({ href: url }).run()
}

async function save() {
  const identity = selected.value
  if (!identity || !editor.value) return
  saving.value = true; notice.value = ''; error.value = ''
  const text = editor.value.getText().trim()
  // Store what compose will insert: sanitised HTML, or nothing when empty.
  const html = (text || editor.value.getHTML().includes('<img')) ? signatureHtml({ signatureHtml: editor.value.getHTML() }) : ''
  try {
    await domainsStore.updateIdentity(identity.uuid, { signatureHtml: html, signatureText: text })
    await domainsStore.fetchIdentities()
    load()
    notice.value = html ? 'Signature saved.' : 'Signature removed.'
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not save the signature.'
  } finally { saving.value = false }
}
</script>

<template>
  <div>
    <h2 class="text-lg font-medium mb-2">Signature</h2>
    <p class="text-sm text-gmail-gray mb-6">Added to new messages, replies and forwards you send from this address. You can still edit it in each message.</p>

    <p v-if="loading && !personal.length" role="status" class="text-sm text-gmail-gray">Loading your addresses…</p>
    <p v-else-if="!personal.length" class="text-sm text-gmail-gray">You have no personal address to add a signature to.</p>
    <div v-else class="space-y-4">
      <label v-if="personal.length > 1" class="block text-sm">
        <span class="text-gmail-gray">Address</span>
        <select v-model="selectedUuid" class="mt-1 block w-full max-w-md bg-white border border-gmail-border rounded-lg p-2">
          <option v-for="identity in personal" :key="identity.uuid" :value="identity.uuid">{{ identity.displayName }} &lt;{{ identity.email }}&gt;</option>
        </select>
      </label>
      <p v-else class="text-sm"><span class="text-gmail-gray">Address:</span> {{ personal[0].email }}</p>

      <div class="border border-gmail-border rounded-lg">
        <div class="flex gap-1 border-b border-gmail-border px-2 py-1">
          <button type="button" @click="editor?.chain().focus().toggleBold().run()" title="Bold" class="p-2 hover:bg-gray-100 rounded"><Bold class="w-4 h-4" /></button>
          <button type="button" @click="editor?.chain().focus().toggleItalic().run()" title="Italic" class="p-2 hover:bg-gray-100 rounded"><Italic class="w-4 h-4" /></button>
          <button type="button" @click="insertLink" title="Insert link" class="p-2 hover:bg-gray-100 rounded"><Link2 class="w-4 h-4" /></button>
        </div>
        <EditorContent :editor="editor" />
      </div>

      <p v-if="error" role="alert" class="p-3 bg-red-50 text-red-700 rounded-lg text-sm">{{ error }}</p>
      <p v-if="notice" role="status" class="p-3 bg-green-50 text-green-800 rounded-lg text-sm">{{ notice }}</p>
      <div class="flex justify-end">
        <Button :disabled="saving || !dirty" @click="save">{{ saving ? 'Saving…' : 'Save signature' }}</Button>
      </div>
    </div>
  </div>
</template>
