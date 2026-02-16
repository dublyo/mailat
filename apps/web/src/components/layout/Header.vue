<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { Search, Menu, HelpCircle, Settings, Bell } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { useInboxStore } from '@/stores/inbox'
import Avatar from '@/components/common/Avatar.vue'
import Dropdown from '@/components/common/Dropdown.vue'

interface Props {
  sidebarOpen?: boolean
}

defineProps<Props>()

const emit = defineEmits<{
  toggleSidebar: []
}>()

const router = useRouter()
const authStore = useAuthStore()
const inboxStore = useInboxStore()

const searchQuery = ref('')
const isSearchFocused = ref(false)

const handleSearch = (e: Event) => {
  e.preventDefault()
  if (searchQuery.value.trim()) {
    inboxStore.searchEmails(searchQuery.value)
  }
}

const clearSearch = () => {
  searchQuery.value = ''
  inboxStore.searchQuery = ''
  inboxStore.isSearching = false
  inboxStore.fetchEmails()
}

const logout = () => {
  authStore.logout()
  router.push('/login')
}
</script>

<template>
  <header class="h-16 bg-white border-b border-gmail-border flex items-center px-4 gap-4">
    <!-- Logo and menu -->
    <div class="flex items-center gap-2">
      <button
        @click="emit('toggleSidebar')"
        class="p-2 hover:bg-gmail-hover rounded-full"
      >
        <Menu class="w-6 h-6 text-gmail-gray" />
      </button>
      <div class="flex items-center gap-2 cursor-pointer" @click="router.push('/')">
        <div class="w-8 h-8 bg-gmail-red rounded flex items-center justify-center">
          <span class="text-white font-bold text-lg">U</span>
        </div>
        <span class="text-xl font-medium text-gmail-gray hidden sm:block">
          Mailat
        </span>
      </div>
    </div>

    <!-- Search bar -->
    <form @submit="handleSearch" class="flex-1 max-w-2xl">
      <div
        :class="[
          isSearchFocused
            ? 'bg-white shadow-lg'
            : 'bg-gmail-lightGray hover:shadow-md'
        ]"
        class="flex items-center gap-3 px-4 py-2 rounded-full transition-all"
      >
        <Search class="w-5 h-5 text-gmail-gray shrink-0" />
        <input
          v-model="searchQuery"
          type="text"
          placeholder="Search mail"
          @focus="isSearchFocused = true"
          @blur="isSearchFocused = false"
          class="flex-1 bg-transparent outline-none text-sm"
        />
        <button
          v-if="searchQuery"
          type="button"
          @click="clearSearch"
          class="text-gmail-gray hover:text-gmail-blue"
        >
          ×
        </button>
      </div>
    </form>

    <!-- Right side actions -->
    <div class="flex items-center gap-1">
      <button class="p-2 hover:bg-gmail-hover rounded-full" title="Support">
        <HelpCircle class="w-5 h-5 text-gmail-gray" />
      </button>
      <button
        @click="router.push('/settings')"
        class="p-2 hover:bg-gmail-hover rounded-full"
        title="Settings"
      >
        <Settings class="w-5 h-5 text-gmail-gray" />
      </button>
      <button class="p-2 hover:bg-gmail-hover rounded-full relative" title="Notifications">
        <Bell class="w-5 h-5 text-gmail-gray" />
        <span class="absolute top-1 right-1 w-2 h-2 bg-gmail-red rounded-full" />
      </button>

      <!-- Profile dropdown -->
      <Dropdown align="right" class="ml-2">
        <template #trigger>
          <button class="rounded-full hover:opacity-90">
            <Avatar
              :name="authStore.user?.name"
              :email="authStore.user?.email"
              size="md"
            />
          </button>
        </template>

        <div class="p-4 text-center border-b border-gmail-border">
          <Avatar
            :name="authStore.user?.name"
            :email="authStore.user?.email"
            size="xl"
            class="mx-auto mb-2"
          />
          <p class="font-medium">{{ authStore.user?.name || 'User' }}</p>
          <p class="text-sm text-gmail-gray">{{ authStore.user?.email }}</p>
        </div>
        <div class="py-1">
          <button
            @click="router.push('/settings')"
            class="w-full text-left px-4 py-2 hover:bg-gmail-hover text-sm"
          >
            Manage your account
          </button>
          <button
            @click="logout"
            class="w-full text-left px-4 py-2 hover:bg-gmail-hover text-sm text-gmail-red"
          >
            Sign out
          </button>
        </div>
      </Dropdown>
    </div>
  </header>
</template>
