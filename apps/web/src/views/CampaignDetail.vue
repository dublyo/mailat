<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  ArrowLeft,
  Mail,
  Users,
  MousePointer,
  Eye,
  AlertTriangle,
  Clock,
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
  PieChart
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
import { campaignApi, type Campaign, type CampaignStats } from '@/lib/api'
import { useCampaignsStore } from '@/stores/campaigns'

// Register Chart.js components
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
const stats = ref<CampaignStats | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const refreshing = ref(false)
const sseProgress = ref<{ sent: number; total: number; percent: number } | null>(null)

// SSE connection for real-time progress
let eventSource: EventSource | null = null

// Computed
const uuid = computed(() => route.params.uuid as string)

const statusConfig = computed(() => {
  const configs: Record<string, { bg: string; text: string; icon: any; label: string }> = {
    sent: { bg: 'bg-green-100', text: 'text-green-800', icon: CheckCircle, label: 'Sent' },
    sending: { bg: 'bg-blue-100', text: 'text-blue-800', icon: Send, label: 'Sending' },
    scheduled: { bg: 'bg-amber-100', text: 'text-amber-800', icon: Calendar, label: 'Scheduled' },
    draft: { bg: 'bg-gray-100', text: 'text-gray-800', icon: Mail, label: 'Draft' },
    paused: { bg: 'bg-orange-100', text: 'text-orange-800', icon: Pause, label: 'Paused' }
  }
  return configs[campaign.value?.status || 'draft'] || configs.draft
})

// Rate calculations
const openRate = computed(() => {
  if (!stats.value?.sent || stats.value.sent === 0) return 0
  return (stats.value.opened / stats.value.sent) * 100
})

const clickRate = computed(() => {
  if (!stats.value?.sent || stats.value.sent === 0) return 0
  return (stats.value.clicked / stats.value.sent) * 100
})

const clickToOpenRate = computed(() => {
  if (!stats.value?.opened || stats.value.opened === 0) return 0
  return (stats.value.clicked / stats.value.opened) * 100
})

const bounceRate = computed(() => {
  if (!stats.value?.sent || stats.value.sent === 0) return 0
  return (stats.value.bounced / stats.value.sent) * 100
})

const unsubscribeRate = computed(() => {
  if (!stats.value?.sent || stats.value.sent === 0) return 0
  return (stats.value.unsubscribed / stats.value.sent) * 100
})

const deliveryRate = computed(() => {
  if (!stats.value?.sent || stats.value.sent === 0) return 0
  return ((stats.value.sent - stats.value.bounced) / stats.value.sent) * 100
})

// Performance score (weighted average)
const performanceScore = computed(() => {
  if (!stats.value?.sent || stats.value.sent === 0) return 0
  const openWeight = 0.3
  const clickWeight = 0.4
  const bounceWeight = 0.2
  const unsubWeight = 0.1

  const score = (
    (openRate.value * openWeight) +
    (clickRate.value * clickWeight) +
    ((100 - bounceRate.value) * bounceWeight) +
    ((100 - unsubscribeRate.value) * unsubWeight)
  )
  return Math.min(100, Math.max(0, score))
})

const performanceLabel = computed(() => {
  const score = performanceScore.value
  if (score >= 80) return { label: 'Excellent', color: 'text-green-600' }
  if (score >= 60) return { label: 'Good', color: 'text-blue-600' }
  if (score >= 40) return { label: 'Average', color: 'text-amber-600' }
  return { label: 'Needs Improvement', color: 'text-red-600' }
})

