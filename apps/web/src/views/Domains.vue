<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { Plus, Globe, CheckCircle, XCircle, RefreshCw, MoreVertical, Copy, Cloud, Server, Shield, X, Loader2, ChevronDown, ChevronUp, Mail, AlertCircle, Check, Zap, ExternalLink, Key, Inbox, Pencil, Trash2, Star } from 'lucide-vue-next'
import AppLayout from '@/components/layout/AppLayout.vue'
import Button from '@/components/common/Button.vue'
import Badge from '@/components/common/Badge.vue'
import Spinner from '@/components/common/Spinner.vue'
import { useDomainsStore } from '@/stores/domains'
import type { CloudflareZone, CloudflareDNSResult } from '@/lib/api'

const domainsStore = useDomainsStore()

// Modal state
const showAddDomainModal = ref(false)
const showAddIdentityModal = ref(false)
const showSetupWizard = ref(false)
const selectedDomainUuid = ref('')
const newDomain = ref('')
const newIdentity = ref({ displayName: '', email: '', password: '', isCatchAll: false })
const isSubmitting = ref(false)
const submitError = ref('')
const successMessage = ref('')
const expandedDomains = ref<Set<string>>(new Set())
const copiedText = ref('')

// Identity actions menu state
const openIdentityMenu = ref<string | null>(null)
const identityActionLoading = ref<string | null>(null)

// Setup wizard state
const setupStep = ref(1)
const cloudflareApiToken = ref('')
const cloudflareZones = ref<CloudflareZone[]>([])
const selectedZoneId = ref('')
const isLoadingZones = ref(false)
const isAddingDNS = ref(false)
const dnsResults = ref<CloudflareDNSResult[]>([])
const sesVerifying = ref(false)
const sesCheckingStatus = ref(false)
const receivingSetupLoading = ref<string | null>(null)
const receivingSetupResult = ref<{
  success: boolean
  requiredDns?: Array<{ recordType: string; hostname: string; value: string }>
} | null>(null)

onMounted(() => {
  domainsStore.fetchDomains()
  domainsStore.fetchIdentities()
})

const copyToClipboard = async (text: string) => {
  await navigator.clipboard.writeText(text)
  copiedText.value = text
  setTimeout(() => copiedText.value = '', 2000)
}

function toggleDomainExpand(uuid: string) {
  if (expandedDomains.value.has(uuid)) {
    expandedDomains.value.delete(uuid)
  } else {
    expandedDomains.value.add(uuid)
  }
  expandedDomains.value = new Set(expandedDomains.value)
}

function getVerificationCount(domain: any) {
  const checks = [domain.mxVerified, domain.spfVerified, domain.dkimVerified, domain.dmarcVerified]
  if (domain.emailProvider === 'ses') checks.push(domain.sesVerified)
  return {
    verified: checks.filter(Boolean).length,
    total: checks.length
  }
}

function getVerificationPercent(domain: any) {
  const { verified, total } = getVerificationCount(domain)
  return Math.round((verified / total) * 100)
}

async function handleAddDomain() {
  if (!newDomain.value.trim()) return

  isSubmitting.value = true
  submitError.value = ''

  try {
    await domainsStore.addDomain(newDomain.value.trim())
    newDomain.value = ''
    showAddDomainModal.value = false
  } catch (e: any) {
    submitError.value = e.message || 'Failed to add domain'
  } finally {
    isSubmitting.value = false
  }
}

async function handleVerifyDomain(uuid: string) {
  try {
    await domainsStore.verifyDomain(uuid)
  } catch (e) {
    console.error('Failed to verify domain:', e)
  }
}

function openAddIdentityModal(domainUuid: string) {
  selectedDomainUuid.value = domainUuid
  newIdentity.value = { displayName: '', email: '', password: '', isCatchAll: false }
  showAddIdentityModal.value = true
}

async function handleAddIdentity() {
  if (!newIdentity.value.displayName.trim() || !newIdentity.value.email.trim() || !newIdentity.value.password.trim()) return

  isSubmitting.value = true
  submitError.value = ''

  try {
    await domainsStore.createIdentity({
      displayName: newIdentity.value.displayName.trim(),
      email: newIdentity.value.email.trim(),
      domainId: selectedDomainUuid.value,
      password: newIdentity.value.password,
      isCatchAll: newIdentity.value.isCatchAll
    })
    showAddIdentityModal.value = false
  } catch (e: any) {
    submitError.value = e.message || 'Failed to add identity'
  } finally {
    isSubmitting.value = false
  }
}

function getDomainIdentities(domainUuid: string) {
  // Find the domain by UUID to get its ID
  const domain = domainsStore.domains.find(d => d.uuid === domainUuid)
  if (!domain) return []
  // Compare identity's domainId (integer) to domain's id (integer)
  return domainsStore.identities.filter(i => Number(i.domainId) === domain.id)
}

// Identity action functions
function toggleIdentityMenu(identityUuid: string) {
  openIdentityMenu.value = openIdentityMenu.value === identityUuid ? null : identityUuid
}

function closeIdentityMenu() {
  openIdentityMenu.value = null
}

async function setIdentityDefault(identityUuid: string) {
  identityActionLoading.value = identityUuid
  try {
    await domainsStore.updateIdentity(identityUuid, { isDefault: true })
    // Refresh to update UI
    await domainsStore.fetchIdentities()
  } catch (e: any) {
    console.error('Failed to set default:', e)
  } finally {
    identityActionLoading.value = null
    openIdentityMenu.value = null
  }
}

async function toggleIdentityCatchAll(identityUuid: string, currentValue: boolean) {
  identityActionLoading.value = identityUuid
  try {
    await domainsStore.setCatchAll(identityUuid, !currentValue)
  } catch (e: any) {
    console.error('Failed to toggle catch-all:', e)
  } finally {
    identityActionLoading.value = null
    openIdentityMenu.value = null
  }
}

