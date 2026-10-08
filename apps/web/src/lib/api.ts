import axios, { type AxiosInstance, type AxiosResponse } from 'axios'

const API_BASE = import.meta.env.VITE_API_URL || ''

/** SPA pages that work with or without a session (router meta.public). */
export const PUBLIC_PATHS = ['/invite', '/forwards/verify']

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

class ApiClient {
  private client: AxiosInstance
  private token: string | null = null

  constructor() {
    this.client = axios.create({
      baseURL: API_BASE,
      headers: {
        'Content-Type': 'application/json'
      }
    })

    // Add auth header interceptor
    this.client.interceptors.request.use((config) => {
      const token = this.getToken()
      if (token) {
        config.headers.Authorization = `Bearer ${token}`
      }
      return config
    })

    // Handle errors
    this.client.interceptors.response.use(
      (response) => response,
      (error) => {
        // Invalid credentials belong to the login form; a redirect would erase
        // its error and the protected route the user was trying to open.
        const requestToken = error.config?.headers?.Authorization
        const belongsToCurrentSession = !requestToken || requestToken === `Bearer ${this.getToken()}`
        if (error.response?.status === 401 && belongsToCurrentSession && !['/api/v1/auth/login', '/api/v1/auth/2fa/challenge'].includes(error.config?.url)) {
          this.setToken(null)
          // Invite and forward-confirmation pages work signed out; an expired
          // session must not navigate away from them and lose the link.
          if (!PUBLIC_PATHS.includes(window.location.pathname)) window.location.href = '/login'
        }
        // data carries structured details such as an automation's validation errors[].
        return Promise.reject(Object.assign(new Error(error.response?.data?.message || error.message || 'Request failed'), { status: error.response?.status, data: error.response?.data?.data }))
      }
    )
  }

  setToken(token: string | null) {
    this.token = token
    if (token) {
      localStorage.setItem('token', token)
    } else {
      localStorage.removeItem('token')
    }
  }

  getToken(): string | null {
    if (this.token) return this.token
    if (typeof window !== 'undefined') {
      this.token = localStorage.getItem('token')
    }
    return this.token
  }

  async get<T>(endpoint: string, signal?: AbortSignal): Promise<T> {
    const response: AxiosResponse<ApiResponse<T>> = await this.client.get(endpoint, { signal })
    return response.data.data
  }

  async download(endpoint: string): Promise<Blob> {
    const response = await this.client.get(endpoint, { responseType: 'blob' })
    return response.data
  }

  async post<T>(endpoint: string, body?: unknown, headers?: Record<string, string>): Promise<T> {
    const response: AxiosResponse<ApiResponse<T>> = await this.client.post(endpoint, body, { headers })
    return response.data.data
  }

  async put<T>(endpoint: string, body?: unknown): Promise<T> {
    const response: AxiosResponse<ApiResponse<T>> = await this.client.put(endpoint, body)
    return response.data.data
  }

  async delete<T>(endpoint: string, body?: unknown): Promise<T> {
    const response: AxiosResponse<ApiResponse<T>> = await this.client.delete(endpoint, { data: body })
    return response.data.data
  }
}

export const api = new ApiClient()

// ============ Types ============

export interface User {
  id: number
  email: string
  name: string
  avatar?: string
  orgId: number
  role: UserRole
  createdAt: string
}

import type { UserRole } from './roles'
export type { UserRole } from './roles'
export { isOrgAdmin, isMailboxUser, isStaff } from './roles'

export interface Email {
  id: string
  uuid: string
  messageId: string
  subject: string
  from: EmailAddress
  to: EmailAddress[]
  cc?: EmailAddress[]
  bcc?: EmailAddress[]
  body: string
  htmlBody?: string
  snippet: string
  folder: string
  isRead: boolean
  isStarred: boolean
  hasAttachments: boolean
  attachments?: Attachment[]
  labels?: string[]
  threadId?: string
  receivedAt: string
  createdAt: string
  envelopeRecipients?: string[]
  replyToAddress?: string
  inReplyTo?: string
  references?: string[]
  fromEmail?: string
  draftVersion?: number
  sourceAttachments?: ReceivedEmailAttachment[]
  // For received emails - identity that received/will send the email
  identityId?: number
  /** True only when remote images may be quoted into a reply or forward. */
  remoteImagesAllowed?: boolean
}

export interface EmailAddress {
  name?: string
  email: string
}

export interface Attachment {
  id: string
  filename: string
  contentType: string
  size: number
  url: string
}

export interface Folder {
  id: string
  name: string
  type: 'system' | 'custom'
  unreadCount: number
  totalCount: number
}

export interface Label {
  id: string
  name: string
  color: string
}

export interface Contact {
  id: string
  uuid: string
  name: string
  email: string
  avatar?: string
  company?: string
  phone?: string
  tags?: string[]
  createdAt: string
}

export interface ContactList {
  confirmationMode: 'single' | 'double'
  type?: string
  id: string
  uuid: string
  name: string
  description?: string
  contactCount: number
  createdAt: string
}

export interface Domain {
  id: number
  uuid: string
  name: string  // domain name
  domain?: string  // alias for backward compatibility
  status: 'pending' | 'active' | 'suspended'
  verified: boolean
  // Email provider
  emailProvider: 'ses' | 'smtp'
  // SES specific fields
  sesVerified: boolean
  sesDkimTokens?: string[]
  sesIdentityArn?: string
  // Verification status
  mxVerified: boolean
  spfVerified: boolean
  dkimVerified: boolean
  dmarcVerified: boolean
  // Email receiving
  receivingEnabled: boolean
  // DNS Records
  dnsRecords: DNSRecord[]
  verifiedAt?: string
  createdAt: string
  updatedAt: string
}

export interface DNSRecord {
  id: number
  domainId: number
  recordType: string  // MX, TXT, CNAME
  type?: string  // alias for backward compatibility
  hostname: string
  name?: string  // alias for backward compatibility
  value: string
  verified: boolean
  verifiedAt?: string
}

export interface Identity {
  id: string | number
  uuid: string
  displayName: string
  email: string
  domainId: string | number
  isDefault: boolean
  isCatchAll?: boolean
  color?: string  // Hex color for UI display
  canSend?: boolean
  canReceive?: boolean
  /** Own personal identities only; sanitise before inserting into compose. */
  signatureHtml?: string
  signatureText?: string
  wildcardSender?: boolean
  sendAliases?: string[]
  kind?: 'personal' | 'shared'
  /** A shared mailbox identity the caller is a member of; permissions are the caller's. */
  shared?: boolean
  canRead?: boolean
  canManage?: boolean
  sharedMailboxUuid?: string
  sharedMailboxName?: string
  createdAt: string
  updatedAt?: string
}

export type CampaignStatus = 'draft' | 'scheduled' | 'sending' | 'paused' | 'sent' | 'cancelled'

// Campaign mirrors the API's flat model.Campaign; opens and clicks are unique counts.
export interface Campaign {
  id: number
  uuid: string
  name: string
  subject: string
  htmlContent?: string
  textContent?: string
  fromName: string
  fromEmail: string
  replyTo?: string
  listId: number
  listName?: string
  listType: 'static' | 'dynamic' | string
  status: CampaignStatus
  statusReason: string | null
  scheduledAt?: string
  startedAt?: string
  completedAt?: string
  preparedAt: string | null
  throttledUntil: string | null
  totalRecipients: number
  sentCount: number
  deliveredCount: number
  openCount: number
  clickCount: number
  bounceCount: number
  unsubscribeCount: number
  complaintCount: number
  failedCount: number
  skippedCount: number
  unknownCount: number
  trackOpens: boolean
  trackClicks: boolean
  createdByUserId?: number
  createdAt: string
  updatedAt?: string
  warnings?: string[]
}

export interface CampaignCreateRequest {
  name: string
  subject: string
  htmlContent: string
  textContent?: string
  fromName: string
  fromEmail: string
  replyTo?: string
  listId: number
  trackOpens?: boolean
  trackClicks?: boolean
}

// Absent fields are unchanged; null clears replyTo or textContent.
export interface CampaignUpdateRequest {
  name?: string
  subject?: string
  htmlContent?: string
  textContent?: string | null
  fromName?: string
  fromEmail?: string
  replyTo?: string | null
  listId?: number
  trackOpens?: boolean
  trackClicks?: boolean
}

// Rates are percentages of sentCount.
export interface CampaignStatsResponse {
  campaign: Campaign
  openRate: number
  clickRate: number
  clickToOpenRate: number
  bounceRate: number
  unsubscribeRate: number
  complaintRate: number
  deliveredRate: number
  clicksByLink: { url: string; clicks: number; uniqueClicks: number }[] | null
  opensByHour: { hour: string; opens: number }[] | null
}

export interface CampaignProgress {
  status: CampaignStatus
  statusReason: string | null
  preparing: boolean
  total: number
  pending: number
  inFlight: number
  sent: number
  failed: number
  unknown: number
  skipped: number
  cancelled: number
  throttledUntil: string | null
  percent: number
}

