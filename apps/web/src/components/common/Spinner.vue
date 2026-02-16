<script setup lang="ts">
import { computed } from 'vue'

interface Props {
  size?: 'sm' | 'md' | 'lg'
}

const props = withDefaults(defineProps<Props>(), {
  size: 'md'
})

const sizeClasses = computed(() => {
  switch (props.size) {
    case 'sm': return 'w-4 h-4 border'
    case 'lg': return 'w-8 h-8 border-[3px]'
    default: return 'w-6 h-6 border-2'
  }
})
</script>

<template>
  <div
    :class="sizeClasses"
    class="border-gmail-blue border-t-transparent rounded-full animate-spin"
  />
</template>
