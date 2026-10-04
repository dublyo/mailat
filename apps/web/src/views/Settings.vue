<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { User, Shield, Bell, Palette, Filter, Webhook, Check, X, Plus } from 'lucide-vue-next'
import AppLayout from '@/components/layout/AppLayout.vue'
import Button from '@/components/common/Button.vue'
import Modal from '@/components/common/Modal.vue'
import { useAuthStore } from '@/stores/auth'
import { useSettingsStore } from '@/stores/settings'
import { webhookApi, type Webhook as WebhookType, type WebhookDelivery, type WebhookAttempt } from '@/lib/api'

const authStore = useAuthStore()
const settingsStore = useSettingsStore()

// Helper to format dates
function formatDate(dateStr: string): string {
  if (!dateStr) return 'Unknown'
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffMins = Math.floor(diffMs / 60000)
  const diffHours = Math.floor(diffMs / 3600000)
  const diffDays = Math.floor(diffMs / 86400000)

  if (diffMins < 1) return 'Just now'
  if (diffMins < 60) return `${diffMins} minute${diffMins > 1 ? 's' : ''} ago`
  if (diffHours < 24) return `${diffHours} hour${diffHours > 1 ? 's' : ''} ago`
  if (diffDays < 7) return `${diffDays} day${diffDays > 1 ? 's' : ''} ago`
  return date.toLocaleDateString()
}

type SettingsTab = 'general' | 'security' | 'notifications' | 'appearance' | 'filters' | 'integrations'

const activeTab = ref<SettingsTab>('general')

const tabs = [
  { id: 'general', label: 'General', icon: User },
  { id: 'security', label: 'Security', icon: Shield },
  { id: 'notifications', label: 'Notifications', icon: Bell },
  { id: 'appearance', label: 'Appearance', icon: Palette },
  { id: 'filters', label: 'Filters & Rules', icon: Filter },
  { id: 'integrations', label: 'Integrations', icon: Webhook },
] as const

// Password change modal
const showPasswordModal = ref(false)
const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const passwordError = ref('')

// Filter modal
const showFilterModal = ref(false)
const filterName = ref('')
const filterConditions = ref('')
const filterActions = ref('')

// Block sender modal
const showBlockModal = ref(false)
const blockEmail = ref('')

// 2FA modal
const show2FAModal = ref(false)
const twoFACode = ref('')
const qrCodeUrl = ref('')

// Webhooks
const webhooks = ref<WebhookType[]>([])
const showWebhookModal = ref(false)
const webhookName = ref('')
const webhookUrl = ref('')
const webhookEvents = ref<string[]>([])

const availableEvents = [
  { value: 'email.received', label: 'Email Received' },
  { value: 'email.sent', label: 'Email Sent' },
  { value: 'email.delivered', label: 'Email Delivered' },
  { value: 'email.bounced', label: 'Email Bounced' },
  { value: 'email.complained', label: 'Spam Complaint' },
  { value: 'email.failed', label: 'Send Failed' },
  { value: 'email.unknown', label: 'Send Outcome Unknown' },
]
const webhookSecret = ref('')
const webhookNotice = ref('')
const webhookBusy = ref('')
const deliveries = ref<WebhookDelivery[]>([])
const deliveryPage = ref(1)
const deliveryTotal = ref(0)
const deliveryStatus = ref('')
const deliveryLoading = ref(false)
const deliveryError = ref('')
let deliveryRequest = 0
const attemptsLoading = ref(false)
const selectedDelivery = ref('')
const deliveryAttempts = ref<WebhookAttempt[]>([])
async function fetchDeliveries(page = 1) {
  const request = ++deliveryRequest
  deliveryLoading.value = true
  deliveryError.value = ''
  try { const data = await webhookApi.deliveries(page, deliveryStatus.value); if (request !== deliveryRequest) return; deliveries.value = data.deliveries; deliveryPage.value = data.page; deliveryTotal.value = data.total }
  catch { if (request === deliveryRequest) deliveryError.value = 'Could not load webhook deliveries. Try again.' }
  finally { if (request === deliveryRequest) deliveryLoading.value = false }
}
async function showAttempts(id: string) {
  selectedDelivery.value = id; deliveryAttempts.value = []; deliveryError.value = ''; attemptsLoading.value = true
  try { const data = await webhookApi.delivery(id); if (selectedDelivery.value === id) deliveryAttempts.value = data.attempts }
  catch { if (selectedDelivery.value === id) deliveryError.value = 'Could not load delivery attempts.' }
  finally { if (selectedDelivery.value === id) attemptsLoading.value = false }
}
async function replayDelivery(id: string) {
  webhookBusy.value = id
  try { await webhookApi.replay(id); webhookNotice.value = 'Replay queued with the same event ID.'; await fetchDeliveries(deliveryPage.value) }
  catch { deliveryError.value = 'Could not replay this delivery. Refresh its status and try again.' }
  finally { webhookBusy.value = '' }
}
async function rotateWebhookSecret(id: string) {
  webhookBusy.value = id
  try { const result = await webhookApi.rotateSecret(id); webhookSecret.value = result.secret; webhookNotice.value = 'Secret rotated. Update your receiver before the next delivery.' }
  catch { settingsStore.error = 'Could not rotate the signing secret.' }
  finally { webhookBusy.value = '' }
}

