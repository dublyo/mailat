<script setup lang="ts">
import { onMounted, computed } from 'vue'
import {
  Activity, Shield, AlertTriangle, CheckCircle, XCircle,
  RefreshCw, TrendingUp, Server, Cloud, Mail, Inbox,
  AlertCircle, ShieldCheck, ShieldAlert, Send, ArrowDownCircle
} from 'lucide-vue-next'
import { useDomainsStore } from '@/stores/domains'
import AppLayout from '@/components/layout/AppLayout.vue'
import Button from '@/components/common/Button.vue'
import Badge from '@/components/common/Badge.vue'
import Spinner from '@/components/common/Spinner.vue'
import { useHealthStore } from '@/stores/health'

const healthStore = useHealthStore()
const domainsStore = useDomainsStore()

onMounted(() => {
  healthStore.initializeHealth()
  domainsStore.fetchDomains()
})

// Computed properties for SES status
const sesDomainsCount = computed(() =>
  domainsStore.domains.filter(d => d.emailProvider === 'ses').length
)

const verifiedSesDomainsCount = computed(() =>
  domainsStore.domains.filter(d => d.emailProvider === 'ses' && d.sesVerified).length
)

// Health status colors
const healthStatusColor = computed(() => {
  switch (healthStore.healthStatus) {
    case 'excellent': return 'text-green-600'
    case 'good': return 'text-green-500'
    case 'fair': return 'text-yellow-600'
    case 'poor': return 'text-orange-600'
    case 'critical': return 'text-red-600'
    default: return 'text-gmail-gray'
  }
})

const healthStatusBg = computed(() => {
  switch (healthStore.healthStatus) {
    case 'excellent': return 'bg-green-100'
    case 'good': return 'bg-green-50'
    case 'fair': return 'bg-yellow-50'
    case 'poor': return 'bg-orange-50'
    case 'critical': return 'bg-red-50'
    default: return 'bg-gray-50'
  }
})

const runBlacklistCheck = async () => {
  await healthStore.checkBlacklists()
}

const refreshAll = async () => {
  await healthStore.initializeHealth()
}

const getAlertSeverityVariant = (severity: string) => {
  switch (severity) {
    case 'critical': return 'error'
    case 'high': return 'error'
    case 'medium': return 'warning'
    case 'low': return 'info'
    default: return 'default'
  }
}

const getWarningSeverityColor = (severity: string) => {
  switch (severity) {
    case 'critical': return 'border-red-500 bg-red-50'
    case 'high': return 'border-orange-500 bg-orange-50'
    case 'medium': return 'border-yellow-500 bg-yellow-50'
    case 'low': return 'border-blue-500 bg-blue-50'
    default: return 'border-gray-300 bg-gray-50'
  }
}

const formatNumber = (num: number) => {
  if (num >= 1000000) return (num / 1000000).toFixed(1) + 'M'
  if (num >= 1000) return (num / 1000).toFixed(1) + 'K'
  return num.toString()
}
</script>

