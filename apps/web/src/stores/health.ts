import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import {
  healthApi,
  type SESAccountLimits,
  type EmailHealthSummary,
  type ReputationMetrics,
  type HealthWarning
} from '@/lib/api'

interface BlacklistResult {
  ip: string
  provider: string
  listed: boolean
  checkedAt: string
}

interface WarmupStatus {
  ip: string
  schedule: string
  currentDay: number
  totalDays: number
  dailyLimit: number
  sentToday: number
  startedAt: string
}

interface Alert {
  id: string
  type: 'blacklist' | 'bounce' | 'complaint' | 'quota' | 'warmup'
  severity: 'low' | 'medium' | 'high' | 'critical'
  title: string
  message: string
  acknowledged: boolean
  createdAt: string
}

interface DeliveryLog {
  id: string
  emailId: string
  recipient: string
  status: 'queued' | 'sent' | 'delivered' | 'bounced' | 'failed'
  message?: string
  createdAt: string
}

export const useHealthStore = defineStore('health', () => {
  const blacklistResults = ref<BlacklistResult[]>([])
  const reputation = ref<ReputationMetrics | null>(null)
  const sesLimits = ref<SESAccountLimits | null>(null)
  const healthSummary = ref<EmailHealthSummary | null>(null)
  const warmupSchedules = ref<string[]>([])
  const warmupStatuses = ref<WarmupStatus[]>([])
  const alerts = ref<Alert[]>([])
  const deliveryLogs = ref<DeliveryLog[]>([])
  const quota = ref<{ used: number; limit: number; resetAt: string } | null>(null)
  const isLoading = ref(false)
  const error = ref<string | null>(null)

  // Computed properties
  const healthScore = computed(() => healthSummary.value?.healthScore || reputation.value?.score || 0)
  const healthStatus = computed(() => healthSummary.value?.healthStatus || 'unknown')
  const warnings = computed<HealthWarning[]>(() => healthSummary.value?.warnings || [])
  const criticalWarnings = computed(() => warnings.value.filter(w => w.severity === 'critical' || w.severity === 'high'))

  async function checkBlacklists(ipAddress?: string) {
    isLoading.value = true
    error.value = null
    try {
      const results = await healthApi.checkBlacklists(ipAddress)
      blacklistResults.value = ((results as BlacklistResult[]) ?? []).filter((r): r is BlacklistResult => r != null)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to check blacklists'
      blacklistResults.value = []
    } finally {
      isLoading.value = false
    }
  }

  async function fetchReputation(period?: string) {
    try {
      const result = await healthApi.getReputation(period)
      reputation.value = result ?? null
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch reputation'
      reputation.value = null
    }
  }

  async function fetchSESLimits() {
    try {
      const result = await healthApi.getSESLimits()
      sesLimits.value = result ?? null
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch SES limits'
      sesLimits.value = null
    }
  }

  async function fetchHealthSummary() {
    isLoading.value = true
    error.value = null
    try {
      const result = await healthApi.getSummary()
      healthSummary.value = result ?? null
      // Also populate reputation and sesLimits from summary
      if (result) {
        reputation.value = result.sendingMetrics
        sesLimits.value = result.sesLimits || null
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch health summary'
      healthSummary.value = null
    } finally {
      isLoading.value = false
    }
  }

  async function fetchWarmupSchedules() {
    try {
      const result = await healthApi.getWarmupSchedules()
      warmupSchedules.value = ((result as string[]) ?? []).filter((s): s is string => s != null)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch warmup schedules'
      warmupSchedules.value = []
    }
  }

  async function startWarmup(ip: string, schedule: string) {
    try {
      await healthApi.startWarmup(ip, schedule)
      const status = (await healthApi.getWarmupStatus(ip)) as WarmupStatus
      warmupStatuses.value.push(status)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to start warmup'
      throw e
    }
  }

  async function fetchWarmupStatus(ip: string) {
    try {
      const status = (await healthApi.getWarmupStatus(ip)) as WarmupStatus
      const index = warmupStatuses.value.findIndex(w => w.ip === ip)
      if (index !== -1) {
        warmupStatuses.value[index] = status
      } else {
        warmupStatuses.value.push(status)
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch warmup status'
    }
  }

  async function fetchQuota() {
    try {
      const result = await healthApi.getQuota()
      quota.value = (result as { used: number; limit: number; resetAt: string }) ?? null
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch quota'
      quota.value = null
    }
  }

  async function fetchAlerts(unacknowledgedOnly = false) {
    try {
      const result = await healthApi.getAlerts(unacknowledgedOnly)
      alerts.value = ((result as Alert[]) ?? []).filter((a): a is Alert => a != null)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch alerts'
      alerts.value = []
    }
  }

  async function acknowledgeAlert(id: string) {
    try {
      await healthApi.acknowledgeAlert(id)
      const index = alerts.value.findIndex(a => a.id === id)
      if (index !== -1) {
        alerts.value[index] = { ...alerts.value[index], acknowledged: true }
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to acknowledge alert'
      throw e
    }
  }

  async function fetchDeliveryLogs(page = 1, status?: string) {
    try {
      const result = await healthApi.getLogs(page, 50, status)
      const logs = (result as { logs: DeliveryLog[] })?.logs ?? []
      deliveryLogs.value = logs.filter((l): l is DeliveryLog => l != null)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch delivery logs'
      deliveryLogs.value = []
    }
  }

  // Initialize all health data
  async function initializeHealth() {
    await Promise.all([
      fetchHealthSummary(),
      fetchAlerts(),
    ])
  }

  return {
    // State
    blacklistResults,
    reputation,
    sesLimits,
    healthSummary,
    warmupSchedules,
    warmupStatuses,
    alerts,
    deliveryLogs,
    quota,
    isLoading,
    error,
    // Computed
    healthScore,
    healthStatus,
    warnings,
    criticalWarnings,
    // Actions
    checkBlacklists,
    fetchReputation,
    fetchSESLimits,
    fetchHealthSummary,
    fetchWarmupSchedules,
    startWarmup,
    fetchWarmupStatus,
    fetchQuota,
    fetchAlerts,
    acknowledgeAlert,
    fetchDeliveryLogs,
    initializeHealth
  }
})