// Sanitize campaign HTML content to prevent XSS attacks
const sanitizedCampaignHtml = computed(() => {
  if (!campaign.value?.htmlBody) return '<p class="text-gray-400">No content</p>'
  return DOMPurify.sanitize(campaign.value.htmlBody, {
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

// Engagement Funnel Chart
const funnelChartData = computed(() => ({
  labels: ['Sent', 'Delivered', 'Opened', 'Clicked'],
  datasets: [{
    label: 'Emails',
    data: [
      stats.value?.sent || 0,
      (stats.value?.sent || 0) - (stats.value?.bounced || 0),
      stats.value?.opened || 0,
      stats.value?.clicked || 0
    ],
    backgroundColor: [
      'rgba(59, 130, 246, 0.8)',
      'rgba(16, 185, 129, 0.8)',
      'rgba(139, 92, 246, 0.8)',
      'rgba(236, 72, 153, 0.8)'
    ],
    borderColor: [
      'rgb(59, 130, 246)',
      'rgb(16, 185, 129)',
      'rgb(139, 92, 246)',
      'rgb(236, 72, 153)'
    ],
    borderWidth: 2,
    borderRadius: 8
  }]
}))

const funnelChartOptions = {
  indexAxis: 'y' as const,
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      display: false
    },
    tooltip: {
      callbacks: {
        label: (context: any) => {
          const value = context.raw
          const total = stats.value?.sent || 1
          const percent = ((value / total) * 100).toFixed(1)
          return ` ${value.toLocaleString()} (${percent}%)`
        }
      }
    }
  },
  scales: {
    x: {
      beginAtZero: true,
      grid: {
        color: 'rgba(0, 0, 0, 0.05)'
      }
    },
    y: {
      grid: {
        display: false
      }
    }
  }
}

// Engagement Rates Comparison Chart
const ratesChartData = computed(() => ({
  labels: ['Open Rate', 'Click Rate', 'Click-to-Open', 'Bounce Rate', 'Unsub Rate'],
  datasets: [{
    label: 'Rate %',
    data: [
      openRate.value,
      clickRate.value,
      clickToOpenRate.value,
      bounceRate.value,
      unsubscribeRate.value
    ],
    backgroundColor: [
      'rgba(16, 185, 129, 0.8)',
      'rgba(139, 92, 246, 0.8)',
      'rgba(236, 72, 153, 0.8)',
      'rgba(239, 68, 68, 0.8)',
      'rgba(245, 158, 11, 0.8)'
    ],
    borderColor: [
      'rgb(16, 185, 129)',
      'rgb(139, 92, 246)',
      'rgb(236, 72, 153)',
      'rgb(239, 68, 68)',
      'rgb(245, 158, 11)'
    ],
    borderWidth: 2,
    borderRadius: 6
  }]
}))

const ratesChartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      display: false
    },
    tooltip: {
      callbacks: {
        label: (context: any) => ` ${context.raw.toFixed(2)}%`
      }
    }
  },
  scales: {
    y: {
      beginAtZero: true,
      max: 100,
      grid: {
        color: 'rgba(0, 0, 0, 0.05)'
      },
      ticks: {
        callback: (value: any) => `${value}%`
      }
    },
    x: {
      grid: {
        display: false
      }
    }
  }
}

// Delivery Status Doughnut
const deliveryDoughnutData = computed(() => ({
  labels: ['Delivered', 'Bounced'],
  datasets: [{
    data: [
      (stats.value?.sent || 0) - (stats.value?.bounced || 0),
      stats.value?.bounced || 0
    ],
    backgroundColor: ['#10B981', '#EF4444'],
    borderWidth: 0,
    cutout: '70%'
  }]
}))

const deliveryDoughnutOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      position: 'bottom' as const,
      labels: {
        padding: 16,
        usePointStyle: true,
        pointStyle: 'circle'
      }
    }
  }
}

// Engagement Breakdown Doughnut
const engagementDoughnutData = computed(() => ({
  labels: ['Clicked', 'Opened Only', 'Not Engaged'],
  datasets: [{
    data: [
      stats.value?.clicked || 0,
      (stats.value?.opened || 0) - (stats.value?.clicked || 0),
      (stats.value?.sent || 0) - (stats.value?.opened || 0)
    ],
    backgroundColor: ['#8B5CF6', '#10B981', '#94A3B8'],
    borderWidth: 0,
    cutout: '70%'
  }]
}))

const engagementDoughnutOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      position: 'bottom' as const,
      labels: {
        padding: 16,
        usePointStyle: true,
        pointStyle: 'circle'
      }
    }
  }
}