async function handleDeleteIdentity(identityUuid: string) {
  if (!confirm('Are you sure you want to delete this identity? This action cannot be undone.')) return

  identityActionLoading.value = identityUuid
  try {
    await domainsStore.deleteIdentity(identityUuid)
  } catch (e: any) {
    console.error('Failed to delete identity:', e)
  } finally {
    identityActionLoading.value = null
    openIdentityMenu.value = null
  }
}

// Setup wizard functions
function openSetupWizard(domainUuid: string) {
  selectedDomainUuid.value = domainUuid
  setupStep.value = 1
  cloudflareApiToken.value = ''
  cloudflareZones.value = []
  selectedZoneId.value = ''
  dnsResults.value = []
  submitError.value = ''
  showSetupWizard.value = true
}

function closeSetupWizard() {
  showSetupWizard.value = false
  selectedDomainUuid.value = ''
}

const selectedDomain = computed(() => {
  return domainsStore.domains.find(d => d.uuid === selectedDomainUuid.value)
})

async function initiateSESVerification() {
  if (!selectedDomainUuid.value) return

  sesVerifying.value = true
  submitError.value = ''

  try {
    await domainsStore.initiateSESVerification(selectedDomainUuid.value)
    setupStep.value = 2
  } catch (e: any) {
    submitError.value = e.message || 'Failed to initiate SES verification'
  } finally {
    sesVerifying.value = false
  }
}

async function checkSESVerificationStatus() {
  if (!selectedDomainUuid.value) return

  sesCheckingStatus.value = true
  submitError.value = ''

  try {
    const status = await domainsStore.checkSESStatus(selectedDomainUuid.value)
    if (status.verified) {
      setupStep.value = 4 // All done!
    }
  } catch (e: any) {
    submitError.value = e.message || 'Failed to check SES status'
  } finally {
    sesCheckingStatus.value = false
  }
}

async function fetchCloudflareZones() {
  if (!cloudflareApiToken.value.trim()) {
    submitError.value = 'Please enter your Cloudflare API token'
    return
  }

  isLoadingZones.value = true
  submitError.value = ''

  try {
    cloudflareZones.value = await domainsStore.getCloudflareZones(cloudflareApiToken.value.trim())

    // Try to auto-select matching zone
    const domain = selectedDomain.value
    if (domain) {
      const matchingZone = cloudflareZones.value.find(
        z => z.name === domain.name || domain.name.endsWith('.' + z.name)
      )
      if (matchingZone) {
        selectedZoneId.value = matchingZone.id
      }
    }
  } catch (e: any) {
    submitError.value = e.message || 'Failed to fetch Cloudflare zones'
  } finally {
    isLoadingZones.value = false
  }
}

async function addDNSRecordsToCloudflare() {
  if (!selectedDomainUuid.value || !cloudflareApiToken.value.trim()) return

  isAddingDNS.value = true
  submitError.value = ''

  try {
    dnsResults.value = await domainsStore.addDNSToCloudflare(
      selectedDomainUuid.value,
      cloudflareApiToken.value.trim(),
      selectedZoneId.value || undefined
    )
    setupStep.value = 3
  } catch (e: any) {
    submitError.value = e.message || 'Failed to add DNS records to Cloudflare'
  } finally {
    isAddingDNS.value = false
  }
}

function skipCloudflare() {
  setupStep.value = 3
}

// Email Receiving Setup
async function handleSetupReceiving(domain: any) {
  if (!domain.id) {
    submitError.value = 'Domain ID not found'
    return
  }

  receivingSetupLoading.value = domain.uuid
  submitError.value = ''
  receivingSetupResult.value = null
  successMessage.value = ''

  try {
    const result = await domainsStore.setupReceiving(domain.id)
    receivingSetupResult.value = result
    const mxValue = result.requiredDns?.[0]?.value || '10 inbound-smtp.us-east-2.amazonaws.com'
    successMessage.value = `Email receiving setup complete! Add MX record: ${mxValue}`
    // Refresh domains to show updated receiving status
    await domainsStore.fetchDomains()
  } catch (e: any) {
    submitError.value = e.message || 'Failed to setup email receiving'
  } finally {
    receivingSetupLoading.value = null
  }
}
</script>

