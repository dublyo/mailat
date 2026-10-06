<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  ArrowLeft,
  Mail,
  MousePointer,
  Eye,
  EyeOff,
  AlertTriangle,
  CheckCircle,
  Send,
  Pause,
  Play,
  Calendar,
  BarChart3,
  TrendingUp,
  RefreshCw,
  Target,
  Zap,
  Shield,
  Activity,
  Clock,
  Edit3,
  XCircle,
  Info,
  Users,
  Link2
} from 'lucide-vue-next'
import { Doughnut, Bar } from 'vue-chartjs'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  BarElement,
  ArcElement,
  Title,
  Tooltip,
  Legend,
  Filler
} from 'chart.js'
import DOMPurify from 'dompurify'
import AppLayout from '@/components/layout/AppLayout.vue'
import Button from '@/components/common/Button.vue'
import Spinner from '@/components/common/Spinner.vue'
import CampaignWizard from '@/components/campaigns/CampaignWizard.vue'
import {
  campaignApi,
  type Campaign,
  type CampaignStatsResponse,
  type CampaignProgress,
  type CampaignPreview,
  type CampaignRecipient
} from '@/lib/api'
import { statusReasonLabel } from '@/lib/campaignStatus'
import { useCampaignsStore } from '@/stores/campaigns'

ChartJS.register(
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  BarElement,
  ArcElement,
  Title,
  Tooltip,
  Legend,
  Filler
)

const route = useRoute()
const router = useRouter()
const campaignsStore = useCampaignsStore()

// State
const campaign = ref<Campaign | null>(null)
const stats = ref<CampaignStatsResponse | null>(null)
const progress = ref<CampaignProgress | null>(null)
const preview = ref<CampaignPreview | null>(null)
const previewError = ref<string | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const refreshing = ref(false)
const actionError = ref<string | null>(null)
const actionBusy = ref(false)
const confirmCancel = ref(false)
const showWizard = ref(false)

const recipients = ref<CampaignRecipient[]>([])
const recipientTotal = ref(0)
const recipientFilter = ref('')
const recipientPage = ref(1)
const recipientsLoading = ref(false)
const RECIPIENT_PAGE_SIZE = 25
const recipientFilters = [
  { value: '', label: 'All' },
  { value: 'pending', label: 'Pending' },
  { value: 'sent', label: 'Sent' },
  { value: 'failed', label: 'Failed' },
  { value: 'unknown', label: 'Uncertain' },
  { value: 'skipped', label: 'Skipped' },
  { value: 'cancelled', label: 'Cancelled' }
]

// Progress is polled with the normal bearer header; the old event stream put the
// JWT in the URL. Polling pauses while the tab is hidden and stops on a terminal status.
const POLL_INTERVAL_MS = 5000
let pollTimer: ReturnType<typeof setInterval> | null = null

const uuid = computed(() => route.params.uuid as string)

const statusConfig = computed(() => {
  const configs: Record<string, { bg: string; text: string; icon: any; label: string }> = {
    sent: { bg: 'bg-green-100', text: 'text-green-800', icon: CheckCircle, label: 'Sent' },
    sending: { bg: 'bg-blue-100', text: 'text-blue-800', icon: Send, label: 'Sending' },
    scheduled: { bg: 'bg-amber-100', text: 'text-amber-800', icon: Calendar, label: 'Scheduled' },
    draft: { bg: 'bg-gray-100', text: 'text-gray-800', icon: Mail, label: 'Draft' },
    paused: { bg: 'bg-orange-100', text: 'text-orange-800', icon: Pause, label: 'Paused' },
    cancelled: { bg: 'bg-red-100', text: 'text-red-800', icon: XCircle, label: 'Cancelled' }
  }
  return configs[campaign.value?.status || 'draft'] || configs.draft
})

const counts = computed(() => {
  const c = campaign.value
  return {
    total: c?.totalRecipients ?? 0,
    sent: c?.sentCount ?? 0,
    delivered: c?.deliveredCount ?? 0,
    opened: c?.openCount ?? 0,
    clicked: c?.clickCount ?? 0,
    bounced: c?.bounceCount ?? 0,
    complained: c?.complaintCount ?? 0,
    unsubscribed: c?.unsubscribeCount ?? 0,
    failed: c?.failedCount ?? 0,
    unknown: c?.unknownCount ?? 0,
    skipped: c?.skippedCount ?? 0
  }
})

const trackOpens = computed(() => campaign.value?.trackOpens !== false)
const trackClicks = computed(() => campaign.value?.trackClicks !== false)

// Rates come from the stats endpoint (denominator: sent).
const openRate = computed(() => stats.value?.openRate ?? 0)
const clickRate = computed(() => stats.value?.clickRate ?? 0)
const clickToOpenRate = computed(() => stats.value?.clickToOpenRate ?? 0)
const bounceRate = computed(() => stats.value?.bounceRate ?? 0)
const complaintRate = computed(() => stats.value?.complaintRate ?? 0)
const unsubscribeRate = computed(() => stats.value?.unsubscribeRate ?? 0)
const deliveredRate = computed(() => stats.value?.deliveredRate ?? 0)

