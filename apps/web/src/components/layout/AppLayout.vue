<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import Header from './Header.vue'
import Sidebar from './Sidebar.vue'
import { useInboxStore } from '@/stores/inbox'
const inboxStore = useInboxStore()
const route = useRoute()
const mobile = ref(window.innerWidth < 1024)
const isSidebarOpen = ref(!mobile.value)
function resize() { const next = window.innerWidth < 1024; if (next !== mobile.value) isSidebarOpen.value = !next; mobile.value = next }
function compose() { inboxStore.openCompose('new'); if (mobile.value) isSidebarOpen.value = false }
watch(() => route.fullPath, () => { if (mobile.value) isSidebarOpen.value = false })
onMounted(() => window.addEventListener('resize', resize))
onUnmounted(() => window.removeEventListener('resize', resize))
</script>

<template>
  <div class="h-[100dvh] flex flex-col bg-gmail-lightGray overflow-hidden">
    <Header :sidebar-open="isSidebarOpen" @toggle-sidebar="isSidebarOpen = !isSidebarOpen" />
    <div class="flex-1 min-h-0 flex relative overflow-hidden">
      <button v-if="mobile && isSidebarOpen" @click="isSidebarOpen = false" class="fixed inset-x-0 bottom-0 top-16 z-30 bg-black/30" aria-label="Close navigation" />
      <Sidebar v-if="isSidebarOpen" @compose="compose" :class="mobile ? 'absolute inset-y-0 left-0 z-40 shadow-xl' : 'shrink-0'" />
      <main class="flex-1 min-w-0 min-h-0 flex bg-white lg:rounded-tl-2xl overflow-hidden"><slot /></main>
    </div>
  </div>
</template>
