import { ref, watch, type Ref } from 'vue'

/**
 * The active settings tab, opened from a ?tab= deep link. Some tabs only show
 * once data loads (a mailbox user's Shared tab waits for their identities), so
 * the requested tab is applied when it becomes visible, unless another tab was
 * chosen first.
 */
export function useRequestedTab<T extends string>(requested: unknown, visible: Ref<T[]>, fallback: T) {
  const wanted = typeof requested === 'string' ? (requested as T) : null
  const active = ref(fallback) as Ref<T>
  let pending = !!wanted
  const apply = (ids: T[]) => {
    if (pending && wanted && ids.includes(wanted)) {
      active.value = wanted
      pending = false
    }
  }
  apply(visible.value)
  watch(visible, apply)
  watch(active, value => { if (value !== wanted) pending = false })
  return active
}
