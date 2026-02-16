<script setup lang="ts">
import { computed } from 'vue'

interface Props {
  name?: string
  email?: string
  src?: string
  size?: 'sm' | 'md' | 'lg' | 'xl'
}

const props = withDefaults(defineProps<Props>(), {
  size: 'md'
})

const initials = computed(() => {
  if (props.name) {
    return props.name
      .split(' ')
      .map(n => n[0])
      .join('')
      .toUpperCase()
      .slice(0, 2)
  }
  if (props.email) {
    return props.email[0].toUpperCase()
  }
  return '?'
})

const bgColor = computed(() => {
  const str = props.email || props.name || ''
  const colors = [
    'bg-red-500', 'bg-orange-500', 'bg-amber-500', 'bg-yellow-500',
    'bg-lime-500', 'bg-green-500', 'bg-emerald-500', 'bg-teal-500',
    'bg-cyan-500', 'bg-sky-500', 'bg-blue-500', 'bg-indigo-500',
    'bg-violet-500', 'bg-purple-500', 'bg-fuchsia-500', 'bg-pink-500'
  ]
  let hash = 0
  for (let i = 0; i < str.length; i++) {
    hash = str.charCodeAt(i) + ((hash << 5) - hash)
  }
  return colors[Math.abs(hash) % colors.length]
})

const sizeClasses = computed(() => {
  switch (props.size) {
    case 'sm': return 'w-6 h-6 text-xs'
    case 'md': return 'w-8 h-8 text-sm'
    case 'lg': return 'w-10 h-10 text-base'
    case 'xl': return 'w-14 h-14 text-lg'
    default: return 'w-8 h-8 text-sm'
  }
})
</script>

<template>
  <div
    v-if="!src"
    :class="[sizeClasses, bgColor]"
    class="rounded-full flex items-center justify-center text-white font-medium shrink-0"
  >
    {{ initials }}
  </div>
  <img
    v-else
    :src="src"
    :alt="name || email || 'Avatar'"
    :class="sizeClasses"
    class="rounded-full object-cover shrink-0"
  />
</template>
