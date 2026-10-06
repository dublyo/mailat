<script setup lang="ts">
import AppLayout from '@/components/layout/AppLayout.vue'
import { automationApi, type Automation, type AutomationStatus, type AutomationValidationError } from '@/lib/api'
import { triggerLabel } from '@/lib/automationGraph'
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import {
  Zap,
  Plus,
  Play,
  Pause,
  Trash2,
  Edit3,
  Archive,
  CheckCircle2,
  AlertCircle,
  Search
} from 'lucide-vue-next'

const router = useRouter()
const automations = ref<Automation[]>([])
const loading = ref(true)
const error = ref('')
const errorDetails = ref<AutomationValidationError[]>([])
const errorAutomation = ref<string | null>(null)
const searchQuery = ref('')
const filterStatus = ref<'all' | AutomationStatus>('all')
const filterTabs: { value: 'all' | AutomationStatus; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'active', label: 'Active' },
  { value: 'paused', label: 'Paused' },
  { value: 'draft', label: 'Draft' },
  { value: 'archived', label: 'Archived' },
]
const confirmState = ref<{ title: string; message: string; confirmLabel: string; run: () => unknown } | null>(null)

onMounted(async () => {
  await loadAutomations()
})

const loadAutomations = async () => {
  loading.value = true
  try {
    clearError()
    const data = await automationApi.list({ page: 1, pageSize: 100 })
    automations.value = data.automations || []
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not load automations'
  } finally {
    loading.value = false
  }
}

function clearError() {
  error.value = ''
  errorDetails.value = []
  errorAutomation.value = null
}

function showError(e: unknown, fallback: string, automation?: Automation) {
  const details = (e as { data?: { errors?: AutomationValidationError[] } })?.data?.errors ?? []
  error.value = e instanceof Error && e.message ? e.message : fallback
  errorDetails.value = details
  errorAutomation.value = details.length && automation ? automation.uuid : null
}

const replaceRow = (updated: Automation) => {
  automations.value = automations.value.map(a => a.uuid === updated.uuid ? { ...a, ...updated } : a)
}

const createAutomation = () => {
  router.push('/automations/new')
}

const editAutomation = (automation: Automation) => {
  router.push(`/automations/${automation.uuid}`)
}

// Resume publishes the saved draft, like the editor's Resume button.
const toggleStatus = async (automation: Automation) => {
  clearError()
  try {
    if (automation.status === 'active') replaceRow(await automationApi.pause(automation.uuid))
    else replaceRow((await automationApi.activate(automation.uuid)).automation)
  } catch (e) {
    showError(e, 'Could not change automation status', automation)
  }
}

const askArchive = (automation: Automation) => {
  confirmState.value = {
    title: `Archive "${automation.name}"?`,
    message: 'Every contact in progress is cancelled, and the automation becomes read-only. This cannot be undone.',
    confirmLabel: 'Archive',
    run: async () => {
      clearError()
      try {
        replaceRow((await automationApi.archive(automation.uuid)).automation)
      } catch (e) {
        showError(e, 'Could not archive automation')
      }
    }
  }
}

const askDelete = (automation: Automation) => {
  confirmState.value = {
    title: `Delete "${automation.name}"?`,
    message: 'The automation and its history are removed permanently.',
    confirmLabel: 'Delete',
    run: async () => {
      clearError()
      try {
        await automationApi.remove(automation.uuid)
        automations.value = automations.value.filter(a => a.uuid !== automation.uuid)
      } catch (e) {
        showError(e, 'Could not delete automation')
      }
    }
  }
}

const confirmRun = async () => {
  const state = confirmState.value
  confirmState.value = null
  await state?.run()
}

const filteredAutomations = computed(() => {
  let result = automations.value

  if (filterStatus.value !== 'all') {
    result = result.filter(a => a.status === filterStatus.value)
  }

  if (searchQuery.value) {
    const query = searchQuery.value.toLowerCase()
    result = result.filter(a =>
      a.name.toLowerCase().includes(query) ||
      a.description?.toLowerCase().includes(query)
    )
  }

  return result
})

