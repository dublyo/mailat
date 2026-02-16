<script setup lang="ts">
import { computed, ref, watch, onMounted } from 'vue'
import { Users, Search, CheckCircle, AlertCircle, ListFilter, RefreshCw } from 'lucide-vue-next'
import type { ContactList } from '@/lib/api'
import { listApi } from '@/lib/api'

const props = defineProps<{
  selectedListId: number | null
  selectedListUuid: string
}>()

const emit = defineEmits<{
  'update:selectedListId': [value: number | null]
  'update:selectedListUuid': [value: string]
  'update:valid': [value: boolean]
}>()

const lists = ref<ContactList[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
const searchQuery = ref('')

const fetchLists = async () => {
  loading.value = true
  error.value = null
  try {
    const response = await listApi.list()
    lists.value = response || []
  } catch (e) {
    error.value = 'Failed to load contact lists'
    console.error('Failed to fetch lists:', e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  fetchLists()
})

const filteredLists = computed(() => {
  if (!searchQuery.value) return lists.value
  const query = searchQuery.value.toLowerCase()
  return lists.value.filter(list =>
    list.name.toLowerCase().includes(query) ||
    (list.description?.toLowerCase().includes(query))
  )
})

const selectedList = computed(() => {
  return lists.value.find(list => list.uuid === props.selectedListUuid)
})

const totalRecipients = computed(() => {
  return selectedList.value?.contactCount || 0
})

const isValid = computed(() => {
  return props.selectedListId !== null && totalRecipients.value > 0
})

watch(isValid, (valid) => {
  emit('update:valid', valid)
}, { immediate: true })

const selectList = (list: ContactList) => {
  // Parse the ID from the list (could be string or number)
  const listId = typeof list.id === 'string' ? parseInt(list.id, 10) : list.id
  emit('update:selectedListId', listId)
  emit('update:selectedListUuid', list.uuid)
}

const clearSelection = () => {
  emit('update:selectedListId', null)
  emit('update:selectedListUuid', '')
}
</script>

<template>
  <div class="space-y-6 max-w-2xl">
    <div class="text-center mb-8">
      <div class="w-16 h-16 bg-purple-100 rounded-full flex items-center justify-center mx-auto mb-4">
        <Users class="w-8 h-8 text-purple-600" />
      </div>
      <h3 class="text-xl font-semibold text-gray-900">Select Recipients</h3>
      <p class="text-gray-500 mt-1">Choose which contact list to send your campaign to</p>
    </div>

    <!-- Search and Actions -->
    <div class="flex items-center gap-3">
      <div class="relative flex-1">
        <Search class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
        <input
          v-model="searchQuery"
          type="text"
          placeholder="Search lists..."
          class="w-full pl-10 pr-4 py-2.5 border border-gray-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-purple-500/20 focus:border-purple-500 transition-all"
        />
      </div>
      <button
        @click="fetchLists"
        class="p-2.5 text-gray-500 hover:text-gray-700 hover:bg-gray-100 rounded-lg transition-colors"
        title="Refresh lists"
      >
        <RefreshCw class="w-4 h-4" :class="{ 'animate-spin': loading }" />
      </button>
    </div>

    <!-- Quick Actions -->
    <div v-if="selectedListUuid" class="flex items-center gap-2">
      <button
        @click="clearSelection"
        class="text-sm text-gray-500 hover:text-gray-700"
      >
        Clear Selection
      </button>
    </div>

    <!-- Loading State -->
    <div v-if="loading" class="space-y-3">
      <div v-for="i in 3" :key="i" class="animate-pulse">
        <div class="h-20 bg-gray-100 rounded-lg"></div>
      </div>
    </div>

    <!-- Error State -->
    <div v-else-if="error" class="text-center py-8">
      <AlertCircle class="w-12 h-12 text-red-400 mx-auto mb-3" />
      <p class="text-gray-600">{{ error }}</p>
      <button
        @click="fetchLists"
        class="mt-3 text-purple-600 hover:text-purple-800 font-medium"
      >
        Try Again
      </button>
    </div>

    <!-- Empty State -->
    <div v-else-if="lists.length === 0" class="text-center py-12 bg-gray-50 rounded-xl border-2 border-dashed border-gray-200">
      <ListFilter class="w-12 h-12 text-gray-300 mx-auto mb-3" />
      <p class="text-gray-600 font-medium">No contact lists found</p>
      <p class="text-gray-400 text-sm mt-1">Create a contact list first to start your campaign</p>
    </div>

    <!-- List Selection -->
    <div v-else class="space-y-3">
      <div
        v-for="list in filteredLists"
        :key="list.uuid"
        @click="selectList(list)"
        class="group relative p-4 border-2 rounded-xl cursor-pointer transition-all"
        :class="[
          selectedListUuid === list.uuid
            ? 'border-purple-500 bg-purple-50/50'
            : 'border-gray-200 hover:border-gray-300 bg-white'
        ]"
      >
        <div class="flex items-center gap-4">
          <!-- Radio Button Style -->
          <div
            class="w-6 h-6 rounded-full border-2 flex items-center justify-center transition-all"
            :class="[
              selectedListUuid === list.uuid
                ? 'bg-purple-500 border-purple-500'
                : 'border-gray-300 group-hover:border-purple-400'
            ]"
          >
            <div
              v-if="selectedListUuid === list.uuid"
              class="w-2 h-2 bg-white rounded-full"
            />
          </div>

          <!-- List Info -->
          <div class="flex-1 min-w-0">
            <h4 class="font-medium text-gray-900 truncate">{{ list.name }}</h4>
            <p v-if="list.description" class="text-sm text-gray-500 truncate">
              {{ list.description }}
            </p>
          </div>

          <!-- Contact Count -->
          <div class="text-right">
            <div class="text-lg font-semibold text-gray-900">
              {{ list.contactCount?.toLocaleString() || 0 }}
            </div>
            <div class="text-xs text-gray-500">contacts</div>
          </div>
        </div>
      </div>

      <!-- No Results -->
      <div v-if="filteredLists.length === 0 && searchQuery" class="text-center py-8">
        <p class="text-gray-500">No lists matching "{{ searchQuery }}"</p>
      </div>
    </div>

    <!-- Summary -->
    <div
      v-if="selectedList"
      class="mt-6 p-4 bg-gradient-to-r from-purple-50 to-indigo-50 rounded-xl border border-purple-100"
    >
      <div class="flex items-center justify-between">
        <div>
          <span class="text-sm text-gray-600">Selected:</span>
          <span class="ml-2 font-semibold text-gray-900">
            {{ selectedList.name }}
          </span>
        </div>
        <div class="flex items-center gap-2">
          <Users class="w-4 h-4 text-purple-600" />
          <span class="font-semibold text-purple-700">
            {{ totalRecipients.toLocaleString() }} recipients
          </span>
        </div>
      </div>
    </div>

    <!-- Warning for no selection -->
    <div
      v-if="!selectedListUuid && !loading && lists.length > 0"
      class="flex items-center gap-2 p-3 bg-amber-50 text-amber-700 rounded-lg"
    >
      <AlertCircle class="w-4 h-4 flex-shrink-0" />
      <span class="text-sm">Select a list to continue</span>
    </div>
  </div>
</template>