const sendProgress = computed(() => {
  const p = progress.value
  if (p) {
    const done = p.sent + p.failed + p.unknown + p.skipped
    return { done, total: p.total, percent: Math.round(p.percent), preparing: p.preparing }
  }
  const c = counts.value
  const done = c.sent + c.failed + c.unknown + c.skipped
  return { done, total: c.total, percent: c.total ? Math.round((done / c.total) * 100) : 0, preparing: !campaign.value?.preparedAt }
})

const throttledUntil = computed(() => progress.value?.throttledUntil ?? campaign.value?.throttledUntil ?? null)

const PRIVACY_PROXY_NOTE = 'Opens are approximate: privacy proxies such as Apple Mail Privacy Protection load images automatically, which inflates open counts.'

interface Banner { kind: 'info' | 'warning' | 'error'; text: string; action?: 'resume' | 'edit' }

const banners = computed<Banner[]>(() => {
  const c = campaign.value
  if (!c) return []
  const list: Banner[] = []
  if (c.status === 'sending' && sendProgress.value.preparing) {
    list.push({ kind: 'info', text: 'Preparing audience... Recipients are snapshotted when sending starts; contacts added later are not included.' })
  }
  if (c.status === 'sending' && throttledUntil.value) {
    list.push({ kind: 'warning', text: `Waiting for SES quota until ${formatDate(throttledUntil.value)}.` })
  }
  if (c.status === 'paused') {
    const why = c.statusReason ? statusReasonLabel(c.statusReason) : 'Paused'
    list.push({ kind: 'warning', text: `${why}. Fix the cause, then resume; recipients already sent are never sent again.`, action: 'resume' })
  }
  if (c.statusReason === 'legacy_requires_review') {
    list.push({ kind: 'warning', text: 'This campaign was created before sending through SES was enabled. Review the sender and content, then send it again.', action: 'edit' })
  }
  if (c.status === 'sent' && c.statusReason === 'no_eligible_recipients') {
    list.push({ kind: 'info', text: 'No contacts were eligible when sending started, so nothing was sent.' })
  }
  if (c.statusReason === 'provider_rejected') {
    list.push({ kind: 'error', text: 'SES rejected several messages in a row. If your SES account is in sandbox mode, only verified addresses can receive mail.' })
  }
  return list
})

const bannerClass = (kind: Banner['kind']) => ({
  info: 'bg-blue-50 border-blue-200 text-blue-800',
  warning: 'bg-amber-50 border-amber-200 text-amber-900',
  error: 'bg-red-50 border-red-200 text-red-800'
}[kind])

const canEdit = computed(() => campaign.value?.status === 'draft' || campaign.value?.status === 'scheduled')
const canCancel = computed(() => ['draft', 'scheduled', 'sending', 'paused'].includes(campaign.value?.status || ''))

// Previews come from the API renderer (tracking off) and are sanitized again before display.
const sanitizedPreviewHtml = computed(() => {
  const html = preview.value?.html
  if (!html) return ''
  return DOMPurify.sanitize(html, {
    ALLOWED_TAGS: ['p', 'br', 'b', 'i', 'u', 'a', 'strong', 'em', 'ul', 'ol', 'li',
                   'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'blockquote', 'pre', 'code',
                   'img', 'table', 'tr', 'td', 'th', 'thead', 'tbody', 'div', 'span',
                   'hr', 'sup', 'sub', 'small', 'font', 'center', 'style'],
    ALLOWED_ATTR: ['href', 'src', 'alt', 'style', 'class', 'target', 'width', 'height',
                   'border', 'cellpadding', 'cellspacing', 'align', 'valign', 'bgcolor',
                   'color', 'size', 'face'],
    ALLOW_DATA_ATTR: false
  })
})

// Charts
const funnelChartData = computed(() => {
  const labels = ['Sent', 'Delivered']
  const data = [counts.value.sent, counts.value.delivered]
  if (trackOpens.value) { labels.push('Opened'); data.push(counts.value.opened) }
  if (trackClicks.value) { labels.push('Clicked'); data.push(counts.value.clicked) }
  const colors = ['59, 130, 246', '16, 185, 129', '139, 92, 246', '236, 72, 153']
  return {
    labels,
    datasets: [{
      label: 'Recipients',
      data,
      backgroundColor: colors.slice(0, data.length).map(c => `rgba(${c}, 0.8)`),
      borderColor: colors.slice(0, data.length).map(c => `rgb(${c})`),
      borderWidth: 2,
      borderRadius: 8
    }]
  }
})

const funnelChartOptions = {
  indexAxis: 'y' as const,
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: { display: false },
    tooltip: {
      callbacks: {
        label: (context: any) => {
          const value = context.raw
          const total = counts.value.sent || 1
          return ` ${value.toLocaleString()} (${((value / total) * 100).toFixed(1)}%)`
        }
      }
    }
  },
  scales: {
    x: { beginAtZero: true, grid: { color: 'rgba(0, 0, 0, 0.05)' } },
    y: { grid: { display: false } }
  }
}