<template>
  <AppLayout>
    <div class="flex-1 flex flex-col p-6 overflow-y-auto">
      <!-- Header -->
      <div class="flex items-center justify-between mb-6">
        <div>
          <h1 class="text-2xl font-medium">Health & Operations</h1>
          <p class="text-gmail-gray">Monitor your email deliverability, reputation, and AWS SES limits</p>
        </div>
        <div class="flex gap-2">
          <Button variant="ghost" @click="runBlacklistCheck" :loading="healthStore.isLoading">
            <Shield class="w-4 h-4" />
            Blacklist Check
          </Button>
          <Button @click="refreshAll" :loading="healthStore.isLoading">
            <RefreshCw class="w-4 h-4" />
            Refresh All
          </Button>
        </div>
      </div>

      <!-- Loading -->
      <div v-if="healthStore.isLoading && !healthStore.healthSummary" class="flex-1 flex items-center justify-center">
        <Spinner size="lg" />
      </div>

      <template v-else>
        <!-- Overall Health Score Banner -->
        <div :class="['rounded-lg border p-4 mb-6', healthStatusBg]">
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-4">
              <div :class="['text-5xl font-bold', healthStatusColor]">
                {{ healthStore.healthScore }}
              </div>
              <div>
                <div :class="['text-lg font-medium capitalize', healthStatusColor]">
                  {{ healthStore.healthStatus }} Health
                </div>
                <div class="text-sm text-gmail-gray">
                  Overall email system health score
                </div>
              </div>
            </div>
            <div v-if="healthStore.criticalWarnings.length > 0" class="flex items-center gap-2 text-red-600">
              <AlertCircle class="w-5 h-5" />
              <span class="font-medium">{{ healthStore.criticalWarnings.length }} critical issue(s)</span>
            </div>
          </div>
        </div>

        <!-- Warnings Section -->
        <div v-if="healthStore.warnings.length > 0" class="mb-6">
          <h2 class="text-lg font-medium mb-3 flex items-center gap-2">
            <AlertTriangle class="w-5 h-5 text-yellow-600" />
            Health Warnings
          </h2>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div
              v-for="(warning, index) in healthStore.warnings"
              :key="index"
              :class="['rounded-lg border-l-4 p-4', getWarningSeverityColor(warning.severity)]"
            >
              <div class="flex items-start justify-between">
                <div>
                  <div class="flex items-center gap-2">
                    <Badge :variant="getAlertSeverityVariant(warning.severity)">
                      {{ warning.severity }}
                    </Badge>
                    <span class="font-medium text-sm uppercase text-gmail-gray">{{ warning.type }}</span>
                  </div>
                  <p class="mt-2 text-sm font-medium">{{ warning.message }}</p>
                  <p class="mt-1 text-sm text-gmail-gray">{{ warning.recommendation }}</p>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Stats cards - Sending Metrics -->
        <h2 class="text-lg font-medium mb-3 flex items-center gap-2">
          <Send class="w-5 h-5 text-gmail-gray" />
          Sending Metrics
        </h2>
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-5 gap-4 mb-6">
          <!-- Reputation Score -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <TrendingUp class="w-4 h-4" />
              <span class="text-sm">Reputation Score</span>
            </div>
            <div class="text-3xl font-medium">
              {{ healthStore.reputation?.score || 0 }}%
            </div>
            <div :class="[
              'text-sm',
              (healthStore.reputation?.score || 0) >= 80 ? 'text-green-600' :
              (healthStore.reputation?.score || 0) >= 60 ? 'text-yellow-600' : 'text-red-600'
            ]">
              {{ (healthStore.reputation?.score || 0) >= 80 ? 'Good standing' :
                 (healthStore.reputation?.score || 0) >= 60 ? 'Needs attention' : 'Critical' }}
            </div>
          </div>

          <!-- Delivery Rate -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <CheckCircle class="w-4 h-4" />
              <span class="text-sm">Delivery Rate</span>
            </div>
            <div class="text-3xl font-medium">
              {{ (healthStore.reputation?.deliveryRate || 0).toFixed(1) }}%
            </div>
            <div class="text-sm text-gmail-gray">
              {{ formatNumber(healthStore.reputation?.delivered || 0) }} delivered
            </div>
          </div>

          <!-- Bounce Rate -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <XCircle class="w-4 h-4" />
              <span class="text-sm">Bounce Rate</span>
            </div>
            <div class="text-3xl font-medium">
              {{ (healthStore.reputation?.bounceRate || 0).toFixed(2) }}%
            </div>
            <div :class="[
              'text-sm',
              (healthStore.reputation?.bounceRate || 0) > 5 ? 'text-red-600' :
              (healthStore.reputation?.bounceRate || 0) > 2 ? 'text-yellow-600' : 'text-green-600'
            ]">
              {{ (healthStore.reputation?.bounceRate || 0) > 5 ? 'Too high!' :
                 (healthStore.reputation?.bounceRate || 0) > 2 ? 'Monitor closely' : 'Healthy' }}
            </div>
          </div>

          <!-- Complaint Rate -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <AlertTriangle class="w-4 h-4" />
              <span class="text-sm">Complaint Rate</span>
            </div>
            <div class="text-3xl font-medium">
              {{ (healthStore.reputation?.complaintRate || 0).toFixed(3) }}%
            </div>
            <div :class="[
              'text-sm',
              (healthStore.reputation?.complaintRate || 0) > 0.1 ? 'text-red-600' :
              (healthStore.reputation?.complaintRate || 0) > 0.05 ? 'text-yellow-600' : 'text-green-600'
            ]">
              {{ (healthStore.reputation?.complaintRate || 0) > 0.1 ? 'Critical!' :
                 (healthStore.reputation?.complaintRate || 0) > 0.05 ? 'Watch closely' : 'Excellent' }}
            </div>
          </div>

          <!-- Total Sent -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <Mail class="w-4 h-4" />
              <span class="text-sm">Total Sent</span>
            </div>
            <div class="text-3xl font-medium">
              {{ formatNumber(healthStore.reputation?.totalSent || 0) }}
            </div>
            <div class="text-sm text-gmail-gray">
              {{ healthStore.reputation?.period || 'Last 30 days' }}
            </div>
          </div>
        </div>

        <!-- AWS SES Limits -->
        <h2 class="text-lg font-medium mb-3 flex items-center gap-2">
          <Cloud class="w-5 h-5 text-gmail-gray" />
          AWS SES Account Limits
        </h2>
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
          <!-- 24 Hour Send Quota -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <Server class="w-4 h-4" />
              <span class="text-sm">24 Hour Quota</span>
            </div>
            <div class="text-2xl font-medium">
              {{ formatNumber(healthStore.sesLimits?.sentLast24Hours || 0) }} / {{ formatNumber(healthStore.sesLimits?.max24HourSend || 0) }}
            </div>
            <div class="mt-2 h-2 bg-gmail-lightGray rounded-full overflow-hidden">
              <div
                :style="{ width: `${healthStore.sesLimits?.usagePercentage || 0}%` }"
                :class="[
                  'h-full rounded-full',
                  (healthStore.sesLimits?.usagePercentage || 0) > 90 ? 'bg-red-500' :
                  (healthStore.sesLimits?.usagePercentage || 0) > 75 ? 'bg-yellow-500' : 'bg-gmail-blue'
                ]"
              />
            </div>
            <div class="text-sm text-gmail-gray mt-1">
              {{ (healthStore.sesLimits?.usagePercentage || 0).toFixed(1) }}% used
            </div>
          </div>

          <!-- Max Send Rate -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <Activity class="w-4 h-4" />
              <span class="text-sm">Max Send Rate</span>
            </div>
            <div class="text-3xl font-medium">
              {{ healthStore.sesLimits?.maxSendRate || 0 }}
            </div>
            <div class="text-sm text-gmail-gray">emails/second</div>
          </div>

          <!-- Remaining Quota -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <TrendingUp class="w-4 h-4" />
              <span class="text-sm">Remaining Today</span>
            </div>
            <div class="text-3xl font-medium">
              {{ formatNumber(healthStore.sesLimits?.remaining24Hour || 0) }}
            </div>
            <div class="text-sm text-gmail-gray">emails available</div>
          </div>

          <!-- Account Status -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <Shield class="w-4 h-4" />
              <span class="text-sm">Account Status</span>
            </div>
            <div class="flex flex-col gap-1 mt-2">
              <div class="flex items-center gap-2">
                <component
                  :is="healthStore.sesLimits?.sendingEnabled ? CheckCircle : XCircle"
                  :class="healthStore.sesLimits?.sendingEnabled ? 'text-green-500' : 'text-red-500'"
                  class="w-4 h-4"
                />
                <span class="text-sm">{{ healthStore.sesLimits?.sendingEnabled ? 'Sending Enabled' : 'Sending Disabled' }}</span>
              </div>
              <div class="flex items-center gap-2">
                <component
                  :is="healthStore.sesLimits?.sandboxMode ? AlertTriangle : CheckCircle"
                  :class="healthStore.sesLimits?.sandboxMode ? 'text-yellow-500' : 'text-green-500'"
                  class="w-4 h-4"
                />
                <span class="text-sm">{{ healthStore.sesLimits?.sandboxMode ? 'Sandbox Mode' : 'Production Mode' }}</span>
              </div>
            </div>
          </div>
        </div>

        <!-- Receiving Metrics -->
        <h2 class="text-lg font-medium mb-3 flex items-center gap-2">
          <ArrowDownCircle class="w-5 h-5 text-gmail-gray" />
          Receiving Metrics
        </h2>
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-5 gap-4 mb-6">
          <!-- Total Received -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <Inbox class="w-4 h-4" />
              <span class="text-sm">Total Received</span>
            </div>
            <div class="text-3xl font-medium">
              {{ formatNumber(healthStore.healthSummary?.receivingMetrics?.totalReceived || 0) }}
            </div>
            <div class="text-sm text-gmail-gray">all time</div>
          </div>

          <!-- Received Today -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <Mail class="w-4 h-4" />
              <span class="text-sm">Received Today</span>
            </div>
            <div class="text-3xl font-medium">
              {{ formatNumber(healthStore.healthSummary?.receivingMetrics?.receivedToday || 0) }}
            </div>
            <div class="text-sm text-gmail-gray">
              Avg: {{ formatNumber(healthStore.healthSummary?.receivingMetrics?.avgDailyReceived || 0) }}/day
            </div>
          </div>

          <!-- Spam Blocked -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <ShieldCheck class="w-4 h-4" />
              <span class="text-sm">Spam Blocked</span>
            </div>
            <div class="text-3xl font-medium">
              {{ formatNumber(healthStore.healthSummary?.receivingMetrics?.spamBlocked || 0) }}
            </div>
            <div class="text-sm text-green-600">Protected</div>
          </div>

          <!-- Virus Blocked -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <ShieldAlert class="w-4 h-4" />
              <span class="text-sm">Virus Blocked</span>
            </div>
            <div class="text-3xl font-medium">
              {{ formatNumber(healthStore.healthSummary?.receivingMetrics?.virusBlocked || 0) }}
            </div>
            <div class="text-sm text-green-600">Protected</div>
          </div>

          <!-- AWS SES Domains -->
          <div class="bg-white rounded-lg border border-gmail-border p-4">
            <div class="flex items-center gap-2 text-gmail-gray mb-2">
              <Cloud class="w-4 h-4" />
              <span class="text-sm">SES Domains</span>
            </div>
            <div class="text-3xl font-medium">
              {{ verifiedSesDomainsCount }} / {{ sesDomainsCount }}
            </div>
            <div :class="[
              'text-sm',
              sesDomainsCount === 0 ? 'text-gmail-gray' : verifiedSesDomainsCount === sesDomainsCount ? 'text-green-600' : 'text-yellow-600'
            ]">
              {{ sesDomainsCount === 0 ? 'No SES domains' : verifiedSesDomainsCount === sesDomainsCount ? 'All verified' : 'Pending verification' }}
            </div>
          </div>
        </div>

        <!-- Domain Authentication Status -->
        <div v-if="healthStore.healthSummary?.authStatus?.domains?.length" class="bg-white rounded-lg border border-gmail-border mb-6">
          <div class="flex items-center justify-between p-4 border-b border-gmail-border">
            <div class="flex items-center gap-2">
              <Shield class="w-5 h-5 text-gmail-gray" />
              <h2 class="font-medium">Domain Authentication</h2>
            </div>
            <Badge :variant="healthStore.healthSummary.authStatus.allDomainsVerified ? 'success' : 'warning'">
              {{ healthStore.healthSummary.authStatus.allDomainsVerified ? 'All Verified' : 'Issues Found' }}
            </Badge>
          </div>
          <div class="divide-y divide-gmail-border">
            <div
              v-for="domain in healthStore.healthSummary.authStatus.domains"
              :key="domain.domain"
              class="flex items-center gap-4 p-4"
            >
              <div class="flex-1 font-medium">{{ domain.domain }}</div>
              <div class="flex items-center gap-4">
                <div class="flex items-center gap-1">
                  <component :is="domain.spfVerified ? CheckCircle : XCircle" :class="domain.spfVerified ? 'text-green-500' : 'text-red-500'" class="w-4 h-4" />
                  <span class="text-sm">SPF</span>
                </div>
                <div class="flex items-center gap-1">
                  <component :is="domain.dkimVerified ? CheckCircle : XCircle" :class="domain.dkimVerified ? 'text-green-500' : 'text-red-500'" class="w-4 h-4" />
                  <span class="text-sm">DKIM</span>
                </div>
                <div class="flex items-center gap-1">
                  <component :is="domain.dmarcVerified ? CheckCircle : XCircle" :class="domain.dmarcVerified ? 'text-green-500' : 'text-red-500'" class="w-4 h-4" />
                  <span class="text-sm">DMARC</span>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Alerts -->
        <div class="bg-white rounded-lg border border-gmail-border mb-6">
          <div class="flex items-center justify-between p-4 border-b border-gmail-border">
            <div class="flex items-center gap-2">
              <AlertTriangle class="w-5 h-5 text-gmail-gray" />
              <h2 class="font-medium">Active Alerts</h2>
            </div>
            <Badge>{{ healthStore.alerts.filter(a => !a.acknowledged).length }}</Badge>
          </div>

          <div v-if="healthStore.alerts.length === 0" class="p-8 text-center text-gmail-gray">
            <Shield class="w-12 h-12 mx-auto mb-2 opacity-50" />
            <p>No active alerts - everything looks good!</p>
          </div>

          <div v-else class="divide-y divide-gmail-border">
            <div
              v-for="alert in healthStore.alerts"
              :key="alert.id"
              :class="[
                'flex items-center gap-4 p-4',
                alert.acknowledged ? 'opacity-50' : ''
              ]"
            >
              <Badge :variant="getAlertSeverityVariant(alert.severity)">
                {{ alert.severity }}
              </Badge>
              <div class="flex-1">
                <div class="font-medium">{{ alert.title }}</div>
                <div class="text-sm text-gmail-gray">{{ alert.message }}</div>
              </div>
              <Button
                v-if="!alert.acknowledged"
                variant="ghost"
                size="sm"
                @click="healthStore.acknowledgeAlert(alert.id)"
              >
                Acknowledge
              </Button>
            </div>
          </div>
        </div>

        <!-- Blacklist Status -->
        <div class="bg-white rounded-lg border border-gmail-border">
          <div class="flex items-center justify-between p-4 border-b border-gmail-border">
            <div class="flex items-center gap-2">
              <Activity class="w-5 h-5 text-gmail-gray" />
              <h2 class="font-medium">Blacklist Status</h2>
            </div>
            <Button variant="ghost" size="sm" @click="runBlacklistCheck" :loading="healthStore.isLoading">
              <RefreshCw class="w-4 h-4" />
              Check Now
            </Button>
          </div>

          <div v-if="healthStore.blacklistResults.length === 0" class="p-8 text-center text-gmail-gray">
            <Shield class="w-12 h-12 mx-auto mb-2 opacity-50" />
            <p>Run a blacklist check to see your IP status across major RBL providers</p>
          </div>

          <div v-else class="divide-y divide-gmail-border">
            <div
              v-for="result in healthStore.blacklistResults"
              :key="`${result.ip}-${result.provider}`"
              class="flex items-center gap-4 p-4"
            >
              <component
                :is="result.listed ? XCircle : CheckCircle"
                :class="[
                  'w-5 h-5',
                  result.listed ? 'text-red-500' : 'text-green-500'
                ]"
              />
              <div class="flex-1">
                <div class="font-medium">{{ result.provider }}</div>
                <div class="text-sm text-gmail-gray">{{ result.ip }}</div>
              </div>
              <Badge :variant="result.listed ? 'error' : 'success'">
                {{ result.listed ? 'Listed' : 'Clean' }}
              </Badge>
            </div>
          </div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>