// An estimate: the audience is snapshotted when sending starts.
export interface CampaignAudience {
  listType: string
  eligible: number
  excludedInactive: number
  excludedSuppressed: number
  warnings: string[] | null
}

export type CampaignRecipientStatus = 'pending' | 'claimed' | 'sending' | 'sent' | 'failed' | 'unknown' | 'skipped' | 'cancelled'

export interface CampaignRecipient {
  email: string
  status: CampaignRecipientStatus
  skipReason: string | null
  deliveryStatus: string | null
  sentAt: string | null
  openCount: number
  clickCount: number
  unsubscribedAt: string | null
  error: string | null
}

export interface CampaignPreview {
  subject: string
  html: string
  text: string
  unknownVariables: string[] | null
}

export interface CampaignTestResponse {
  status: string
  results: { email: string; status: string; error?: string }[]
}

export interface CampaignSettings {
  postalAddress: string
}

// ============ Auth API ============

export type LoginResponse = { token: string; user: User; requiresTwoFactor?: false } | { requiresTwoFactor: true; challengeToken: string }

export const authApi = {
  login: (email: string, password: string) =>
    api.post<LoginResponse>('/api/v1/auth/login', { email, password }),

  register: (data: { email: string; password: string; name: string }) =>
    api.post<{ token: string; user: User }>('/api/v1/auth/register', data),

  registerStatus: () => api.get<{ open: boolean }>('/api/v1/auth/register-status'),

  me: () => api.get<User>('/api/v1/auth/me'),
  completeChallenge: (challengeToken: string, code: string) =>
    api.post<{ token: string; user: User }>('/api/v1/auth/2fa/challenge', { challengeToken, code }),
  logout: (token: string) => api.post<void>('/api/v1/auth/logout', undefined, { Authorization: `Bearer ${token}` }),
  streamToken: () => api.post<{ token: string; expiresAt: string }>('/api/v1/auth/stream-token'),
}

// ============ Two-factor and sign-in providers ============

export interface TwoFactorSetup {
  secret: string
  qrCodeUrl: string
  qrCodeDataUrl: string
  manualCode: string
}

export interface TwoFactorStatus {
  enabled: boolean
  backupCodesCount: number
}

export interface OAuthConnection {
  id: number
  provider: string
  providerUserId: string
  email?: string
  name?: string
  avatarUrl?: string
  createdAt: string
}

export const oauthApi = {
  providers: () => api.get<{ providers: string[] }>('/api/v1/oauth/providers'),
  connections: () => api.get<OAuthConnection[] | null>('/api/v1/oauth/connections'),
  // The browser then navigates to authUrl; the provider returns to Settings with a ticket.
  connect: (provider: string) => api.post<{ authUrl: string }>(`/api/v1/oauth/${encodeURIComponent(provider)}/connect`),
  disconnect: (provider: string) => api.delete(`/api/v1/oauth/${encodeURIComponent(provider)}`),
  confirmLink: (ticket: string) => api.post<{ provider: string }>('/api/v1/oauth/link/confirm', { ticket }),
  // Sign-in starts with a top-level navigation to the API origin.
  // nonce comes back in the /login fragment (see lib/oauthNonce).
  loginUrl: (provider: string, nonce?: string) => `${API_BASE}/api/v1/oauth/${encodeURIComponent(provider)}${nonce ? `?nonce=${encodeURIComponent(nonce)}` : ''}`,
}

// ============ Inbox API ============

export const inboxApi = {
  list: (folder: string, page = 1, limit = 50) =>
    api.get<{ emails: Email[]; total: number }>(
      `/api/v1/inbox?folder=${folder}&page=${page}&limit=${limit}`
    ),

  get: (uuid: string) => api.get<Email>(`/api/v1/inbox/${uuid}`),

  markRead: (uuids: string[]) => api.post('/api/v1/inbox/mark-read', { uuids }),

  markUnread: (uuids: string[]) => api.post('/api/v1/inbox/mark-unread', { uuids }),

  star: (uuid: string) => api.post(`/api/v1/inbox/${uuid}/star`),

  unstar: (uuid: string) => api.post(`/api/v1/inbox/${uuid}/unstar`),

  move: (uuids: string[], folder: string) =>
    api.post('/api/v1/inbox/move', { uuids, folder }),

  delete: (uuids: string[]) => api.post('/api/v1/inbox/delete', { uuids }),

  search: (query: string, page = 1, limit = 50) =>
    api.get<{ emails: Email[]; total: number }>(
      `/api/v1/inbox/search?q=${encodeURIComponent(query)}&page=${page}&limit=${limit}`
    ),
}

// ============ Compose API ============

export interface ComposeAttachment {
  name: string
  type: string
  content?: string
  blobId?: string
  size?: number
}

export interface ComposeRequest {
  identityId: number
  fromEmail?: string
  to: EmailAddress[]
  cc?: EmailAddress[]
  bcc?: EmailAddress[]
  subject: string
  textBody: string
  htmlBody?: string
  inReplyTo?: string
  references?: string[]
  attachments?: ComposeAttachment[]
  draftId?: string
  draftVersion?: number
  version?: number
}

export interface DraftResult {
  id: string
  version: number
  identityId: number
  createdAt: string
  updatedAt: string
}

export interface SendResult {
  emailId: string
  messageId?: string
  status: 'sent' | 'delivered' | 'bounced' | 'complained' | 'failed' | 'unknown' | 'sending'
  sentAt?: string
  sendError?: string
  /** A failed send that SES never accepted (throttling); a retry is safe. */
  retryable?: boolean
}

export const composeApi = {
  send: (data: ComposeRequest, submissionKey: string) =>
    api.post<SendResult>('/api/v1/compose/send', data, { 'Idempotency-Key': submissionKey }),
  saveDraft: (data: ComposeRequest) => api.post<DraftResult>('/api/v1/compose/drafts', data),
  updateDraft: (id: string, data: ComposeRequest) => api.put<DraftResult>(`/api/v1/compose/drafts/${id}`, data),
  deleteDraft: (id: string) => api.delete(`/api/v1/compose/drafts/${id}`),
}

// ============ Domains API ============

export interface CloudflareZone {
  id: string
  name: string
  status: string
}

export interface CloudflareDNSResult {
  status?: 'created' | 'preserved' | 'conflict' | 'skipped' | 'failed'
  hostname: string
  type: string
  value: string
  success: boolean
  skipped?: boolean
  reason?: string
  error?: string
  dmarc?: DomainDMARCStatus
  receiving?: boolean
}

export type ReceivingMXStatus = 'not_enabled' | 'missing' | 'published' | 'conflict' | 'unknown'

export interface ReceivingMXRecord {
  type: 'MX'
  host: string
  name: string
  value: string
  priority: number
  target: string
}

// Receiving is the owner's opt-in; mxStatus comes from a live public lookup.
export interface DomainReceivingStatus {
  domainUuid: string
  domain: string
  enabled: boolean
  mxRecord: ReceivingMXRecord
  mxStatus: ReceivingMXStatus
  existingMx: string[]
  reason: string
  checkedAt: string
}

export interface DomainDMARCStatus {
  status: 'absent' | 'existing' | 'inherited' | 'conflict' | 'unknown'
  hostname: string
  policyHostname: string
  value: string
  policy: string
  reason: string
  checkedAt: string
  canCreate: boolean
  verified: boolean
  suggestedValue: string
}

export type DomainReadinessKey = 'verified' | 'dmarc' | 'sending_resources' | 'sending_identity' | 'receiving'
export type DomainReadinessStatus = 'ok' | 'missing' | 'pending' | 'attention' | 'unknown' | 'off'
export type DomainReadinessFix = '' | 'verify' | 'dmarc' | 'setup_sending' | 'create_identity' | 'receiving'

export interface DomainReadinessItem {
  key: DomainReadinessKey
  label: string
  status: DomainReadinessStatus
  state: string
  optional: boolean
  detail: string
  fix: DomainReadinessFix
  value: string
}

// GET /domains/:uuid/readiness: the caller's API sending checklist.
export interface DomainReadiness {
  domainUuid: string
  domain: string
  ready: boolean
  items: DomainReadinessItem[]
  suggestedIdentity: string
  checkedAt: string
}

export interface DomainSendingReadiness {
  domainUuid: string
  storageReady: boolean
  feedbackConfigured: boolean
  subscriptionStatus: 'not_configured' | 'pending' | 'active'
  feedbackReady: boolean
  reason: string
  checkedAt: string
}

