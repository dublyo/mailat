<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import {
  Plus,
  Mail,
  BarChart3,
  Calendar,
  MoreVertical,
  Edit3,
  Trash2,
  Copy,
  Play,
  Pause,
  Eye,
  Send,
  Search,
  RefreshCw,
  CheckCircle,
  Clock,
  FileText,
  AlertCircle,
  XCircle
} from 'lucide-vue-next'
import AppLayout from '@/components/layout/AppLayout.vue'
import Button from '@/components/common/Button.vue'
import Badge from '@/components/common/Badge.vue'
import Spinner from '@/components/common/Spinner.vue'
import CampaignWizard from '@/components/campaigns/CampaignWizard.vue'
import { useCampaignsStore } from '@/stores/campaigns'
import type { Campaign } from '@/lib/api'
import { openRate, clickRate, statusReasonLabel, campaignProgressPercent } from '@/lib/campaignStatus'

const router = useRouter()
const campaignsStore = useCampaignsStore()

// State
const showWizard = ref(false)
const editingCampaign = ref<Campaign | null>(null)
const activeTab = ref<string>('all')
const searchQuery = ref('')
const selectedCampaigns = ref<string[]>([])
const showActionsMenu = ref<string | null>(null)
const confirmDelete = ref<Campaign | null>(null)
const isDeleting = ref(false)
const confirmCancel = ref<Campaign | null>(null)
const isCancelling = ref(false)
const actionError = ref<string | null>(null)

// Status tabs configuration
const tabs = [
  { id: 'all', label: 'All Campaigns', icon: Mail },
  { id: 'draft', label: 'Drafts', icon: FileText },
  { id: 'scheduled', label: 'Scheduled', icon: Clock },
  { id: 'sending', label: 'Sending', icon: Send },
  { id: 'sent', label: 'Sent', icon: CheckCircle },
  { id: 'paused', label: 'Paused', icon: Pause },
  { id: 'cancelled', label: 'Cancelled', icon: XCircle }
]

// Computed
const filteredCampaigns = computed(() => {
  let campaigns = campaignsStore.campaigns

  // Filter by status tab
  if (activeTab.value !== 'all') {
    campaigns = campaigns.filter(c => c.status === activeTab.value)
  }

  // Filter by search query
  if (searchQuery.value) {
    const query = searchQuery.value.toLowerCase()
    campaigns = campaigns.filter(c =>
      c.name.toLowerCase().includes(query) ||
      c.subject.toLowerCase().includes(query)
    )
  }

  return campaigns
})

const tabCounts = computed(() => {
  const counts: Record<string, number> = { all: campaignsStore.campaigns.length }
  for (const campaign of campaignsStore.campaigns) {
    counts[campaign.status] = (counts[campaign.status] || 0) + 1
  }
  return counts
})

// While anything is sending, refresh the list so counters and progress move.
const LIST_POLL_MS = 10000
let listTimer: ReturnType<typeof setInterval> | null = null
const hasActive = computed(() => campaignsStore.campaigns.some(c => c.status === 'sending'))

onMounted(() => {
  campaignsStore.fetchCampaigns()
  listTimer = setInterval(() => {
    if (hasActive.value && !document.hidden) campaignsStore.fetchCampaigns()
  }, LIST_POLL_MS)
})

onUnmounted(() => {
  if (listTimer) clearInterval(listTimer)
})

const getStatusVariant = (status: string) => {
  switch (status) {
    case 'sent': return 'success'
    case 'sending': return 'info'
    case 'scheduled': return 'warning'
    case 'draft': return 'default'
    case 'paused': return 'warning'
    case 'cancelled': return 'error'
    default: return 'default'
  }
}

const formatDate = (dateString: string | null | undefined) => {
  if (!dateString) return '-'
  return new Date(dateString).toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: 'numeric',
    minute: '2-digit'
  })
}

const openCreateWizard = () => {
  editingCampaign.value = null
  showWizard.value = true
}

const openEditWizard = (campaign: Campaign) => {
  editingCampaign.value = campaign
  showWizard.value = true
  showActionsMenu.value = null
}

// The wizard may have saved a draft before closing, so always refresh.
const closeWizard = () => {
  showWizard.value = false
  editingCampaign.value = null
  campaignsStore.fetchCampaigns()
}