const counts = (a: Automation) => ({
  enrolled: a.stats?.enrolled ?? a.enrolledCount ?? 0,
  inProgress: a.stats?.active ?? a.inProgressCount ?? 0,
  completed: a.stats?.completed ?? a.completedCount ?? 0,
})

const getStatusColor = (status: string) => {
  switch (status) {
    case 'active': return 'bg-green-100 text-green-700'
    case 'paused': return 'bg-yellow-100 text-yellow-700'
    case 'archived': return 'bg-gray-200 text-gray-700'
    default: return 'bg-gray-100 text-gray-600'
  }
}

const getStatusIcon = (status: string) => {
  switch (status) {
    case 'active': return CheckCircle2
    case 'paused': return Pause
    case 'draft': return Edit3
    case 'archived': return Archive
    default: return AlertCircle
  }
}

const formatDate = (dateStr: string) => {
  return new Date(dateStr).toLocaleDateString('en-US', {
    month: 'short',
    day: 'numeric',
    year: 'numeric'
  })
}
</script>

<template>
  <AppLayout>
  <div class="automations-page">
    <div v-if="error" role="alert" class="m-4 rounded-lg bg-red-50 text-red-700 p-3 text-sm">
      <p>{{ error }}<template v-if="errorDetails.length">: {{ errorDetails.map(e => e.message).join('; ') }}</template></p>
      <button v-if="errorAutomation" class="mt-1 underline" @click="router.push(`/automations/${errorAutomation}`)">Open in editor</button>
    </div>
    <!-- Header -->
    <header class="page-header">
      <div class="header-content">
        <div class="header-left">
          <h1>Automations</h1>
          <p>Create automated email workflows to engage your contacts</p>
        </div>
        <button @click="createAutomation" class="create-btn">
          <Plus class="w-4 h-4" />
          <span>Create Automation</span>
        </button>
      </div>

      <!-- Filters -->
      <div class="filters-bar">
        <div class="search-box">
          <Search class="w-4 h-4 text-gray-400" />
          <input
            v-model="searchQuery"
            type="text"
            placeholder="Search automations..."
          />
        </div>
        <div class="filter-tabs">
          <button
            v-for="tab in filterTabs"
            :key="tab.value"
            :class="['filter-tab', { active: filterStatus === tab.value }]"
            @click="filterStatus = tab.value"
          >
            {{ tab.label }}
          </button>
        </div>
      </div>
    </header>

    <!-- Content -->
    <main class="page-content">
      <!-- Loading -->
      <div v-if="loading" class="loading-state">
        <div class="spinner"></div>
        <p>Loading automations...</p>
      </div>

      <!-- Empty State -->
      <div v-else-if="automations.length === 0" class="empty-state">
        <div class="empty-icon">
          <Zap class="w-10 h-10" />
        </div>
        <h2>No automations yet</h2>
        <p>Create your first automation to start engaging with contacts automatically.</p>
        <button @click="createAutomation" class="create-btn">
          <Plus class="w-4 h-4" />
          <span>Create Your First Automation</span>
        </button>
      </div>

      <!-- No Results -->
      <div v-else-if="filteredAutomations.length === 0" class="empty-state">
        <div class="empty-icon">
          <Search class="w-10 h-10" />
        </div>
        <h2>No results found</h2>
        <p>Try adjusting your search or filter criteria.</p>
      </div>

      <!-- Automations List -->
      <div v-else class="automations-grid">
        <div
          v-for="automation in filteredAutomations"
          :key="automation.uuid"
          class="automation-card"
          @click="editAutomation(automation)"
        >
          <div class="card-header">
            <div :class="['status-icon', `status-${automation.status}`]">
              <component :is="getStatusIcon(automation.status)" class="w-5 h-5" />
            </div>
            <div class="card-title">
              <h3>{{ automation.name }}</h3>
              <span :class="['status-badge', getStatusColor(automation.status)]">
                {{ automation.status }}
              </span>
            </div>
          </div>

          <p class="card-description">{{ automation.description || 'No description' }}</p>

          <div class="card-trigger">
            <Zap class="w-3.5 h-3.5" />
            <span>{{ triggerLabel(automation.triggerType) }}</span>
            <span v-if="automation.publishedVersion" class="text-gray-400">· v{{ automation.publishedVersion }}</span>
            <span v-if="automation.hasUnpublishedChanges && automation.status !== 'archived'" class="text-indigo-600">· unpublished changes</span>
          </div>

          <div class="card-stats">
            <div class="stat">
              <span class="stat-value">{{ counts(automation).enrolled.toLocaleString() }}</span>
              <span class="stat-label">Enrolled</span>
            </div>
            <div class="stat">
              <span class="stat-value">{{ counts(automation).inProgress.toLocaleString() }}</span>
              <span class="stat-label">In Progress</span>
            </div>
            <div class="stat">
              <span class="stat-value text-green-600">{{ counts(automation).completed.toLocaleString() }}</span>
              <span class="stat-label">Completed</span>
            </div>
          </div>

          <div class="card-footer">
            <span class="updated-at">Updated {{ formatDate(automation.updatedAt) }}</span>
            <div class="card-actions" @click.stop>
              <button
                v-if="automation.status === 'active' || automation.status === 'paused'"
                @click="toggleStatus(automation)"
                class="action-btn"
                :title="automation.status === 'active' ? 'Pause' : 'Resume'"
                :aria-label="`${automation.status === 'active' ? 'Pause' : 'Resume'} ${automation.name}`"
              >
                <Pause v-if="automation.status === 'active'" class="w-4 h-4" />
                <Play v-else class="w-4 h-4" />
              </button>
              <button
                v-if="automation.status !== 'archived'"
                @click="askArchive(automation)"
                class="action-btn"
                title="Archive"
                :aria-label="`Archive ${automation.name}`"
              >
                <Archive class="w-4 h-4" />
              </button>
              <button
                v-if="automation.status === 'draft' || automation.status === 'archived'"
                @click="askDelete(automation)"
                class="action-btn delete"
                title="Delete"
                :aria-label="`Delete ${automation.name}`"
              >
                <Trash2 class="w-4 h-4" />
              </button>
            </div>
          </div>
        </div>
      </div>
    </main>

    <div v-if="confirmState" class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4" @click.self="confirmState = null">
      <div class="w-full max-w-md rounded-xl bg-white p-5 shadow-xl" role="dialog" aria-modal="true" :aria-label="confirmState.title">
        <h3 class="text-base font-semibold text-gray-900">{{ confirmState.title }}</h3>
        <p class="mt-2 text-sm text-gray-600">{{ confirmState.message }}</p>
        <div class="mt-4 flex justify-end gap-2">
          <button class="rounded-lg bg-gray-100 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-200" @click="confirmState = null">Cancel</button>
          <button class="rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700" @click="confirmRun">{{ confirmState.confirmLabel }}</button>
        </div>
      </div>
    </div>
  </div>
  </AppLayout>