export const domainApi = {
  list: () => api.get<Domain[]>('/api/v1/domains'),

  create: async (domain: string): Promise<Domain> => {
    const response = await api.post<{ domain: Domain; dnsRecords: DNSRecord[] }>('/api/v1/domains', { name: domain })
    return { ...response.domain, dnsRecords: response.dnsRecords || [] }
  },

  get: async (uuid: string): Promise<Domain> => {
    const response = await api.get<{ domain: Domain; dnsRecords: DNSRecord[] }>(`/api/v1/domains/${uuid}`)
    return { ...response.domain, dnsRecords: response.dnsRecords || [] }
  },

  verify: async (uuid: string): Promise<Domain> => {
    const response = await api.post<{ domain: Domain; dnsRecords: DNSRecord[]; verificationResults: Record<string, boolean> }>(`/api/v1/domains/${uuid}/verify`)
    return { ...response.domain, dnsRecords: response.dnsRecords || [] }
  },

  delete: (uuid: string) => api.delete(`/api/v1/domains/${uuid}`),

  inspectDMARC: (uuid: string, signal?: AbortSignal) =>
    api.get<DomainDMARCStatus>(`/api/v1/domains/${uuid}/dmarc`, signal),

  sendingStatus: (uuid: string, signal?: AbortSignal) =>
    api.get<DomainSendingReadiness>(`/api/v1/domains/${uuid}/sending-status`, signal),

  setupSending: (uuid: string) =>
    api.post<DomainSendingReadiness>(`/api/v1/domains/${uuid}/setup-sending`),

  readiness: (uuid: string, signal?: AbortSignal) =>
    api.get<DomainReadiness>(`/api/v1/domains/${uuid}/readiness`, signal),

  // SES Integration
  initiateSES: async (uuid: string): Promise<{ domain: Domain; dnsRecords: DNSRecord[]; sesRecords: Array<{ type: string; name: string; value: string }> }> => {
    return api.post(`/api/v1/domains/${uuid}/ses-verify`)
  },

  checkSESStatus: async (uuid: string): Promise<{ domain: Domain; sesStatus: { verified: boolean; domain: string; dkimTokens: string[]; mailFromDomain?: string; mailFromVerified?: boolean } }> => {
    return api.get(`/api/v1/domains/${uuid}/ses-status`)
  },

  // Cloudflare Integration
  getCloudflareZones: (apiToken: string) =>
    api.post<CloudflareZone[]>('/api/v1/domains/cloudflare/zones', { apiToken }),

  // scope 'receiving-mx' adds only the root receiving MX (server-gated).
  addDNSToCloudflare: (uuid: string, apiToken: string, zoneId?: string, scope?: 'receiving-mx') =>
    api.post<{ results: CloudflareDNSResult[] }>(`/api/v1/domains/${uuid}/dns/cloudflare`, scope ? { apiToken, zoneId, scope } : { apiToken, zoneId }),

  receivingStatus: (uuid: string, refresh = false, signal?: AbortSignal) =>
    api.get<DomainReceivingStatus>(`/api/v1/domains/${uuid}/receiving${refresh ? '?refresh=true' : ''}`, signal),
}

// ============ Identity API ============

export const identityApi = {
  list: () => api.get<Identity[]>('/api/v1/identities'),

  create: (data: { displayName: string; email: string; domainId: string; password?: string; isCatchAll?: boolean }) =>
    api.post<Identity>('/api/v1/identities', data),

  update: (uuid: string, data: { displayName?: string; isDefault?: boolean; isCatchAll?: boolean; signatureHtml?: string; signatureText?: string }) =>
    api.put<Identity>(`/api/v1/identities/${uuid}`, data),

  delete: (uuid: string) => api.delete(`/api/v1/identities/${uuid}`),
}

// ============ Contacts API ============

export interface ContactFull {
  id: number
  uuid: string
  orgId: number
  email: string
  firstName?: string
  lastName?: string
  attributes?: Record<string, unknown>
  status: 'active' | 'unsubscribed' | 'bounced' | 'complained'
  consentSource?: string
  consentTimestamp?: string
  lastEngagedAt?: string
  engagementScore: number
  createdAt: string
  updatedAt: string
}

export interface ImportContactRow {
  email: string
  firstName?: string
  lastName?: string
  attributes?: Record<string, unknown>
}

export interface ImportContactsRequest {
  contacts: ImportContactRow[]
  listIds?: number[]
  updateExisting?: boolean
  consentSource?: string
}

export interface ImportContactsResponse {
  imported: number
  updated: number
  skipped: number
  /** Rows not added to lists because the address is suppressed or no longer active. */
  suppressed: number
  errors?: string[]
}

export interface ExportContactsRequest {
  listIds?: number[]
  status?: string[]
  format?: string
}

export const contactApi = {
  list: (page = 1, limit = 50) =>
    api.get<{ contacts: ContactFull[]; total: number; page: number; pageSize: number; totalPages: number }>(
      `/api/v1/contacts?page=${page}&pageSize=${limit}`
    ),

  // Literal, case-insensitive match on email and names (server escapes % and _).
  search: (query: string, page = 1, pageSize = 50) =>
    api.get<ListContactsResponse>(
      `/api/v1/contacts?query=${encodeURIComponent(query)}&page=${page}&pageSize=${pageSize}`
    ),

  create: (data: { email: string; firstName?: string; lastName?: string; attributes?: Record<string, unknown>; listIds?: number[]; consentSource?: string }) =>
    api.post<ContactFull>('/api/v1/contacts', data),

  update: (uuid: string, data: { email?: string; firstName?: string; lastName?: string; attributes?: Record<string, unknown>; status?: string }) =>
    api.put<ContactFull>(`/api/v1/contacts/${uuid}`, data),

  delete: (uuid: string) => api.delete(`/api/v1/contacts/${uuid}`),

  import: (data: ImportContactsRequest) =>
    api.post<ImportContactsResponse>('/api/v1/contacts/import', data),

  export: (data?: ExportContactsRequest) =>
    api.post<ContactFull[]>('/api/v1/contacts/export', data || {}),
}

// ============ Lists API ============

export interface ListContactsResponse {
  contacts: ContactFull[]
  total: number
  page: number
  pageSize: number
  totalPages: number
}

export interface ImportToListResponse {
  imported: number
  updated: number
  skipped: number
  suppressed?: number
  errors?: string[]
}

export const listApi = {
  list: () => api.get<ContactList[]>('/api/v1/lists'),

  create: (data: { name: string; description?: string; confirmationMode?: 'single' | 'double' }) =>
    api.post<ContactList>('/api/v1/lists', data),

  get: (uuid: string) => api.get<ContactList>(`/api/v1/lists/${uuid}`),

  update: (uuid: string, data: { name?: string; description?: string; confirmationMode?: 'single' | 'double' }) =>
    api.put<ContactList>(`/api/v1/lists/${uuid}`, data),

  delete: (uuid: string) => api.delete(`/api/v1/lists/${uuid}`),

  getContacts: (uuid: string, page = 1, pageSize = 50) =>
    api.get<ListContactsResponse>(`/api/v1/lists/${uuid}/contacts?page=${page}&pageSize=${pageSize}`),

  addContacts: (uuid: string, contactIds: string[]) =>
    api.post(`/api/v1/lists/${uuid}/contacts`, { contactIds }),

  removeContacts: (uuid: string, contactIds: string[]) =>
    api.delete(`/api/v1/lists/${uuid}/contacts`, { contactIds }),

  importContacts: (uuid: string, data: { contacts: ImportContactRow[]; updateExisting?: boolean; consentSource?: string }) =>
    api.post<ImportToListResponse>(`/api/v1/lists/${uuid}/contacts/import`, data),

  manualAddContact: (uuid: string, data: { email: string; firstName?: string; lastName?: string; attributes?: Record<string, unknown> }) =>
    api.post<ContactFull>(`/api/v1/lists/${uuid}/contacts/manual`, data),
}

// ============ Campaigns API ============

export interface CampaignListResponse {
  campaigns: Campaign[]
  total: number
  page: number
  pageSize: number
  totalPages: number
}

export const campaignApi = {
  list: (params: { page?: number; pageSize?: number; status?: string } = {}) => {
    const query = new URLSearchParams({ page: String(params.page ?? 1), pageSize: String(params.pageSize ?? 100) })
    if (params.status) query.set('status', params.status)
    return api.get<CampaignListResponse>(`/api/v1/campaigns?${query}`)
  },

  create: (data: CampaignCreateRequest) => api.post<Campaign>('/api/v1/campaigns', data),

  get: (uuid: string) => api.get<Campaign>(`/api/v1/campaigns/${uuid}`),

  update: (uuid: string, data: CampaignUpdateRequest) =>
    api.put<Campaign>(`/api/v1/campaigns/${uuid}`, data),

  delete: (uuid: string) => api.delete(`/api/v1/campaigns/${uuid}`),

  schedule: (uuid: string, scheduledAt: string) =>
    api.post<Campaign>(`/api/v1/campaigns/${uuid}/schedule`, { scheduledAt }),

  send: (uuid: string) => api.post<Campaign>(`/api/v1/campaigns/${uuid}/send`),

  pause: (uuid: string) => api.post<Campaign>(`/api/v1/campaigns/${uuid}/pause`),

  resume: (uuid: string) => api.post<Campaign>(`/api/v1/campaigns/${uuid}/resume`),

  cancel: (uuid: string) => api.post<Campaign>(`/api/v1/campaigns/${uuid}/cancel`),

  getStats: (uuid: string) => api.get<CampaignStatsResponse>(`/api/v1/campaigns/${uuid}/stats`),

  progress: (uuid: string) => api.get<CampaignProgress>(`/api/v1/campaigns/${uuid}/progress`),

  audience: (uuid: string) => api.get<CampaignAudience>(`/api/v1/campaigns/${uuid}/audience`),

  recipients: (uuid: string, status = '', page = 1, pageSize = 50) => {
    const query = new URLSearchParams({ page: String(page), pageSize: String(pageSize) })
    if (status) query.set('status', status)
    return api.get<{ recipients: CampaignRecipient[]; total: number }>(`/api/v1/campaigns/${uuid}/recipients?${query}`)
  },

  preview: (uuid: string, contactUuid?: string) =>
    api.post<CampaignPreview>(`/api/v1/campaigns/${uuid}/preview`, contactUuid ? { contactUuid } : {}),

  // The key makes a retry of the same request return the stored outcome instead of sending again.
  sendTest: (uuid: string, emails: string[], idempotencyKey: string) =>
    api.post<CampaignTestResponse>(`/api/v1/campaigns/${uuid}/test`, { emails }, { 'Idempotency-Key': idempotencyKey }),

  getSettings: () => api.get<CampaignSettings>('/api/v1/campaign-settings'),

  updateSettings: (data: CampaignSettings) => api.put<CampaignSettings>('/api/v1/campaign-settings', data),
}

