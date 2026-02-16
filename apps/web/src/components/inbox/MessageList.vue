<script setup lang="ts">
import { onMounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import {
  Archive, Trash2, Mail, MailOpen, MoreVertical, RefreshCw,
  ChevronLeft, ChevronRight
} from 'lucide-vue-next'
import { useInboxStore } from '@/stores/inbox'
import MessageListItem from './MessageListItem.vue'
import Spinner from '@/components/common/Spinner.vue'

const route = useRoute()
const inboxStore = useInboxStore()

onMounted(() => {
  inboxStore.fetchEmails()
})

watch(() => route.params.folder, (folder) => {
  if (folder) {
    inboxStore.setCurrentFolder(folder as string)
    inboxStore.fetchEmails()
  }
})

const handleMarkRead = async () => {
  if (!inboxStore.hasSelection) return
  await inboxStore.markAsRead(Array.from(inboxStore.selectedEmails))
  inboxStore.clearSelection()
}

const handleMarkUnread = async () => {
  if (!inboxStore.hasSelection) return
  await inboxStore.markAsUnread(Array.from(inboxStore.selectedEmails))
  inboxStore.clearSelection()
}

const handleDelete = async () => {
  if (!inboxStore.hasSelection) return
  await inboxStore.deleteEmails(Array.from(inboxStore.selectedEmails))
}

const handleArchive = async () => {
  if (!inboxStore.hasSelection) return
  await inboxStore.moveToFolder(Array.from(inboxStore.selectedEmails), 'archive')
}

const toggleSelectAll = () => {
  if (inboxStore.allSelected) {
    inboxStore.clearSelection()
  } else {
    inboxStore.selectAll()
  }
}

const refresh = () => {
  inboxStore.fetchEmails()
}

const prevPage = () => {
  if (inboxStore.page > 1) {
    inboxStore.page--
    inboxStore.fetchEmails()
  }
}

const nextPage = () => {
  if (inboxStore.page * 50 < inboxStore.totalEmails) {
    inboxStore.page++
    inboxStore.fetchEmails()
  }
}
</script>

<template>
  <div class="flex-1 flex flex-col">
    <!-- Toolbar -->
    <div class="flex items-center gap-2 px-4 py-2 border-b border-gmail-border">
      <div class="flex items-center gap-1">
        <input
          type="checkbox"
          :checked="inboxStore.allSelected"
          @change="toggleSelectAll"
          class="gmail-checkbox"
        />
      </div>

      <template v-if="inboxStore.hasSelection">
        <button
          @click="handleArchive"
          class="p-2 hover:bg-gmail-hover rounded-full"
          title="Archive"
        >
          <Archive class="w-5 h-5 text-gmail-gray" />
        </button>
        <button
          @click="handleDelete"
          class="p-2 hover:bg-gmail-hover rounded-full"
          title="Delete"
        >
          <Trash2 class="w-5 h-5 text-gmail-gray" />
        </button>
        <button
          @click="handleMarkRead"
          class="p-2 hover:bg-gmail-hover rounded-full"
          title="Mark as read"
        >
          <MailOpen class="w-5 h-5 text-gmail-gray" />
        </button>
        <button
          @click="handleMarkUnread"
          class="p-2 hover:bg-gmail-hover rounded-full"
          title="Mark as unread"
        >
          <Mail class="w-5 h-5 text-gmail-gray" />
        </button>
      </template>
      <template v-else>
        <button
          @click="refresh"
          class="p-2 hover:bg-gmail-hover rounded-full"
          title="Refresh"
        >
          <RefreshCw class="w-5 h-5 text-gmail-gray" />
        </button>
      </template>

      <button class="p-2 hover:bg-gmail-hover rounded-full ml-auto" title="More">
        <MoreVertical class="w-5 h-5 text-gmail-gray" />
      </button>

      <!-- Pagination -->
      <div class="flex items-center gap-2 text-sm text-gmail-gray">
        <span>
          {{ (inboxStore.page - 1) * 50 + 1 }}-{{ Math.min(inboxStore.page * 50, inboxStore.totalEmails) }}
          of {{ inboxStore.totalEmails }}
        </span>
        <button
          @click="prevPage"
          :disabled="inboxStore.page === 1"
          class="p-1 hover:bg-gmail-hover rounded disabled:opacity-50"
        >
          <ChevronLeft class="w-5 h-5" />
        </button>
        <button
          @click="nextPage"
          :disabled="inboxStore.page * 50 >= inboxStore.totalEmails"
          class="p-1 hover:bg-gmail-hover rounded disabled:opacity-50"
        >
          <ChevronRight class="w-5 h-5" />
        </button>
      </div>
    </div>

    <!-- Email list -->
    <div class="flex-1 overflow-y-auto">
      <!-- Loading -->
      <div v-if="inboxStore.isLoading" class="flex items-center justify-center h-full">
        <Spinner size="lg" />
      </div>

      <!-- Empty state -->
      <div
        v-else-if="inboxStore.emails.length === 0"
        class="flex flex-col items-center justify-center h-full text-gmail-gray"
      >
        <Mail class="w-16 h-16 mb-4 opacity-50" />
        <p class="text-lg">No emails in {{ inboxStore.currentFolder }}</p>
      </div>

      <!-- Email rows -->
      <ul v-else>
        <MessageListItem
          v-for="email in inboxStore.emails"
          :key="email.uuid"
          :email="email"
          :selected="inboxStore.selectedEmails.has(email.uuid)"
          @select="inboxStore.toggleEmailSelection(email.uuid)"
          @click="inboxStore.setActiveEmail(email)"
          @star="inboxStore.toggleStar(email.uuid)"
        />
      </ul>
    </div>
  </div>
</template>
