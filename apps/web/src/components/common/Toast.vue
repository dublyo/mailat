<script setup lang="ts">
import { computed, onMounted, onUnmounted } from 'vue'
import { X, CheckCircle, AlertCircle, Info, AlertTriangle } from 'lucide-vue-next'

interface Props {
  type?: 'success' | 'error' | 'info' | 'warning'
  message: string
  duration?: number
}

const props = withDefaults(defineProps<Props>(), {
  type: 'info',
  duration: 5000
})

const emit = defineEmits<{
  close: []
}>()

const icons = {
  success: CheckCircle,
  error: AlertCircle,
  info: Info,
  warning: AlertTriangle
}

const colors = {
  success: 'bg-green-600',
  error: 'bg-red-600',
  info: 'bg-gmail-gray',
  warning: 'bg-yellow-600'
}

const icon = computed(() => icons[props.type])
const bgColor = computed(() => colors[props.type])

let timeout: ReturnType<typeof setTimeout> | null = null

onMounted(() => {
  if (props.duration > 0) {
    timeout = setTimeout(() => {
      emit('close')
    }, props.duration)
  }
})

onUnmounted(() => {
  if (timeout) {
    clearTimeout(timeout)
  }
})
</script>

<template>
  <div
    :class="bgColor"
    class="fixed bottom-4 left-1/2 -translate-x-1/2 flex items-center gap-3 px-4 py-3 rounded-lg shadow-lg text-white z-50 max-w-md"
  >
    <component :is="icon" class="w-5 h-5 shrink-0" />
    <span class="flex-1">{{ message }}</span>
    <button @click="emit('close')" class="p-1 hover:bg-white/20 rounded">
      <X class="w-4 h-4" />
    </button>
  </div>
</template>