// ============ Health API ============

export interface SESAccountLimits {
  max24HourSend: number
  maxSendRate: number
  sentLast24Hours: number
  sendingEnabled: boolean
  sandboxMode: boolean
  remaining24Hour: number
  usagePercentage: number
}

export interface ReceivingMetrics {
  totalReceived: number
  totalSpam: number
  totalVirus: number
  totalRead: number
  spamRate: number
  readRate: number
  byIdentity?: Record<string, number>
}

export interface DomainAuthStatus {
  domain: string
  spfVerified: boolean
  dkimVerified: boolean
  dmarcVerified: boolean
  sesVerified: boolean
}

export interface AuthenticationStatus {
  totalDomains: number
  verifiedDomains: number
  dkimConfigured: number
  spfConfigured: number
  dmarcConfigured: number
  domains: DomainAuthStatus[]
}

export interface HealthWarning {
  type: string
  severity: 'info' | 'warning' | 'critical'
  title: string
  message: string
  action?: string
}

export interface ReputationMetrics {
  orgId: number
  period: string
  score: number
  totalSent: number
  totalDelivered: number
  totalBounced: number
  totalFailed: number
  totalComplaints: number
  totalReceived: number
  totalSpam: number
  deliveryRate: number
  bounceRate: number
  complaintRate: number
  spamRate: number
  byDomain?: Record<string, {
    domain: string
    sent: number
    delivered: number
    bounced: number
    complaints: number
    deliveryRate: number
    bounceRate: number
    complaintRate: number
  }>
}

export interface EmailHealthSummary {
  sendingMetrics: ReputationMetrics
  sesLimits?: SESAccountLimits
  receivingMetrics: ReceivingMetrics
  authStatus: AuthenticationStatus
  warnings: HealthWarning[]
  healthScore: number
  healthStatus: 'excellent' | 'good' | 'warning' | 'critical' | 'unknown'
}

export const healthApi = {
  checkBlacklists: (ipAddress?: string) =>
    api.post('/api/v1/health/blacklist-check', ipAddress ? { ipAddress } : {}),

  getReputation: (period?: string) =>
    api.get<ReputationMetrics>(`/api/v1/health/reputation${period ? `?period=${period}` : ''}`),

  getSESLimits: () => api.get<SESAccountLimits>('/api/v1/health/ses-limits'),

  getSummary: () => api.get<EmailHealthSummary>('/api/v1/health/summary'),

  getWarmupSchedules: () => api.get('/api/v1/health/warmup/schedules'),

  startWarmup: (ip: string, schedule: string) =>
    api.post('/api/v1/health/warmup', { ip, schedule }),

  getWarmupStatus: (ip: string) => api.get(`/api/v1/health/warmup/${ip}`),

  getQuota: () => api.get('/api/v1/health/quota'),

  getAlerts: (unacknowledgedOnly = false) =>
    api.get(`/api/v1/health/alerts${unacknowledgedOnly ? '?unacknowledged=true' : ''}`),

  acknowledgeAlert: (id: string) => api.post(`/api/v1/health/alerts/${id}/acknowledge`),

  getLogs: (page = 1, limit = 50, status?: string, emailId?: number) => {
    const params = new URLSearchParams()
    params.set('page', page.toString())
    params.set('pageSize', limit.toString())
    if (status) params.set('status', status)
    if (emailId) params.set('emailId', emailId.toString())
    return api.get(`/api/v1/health/logs?${params}`)
  },
}

// ============ Settings API ============

export const settingsApi = {
  get: () => api.get('/api/v1/settings'),

  update: (data: Record<string, unknown>) =>
    api.put('/api/v1/settings', data),

  changePassword: (currentPassword: string, newPassword: string) =>
    api.post('/api/v1/auth/change-password', { currentPassword, newPassword }),

  // Sessions
  getSessions: () => api.get<{ sessions: Session[] }>('/api/v1/auth/sessions'),

  revokeSession: (uuid: string) => api.delete(`/api/v1/auth/sessions/${uuid}`),

  revokeAllSessions: () => api.post('/api/v1/auth/sessions/revoke-all'),

  // 2FA
  enable2FA: () => api.post<TwoFactorSetup>('/api/v1/security/2fa/setup'),

  verify2FA: (code: string) => api.post<{ backupCodes: string[] }>('/api/v1/security/2fa/verify', { code }),

  disable2FA: (password: string, code: string) => api.post('/api/v1/security/2fa/disable', { password, code }),
}

// ============ API Keys ============

export interface ApiKey {
  id: number
  uuid: string
  name: string
  key?: string // Only returned on creation
  keyPrefix: string
  permissions: string[]
  rateLimit: number
  lastUsedAt?: string
  expiresAt?: string
  createdAt: string
}

export interface CreateApiKeyRequest {
  name: string
  permissions: string[]
  rateLimit?: number // Requests per minute (default: 100)
  expiresAt?: string // RFC3339 format or null for no expiry
}

// Available API key permission scopes
export const API_KEY_PERMISSIONS = [
  { value: 'email:send', label: 'Send Email', description: 'Send transactional emails' },
  { value: 'email:read', label: 'Read Email', description: 'Read inbox and email content' },
  { value: 'email:manage', label: 'Manage Email', description: 'Mark read, star, move, trash' },
  { value: 'domains:read', label: 'Read Domains', description: 'List domains and DNS records' },
  { value: 'domains:manage', label: 'Manage Domains', description: 'Add/verify/delete domains' },
  { value: 'identities:read', label: 'Read Identities', description: 'List identities' },
  { value: 'identities:manage', label: 'Manage Identities', description: 'Create/update/delete identities' },
  { value: 'templates:read', label: 'Read Templates', description: 'List templates' },
  { value: 'templates:manage', label: 'Manage Templates', description: 'Create/update/delete templates' },
  { value: 'webhooks:manage', label: 'Manage Webhooks', description: 'Configure webhooks' },
  { value: 'contacts:manage', label: 'Manage Contacts', description: 'Manage contacts and lists' },
  { value: 'campaigns:read', label: 'Read Campaigns', description: 'List campaigns, stats, progress and recipients' },
  { value: 'campaigns:manage', label: 'Manage Campaigns', description: 'Create, send, schedule, pause and cancel campaigns' },
  { value: 'automations:read', label: 'Read Automations', description: 'Read automations, stats and enrollments (includes contact emails)' },
  { value: 'automations:enroll', label: 'Enroll in Automations', description: 'Enroll, cancel and retry contacts' },
] as const

