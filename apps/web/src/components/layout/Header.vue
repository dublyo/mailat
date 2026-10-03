<script setup lang="ts">
import { ref, watch, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Search, Menu, Settings, X } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import Avatar from '@/components/common/Avatar.vue'
import Dropdown from '@/components/common/Dropdown.vue'
defineProps<{ sidebarOpen?: boolean }>()
const emit = defineEmits<{ toggleSidebar: [] }>()
const router = useRouter()
const route = useRoute()
const authStore = useAuthStore()
const searchQuery = ref(String(route.query.q || ''))
const isSearchFocused = ref(false)
let searchTimer: ReturnType<typeof setTimeout> | undefined
watch(() => route.query.q, value => { searchQuery.value = String(value || '') })
function handleSearch() {
  clearTimeout(searchTimer)
  const inMailbox = ['/received', '/inbox'].some(path => route.path.startsWith(path))
  void router.replace({ path: '/received', query: { ...(inMailbox ? route.query : {}), q: searchQuery.value.trim() || undefined, page: undefined } })
}
function searchChanged() { clearTimeout(searchTimer); searchTimer = setTimeout(handleSearch, 350) }
function clearSearch() { searchQuery.value = ''; handleSearch() }
function logout() { authStore.logout(); void router.push('/login') }
onUnmounted(() => clearTimeout(searchTimer))
</script>

<template>
  <header class="h-16 bg-white border-b border-gmail-border flex items-center px-2 sm:px-4 gap-2 sm:gap-4 shrink-0">
    <!-- Logo and menu -->
    <div class="flex items-center gap-2">
      <button
        @click="emit('toggleSidebar')"
        aria-label="Toggle navigation"
        :aria-expanded="sidebarOpen"
        class="p-2 hover:bg-gmail-hover rounded-full"
      >
        <Menu class="w-6 h-6 text-gmail-gray" />
      </button>
      <div class="flex items-center gap-2 cursor-pointer" @click="router.push('/')">
        <img src="/logo.jpg" alt="Mailat" class="w-8 h-8 rounded object-contain" />
        <span class="text-xl font-medium text-gmail-gray hidden sm:block">
          Mailat
        </span>
      </div>
    </div>

    <!-- Search bar -->
    <form @submit.prevent="handleSearch" class="flex-1 min-w-0 max-w-2xl" role="search">
      <div
        :class="[
          isSearchFocused
            ? 'bg-white shadow-lg'
            : 'bg-gmail-lightGray hover:shadow-md'
        ]"
        class="flex items-center gap-2 px-3 py-2 rounded-full transition-all"
      >
        <Search class="w-5 h-5 text-gmail-gray shrink-0" />
        <input
          v-model="searchQuery"
          @input="searchChanged"
          aria-label="Search mail"
          type="text"
          placeholder="Search mail"
          @focus="isSearchFocused = true"
          @blur="isSearchFocused = false"
          class="flex-1 min-w-0 w-full bg-transparent outline-none text-sm"
        />
        <button
          v-if="searchQuery"
          type="button"
          @click="clearSearch"
          aria-label="Clear search"
          class="text-gmail-gray hover:text-gmail-blue"
        >
          <X class="w-4 h-4" />
        </button>
      </div>
    </form>

    <!-- Right side actions -->
    <div class="flex items-center gap-1">
      <button
        @click="router.push('/settings')"
        class="hidden sm:block p-2 hover:bg-gmail-hover rounded-full"
        title="Settings"
      >
        <Settings class="w-5 h-5 text-gmail-gray" />
      </button>
      <!-- Profile dropdown -->
      <Dropdown align="right" class="ml-2">
        <template #trigger>
          <button class="rounded-full hover:opacity-90" aria-label="Account menu">
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