const opensChartData = computed(() => {
  const buckets = stats.value?.opensByHour ?? []
  return {
    labels: buckets.map(b => new Date(b.hour).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: 'numeric' })),
    datasets: [{
      label: 'Opens',
      data: buckets.map(b => b.opens),
      backgroundColor: 'rgba(16, 185, 129, 0.7)',
      borderRadius: 4
    }]
  }
})

const opensChartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: { legend: { display: false } },
  scales: {
    y: { beginAtZero: true, ticks: { precision: 0 }, grid: { color: 'rgba(0, 0, 0, 0.05)' } },
    x: { grid: { display: false } }
  }
}

const deliveryDoughnutData = computed(() => {
  const c = counts.value
  const awaiting = Math.max(0, c.sent - c.delivered - c.bounced - c.complained)
  return {
    labels: ['Delivered', 'Bounced', 'Complained', 'Awaiting feedback'],
    datasets: [{
      data: [c.delivered, c.bounced, c.complained, awaiting],
      backgroundColor: ['#10B981', '#EF4444', '#F59E0B', '#CBD5E1'],
      borderWidth: 0,
      cutout: '70%'
    }]
  }
})

const deliveryDoughnutOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: { position: 'bottom' as const, labels: { padding: 16, usePointStyle: true, pointStyle: 'circle' } }
  }
}

const topLinks = computed(() => stats.value?.clicksByLink ?? [])

// Data loading
const fetchStats = async () => {
  try {
    stats.value = await campaignApi.getStats(uuid.value)
    if (stats.value?.campaign) campaign.value = stats.value.campaign
  } catch (e) {
    console.error('Failed to fetch stats:', e)
  }
}

const fetchPreview = async () => {
  previewError.value = null
  try {
    preview.value = await campaignApi.preview(uuid.value)
  } catch (e) {
    previewError.value = e instanceof Error ? e.message : 'Preview unavailable'
  }
}

const fetchRecipients = async () => {
  if (!campaign.value || campaign.value.status === 'draft' || campaign.value.status === 'scheduled') {
    recipients.value = []
    recipientTotal.value = 0
    return
  }
  recipientsLoading.value = true
  try {
    const result = await campaignApi.recipients(uuid.value, recipientFilter.value, recipientPage.value, RECIPIENT_PAGE_SIZE)
    recipients.value = result?.recipients ?? []
    recipientTotal.value = result?.total ?? 0
  } catch (e) {
    console.error('Failed to fetch recipients:', e)
  } finally {
    recipientsLoading.value = false
  }
}

const fetchCampaign = async () => {
  loading.value = true
  error.value = null
  try {
    campaign.value = await campaignApi.get(uuid.value)
    await Promise.all([fetchStats(), fetchPreview(), fetchRecipients()])
  } catch (e) {
    error.value = 'Failed to load campaign'
    console.error('Failed to fetch campaign:', e)
  } finally {
    loading.value = false
  }
}

const refreshData = async () => {
  refreshing.value = true
  try {
    campaign.value = await campaignApi.get(uuid.value)
    await Promise.all([fetchStats(), fetchRecipients()])
  } catch (e) {
    console.error('Failed to refresh campaign:', e)
  } finally {
    refreshing.value = false
  }
}

const recipientPages = computed(() => Math.max(1, Math.ceil(recipientTotal.value / RECIPIENT_PAGE_SIZE)))

watch(recipientFilter, () => {
  recipientPage.value = 1
  fetchRecipients()
})

const goToRecipientPage = (page: number) => {
  recipientPage.value = Math.min(Math.max(1, page), recipientPages.value)
  fetchRecipients()
}

// Actions
const goBack = () => {
  router.push({ name: 'campaigns' })
}

const runAction = async (fallback: string, action: () => Promise<Campaign>) => {
  actionError.value = null
  actionBusy.value = true
  try {
    campaign.value = await action()
    progress.value = null
    await refreshData()
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : fallback
  } finally {
    actionBusy.value = false
  }
}

const pauseCampaign = () => campaign.value && runAction('Failed to pause campaign', () => campaignsStore.pauseCampaign(uuid.value))
const resumeCampaign = () => campaign.value && runAction('Failed to resume campaign', () => campaignsStore.resumeCampaign(uuid.value))
const cancelCampaign = async () => {
  confirmCancel.value = false
  await runAction('Failed to cancel campaign', () => campaignsStore.cancelCampaign(uuid.value))
}

const onBannerAction = (action: Banner['action']) => {
  if (action === 'resume') resumeCampaign()
  if (action === 'edit') showWizard.value = true
}

const closeWizard = async () => {
  showWizard.value = false
  await refreshData()
  await fetchPreview()
}

// Polling
const pollCampaign = async () => {
  const c = campaign.value
  if (!c || document.hidden) return
  if (c.status === 'scheduled' && c.scheduledAt && new Date(c.scheduledAt).getTime() > Date.now()) return
  try {
    const latest = await campaignApi.progress(uuid.value)
    const changed = latest.status !== c.status || latest.preparing !== !c.preparedAt
    progress.value = latest
    campaign.value = { ...c, status: latest.status, statusReason: latest.statusReason, throttledUntil: latest.throttledUntil }
    if (changed) await refreshData()
  } catch (e) {
    console.error('Failed to refresh progress:', e)
  }
}