export const apiKeyApi = {
  // List all API keys
  list: () => api.get<ApiKey[]>('/api/v1/api-keys'),

  // Create a new API key
  create: (data: CreateApiKeyRequest) =>
    api.post<ApiKey>('/api/v1/api-keys', data),

  // Delete an API key
  delete: (uuid: string) =>
    api.delete<void>(`/api/v1/api-keys/${uuid}`),
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

// ============ Webhooks API ============

export interface Webhook {
  id: string
  uuid: string
  name: string
  url: string
  events: string[]
  active: boolean
  secret?: string
  successCount: number
  failureCount: number
  lastTriggeredAt?: string
  createdAt: string
}

export interface WebhookCall {
  id: string
  eventType: string
  payload: Record<string, unknown>
  responseStatus?: number
  responseBody?: string
  responseTimeMs?: number
  status: string
  attempts: number
  error?: string
  createdAt: string
}

export interface WebhookDelivery {
  id: string; eventId: string; type: string; status: string; attempts: number; replayCount: number; httpStatus: number; error?: string; createdAt: string; nextAttemptAt: string
}
export interface WebhookAttempt { attempt: number; replay: number; httpStatus: number; error?: string; responseBody?: string; durationMs: number; createdAt: string }
export interface WebhookTestResult { eventId: string; deliveryId: string; status: string; httpStatus: number; error?: string }

export const webhookApi = {
  list: () => api.get<Webhook[]>('/api/v1/webhooks'),

  create: (data: { name: string; url: string; events: string[] }) =>
    api.post<Webhook>('/api/v1/webhooks', data),

  get: (uuid: string) => api.get<Webhook>(`/api/v1/webhooks/${uuid}`),

  update: (uuid: string, data: { name?: string; url?: string; events?: string[]; active?: boolean }) =>
    api.put<Webhook>(`/api/v1/webhooks/${uuid}`, data),

  delete: (uuid: string) => api.delete(`/api/v1/webhooks/${uuid}`),

  rotateSecret: (uuid: string) =>
    api.post<{ secret: string }>(`/api/v1/webhooks/${uuid}/rotate-secret`),

  getCalls: (uuid: string) =>
    api.get<WebhookCall[]>(`/api/v1/webhooks/${uuid}/calls`),

  test: (uuid: string) => api.post<WebhookTestResult>(`/api/v1/webhooks/${uuid}/test`),
  deliveries: (page = 1, status = '') => api.get<{ deliveries: WebhookDelivery[]; total: number; page: number; pageSize: number }>(`/api/v1/webhook-deliveries?page=${page}&pageSize=20&status=${encodeURIComponent(status)}`),
  delivery: (uuid: string) => api.get<{ delivery: WebhookDelivery; attempts: WebhookAttempt[] }>(`/api/v1/webhook-deliveries/${uuid}`),
  replay: (uuid: string) => api.post(`/api/v1/webhook-deliveries/${uuid}/replay`),
}

// ============ Templates API ============

export interface EmailTemplate {
  id: number
  uuid: string
  name: string
  description?: string
  subject: string
  htmlBody: string
  textBody?: string
  variables: string[]
  isActive: boolean
  createdAt: string
  updatedAt: string
}

export const templateApi = {
  list: () => api.get<EmailTemplate[]>('/api/v1/templates'),
}

// ============ Automations API ============

export type AutomationStatus = 'draft' | 'active' | 'paused' | 'archived'
export type AutomationReentryPolicy = 'never' | 'after_exit'
export type EnrollmentStatus = 'active' | 'completed' | 'exited' | 'failed' | 'cancelled'

export interface AutomationGraphNode {
  id: string
  type: string
  position: { x: number; y: number }
  data: { label: string; type: string; config?: Record<string, unknown> }
}

export interface AutomationGraphEdge {
  id: string
  source: string
  target: string
  sourceHandle?: string | null
  targetHandle?: string | null
  type?: string
  animated?: boolean
}

export interface AutomationWorkflow {
  schemaVersion?: number
  nodes: AutomationGraphNode[]
  edges: AutomationGraphEdge[]
}

export interface AutomationCounts {
  enrolled: number
  active: number
  waiting: number
  completed: number
  exited: number
  failed: number
  cancelled: number
}

export interface Automation {
  id: number
  uuid: string
  name: string
  description?: string
  triggerType: string
  triggerConfig?: Record<string, unknown>
  workflow?: AutomationWorkflow | null
  status: AutomationStatus
  reentryPolicy: AutomationReentryPolicy
  publishedVersion: number | null
  hasUnpublishedChanges: boolean
  activatedAt: string | null
  archivedAt: string | null
  stats: AutomationCounts
  enrolledCount: number
  completedCount: number
  inProgressCount: number
  createdAt: string
  updatedAt: string
}

export interface AutomationListResponse {
  automations: Automation[]
  total: number
  page: number
  pageSize: number
}

export interface AutomationValidationError {
  nodeId: string
  field: string
  message: string
}

export interface AutomationSaveRequest {
  name?: string
  description?: string
  triggerType?: string
  triggerConfig?: Record<string, unknown>
  workflow?: AutomationWorkflow
  reentryPolicy?: AutomationReentryPolicy
}

export interface AutomationActivation {
  automation: Automation
  version: number
  published: boolean
}

export interface AutomationNodeStats {
  entered: number
  waiting: number
  succeeded: number
  skipped: number
  failed: number
  yes: number
  no: number
  sent: number
  opened: number
  clicked: number
}

export interface AutomationStats extends AutomationCounts {
  automationUuid: string
  completionRate: number
  version: number
  nodes: Record<string, AutomationNodeStats>
}

export interface AutomationStepRun {
  nodeId: string
  nodeType: string
  status: 'waiting' | 'succeeded' | 'skipped' | 'failed'
  outcome: string | null
  messageUuid?: string
  error: string | null
  startedAt: string
  finishedAt: string | null
  resumeAt: string | null
}

export interface AutomationEnrollment {
  uuid: string
  contactUuid: string
  contactEmail: string
  status: EnrollmentStatus
  exitReason: string | null
  version: number | null
  currentNodeId: string | null
  nextRunAt: string | null
  retryCount: number
  error: string | null
  enrolledAt: string
  completedAt: string | null
  steps?: AutomationStepRun[]
}

export interface AutomationEnrollmentListResponse {
  enrollments: AutomationEnrollment[]
  total: number
  page: number
  pageSize: number
}

const automationPath = (uuid: string) => `/api/v1/automations/${encodeURIComponent(uuid)}`

function pageQuery(params: { page?: number; pageSize?: number; status?: string }) {
  const query = new URLSearchParams({ page: String(params.page ?? 1), pageSize: String(params.pageSize ?? 50) })
  if (params.status) query.set('status', params.status)
  return query.toString()
}

// Validation failures reject with error.data.errors (AutomationValidationError[]).
export const automationApi = {
  list: (params: { page?: number; pageSize?: number; status?: AutomationStatus | '' } = {}) =>
    api.get<AutomationListResponse>(`/api/v1/automations?${pageQuery(params)}`),
  get: (uuid: string) => api.get<Automation>(automationPath(uuid)),
  create: (data: AutomationSaveRequest & { name: string }) => api.post<Automation>('/api/v1/automations', data),
  update: (uuid: string, data: AutomationSaveRequest) => api.put<Automation>(automationPath(uuid), data),
  remove: (uuid: string) => api.delete<void>(automationPath(uuid)),
  validate: (uuid: string) =>
    api.post<{ valid: boolean; errors: AutomationValidationError[] }>(`${automationPath(uuid)}/validate`),
  // publishDraft=false resumes the current published version without publishing the draft.
  activate: (uuid: string, options: { publishDraft?: boolean } = {}) =>
    api.post<AutomationActivation>(`${automationPath(uuid)}/activate`, { publishDraft: options.publishDraft ?? true }),
  pause: (uuid: string) => api.post<Automation>(`${automationPath(uuid)}/pause`),
  archive: (uuid: string) =>
    api.post<{ automation: Automation; cancelledEnrollments: number }>(`${automationPath(uuid)}/archive`),
  stats: (uuid: string, version?: number) =>
    api.get<AutomationStats>(`${automationPath(uuid)}/stats${version ? `?version=${version}` : ''}`),
  enroll: (uuid: string, target: { contactUuid: string } | { listUuid: string }) =>
    api.post<{ enrolled: number; skipped: number }>(`${automationPath(uuid)}/enroll`, target),
  // status also accepts the derived 'waiting'.
  enrollments: (uuid: string, params: { page?: number; pageSize?: number; status?: EnrollmentStatus | 'waiting' | '' } = {}) =>
    api.get<AutomationEnrollmentListResponse>(`${automationPath(uuid)}/enrollments?${pageQuery(params)}`),
  enrollment: (uuid: string, enrollmentUuid: string) =>
    api.get<AutomationEnrollment>(`${automationPath(uuid)}/enrollments/${encodeURIComponent(enrollmentUuid)}`),
  cancelEnrollment: (uuid: string, enrollmentUuid: string) =>
    api.post<AutomationEnrollment>(`${automationPath(uuid)}/enrollments/${encodeURIComponent(enrollmentUuid)}/cancel`),
  retryEnrollment: (uuid: string, enrollmentUuid: string) =>
    api.post<AutomationEnrollment>(`${automationPath(uuid)}/enrollments/${encodeURIComponent(enrollmentUuid)}/retry`),
}

// ============ Received Inbox Types ============

export interface ReceivedEmail {
  id: number
  uuid: string
  orgId: number
  domainId: number
  identityId: number
  envelopeRecipients?: string[]
  direction?: 'inbound' | 'outbound'
  draftVersion?: number
  version?: number
  sendStatus?: string
  deliveryStatus?: string
  messageId: string
  inReplyTo?: string
  references?: string[]
  threadId?: string
  fromEmail: string
  fromName?: string
  toEmails: string[]
  ccEmails?: string[]
  bccEmails?: string[]
  replyTo?: string
  subject: string
  textBody?: string
  htmlBody?: string
  snippet?: string
  rawS3Key?: string
  rawS3Bucket?: string
  sizeBytes: number
  hasAttachments: boolean
  folder: string
  isRead: boolean
  isStarred: boolean
  isArchived: boolean
  isTrashed: boolean
  isSpam: boolean
  labels?: string[]
  spamScore?: number
  spamVerdict?: string
  virusVerdict?: string
  spfVerdict?: string
  dkimVerdict?: string
  dmarcVerdict?: string
  sesMessageId?: string
  receivedAt: string
  readAt?: string
  trashedAt?: string
  createdAt: string
  updatedAt: string
  attachments?: ReceivedEmailAttachment[]
  /** Viewer-specific decision, present on single-message reads. */
  remoteImages?: 'blocked' | 'allowed'
  trustedSender?: boolean
  // Identity info for unified inbox display
  identityEmail?: string
  identityDisplayName?: string
  identityColor?: string
}

export interface ReceivedEmailAttachment {
  id: number
  uuid: string
  filename: string
  contentType: string
  sizeBytes: number
  s3Key: string
  s3Bucket: string
  contentId?: string
  isInline: boolean
  checksum?: string
  downloadUrl?: string
}

export interface InboxListResponse {
  emails: ReceivedEmail[]
  total: number
  unread: number
  page: number
  pageSize: number
  totalPages: number
}

export interface InboxCounts {
  inbox: number
  inboxUnread: number
  dmarcReports: number
  dmarcReportsUnread: number
  // Global unread remains independent of the Inbox and report folder badges.
  unread: number
  starred: number
  sent: number
  drafts: number
  archive?: number
  outbox?: number
  spam: number
  trash: number
  labels?: Record<string, number>
}

export interface MailboxChange {
  cursor: string
  messageUuid: string
  identityId: number
  domainId: number
  operation: 'created' | 'updated' | 'deleted'
  changedAt: string
}

export interface MailboxChanges {
  changes: MailboxChange[]
  nextCursor: string
  hasMore: boolean
}

export interface EmailLabel {
  id: number
  uuid: string
  name: string
  color: string
  createdAt: string
  updatedAt: string
}

export type InboxFilterKind = 'filter' | 'blocked_sender'

export interface InboxFilter {
  id: number
  uuid: string
  kind: InboxFilterKind
  identityId?: number | null
  name: string
  priority: number
  active: boolean
  conditions: FilterCondition[]
  conditionLogic: 'all' | 'any'
  actionLabels?: string[]
  actionFolder?: string
  actionStar: boolean
  actionMarkRead: boolean
  actionArchive: boolean
  actionTrash: boolean
  actionForward?: string
  matchCount: number
  lastMatchedAt?: string
  createdAt: string
  updatedAt: string
}

export interface FilterCondition {
  field: 'from' | 'to' | 'subject' | 'body' | 'hasAttachment'
  operator: 'contains' | 'equals' | 'startsWith' | 'endsWith' | 'regex' | 'notContains' | 'notEquals'
  value: string
}

// ============ Received Inbox API ============

export interface InboxListOptions {
  folder?: string
  domainId?: number
  isRead?: boolean
  isStarred?: boolean
  hasAttachments?: boolean
  search?: string
  sender?: string
  dateFrom?: string
  dateTo?: string
  labels?: string[]
  page?: number
  pageSize?: number
}

export const receivedInboxApi = {
  // List emails
  // If identityId is 0 or omitted, returns emails from all identities (unified inbox)
  list: (identityId: number, options: InboxListOptions = {}, signal?: AbortSignal) => {
    const params = new URLSearchParams()
    if (identityId > 0) params.set('identityId', String(identityId))
    for (const [key, value] of Object.entries(options)) {
      if (value !== undefined && value !== null && value !== '') {
        params.set(key, Array.isArray(value) ? value.join(',') : String(value))
      }
    }
    return api.get<InboxListResponse>(`/api/v1/inbox/received?${params}`, signal)
  },

  // Get single email
  get: (uuid: string, signal?: AbortSignal) => api.get<ReceivedEmail>(`/api/v1/inbox/received/${uuid}`, signal),

  // Mark emails as read/unread
  mark: (emailUuids: string[], isRead: boolean) =>
    api.post('/api/v1/inbox/received/mark', { emailUuids, isRead }),

  // Star/unstar emails
  star: (emailUuids: string[], isStarred: boolean) =>
    api.post('/api/v1/inbox/received/star', { emailUuids, isStarred }),

  // Move emails to folder
  move: (emailUuids: string[], folder: string) =>
    api.post('/api/v1/inbox/received/move', { emailUuids, folder }),

  // Trash/delete emails
  trash: (emailUuids: string[], permanent = false) =>
    api.post('/api/v1/inbox/received/trash', { emailUuids, permanent }),

  // Get folder counts (identityId = 0 for all identities)
  getCounts: (identityId: number) =>
    api.get<InboxCounts>(`/api/v1/inbox/received/counts${identityId > 0 ? `?identityId=${identityId}` : ''}`),

  // Setup email receiving for a domain
  setupReceiving: (domainId: number) =>
    api.post<{
      success: boolean
      s3Bucket: string
      snsTopicArn: string
      ruleSetName: string
      ruleName: string
      webhookUrl: string
      requiredDns?: Array<{ recordType: string; hostname: string; value: string }>
    }>('/api/v1/inbox/setup', { domainId }),

  // Durable change feed; 'now' returns the current cursor without changes.
  changes: (cursor: string, limit = 100) =>
    api.get<MailboxChanges>(`/api/v1/inbox/changes?cursor=${encodeURIComponent(cursor)}&limit=${limit}`),

  // Set identity as catch-all
  setCatchAll: (identityUuid: string, isCatchAll: boolean) =>
    api.post(`/api/v1/identities/${identityUuid}/catch-all`, { isCatchAll }),
}

// ============ Labels API ============

export const labelApi = {
  list: () => api.get<EmailLabel[]>('/api/v1/inbox/labels'),

  create: (data: { name: string; color?: string }) =>
    api.post<EmailLabel>('/api/v1/inbox/labels', data),

  update: (uuid: string, data: { name?: string; color?: string }) =>
    api.put<EmailLabel>(`/api/v1/inbox/labels/${uuid}`, data),

  delete: (uuid: string) => api.delete(`/api/v1/inbox/labels/${uuid}`),

  // Add/remove labels from emails
  apply: (emailUuids: string[], addLabels?: string[], removeLabels?: string[]) =>
    api.post('/api/v1/inbox/received/labels', { emailUuids, addLabels, removeLabels }),
}

// ============ Inbox Filters API ============

export type InboxFilterInput = Partial<Omit<InboxFilter, 'id' | 'uuid' | 'matchCount' | 'lastMatchedAt' | 'createdAt' | 'updatedAt'>>

export const inboxFiltersApi = {
  list: (kind?: InboxFilterKind) => api.get<InboxFilter[]>(`/api/v1/inbox/filters${kind ? `?kind=${kind}` : ''}`),

  create: (data: {
    kind?: InboxFilterKind
    name: string
    identityId?: number | null
    priority?: number
    conditions: FilterCondition[]
    conditionLogic?: 'all' | 'any'
    actionLabels?: string[]
    actionFolder?: string
    actionStar?: boolean
    actionMarkRead?: boolean
    actionArchive?: boolean
    actionTrash?: boolean
    actionForward?: string
  }) => api.post<InboxFilter>('/api/v1/inbox/filters', data),

  update: (uuid: string, data: InboxFilterInput) =>
    api.put<InboxFilter>(`/api/v1/inbox/filters/${uuid}`, data),

  delete: (uuid: string) => api.delete(`/api/v1/inbox/filters/${uuid}`),

  /** Blocks an address or @domain; the server returns the existing rule for duplicates. */
  blockSender: (sender: string, folder: 'spam' | 'trash' = 'spam') => {
    const value = sender.trim().toLowerCase()
    return api.post<InboxFilter>('/api/v1/inbox/filters', {
      kind: 'blocked_sender', name: `Blocked: ${value}`, priority: 0, active: true,
      conditions: [{ field: 'from', operator: value.startsWith('@') ? 'endsWith' : 'equals', value }],
      conditionLogic: 'all', actionFolder: folder, actionMarkRead: true,
    })
  },
}

// ============ Trusted senders (remote images) ============

export interface TrustedSender {
  uuid: string
  sender: string
  createdAt: string
}

export const trustedSendersApi = {
  list: () => api.get<TrustedSender[]>('/api/v1/inbox/trusted-senders'),
  add: (sender: string) => api.post<TrustedSender>('/api/v1/inbox/trusted-senders', { sender }),
  delete: (uuid: string) => api.delete(`/api/v1/inbox/trusted-senders/${uuid}`),
}

// ============ Members, invites and org identities ============

export interface OrgMember {
  uuid: string
  email: string
  name: string
  role: UserRole
  /** pending and suspended are mailbox users; disabled means removed. */
  status: 'active' | 'pending' | 'suspended' | 'disabled'
  lastLoginAt: string | null
  createdAt: string
}

export type InviteStatus = 'pending' | 'expired' | 'accepted' | 'revoked'

export interface OrgInvite {
  uuid: string
  email: string
  role: 'admin' | 'member'
  status: InviteStatus
  expiresAt: string
  invitedBy: string
  sendCount: number
  createdAt: string
}

export interface OrgIdentity {
  uuid: string
  email: string
  kind: 'personal' | 'shared'
  ownerUuid: string
  ownerEmail: string
  canSend: boolean
  canReceive: boolean
  isCatchAll: boolean
  /** The live primary identity of a mailbox user; managed on the Mailboxes page. */
  mailboxPrimary?: boolean
}

export interface InviteLookup {
  orgName: string
  email: string
  role: 'admin' | 'member' | 'mailbox'
  inviterName: string
  expiresAt: string
  purpose?: 'join' | 'mailbox_setup' | 'password_reset'
  /** The mailbox user's name, for mailbox_setup. */
  name?: string
}

export const orgApi = {
  // Mailbox users are left out unless asked for.
  members: (opts: { includeMailboxes?: boolean } = {}) => api.get<OrgMember[]>(`/api/v1/org/members${opts.includeMailboxes ? '?includeMailboxes=true' : ''}`),
  changeRole: (uuid: string, role: 'admin' | 'member') => api.put<OrgMember>(`/api/v1/org/members/${encodeURIComponent(uuid)}`, { role }),
  // Without a transfer target the member's personal identities are disabled.
  removeMember: (uuid: string, transferIdentitiesTo?: string) =>
    api.delete<{ removed: boolean; identitiesTransferred: number; identitiesDisabled: number }>(`/api/v1/org/members/${encodeURIComponent(uuid)}`, transferIdentitiesTo ? { transferIdentitiesTo } : {}),
  invites: () => api.get<OrgInvite[]>('/api/v1/org/invites'),
  invite: (data: { email: string; role: 'admin' | 'member'; senderIdentityUuid?: string }) => api.post<OrgInvite>('/api/v1/org/invites', data),
  resendInvite: (uuid: string) => api.post<OrgInvite>(`/api/v1/org/invites/${encodeURIComponent(uuid)}/resend`),
  revokeInvite: (uuid: string) => api.delete(`/api/v1/org/invites/${encodeURIComponent(uuid)}`),
  identities: () => api.get<OrgIdentity[]>('/api/v1/org/identities'),
  transferIdentity: (uuid: string, userUuid: string) => api.put<OrgIdentity>(`/api/v1/org/identities/${encodeURIComponent(uuid)}/owner`, { userUuid }),
}

// Public: the token comes from the /invite#token= link.
export const invitesApi = {
  lookup: (token: string) => api.post<InviteLookup>('/api/v1/auth/invites/lookup', { token }),
  // signedIn is false after a password reset of an account with two-factor sign-in.
  accept: (data: { token: string; name?: string; password: string }) => api.post<{ token?: string; user?: User; signedIn: boolean }>('/api/v1/auth/invites/accept', data),
}

// ============ Mailbox users (owner/admin) ============

export type MailboxStatus = 'active' | 'invited' | 'invite_expired' | 'suspended' | 'removed'

export interface MailboxAccount {
  userUuid: string
  identityUuid: string
  address: string
  name: string
  status: MailboxStatus
  maySend: boolean
  mayReceive: boolean
  wildcardSender: boolean
  aliasCount: number
  isCatchAll: boolean
  lastLoginAt: string | null
  createdAt: string
}

export interface MailboxDomainInfo { uuid: string; name: string; sesVerified: boolean; receivingEnabled: boolean }

export interface DomainMailboxes {
  domain: MailboxDomainInfo
  catchAll: MailboxCatchAllInfo | null
  mailboxes: MailboxAccount[]
  /** Receiving personal identities on the domain that can be the catch-all. */
  catchAllOptions: MailboxCatchAllInfo[]
}

export interface MailboxCatchAllInfo { identityUuid: string; email: string; ownerEmail: string; isMailbox: boolean }

export interface MailboxLink {
  uuid: string
  purpose: 'mailbox_setup' | 'password_reset'
  status: 'pending' | 'expired' | 'accepted' | 'revoked'
  expiresAt: string
  sendCount: number
}

export interface MailboxDetail {
  mailbox: MailboxAccount
  domain: MailboxDomainInfo
  overview: {
    maySend: boolean; mayReceive: boolean; wildcardSender: boolean; isCatchAll: boolean
    forwardsActive: boolean; autoReplyActive: boolean; twoFactor: boolean; recoveryEmail: string
  }
  aliases: { uuid: string; address: string }[]
  invite: MailboxLink | null
}

export type MailboxAccess =
  | { mode: 'invite'; inviteEmail: string; senderIdentityUuid?: string }
  | { mode: 'password'; password: string }

export interface MailboxImportRow { line: number; address: string; result: 'ok' | 'created' | 'error'; message?: string }

const mailboxPath = (userUuid: string) => `/api/v1/org/mailboxes/${encodeURIComponent(userUuid)}`

export const mailboxAdminApi = {
  list: (domainUuid: string, removed = false) =>
    api.get<DomainMailboxes>(`/api/v1/org/domains/${encodeURIComponent(domainUuid)}/mailboxes${removed ? '?removed=true' : ''}`),
  create: (domainUuid: string, data: { localPart: string; name: string; access: MailboxAccess; maySend?: boolean; mayReceive?: boolean }) =>
    api.post<{ mailbox: MailboxAccount; warnings: string[] }>(`/api/v1/org/domains/${encodeURIComponent(domainUuid)}/mailboxes`, data),
  // The CSV is sent as the raw body; it may hold passwords.
  importCsv: (domainUuid: string, csv: string, dryRun: boolean) =>
    api.post<{ dryRun: boolean; rows: MailboxImportRow[] }>(`/api/v1/org/domains/${encodeURIComponent(domainUuid)}/mailboxes/import?dryRun=${dryRun}`, csv, { 'Content-Type': 'text/csv' }),
  get: (userUuid: string) => api.get<MailboxDetail>(mailboxPath(userUuid)),
  update: (userUuid: string, data: { name?: string; maySend?: boolean; mayReceive?: boolean; wildcardSender?: boolean; recoveryEmail?: string }) =>
    api.put<MailboxAccount>(mailboxPath(userUuid), data),
  addAlias: (userUuid: string, localPart: string) => api.post<{ uuid: string; address: string }>(`${mailboxPath(userUuid)}/aliases`, { localPart }),
  deleteAlias: (userUuid: string, aliasUuid: string) => api.delete(`${mailboxPath(userUuid)}/aliases/${encodeURIComponent(aliasUuid)}`),
  setPassword: (userUuid: string, password: string) => api.post<{ sessionsRevoked: number }>(`${mailboxPath(userUuid)}/password`, { mode: 'set', password }),
  sendResetLink: (userUuid: string, email?: string) => api.post<{ invite: MailboxLink }>(`${mailboxPath(userUuid)}/password`, { mode: 'link', email: email || undefined }),
  resetTwoFactor: (userUuid: string) => api.post<MailboxAccount>(`${mailboxPath(userUuid)}/2fa/reset`),
  resendInvite: (userUuid: string) => api.post<{ invite: MailboxLink }>(`${mailboxPath(userUuid)}/invite/resend`),
  suspend: (userUuid: string) => api.post<MailboxAccount>(`${mailboxPath(userUuid)}/suspend`),
  reactivate: (userUuid: string) => api.post<MailboxAccount>(`${mailboxPath(userUuid)}/reactivate`),
  /** Moves the domain catch-all to one receiving identity, or removes it with ''. */
  setCatchAll: (domainUuid: string, identityUuid: string) =>
    api.put<MailboxCatchAllInfo | null>(`/api/v1/org/domains/${encodeURIComponent(domainUuid)}/catch-all`, { identityUuid }),
  remove: (userUuid: string, transferIdentitiesTo?: string) =>
    api.delete<{ removed: boolean; identitiesTransferred: number; identitiesDisabled: number }>(mailboxPath(userUuid), transferIdentitiesTo ? { transferIdentitiesTo } : {}),
}

// ============ Forwards and auto-replies ============

export type ForwardStatus = 'pending' | 'active' | 'paused' | 'suspended'

export interface EmailForward {
  uuid: string
  identityUuid: string
  identityEmail: string
  forwardTo: string
  keepCopy: boolean
  status: ForwardStatus
  active: boolean
  verified: boolean
  verifiedAt?: string
  verifyExpiresAt?: string
  lastError?: string | null
  forwardCount: number
  lastForwardedAt?: string
  createdAt: string
  updatedAt: string
}

export const forwardsApi = {
  list: () => api.get<EmailForward[]>('/api/v1/forwards'),
  create: (data: { identityUuid: string; forwardTo: string; keepCopy: boolean }) => api.post<EmailForward>('/api/v1/forwards', data),
  // Resuming works only for a verified, paused forward.
  update: (uuid: string, data: { active?: boolean; keepCopy?: boolean }) => api.put<EmailForward>(`/api/v1/forwards/${encodeURIComponent(uuid)}`, data),
  resendVerification: (uuid: string) => api.post<EmailForward>(`/api/v1/forwards/${encodeURIComponent(uuid)}/resend-verification`),
  delete: (uuid: string) => api.delete(`/api/v1/forwards/${encodeURIComponent(uuid)}`),
  // Public: opened from the verification email's /forwards/verify#id=&token= link.
  verify: (uuid: string, token: string) => api.post<{ verified: boolean }>('/api/v1/forwards/verify', { uuid, token }),
}

export interface AutoReply {
  id: number
  uuid: string
  name: string
  startDate: string
  endDate?: string
  subject: string
  htmlContent: string
  textContent?: string
  replyOnce: boolean
  replyIntervalDays: number
  excludePatterns?: string[]
  /** Empty means all of the caller's personal identities. */
  identityIds?: number[]
  active: boolean
  replyCount: number
  lastRepliedAt?: string
  lastError?: string | null
  createdAt: string
  updatedAt: string
}

export interface AutoReplyInput {
  name: string
  startDate: string
  endDate?: string
  subject: string
  htmlContent: string
  textContent?: string
  replyOnce: boolean
  replyIntervalDays: number
  excludePatterns: string[]
  identityIds: number[]
  active: boolean
}

export const autoRepliesApi = {
  list: () => api.get<AutoReply[]>('/api/v1/auto-replies'),
  create: (data: AutoReplyInput) => api.post<AutoReply>('/api/v1/auto-replies', data),
  update: (id: number, data: Partial<AutoReplyInput>) => api.put<AutoReply>(`/api/v1/auto-replies/${id}`, data),
  delete: (id: number) => api.delete(`/api/v1/auto-replies/${id}`),
}

// ============ Shared mailboxes ============

export interface SharedMailbox {
  id: number
  uuid: string
  name: string
  email: string
  description?: string
  identityUuid?: string
  /** False for legacy rows created before shared delivery existed; they deliver nothing. */
  active: boolean
  memberCount: number
  canRead: boolean
  canSend: boolean
  canManage: boolean
  createdAt: string
  updatedAt: string
}

export interface SharedMailboxMember {
  userUuid: string
  email: string
  name: string
  canRead: boolean
  canSend: boolean
  canManage: boolean
  createdAt: string
}

export type SharedMemberPermissions = { canRead: boolean; canSend: boolean; canManage: boolean }

export const sharedMailboxApi = {
  list: () => api.get<SharedMailbox[]>('/api/v1/shared-mailboxes'),
  create: (data: { name: string; email: string; description?: string }) => api.post<SharedMailbox>('/api/v1/shared-mailboxes', data),
  delete: (id: number) => api.delete(`/api/v1/shared-mailboxes/${id}`),
  members: (id: number) => api.get<SharedMailboxMember[]>(`/api/v1/shared-mailboxes/${id}/members`),
  addMember: (id: number, data: SharedMemberPermissions & { userUuid: string }) => api.post<SharedMailboxMember>(`/api/v1/shared-mailboxes/${id}/members`, data),
  updateMember: (id: number, userUuid: string, data: SharedMemberPermissions) =>
    api.put<SharedMailboxMember>(`/api/v1/shared-mailboxes/${id}/members/${encodeURIComponent(userUuid)}`, data),
  removeMember: (id: number, userUuid: string) => api.delete(`/api/v1/shared-mailboxes/${id}/members/${encodeURIComponent(userUuid)}`),
}

// ============ Web push ============

export interface PushSubscriptionInfo {
  id: number
  uuid: string
  endpoint: string
  deviceName?: string
  notifyNewEmail: boolean
  active: boolean
  lastUsedAt?: string
  createdAt: string
}

export const pushApi = {
  vapidKey: () => api.get<{ enabled: boolean; publicKey: string }>('/api/v1/push/vapid-key'),
  subscribe: (data: { endpoint: string; p256dhKey: string; authKey: string; deviceName?: string }) => api.post<PushSubscriptionInfo>('/api/v1/push/subscribe', data),
  // token: the session to use, for a sign-out that has already cleared it.
  unsubscribe: (endpoint: string, token?: string) => api.post('/api/v1/push/unsubscribe', { endpoint }, token ? { Authorization: `Bearer ${token}` } : undefined),
  list: () => api.get<PushSubscriptionInfo[] | null>('/api/v1/push/subscriptions'),
  setNewEmail: (uuid: string, notifyNewEmail: boolean) => api.put(`/api/v1/push/subscriptions/${encodeURIComponent(uuid)}/preferences`, { notifyNewEmail }),
}

// ============ SSE (Server-Sent Events) ============

// Change events carry the feed cursor after that change; a client that stores
// it resumes exactly there on reconnect.
export interface MailboxCreatedEvent { type: 'new_email'; cursor: string; uuid: string; identityId: number; summary: ReceivedEmail }
export interface MailboxUpdatedEvent { type: 'email_update'; cursor: string; uuid: string; summary: ReceivedEmail }
export interface MailboxDeletedEvent { type: 'email_deleted'; cursor: string; uuids: string[] }
export type MailboxEvent = MailboxCreatedEvent | MailboxUpdatedEvent | MailboxDeletedEvent

export interface InboxSSEHandlers {
  onConnected?: (data: { clientId: string; cursor: string }) => void
  onNewEmail?: (data: MailboxCreatedEvent) => void
  onEmailUpdate?: (data: MailboxUpdatedEvent) => void
  onEmailDeleted?: (data: MailboxDeletedEvent) => void
  onCountsUpdate?: (data: { counts: InboxCounts }) => void
  /** The cursor is unusable (expired, ahead of the server or too far behind): reload everything. */
  onResync?: (data: { cursor: string; reason: string }) => void
  onError?: (error: Event) => void
}

export class InboxSSE {
  private eventSource: EventSource | null = null
  private reconnectTimeout: number | null = null
  private reconnectDelay = 1000
  private maxReconnectDelay = 30000
  private connectionGeneration = 0

  // cursor is read on every (re)connect so a reconnect resumes from the latest
  // event the caller applied. Every connect redeems a fresh single-use ticket.
  async connect(handlers: InboxSSEHandlers, options: { cursor?: () => string | undefined } = {}) {
    this.disconnect()
    const token = api.getToken()
    if (!token) {
      console.error('Cannot connect to SSE: No auth token')
      return
    }

    const generation = this.connectionGeneration
    let ticket: { token: string }
    try {
      ticket = await authApi.streamToken()
    } catch {
      if (generation === this.connectionGeneration && api.getToken() === token) {
        handlers.onError?.(new Event('error'))
        this.scheduleReconnect(handlers, options)
      }
      return
    }
    // A late ticket from a logged-out account must never reopen its stream.
    if (generation !== this.connectionGeneration || api.getToken() !== token) return
    const baseUrl = import.meta.env.VITE_API_URL || ''
    const cursor = options.cursor?.()
    const url = `${baseUrl}/api/v1/sse/connect?token=${encodeURIComponent(ticket.token)}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`

    const source = new EventSource(url)
    this.eventSource = source
    const active = () => this.eventSource === source && api.getToken() === token
    const on = <T>(name: string, handler: ((data: T) => void) | undefined, extra?: Partial<T>) => {
      source.addEventListener(name, (event) => {
        if (!active()) return
        if (name === 'connected') this.reconnectDelay = 1000
        handler?.({ ...JSON.parse((event as MessageEvent).data).data, ...extra })
      })
    }
    on('connected', handlers.onConnected)
    on<MailboxCreatedEvent>('new_email', handlers.onNewEmail, { type: 'new_email' })
    on<MailboxUpdatedEvent>('email_update', handlers.onEmailUpdate, { type: 'email_update' })
    on<MailboxDeletedEvent>('email_deleted', handlers.onEmailDeleted, { type: 'email_deleted' })
    on('counts_update', handlers.onCountsUpdate)
    on('resync', handlers.onResync)

    source.onerror = (error) => {
      if (!active()) return
      // Own the retry loop: leaving this source open also enables the browser's
      // native retry, which would reuse the spent ticket.
      source.close()
      this.eventSource = null
      handlers.onError?.(error)
      this.scheduleReconnect(handlers, options)
    }
  }

  private scheduleReconnect(handlers: InboxSSEHandlers, options: { cursor?: () => string | undefined }) {
    if (this.reconnectTimeout) {
      clearTimeout(this.reconnectTimeout)
    }

    this.reconnectTimeout = window.setTimeout(() => {
      this.disconnect()
      this.connect(handlers, options)
      this.reconnectDelay = Math.min(this.reconnectDelay * 2, this.maxReconnectDelay)
    }, this.reconnectDelay)
  }

  disconnect() {
    this.connectionGeneration++
    if (this.eventSource) {
      this.eventSource.close()
      this.eventSource = null
    }
    if (this.reconnectTimeout) {
      clearTimeout(this.reconnectTimeout)
      this.reconnectTimeout = null
    }
  }
}

export const inboxSSE = new InboxSSE()
