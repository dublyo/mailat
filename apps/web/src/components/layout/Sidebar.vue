<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  Inbox, Star, Send, FileText, Trash2, AlertCircle,
  Mail, BarChart3, Users, Globe, Activity, Settings, Plus,
  ChevronDown, ChevronRight, Zap, Archive, AtSign, Layers
} from 'lucide-vue-next'
import { useReceivedInboxStore } from '@/stores/receivedInbox'
import { useDomainsStore } from '@/stores/domains'
import { useAuthStore } from '@/stores/auth'
import { visibleFor, type Audience } from '@/lib/roles'
import { folderLocation, scopeFromQuery, scopeLocation, showScopeTree } from '@/lib/mailboxScopes'

const route = useRoute()
const router = useRouter()
const receivedInboxStore = useReceivedInboxStore()
const domainsStore = useDomainsStore()
const authStore = useAuthStore()
// Shared mailboxes the user reads; each opens the inbox filtered to it.
const sharedIdentities = computed(() => domainsStore.identities.filter(i => i.shared && i.canRead !== false))
const isSharedActive = (id: string | number) => ['/received', '/inbox'].includes(route.path) && String(route.query.identity || '') === String(id)
onMounted(() => {
  if (!domainsStore.identities.length) void domainsStore.fetchIdentities()
  void receivedInboxStore.fetchScopes()
})

// "All inboxes" → domain → address, each with its unread count. Shown once
// there is more than one address to choose between.
const scopes = computed(() => receivedInboxStore.scopes)
const showTree = computed(() => showScopeTree(scopes.value))
const onMailPage = computed(() => ['/received', '/inbox'].includes(route.path))
const selectedScope = computed(() => onMailPage.value ? scopeFromQuery(route.query) : { identityId: 0, domainId: 0 })
const EXPANDED_KEY = 'mailat.sidebar.expandedDomains'
function readExpanded(): number[] {
  try { const v = JSON.parse(localStorage.getItem(EXPANDED_KEY) || '[]'); return Array.isArray(v) ? v.filter(Number.isInteger) : [] } catch { return [] }
}
const expanded = ref(new Set<number>(readExpanded()))
// Domains the user closed while one of their addresses was selected.
const collapsed = ref(new Set<number>())
type TreeDomain = { domainId: number; identities: { identityId: number }[] }
const holdsSelection = (domain: TreeDomain) => domain.identities.some(i => i.identityId === selectedScope.value.identityId)
// The domain holding the selected address opens by itself unless closed here.
const isExpanded = (domain: TreeDomain) =>
  expanded.value.has(domain.domainId) || (holdsSelection(domain) && !collapsed.value.has(domain.domainId))
function toggleDomain(domain: TreeDomain) {
  const open = isExpanded(domain)
  const next = new Set(expanded.value), shut = new Set(collapsed.value)
  if (open) { next.delete(domain.domainId); shut.add(domain.domainId) } else { next.add(domain.domainId); shut.delete(domain.domainId) }
  expanded.value = next
  collapsed.value = shut
  try { localStorage.setItem(EXPANDED_KEY, JSON.stringify([...next])) } catch { /* per-browser convenience only */ }
}
// Highlighted only on the Inbox folder. The folder list keeps aria-current
// "page"; the tree marks the selected scope as the current location instead,
// so assistive technology never hears two current pages.
const onInbox = computed(() => onMailPage.value && String(route.query.folder || 'inbox') === 'inbox')
const isAllInboxesActive = computed(() => onInbox.value && !selectedScope.value.identityId && !selectedScope.value.domainId)
const isDomainActive = (domainId: number) => onInbox.value && selectedScope.value.domainId === domainId
const isIdentityActive = (identityId: number) => onInbox.value && selectedScope.value.identityId === identityId
const openScope = (scope: { identityId?: number; domainId?: number }) => router.push(scopeLocation(scope))
const localPart = (email: string) => email.slice(0, email.lastIndexOf('@')) || email

type NavItem = { id: string; label: string; icon: typeof Inbox; route: string; folder?: string; audience?: Audience }