async function fetchWebhooks() {
  try {
    const result = await webhookApi.list()
    webhooks.value = result ?? []
  } catch {
    webhooks.value = []
  }
}

async function handleAddWebhook() {
  if (!webhookName.value || !webhookUrl.value || webhookEvents.value.length === 0) return

  try {
    const webhook = await webhookApi.create({
      name: webhookName.value,
      url: webhookUrl.value,
      events: webhookEvents.value,
    })
    const { secret, ...savedWebhook } = webhook
    webhooks.value.push(savedWebhook)
    webhookSecret.value = secret ?? ''
 webhookNotice.value = 'Webhook created. Save the signing secret before dismissing it.'
    showWebhookModal.value = false
    webhookName.value = ''
    webhookUrl.value = ''
    webhookEvents.value = []
  } catch (e) {
    settingsStore.error = e instanceof Error ? e.message : 'Failed to create webhook'
  }
}

async function deleteWebhook(uuid: string) {
  try {
    await webhookApi.delete(uuid)
    webhooks.value = webhooks.value.filter(w => w.uuid !== uuid)
  } catch {
    settingsStore.error = 'Failed to delete webhook'
  }
}

async function toggleWebhook(webhook: WebhookType) {
  try {
    await webhookApi.update(webhook.uuid, { active: !webhook.active })
    const index = webhooks.value.findIndex(w => w.uuid === webhook.uuid)
    if (index !== -1) {
      webhooks.value[index] = { ...webhooks.value[index], active: !webhooks.value[index].active }
    }
  } catch {
    settingsStore.error = 'Failed to update webhook'
  }
}

async function testWebhook(uuid: string) {
  webhookBusy.value = uuid
  try {
    const result = await webhookApi.test(uuid)
    webhookNotice.value = result.status === 'delivered'
      ? `Test delivered (HTTP ${result.httpStatus}).`
      : `Test ${result.status}: ${result.error || 'Delivery is queued; check attempts below.'}`
    await fetchDeliveries()
  } catch { settingsStore.error = 'Could not test this webhook. Check its URL and active status.' }
  finally { webhookBusy.value = '' }
}

onMounted(() => {
  settingsStore.fetchSettings()
  settingsStore.fetchSessions()
  fetchWebhooks()
  fetchDeliveries()
})

// Computed for easy access
const settings = computed(() => settingsStore.settings)

// Save handlers
async function saveGeneral() {
  settingsStore.settings.displayName = settingsStore.settings.displayName || authStore.user?.name || ''
  await settingsStore.saveSettings()
}

async function saveNotifications() {
  await settingsStore.saveSettings()
}

async function saveAppearance() {
  await settingsStore.saveSettings()
}

// Password change
async function handleChangePassword() {
  passwordError.value = ''

  if (newPassword.value !== confirmPassword.value) {
    passwordError.value = 'Passwords do not match'
    return
  }

  if (newPassword.value.length < 8) {
    passwordError.value = 'Password must be at least 8 characters'
    return
  }

  const success = await settingsStore.changePassword(currentPassword.value, newPassword.value)
  if (success) {
    showPasswordModal.value = false
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
  } else {
    passwordError.value = settingsStore.error || 'Failed to change password'
  }
}

// 2FA
async function handleEnable2FA() {
  const result = await settingsStore.enable2FA('authenticator')
  if (result?.qrCode) {
    qrCodeUrl.value = result.qrCode
    show2FAModal.value = true
  }
}

async function handleVerify2FA() {
  const success = await settingsStore.verify2FA(twoFACode.value)
  if (success) {
    show2FAModal.value = false
    twoFACode.value = ''
  }
}

// Filters
function handleAddFilter() {
  if (!filterName.value || !filterConditions.value || !filterActions.value) return

  settingsStore.addFilter({
    name: filterName.value,
    conditions: filterConditions.value,
    actions: filterActions.value,
    enabled: true,
  })

  showFilterModal.value = false
  filterName.value = ''
  filterConditions.value = ''
  filterActions.value = ''
}

function handleDeleteFilter(id: string) {
  settingsStore.deleteFilter(id)
}

// Blocked senders
function handleBlockSender() {
  if (!blockEmail.value) return
  settingsStore.blockSender(blockEmail.value)
  showBlockModal.value = false
  blockEmail.value = ''
}

function handleUnblockSender(id: string) {
  settingsStore.unblockSender(id)
}

// Browser notifications
async function handleEnableBrowserNotifications() {
  await settingsStore.requestBrowserNotifications()
}

// Sign out all sessions
async function handleSignOutAll() {
  await settingsStore.revokeAllOtherSessions()
}
</script>

