<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import Header from './Header.vue'
import Sidebar from './Sidebar.vue'
import { useInboxStore } from '@/stores/inbox'
import { useSettingsStore } from '@/stores/settings'
const inboxStore = useInboxStore()
const settingsStore = useSettingsStore()
// Rules older versions kept only in this browser were never applied; offer an
// opt-in import instead of migrating them silently.
const localRules = computed(() => settingsStore.localRules)
const showLocalRulesNotice = computed(() => (localRules.value.blockedSenders.length > 0 || localRules.value.filters.length > 0) && !(route.path === '/settings' && route.query.tab === 'filters'))
const route = useRoute()
const mobile = ref(window.innerWidth < 1024)
const isSidebarOpen = ref(!mobile.value)
function resize() { const next = window.innerWidth < 1024; if (next !== mobile.value) isSidebarOpen.value = !next; mobile.value = next }
function compose() { inboxStore.openCompose('new'); if (mobile.value) isSidebarOpen.value = false }
watch(() => route.fullPath, () => { if (mobile.value) isSidebarOpen.value = false })
onMounted(() => { window.addEventListener('resize', resize); settingsStore.loadLocalMailRules() })
onUnmounted(() => window.removeEventListener('resize', resize))
</script>

<template>
  <div class="h-[100dvh] flex flex-col bg-gmail-lightGray overflow-hidden">
    <Header :sidebar-open="isSidebarOpen" @toggle-sidebar="isSidebarOpen = !isSidebarOpen" />
    <div v-if="showLocalRulesNotice" role="status" class="px-4 py-2 text-sm bg-amber-50 text-amber-900 border-b border-amber-200 flex flex-wrap gap-x-3">
      <span>{{ localRules.blockedSenders.length }} blocked sender{{ localRules.blockedSenders.length === 1 ? '' : 's' }} and {{ localRules.filters.length }} filter{{ localRules.filters.length === 1 ? ' were' : 's were' }} saved in this browser only and never applied.</span>
      <router-link to="/settings?tab=filters" class="underline">Review in Settings</router-link>
    </div>
    <div class="flex-1 min-h-0 flex relative overflow-hidden">
      <button v-if="mobile && isSidebarOpen" @click="isSidebarOpen = false" class="fixed inset-x-0 bottom-0 top-16 z-30 bg-black/30" aria-label="Close navigation" />
      <Sidebar v-if="isSidebarOpen" @compose="compose" :class="mobile ? 'absolute inset-y-0 left-0 z-40 shadow-xl' : 'shrink-0'" />
      <main class="flex-1 min-w-0 min-h-0 flex bg-white lg:rounded-tl-2xl overflow-hidden"><slot /></main>
    </div>
  </div>
</template>
