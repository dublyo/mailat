<script setup lang="ts">
import { computed, watch, onBeforeUnmount } from 'vue'
import { useEditor, EditorContent } from '@tiptap/vue-3'
import StarterKit from '@tiptap/starter-kit'
import Link from '@tiptap/extension-link'
import Image from '@tiptap/extension-image'
import Placeholder from '@tiptap/extension-placeholder'
import TextAlign from '@tiptap/extension-text-align'
import {
  FileText,
  Bold,
  Italic,
  Strikethrough,
  List,
  ListOrdered,
  Quote,
  Link as LinkIcon,
  Image as ImageIcon,
  AlignLeft,
  AlignCenter,
  AlignRight,
  Undo,
  Redo,
  Code,
  Heading1,
  Heading2,
  Minus
} from 'lucide-vue-next'

const props = defineProps<{
  htmlContent: string
  textContent: string
}>()

const emit = defineEmits<{
  'update:htmlContent': [value: string]
  'update:textContent': [value: string]
  'update:valid': [value: boolean]
}>()

const editor = useEditor({
  content: props.htmlContent,
  extensions: [
    StarterKit.configure({
      heading: {
        levels: [1, 2, 3]
      }
    }),
    Link.configure({
      openOnClick: false,
      HTMLAttributes: {
        class: 'text-blue-600 underline hover:text-blue-800'
      }
    }),
    Image.configure({
      HTMLAttributes: {
        class: 'max-w-full h-auto rounded-lg'
      }
    }),
    Placeholder.configure({
      placeholder: 'Start writing your email content...'
    }),
    TextAlign.configure({
      types: ['heading', 'paragraph']
    })
  ],
  editorProps: {
    attributes: {
      class: 'prose prose-sm max-w-none focus:outline-none min-h-[300px] p-4'
    }
  },
  onUpdate: ({ editor }) => {
    const html = editor.getHTML()
    const text = editor.getText()
    emit('update:htmlContent', html)
    emit('update:textContent', text)
  }
})

// Watch for external content changes
watch(() => props.htmlContent, (newContent) => {
  if (editor.value && editor.value.getHTML() !== newContent) {
    editor.value.commands.setContent(newContent, { emitUpdate: false })
  }
})

onBeforeUnmount(() => {
  editor.value?.destroy()
})

const contentLength = computed(() => props.textContent.length)
const wordCount = computed(() => {
  const text = props.textContent.trim()
  if (!text) return 0
  return text.split(/\s+/).length
})

const isValid = computed(() => {
  return props.textContent.trim().length >= 10
})

watch(isValid, (valid) => {
  emit('update:valid', valid)
}, { immediate: true })

const setLink = () => {
  const url = window.prompt('Enter URL:')
  if (url && editor.value) {
    editor.value.chain().focus().extendMarkRange('link').setLink({ href: url }).run()
  }
}

const addImage = () => {
  const url = window.prompt('Enter image URL:')
  if (url && editor.value) {
    editor.value.chain().focus().setImage({ src: url }).run()
  }
}

// Template snippets
const templates = [
  {
    name: 'Welcome Email',
    content: `<h2>Welcome to our community!</h2>
<p>We're thrilled to have you on board. Here's what you can expect:</p>
<ul>
<li>Regular updates and news</li>
<li>Exclusive offers and content</li>
<li>Tips and best practices</li>
</ul>
<p>Feel free to reply to this email if you have any questions.</p>
<p>Best regards,<br>The Team</p>`
  },
  {
    name: 'Newsletter',
    content: `<h2>This Week's Highlights</h2>
<p>Hello!</p>
<p>Here's what's been happening:</p>
<h3>Feature Update</h3>
<p>We've launched exciting new features that you'll love.</p>
<h3>Tips & Tricks</h3>
<p>Check out our latest tips to get the most out of our platform.</p>
<p>Stay tuned for more updates!</p>`
  },
  {
    name: 'Announcement',
    content: `<h2>Important Announcement</h2>
<p>We have some exciting news to share with you!</p>
<p>[Your announcement details here]</p>
<p>Thank you for being a valued member of our community.</p>`
  }
]

const applyTemplate = (content: string) => {
  if (editor.value) {
    editor.value.commands.setContent(content)
  }
}
</script>

