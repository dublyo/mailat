<script setup lang="ts">
import { RouterView } from 'vue-router'
import { defineAsyncComponent } from 'vue'
import { useInboxStore } from '@/stores/inbox'
// Load the rich text editor only when composing, keeping initial inbox startup small.
const ComposeModal = defineAsyncComponent(() => import('@/components/inbox/ComposeModal.vue'))
const inbox = useInboxStore()
</script>

<template>
  <RouterView />
  <ComposeModal v-if="inbox.isComposeOpen" />
</template>

<style>
#app {
  min-height: 100vh;
}
</style>
