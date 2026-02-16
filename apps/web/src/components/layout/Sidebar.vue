<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  Inbox, Star, Send, FileText, Trash2, AlertCircle,
  Mail, BarChart3, Users, Globe, Activity, Settings, Plus,
  ChevronDown, ChevronRight, Zap
} from 'lucide-vue-next'
import { useReceivedInboxStore } from '@/stores/receivedInbox'

const route = useRoute()
const router = useRouter()
const receivedInboxStore = useReceivedInboxStore()

const mainNavItems = [
  { id: 'inbox', label: 'Inbox', icon: Inbox, route: '/received', folder: 'inbox' },
  { id: 'starred', label: 'Starred', icon: Star, route: '/received?folder=starred', folder: 'starred' },
  { id: 'sent', label: 'Sent', icon: Send, route: '/received?folder=sent', folder: 'sent' },
  { id: 'drafts', label: 'Drafts', icon: FileText, route: '/received?folder=drafts', folder: 'drafts' },
]

const moreNavItems = [
  { id: 'spam', label: 'Spam', icon: AlertCircle, route: '/received?folder=spam', folder: 'spam' },
  { id: 'trash', label: 'Trash', icon: Trash2, route: '/received?folder=trash', folder: 'trash' },
]

const appNavItems = [
  { id: 'campaigns', label: 'Campaigns', icon: Mail, route: '/campaigns' },
  { id: 'automations', label: 'Automations', icon: Zap, route: '/automations' },
  { id: 'contacts', label: 'Contacts', icon: Users, route: '/contacts' },
  { id: 'domains', label: 'Domains', icon: Globe, route: '/domains' },
  { id: 'health', label: 'Health', icon: Activity, route: '/health' },
  { id: 'api', label: 'API', icon: BarChart3, route: '/api-docs' },
]

const isActive = (itemRoute: string, folder?: string) => {
  // For received inbox, check both path and folder query param
  if (itemRoute.startsWith('/received')) {
    if (route.path !== '/received') return false
    const currentFolder = route.query.folder as string || 'inbox'
    return currentFolder === (folder || 'inbox')
  }
  return route.path === itemRoute || route.path.startsWith(itemRoute + '/')
}

const navigateTo = (item: { route: string }) => {
  router.push(item.route)
}

const getUnreadCount = (folderId: string) => {
  // Use the received inbox store counts
  const counts = receivedInboxStore.counts
  if (!counts) return 0
  switch (folderId) {
    case 'inbox': return counts.inbox || 0
    case 'starred': return counts.starred || 0
    case 'drafts': return counts.drafts || 0
    default: return 0
  }
}

const emit = defineEmits<{
  compose: []
}>()
</script>

<template>
  <aside class="w-64 h-full bg-white flex flex-col border-r border-gmail-border">
    <!-- Compose Button -->
    <div class="p-4">
      <button
        @click="emit('compose')"
        class="flex items-center gap-3 px-6 py-3 bg-white border border-gmail-border rounded-2xl shadow-md hover:shadow-lg transition-shadow"
      >
        <Plus class="w-5 h-5 text-gmail-gray" />
        <span class="font-medium text-gmail-gray">Compose</span>
      </button>
    </div>

    <!-- Navigation -->
    <nav class="flex-1 overflow-y-auto">
      <!-- Main folders -->
      <ul class="space-y-0.5">
        <li v-for="item in mainNavItems" :key="item.id">
          <button
            @click="navigateTo(item)"
            :class="{ 'active': isActive(item.route, item.folder) }"
            class="nav-item w-full"
          >
            <component :is="item.icon" class="w-5 h-5 text-gmail-gray" />
            <span class="flex-1 text-left text-sm">{{ item.label }}</span>
            <span v-if="getUnreadCount(item.id) > 0" class="text-xs font-medium">
              {{ getUnreadCount(item.id) }}
            </span>
          </button>
        </li>
      </ul>

      <!-- More folders -->
      <details class="mt-2 group">
        <summary class="flex items-center gap-4 px-6 py-2 cursor-pointer hover:bg-gmail-hover text-sm text-gmail-gray">
          <ChevronRight class="w-4 h-4 group-open:hidden" />
          <ChevronDown class="w-4 h-4 hidden group-open:block" />
          <span>More</span>
        </summary>
        <ul class="space-y-0.5">
          <li v-for="item in moreNavItems" :key="item.id">
            <button
              @click="navigateTo(item)"
              :class="{ 'active': isActive(item.route, item.folder) }"
              class="nav-item w-full"
            >
              <component :is="item.icon" class="w-5 h-5 text-gmail-gray" />
              <span class="flex-1 text-left text-sm">{{ item.label }}</span>
            </button>
          </li>
        </ul>
      </details>

      <!-- Divider -->
      <div class="my-4 border-t border-gmail-border" />

      <!-- App navigation -->
      <ul class="space-y-0.5">
        <li v-for="item in appNavItems" :key="item.id">
          <button
            @click="navigateTo(item)"
            :class="{ 'active': isActive(item.route) }"
            class="nav-item w-full"
          >
            <component :is="item.icon" class="w-5 h-5 text-gmail-gray" />
            <span class="flex-1 text-left text-sm">{{ item.label }}</span>
          </button>
        </li>
      </ul>
    </nav>

    <!-- Settings -->
    <div class="border-t border-gmail-border p-2">
      <button
        @click="router.push('/settings')"
        :class="{ 'active': isActive('/settings') }"
        class="nav-item w-full"
      >
        <Settings class="w-5 h-5 text-gmail-gray" />
        <span class="text-sm text-gmail-gray">Settings</span>
      </button>
    </div>
  </aside>
</template>