// Methods
const fetchCampaign = async () => {
  loading.value = true
  error.value = null
  try {
    campaign.value = await campaignApi.get(uuid.value)
    await fetchStats()
  } catch (e) {
    error.value = 'Failed to load campaign'
    console.error('Failed to fetch campaign:', e)
  } finally {
    loading.value = false
  }
}

const fetchStats = async () => {
  try {
    stats.value = await campaignApi.getStats(uuid.value)
  } catch (e) {
    console.error('Failed to fetch stats:', e)
  }
}

const refreshData = async () => {
  refreshing.value = true
  try {
    await fetchStats()
    campaign.value = await campaignApi.get(uuid.value)
  } finally {
    refreshing.value = false
  }
}

const goBack = () => {
  router.push({ name: 'campaigns' })
}

const pauseCampaign = async () => {
  if (!campaign.value) return
  try {
    await campaignsStore.pauseCampaign(campaign.value.uuid)
    await fetchCampaign()
  } catch (e) {
    console.error('Failed to pause campaign:', e)
  }
}

const resumeCampaign = async () => {
  if (!campaign.value) return
  try {
    await campaignsStore.resumeCampaign(campaign.value.uuid)
    await fetchCampaign()
  } catch (e) {
    console.error('Failed to resume campaign:', e)
  }
}

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

// Setup SSE for real-time progress when campaign is sending
const setupSSE = () => {
  if (campaign.value?.status !== 'sending') return

  const token = localStorage.getItem('token')
  if (!token) return

  eventSource = new EventSource(`/api/v1/campaigns/${uuid.value}/progress?token=${token}`)

  eventSource.onmessage = (event) => {
    const data = JSON.parse(event.data)
    if (data.type === 'progress') {
      sseProgress.value = {
        sent: data.sent,
        total: data.total,
        percent: Math.round((data.sent / data.total) * 100)
      }
      if (stats.value) {
        stats.value.sent = data.sent
      }
    } else if (data.type === 'complete') {
      sseProgress.value = null
      fetchCampaign()
    }
  }

  eventSource.onerror = () => {
    eventSource?.close()
    eventSource = null
  }
}

const cleanupSSE = () => {
  if (eventSource) {
    eventSource.close()
    eventSource = null
  }
}

// Lifecycle
onMounted(() => {
  fetchCampaign()
})

onUnmounted(() => {
  cleanupSSE()
})

