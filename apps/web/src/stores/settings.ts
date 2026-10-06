import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { api, inboxFiltersApi, trustedSendersApi, type InboxFilter, type InboxFilterInput, type TrustedSender, type TwoFactorSetup, type TwoFactorStatus } from '@/lib/api'

// Browser-local copies of account data. They are cleared on logout so a shared
// browser does not show the previous user's settings or rules.
const LOCAL_SETTINGS_KEYS = ['userSettings', 'userFilters', 'blockedSenders'] as const

export interface UserSettings {
  // General
  displayName: string
  showSnippets: boolean
  conversationView: boolean
  autoAdvance: boolean
  autoOrganizeDmarcReports: boolean

  // Notifications
  newEmailNotifications: boolean
  campaignReports: boolean
  weeklyDigest: boolean
  blacklistAlerts: boolean
  bounceRateWarnings: boolean
  quotaWarnings: boolean
  browserNotifications: boolean

  // Appearance
  theme: 'light' | 'dark' | 'system'
  density: 'comfortable' | 'cozy' | 'compact'
  inboxLayout: 'default' | 'split'

  // Privacy: 'ask' blocks remote images until shown per message
  remoteImages: 'ask' | 'always'

  // Security
  twoFactorEnabled: boolean
  twoFactorMethod: 'authenticator' | 'webauthn' | null
}

const defaultSettings: UserSettings = {
  displayName: '',
  showSnippets: true,
  conversationView: true,
  autoAdvance: false,
  autoOrganizeDmarcReports: true,
  newEmailNotifications: true,
  campaignReports: true,
  weeklyDigest: false,
  blacklistAlerts: true,
  bounceRateWarnings: true,
  quotaWarnings: true,
  browserNotifications: false,
  theme: 'light',
  density: 'comfortable',
  inboxLayout: 'default',
  remoteImages: 'ask',
  twoFactorEnabled: false,
  twoFactorMethod: null,
}

// Rules older versions kept only in this browser. They were never applied by
// the server; the user can import them (blocked senders) or recreate them.
export interface LocalFilter {
  id: string
  name: string
  conditions: string
  actions: string
  enabled?: boolean
}

export interface LocalBlockedSender {
  id: string
  email: string
  blockedAt?: string
}

export interface LocalMailRules {
  filters: LocalFilter[]
  blockedSenders: LocalBlockedSender[]
}

function readLocalList<T>(key: string): T[] {
  try {
    const parsed = JSON.parse(localStorage.getItem(key) || '[]')
    return Array.isArray(parsed) ? parsed.filter(item => item && typeof item === 'object') : []
  } catch {
    return []
  }
}

function errorMessage(e: unknown, fallback: string) {
  return e instanceof Error && e.message ? e.message : fallback
}

export interface Session {
  id: string
  uuid: string
  deviceName: string
  deviceType: string
  browser: string
  os: string
  ipAddress: string
  location: string
  lastSeenAt: string
  isCurrent: boolean
}