</template>

<style scoped>
.automations-page {
  flex: 1;
  min-width: 0;
  overflow: auto;
  height: 100%;
  display: flex;
  flex-direction: column;
  background: #f9fafb;
}

/* Header */
.page-header {
  background: white;
  border-bottom: 1px solid #e5e7eb;
  padding: 24px 32px 0;
}

.header-content {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: 24px;
}

.header-left h1 {
  font-size: 24px;
  font-weight: 700;
  color: #111827;
  margin-bottom: 4px;
}

.header-left p {
  font-size: 14px;
  color: #6b7280;
}

.create-btn {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 20px;
  background: #6366f1;
  color: white;
  font-weight: 500;
  font-size: 14px;
  border-radius: 10px;
  transition: all 0.2s;
  box-shadow: 0 2px 4px rgb(99 102 241 / 0.3);
}

.create-btn:hover {
  background: #4f46e5;
  transform: translateY(-1px);
  box-shadow: 0 4px 8px rgb(99 102 241 / 0.4);
}

/* Filters */
.filters-bar {
  display: flex;
  align-items: center;
  gap: 20px;
  padding-bottom: 16px;
}

.search-box {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 14px;
  background: #f9fafb;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  width: 280px;
}

.search-box input {
  flex: 1;
  border: none;
  background: transparent;
  font-size: 14px;
  outline: none;
}