const viewCampaignDetails = (campaign: Campaign) => {
  router.push({ name: 'campaign-detail', params: { uuid: campaign.uuid } })
}

const toggleActionsMenu = (uuid: string) => {
  showActionsMenu.value = showActionsMenu.value === uuid ? null : uuid
}

const duplicateCampaign = (campaign: Campaign) => {
  showActionsMenu.value = null
  // A copy is a new draft: none of the original's sending state carries over.
  const duplicatedCampaign: Campaign = {
    ...campaign,
    id: 0,
    uuid: '',
    name: `${campaign.name} (Copy)`,
    status: 'draft',
    statusReason: null,
    scheduledAt: undefined,
    startedAt: undefined,
    completedAt: undefined,
    preparedAt: null,
    throttledUntil: null,
    totalRecipients: 0,
    sentCount: 0,
    deliveredCount: 0,
    openCount: 0,
    clickCount: 0,
    bounceCount: 0,
    unsubscribeCount: 0,
    complaintCount: 0,
    failedCount: 0,
    skippedCount: 0,
    unknownCount: 0,
    warnings: undefined,
    trackOpens: campaign.trackOpens,
    trackClicks: campaign.trackClicks
  }
  editingCampaign.value = duplicatedCampaign
  showWizard.value = true
}

const runAction = async (fallback: string, action: () => Promise<unknown>) => {
  showActionsMenu.value = null
  actionError.value = null
  try {
    await action()
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : fallback
  }
}

const pauseCampaign = (campaign: Campaign) =>
  runAction('Failed to pause campaign', () => campaignsStore.pauseCampaign(campaign.uuid))

const resumeCampaign = (campaign: Campaign) =>
  runAction('Failed to resume campaign', () => campaignsStore.resumeCampaign(campaign.uuid))

const canEdit = (campaign: Campaign) => campaign.status === 'draft' || campaign.status === 'scheduled'
const canCancel = (campaign: Campaign) => ['draft', 'scheduled', 'sending', 'paused'].includes(campaign.status)
const canDelete = (campaign: Campaign) =>
  campaign.status === 'draft' || (campaign.status === 'cancelled' && !campaign.preparedAt)
const showsPerformance = (campaign: Campaign) =>
  campaign.sentCount > 0 || ['sending', 'sent', 'paused'].includes(campaign.status)

const promptCancelCampaign = (campaign: Campaign) => {
  showActionsMenu.value = null
  confirmCancel.value = campaign
}

const confirmCancelCampaign = async () => {
  if (!confirmCancel.value) return
  isCancelling.value = true
  const target = confirmCancel.value
  await runAction('Failed to cancel campaign', () => campaignsStore.cancelCampaign(target.uuid))
  confirmCancel.value = null
  isCancelling.value = false
}

const promptDeleteCampaign = (campaign: Campaign) => {
  showActionsMenu.value = null
  confirmDelete.value = campaign
}

const cancelDelete = () => {
  confirmDelete.value = null
}

const confirmDeleteCampaign = async () => {
  if (!confirmDelete.value) return
  isDeleting.value = true
  const target = confirmDelete.value
  await runAction('Failed to delete campaign', () => campaignsStore.deleteCampaign(target.uuid))
  confirmDelete.value = null
  isDeleting.value = false
}

const refreshCampaigns = () => {
  campaignsStore.fetchCampaigns()
}
</script>