<template>
  <div class="space-y-6">
    <div class="text-center mb-8">
      <div class="w-16 h-16 bg-green-100 rounded-full flex items-center justify-center mx-auto mb-4">
        <FileText class="w-8 h-8 text-green-600" />
      </div>
      <h3 class="text-xl font-semibold text-gray-900">Email Content</h3>
      <p class="text-gray-500 mt-1">Create your email using our rich text editor</p>
    </div>

    <!-- Template Quick Start -->
    <div class="mb-4">
      <p class="text-sm text-gray-500 mb-2">Quick start with a template:</p>
      <div class="flex flex-wrap gap-2">
        <button
          v-for="template in templates"
          :key="template.name"
          @click="applyTemplate(template.content)"
          class="px-3 py-1.5 text-xs bg-gray-100 hover:bg-gray-200 rounded-full text-gray-600 transition-colors"
        >
          {{ template.name }}
        </button>
      </div>
    </div>

    <!-- Editor Container -->
    <div class="border border-gray-300 rounded-xl overflow-hidden bg-white shadow-sm">
      <!-- Toolbar -->
      <div class="border-b border-gray-200 bg-gray-50 p-2 flex flex-wrap items-center gap-1">
        <!-- Text Formatting -->
        <div class="flex items-center gap-0.5 pr-2 border-r border-gray-200">
          <button
            @click="editor?.chain().focus().toggleBold().run()"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive('bold') ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Bold"
          >
            <Bold class="w-4 h-4" />
          </button>
          <button
            @click="editor?.chain().focus().toggleItalic().run()"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive('italic') ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Italic"
          >
            <Italic class="w-4 h-4" />
          </button>
          <button
            @click="editor?.chain().focus().toggleStrike().run()"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive('strike') ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Strikethrough"
          >
            <Strikethrough class="w-4 h-4" />
          </button>
          <button
            @click="editor?.chain().focus().toggleCode().run()"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive('code') ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Code"
          >
            <Code class="w-4 h-4" />
          </button>
        </div>

        <!-- Headings -->
        <div class="flex items-center gap-0.5 px-2 border-r border-gray-200">
          <button
            @click="editor?.chain().focus().toggleHeading({ level: 1 }).run()"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive('heading', { level: 1 }) ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Heading 1"
          >
            <Heading1 class="w-4 h-4" />
          </button>
          <button
            @click="editor?.chain().focus().toggleHeading({ level: 2 }).run()"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive('heading', { level: 2 }) ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Heading 2"
          >
            <Heading2 class="w-4 h-4" />
          </button>
        </div>

        <!-- Lists -->
        <div class="flex items-center gap-0.5 px-2 border-r border-gray-200">
          <button
            @click="editor?.chain().focus().toggleBulletList().run()"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive('bulletList') ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Bullet List"
          >
            <List class="w-4 h-4" />
          </button>
          <button
            @click="editor?.chain().focus().toggleOrderedList().run()"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive('orderedList') ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Numbered List"
          >
            <ListOrdered class="w-4 h-4" />
          </button>
          <button
            @click="editor?.chain().focus().toggleBlockquote().run()"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive('blockquote') ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Quote"
          >
            <Quote class="w-4 h-4" />
          </button>
          <button
            @click="editor?.chain().focus().setHorizontalRule().run()"
            class="p-2 rounded hover:bg-gray-200 transition-colors text-gray-600"
            title="Horizontal Rule"
          >
            <Minus class="w-4 h-4" />
          </button>
        </div>

        <!-- Alignment -->
        <div class="flex items-center gap-0.5 px-2 border-r border-gray-200">
          <button
            @click="editor?.chain().focus().setTextAlign('left').run()"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive({ textAlign: 'left' }) ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Align Left"
          >
            <AlignLeft class="w-4 h-4" />
          </button>
          <button
            @click="editor?.chain().focus().setTextAlign('center').run()"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive({ textAlign: 'center' }) ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Align Center"
          >
            <AlignCenter class="w-4 h-4" />
          </button>
          <button
            @click="editor?.chain().focus().setTextAlign('right').run()"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive({ textAlign: 'right' }) ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Align Right"
          >
            <AlignRight class="w-4 h-4" />
          </button>
        </div>

        <!-- Media -->
        <div class="flex items-center gap-0.5 px-2 border-r border-gray-200">
          <button
            @click="setLink"
            :class="[
              'p-2 rounded hover:bg-gray-200 transition-colors',
              editor?.isActive('link') ? 'bg-gray-200 text-blue-600' : 'text-gray-600'
            ]"
            title="Add Link"
          >
            <LinkIcon class="w-4 h-4" />
          </button>
          <button
            @click="addImage"
            class="p-2 rounded hover:bg-gray-200 transition-colors text-gray-600"
            title="Add Image"
          >
            <ImageIcon class="w-4 h-4" />
          </button>
        </div>

        <!-- History -->
        <div class="flex items-center gap-0.5 pl-2">
          <button
            @click="editor?.chain().focus().undo().run()"
            :disabled="!editor?.can().undo()"
            class="p-2 rounded hover:bg-gray-200 transition-colors text-gray-600 disabled:opacity-40 disabled:cursor-not-allowed"
            title="Undo"
          >
            <Undo class="w-4 h-4" />
          </button>
          <button
            @click="editor?.chain().focus().redo().run()"
            :disabled="!editor?.can().redo()"
            class="p-2 rounded hover:bg-gray-200 transition-colors text-gray-600 disabled:opacity-40 disabled:cursor-not-allowed"
            title="Redo"
          >
            <Redo class="w-4 h-4" />
          </button>
        </div>
      </div>

      <!-- Editor Area -->
      <EditorContent :editor="editor" class="min-h-[350px]" />

      <!-- Status Bar -->
      <div class="border-t border-gray-200 bg-gray-50 px-4 py-2 flex items-center justify-between text-xs text-gray-500">
        <div class="flex items-center gap-4">
          <span>{{ wordCount }} words</span>
          <span>{{ contentLength }} characters</span>
        </div>
        <div v-if="contentLength < 10" class="text-amber-600">
          Minimum 10 characters required
        </div>
        <div v-else class="text-green-600">
          Content ready
        </div>
      </div>
    </div>

    <!-- Tips -->
    <div class="bg-blue-50 rounded-lg p-4">
      <h4 class="font-medium text-blue-900 mb-2">Tips for better emails:</h4>
      <ul class="text-sm text-blue-800 space-y-1 list-disc list-inside">
        <li>Keep paragraphs short and scannable</li>
        <li>Use a clear call-to-action</li>
        <li>Personalize with merge tags: <code class="bg-blue-100 px-1 rounded" v-text="'{{firstName}}'"></code></li>
        <li>Test your email before sending</li>
      </ul>
    </div>
  </div>
