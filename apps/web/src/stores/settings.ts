import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { api } from '@/lib/api'

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
  twoFactorEnabled: false,
  twoFactorMethod: null,
}

export interface Filter {
  id: string
  name: string
  conditions: string
  actions: string
  enabled: boolean
}

export interface BlockedSender {
  id: string
  email: string
  blockedAt: string
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
  const filters = ref<Filter[]>([])
  const blockedSenders = ref<BlockedSender[]>([])
  const sessions = ref<Session[]>([])
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

    // Load filters
    const storedFilters = localStorage.getItem('userFilters')
    if (storedFilters) {
      try {
        filters.value = JSON.parse(storedFilters)
      } catch {
        filters.value = []
      }
    }

    // Load blocked senders
    const storedBlocked = localStorage.getItem('blockedSenders')
    if (storedBlocked) {
      try {
        blockedSenders.value = JSON.parse(storedBlocked)
      } catch {
        blockedSenders.value = []
      }
    }
  }

  // Save settings to localStorage
  function saveToStorage() {
    localStorage.setItem('userSettings', JSON.stringify(settings.value))
    localStorage.setItem('userFilters', JSON.stringify(filters.value))
    localStorage.setItem('blockedSenders', JSON.stringify(blockedSenders.value))
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

  // Filters
  function addFilter(filter: Omit<Filter, 'id'>) {
    const newFilter: Filter = {
      ...filter,
      id: crypto.randomUUID(),
    }
    filters.value.push(newFilter)
    saveToStorage()
  }

  function updateFilter(id: string, updates: Partial<Filter>) {
    const index = filters.value.findIndex(f => f.id === id)
    if (index !== -1) {
      filters.value[index] = { ...filters.value[index], ...updates }
      saveToStorage()
    }
  }

  function deleteFilter(id: string) {
    filters.value = filters.value.filter(f => f.id !== id)
    saveToStorage()
  }

  // Blocked senders
  function blockSender(email: string) {
    if (blockedSenders.value.some(b => b.email === email)) return
    blockedSenders.value.push({
      id: crypto.randomUUID(),
      email,
      blockedAt: new Date().toISOString(),
    })
    saveToStorage()
  }

  function unblockSender(id: string) {
    blockedSenders.value = blockedSenders.value.filter(b => b.id !== id)
    saveToStorage()
  }

  // Sessions
  async function fetchSessions() {
    try {
      const result = await api.get<{ sessions: Session[] }>('/api/v1/auth/sessions')
      sessions.value = (result?.sessions ?? []).filter((s): s is Session => s != null)
    } catch {
      // Mock data for now
      sessions.value = [{
        id: '1',
        uuid: '1',
        deviceName: 'Chrome on macOS',
        deviceType: 'desktop',
        browser: 'Chrome',
        os: 'macOS',
        ipAddress: '',
        location: 'Current Session',
        lastSeenAt: new Date().toISOString(),
        isCurrent: true,
      }]
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

  // 2FA
  async function enable2FA(method: 'authenticator' | 'webauthn') {
    try {
      const result = await api.post<{ secret?: string; qrCode?: string }>('/api/v1/auth/2fa/enable', { method })
      return result
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to enable 2FA'
      return null
    }
  }

  async function verify2FA(code: string) {
    try {
      await api.post('/api/v1/auth/2fa/verify', { code })
      settings.value.twoFactorEnabled = true
      settings.value.twoFactorMethod = 'authenticator'
      saveToStorage()
      return true
    } catch {
      error.value = 'Invalid verification code'
      return false
    }
  }

  async function disable2FA(code: string) {
    try {
      await api.post('/api/v1/auth/2fa/disable', { code })
      settings.value.twoFactorEnabled = false
      settings.value.twoFactorMethod = null
      saveToStorage()
      return true
    } catch {
      error.value = 'Failed to disable 2FA'
      return false
    }
  }

  // Browser notifications
  async function requestBrowserNotifications() {
    if (!('Notification' in window)) {
      error.value = 'Browser notifications not supported'
      return false
    }

    const permission = await Notification.requestPermission()
    if (permission === 'granted') {
      settings.value.browserNotifications = true
      saveToStorage()
      return true
    }
    return false
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
  applyTheme()

  return {
    settings,
    filters,
    blockedSenders,
    sessions,
    isLoading,
    isSaving,
    error,
    saveSuccess,
    settingsLoaded,
    saveDmarcOrganization,
    fetchSettings,
    saveSettings,
    updateSetting,
    addFilter,
    updateFilter,
    deleteFilter,
    blockSender,
    unblockSender,
    fetchSessions,
    revokeSession,
    revokeAllOtherSessions,
    changePassword,
    enable2FA,
    verify2FA,
    disable2FA,
    requestBrowserNotifications,
    applyTheme,
  }
})
