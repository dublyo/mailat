<script setup lang="ts">
import { ref } from 'vue'
import Header from './Header.vue'
import Sidebar from './Sidebar.vue'
import ComposeModal from '@/components/inbox/ComposeModal.vue'
import { useInboxStore } from '@/stores/inbox'

const inboxStore = useInboxStore()
const isSidebarOpen = ref(true)

const toggleSidebar = () => {
  isSidebarOpen.value = !isSidebarOpen.value
}

const openCompose = () => {
  inboxStore.openCompose('new')
}
</script>

<template>
  <div class="h-screen flex flex-col bg-gmail-lightGray">
    <Header
      :sidebar-open="isSidebarOpen"
      @toggle-sidebar="toggleSidebar"
    />

    <div class="flex-1 flex overflow-hidden">
      <!-- Sidebar -->
      <Transition name="slide">
        <Sidebar
          v-show="isSidebarOpen"
          @compose="openCompose"
        />
      </Transition>

      <!-- Main content -->
      <main class="flex-1 flex bg-white rounded-tl-2xl overflow-hidden">
        <slot />
      </main>
    </div>

    <!-- Compose Modal -->
    <ComposeModal />
  </div>
</template>

<style scoped>
.slide-enter-active,
.slide-leave-active {
  transition: all 0.2s ease;
}

.slide-enter-from,
.slide-leave-to {
  transform: translateX(-100%);
  opacity: 0;
}
</style>