<template>
  <AppLayout>
    <div class="flex-1 flex">
      <!-- Sidebar -->
      <aside class="w-64 border-r border-gmail-border bg-white">
        <div class="p-4">
          <h1 class="text-xl font-medium">Settings</h1>
        </div>
        <nav>
          <button
            v-for="tab in tabs"
            :key="tab.id"
            @click="activeTab = tab.id"
            :class="[
              'w-full flex items-center gap-3 px-4 py-3 text-left transition-colors',
              activeTab === tab.id
                ? 'bg-gmail-selected text-gmail-blue'
                : 'hover:bg-gmail-hover text-gmail-gray'
            ]"
          >
            <component :is="tab.icon" class="w-5 h-5" />
            <span class="text-sm font-medium">{{ tab.label }}</span>
          </button>
        </nav>
      </aside>

      <!-- Content -->
      <main class="flex-1 p-6 overflow-y-auto">
        <!-- Success message -->
        <div
          v-if="settingsStore.saveSuccess"
          role="status"
          class="mb-4 p-3 bg-green-100 text-green-800 rounded-lg flex items-center gap-2"
        >
          <Check class="w-5 h-5" />
          Settings saved successfully!
        </div>

        <div v-if="settingsStore.error" role="alert" class="mb-4 p-3 bg-red-50 text-red-700 rounded-lg text-sm">{{ settingsStore.error }}</div>

        <!-- General Settings -->
        <div v-if="activeTab === 'general'" class="max-w-2xl">
          <h2 class="text-lg font-medium mb-6">General Settings</h2>

          <div class="space-y-6">
            <section>
              <h3 class="text-sm font-medium text-gmail-gray mb-4 flex items-center gap-2">
                <User class="w-4 h-4" />
                Profile
              </h3>
              <div class="space-y-4">
                <div>
                  <label class="block text-sm font-medium mb-1">Display name</label>
                  <input
                    type="text"
                    v-model="settingsStore.settings.displayName"
                    :placeholder="authStore.user?.name"
                    class="w-full px-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
                  />
                </div>
                <div>
                  <label class="block text-sm font-medium mb-1">Email</label>
                  <input
                    type="email"
                    :value="authStore.user?.email"
                    disabled
                    class="w-full px-4 py-2 border border-gmail-border rounded-lg bg-gmail-lightGray text-gmail-gray"
                  />
                </div>
              </div>
            </section>

            <section class="pt-6 border-t border-gmail-border">
              <h3 class="text-sm font-medium text-gmail-gray mb-4">Email Preferences</h3>
              <div class="space-y-3">
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    v-model="settingsStore.settings.showSnippets"
                    class="w-4 h-4 text-gmail-blue rounded border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">Show email snippets in inbox</span>
                </label>
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    v-model="settingsStore.settings.conversationView"
                    class="w-4 h-4 text-gmail-blue rounded border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">Enable conversation view</span>
                </label>
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    v-model="settingsStore.settings.autoAdvance"
                    class="w-4 h-4 text-gmail-blue rounded border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">Auto-advance to next email after delete</span>
                </label>
              </div>
            </section>

            <div class="pt-6">
              <Button @click="saveGeneral" :disabled="settingsStore.isSaving">
                {{ settingsStore.isSaving ? 'Saving...' : 'Save Changes' }}
              </Button>
            </div>
          </div>
        </div>

        <!-- Security Settings -->
        <div v-else-if="activeTab === 'security'" class="max-w-2xl">
          <h2 class="text-lg font-medium mb-6">Security Settings</h2>

          <div class="space-y-6">
            <section>
              <h3 class="text-sm font-medium text-gmail-gray mb-4">Password</h3>
              <Button variant="secondary" @click="showPasswordModal = true">Change Password</Button>
            </section>

            <section class="pt-6 border-t border-gmail-border">
              <h3 class="text-sm font-medium text-gmail-gray mb-4">Two-Factor Authentication</h3>
              <div class="space-y-4">
                <div class="flex items-center justify-between p-4 bg-gmail-lightGray rounded-lg">
                  <div>
                    <p class="font-medium">Authenticator App</p>
                    <p class="text-sm text-gmail-gray">Use an app like Google Authenticator</p>
                  </div>
                  <Button
                    v-if="!settingsStore.settings.twoFactorEnabled"
                    @click="handleEnable2FA"
                  >
                    Enable
                  </Button>
                  <span v-else class="text-green-600 text-sm font-medium flex items-center gap-1">
                    <Check class="w-4 h-4" /> Enabled
                  </span>
                </div>
                <div class="flex items-center justify-between p-4 bg-gmail-lightGray rounded-lg">
                  <div>
                    <p class="font-medium">Security Keys (WebAuthn)</p>
                    <p class="text-sm text-gmail-gray">Use hardware security keys like YubiKey</p>
                  </div>
                  <Button variant="secondary">Add Key</Button>
                </div>
              </div>
            </section>

            <section class="pt-6 border-t border-gmail-border">
              <h3 class="text-sm font-medium text-gmail-gray mb-4">Active Sessions</h3>
              <div class="space-y-2">
                <div
                  v-for="session in settingsStore.sessions"
                  :key="session.uuid"
                  class="flex items-center justify-between p-3 border border-gmail-border rounded-lg"
                >
                  <div>
                    <p class="font-medium text-sm">{{ session.deviceName }}</p>
                    <p class="text-xs text-gmail-gray">
                      {{ session.ipAddress ? `${session.ipAddress} · ` : '' }}Last seen: {{ formatDate(session.lastSeenAt) }}
                    </p>
                  </div>
                  <span
                    v-if="session.isCurrent"
                    class="text-xs text-green-600 bg-green-100 px-2 py-1 rounded"
                  >
                    Current
                  </span>
                  <button
                    v-else
                    @click="settingsStore.revokeSession(session.uuid)"
                    class="text-gmail-red text-sm hover:underline"
                  >
                    Revoke
                  </button>
                </div>
              </div>
              <button
                @click="handleSignOutAll"
                class="mt-4 text-gmail-red text-sm hover:underline"
              >
                Sign out of all other sessions
              </button>
            </section>
          </div>
        </div>

        <!-- Notifications Settings -->
        <div v-else-if="activeTab === 'notifications'" class="max-w-2xl">
          <h2 class="text-lg font-medium mb-6">Notification Settings</h2>

          <div class="space-y-6">
            <section>
              <h3 class="text-sm font-medium text-gmail-gray mb-4">Email Notifications</h3>
              <div class="space-y-3">
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    v-model="settingsStore.settings.newEmailNotifications"
                    class="w-4 h-4 text-gmail-blue rounded border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">New email notifications</span>
                </label>
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    v-model="settingsStore.settings.campaignReports"
                    class="w-4 h-4 text-gmail-blue rounded border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">Campaign delivery reports</span>
                </label>
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    v-model="settingsStore.settings.weeklyDigest"
                    class="w-4 h-4 text-gmail-blue rounded border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">Weekly digest summary</span>
                </label>
              </div>
            </section>

            <section class="pt-6 border-t border-gmail-border">
              <h3 class="text-sm font-medium text-gmail-gray mb-4">Alert Notifications</h3>
              <div class="space-y-3">
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    v-model="settingsStore.settings.blacklistAlerts"
                    class="w-4 h-4 text-gmail-blue rounded border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">Blacklist alerts</span>
                </label>
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    v-model="settingsStore.settings.bounceRateWarnings"
                    class="w-4 h-4 text-gmail-blue rounded border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">High bounce rate warnings</span>
                </label>
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    v-model="settingsStore.settings.quotaWarnings"
                    class="w-4 h-4 text-gmail-blue rounded border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">Quota limit warnings</span>
                </label>
              </div>
            </section>

            <section class="pt-6 border-t border-gmail-border">
              <h3 class="text-sm font-medium text-gmail-gray mb-4">Desktop Notifications</h3>
              <div class="flex items-center justify-between p-4 bg-gmail-lightGray rounded-lg">
                <div>
                  <p class="font-medium">Browser Notifications</p>
                  <p class="text-sm text-gmail-gray">Get notified when you receive new emails</p>
                </div>
                <Button
                  v-if="!settingsStore.settings.browserNotifications"
                  variant="secondary"
                  @click="handleEnableBrowserNotifications"
                >
                  Enable
                </Button>
                <span v-else class="text-green-600 text-sm font-medium flex items-center gap-1">
                  <Check class="w-4 h-4" /> Enabled
                </span>
              </div>
            </section>

            <div class="pt-6">
              <Button @click="saveNotifications" :disabled="settingsStore.isSaving">
                {{ settingsStore.isSaving ? 'Saving...' : 'Save Changes' }}
              </Button>
            </div>
          </div>
        </div>

        <!-- Appearance Settings -->
        <div v-else-if="activeTab === 'appearance'" class="max-w-2xl">
          <h2 class="text-lg font-medium mb-6">Appearance Settings</h2>

          <div class="space-y-6">
            <section>
              <h3 class="text-sm font-medium text-gmail-gray mb-4">Theme</h3>
              <div class="grid grid-cols-3 gap-4">
                <button
                  @click="settingsStore.settings.theme = 'light'"
                  :class="[
                    'p-4 border-2 rounded-lg text-center transition-colors',
                    settingsStore.settings.theme === 'light' ? 'border-gmail-blue' : 'border-gmail-border hover:border-gmail-gray'
                  ]"
                >
                  <div class="w-full h-12 bg-white border border-gmail-border rounded mb-2"></div>
                  <span class="text-sm font-medium">Light</span>
                </button>
                <button
                  @click="settingsStore.settings.theme = 'dark'"
                  :class="[
                    'p-4 border-2 rounded-lg text-center transition-colors',
                    settingsStore.settings.theme === 'dark' ? 'border-gmail-blue' : 'border-gmail-border hover:border-gmail-gray'
                  ]"
                >
                  <div class="w-full h-12 bg-gray-900 rounded mb-2"></div>
                  <span class="text-sm font-medium">Dark</span>
                </button>
                <button
                  @click="settingsStore.settings.theme = 'system'"
                  :class="[
                    'p-4 border-2 rounded-lg text-center transition-colors',
                    settingsStore.settings.theme === 'system' ? 'border-gmail-blue' : 'border-gmail-border hover:border-gmail-gray'
                  ]"
                >
                  <div class="w-full h-12 bg-gradient-to-r from-white to-gray-900 rounded mb-2"></div>
                  <span class="text-sm font-medium">System</span>
                </button>
              </div>
            </section>

            <section class="pt-6 border-t border-gmail-border">
              <h3 class="text-sm font-medium text-gmail-gray mb-4">Density</h3>
              <div class="space-y-2">
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="radio"
                    name="density"
                    value="comfortable"
                    v-model="settingsStore.settings.density"
                    class="w-4 h-4 text-gmail-blue border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">Comfortable</span>
                </label>
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="radio"
                    name="density"
                    value="cozy"
                    v-model="settingsStore.settings.density"
                    class="w-4 h-4 text-gmail-blue border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">Cozy</span>
                </label>
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="radio"
                    name="density"
                    value="compact"
                    v-model="settingsStore.settings.density"
                    class="w-4 h-4 text-gmail-blue border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">Compact</span>
                </label>
              </div>
            </section>

            <section class="pt-6 border-t border-gmail-border">
              <h3 class="text-sm font-medium text-gmail-gray mb-4">Inbox Layout</h3>
              <div class="space-y-2">
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="radio"
                    name="layout"
                    value="default"
                    v-model="settingsStore.settings.inboxLayout"
                    class="w-4 h-4 text-gmail-blue border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">Default</span>
                </label>
                <label class="flex items-center gap-3 cursor-pointer">
                  <input
                    type="radio"
                    name="layout"
                    value="split"
                    v-model="settingsStore.settings.inboxLayout"
                    class="w-4 h-4 text-gmail-blue border-gmail-border focus:ring-gmail-blue"
                  />
                  <span class="text-sm">Split View (reading pane on right)</span>
                </label>
              </div>
            </section>

            <div class="pt-6">
              <Button @click="saveAppearance" :disabled="settingsStore.isSaving">
                {{ settingsStore.isSaving ? 'Saving...' : 'Save Changes' }}
              </Button>
            </div>
          </div>
        </div>

        <!-- Filters & Rules Settings -->
        <div v-else-if="activeTab === 'filters'" class="max-w-2xl">
          <h2 class="text-lg font-medium mb-6">Filters & Rules</h2>

          <section class="mb-6 p-4 border border-gmail-border rounded-lg" aria-labelledby="dmarc-organization-title" :aria-busy="settingsStore.isLoading || settingsStore.isSaving">
            <label class="flex items-start gap-3">
              <input v-model="settingsStore.settings.autoOrganizeDmarcReports" type="checkbox" :disabled="settingsStore.isLoading || settingsStore.isSaving || !settingsStore.settingsLoaded" aria-describedby="dmarc-organization-description" class="mt-1 w-4 h-4 rounded border-gray-300 text-gmail-blue focus:ring-gmail-blue" />
              <span><span id="dmarc-organization-title" class="text-sm font-medium">Automatically organize DMARC reports</span><span id="dmarc-organization-description" class="block mt-1 text-sm text-gmail-gray">Move verified aggregate reports into DMARC Reports as they arrive for any of your receiving domains. Your filters can choose a different destination. Turning this off affects future mail; existing reports stay where they are.</span></span>
            </label>
            <p class="text-xs text-gmail-gray mt-3">This setting does not change DNS records or enable receiving for sending-only domains.</p>
            <div class="mt-4 flex items-center gap-3">
              <Button :disabled="settingsStore.isLoading || settingsStore.isSaving || !settingsStore.settingsLoaded" @click="settingsStore.saveDmarcOrganization()">{{ settingsStore.isSaving ? 'Saving…' : 'Save organization preference' }}</Button>
              <button v-if="!settingsStore.settingsLoaded && !settingsStore.isLoading" @click="settingsStore.fetchSettings()" class="text-sm text-gmail-blue underline">Retry loading settings</button>
              <span v-if="settingsStore.isLoading" role="status" class="text-sm text-gmail-gray">Loading preference…</span>
            </div>
          </section>

          <div class="space-y-6">
            <div class="flex items-center justify-between">
              <p class="text-gmail-gray text-sm">Create rules to automatically organize incoming emails</p>
              <Button @click="showFilterModal = true">
                <Plus class="w-4 h-4" />
                Create Filter
              </Button>
            </div>

            <div class="border border-gmail-border rounded-lg overflow-hidden">
              <div class="bg-gmail-lightGray px-4 py-3 border-b border-gmail-border">
                <span class="text-sm font-medium">Active Filters ({{ settingsStore.filters.length }})</span>
              </div>
              <div v-if="settingsStore.filters.length === 0" class="p-8 text-center text-gmail-gray">
                <Filter class="w-12 h-12 mx-auto mb-3 opacity-50" />
                <p>No filters created yet</p>
              </div>
              <div v-else class="divide-y divide-gmail-border">
                <div
                  v-for="filter in settingsStore.filters"
                  :key="filter.id"
                  class="p-4 flex items-center justify-between"
                >
                  <div>
                    <p class="font-medium text-sm">{{ filter.name }}</p>
                    <p class="text-xs text-gmail-gray">{{ filter.conditions }} → {{ filter.actions }}</p>
                  </div>
                  <div class="flex items-center gap-2">
                    <button class="text-gmail-blue text-sm hover:underline">Edit</button>
                    <button
                      @click="handleDeleteFilter(filter.id)"
                      class="text-gmail-red text-sm hover:underline"
                    >
                      Delete
                    </button>
                  </div>
                </div>
              </div>
            </div>

            <section class="pt-6 border-t border-gmail-border">
              <h3 class="text-sm font-medium text-gmail-gray mb-4">Blocked Senders ({{ settingsStore.blockedSenders.length }})</h3>
              <div v-if="settingsStore.blockedSenders.length === 0" class="text-center text-gmail-gray py-4">
                <p class="text-sm">No blocked senders</p>
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="blocked in settingsStore.blockedSenders"
                  :key="blocked.id"
                  class="flex items-center justify-between p-3 bg-gmail-lightGray rounded-lg"
                >
                  <span class="text-sm">{{ blocked.email }}</span>
                  <button
                    @click="handleUnblockSender(blocked.id)"
                    class="text-gmail-red text-sm hover:underline"
                  >
                    Unblock
                  </button>
                </div>
              </div>
              <Button variant="secondary" class="mt-4" @click="showBlockModal = true">
                Add Blocked Address
              </Button>
            </section>
          </div>
        </div>

        <!-- Integrations Settings -->
        <div v-else-if="activeTab === 'integrations'" class="max-w-2xl">
          <h2 class="text-lg font-medium mb-6">Integrations</h2>

          <div class="space-y-6">
            <section>
              <div class="flex items-center justify-between mb-4">
                <h3 class="text-sm font-medium text-gmail-gray">Webhooks</h3>
                <Button @click="showWebhookModal = true" class="gap-2">
                  <Plus class="w-4 h-4" />
                  Add Webhook
                </Button>
              </div>

              <p v-if="webhookNotice" role="status" class="mb-4 text-sm text-gmail-gray">{{ webhookNotice }}</p>
              <div v-if="webhookSecret" class="mb-4 rounded-lg border border-blue-200 bg-blue-50 p-4">
                <p class="text-sm font-medium">Save your signing secret</p>
                <p class="text-sm text-gmail-gray mt-1">Shown once. Use this to verify each webhook before processing its event.</p>
                <input :value="webhookSecret" readonly aria-label="Webhook signing secret" class="w-full mt-2 p-2 rounded border border-gmail-border font-mono text-xs" @focus="($event.target as HTMLInputElement).select()" />
                <button class="text-sm text-gmail-blue mt-2 hover:underline" @click="webhookSecret = ''">I saved it — dismiss</button>
              </div>
              <div v-if="webhooks.length === 0" class="p-6 text-center border border-dashed border-gmail-border rounded-lg">
                <Webhook class="w-8 h-8 mx-auto text-gmail-gray mb-2" />
                <p class="text-gmail-gray">No webhooks configured</p>
                <p class="text-sm text-gmail-gray mt-1">Add a webhook to receive real-time event notifications</p>
              </div>

              <div v-else class="space-y-3">
                <div
                  v-for="webhook in webhooks"
                  :key="webhook.uuid"
                  class="p-4 border border-gmail-border rounded-lg"
                >
                  <div class="flex items-start justify-between">
                    <div class="flex-1 min-w-0">
                      <div class="flex items-center gap-2">
                        <p class="font-medium truncate">{{ webhook.name }}</p>
                        <span
                          :class="webhook.active ? 'bg-green-100 text-green-800' : 'bg-gray-100 text-gray-600'"
                          class="text-xs px-2 py-0.5 rounded"
                        >
                          {{ webhook.active ? 'Active' : 'Paused' }}
                        </span>
                      </div>
                      <p class="text-sm text-gmail-gray truncate mt-1">{{ webhook.url }}</p>
                      <div class="flex flex-wrap gap-1 mt-2">
                        <span
                          v-for="event in webhook.events"
                          :key="event"
                          class="text-xs bg-gmail-lightGray px-2 py-0.5 rounded"
                        >
                          {{ event }}
                        </span>
                      </div>
                    </div>
                    <div class="flex items-center gap-2 ml-4">
                      <button
                        @click="testWebhook(webhook.uuid)"
 :disabled="webhookBusy === webhook.uuid || !webhook.active"
                        class="text-sm text-gmail-blue hover:underline"
                      >
                        Test
                      </button>
                      <button @click="rotateWebhookSecret(webhook.uuid)" :disabled="webhookBusy === webhook.uuid" class="text-sm text-gmail-blue hover:underline">Rotate secret</button>
                      <button
                        @click="toggleWebhook(webhook)"
                        class="text-sm hover:underline"
                        :class="webhook.active ? 'text-gmail-gray' : 'text-green-600'"
                      >
                        {{ webhook.active ? 'Pause' : 'Enable' }}
                      </button>
                      <button
                        @click="deleteWebhook(webhook.uuid)"
                        class="text-sm text-gmail-red hover:underline"
                      >
                        Delete
                      </button>
                    </div>
                  </div>
                </div>
              </div>
            </section>

            <section class="pt-6 border-t border-gmail-border">
              <div class="flex flex-wrap items-center gap-3 mb-3">
                <h3 class="font-medium mr-auto">Webhook deliveries</h3>
                <select v-model="deliveryStatus" @change="fetchDeliveries()" aria-label="Filter webhook delivery status" class="border border-gmail-border rounded p-2 text-sm">
                  <option value="">All statuses</option><option value="delivered">Delivered</option><option value="retry">Retrying</option><option value="dead_letter">Needs attention</option><option value="pending">Pending</option><option value="cancelled">Cancelled</option>
                </select>
                <button @click="fetchDeliveries(deliveryPage)" :disabled="deliveryLoading" class="text-sm text-gmail-blue">Refresh</button>
              </div>
              <p class="text-sm text-gmail-gray mb-3">Failed deliveries retry automatically up to eight attempts. Replay keeps the same event ID, so your receiver can prevent duplicate processing.</p>
              <p v-if="deliveryError" role="alert" class="text-sm text-gmail-red">{{ deliveryError }}</p>
              <p v-if="deliveryLoading" role="status" class="text-sm text-gmail-gray">Loading deliveries…</p>
              <p v-else-if="deliveries.length === 0" class="text-sm text-gmail-gray py-4">No deliveries match this filter.</p>
              <div v-for="delivery in deliveries" :key="delivery.id" class="border border-gmail-border rounded-lg p-3 mb-2">
                <div class="flex flex-wrap items-center gap-2"><span class="font-medium text-sm">{{ delivery.type }}</span><span class="text-xs rounded bg-gmail-lightGray px-2 py-1">{{ delivery.status }}</span><span class="text-xs text-gmail-gray">{{ delivery.attempts }} attempts · {{ new Date(delivery.createdAt).toLocaleString() }}</span></div>
                <p v-if="delivery.error" class="text-sm text-gmail-red mt-1">{{ delivery.error }}</p>
                <p class="text-xs text-gmail-gray mt-1 break-all">Event {{ delivery.eventId }}</p>
                <div class="flex gap-4 mt-2"><button @click="showAttempts(delivery.id)" class="text-sm text-gmail-blue">View attempts</button><button v-if="['dead_letter', 'delivered', 'cancelled'].includes(delivery.status)" @click="replayDelivery(delivery.id)" :disabled="webhookBusy === delivery.id" class="text-sm text-gmail-blue">Replay</button></div>
                <div v-if="selectedDelivery === delivery.id" class="mt-3 space-y-2">
                  <p v-if="attemptsLoading" role="status" class="text-xs text-gmail-gray">Loading attempts…</p>
                  <p v-else-if="deliveryAttempts.length === 0" class="text-xs text-gmail-gray">No recorded attempts yet.</p>
                  <div v-for="attempt in deliveryAttempts" :key="`${attempt.replay}-${attempt.attempt}`" class="text-xs bg-gmail-lightGray rounded p-2">Attempt {{ attempt.attempt }} · replay {{ attempt.replay }} · {{ attempt.httpStatus ? `HTTP ${attempt.httpStatus}` : 'No HTTP response' }} · {{ attempt.durationMs }} ms<p v-if="attempt.error" class="mt-1">{{ attempt.error }}</p></div>
                </div>
              </div>
              <div class="flex items-center justify-between mt-3 text-sm"><button :disabled="deliveryPage <= 1 || deliveryLoading" @click="fetchDeliveries(deliveryPage - 1)" class="text-gmail-blue disabled:text-gmail-gray">Previous</button><span>{{ deliveryTotal }} deliveries · page {{ deliveryPage }}</span><button :disabled="deliveryPage * 20 >= deliveryTotal || deliveryLoading" @click="fetchDeliveries(deliveryPage + 1)" class="text-gmail-blue disabled:text-gmail-gray">Next</button></div>
            </section>

            <section class="pt-6 border-t border-gmail-border">
              <h3 class="text-sm font-medium text-gmail-gray mb-4">Connect an automation tool</h3>
              <div class="p-4 bg-gmail-lightGray rounded-lg space-y-3 text-sm">
                <p class="font-medium">n8n, Zapier and Make</p>
                <p class="text-gmail-gray">Use an HTTP request action to send and manage mail. Add a webhook above to receive email events, then verify its signature before processing it.</p>
                <p class="text-gmail-gray">Create a scoped API key for each workflow. Keep the same request key when retrying a send, and store processed event IDs to prevent duplicate work.</p>
                <RouterLink to="/api-docs" class="inline-flex text-gmail-blue hover:underline">Open API keys and integration examples</RouterLink>
              </div>
            </section>
          </div>
        </div>
      </main>
    </div>

    <!-- Password Change Modal -->
    <Modal :open="showPasswordModal" @close="showPasswordModal = false" title="Change Password">
      <form @submit.prevent="handleChangePassword" class="space-y-4">
        <div v-if="passwordError" class="p-3 bg-red-100 text-red-800 rounded-lg text-sm">
          {{ passwordError }}
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">Current Password</label>
          <input
            type="password"
            v-model="currentPassword"
            class="w-full px-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            required
          />
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">New Password</label>
          <input
            type="password"
            v-model="newPassword"
            class="w-full px-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            required
            minlength="8"
          />
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">Confirm New Password</label>
          <input
            type="password"
            v-model="confirmPassword"
            class="w-full px-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            required
          />
        </div>
        <div class="flex justify-end gap-3 pt-4">
          <Button type="button" variant="secondary" @click="showPasswordModal = false">Cancel</Button>
          <Button type="submit">Change Password</Button>
        </div>
      </form>
    </Modal>

    <!-- 2FA Modal -->
    <Modal :open="show2FAModal" @close="show2FAModal = false" title="Enable Two-Factor Authentication">
      <div class="space-y-4">
        <p class="text-sm text-gmail-gray">
          Scan the QR code with your authenticator app, then enter the verification code below.
        </p>
        <div v-if="qrCodeUrl" class="flex justify-center p-4 bg-white rounded-lg">
          <img :src="qrCodeUrl" alt="2FA QR Code" class="w-48 h-48" />
        </div>
        <div v-else class="flex justify-center p-8">
          <div class="w-48 h-48 bg-gmail-lightGray rounded-lg flex items-center justify-center">
            <span class="text-gmail-gray">Loading QR Code...</span>
          </div>
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">Verification Code</label>
          <input
            type="text"
            v-model="twoFACode"
            placeholder="000000"
            maxlength="6"
            class="w-full px-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue text-center text-2xl tracking-widest font-mono"
          />
        </div>
        <div class="flex justify-end gap-3 pt-4">
          <Button variant="secondary" @click="show2FAModal = false">Cancel</Button>
          <Button @click="handleVerify2FA" :disabled="twoFACode.length !== 6">Verify & Enable</Button>
        </div>
      </div>
    </Modal>

    <!-- Filter Modal -->
    <Modal :open="showFilterModal" @close="showFilterModal = false" title="Create Filter">
      <form @submit.prevent="handleAddFilter" class="space-y-4">
        <div>
          <label class="block text-sm font-medium mb-1">Filter Name</label>
          <input
            type="text"
            v-model="filterName"
            placeholder="e.g., Newsletter Filter"
            class="w-full px-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            required
          />
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">Conditions</label>
          <input
            type="text"
            v-model="filterConditions"
            placeholder="e.g., From: *@newsletter.*"
            class="w-full px-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            required
          />
          <p class="text-xs text-gmail-gray mt-1">Use * as wildcard. Examples: From:, To:, Subject:, Has:</p>
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">Actions</label>
          <input
            type="text"
            v-model="filterActions"
            placeholder="e.g., Label: Newsletters, Skip Inbox"
            class="w-full px-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            required
          />
          <p class="text-xs text-gmail-gray mt-1">Available: Label, Skip Inbox, Mark Read, Star, Delete</p>
        </div>
        <div class="flex justify-end gap-3 pt-4">
          <Button type="button" variant="secondary" @click="showFilterModal = false">Cancel</Button>
          <Button type="submit">Create Filter</Button>
        </div>
      </form>
    </Modal>

    <!-- Block Sender Modal -->
    <Modal :open="showBlockModal" @close="showBlockModal = false" title="Block Sender">
      <form @submit.prevent="handleBlockSender" class="space-y-4">
        <div>
          <label class="block text-sm font-medium mb-1">Email Address</label>
          <input
            type="email"
            v-model="blockEmail"
            placeholder="spam@example.com"
            class="w-full px-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            required
          />
        </div>
        <p class="text-sm text-gmail-gray">
          Emails from this address will be automatically moved to spam.
        </p>
        <div class="flex justify-end gap-3 pt-4">
          <Button type="button" variant="secondary" @click="showBlockModal = false">Cancel</Button>
          <Button type="submit">Block Sender</Button>
        </div>
      </form>
    </Modal>

    <!-- Webhook Modal -->
    <Modal :open="showWebhookModal" @close="showWebhookModal = false" title="Add Webhook">
      <form @submit.prevent="handleAddWebhook" class="space-y-4">
        <div>
          <label class="block text-sm font-medium mb-1">Name</label>
          <input
            type="text"
            v-model="webhookName"
            placeholder="e.g., Email Delivery Events"
            class="w-full px-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            required
          />
        </div>
        <div>
          <label class="block text-sm font-medium mb-1">Endpoint URL</label>
          <input
            type="url"
            v-model="webhookUrl"
            placeholder="https://your-server.com/webhook"
            class="w-full px-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue font-mono text-sm"
            required
          />
        </div>
        <div>
          <label class="block text-sm font-medium mb-2">Events</label>
          <div class="grid grid-cols-2 gap-2">
            <label
              v-for="event in availableEvents"
              :key="event.value"
              class="flex items-center gap-2 p-2 border border-gmail-border rounded-lg cursor-pointer hover:bg-gmail-hover"
              :class="{ 'border-gmail-blue bg-blue-50': webhookEvents.includes(event.value) }"
            >
              <input
                type="checkbox"
                :value="event.value"
                v-model="webhookEvents"
                class="rounded border-gmail-border text-gmail-blue focus:ring-gmail-blue"
              />
              <span class="text-sm">{{ event.label }}</span>
            </label>
          </div>
        </div>
        <div class="flex justify-end gap-3 pt-4">
          <Button type="button" variant="secondary" @click="showWebhookModal = false">Cancel</Button>
          <Button type="submit" :disabled="!webhookName || !webhookUrl || webhookEvents.length === 0">
            Create Webhook
          </Button>
        </div>
      </form>
    </Modal>
  </AppLayout>
</template>