export const useSettingsStore = defineStore('settings', () => {
  const settings = ref<UserSettings>({ ...defaultSettings })
  const filters = ref<InboxFilter[]>([])
  const blockedSenders = ref<InboxFilter[]>([])
  const trustedSenders = ref<TrustedSender[]>([])
  const rulesLoading = ref(false)
  const rulesError = ref<string | null>(null)
  const localRules = ref<LocalMailRules>({ filters: [], blockedSenders: [] })
  const sessions = ref<Session[]>([])
  const sessionsError = ref<string | null>(null)
  const twoFactor = ref<TwoFactorStatus | null>(null)
  const isLoading = ref(false)
  const isSaving = ref(false)
  const error = ref<string | null>(null)
  const saveSuccess = ref(false)
  const settingsLoaded = ref(false)
  let settingsOwner: string | null = null
  let settingsRequest = 0

  // Load settings from localStorage on init
  function loadFromStorage() {
    const stored = localStorage.getItem('userSettings')
    if (stored) {
      try {
        const parsed = JSON.parse(stored)
        settings.value = { ...defaultSettings, ...parsed }
      } catch {
        settings.value = { ...defaultSettings }
      }
    }
  }

  // Save settings to localStorage (display preferences only; rules are server-side)
  function saveToStorage() {
    localStorage.setItem('userSettings', JSON.stringify(settings.value))
  }

  async function fetchSettings() {
    const request = ++settingsRequest
    const token = api.getToken()
    isLoading.value = true
    settingsLoaded.value = false
    error.value = null
    try {
      const result = await api.get<UserSettings>('/api/v1/settings')
      if (request !== settingsRequest || token !== api.getToken()) return
      if (result) {
        settings.value = { ...defaultSettings, ...result }
        settingsOwner = token
        settingsLoaded.value = true
        saveToStorage()
      }
    } catch {
      if (request !== settingsRequest || token !== api.getToken()) return
      loadFromStorage()
      error.value = 'Could not load settings. Retry before changing mail organization.'
    } finally {
      if (request === settingsRequest) isLoading.value = false
    }
  }

  async function saveDmarcOrganization() {
    if (isSaving.value || !settingsLoaded.value || settingsOwner !== api.getToken()) return false
    const token = settingsOwner
    isSaving.value = true
    error.value = null
    saveSuccess.value = false
    try {
      // This server-side preference cannot fall back to a local-only save.
      // Send the boolean explicitly so opting out is not treated as omission.
      await api.put('/api/v1/settings', { autoOrganizeDmarcReports: settings.value.autoOrganizeDmarcReports })
      if (token !== api.getToken()) return false
      saveToStorage()
      saveSuccess.value = true
      setTimeout(() => { saveSuccess.value = false }, 3000)
      return true
    } catch {
      if (token === api.getToken()) error.value = 'DMARC organization was not saved. Please try again.'
      return false
    } finally {
      isSaving.value = false
    }
  }

  async function saveSettings() {
    if (isSaving.value) return false
    if (!settingsLoaded.value || settingsOwner !== api.getToken()) {
      saveSuccess.value = false
      error.value = 'Load your current account settings before saving changes.'
      return false
    }
    const token = settingsOwner
    // Mail organization is saved only by its dedicated action. An unrelated
    // appearance/general save must not submit an unsaved DMARC checkbox value.
    const { autoOrganizeDmarcReports: _dmarcPreference, ...updates } = settings.value
    isSaving.value = true
    error.value = null
    saveSuccess.value = false
    try {
      await api.put('/api/v1/settings', updates)
      if (token !== api.getToken() || settingsOwner !== token) return false
      saveToStorage()
      saveSuccess.value = true
      setTimeout(() => { saveSuccess.value = false }, 3000)
      return true
    } catch {
      // Server preferences affect receiving; a local write cannot confirm them.
      if (token === api.getToken() && settingsOwner === token) error.value = 'Settings were not saved. Please try again.'
      return false
    } finally {
      isSaving.value = false
    }
  }

  function updateSetting<K extends keyof UserSettings>(key: K, value: UserSettings[K]) {
    settings.value[key] = value
  }

  // Filters and blocked senders live on the server (/inbox/filters).
  async function fetchMailRules() {
    rulesLoading.value = true
    rulesError.value = null
    try {
      const [userFilters, blocked] = await Promise.all([inboxFiltersApi.list('filter'), inboxFiltersApi.list('blocked_sender')])
      filters.value = userFilters ?? []
      blockedSenders.value = blocked ?? []
    } catch (e) {
      rulesError.value = errorMessage(e, 'Could not load your filters. Try again.')
    } finally {
      rulesLoading.value = false
    }
  }

  async function saveFilter(input: InboxFilterInput, uuid?: string) {
    rulesError.value = null
    try {
      const saved = uuid ? await inboxFiltersApi.update(uuid, input) : await inboxFiltersApi.create({ ...input, kind: 'filter', name: input.name || '', conditions: input.conditions || [] })
      const index = filters.value.findIndex(f => f.uuid === saved.uuid)
      if (index === -1) filters.value.push(saved)
      else filters.value[index] = saved
      filters.value.sort((a, b) => b.priority - a.priority || a.id - b.id)
      return saved
    } catch (e) {
      rulesError.value = errorMessage(e, 'The filter was not saved.')
      return null
    }
  }

  async function setFilterActive(filter: InboxFilter, active: boolean) {
    return saveFilter({ active }, filter.uuid)
  }

  async function deleteFilter(uuid: string) {
    rulesError.value = null
    try {
      await inboxFiltersApi.delete(uuid)
      filters.value = filters.value.filter(f => f.uuid !== uuid)
      return true
    } catch (e) {
      rulesError.value = errorMessage(e, 'The filter was not deleted.')
      return false
    }
  }

  async function blockSender(sender: string, folder: 'spam' | 'trash' = 'spam') {
    rulesError.value = null
    try {
      const saved = await inboxFiltersApi.blockSender(sender, folder)
      if (!blockedSenders.value.some(b => b.uuid === saved.uuid)) blockedSenders.value.push(saved)
      return saved
    } catch (e) {
      rulesError.value = errorMessage(e, 'The sender was not blocked.')
      return null
    }
  }

  async function unblockSender(uuid: string) {
    rulesError.value = null
    try {
      await inboxFiltersApi.delete(uuid)
      blockedSenders.value = blockedSenders.value.filter(b => b.uuid !== uuid)
      return true
    } catch (e) {
      rulesError.value = errorMessage(e, 'The sender was not unblocked.')
      return false
    }
  }

  // Trusted senders: their DMARC-passing mail loads remote images.
  async function fetchTrustedSenders() {
    try {
      trustedSenders.value = (await trustedSendersApi.list()) ?? []
    } catch (e) {
      rulesError.value = errorMessage(e, 'Could not load trusted senders.')
    }
  }

  async function addTrustedSender(sender: string) {
    rulesError.value = null
    try {
      const saved = await trustedSendersApi.add(sender.trim().toLowerCase())
      if (!trustedSenders.value.some(t => t.uuid === saved.uuid)) trustedSenders.value.push(saved)
      return saved
    } catch (e) {
      rulesError.value = errorMessage(e, 'The trusted sender was not saved.')
      return null
    }
  }

  async function removeTrustedSender(uuid: string) {
    rulesError.value = null
    try {
      await trustedSendersApi.delete(uuid)
      trustedSenders.value = trustedSenders.value.filter(t => t.uuid !== uuid)
      return true
    } catch (e) {
      rulesError.value = errorMessage(e, 'The trusted sender was not removed.')
      return false
    }
  }

  // Browser-local rules from older versions. Nothing migrates silently, so a
  // shared browser never pushes another person's rules into this account.
  function loadLocalMailRules() {
    localRules.value = {
      filters: readLocalList<LocalFilter>('userFilters'),
      blockedSenders: readLocalList<LocalBlockedSender>('blockedSenders').filter(b => typeof b.email === 'string' && b.email.trim()),
    }
    return localRules.value
  }

  // POSTs every local blocked sender (the server is idempotent). The browser
  // key is removed only after all of them succeed.
  async function importLocalMailRules() {
    const pending = loadLocalMailRules().blockedSenders
    let imported = 0
    const failed: string[] = []
    for (const local of pending) {
      try {
        const saved = await inboxFiltersApi.blockSender(local.email)
        if (!blockedSenders.value.some(b => b.uuid === saved.uuid)) blockedSenders.value.push(saved)
        imported++
      } catch {
        failed.push(local.email)
      }
    }
    if (failed.length === 0) {
      try { localStorage.removeItem('blockedSenders') } catch { /* storage unavailable */ }
    } else {
      rulesError.value = `${failed.length} blocked sender${failed.length === 1 ? '' : 's'} could not be imported: ${failed.join(', ')}`
    }
    loadLocalMailRules()
    return { imported, failed }
  }

  // Called after a local filter was recreated on the server (or dismissed).
  function forgetLocalFilter(id: string) {
    const remaining = readLocalList<LocalFilter>('userFilters').filter(f => f.id !== id)
    try {
      if (remaining.length) localStorage.setItem('userFilters', JSON.stringify(remaining))
      else localStorage.removeItem('userFilters')
    } catch { /* storage unavailable */ }
    loadLocalMailRules()
  }

  function discardLocalMailRules() {
    for (const key of ['userFilters', 'blockedSenders']) {
      try { localStorage.removeItem(key) } catch { /* storage unavailable */ }
    }
    loadLocalMailRules()
  }

  // Sessions
  async function fetchSessions() {
    sessionsError.value = null
    try {
      const result = await api.get<{ sessions: Session[] }>('/api/v1/auth/sessions')
      sessions.value = (result?.sessions ?? []).filter((s): s is Session => s != null)
    } catch (e) {
      sessions.value = []
      sessionsError.value = errorMessage(e, 'Could not load your sessions.')
    }
  }

  async function revokeSession(uuid: string) {
    try {
      await api.delete(`/api/v1/auth/sessions/${uuid}`)
      sessions.value = sessions.value.filter(s => s.uuid !== uuid)
    } catch {
      error.value = 'Failed to revoke session'
    }
  }

  async function revokeAllOtherSessions() {
    try {
      await api.post('/api/v1/auth/sessions/revoke-all')
      sessions.value = sessions.value.filter(s => s.isCurrent)
    } catch {
      error.value = 'Failed to revoke sessions'
    }
  }

  // Password change
  async function changePassword(currentPassword: string, newPassword: string) {
    try {
      await api.post('/api/v1/auth/change-password', { currentPassword, newPassword })
      return true
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to change password'
      return false
    }
  }

  // 2FA (server state is authoritative; the settings row copy is not used)
  async function fetch2FAStatus() {
    try {
      twoFactor.value = await api.get<TwoFactorStatus>('/api/v1/security/2fa/status')
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Could not load two-factor status'
    }
  }

  async function enable2FA(): Promise<TwoFactorSetup | null> {
    try {
      return await api.post<TwoFactorSetup>('/api/v1/security/2fa/setup')
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to start two-factor setup'
      return null
    }
  }

  // Returns the one-time backup codes. Other sessions are signed out by the server.
  async function verify2FA(code: string): Promise<string[] | null> {
    try {
      const result = await api.post<{ backupCodes: string[] }>('/api/v1/security/2fa/verify', { code })
      const backupCodes = result?.backupCodes ?? []
      twoFactor.value = { enabled: true, backupCodesCount: backupCodes.length }
      return backupCodes
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Invalid verification code'
      return null
    }
  }

  async function disable2FA(password: string, code: string) {
    try {
      await api.post('/api/v1/security/2fa/disable', { password, code })
      twoFactor.value = { enabled: false, backupCodesCount: 0 }
      return true
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to disable two-factor authentication'
      return false
    }
  }

  async function regenerateBackupCodes(password: string, code: string): Promise<string[] | null> {
    try {
      const result = await api.post<{ backupCodes: string[] }>('/api/v1/security/2fa/backup-codes', { password, code })
      const backupCodes = result?.backupCodes ?? []
      twoFactor.value = { enabled: true, backupCodesCount: backupCodes.length }
      return backupCodes
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to generate new backup codes'
      return null
    }
  }

  // Called on logout: forget every browser-local copy of account data.
  // keepLegacyRules leaves the pre-server filters and blocked senders in place
  // after an expired session, so they can still be imported after signing in.
  function clearLocalSettings(options: { keepLegacyRules?: boolean } = {}) {
    for (const key of LOCAL_SETTINGS_KEYS) {
      if (options.keepLegacyRules && key !== 'userSettings') continue
      try { localStorage.removeItem(key) } catch { /* storage unavailable */ }
    }
    settingsRequest++
    settings.value = { ...defaultSettings }
    filters.value = []
    blockedSenders.value = []
    trustedSenders.value = []
    localRules.value = { filters: [], blockedSenders: [] }
    rulesError.value = null
    sessions.value = []
    sessionsError.value = null
    twoFactor.value = null
    settingsLoaded.value = false
    settingsOwner = null
    error.value = null
  }

  // Apply theme
  function applyTheme() {
    const theme = settings.value.theme
    const root = document.documentElement

    if (theme === 'dark') {
      root.classList.add('dark')
    } else if (theme === 'light') {
      root.classList.remove('dark')
    } else {
      // System preference
      if (window.matchMedia('(prefers-color-scheme: dark)').matches) {
        root.classList.add('dark')
      } else {
        root.classList.remove('dark')
      }
    }
  }

  // Watch for theme changes
  watch(() => settings.value.theme, applyTheme)

  // Initialize
  loadFromStorage()
  loadLocalMailRules()
  applyTheme()

  return {
    settings,
    filters,
    blockedSenders,
    trustedSenders,
    rulesLoading,
    rulesError,
    localRules,
    sessions,
    sessionsError,
    twoFactor,
    isLoading,
    isSaving,
    error,
    saveSuccess,
    settingsLoaded,
    saveDmarcOrganization,
    fetchSettings,
    saveSettings,
    updateSetting,
    fetchMailRules,
    saveFilter,
    setFilterActive,
    deleteFilter,
    blockSender,
    unblockSender,
    fetchTrustedSenders,
    addTrustedSender,
    removeTrustedSender,
    loadLocalMailRules,
    importLocalMailRules,
    forgetLocalFilter,
    discardLocalMailRules,
    fetchSessions,
    revokeSession,
    revokeAllOtherSessions,
    changePassword,
    fetch2FAStatus,
    enable2FA,
    verify2FA,
    disable2FA,
    regenerateBackupCodes,
    clearLocalSettings,
    applyTheme,
  }
})
