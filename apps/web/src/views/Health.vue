<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { RefreshCw, Activity, Cloud, Mail, Inbox, Shield, AlertTriangle } from 'lucide-vue-next'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useHealthStore } from '@/stores/health'
const health = useHealthStore()
onMounted(() => health.initializeHealth())
const sending = computed(() => health.healthSummary?.sendingMetrics)
const receiving = computed(() => health.healthSummary?.receivingMetrics)
const hasSendingData = computed(() => (sending.value?.totalSent ?? 0) > 0)
const summaryCards = computed(() => [
  { name: 'Accepted for sending', value: sending.value?.totalSent, detail: 'Last 30 days' },
  { name: 'Confirmed deliveries', value: sending.value?.totalDelivered, detail: 'From provider delivery events' },
  { name: 'Bounced', value: sending.value?.totalBounced, detail: 'From provider bounce events' },
  { name: 'Complaints', value: sending.value?.totalComplaints, detail: 'From provider complaint events' },
  { name: 'Failed sends', value: sending.value?.totalFailed, detail: 'Last 30 days' },
])
function number(value?: number | null) { return value == null ? 'Unavailable' : new Intl.NumberFormat().format(value) }
function rate(value?: number) { return !hasSendingData.value || value == null ? 'No sending data' : `${value.toFixed(2)}%` }
</script>
<template>
  <AppLayout>
    <section class="flex-1 min-w-0 overflow-y-auto p-4 sm:p-6">
      <header class="flex flex-wrap justify-between items-start gap-4 mb-6"><div><h1 class="text-2xl font-medium">Health & Operations</h1><p class="text-gray-500 text-sm mt-1">SES account status and recorded email activity.</p></div><button @click="health.initializeHealth" :disabled="health.isLoading" class="flex gap-2 items-center px-4 py-2 bg-gmail-blue rounded-full text-white disabled:opacity-50"><RefreshCw :class="['w-4 h-4', health.isLoading ? 'animate-spin' : '']" />Refresh</button></header>
      <p v-if="health.error" role="alert" class="bg-red-50 text-red-700 border border-red-100 rounded-lg p-4 mb-4">{{ health.error }}</p>
      <div v-if="health.isLoading && !health.healthSummary" role="status" class="space-y-4 animate-pulse"><div class="h-24 rounded-lg bg-gray-100" /><div class="h-48 rounded-lg bg-gray-100" /></div>
      <template v-else-if="health.healthSummary">
        <div class="p-5 border rounded-xl mb-6 bg-gray-50 flex gap-4 items-center"><Activity class="w-8 h-8 text-gray-500" /><div><h2 class="text-lg font-medium capitalize">{{ health.healthStatus === 'unknown' ? 'Not enough sending data to assess health' : `${health.healthStatus} health · ${health.healthScore}/100` }}</h2><p class="text-sm text-gray-500 mt-1">Provider acceptance and confirmed delivery are counted separately.</p></div></div>
        <div v-if="health.warnings.length" class="grid md:grid-cols-2 gap-3 mb-6"><div v-for="warning in health.warnings" :key="warning.type + warning.message" class="rounded-lg border bg-amber-50 border-amber-200 p-4"><p class="font-medium text-sm flex gap-2"><AlertTriangle class="w-4 h-4 shrink-0" />{{ warning.message }}</p><p v-if="warning.action" class="text-sm text-gray-600 mt-2">{{ warning.action }}</p></div></div>
        <h2 class="flex items-center gap-2 text-lg font-medium mb-3"><Mail class="w-5 h-5" />Sending · Last 30 days</h2>
        <div class="grid sm:grid-cols-2 xl:grid-cols-5 gap-3 mb-4"><div v-for="card in summaryCards" :key="card.name" class="border rounded-xl p-4"><p class="text-sm text-gray-500">{{ card.name }}</p><p class="text-3xl my-2">{{ number(card.value) }}</p><p class="text-xs text-gray-500">{{ card.detail }}</p></div></div>
        <div class="grid sm:grid-cols-3 gap-3 mb-6"><div class="border rounded-xl p-4"><p class="text-sm text-gray-500">Delivery rate</p><p class="text-lg mt-1">{{ rate(sending?.deliveryRate) }}</p></div><div class="border rounded-xl p-4"><p class="text-sm text-gray-500">Bounce rate</p><p class="text-lg mt-1">{{ rate(sending?.bounceRate) }}</p></div><div class="border rounded-xl p-4"><p class="text-sm text-gray-500">Complaint rate</p><p class="text-lg mt-1">{{ rate(sending?.complaintRate) }}</p></div></div>
        <h2 class="flex items-center gap-2 text-lg font-medium mb-3"><Cloud class="w-5 h-5" />AWS SES account</h2>
        <p v-if="!health.sesLimits" class="text-sm text-gray-500 border rounded-xl p-4 mb-6">SES account limits are unavailable. This does not confirm that sending is enabled or that the account is in production mode.</p>
        <div v-else class="grid sm:grid-cols-2 lg:grid-cols-4 gap-3 mb-6"><div class="border rounded-xl p-4"><p class="text-sm text-gray-500">Rolling 24-hour quota</p><p class="text-xl my-2">{{ number(health.sesLimits.sentLast24Hours) }} / {{ number(health.sesLimits.max24HourSend) }}</p><p class="text-xs text-gray-500">{{ number(health.sesLimits.remaining24Hour) }} remaining</p></div><div class="border rounded-xl p-4"><p class="text-sm text-gray-500">Maximum sending rate</p><p class="text-xl my-2">{{ number(health.sesLimits.maxSendRate) }}/second</p></div><div class="border rounded-xl p-4"><p class="text-sm text-gray-500">Sending</p><p class="text-xl my-2">{{ health.sesLimits.sendingEnabled ? 'Enabled' : 'Disabled' }}</p></div><div class="border rounded-xl p-4"><p class="text-sm text-gray-500">Account mode</p><p class="text-xl my-2">{{ health.sesLimits.sandboxMode ? 'Sandbox' : 'Production' }}</p></div></div>
        <h2 class="flex items-center gap-2 text-lg font-medium mb-3"><Inbox class="w-5 h-5" />Receiving</h2>
        <div class="grid sm:grid-cols-2 lg:grid-cols-4 gap-3 mb-6"><div class="border rounded-xl p-4"><p class="text-sm text-gray-500">Received · Last 30 days</p><p class="text-3xl mt-2">{{ number(receiving?.totalReceived) }}</p></div><div class="border rounded-xl p-4"><p class="text-sm text-gray-500">Read · Last 30 days</p><p class="text-3xl mt-2">{{ number(receiving?.totalRead) }}</p></div><div class="border rounded-xl p-4"><p class="text-sm text-gray-500">Spam flagged</p><p class="text-3xl mt-2">{{ number(receiving?.totalSpam) }}</p></div><div class="border rounded-xl p-4"><p class="text-sm text-gray-500">Virus flagged</p><p class="text-3xl mt-2">{{ number(receiving?.totalVirus) }}</p></div></div>
        <h2 class="flex items-center gap-2 text-lg font-medium mb-3"><Shield class="w-5 h-5" />Domain authentication</h2>
        <p v-if="!health.healthSummary.authStatus?.domains?.length" class="border rounded-xl p-4 text-sm text-gray-500">No domain authentication results yet.</p>
        <div v-else class="border rounded-xl divide-y"><div v-for="domain in health.healthSummary.authStatus.domains" :key="domain.domain" class="p-4 flex flex-wrap gap-3 items-center"><span class="font-medium flex-1 min-w-0 break-all">{{ domain.domain }}</span><span v-for="check in [{ name: 'SPF', verified: domain.spfVerified }, { name: 'DKIM', verified: domain.dkimVerified }, { name: 'DMARC', verified: domain.dmarcVerified }]" :key="check.name" :class="['text-xs px-2 py-1 rounded-full', check.verified ? 'bg-green-50 text-green-700' : 'bg-amber-50 text-amber-700']">{{ check.name }} · {{ check.verified ? 'Verified' : 'Unverified' }}</span></div></div>
      </template>
      <p v-else class="border rounded-xl p-5 text-gray-500">Health data is unavailable. Refresh to try again.</p>
    </section>
  </AppLayout>
</template>
