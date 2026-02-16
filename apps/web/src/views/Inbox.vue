<script setup lang="ts">
import { computed } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import MessageList from '@/components/inbox/MessageList.vue'
import MessageView from '@/components/inbox/MessageView.vue'
import { useInboxStore } from '@/stores/inbox'

const inboxStore = useInboxStore()

const showMessageView = computed(() => !!inboxStore.activeEmail)
</script>

<template>
  <AppLayout>
    <MessageView v-if="showMessageView" />
    <MessageList v-else />
  </AppLayout>
</template>
