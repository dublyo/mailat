<script setup lang="ts">
import { computed } from 'vue'
import Spinner from './Spinner.vue'

interface Props {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger'
  size?: 'sm' | 'md' | 'lg'
  disabled?: boolean
  loading?: boolean
  type?: 'button' | 'submit' | 'reset'
  icon?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  variant: 'primary',
  size: 'md',
  disabled: false,
  loading: false,
  type: 'button',
  icon: false
})

const emit = defineEmits<{
  click: [event: MouseEvent]
}>()

const variantClasses = computed(() => {
  switch (props.variant) {
    case 'primary':
      return 'bg-gmail-blue text-white hover:bg-blue-700 shadow-md'
    case 'secondary':
      return 'bg-white text-gmail-gray border border-gmail-border hover:bg-gmail-hover'
    case 'ghost':
      return 'text-gmail-gray hover:bg-gmail-hover'
    case 'danger':
      return 'bg-red-600 text-white hover:bg-red-700'
    default:
      return 'bg-gmail-blue text-white hover:bg-blue-700'
  }
})

const sizeClasses = computed(() => {
  if (props.icon) {
    switch (props.size) {
      case 'sm': return 'p-1.5'
      case 'lg': return 'p-3'
      default: return 'p-2'
    }
  }
  switch (props.size) {
    case 'sm': return 'px-3 py-1.5 text-sm'
    case 'lg': return 'px-8 py-3 text-lg'
    default: return 'px-4 py-2 text-sm'
  }
})

const handleClick = (event: MouseEvent) => {
  if (!props.disabled && !props.loading) {
    emit('click', event)
  }
}
</script>

<template>
  <button
    :type="type"
    :disabled="disabled || loading"
    :class="[
      variantClasses,
      sizeClasses,
      icon ? 'rounded-full' : 'rounded-lg',
      { 'opacity-50 cursor-not-allowed': disabled || loading }
    ]"
    class="inline-flex items-center justify-center gap-2 font-medium transition-all duration-200"
    @click="handleClick"
  >
    <Spinner v-if="loading" size="sm" />
    <slot />
  </button>
</template>