const startPolling = () => {
  if (pollTimer) return
  pollTimer = setInterval(pollCampaign, POLL_INTERVAL_MS)
}

const stopPolling = () => {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

const onVisibilityChange = () => {
  if (!document.hidden && pollTimer) pollCampaign()
}

// Formatting
const formatDate = (dateString: string | null | undefined) => {
  if (!dateString) return '-'
  return new Date(dateString).toLocaleDateString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: 'numeric',
    minute: '2-digit'
  })
}

const formatNumber = (num: number) => {
  if (num >= 1000000) return `${(num / 1000000).toFixed(1)}M`
  if (num >= 1000) return `${(num / 1000).toFixed(1)}K`
  return num.toString()
}

const recipientStatusClass = (status: string) => ({
  sent: 'bg-green-100 text-green-800',
  failed: 'bg-red-100 text-red-800',
  unknown: 'bg-amber-100 text-amber-800',
  skipped: 'bg-gray-100 text-gray-700',
  cancelled: 'bg-gray-100 text-gray-500'
}[status] || 'bg-blue-100 text-blue-800')

// Lifecycle
onMounted(() => {
  document.addEventListener('visibilitychange', onVisibilityChange)
  fetchCampaign()
})

onUnmounted(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibilityChange)
})

// A poll that sees the status change refreshes the campaign and stats itself.
watch(() => campaign.value?.status, (status) => {
  if (status === 'sending' || status === 'scheduled') {
    startPolling()
  } else {
    stopPolling()
  }
})
</script>