const allMainNavItems: NavItem[] = [
  { id: 'inbox', label: 'Inbox', icon: Inbox, route: '/received', folder: 'inbox', audience: 'all' },
  { id: 'dmarc-reports', label: 'Reports', icon: FileText, route: '/received?folder=dmarc-reports', folder: 'dmarc-reports', audience: 'all' },
  { id: 'all', label: 'All Mail', icon: Mail, route: '/received?folder=all', folder: 'all', audience: 'all' },
  { id: 'starred', label: 'Starred', icon: Star, route: '/received?folder=starred', folder: 'starred', audience: 'all' },
  { id: 'sent', label: 'Sent', icon: Send, route: '/received?folder=sent', folder: 'sent', audience: 'all' },
  { id: 'drafts', label: 'Drafts', icon: FileText, route: '/received?folder=drafts', folder: 'drafts', audience: 'all' },
  { id: 'outbox', label: 'Outbox', icon: Send, route: '/received?folder=outbox', folder: 'outbox', audience: 'all' },
]
const mainNavItems = computed(() => visibleFor(allMainNavItems, authStore.user))

const moreNavItems = [
  { id: 'archive', label: 'Archive', icon: Archive, route: '/received?folder=archive', folder: 'archive' },
  { id: 'spam', label: 'Spam', icon: AlertCircle, route: '/received?folder=spam', folder: 'spam' },
  { id: 'trash', label: 'Trash', icon: Trash2, route: '/received?folder=trash', folder: 'trash' },
]

// Mailbox users see mail only; Health is org-wide and admin-only.
const allAppNavItems: NavItem[] = [
  // Migadu-style: one place to create and manage mailbox logins on any domain.
  { id: 'mailboxes', label: 'Mailboxes', icon: AtSign, route: '/mailboxes', audience: 'admin' },
  { id: 'campaigns', label: 'Campaigns', icon: Mail, route: '/campaigns', audience: 'staff' },
  { id: 'automations', label: 'Automations', icon: Zap, route: '/automations', audience: 'staff' },
  { id: 'contacts', label: 'Contacts', icon: Users, route: '/contacts', audience: 'staff' },
  { id: 'forms', label: 'Signup forms', icon: FileText, route: '/forms', audience: 'staff' },
  { id: 'domains', label: 'Domains', icon: Globe, route: '/domains', audience: 'staff' },
  { id: 'health', label: 'Health', icon: Activity, route: '/health', audience: 'admin' },
  { id: 'api', label: 'API', icon: BarChart3, route: '/api-docs', audience: 'staff' },
]
const appNavItems = computed(() => visibleFor(allAppNavItems, authStore.user))

const isMailboxPage = () => route.path === '/mailboxes' || /^\/domains\/[^/]+\/mailboxes(\/|$)/.test(route.path)
const isActive = (itemRoute: string, folder?: string) => {
  // Mailbox pages live under /domains/:uuid but belong to the Mailboxes item.
  if (itemRoute === '/mailboxes') return isMailboxPage()
  if (itemRoute === '/domains' && isMailboxPage()) return false
  // For received inbox, check both path and folder query param
  if (itemRoute.startsWith('/received')) {
    if (!['/received', '/inbox'].includes(route.path)) return false
    const currentFolder = route.query.folder as string || 'inbox'
    return currentFolder === (folder || 'inbox')
  }
  return route.path === itemRoute || route.path.startsWith(itemRoute + '/')
}

// Mail folders keep the selected domain or address; other pages open plainly.
const navigateTo = (item: { route: string }) => {
  if (item.route.startsWith('/received') && onMailPage.value) router.push(folderLocation(item.route, route.query))
  else router.push(item.route)
}