</template>

<style>
/* TipTap Editor Styles */
.ProseMirror {
  min-height: 300px;
  outline: none;
}

.ProseMirror p.is-editor-empty:first-child::before {
  color: #9ca3af;
  content: attr(data-placeholder);
  float: left;
  height: 0;
  pointer-events: none;
}

.ProseMirror h1 {
  font-size: 1.5rem;
  font-weight: 700;
  margin-bottom: 0.5rem;
}

.ProseMirror h2 {
  font-size: 1.25rem;
  font-weight: 600;
  margin-bottom: 0.5rem;
}

.ProseMirror h3 {
  font-size: 1.125rem;
  font-weight: 600;
  margin-bottom: 0.5rem;
}

.ProseMirror ul,
.ProseMirror ol {
  padding-left: 1.5rem;
  margin: 0.5rem 0;
}

.ProseMirror ul {
  list-style-type: disc;
}

.ProseMirror ol {
  list-style-type: decimal;
}

.ProseMirror blockquote {
  border-left: 3px solid #e5e7eb;
  padding-left: 1rem;
  margin: 0.5rem 0;
  color: #6b7280;
}

.ProseMirror code {
  background-color: #f3f4f6;
  padding: 0.125rem 0.25rem;
  border-radius: 0.25rem;
  font-family: monospace;
}

.ProseMirror hr {
  border: none;
  border-top: 2px solid #e5e7eb;
  margin: 1rem 0;
}
</style>
