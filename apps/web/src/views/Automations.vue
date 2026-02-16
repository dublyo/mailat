<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import {
  Zap,
  Plus,
  Play,
  Pause,
  Trash2,
  Edit3,
  Users,
  CheckCircle2,
  AlertCircle,
  Search,
  Filter,
  MoreHorizontal
} from 'lucide-vue-next'

interface Automation {
  id: number
  uuid: string
  name: string
  description: string
  status: 'active' | 'paused' | 'draft'
  triggerType: string
  enrolledCount: number
  completedCount: number
  inProgressCount: number
  createdAt: string
  updatedAt: string
}

const router = useRouter()
const automations = ref<Automation[]>([])
const loading = ref(true)
const searchQuery = ref('')
const filterStatus = ref<string>('all')

onMounted(async () => {
  await loadAutomations()
})

const loadAutomations = async () => {
  loading.value = true
  try {
    const token = localStorage.getItem('token')
    const response = await fetch('/api/v1/automations?page=1&pageSize=50', {
      headers: { 'Authorization': `Bearer ${token}` }
    })

    if (response.ok) {
      const data = await response.json()
      automations.value = data.data?.automations || []
    }
  } catch (error) {
    console.error('Failed to load automations:', error)
  } finally {
    loading.value = false
  }
}

const createAutomation = () => {
  router.push('/automations/new')
}

const editAutomation = (automation: Automation) => {
  router.push(`/automations/${automation.uuid}`)
}

const toggleStatus = async (automation: Automation, event: Event) => {
  event.stopPropagation()
  try {
    const token = localStorage.getItem('token')
    const action = automation.status === 'active' ? 'pause' : 'activate'
    const response = await fetch(`/api/v1/automations/${automation.uuid}/${action}`, {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${token}` }
    })

    if (response.ok) {
      automation.status = automation.status === 'active' ? 'paused' : 'active'
    }
  } catch (error) {
    console.error('Failed to toggle status:', error)
  }
}

const deleteAutomation = async (automation: Automation, event: Event) => {
  event.stopPropagation()
  if (!confirm(`Are you sure you want to delete "${automation.name}"?`)) return

  try {
    const token = localStorage.getItem('token')
    const response = await fetch(`/api/v1/automations/${automation.uuid}`, {
      method: 'DELETE',
      headers: { 'Authorization': `Bearer ${token}` }
    })

    if (response.ok) {
      automations.value = automations.value.filter(a => a.uuid !== automation.uuid)
    }
  } catch (error) {
    console.error('Failed to delete automation:', error)
  }
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

const getStatusColor = (status: string) => {
  switch (status) {
    case 'active': return 'bg-green-100 text-green-700'
    case 'paused': return 'bg-yellow-100 text-yellow-700'
    case 'draft': return 'bg-gray-100 text-gray-600'
    default: return 'bg-gray-100 text-gray-600'
  }
}

const getStatusIcon = (status: string) => {
  switch (status) {
    case 'active': return CheckCircle2
    case 'paused': return Pause
    case 'draft': return Edit3
    default: return AlertCircle
  }
}

const getTriggerLabel = (triggerType: string) => {
  const labels: Record<string, string> = {
    'contact_added': 'Contact Added',
    'contact.created': 'Contact Created',
    'contact.subscribed': 'Contact Subscribed',
    'tag.added': 'Tag Added',
    'form.submitted': 'Form Submitted',
    'email.opened': 'Email Opened',
    'api_trigger': 'API Trigger'
  }
  return labels[triggerType] || triggerType
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
  <div class="automations-page">
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
            :class="['filter-tab', { active: filterStatus === 'all' }]"
            @click="filterStatus = 'all'"
          >
            All
          </button>
          <button
            :class="['filter-tab', { active: filterStatus === 'active' }]"
            @click="filterStatus = 'active'"
          >
            Active
          </button>
          <button
            :class="['filter-tab', { active: filterStatus === 'paused' }]"
            @click="filterStatus = 'paused'"
          >
            Paused
          </button>
          <button
            :class="['filter-tab', { active: filterStatus === 'draft' }]"
            @click="filterStatus = 'draft'"
          >
            Draft
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
            <span>{{ getTriggerLabel(automation.triggerType) }}</span>
          </div>

          <div class="card-stats">
            <div class="stat">
              <span class="stat-value">{{ automation.enrolledCount?.toLocaleString() || 0 }}</span>
              <span class="stat-label">Enrolled</span>
            </div>
            <div class="stat">
              <span class="stat-value">{{ automation.inProgressCount?.toLocaleString() || 0 }}</span>
              <span class="stat-label">In Progress</span>
            </div>
            <div class="stat">
              <span class="stat-value text-green-600">{{ automation.completedCount?.toLocaleString() || 0 }}</span>
              <span class="stat-label">Completed</span>
            </div>
          </div>

          <div class="card-footer">
            <span class="updated-at">Updated {{ formatDate(automation.updatedAt) }}</span>
            <div class="card-actions" @click.stop>
              <button
                v-if="automation.status !== 'draft'"
                @click="toggleStatus(automation, $event)"
                class="action-btn"
                :title="automation.status === 'active' ? 'Pause' : 'Resume'"
              >
                <Pause v-if="automation.status === 'active'" class="w-4 h-4" />
                <Play v-else class="w-4 h-4" />
              </button>
              <button
                @click="deleteAutomation(automation, $event)"
                class="action-btn delete"
                title="Delete"
              >
                <Trash2 class="w-4 h-4" />
              </button>
            </div>
          </div>
        </div>
      </div>
    </main>
  </div>
</template>

<style scoped>
.automations-page {
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