const getUnreadCount = (folderId: string) => {
  // Use the received inbox store counts
  const counts = receivedInboxStore.counts
  if (!counts) return 0
  switch (folderId) {
    case 'inbox': return counts.inboxUnread ?? 0
    case 'dmarc-reports': return counts.dmarcReportsUnread ?? 0
    case 'all': return counts.unread ?? 0
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
            :aria-current="isActive(item.route, item.folder) ? 'page' : undefined"
            :class="{ 'active': isActive(item.route, item.folder) }"
            class="nav-item w-full"
          >
            <component :is="item.icon" class="w-5 h-5 text-gmail-gray" />
            <span class="flex-1 text-left text-sm">{{ item.label }}</span>
            <span v-if="getUnreadCount(item.id) > 0" :aria-label="`${getUnreadCount(item.id)} ${item.id === 'drafts' || item.id === 'starred' ? 'messages' : 'unread'}`" class="text-xs font-medium">
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

      <template v-if="showTree && scopes">
        <h2 class="px-6 pt-4 pb-1 text-xs font-medium uppercase tracking-wide text-gmail-gray">Inboxes</h2>
        <ul class="space-y-0.5" aria-label="Inboxes by domain and address">
          <li>
            <button
              @click="openScope({})"
              :aria-current="isAllInboxesActive ? 'location' : undefined"
              :class="{ 'active': isAllInboxesActive }"
              class="nav-item w-full"
            >
              <Layers class="w-5 h-5 text-gmail-gray" />
              <span class="flex-1 text-left text-sm">All inboxes</span>
              <span v-if="scopes.unread > 0" :aria-label="`${scopes.unread} unread`" class="text-xs font-medium">{{ scopes.unread }}</span>
            </button>
          </li>
          <li v-for="domain in scopes.domains" :key="domain.domainId">
            <div class="flex items-center">
              <button
                type="button"
                @click="toggleDomain(domain)"
                :aria-expanded="isExpanded(domain)"
                :aria-label="`${isExpanded(domain) ? 'Hide' : 'Show'} addresses on ${domain.name}`"
                class="ml-3 p-1 rounded hover:bg-gmail-hover text-gmail-gray"
              >
                <component :is="isExpanded(domain) ? ChevronDown : ChevronRight" class="w-4 h-4" />
              </button>
              <button
                @click="openScope({ domainId: domain.domainId })"
                :aria-current="isDomainActive(domain.domainId) ? 'location' : undefined"
                :class="{ 'active': isDomainActive(domain.domainId) }"
                :title="`Inbox for ${domain.name}`"
                class="nav-item flex-1 min-w-0 !pl-2 !gap-2"
              >
                <Globe class="w-4 h-4 text-gmail-gray shrink-0" />
                <span class="flex-1 text-left text-sm truncate">{{ domain.name }}</span>
                <span v-if="domain.unread > 0" :aria-label="`${domain.unread} unread`" class="text-xs font-medium">{{ domain.unread }}</span>
              </button>
            </div>
            <ul v-if="isExpanded(domain)" class="space-y-0.5">
              <li v-for="identity in domain.identities" :key="identity.identityId">
                <button
                  @click="openScope({ identityId: identity.identityId })"
                  :aria-current="isIdentityActive(identity.identityId) ? 'location' : undefined"
                  :class="{ 'active': isIdentityActive(identity.identityId) }"
                  :title="identity.email + (identity.isCatchAll ? ' (catch-all: also gets mail for unknown addresses)' : '') + (identity.kind === 'shared' ? ' (shared mailbox)' : '')"
                  class="nav-item w-full !pl-14 !gap-2"
                >
                  <Users v-if="identity.kind === 'shared'" class="w-4 h-4 text-gmail-gray shrink-0" />
                  <span v-else class="w-2.5 h-2.5 rounded-full shrink-0" :style="{ backgroundColor: identity.color || '#9CA3AF' }" aria-hidden="true" />
                  <span class="flex-1 text-left text-sm truncate">{{ localPart(identity.email) }}</span>
                  <span v-if="identity.isCatchAll" class="text-[10px] text-gray-500 rounded bg-gray-100 px-1">catch-all</span>
                  <span v-if="identity.unread > 0" :aria-label="`${identity.unread} unread`" class="text-xs font-medium">{{ identity.unread }}</span>
                </button>
              </li>
            </ul>
          </li>
        </ul>
      </template>

      <template v-else-if="sharedIdentities.length">
        <h2 class="px-6 pt-4 pb-1 text-xs font-medium uppercase tracking-wide text-gmail-gray">Shared</h2>
        <ul class="space-y-0.5">
          <li v-for="identity in sharedIdentities" :key="identity.uuid">
            <button
              @click="router.push({ path: '/received', query: { identity: String(identity.id) } })"
              :aria-current="isSharedActive(identity.id) ? 'page' : undefined"
              :class="{ 'active': isSharedActive(identity.id) }"
              :title="identity.email"
              class="nav-item w-full"
            >
              <Users class="w-5 h-5 text-gmail-gray" />
              <span class="flex-1 text-left text-sm truncate">{{ identity.sharedMailboxName || identity.email }}</span>
            </button>
          </li>
        </ul>
      </template>

      <!-- Divider -->
      <div v-if="appNavItems.length" class="my-4 border-t border-gmail-border" />

      <!-- App navigation -->
      <ul v-if="appNavItems.length" class="space-y-0.5">
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