<template>
  <AppLayout>
    <div class="flex-1 flex flex-col">
      <!-- Header -->
      <div class="px-6 py-5 border-b border-gray-200 bg-white">
        <div class="flex items-center justify-between">
          <div>
            <h1 class="text-2xl font-semibold text-gray-900">Campaigns</h1>
            <p class="text-gray-500 mt-1">Create and manage your email marketing campaigns</p>
          </div>
          <div class="flex items-center gap-3">
            <button
              @click="refreshCampaigns"
              class="p-2 text-gray-500 hover:text-gray-700 hover:bg-gray-100 rounded-lg transition-colors"
              :class="{ 'animate-spin': campaignsStore.isLoading }"
            >
              <RefreshCw class="w-5 h-5" />
            </button>
            <Button @click="openCreateWizard">
              <Plus class="w-4 h-4" />
              New Campaign
            </Button>
          </div>
        </div>

        <!-- Tabs -->
        <div class="flex items-center gap-1 mt-6 overflow-x-auto">
          <button
            v-for="tab in tabs"
            :key="tab.id"
            @click="activeTab = tab.id"
            :class="[
              'flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium transition-all whitespace-nowrap',
              activeTab === tab.id
                ? 'bg-gmail-blue text-white'
                : 'text-gray-600 hover:bg-gray-100'
            ]"
          >
            <component :is="tab.icon" class="w-4 h-4" />
            {{ tab.label }}
            <span
              v-if="tabCounts[tab.id]"
              :class="[
                'px-2 py-0.5 rounded-full text-xs',
                activeTab === tab.id
                  ? 'bg-white/20 text-white'
                  : 'bg-gray-200 text-gray-600'
              ]"
            >
              {{ tabCounts[tab.id] }}
            </span>
          </button>
        </div>
      </div>

      <!-- Search Bar -->
      <div class="px-6 py-4 border-b border-gray-200 bg-gray-50">
        <div class="relative max-w-md">
          <Search class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
          <input
            v-model="searchQuery"
            type="text"
            placeholder="Search campaigns..."
            class="w-full pl-10 pr-4 py-2 border border-gray-300 rounded-lg focus:outline-none focus:ring-2 focus:ring-gmail-blue/20 focus:border-gmail-blue transition-all"
          />
        </div>
      </div>

      <div v-if="actionError" class="mx-6 mt-4 p-3 bg-red-50 border border-red-200 rounded-lg text-sm text-red-700 flex items-center gap-2" role="alert">
        <AlertCircle class="w-4 h-4 flex-shrink-0" />
        {{ actionError }}
      </div>

      <!-- Loading -->
      <div v-if="campaignsStore.isLoading && filteredCampaigns.length === 0" class="flex-1 flex items-center justify-center">
        <div class="text-center">
          <Spinner size="lg" />
          <p class="text-gray-500 mt-3">Loading campaigns...</p>
        </div>
      </div>

      <!-- Empty state -->
      <div
        v-else-if="filteredCampaigns.length === 0"
        class="flex-1 flex flex-col items-center justify-center p-8"
      >
        <div class="w-20 h-20 bg-gray-100 rounded-full flex items-center justify-center mb-4">
          <Mail class="w-10 h-10 text-gray-400" />
        </div>
        <h3 class="text-lg font-medium text-gray-900 mb-1">
          {{ searchQuery ? 'No campaigns found' : 'No campaigns yet' }}
        </h3>
        <p class="text-gray-500 mb-6 text-center max-w-md">
          {{ searchQuery
            ? `No campaigns matching "${searchQuery}"`
            : 'Create your first email campaign to start reaching your audience'
          }}
        </p>
        <Button v-if="!searchQuery" @click="openCreateWizard">
          <Plus class="w-4 h-4" />
          Create your first campaign
        </Button>
      </div>

      <!-- Campaign list -->
      <div v-else class="flex-1 overflow-y-auto p-6">
        <div class="bg-white rounded-xl border border-gray-200 overflow-hidden shadow-sm">
          <table class="w-full">
            <thead class="bg-gray-50 border-b border-gray-200">
              <tr>
                <th class="text-left px-5 py-4 text-xs font-semibold text-gray-600 uppercase tracking-wider">Campaign</th>
                <th class="text-left px-5 py-4 text-xs font-semibold text-gray-600 uppercase tracking-wider">Status</th>
                <th class="text-left px-5 py-4 text-xs font-semibold text-gray-600 uppercase tracking-wider">Sent</th>
                <th class="text-left px-5 py-4 text-xs font-semibold text-gray-600 uppercase tracking-wider">Performance</th>
                <th class="text-left px-5 py-4 text-xs font-semibold text-gray-600 uppercase tracking-wider">Date</th>
                <th class="w-16"></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100">
              <tr
                v-for="campaign in filteredCampaigns"
                :key="campaign.uuid"
                class="hover:bg-gray-50 transition-colors group"
              >
                <td class="px-5 py-4">
                  <div
                    class="cursor-pointer"
                    @click="viewCampaignDetails(campaign)"
                  >
                    <div class="font-medium text-gray-900 group-hover:text-gmail-blue transition-colors">
                      {{ campaign.name }}
                    </div>
                    <div class="text-sm text-gray-500 truncate max-w-xs">
                      {{ campaign.subject }}
                    </div>
                  </div>
                </td>
                <td class="px-5 py-4">
                  <Badge :variant="getStatusVariant(campaign.status)" class="capitalize">
                    {{ campaign.status }}
                  </Badge>
                  <div
                    v-if="campaign.statusReason"
                    class="mt-1.5 inline-flex px-2 py-0.5 rounded bg-amber-50 text-amber-800 text-xs"
                    data-test="status-reason"
                  >
                    {{ statusReasonLabel(campaign.statusReason) }}
                  </div>
                  <div v-if="campaign.status === 'sending'" class="mt-2 w-32" data-test="list-progress">
                    <div v-if="!campaign.preparedAt" class="text-xs text-gray-500">Preparing audience...</div>
                    <template v-else>
                      <div class="h-1.5 bg-gray-200 rounded-full overflow-hidden">
                        <div class="h-full bg-blue-500 transition-all" :style="{ width: `${campaignProgressPercent(campaign)}%` }" />
                      </div>
                      <div class="text-xs text-gray-500 mt-1">{{ campaignProgressPercent(campaign) }}%</div>
                    </template>
                  </div>
                </td>
                <td class="px-5 py-4">
                  <div class="text-sm font-semibold text-gray-900" data-test="sent-count">
                    {{ campaign.sentCount.toLocaleString() }}
                    <span v-if="campaign.totalRecipients" class="font-normal text-gray-500">/ {{ campaign.totalRecipients.toLocaleString() }}</span>
                  </div>
                </td>
                <td class="px-5 py-4">
                  <div v-if="showsPerformance(campaign)" class="flex items-center gap-6">
                    <div class="text-center">
                      <div class="text-lg font-semibold text-green-600" data-test="open-rate">
                        {{ campaign.trackOpens ? `${openRate(campaign).toFixed(1)}%` : '-' }}
                      </div>
                      <div class="text-xs text-gray-500">Opened</div>
                    </div>
                    <div class="text-center">
                      <div class="text-lg font-semibold text-blue-600" data-test="click-rate">
                        {{ campaign.trackClicks ? `${clickRate(campaign).toFixed(1)}%` : '-' }}
                      </div>
                      <div class="text-xs text-gray-500">Clicked</div>
                    </div>
                  </div>
                  <div v-else class="text-sm text-gray-400">
                    -
                  </div>
                </td>
                <td class="px-5 py-4">
                  <div class="flex items-center gap-2 text-sm text-gray-600">
                    <Calendar class="w-4 h-4 text-gray-400" />
                    {{ formatDate(campaign.completedAt || campaign.startedAt || campaign.scheduledAt || campaign.createdAt) }}
                  </div>
                </td>
                <td class="px-5 py-4">
                  <div class="relative">
                    <button
                      @click="toggleActionsMenu(campaign.uuid)"
                      class="p-2 hover:bg-gray-100 rounded-lg transition-colors"
                      :aria-label="`Actions for ${campaign.name}`"
                    >
                      <MoreVertical class="w-4 h-4 text-gray-500" />
                    </button>

                    <!-- Actions Dropdown -->
                    <div
                      v-if="showActionsMenu === campaign.uuid"
                      class="absolute right-0 top-full mt-1 w-48 bg-white rounded-lg shadow-lg border border-gray-200 py-1 z-20"
                    >
                      <button
                        @click="viewCampaignDetails(campaign)"
                        class="w-full px-4 py-2 text-left text-sm text-gray-700 hover:bg-gray-50 flex items-center gap-2"
                      >
                        <Eye class="w-4 h-4" />
                        View Details
                      </button>
                      <button
                        v-if="canEdit(campaign)"
                        @click="openEditWizard(campaign)"
                        class="w-full px-4 py-2 text-left text-sm text-gray-700 hover:bg-gray-50 flex items-center gap-2"
                      >
                        <Edit3 class="w-4 h-4" />
                        Edit Campaign
                      </button>
                      <button
                        @click="duplicateCampaign(campaign)"
                        class="w-full px-4 py-2 text-left text-sm text-gray-700 hover:bg-gray-50 flex items-center gap-2"
                      >
                        <Copy class="w-4 h-4" />
                        Duplicate
                      </button>
                      <button
                        v-if="campaign.status === 'sending'"
                        @click="pauseCampaign(campaign)"
                        class="w-full px-4 py-2 text-left text-sm text-amber-600 hover:bg-amber-50 flex items-center gap-2"
                      >
                        <Pause class="w-4 h-4" />
                        Pause Sending
                      </button>
                      <button
                        v-if="campaign.status === 'paused'"
                        @click="resumeCampaign(campaign)"
                        class="w-full px-4 py-2 text-left text-sm text-green-600 hover:bg-green-50 flex items-center gap-2"
                      >
                        <Play class="w-4 h-4" />
                        Resume Sending
                      </button>
                      <button
                        v-if="canCancel(campaign)"
                        @click="promptCancelCampaign(campaign)"
                        class="w-full px-4 py-2 text-left text-sm text-red-600 hover:bg-red-50 flex items-center gap-2"
                      >
                        <XCircle class="w-4 h-4" />
                        Cancel Campaign
                      </button>
                      <hr v-if="canDelete(campaign)" class="my-1 border-gray-100" />
                      <button
                        v-if="canDelete(campaign)"
                        @click="promptDeleteCampaign(campaign)"
                        class="w-full px-4 py-2 text-left text-sm text-red-600 hover:bg-red-50 flex items-center gap-2"
                      >
                        <Trash2 class="w-4 h-4" />
                        Delete
                      </button>
                    </div>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- Campaign Wizard Modal -->
    <CampaignWizard
      v-if="showWizard"
      :campaign="editingCampaign"
      @close="closeWizard"
      @created="closeWizard"
      @updated="closeWizard"
    />

    <!-- Cancel Confirmation Modal -->
    <div
      v-if="confirmCancel"
      class="fixed inset-0 bg-black/50 flex items-center justify-center z-50"
    >
      <div class="bg-white rounded-xl shadow-2xl w-full max-w-md mx-4 p-6">
        <div class="flex items-center gap-4 mb-4">
          <div class="w-12 h-12 bg-red-100 rounded-full flex items-center justify-center">
            <XCircle class="w-6 h-6 text-red-600" />
          </div>
          <div>
            <h3 class="text-lg font-semibold text-gray-900">Cancel Campaign</h3>
            <p class="text-gray-500 text-sm">A cancelled campaign cannot be resumed.</p>
          </div>
        </div>
        <p class="text-gray-600 mb-6">
          Stop <strong>{{ confirmCancel.name }}</strong>? Recipients who have not been sent to yet will be skipped.
          Messages already handed to SES still finish.
        </p>
        <div class="flex items-center justify-end gap-3">
          <Button variant="secondary" @click="confirmCancel = null" :disabled="isCancelling">
            Keep Campaign
          </Button>
          <Button class="bg-red-600 hover:bg-red-700" @click="confirmCancelCampaign" :loading="isCancelling">
            <XCircle class="w-4 h-4" />
            Cancel Campaign
          </Button>
        </div>
      </div>
    </div>

    <!-- Delete Confirmation Modal -->
    <div
      v-if="confirmDelete"
      class="fixed inset-0 bg-black/50 flex items-center justify-center z-50"
    >
      <div class="bg-white rounded-xl shadow-2xl w-full max-w-md mx-4 p-6">
        <div class="flex items-center gap-4 mb-4">
          <div class="w-12 h-12 bg-red-100 rounded-full flex items-center justify-center">
            <AlertCircle class="w-6 h-6 text-red-600" />
          </div>
          <div>
            <h3 class="text-lg font-semibold text-gray-900">Delete Campaign</h3>
            <p class="text-gray-500 text-sm">This action cannot be undone.</p>
          </div>
        </div>
        <p class="text-gray-600 mb-6">
          Are you sure you want to delete <strong>{{ confirmDelete.name }}</strong>?
          All campaign data and statistics will be permanently removed.
        </p>
        <div class="flex items-center justify-end gap-3">
          <Button variant="secondary" @click="cancelDelete" :disabled="isDeleting">
            Cancel
          </Button>
          <Button
            class="bg-red-600 hover:bg-red-700"
            @click="confirmDeleteCampaign"
            :loading="isDeleting"
          >
            <Trash2 class="w-4 h-4" />
            Delete Campaign
          </Button>
        </div>
      </div>
    </div>

    <!-- Click outside to close actions menu -->
    <div
      v-if="showActionsMenu"
      class="fixed inset-0 z-10"
      @click="showActionsMenu = null"
    />
  </AppLayout>
</template>