.search-box input::placeholder {
  color: #9ca3af;
}

.filter-tabs {
  display: flex;
  gap: 4px;
}

.filter-tab {
  padding: 8px 16px;
  font-size: 13px;
  font-weight: 500;
  color: #6b7280;
  border-radius: 6px;
  transition: all 0.2s;
}

.filter-tab:hover {
  background: #f3f4f6;
}

.filter-tab.active {
  background: #eef2ff;
  color: #4f46e5;
}

/* Content */
.page-content {
  flex: 1;
  overflow-y: auto;
  padding: 24px 32px;
}

/* Loading */
.loading-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 300px;
  gap: 16px;
  color: #6b7280;
}

.spinner {
  width: 40px;
  height: 40px;
  border: 3px solid #e5e7eb;
  border-top-color: #6366f1;
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

/* Empty State */
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px 20px;
  background: white;
  border-radius: 16px;
  border: 2px dashed #e5e7eb;
  text-align: center;
}

.empty-icon {
  width: 80px;
  height: 80px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #eef2ff;
  border-radius: 20px;
  margin-bottom: 20px;
  color: #6366f1;
}

.empty-state h2 {
  font-size: 18px;
  font-weight: 600;
  color: #111827;
  margin-bottom: 8px;
}

.empty-state p {
  font-size: 14px;
  color: #6b7280;
  margin-bottom: 24px;
  max-width: 400px;
}

/* Automations Grid */
.automations-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
  gap: 20px;
}

.automation-card {
  background: white;
  border-radius: 14px;
  border: 1px solid #e5e7eb;
  padding: 20px;
  cursor: pointer;
  transition: all 0.2s;
}

.automation-card:hover {
  border-color: #6366f1;
  box-shadow: 0 4px 16px rgb(0 0 0 / 0.08);
  transform: translateY(-2px);
}

.card-header {
  display: flex;
  align-items: flex-start;
  gap: 14px;
  margin-bottom: 14px;
}

.status-icon {
  width: 44px;
  height: 44px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 12px;
  flex-shrink: 0;
}

.status-icon.status-active {
  background: #dcfce7;
  color: #16a34a;
}

.status-icon.status-paused {
  background: #fef3c7;
  color: #d97706;
}

.status-icon.status-archived {
  background: #e5e7eb;
  color: #4b5563;
}

.status-icon.status-draft {
  background: #f3f4f6;
  color: #6b7280;
}

.card-title {
  flex: 1;
  min-width: 0;
}

.card-title h3 {
  font-size: 16px;
  font-weight: 600;
  color: #111827;
  margin-bottom: 6px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.status-badge {
  display: inline-flex;
  padding: 3px 10px;
  border-radius: 9999px;
  font-size: 11px;
  font-weight: 500;
  text-transform: capitalize;
}

.card-description {
  font-size: 13px;
  color: #6b7280;
  margin-bottom: 14px;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  line-height: 1.5;
}

.card-trigger {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 12px;
  background: #f9fafb;
  border-radius: 8px;
  font-size: 12px;
  color: #6b7280;
  margin-bottom: 18px;
}

.card-stats {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 16px;
  padding: 16px 0;
  border-top: 1px solid #f3f4f6;
  border-bottom: 1px solid #f3f4f6;
}

.stat {
  text-align: center;
}

.stat-value {
  display: block;
  font-size: 20px;
  font-weight: 600;
  color: #111827;
}

.stat-label {
  display: block;
  font-size: 11px;
  color: #9ca3af;
  margin-top: 2px;
}

.card-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding-top: 16px;
}

.updated-at {
  font-size: 12px;
  color: #9ca3af;
}

.card-actions {
  display: flex;
  gap: 6px;
}

.action-btn {
  width: 32px;
  height: 32px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  color: #6b7280;
  transition: all 0.2s;
}

.action-btn:hover {
  background: #f3f4f6;
  color: #374151;
}

.action-btn.delete:hover {
  background: #fef2f2;
  color: #ef4444;
}
</style>
