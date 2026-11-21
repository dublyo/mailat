<script setup lang="ts">
import { ref, watch, onMounted, nextTick } from 'vue'
import { PIN_LENGTH, pinDigits } from '@/lib/loginPin'

// Four single-digit boxes: typing moves to the next box, Backspace on an
// empty box moves back, and pasting (or one-time-code autofill) fills them all.
const props = defineProps<{ modelValue: string; label: string; disabled?: boolean; autofocus?: boolean; invalid?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: string]; complete: [value: string] }>()

const cells = ref<string[]>(Array.from({ length: PIN_LENGTH }, (_, i) => props.modelValue[i] || ''))
const boxes = ref<HTMLInputElement[]>([])

watch(() => props.modelValue, value => {
  if (value !== cells.value.join('')) cells.value = Array.from({ length: PIN_LENGTH }, (_, i) => value[i] || '')
  if (!value) nextTick(() => boxes.value[0]?.focus())
})

onMounted(() => { if (props.autofocus) focus(0) })
// A disabled box cannot take focus: focus once the form is usable again.
watch(() => props.disabled, disabled => { if (!disabled && props.autofocus && !cells.value.join('')) focus(0) })

function focus(index: number) {
  nextTick(() => {
    const box = boxes.value[Math.max(0, Math.min(PIN_LENGTH - 1, index))]
    box?.focus()
    box?.select()
  })
}

function publish(next: string[], focusIndex: number) {
  cells.value = next
  const value = next.join('')
  emit('update:modelValue', value)
  focus(focusIndex)
  if (next.every(c => c !== '')) emit('complete', value)
}

function fillFrom(index: number, digits: string[]) {
  const next = cells.value.slice()
  digits.forEach((d, k) => { if (index + k < PIN_LENGTH) next[index + k] = d })
  publish(next, Math.min(index + digits.length, PIN_LENGTH - 1))
}

function onInput(index: number, event: Event) {
  const input = event.target as HTMLInputElement
  const digits = pinDigits(input.value)
  if (!digits.length) {
    input.value = cells.value[index]
    return
  }
  // A whole code typed or autofilled into one box spreads from the first box.
  fillFrom(digits.length >= PIN_LENGTH ? 0 : index, digits.length >= PIN_LENGTH ? digits : digits.slice(-1))
}

function onKeydown(index: number, event: KeyboardEvent) {
  if (event.key === 'Backspace') {
    event.preventDefault()
    const next = cells.value.slice()
    if (next[index]) {
      next[index] = ''
      publish(next, index)
    } else if (index > 0) {
      next[index - 1] = ''
      publish(next, index - 1)
    }
  } else if (event.key === 'ArrowLeft') {
    event.preventDefault()
    focus(index - 1)
  } else if (event.key === 'ArrowRight') {
    event.preventDefault()
    focus(index + 1)
  }
}

function onPaste(index: number, event: ClipboardEvent) {
  const digits = pinDigits(event.clipboardData?.getData('text') || '')
  event.preventDefault()
  if (digits.length) fillFrom(digits.length >= PIN_LENGTH ? 0 : index, digits)
}
</script>

<template>
  <div role="group" :aria-label="label" class="flex gap-3 justify-center">
    <input
      v-for="(cell, index) in cells"
      :key="index"
      :ref="el => { if (el) boxes[index] = el as HTMLInputElement }"
      :value="cell"
      type="password"
      inputmode="numeric"
      pattern="[0-9]*"
      autocomplete="one-time-code"
      :aria-label="`${label}, digit ${index + 1} of ${PIN_LENGTH}`"
      :aria-invalid="invalid || undefined"
      :disabled="disabled"
      class="w-12 h-14 text-center text-2xl border rounded-lg focus:outline-none focus:border-gmail-blue focus:ring-1 focus:ring-gmail-blue disabled:opacity-60"
      :class="invalid ? 'border-red-400' : 'border-gmail-border'"
      @input="onInput(index, $event)"
      @keydown="onKeydown(index, $event)"
      @paste="onPaste(index, $event)"
      @focus="($event.target as HTMLInputElement).select()"
    />
  </div>
</template>