<template>
  <AppLayout>
    <div class="flex-1 flex flex-col bg-gray-50 min-h-0">
      <!-- Loading -->
      <div v-if="loading" class="flex-1 flex items-center justify-center">
        <div class="text-center">
          <Spinner size="lg" />
          <p class="text-gray-500 mt-3">Loading campaign...</p>
        </div>
      </div>

      <!-- Error -->
      <div v-else-if="error" class="flex-1 flex items-center justify-center">
        <div class="text-center">
          <AlertTriangle class="w-16 h-16 text-red-400 mx-auto mb-4" />
          <h2 class="text-xl font-semibold text-gray-900 mb-2">{{ error }}</h2>
          <Button @click="goBack" variant="secondary">
            <ArrowLeft class="w-4 h-4" />
            Back to Campaigns
          </Button>
        </div>
      </div>

      <!-- Campaign Detail -->
      <template v-else-if="campaign">
        <!-- Fixed Header -->
        <div class="bg-white border-b border-gray-200 px-6 py-4 flex-shrink-0 shadow-sm">
          <div class="flex items-center gap-4">
            <button
              @click="goBack"
              class="p-2 hover:bg-gray-100 rounded-lg transition-colors"
              aria-label="Back to campaigns"
            >
              <ArrowLeft class="w-5 h-5 text-gray-600" />
            </button>
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-3 flex-wrap">
                <h1 class="text-xl font-semibold text-gray-900 truncate">{{ campaign.name }}</h1>
                <span :class="['px-3 py-1 rounded-full text-xs font-medium capitalize flex items-center gap-1.5', statusConfig.bg, statusConfig.text]">
                  <component :is="statusConfig.icon" class="w-3.5 h-3.5" />
                  {{ statusConfig.label }}
                </span>
                <span v-if="campaign.statusReason" class="px-2 py-0.5 rounded bg-amber-50 text-amber-800 text-xs" data-test="status-reason">
                  {{ statusReasonLabel(campaign.statusReason) }}
                </span>
              </div>
              <p class="text-sm text-gray-500 truncate mt-0.5">{{ campaign.subject }}</p>
            </div>
            <div class="flex items-center gap-2 flex-shrink-0">
              <button
                @click="refreshData"
                class="p-2 text-gray-500 hover:text-gray-700 hover:bg-gray-100 rounded-lg transition-colors"
                :class="{ 'animate-spin': refreshing }"
                title="Refresh data"
                aria-label="Refresh data"
              >
                <RefreshCw class="w-5 h-5" />
              </button>
              <Button v-if="canEdit" variant="secondary" @click="showWizard = true" :disabled="actionBusy">
                <Edit3 class="w-4 h-4" />
                Edit
              </Button>
              <Button
                v-if="campaign.status === 'sending'"
                variant="secondary"
                @click="pauseCampaign"
                :disabled="actionBusy"
                class="text-amber-600 border-amber-300 hover:bg-amber-50"
              >
                <Pause class="w-4 h-4" />
                Pause
              </Button>
              <Button
                v-if="campaign.status === 'paused'"
                @click="resumeCampaign"
                :disabled="actionBusy"
                class="bg-green-600 hover:bg-green-700"
              >
                <Play class="w-4 h-4" />
                Resume
              </Button>
              <Button v-if="canCancel" variant="danger" @click="confirmCancel = true" :disabled="actionBusy">
                <XCircle class="w-4 h-4" />
                Cancel
              </Button>
            </div>
          </div>

          <div v-if="actionError" class="mt-3 p-3 bg-red-50 border border-red-200 rounded-lg text-sm text-red-700" role="alert">
            {{ actionError }}
          </div>

          <!-- Progress bar for sending campaigns -->
          <div v-if="campaign.status === 'sending' && !sendProgress.preparing && sendProgress.total" class="mt-4" data-test="send-progress">
            <div class="flex items-center justify-between text-sm text-gray-600 mb-1.5">
              <span class="flex items-center gap-2">
                <Activity class="w-4 h-4 text-blue-500 animate-pulse" />
                Sending in progress...
              </span>
              <span class="font-medium">
                {{ sendProgress.done.toLocaleString() }} / {{ sendProgress.total.toLocaleString() }}
                <span class="text-blue-600">({{ sendProgress.percent }}%)</span>
              </span>
            </div>
            <div class="w-full h-2 bg-gray-200 rounded-full overflow-hidden">
              <div
                class="h-full bg-gradient-to-r from-blue-500 to-blue-600 transition-all duration-500 ease-out"
                :style="{ width: `${sendProgress.percent}%` }"
              />
            </div>
          </div>
        </div>

        <!-- Scrollable Content -->
        <div class="flex-1 overflow-y-auto">
          <div class="px-6 py-6 space-y-6">

            <!-- Banners -->
            <div
              v-for="banner in banners"
              :key="banner.text"
              :class="['p-4 rounded-xl border flex items-start gap-3', bannerClass(banner.kind)]"
              role="status"
              data-test="banner"
            >
              <Clock v-if="banner.kind === 'info'" class="w-5 h-5 flex-shrink-0 mt-0.5" />
              <AlertTriangle v-else class="w-5 h-5 flex-shrink-0 mt-0.5" />
              <p class="flex-1 text-sm">{{ banner.text }}</p>
              <Button v-if="banner.action === 'resume' && campaign.status === 'paused'" size="sm" @click="onBannerAction('resume')" :disabled="actionBusy">
                <Play class="w-4 h-4" />
                Resume
              </Button>
              <Button v-if="banner.action === 'edit' && canEdit" size="sm" variant="secondary" @click="onBannerAction('edit')">
                <Edit3 class="w-4 h-4" />
                Edit
              </Button>
            </div>

            <!-- Tracking notes -->
            <div v-if="!trackOpens || !trackClicks" class="flex flex-wrap gap-2 text-xs text-gray-600" data-test="tracking-notes">
              <span v-if="!trackOpens" class="px-2 py-1 rounded bg-gray-100 flex items-center gap-1"><EyeOff class="w-3.5 h-3.5" />Opens not tracked</span>
              <span v-if="!trackClicks" class="px-2 py-1 rounded bg-gray-100 flex items-center gap-1"><MousePointer class="w-3.5 h-3.5" />Clicks not tracked</span>
            </div>

            <!-- Summary Card -->
            <div class="bg-gradient-to-r from-indigo-600 to-purple-600 rounded-2xl p-6 text-white shadow-lg">
              <div class="grid grid-cols-2 sm:grid-cols-4 gap-6 text-center">
                <div>
                  <div class="text-3xl font-bold">{{ formatNumber(counts.total) }}</div>
                  <div class="text-indigo-200 text-sm">Recipients</div>
                </div>
                <div>
                  <div class="text-3xl font-bold" data-test="summary-sent">{{ formatNumber(counts.sent) }}</div>
                  <div class="text-indigo-200 text-sm">Sent</div>
                </div>
                <div v-if="trackOpens" :title="PRIVACY_PROXY_NOTE">
                  <div class="text-3xl font-bold">{{ openRate.toFixed(1) }}%</div>
                  <div class="text-indigo-200 text-sm flex items-center justify-center gap-1">Open Rate <Info class="w-3.5 h-3.5" /></div>
                </div>
                <div v-if="trackClicks">
                  <div class="text-3xl font-bold">{{ clickRate.toFixed(1) }}%</div>
                  <div class="text-indigo-200 text-sm">Click Rate</div>
                </div>
              </div>
            </div>

            <!-- Quick Stats Grid -->
            <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-4">
              <div class="bg-white rounded-xl border border-gray-200 p-4">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-emerald-100 flex items-center justify-center flex-shrink-0">
                    <CheckCircle class="w-5 h-5 text-emerald-600" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-xl font-bold text-gray-900 truncate">{{ deliveredRate.toFixed(1) }}%</div>
                    <div class="text-xs text-gray-500">Delivered</div>
                  </div>
                </div>
              </div>
              <div v-if="trackOpens" class="bg-white rounded-xl border border-gray-200 p-4" :title="PRIVACY_PROXY_NOTE" data-test="open-rate-card">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-green-100 flex items-center justify-center flex-shrink-0">
                    <Eye class="w-5 h-5 text-green-600" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-xl font-bold text-gray-900 truncate">{{ openRate.toFixed(1) }}%</div>
                    <div class="text-xs text-gray-500">Open Rate</div>
                  </div>
                </div>
              </div>
              <div v-if="trackClicks" class="bg-white rounded-xl border border-gray-200 p-4" data-test="click-rate-card">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-purple-100 flex items-center justify-center flex-shrink-0">
                    <MousePointer class="w-5 h-5 text-purple-600" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-xl font-bold text-gray-900 truncate">{{ clickRate.toFixed(1) }}%</div>
                    <div class="text-xs text-gray-500">Click Rate</div>
                  </div>
                </div>
              </div>
              <div v-if="trackOpens && trackClicks" class="bg-white rounded-xl border border-gray-200 p-4">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-pink-100 flex items-center justify-center flex-shrink-0">
                    <Target class="w-5 h-5 text-pink-600" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-xl font-bold text-gray-900 truncate">{{ clickToOpenRate.toFixed(1) }}%</div>
                    <div class="text-xs text-gray-500">Click-to-Open</div>
                  </div>
                </div>
              </div>
              <div class="bg-white rounded-xl border border-gray-200 p-4">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-red-100 flex items-center justify-center flex-shrink-0">
                    <AlertTriangle class="w-5 h-5 text-red-600" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-xl font-bold text-gray-900 truncate">{{ bounceRate.toFixed(1) }}%</div>
                    <div class="text-xs text-gray-500">Bounce Rate</div>
                  </div>
                </div>
              </div>
              <div class="bg-white rounded-xl border border-gray-200 p-4">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-amber-100 flex items-center justify-center flex-shrink-0">
                    <Shield class="w-5 h-5 text-amber-600" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-xl font-bold text-gray-900 truncate">{{ complaintRate.toFixed(2) }}%</div>
                    <div class="text-xs text-gray-500">Complaint Rate</div>
                  </div>
                </div>
              </div>
            </div>

            <!-- Charts -->
            <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
              <div class="bg-white rounded-xl border border-gray-200 p-6">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <TrendingUp class="w-5 h-5 text-blue-500" />
                  Engagement Funnel
                </h3>
                <div class="h-56">
                  <Bar :data="funnelChartData" :options="funnelChartOptions" />
                </div>
              </div>

              <div class="bg-white rounded-xl border border-gray-200 p-6">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <Shield class="w-5 h-5 text-green-500" />
                  Delivery Status
                </h3>
                <div class="h-56 relative">
                  <Doughnut :data="deliveryDoughnutData" :options="deliveryDoughnutOptions" />
                </div>
              </div>
            </div>

            <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
              <div v-if="trackOpens" class="bg-white rounded-xl border border-gray-200 p-6">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2" :title="PRIVACY_PROXY_NOTE">
                  <BarChart3 class="w-5 h-5 text-purple-500" />
                  Opens by Hour
                </h3>
                <div v-if="opensChartData.labels.length" class="h-56">
                  <Bar :data="opensChartData" :options="opensChartOptions" />
                </div>
                <p v-else class="text-sm text-gray-500">No opens recorded yet.</p>
              </div>

              <div v-if="trackClicks" class="bg-white rounded-xl border border-gray-200 p-6">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <Link2 class="w-5 h-5 text-pink-500" />
                  Top Links
                </h3>
                <table v-if="topLinks.length" class="w-full text-sm" data-test="top-links">
                  <thead>
                    <tr class="text-left text-xs text-gray-500 uppercase">
                      <th class="py-1">URL</th>
                      <th class="py-1 text-right">Unique</th>
                      <th class="py-1 text-right">Total</th>
                    </tr>
                  </thead>
                  <tbody class="divide-y divide-gray-100">
                    <tr v-for="link in topLinks" :key="link.url">
                      <td class="py-2 pr-2 truncate max-w-xs" :title="link.url">{{ link.url }}</td>
                      <td class="py-2 text-right font-medium">{{ link.uniqueClicks.toLocaleString() }}</td>
                      <td class="py-2 text-right text-gray-500">{{ link.clicks.toLocaleString() }}</td>
                    </tr>
                  </tbody>
                </table>
                <p v-else class="text-sm text-gray-500">No clicks recorded yet.</p>
              </div>
            </div>

            <!-- Detailed Stats & Campaign Info -->
            <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
              <div class="bg-white rounded-xl border border-gray-200 p-6">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <Zap class="w-5 h-5 text-amber-500" />
                  Detailed Statistics
                </h3>
                <dl class="space-y-2 text-sm">
                  <div class="flex justify-between py-1.5 border-b border-gray-100"><dt class="text-gray-600">Recipients</dt><dd class="font-semibold">{{ counts.total.toLocaleString() }}</dd></div>
                  <div class="flex justify-between py-1.5 border-b border-gray-100"><dt class="text-gray-600">Sent</dt><dd class="font-semibold">{{ counts.sent.toLocaleString() }}</dd></div>
                  <div class="flex justify-between py-1.5 border-b border-gray-100"><dt class="text-gray-600">Delivered</dt><dd class="font-semibold text-emerald-600">{{ counts.delivered.toLocaleString() }}</dd></div>
                  <div v-if="trackOpens" class="flex justify-between py-1.5 border-b border-gray-100"><dt class="text-gray-600">Opened (unique)</dt><dd class="font-semibold text-green-600">{{ counts.opened.toLocaleString() }}</dd></div>
                  <div v-if="trackClicks" class="flex justify-between py-1.5 border-b border-gray-100"><dt class="text-gray-600">Clicked (unique)</dt><dd class="font-semibold text-purple-600">{{ counts.clicked.toLocaleString() }}</dd></div>
                  <div class="flex justify-between py-1.5 border-b border-gray-100"><dt class="text-gray-600">Bounced</dt><dd class="font-semibold text-red-600">{{ counts.bounced.toLocaleString() }}</dd></div>
                  <div class="flex justify-between py-1.5 border-b border-gray-100"><dt class="text-gray-600">Complaints</dt><dd class="font-semibold text-amber-600">{{ counts.complained.toLocaleString() }}</dd></div>
                  <div class="flex justify-between py-1.5 border-b border-gray-100"><dt class="text-gray-600">Unsubscribed</dt><dd class="font-semibold text-orange-600">{{ counts.unsubscribed.toLocaleString() }} <span class="font-normal text-gray-400">({{ unsubscribeRate.toFixed(1) }}%)</span></dd></div>
                  <div class="flex justify-between py-1.5 border-b border-gray-100"><dt class="text-gray-600">Failed</dt><dd class="font-semibold">{{ counts.failed.toLocaleString() }}</dd></div>
                  <div class="flex justify-between py-1.5 border-b border-gray-100" title="SES did not confirm these sends; they are never retried."><dt class="text-gray-600">Outcome uncertain</dt><dd class="font-semibold">{{ counts.unknown.toLocaleString() }}</dd></div>
                  <div class="flex justify-between py-1.5"><dt class="text-gray-600">Skipped</dt><dd class="font-semibold">{{ counts.skipped.toLocaleString() }}</dd></div>
                </dl>
              </div>

              <div class="bg-white rounded-xl border border-gray-200 p-6">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <Mail class="w-5 h-5 text-blue-500" />
                  Campaign Details
                </h3>
                <dl class="space-y-2 text-sm">
                  <div class="flex justify-between py-1.5 border-b border-gray-100 gap-2"><dt class="text-gray-600">From</dt><dd class="font-medium text-gray-900 truncate">{{ campaign.fromName }} &lt;{{ campaign.fromEmail }}&gt;</dd></div>
                  <div class="flex justify-between py-1.5 border-b border-gray-100 gap-2">
                    <dt class="text-gray-600">List</dt>
                    <dd class="font-medium text-gray-900 truncate">
                      {{ campaign.listName || '-' }}
                      <span v-if="campaign.listType === 'dynamic'" class="ml-1 px-1.5 py-0.5 rounded bg-indigo-100 text-indigo-700 text-xs">Segment</span>
                    </dd>
                  </div>
                  <div class="flex justify-between py-1.5 border-b border-gray-100"><dt class="text-gray-600">Created</dt><dd>{{ formatDate(campaign.createdAt) }}</dd></div>
                  <div v-if="campaign.scheduledAt" class="flex justify-between py-1.5 border-b border-gray-100"><dt class="text-gray-600">Scheduled for</dt><dd>{{ formatDate(campaign.scheduledAt) }}</dd></div>
                  <div v-if="campaign.startedAt" class="flex justify-between py-1.5 border-b border-gray-100"><dt class="text-gray-600">Started</dt><dd>{{ formatDate(campaign.startedAt) }}</dd></div>
                  <div v-if="campaign.completedAt" class="flex justify-between py-1.5"><dt class="text-gray-600">Completed</dt><dd>{{ formatDate(campaign.completedAt) }}</dd></div>
                </dl>
              </div>

              <div class="bg-white rounded-xl border border-gray-200 p-6">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <Eye class="w-5 h-5 text-green-500" />
                  Email Preview
                </h3>
                <div class="border border-gray-200 rounded-lg overflow-hidden">
                  <div class="px-3 py-2 bg-gray-50 border-b border-gray-200 text-xs text-gray-500">
                    <div><strong class="text-gray-700">Subject:</strong> {{ preview?.subject || campaign.subject }}</div>
                    <div class="mt-1"><strong class="text-gray-700">From:</strong> {{ campaign.fromName }} &lt;{{ campaign.fromEmail }}&gt;</div>
                  </div>
                  <p v-if="previewError" class="p-3 text-xs text-red-600">{{ previewError }}</p>
                  <div
                    v-else-if="sanitizedPreviewHtml"
                    class="p-3 prose prose-sm max-w-none overflow-y-auto text-xs"
                    style="max-height: 240px;"
                    data-test="preview-html"
                    v-html="sanitizedPreviewHtml"
                  />
                  <pre v-else-if="preview?.text" class="p-3 text-xs whitespace-pre-wrap overflow-y-auto" style="max-height: 240px;">{{ preview.text }}</pre>
                  <p v-else class="p-3 text-xs text-gray-400">No content</p>
                </div>
                <p v-if="preview?.unknownVariables?.length" class="mt-2 text-xs text-amber-700">
                  Unknown variables render empty: {{ preview.unknownVariables.join(', ') }}
                </p>
              </div>
            </div>

            <!-- Recipients -->
            <div v-if="campaign.status !== 'draft' && campaign.status !== 'scheduled'" class="bg-white rounded-xl border border-gray-200 p-6">
              <div class="flex items-center justify-between mb-4 gap-3 flex-wrap">
                <h3 class="text-base font-semibold text-gray-900 flex items-center gap-2">
                  <Users class="w-5 h-5 text-blue-500" />
                  Recipients
                  <span class="text-sm font-normal text-gray-500">({{ recipientTotal.toLocaleString() }})</span>
                </h3>
                <label class="text-sm text-gray-600 flex items-center gap-2">
                  Status
                  <select v-model="recipientFilter" class="border border-gray-300 rounded-lg px-2 py-1 text-sm" aria-label="Filter recipients by status">
                    <option v-for="f in recipientFilters" :key="f.value" :value="f.value">{{ f.label }}</option>
                  </select>
                </label>
              </div>
              <div v-if="recipientsLoading && !recipients.length" class="py-6 text-center"><Spinner /></div>
              <p v-else-if="!recipients.length" class="text-sm text-gray-500">No recipients{{ recipientFilter ? ' with this status' : ' yet' }}.</p>
              <div v-else class="overflow-x-auto">
                <table class="w-full text-sm" data-test="recipients-table">
                  <thead>
                    <tr class="text-left text-xs text-gray-500 uppercase border-b border-gray-200">
                      <th class="py-2 pr-3">Email</th>
                      <th class="py-2 pr-3">Status</th>
                      <th class="py-2 pr-3">Delivery</th>
                      <th class="py-2 pr-3">Sent</th>
                      <th v-if="trackOpens" class="py-2 pr-3 text-right">Opens</th>
                      <th v-if="trackClicks" class="py-2 pr-3 text-right">Clicks</th>
                      <th class="py-2">Notes</th>
                    </tr>
                  </thead>
                  <tbody class="divide-y divide-gray-100">
                    <tr v-for="(r, index) in recipients" :key="`${r.email}-${index}`">
                      <td class="py-2 pr-3 truncate max-w-[220px]">{{ r.email }}</td>
                      <td class="py-2 pr-3">
                        <span :class="['px-2 py-0.5 rounded text-xs font-medium capitalize', recipientStatusClass(r.status)]">{{ r.status === 'unknown' ? 'uncertain' : r.status }}</span>
                      </td>
                      <td class="py-2 pr-3 capitalize">{{ r.deliveryStatus || '-' }}</td>
                      <td class="py-2 pr-3 whitespace-nowrap">{{ r.sentAt ? formatDate(r.sentAt) : '-' }}</td>
                      <td v-if="trackOpens" class="py-2 pr-3 text-right">{{ r.openCount }}</td>
                      <td v-if="trackClicks" class="py-2 pr-3 text-right">{{ r.clickCount }}</td>
                      <td class="py-2 text-xs text-gray-600">
                        <span v-if="r.status === 'unknown'" class="text-amber-700">Outcome uncertain — not retried</span>
                        <span v-else-if="r.skipReason">Skipped: {{ r.skipReason.replace(/_/g, ' ') }}</span>
                        <span v-else-if="r.error" class="text-red-600">{{ r.error }}</span>
                        <span v-if="r.unsubscribedAt" class="ml-1 text-orange-600">Unsubscribed</span>
                      </td>
                    </tr>
                  </tbody>
                </table>
                <div v-if="recipientPages > 1" class="flex items-center justify-end gap-2 mt-3 text-sm">
                  <Button size="sm" variant="secondary" :disabled="recipientPage <= 1" @click="goToRecipientPage(recipientPage - 1)">Previous</Button>
                  <span class="text-gray-600">Page {{ recipientPage }} of {{ recipientPages }}</span>
                  <Button size="sm" variant="secondary" :disabled="recipientPage >= recipientPages" @click="goToRecipientPage(recipientPage + 1)">Next</Button>
                </div>
              </div>
            </div>

          </div>
        </div>
      </template>
    </div>

    <!-- Cancel confirmation -->
    <div v-if="confirmCancel && campaign" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50" role="dialog" aria-modal="true">
      <div class="bg-white rounded-xl shadow-2xl w-full max-w-md mx-4 p-6">
        <h3 class="text-lg font-semibold text-gray-900 mb-2">Cancel this campaign?</h3>
        <p class="text-gray-600 mb-6 text-sm">
          Recipients who have not been sent to yet are skipped and a cancelled campaign cannot be resumed.
          Messages already handed to SES still finish.
        </p>
        <div class="flex items-center justify-end gap-3">
          <Button variant="secondary" @click="confirmCancel = false">Keep Campaign</Button>
          <Button variant="danger" @click="cancelCampaign" :loading="actionBusy">
            <XCircle class="w-4 h-4" />
            Cancel Campaign
          </Button>
        </div>
      </div>
    </div>

    <CampaignWizard v-if="showWizard" :campaign="campaign" @close="closeWizard" @created="closeWizard" @updated="closeWizard" />
  </AppLayout>
</template>