watch(() => campaign.value?.status, (newStatus) => {
  if (newStatus === 'sending') {
    setupSSE()
  } else {
    cleanupSSE()
    sseProgress.value = null
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
            >
              <ArrowLeft class="w-5 h-5 text-gray-600" />
            </button>
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-3">
                <h1 class="text-xl font-semibold text-gray-900 truncate">{{ campaign.name }}</h1>
                <span :class="['px-3 py-1 rounded-full text-xs font-medium capitalize flex items-center gap-1.5', statusConfig.bg, statusConfig.text]">
                  <component :is="statusConfig.icon" class="w-3.5 h-3.5" />
                  {{ statusConfig.label }}
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
              >
                <RefreshCw class="w-5 h-5" />
              </button>
              <Button
                v-if="campaign.status === 'sending'"
                variant="secondary"
                @click="pauseCampaign"
                class="text-amber-600 border-amber-300 hover:bg-amber-50"
              >
                <Pause class="w-4 h-4" />
                Pause
              </Button>
              <Button
                v-if="campaign.status === 'paused'"
                @click="resumeCampaign"
                class="bg-green-600 hover:bg-green-700"
              >
                <Play class="w-4 h-4" />
                Resume
              </Button>
            </div>
          </div>

          <!-- Progress bar for sending campaigns -->
          <div v-if="campaign.status === 'sending' && sseProgress" class="mt-4">
            <div class="flex items-center justify-between text-sm text-gray-600 mb-1.5">
              <span class="flex items-center gap-2">
                <Activity class="w-4 h-4 text-blue-500 animate-pulse" />
                Sending in progress...
              </span>
              <span class="font-medium">
                {{ sseProgress.sent.toLocaleString() }} / {{ sseProgress.total.toLocaleString() }}
                <span class="text-blue-600">({{ sseProgress.percent }}%)</span>
              </span>
            </div>
            <div class="w-full h-2 bg-gray-200 rounded-full overflow-hidden">
              <div
                class="h-full bg-gradient-to-r from-blue-500 to-blue-600 transition-all duration-500 ease-out"
                :style="{ width: `${sseProgress.percent}%` }"
              />
            </div>
          </div>
        </div>

        <!-- Scrollable Content -->
        <div class="flex-1 overflow-y-auto">
          <div class="px-6 py-6 space-y-6">

            <!-- Performance Summary Card -->
            <div class="bg-gradient-to-r from-indigo-600 to-purple-600 rounded-2xl p-6 text-white shadow-lg">
              <div class="flex items-center justify-between">
                <div>
                  <h2 class="text-lg font-medium text-indigo-100">Campaign Performance</h2>
                  <div class="flex items-baseline gap-3 mt-2">
                    <span class="text-4xl font-bold">{{ performanceScore.toFixed(0) }}</span>
                    <span class="text-indigo-200">/100</span>
                    <span :class="['px-2 py-0.5 rounded text-sm font-medium bg-white/20', performanceLabel.color.replace('text-', 'text-white')]">
                      {{ performanceLabel.label }}
                    </span>
                  </div>
                </div>
                <div class="grid grid-cols-3 gap-6 text-center">
                  <div>
                    <div class="text-3xl font-bold">{{ formatNumber(stats?.sent || 0) }}</div>
                    <div class="text-indigo-200 text-sm">Sent</div>
                  </div>
                  <div>
                    <div class="text-3xl font-bold">{{ openRate.toFixed(1) }}%</div>
                    <div class="text-indigo-200 text-sm">Open Rate</div>
                  </div>
                  <div>
                    <div class="text-3xl font-bold">{{ clickRate.toFixed(1) }}%</div>
                    <div class="text-indigo-200 text-sm">Click Rate</div>
                  </div>
                </div>
              </div>
            </div>

            <!-- Quick Stats Grid -->
            <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-4">
              <!-- Total Sent -->
              <div class="bg-white rounded-xl border border-gray-200 p-4 hover:shadow-md transition-shadow">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-blue-100 flex items-center justify-center flex-shrink-0">
                    <Send class="w-5 h-5 text-blue-600" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-xl font-bold text-gray-900 truncate">
                      {{ (stats?.sent || 0).toLocaleString() }}
                    </div>
                    <div class="text-xs text-gray-500">Sent</div>
                  </div>
                </div>
              </div>

              <!-- Delivered -->
              <div class="bg-white rounded-xl border border-gray-200 p-4 hover:shadow-md transition-shadow">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-emerald-100 flex items-center justify-center flex-shrink-0">
                    <CheckCircle class="w-5 h-5 text-emerald-600" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-xl font-bold text-gray-900 truncate">
                      {{ deliveryRate.toFixed(1) }}%
                    </div>
                    <div class="text-xs text-gray-500">Delivered</div>
                  </div>
                </div>
              </div>

              <!-- Opens -->
              <div class="bg-white rounded-xl border border-gray-200 p-4 hover:shadow-md transition-shadow">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-green-100 flex items-center justify-center flex-shrink-0">
                    <Eye class="w-5 h-5 text-green-600" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-xl font-bold text-gray-900 truncate">
                      {{ openRate.toFixed(1) }}%
                    </div>
                    <div class="text-xs text-gray-500">Open Rate</div>
                  </div>
                </div>
              </div>

              <!-- Clicks -->
              <div class="bg-white rounded-xl border border-gray-200 p-4 hover:shadow-md transition-shadow">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-purple-100 flex items-center justify-center flex-shrink-0">
                    <MousePointer class="w-5 h-5 text-purple-600" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-xl font-bold text-gray-900 truncate">
                      {{ clickRate.toFixed(1) }}%
                    </div>
                    <div class="text-xs text-gray-500">Click Rate</div>
                  </div>
                </div>
              </div>

              <!-- Bounced -->
              <div class="bg-white rounded-xl border border-gray-200 p-4 hover:shadow-md transition-shadow">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-red-100 flex items-center justify-center flex-shrink-0">
                    <AlertTriangle class="w-5 h-5 text-red-600" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-xl font-bold text-gray-900 truncate">
                      {{ bounceRate.toFixed(1) }}%
                    </div>
                    <div class="text-xs text-gray-500">Bounce Rate</div>
                  </div>
                </div>
              </div>

              <!-- Click-to-Open -->
              <div class="bg-white rounded-xl border border-gray-200 p-4 hover:shadow-md transition-shadow">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-pink-100 flex items-center justify-center flex-shrink-0">
                    <Target class="w-5 h-5 text-pink-600" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-xl font-bold text-gray-900 truncate">
                      {{ clickToOpenRate.toFixed(1) }}%
                    </div>
                    <div class="text-xs text-gray-500">Click-to-Open</div>
                  </div>
                </div>
              </div>
            </div>

            <!-- Charts Row 1 -->
            <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
              <!-- Engagement Funnel -->
              <div class="bg-white rounded-xl border border-gray-200 p-6 hover:shadow-md transition-shadow">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <TrendingUp class="w-5 h-5 text-blue-500" />
                  Engagement Funnel
                </h3>
                <div class="h-56">
                  <Bar :data="funnelChartData" :options="funnelChartOptions" />
                </div>
              </div>

              <!-- Performance Rates -->
              <div class="bg-white rounded-xl border border-gray-200 p-6 hover:shadow-md transition-shadow">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <BarChart3 class="w-5 h-5 text-purple-500" />
                  Performance Rates
                </h3>
                <div class="h-56">
                  <Bar :data="ratesChartData" :options="ratesChartOptions" />
                </div>
              </div>
            </div>

            <!-- Charts Row 2 -->
            <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
              <!-- Delivery Status Doughnut -->
              <div class="bg-white rounded-xl border border-gray-200 p-6 hover:shadow-md transition-shadow">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <Shield class="w-5 h-5 text-green-500" />
                  Delivery Status
                </h3>
                <div class="h-56 relative">
                  <Doughnut :data="deliveryDoughnutData" :options="deliveryDoughnutOptions" />
                  <div class="absolute inset-0 flex items-center justify-center pointer-events-none" style="margin-bottom: 40px;">
                    <div class="text-center">
                      <div class="text-2xl font-bold text-gray-900">{{ deliveryRate.toFixed(1) }}%</div>
                      <div class="text-xs text-gray-500">Delivered</div>
                    </div>
                  </div>
                </div>
              </div>

              <!-- Engagement Breakdown Doughnut -->
              <div class="bg-white rounded-xl border border-gray-200 p-6 hover:shadow-md transition-shadow">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <PieChart class="w-5 h-5 text-purple-500" />
                  Engagement Breakdown
                </h3>
                <div class="h-56 relative">
                  <Doughnut :data="engagementDoughnutData" :options="engagementDoughnutOptions" />
                  <div class="absolute inset-0 flex items-center justify-center pointer-events-none" style="margin-bottom: 40px;">
                    <div class="text-center">
                      <div class="text-2xl font-bold text-gray-900">{{ openRate.toFixed(1) }}%</div>
                      <div class="text-xs text-gray-500">Engaged</div>
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <!-- Detailed Stats & Campaign Info -->
            <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
              <!-- Detailed Numbers -->
              <div class="bg-white rounded-xl border border-gray-200 p-6 hover:shadow-md transition-shadow">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <Zap class="w-5 h-5 text-amber-500" />
                  Detailed Statistics
                </h3>
                <div class="space-y-3">
                  <div class="flex justify-between items-center py-2 border-b border-gray-100">
                    <span class="text-gray-600">Total Sent</span>
                    <span class="font-semibold text-gray-900">{{ (stats?.sent || 0).toLocaleString() }}</span>
                  </div>
                  <div class="flex justify-between items-center py-2 border-b border-gray-100">
                    <span class="text-gray-600">Opened</span>
                    <span class="font-semibold text-green-600">{{ (stats?.opened || 0).toLocaleString() }}</span>
                  </div>
                  <div class="flex justify-between items-center py-2 border-b border-gray-100">
                    <span class="text-gray-600">Clicked</span>
                    <span class="font-semibold text-purple-600">{{ (stats?.clicked || 0).toLocaleString() }}</span>
                  </div>
                  <div class="flex justify-between items-center py-2 border-b border-gray-100">
                    <span class="text-gray-600">Bounced</span>
                    <span class="font-semibold text-red-600">{{ (stats?.bounced || 0).toLocaleString() }}</span>
                  </div>
                  <div class="flex justify-between items-center py-2 border-b border-gray-100">
                    <span class="text-gray-600">Complaints</span>
                    <span class="font-semibold text-amber-600">{{ (stats?.complained || 0).toLocaleString() }}</span>
                  </div>
                  <div class="flex justify-between items-center py-2">
                    <span class="text-gray-600">Unsubscribed</span>
                    <span class="font-semibold text-orange-600">{{ (stats?.unsubscribed || 0).toLocaleString() }}</span>
                  </div>
                </div>
              </div>

              <!-- Campaign Info -->
              <div class="bg-white rounded-xl border border-gray-200 p-6 hover:shadow-md transition-shadow">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <Mail class="w-5 h-5 text-blue-500" />
                  Campaign Details
                </h3>
                <div class="space-y-3">
                  <div class="flex justify-between items-center py-2 border-b border-gray-100">
                    <span class="text-gray-600">Status</span>
                    <span :class="['px-2 py-0.5 rounded text-xs font-medium capitalize', statusConfig.bg, statusConfig.text]">
                      {{ campaign.status }}
                    </span>
                  </div>
                  <div class="flex justify-between items-center py-2 border-b border-gray-100">
                    <span class="text-gray-600">Recipients</span>
                    <span class="font-semibold text-gray-900">{{ (campaign.recipientCount || 0).toLocaleString() }}</span>
                  </div>
                  <div class="flex justify-between items-center py-2 border-b border-gray-100">
                    <span class="text-gray-600">List</span>
                    <span class="font-medium text-gray-900 truncate max-w-[120px]">{{ (campaign as any).listName || '-' }}</span>
                  </div>
                  <div class="py-2 border-b border-gray-100">
                    <div class="text-gray-600 mb-1">Created</div>
                    <div class="text-sm text-gray-900">{{ formatDate(campaign.createdAt) }}</div>
                  </div>
                  <div v-if="campaign.scheduledAt" class="py-2 border-b border-gray-100">
                    <div class="text-gray-600 mb-1">Scheduled For</div>
                    <div class="text-sm text-gray-900">{{ formatDate(campaign.scheduledAt) }}</div>
                  </div>
                  <div v-if="campaign.sentAt" class="py-2">
                    <div class="text-gray-600 mb-1">Sent At</div>
                    <div class="text-sm text-gray-900">{{ formatDate(campaign.sentAt) }}</div>
                  </div>
                </div>
              </div>

              <!-- Email Preview -->
              <div class="bg-white rounded-xl border border-gray-200 p-6 hover:shadow-md transition-shadow">
                <h3 class="text-base font-semibold text-gray-900 mb-4 flex items-center gap-2">
                  <Eye class="w-5 h-5 text-green-500" />
                  Email Preview
                </h3>
                <div class="border border-gray-200 rounded-lg overflow-hidden">
                  <div class="px-3 py-2 bg-gray-50 border-b border-gray-200">
                    <div class="text-xs text-gray-500">
                      <strong class="text-gray-700">Subject:</strong> {{ campaign.subject }}
                    </div>
                    <div class="text-xs text-gray-500 mt-1">
                      <strong class="text-gray-700">From:</strong> {{ (campaign as any).fromName || 'Unknown' }} &lt;{{ (campaign as any).fromEmail || '-' }}&gt;
                    </div>
                  </div>
                  <div
                    class="p-3 prose prose-sm max-w-none overflow-y-auto text-xs"
                    style="max-height: 180px;"
                    v-html="sanitizedCampaignHtml"
                  />
                </div>
              </div>
            </div>

          </div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>