<template>
  <AppLayout>
    <div class="flex-1 flex flex-col h-full overflow-hidden bg-gradient-to-br from-gray-50 to-gray-100">
      <!-- Header -->
      <div class="flex-shrink-0 px-6 py-5 bg-white border-b border-gray-200 shadow-sm">
        <div class="flex items-center justify-between max-w-7xl mx-auto">
          <div>
            <h1 class="text-2xl font-semibold text-gray-900">Domains</h1>
            <p class="text-sm text-gray-500 mt-1">Manage your email domains and sender identities</p>
          </div>
          <Button @click="showAddDomainModal = true" class="shadow-md hover:shadow-lg transition-shadow">
            <Plus class="w-4 h-4" />
            Add Domain
          </Button>
        </div>
      </div>

      <!-- Success Message Banner -->
      <div v-if="successMessage" class="px-6 py-3 bg-green-50 border-b border-green-200">
        <div class="max-w-7xl mx-auto flex items-center justify-between">
          <div class="flex items-center gap-2 text-green-700">
            <CheckCircle class="w-5 h-5" />
            <span>{{ successMessage }}</span>
          </div>
          <button @click="successMessage = ''" class="text-green-600 hover:text-green-800">
            <X class="w-4 h-4" />
          </button>
        </div>
      </div>

      <!-- Main Content - Scrollable -->
      <div class="flex-1 overflow-y-auto">
        <div class="max-w-7xl mx-auto px-6 py-6">
          <!-- Loading -->
          <div v-if="domainsStore.isLoading" class="flex items-center justify-center py-20">
            <div class="text-center">
              <Spinner size="lg" class="mb-4" />
              <p class="text-gray-500">Loading domains...</p>
            </div>
          </div>

          <!-- Empty state -->
          <div
            v-else-if="domainsStore.domains.length === 0"
            class="flex flex-col items-center justify-center py-20 bg-white rounded-2xl border-2 border-dashed border-gray-200"
          >
            <div class="w-20 h-20 rounded-full bg-blue-50 flex items-center justify-center mb-6">
              <Globe class="w-10 h-10 text-blue-500" />
            </div>
            <h3 class="text-xl font-semibold text-gray-900 mb-2">No domains configured</h3>
            <p class="text-gray-500 mb-6 text-center max-w-sm">
              Add your first domain to start sending emails with your own brand identity
            </p>
            <Button @click="showAddDomainModal = true" size="lg">
              <Plus class="w-5 h-5" />
              Add your first domain
            </Button>
          </div>

          <!-- Domain list -->
          <div v-else class="space-y-4">
            <!-- Stats Summary -->
            <div class="grid grid-cols-1 md:grid-cols-3 gap-4 mb-6">
              <div class="bg-white rounded-xl p-4 border border-gray-200 shadow-sm">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-blue-100 flex items-center justify-center">
                    <Globe class="w-5 h-5 text-blue-600" />
                  </div>
                  <div>
                    <p class="text-2xl font-bold text-gray-900">{{ domainsStore.domains.length }}</p>
                    <p class="text-sm text-gray-500">Total Domains</p>
                  </div>
                </div>
              </div>
              <div class="bg-white rounded-xl p-4 border border-gray-200 shadow-sm">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-green-100 flex items-center justify-center">
                    <CheckCircle class="w-5 h-5 text-green-600" />
                  </div>
                  <div>
                    <p class="text-2xl font-bold text-gray-900">{{ domainsStore.domains.filter(d => d.status === 'active').length }}</p>
                    <p class="text-sm text-gray-500">Verified</p>
                  </div>
                </div>
              </div>
              <div class="bg-white rounded-xl p-4 border border-gray-200 shadow-sm">
                <div class="flex items-center gap-3">
                  <div class="w-10 h-10 rounded-lg bg-purple-100 flex items-center justify-center">
                    <Mail class="w-5 h-5 text-purple-600" />
                  </div>
                  <div>
                    <p class="text-2xl font-bold text-gray-900">{{ domainsStore.identities.length }}</p>
                    <p class="text-sm text-gray-500">Identities</p>
                  </div>
                </div>
              </div>
            </div>

            <!-- Domain Cards -->
            <div
              v-for="domain in domainsStore.domains"
              :key="domain.uuid"
              class="bg-white rounded-xl border border-gray-200 shadow-sm overflow-hidden hover:shadow-md transition-shadow"
            >
              <!-- Domain header -->
              <div class="p-5">
                <div class="flex items-start justify-between">
                  <div class="flex items-center gap-4">
                    <div class="w-12 h-12 rounded-xl flex items-center justify-center"
                         :class="domain.status === 'active' ? 'bg-green-100' : 'bg-amber-100'">
                      <Globe class="w-6 h-6" :class="domain.status === 'active' ? 'text-green-600' : 'text-amber-600'" />
                    </div>
                    <div>
                      <div class="flex items-center gap-3">
                        <h3 class="text-lg font-semibold text-gray-900">{{ domain.name || domain.domain }}</h3>
                        <Badge :variant="domain.status === 'active' ? 'success' : domain.status === 'pending' ? 'warning' : 'error'" class="capitalize">
                          {{ domain.status }}
                        </Badge>
                      </div>
                      <div class="flex items-center gap-3 mt-1">
                        <span class="inline-flex items-center text-xs text-gray-500">
                          <component :is="domain.emailProvider === 'ses' ? Cloud : Server" class="w-3 h-3 mr-1" />
                          {{ domain.emailProvider === 'ses' ? 'AWS SES' : 'SMTP' }}
                        </span>
                        <span class="text-gray-300">|</span>
                        <span class="text-xs text-gray-500">
                          {{ getDomainIdentities(domain.uuid).length }} {{ getDomainIdentities(domain.uuid).length === 1 ? 'identity' : 'identities' }}
                        </span>
                      </div>
                    </div>
                  </div>
                  <div class="flex items-center gap-2">
                    <Button v-if="domain.status !== 'active'" variant="primary" size="sm" @click="openSetupWizard(domain.uuid)">
                      <Zap class="w-4 h-4" />
                      Setup
                    </Button>
                    <Button v-if="domain.status !== 'active'" variant="secondary" size="sm" @click="handleVerifyDomain(domain.uuid)">
                      <RefreshCw class="w-4 h-4" />
                      Verify
                    </Button>
                    <Button
                      v-if="domain.status === 'active' && !domain.receivingEnabled"
                      variant="secondary"
                      size="sm"
                      @click="handleSetupReceiving(domain)"
                      :disabled="receivingSetupLoading === domain.uuid"
                    >
                      <Loader2 v-if="receivingSetupLoading === domain.uuid" class="w-4 h-4 animate-spin" />
                      <Inbox v-else class="w-4 h-4" />
                      Setup Receiving
                    </Button>
                    <Badge v-if="domain.receivingEnabled" variant="success" size="sm">
                      <Inbox class="w-3 h-3 mr-1" />
                      Receiving
                    </Badge>
                    <button
                      @click="toggleDomainExpand(domain.uuid)"
                      class="p-2 hover:bg-gray-100 rounded-lg transition-colors"
                    >
                      <component :is="expandedDomains.has(domain.uuid) ? ChevronUp : ChevronDown" class="w-5 h-5 text-gray-400" />
                    </button>
                  </div>
                </div>

                <!-- Verification Progress -->
                <div class="mt-4">
                  <div class="flex items-center justify-between text-sm mb-2">
                    <span class="text-gray-600 font-medium">Verification Progress</span>
                    <span class="text-gray-500">{{ getVerificationCount(domain).verified }}/{{ getVerificationCount(domain).total }} verified</span>
                  </div>
                  <div class="h-2 bg-gray-100 rounded-full overflow-hidden">
                    <div
                      class="h-full rounded-full transition-all duration-500"
                      :class="getVerificationPercent(domain) === 100 ? 'bg-green-500' : 'bg-blue-500'"
                      :style="{ width: getVerificationPercent(domain) + '%' }"
                    ></div>
                  </div>
                </div>

                <!-- Quick Verification Status -->
                <div class="flex flex-wrap gap-2 mt-4">
                  <div v-if="domain.emailProvider === 'ses'"
                       class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium"
                       :class="domain.sesVerified ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-700'">
                    <component :is="domain.sesVerified ? CheckCircle : XCircle" class="w-3.5 h-3.5" />
                    SES
                  </div>
                  <div class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium"
                       :class="domain.mxVerified ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-700'">
                    <component :is="domain.mxVerified ? CheckCircle : XCircle" class="w-3.5 h-3.5" />
                    MX
                  </div>
                  <div class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium"
                       :class="domain.spfVerified ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-700'">
                    <component :is="domain.spfVerified ? CheckCircle : XCircle" class="w-3.5 h-3.5" />
                    SPF
                  </div>
                  <div class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium"
                       :class="domain.dkimVerified ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-700'">
                    <component :is="domain.dkimVerified ? CheckCircle : XCircle" class="w-3.5 h-3.5" />
                    DKIM
                  </div>
                  <div class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium"
                       :class="domain.dmarcVerified ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-700'">
                    <component :is="domain.dmarcVerified ? CheckCircle : XCircle" class="w-3.5 h-3.5" />
                    DMARC
                  </div>
                </div>
              </div>

              <!-- Expanded Content -->
              <div v-if="expandedDomains.has(domain.uuid)" class="border-t border-gray-100">
                <!-- DNS Records -->
                <div class="p-5 bg-gray-50">
                  <h4 class="text-sm font-semibold text-gray-700 mb-3 flex items-center gap-2">
                    <Shield class="w-4 h-4" />
                    DNS Records
                  </h4>
                  <div v-if="domain.dnsRecords && domain.dnsRecords.length > 0" class="space-y-2">
                    <div
                      v-for="record in domain.dnsRecords"
                      :key="record.hostname || record.name"
                      class="bg-white rounded-lg border border-gray-200 p-3"
                    >
                      <div class="flex items-start justify-between gap-4">
                        <div class="flex-1 min-w-0">
                          <div class="flex items-center gap-2 mb-1">
                            <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-mono font-semibold bg-gray-100 text-gray-700">
                              {{ record.recordType || record.type }}
                            </span>
                            <component
                              :is="record.verified ? CheckCircle : XCircle"
                              :class="['w-4 h-4', record.verified ? 'text-green-500' : 'text-amber-500']"
                            />
                          </div>
                          <p class="text-xs text-gray-500 font-mono truncate">{{ record.hostname || record.name }}</p>
                          <p class="text-sm text-gray-700 font-mono mt-1 break-all">{{ record.value }}</p>
                        </div>
                        <button
                          @click="copyToClipboard(record.value)"
                          class="flex-shrink-0 p-2 hover:bg-gray-100 rounded-lg transition-colors group"
                          title="Copy value"
                        >
                          <Check v-if="copiedText === record.value" class="w-4 h-4 text-green-500" />
                          <Copy v-else class="w-4 h-4 text-gray-400 group-hover:text-gray-600" />
                        </button>
                      </div>
                    </div>
                  </div>
                  <div v-else class="text-sm text-gray-500 italic bg-white rounded-lg border border-dashed border-gray-200 p-4 text-center">
                    <AlertCircle class="w-5 h-5 mx-auto mb-2 text-gray-400" />
                    No DNS records configured yet. Click "Setup" to configure your domain.
                  </div>
                </div>

                <!-- Identities -->
                <div class="p-5 border-t border-gray-100">
                  <div class="flex items-center justify-between mb-3">
                    <h4 class="text-sm font-semibold text-gray-700 flex items-center gap-2">
                      <Mail class="w-4 h-4" />
                      Sender Identities
                    </h4>
                    <Button variant="secondary" size="sm" @click="openAddIdentityModal(domain.uuid)">
                      <Plus class="w-4 h-4" />
                      Add Identity
                    </Button>
                  </div>
                  <div v-if="getDomainIdentities(domain.uuid).length > 0" class="space-y-2">
                    <div
                      v-for="identity in getDomainIdentities(domain.uuid)"
                      :key="identity.uuid"
                      class="flex items-center justify-between p-3 bg-gray-50 rounded-lg hover:bg-gray-100 transition-colors"
                    >
                      <div class="flex items-center gap-3">
                        <div
                          class="w-9 h-9 rounded-full flex items-center justify-center text-white font-medium text-sm"
                          :style="{ backgroundColor: identity.color || '#3B82F6' }"
                        >
                          {{ identity.displayName.charAt(0).toUpperCase() }}
                        </div>
                        <div>
                          <div class="flex items-center gap-2">
                            <span class="font-medium text-gray-900">{{ identity.displayName }}</span>
                            <Badge v-if="identity.isDefault" variant="info" size="sm">Default</Badge>
                            <Badge v-if="identity.isCatchAll" variant="warning" size="sm">Catch-All</Badge>
                          </div>
                          <span class="text-sm text-gray-500">{{ identity.email }}</span>
                        </div>
                      </div>
                      <!-- Identity Actions Menu -->
                      <div class="relative">
                        <button
                          @click.stop="toggleIdentityMenu(identity.uuid)"
                          class="p-2 hover:bg-gray-200 rounded-lg transition-colors"
                          :disabled="identityActionLoading === identity.uuid"
                        >
                          <Loader2 v-if="identityActionLoading === identity.uuid" class="w-4 h-4 text-gray-400 animate-spin" />
                          <MoreVertical v-else class="w-4 h-4 text-gray-400" />
                        </button>
                        <!-- Dropdown Menu -->
                        <Transition
                          enter-active-class="transition-all duration-150"
                          leave-active-class="transition-all duration-100"
                          enter-from-class="opacity-0 scale-95"
                          leave-to-class="opacity-0 scale-95"
                        >
                          <div
                            v-if="openIdentityMenu === identity.uuid"
                            class="absolute right-0 top-full mt-1 w-48 bg-white border border-gray-200 rounded-lg shadow-lg z-30 py-1"
                          >
                            <button
                              v-if="!identity.isDefault"
                              @click="setIdentityDefault(identity.uuid)"
                              class="w-full px-3 py-2 text-left text-sm flex items-center gap-2 hover:bg-gray-50"
                            >
                              <Star class="w-4 h-4 text-gray-400" />
                              Set as Default
                            </button>
                            <button
                              @click="toggleIdentityCatchAll(identity.uuid, identity.isCatchAll || false)"
                              class="w-full px-3 py-2 text-left text-sm flex items-center gap-2 hover:bg-gray-50"
                            >
                              <Inbox class="w-4 h-4 text-gray-400" />
                              {{ identity.isCatchAll ? 'Disable Catch-All' : 'Enable Catch-All' }}
                            </button>
                            <div class="border-t border-gray-100 my-1"></div>
                            <button
                              @click="handleDeleteIdentity(identity.uuid)"
                              class="w-full px-3 py-2 text-left text-sm flex items-center gap-2 hover:bg-red-50 text-red-600"
                            >
                              <Trash2 class="w-4 h-4" />
                              Delete Identity
                            </button>
                          </div>
                        </Transition>
                        <!-- Click outside to close -->
                        <div
                          v-if="openIdentityMenu === identity.uuid"
                          class="fixed inset-0 z-20"
                          @click="closeIdentityMenu"
                        ></div>
                      </div>
                    </div>
                  </div>
                  <div v-else class="text-sm text-gray-500 italic bg-gray-50 rounded-lg border border-dashed border-gray-200 p-4 text-center">
                    <Mail class="w-5 h-5 mx-auto mb-2 text-gray-400" />
                    No identities configured. Add one to start sending emails.
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Add Domain Modal -->
    <Teleport to="body">
      <Transition
        enter-active-class="transition-opacity duration-200"
        leave-active-class="transition-opacity duration-200"
        enter-from-class="opacity-0"
        leave-to-class="opacity-0"
      >
        <div v-if="showAddDomainModal" class="fixed inset-0 z-50 flex items-center justify-center p-4">
          <div class="absolute inset-0 bg-black/60 backdrop-blur-sm" @click="showAddDomainModal = false"></div>
          <Transition
            enter-active-class="transition-all duration-200"
            leave-active-class="transition-all duration-200"
            enter-from-class="opacity-0 scale-95"
            leave-to-class="opacity-0 scale-95"
          >
            <div v-if="showAddDomainModal" class="relative bg-white rounded-2xl shadow-2xl w-full max-w-md">
              <div class="p-6">
                <div class="flex items-center justify-between mb-6">
                  <div>
                    <h2 class="text-xl font-semibold text-gray-900">Add Domain</h2>
                    <p class="text-sm text-gray-500 mt-1">Enter your domain name to get started</p>
                  </div>
                  <button @click="showAddDomainModal = false" class="p-2 hover:bg-gray-100 rounded-lg transition-colors">
                    <X class="w-5 h-5 text-gray-400" />
                  </button>
                </div>
                <form @submit.prevent="handleAddDomain" class="space-y-4">
                  <div>
                    <label class="block text-sm font-medium text-gray-700 mb-2">Domain Name</label>
                    <div class="relative">
                      <Globe class="absolute left-3 top-1/2 -translate-y-1/2 w-5 h-5 text-gray-400" />
                      <input
                        v-model="newDomain"
                        type="text"
                        placeholder="example.com"
                        class="w-full pl-10 pr-4 py-3 border border-gray-200 rounded-xl focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition-shadow"
                        :disabled="isSubmitting"
                      />
                    </div>
                    <p class="text-xs text-gray-500 mt-2">You'll need to verify ownership by adding DNS records</p>
                  </div>
                  <div v-if="submitError" class="flex items-center gap-2 p-3 bg-red-50 text-red-700 rounded-lg text-sm">
                    <AlertCircle class="w-4 h-4 flex-shrink-0" />
                    {{ submitError }}
                  </div>
                  <div class="flex gap-3 pt-2">
                    <Button variant="secondary" type="button" @click="showAddDomainModal = false" :disabled="isSubmitting" class="flex-1">
                      Cancel
                    </Button>
                    <Button type="submit" :disabled="!newDomain.trim() || isSubmitting" class="flex-1">
                      <Loader2 v-if="isSubmitting" class="w-4 h-4 animate-spin" />
                      <Plus v-else class="w-4 h-4" />
                      Add Domain
                    </Button>
                  </div>
                </form>
              </div>
            </div>
          </Transition>
        </div>
      </Transition>
    </Teleport>

    <!-- Add Identity Modal -->
    <Teleport to="body">
      <Transition
        enter-active-class="transition-opacity duration-200"
        leave-active-class="transition-opacity duration-200"
        enter-from-class="opacity-0"
        leave-to-class="opacity-0"
      >
        <div v-if="showAddIdentityModal" class="fixed inset-0 z-50 flex items-center justify-center p-4">
          <div class="absolute inset-0 bg-black/60 backdrop-blur-sm" @click="showAddIdentityModal = false"></div>
          <Transition
            enter-active-class="transition-all duration-200"
            leave-active-class="transition-all duration-200"
            enter-from-class="opacity-0 scale-95"
            leave-to-class="opacity-0 scale-95"
          >
            <div v-if="showAddIdentityModal" class="relative bg-white rounded-2xl shadow-2xl w-full max-w-md">
              <div class="p-6">
                <div class="flex items-center justify-between mb-6">
                  <div>
                    <h2 class="text-xl font-semibold text-gray-900">Add Identity</h2>
                    <p class="text-sm text-gray-500 mt-1">Create a new sender identity for this domain</p>
                  </div>
                  <button @click="showAddIdentityModal = false" class="p-2 hover:bg-gray-100 rounded-lg transition-colors">
                    <X class="w-5 h-5 text-gray-400" />
                  </button>
                </div>
                <form @submit.prevent="handleAddIdentity" class="space-y-4">
                  <div>
                    <label class="block text-sm font-medium text-gray-700 mb-2">Display Name</label>
                    <input
                      v-model="newIdentity.displayName"
                      type="text"
                      placeholder="John Doe"
                      class="w-full px-4 py-3 border border-gray-200 rounded-xl focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition-shadow"
                      :disabled="isSubmitting"
                    />
                  </div>
                  <div>
                    <label class="block text-sm font-medium text-gray-700 mb-2">Email Address</label>
                    <div class="relative">
                      <Mail class="absolute left-3 top-1/2 -translate-y-1/2 w-5 h-5 text-gray-400" />
                      <input
                        v-model="newIdentity.email"
                        type="email"
                        placeholder="john@example.com"
                        class="w-full pl-10 pr-4 py-3 border border-gray-200 rounded-xl focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition-shadow"
                        :disabled="isSubmitting"
                      />
                    </div>
                  </div>
                  <div>
                    <label class="block text-sm font-medium text-gray-700 mb-2">Password</label>
                    <input
                      v-model="newIdentity.password"
                      type="password"
                      placeholder="Min. 8 characters"
                      class="w-full px-4 py-3 border border-gray-200 rounded-xl focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition-shadow"
                      :disabled="isSubmitting"
                    />
                    <p class="text-xs text-gray-500 mt-1">Password for IMAP/SMTP access to this mailbox</p>
                  </div>
                  <div class="flex items-center justify-between p-4 bg-gray-50 rounded-xl">
                    <div>
                      <label class="block text-sm font-medium text-gray-700">Catch-All Address</label>
                      <p class="text-xs text-gray-500 mt-0.5">Receive all emails sent to any address @this-domain</p>
                    </div>
                    <label class="relative inline-flex items-center cursor-pointer">
                      <input
                        v-model="newIdentity.isCatchAll"
                        type="checkbox"
                        class="sr-only peer"
                        :disabled="isSubmitting"
                      />
                      <div class="w-11 h-6 bg-gray-200 peer-focus:outline-none peer-focus:ring-4 peer-focus:ring-blue-300 rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-blue-600"></div>
                    </label>
                  </div>
                  <div v-if="submitError" class="flex items-center gap-2 p-3 bg-red-50 text-red-700 rounded-lg text-sm">
                    <AlertCircle class="w-4 h-4 flex-shrink-0" />
                    {{ submitError }}
                  </div>
                  <div class="flex gap-3 pt-2">
                    <Button variant="secondary" type="button" @click="showAddIdentityModal = false" :disabled="isSubmitting" class="flex-1">
                      Cancel
                    </Button>
                    <Button type="submit" :disabled="!newIdentity.displayName.trim() || !newIdentity.email.trim() || newIdentity.password.length < 8 || isSubmitting" class="flex-1">
                      <Loader2 v-if="isSubmitting" class="w-4 h-4 animate-spin" />
                      <Plus v-else class="w-4 h-4" />
                      Add Identity
                    </Button>
                  </div>
                </form>
              </div>
            </div>
          </Transition>
        </div>
      </Transition>
    </Teleport>

    <!-- Setup Wizard Modal -->
    <Teleport to="body">
      <Transition
        enter-active-class="transition-opacity duration-200"
        leave-active-class="transition-opacity duration-200"
        enter-from-class="opacity-0"
        leave-to-class="opacity-0"
      >
        <div v-if="showSetupWizard" class="fixed inset-0 z-50 flex items-center justify-center p-4">
          <div class="absolute inset-0 bg-black/60 backdrop-blur-sm" @click="closeSetupWizard"></div>
          <Transition
            enter-active-class="transition-all duration-200"
            leave-active-class="transition-all duration-200"
            enter-from-class="opacity-0 scale-95"
            leave-to-class="opacity-0 scale-95"
          >
            <div v-if="showSetupWizard" class="relative bg-white rounded-2xl shadow-2xl w-full max-w-2xl max-h-[90vh] overflow-hidden flex flex-col">
              <!-- Header -->
              <div class="p-6 border-b border-gray-100">
                <div class="flex items-center justify-between">
                  <div>
                    <h2 class="text-xl font-semibold text-gray-900">Domain Setup Wizard</h2>
                    <p class="text-sm text-gray-500 mt-1">{{ selectedDomain?.name }}</p>
                  </div>
                  <button @click="closeSetupWizard" class="p-2 hover:bg-gray-100 rounded-lg transition-colors">
                    <X class="w-5 h-5 text-gray-400" />
                  </button>
                </div>

                <!-- Progress Steps -->
                <div class="flex items-center gap-2 mt-6">
                  <div v-for="step in 4" :key="step" class="flex items-center">
                    <div
                      class="w-8 h-8 rounded-full flex items-center justify-center text-sm font-medium transition-colors"
                      :class="step <= setupStep ? 'bg-blue-500 text-white' : 'bg-gray-100 text-gray-400'"
                    >
                      <Check v-if="step < setupStep" class="w-4 h-4" />
                      <span v-else>{{ step }}</span>
                    </div>
                    <div v-if="step < 4" class="w-16 h-1 mx-1 rounded" :class="step < setupStep ? 'bg-blue-500' : 'bg-gray-100'"></div>
                  </div>
                </div>
              </div>

              <!-- Content -->
              <div class="flex-1 overflow-y-auto p-6">
                <!-- Step 1: Initiate SES Verification -->
                <div v-if="setupStep === 1">
                  <div class="text-center mb-6">
                    <div class="w-16 h-16 rounded-full bg-orange-100 flex items-center justify-center mx-auto mb-4">
                      <Cloud class="w-8 h-8 text-orange-500" />
                    </div>
                    <h3 class="text-lg font-semibold text-gray-900">Step 1: Register with AWS SES</h3>
                    <p class="text-gray-500 mt-2">We'll register your domain with AWS SES to enable email sending.</p>
                  </div>

                  <div class="bg-blue-50 rounded-xl p-4 mb-6">
                    <div class="flex gap-3">
                      <AlertCircle class="w-5 h-5 text-blue-500 flex-shrink-0 mt-0.5" />
                      <div class="text-sm text-blue-700">
                        <p class="font-medium">What happens in this step:</p>
                        <ul class="list-disc ml-4 mt-2 space-y-1">
                          <li>Your domain is registered with AWS SES</li>
                          <li>SES generates DKIM records for email authentication</li>
                          <li>DNS records are created that you'll need to add to your DNS provider</li>
                        </ul>
                      </div>
                    </div>
                  </div>

                  <div v-if="submitError" class="flex items-center gap-2 p-3 bg-red-50 text-red-700 rounded-lg text-sm mb-4">
                    <AlertCircle class="w-4 h-4 flex-shrink-0" />
                    {{ submitError }}
                  </div>

                  <Button @click="initiateSESVerification" :disabled="sesVerifying" class="w-full">
                    <Loader2 v-if="sesVerifying" class="w-4 h-4 animate-spin" />
                    <Zap v-else class="w-4 h-4" />
                    Register Domain with AWS SES
                  </Button>
                </div>

                <!-- Step 2: Add DNS Records -->
                <div v-if="setupStep === 2">
                  <div class="text-center mb-6">
                    <div class="w-16 h-16 rounded-full bg-blue-100 flex items-center justify-center mx-auto mb-4">
                      <Shield class="w-8 h-8 text-blue-500" />
                    </div>
                    <h3 class="text-lg font-semibold text-gray-900">Step 2: Add DNS Records</h3>
                    <p class="text-gray-500 mt-2">Add the required DNS records to verify your domain.</p>
                  </div>

                  <!-- DNS Records List -->
                  <div v-if="selectedDomain?.dnsRecords && selectedDomain.dnsRecords.length > 0" class="space-y-2 mb-6">
                    <div
                      v-for="record in selectedDomain.dnsRecords"
                      :key="record.hostname || record.name"
                      class="bg-gray-50 rounded-lg border border-gray-200 p-3"
                    >
                      <div class="flex items-start justify-between gap-4">
                        <div class="flex-1 min-w-0">
                          <div class="flex items-center gap-2 mb-1">
                            <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-mono font-semibold bg-gray-200 text-gray-700">
                              {{ record.recordType || record.type }}
                            </span>
                          </div>
                          <p class="text-xs text-gray-500 font-mono truncate">{{ record.hostname || record.name }}</p>
                          <p class="text-sm text-gray-700 font-mono mt-1 break-all">{{ record.value }}</p>
                        </div>
                        <button
                          @click="copyToClipboard(record.value)"
                          class="flex-shrink-0 p-2 hover:bg-gray-200 rounded-lg transition-colors group"
                          title="Copy value"
                        >
                          <Check v-if="copiedText === record.value" class="w-4 h-4 text-green-500" />
                          <Copy v-else class="w-4 h-4 text-gray-400 group-hover:text-gray-600" />
                        </button>
                      </div>
                    </div>
                  </div>

                  <!-- Cloudflare Integration -->
                  <div class="bg-gradient-to-r from-orange-50 to-amber-50 rounded-xl p-4 border border-orange-200 mb-6">
                    <div class="flex items-start gap-3">
                      <div class="w-10 h-10 rounded-lg bg-orange-100 flex items-center justify-center flex-shrink-0">
                        <Cloud class="w-5 h-5 text-orange-600" />
                      </div>
                      <div class="flex-1">
                        <h4 class="font-medium text-gray-900">Auto-add to Cloudflare</h4>
                        <p class="text-sm text-gray-600 mt-1">Enter your Cloudflare API token to automatically add DNS records.</p>

                        <div class="mt-4 space-y-3">
                          <div>
                            <label class="block text-sm font-medium text-gray-700 mb-1">Cloudflare API Token</label>
                            <div class="relative">
                              <Key class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
                              <input
                                v-model="cloudflareApiToken"
                                type="password"
                                placeholder="Enter your API token"
                                class="w-full pl-9 pr-4 py-2 text-sm border border-gray-200 rounded-lg focus:outline-none focus:ring-2 focus:ring-orange-500 focus:border-transparent"
                              />
                            </div>
                            <a href="https://dash.cloudflare.com/profile/api-tokens" target="_blank" class="inline-flex items-center gap-1 text-xs text-orange-600 hover:text-orange-700 mt-1">
                              Get API Token <ExternalLink class="w-3 h-3" />
                            </a>
                          </div>

                          <div v-if="cloudflareZones.length > 0">
                            <label class="block text-sm font-medium text-gray-700 mb-1">Select Zone</label>
                            <select
                              v-model="selectedZoneId"
                              class="w-full px-3 py-2 text-sm border border-gray-200 rounded-lg focus:outline-none focus:ring-2 focus:ring-orange-500"
                            >
                              <option value="">Auto-detect</option>
                              <option v-for="zone in cloudflareZones" :key="zone.id" :value="zone.id">
                                {{ zone.name }}
                              </option>
                            </select>
                          </div>

                          <div class="flex gap-2">
                            <Button
                              v-if="cloudflareZones.length === 0"
                              variant="secondary"
                              size="sm"
                              @click="fetchCloudflareZones"
                              :disabled="isLoadingZones || !cloudflareApiToken.trim()"
                            >
                              <Loader2 v-if="isLoadingZones" class="w-4 h-4 animate-spin" />
                              <RefreshCw v-else class="w-4 h-4" />
                              Load Zones
                            </Button>
                            <Button
                              v-if="cloudflareZones.length > 0"
                              variant="primary"
                              size="sm"
                              @click="addDNSRecordsToCloudflare"
                              :disabled="isAddingDNS"
                            >
                              <Loader2 v-if="isAddingDNS" class="w-4 h-4 animate-spin" />
                              <Zap v-else class="w-4 h-4" />
                              Add DNS Records
                            </Button>
                          </div>
                        </div>
                      </div>
                    </div>
                  </div>

                  <div v-if="submitError" class="flex items-center gap-2 p-3 bg-red-50 text-red-700 rounded-lg text-sm mb-4">
                    <AlertCircle class="w-4 h-4 flex-shrink-0" />
                    {{ submitError }}
                  </div>

                  <div class="flex gap-3">
                    <Button variant="secondary" @click="skipCloudflare" class="flex-1">
                      Skip (Add Manually)
                    </Button>
                  </div>
                </div>

                <!-- Step 3: Verify DNS -->
                <div v-if="setupStep === 3">
                  <div class="text-center mb-6">
                    <div class="w-16 h-16 rounded-full bg-green-100 flex items-center justify-center mx-auto mb-4">
                      <RefreshCw class="w-8 h-8 text-green-500" />
                    </div>
                    <h3 class="text-lg font-semibold text-gray-900">Step 3: Verify Configuration</h3>
                    <p class="text-gray-500 mt-2">Click verify to check if your DNS records have propagated.</p>
                  </div>

                  <!-- DNS Results (if from Cloudflare) -->
                  <div v-if="dnsResults.length > 0" class="mb-6">
                    <h4 class="text-sm font-medium text-gray-700 mb-2">Cloudflare DNS Results:</h4>
                    <div class="space-y-2">
                      <div
                        v-for="result in dnsResults"
                        :key="result.hostname"
                        class="flex items-center gap-2 p-2 rounded-lg text-sm"
                        :class="result.success ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-700'"
                      >
                        <component :is="result.success ? CheckCircle : XCircle" class="w-4 h-4" />
                        <span class="font-mono">{{ result.type }}</span>
                        <span>{{ result.hostname }}</span>
                        <span v-if="result.error" class="text-xs">- {{ result.error }}</span>
                      </div>
                    </div>
                  </div>

                  <div class="bg-amber-50 rounded-xl p-4 mb-6">
                    <div class="flex gap-3">
                      <AlertCircle class="w-5 h-5 text-amber-500 flex-shrink-0 mt-0.5" />
                      <div class="text-sm text-amber-700">
                        <p class="font-medium">DNS Propagation</p>
                        <p class="mt-1">DNS changes can take up to 48 hours to propagate globally. You can check the status anytime.</p>
                      </div>
                    </div>
                  </div>

                  <div v-if="submitError" class="flex items-center gap-2 p-3 bg-red-50 text-red-700 rounded-lg text-sm mb-4">
                    <AlertCircle class="w-4 h-4 flex-shrink-0" />
                    {{ submitError }}
                  </div>

                  <div class="flex gap-3">
                    <Button variant="secondary" @click="closeSetupWizard" class="flex-1">
                      Done for Now
                    </Button>
                    <Button @click="checkSESVerificationStatus" :disabled="sesCheckingStatus" class="flex-1">
                      <Loader2 v-if="sesCheckingStatus" class="w-4 h-4 animate-spin" />
                      <RefreshCw v-else class="w-4 h-4" />
                      Check Verification
                    </Button>
                  </div>
                </div>

                <!-- Step 4: Complete -->
                <div v-if="setupStep === 4">
                  <div class="text-center">
                    <div class="w-20 h-20 rounded-full bg-green-100 flex items-center justify-center mx-auto mb-4">
                      <CheckCircle class="w-10 h-10 text-green-500" />
                    </div>
                    <h3 class="text-xl font-semibold text-gray-900">Domain Verified!</h3>
                    <p class="text-gray-500 mt-2">Your domain is now ready to send emails via AWS SES.</p>

                    <div class="mt-8 space-y-3">
                      <Button @click="closeSetupWizard" class="w-full">
                        <Check class="w-4 h-4" />
                        Done
                      </Button>
                      <Button variant="secondary" @click="openAddIdentityModal(selectedDomainUuid)" class="w-full">
                        <Plus class="w-4 h-4" />
                        Add Sender Identity
                      </Button>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </Transition>
        </div>
      </Transition>
    </Teleport>
  </AppLayout>
</template>
