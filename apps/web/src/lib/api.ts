import axios, { type AxiosInstance, type AxiosResponse } from 'axios'

const API_BASE = import.meta.env.VITE_API_URL || ''

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
        if (error.response?.status === 401) {
          this.setToken(null)
          window.location.href = '/login'
        }
        return Promise.reject(error)
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

  async get<T>(endpoint: string): Promise<T> {
    const response: AxiosResponse<ApiResponse<T>> = await this.client.get(endpoint)
    return response.data.data
  }

  async post<T>(endpoint: string, body?: unknown): Promise<T> {
    const response: AxiosResponse<ApiResponse<T>> = await this.client.post(endpoint, body)
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
  role: string
  createdAt: string
}

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
  // For received emails - identity that received/will send the email
  identityId?: number
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
  signature?: string
  createdAt: string
  updatedAt?: string
}

export interface Campaign {
  id: string
  uuid: string
  name: string
  subject: string
  status: 'draft' | 'scheduled' | 'sending' | 'sent' | 'paused' | 'cancelled'
  scheduledAt?: string
  sentAt?: string
  completedAt?: string
  fromIdentityId?: number | null
  replyTo?: string
  listIds?: string[]
  htmlBody?: string
  textBody?: string
  recipientCount?: number
  stats: CampaignStats
  createdAt: string
}

export interface CampaignStats {
  total: number
  sent: number
  delivered: number
  opened: number
  clicked: number
  bounced: number
  unsubscribed: number
  complained?: number
}

// ============ Auth API ============

export const authApi = {
  login: (email: string, password: string) =>
    api.post<{ token: string; user: User }>('/api/v1/auth/login', { email, password }),

  register: (data: { email: string; password: string; name: string }) =>
    api.post<{ token: string; user: User }>('/api/v1/auth/register', data),

  me: () => api.get<User>('/api/v1/auth/me'),
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

export const composeApi = {
  send: (data: {
    identityId: string | number
    to: string[]
    cc?: string[]
    bcc?: string[]
    subject: string
    body: string
    htmlBody?: string
    replyTo?: string
  }) => {
    // Transform to backend format
    const payload = {
      identityId: typeof data.identityId === 'string' ? parseInt(data.identityId, 10) : data.identityId,
      to: data.to.map(email => ({ name: '', email })),
      cc: data.cc?.map(email => ({ name: '', email })),
      bcc: data.bcc?.map(email => ({ name: '', email })),
      subject: data.subject,
      textBody: data.body,
      htmlBody: data.htmlBody,
      inReplyTo: data.replyTo
    }
    return api.post<Email>('/api/v1/compose/send', payload)
  },

  saveDraft: (data: {
    identityId?: string
    to?: string[]
    subject?: string
    body?: string
  }) => api.post<Email>('/api/v1/compose/draft', data),

  reply: (uuid: string, data: { body: string; replyAll?: boolean }) =>
    api.post<Email>(`/api/v1/compose/${uuid}/reply`, data),

  forward: (uuid: string, data: { to: string[]; body?: string }) =>
    api.post<Email>(`/api/v1/compose/${uuid}/forward`, data),
}

// ============ Domains API ============

export interface CloudflareZone {
  id: string
  name: string
  status: string
}

export interface CloudflareDNSResult {
  hostname: string
  type: string
  value: string
  success: boolean
  error?: string
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

  addDNSToCloudflare: (uuid: string, apiToken: string, zoneId?: string) =>
    api.post<{ results: CloudflareDNSResult[] }>(`/api/v1/domains/${uuid}/dns/cloudflare`, { apiToken, zoneId }),
}

// ============ Identity API ============

export const identityApi = {
  list: () => api.get<Identity[]>('/api/v1/identities'),

  create: (data: { displayName: string; email: string; domainId: string; password: string; isCatchAll?: boolean }) =>
    api.post<Identity>('/api/v1/identities', data),

  update: (uuid: string, data: { displayName?: string; isDefault?: boolean; isCatchAll?: boolean }) =>
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

  search: (query: string) =>
    api.get<Contact[]>(`/api/v1/contacts/search?q=${encodeURIComponent(query)}`),

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
  errors?: string[]
}

export const listApi = {
  list: () => api.get<ContactList[]>('/api/v1/lists'),

  create: (data: { name: string; description?: string }) =>
    api.post<ContactList>('/api/v1/lists', data),

  get: (uuid: string) => api.get<ContactList>(`/api/v1/lists/${uuid}`),

  update: (uuid: string, data: { name?: string; description?: string }) =>
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
  list: () => api.get<CampaignListResponse>('/api/v1/campaigns'),

  create: (data: {
    name: string
    subject: string
    htmlContent: string
    textContent?: string
    fromName: string
    fromEmail: string
    listId: number
    replyTo?: string
    templateId?: number
  }) => api.post<Campaign>('/api/v1/campaigns', data),

  get: (uuid: string) => api.get<Campaign>(`/api/v1/campaigns/${uuid}`),

  update: (uuid: string, data: Partial<Campaign>) =>
    api.put<Campaign>(`/api/v1/campaigns/${uuid}`, data),

  delete: (uuid: string) => api.delete(`/api/v1/campaigns/${uuid}`),

  schedule: (uuid: string, scheduledAt: string) =>
    api.post(`/api/v1/campaigns/${uuid}/schedule`, { scheduledAt }),

  send: (uuid: string) => api.post(`/api/v1/campaigns/${uuid}/send`),

  pause: (uuid: string) => api.post(`/api/v1/campaigns/${uuid}/pause`),

  resume: (uuid: string) => api.post(`/api/v1/campaigns/${uuid}/resume`),

  getStats: (uuid: string) => api.get<CampaignStats>(`/api/v1/campaigns/${uuid}/stats`),
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
  receivedToday: number
  spamBlocked: number
  virusBlocked: number
  avgDailyReceived: number
}

export interface DomainAuthStatus {
  domain: string
  spfVerified: boolean
  dkimVerified: boolean
  dmarcVerified: boolean
}

export interface AuthenticationStatus {
  domains: DomainAuthStatus[]
  allDomainsVerified: boolean
}

export interface HealthWarning {
  type: string
  severity: 'low' | 'medium' | 'high' | 'critical'
  message: string
  recommendation: string
}

export interface ReputationMetrics {
  score: number
  totalSent: number
  delivered: number
  bounced: number
  complained: number
  failed: number
  deliveryRate: number
  bounceRate: number
  complaintRate: number
  period: string
}

export interface EmailHealthSummary {
  sendingMetrics: ReputationMetrics
  sesLimits?: SESAccountLimits
  receivingMetrics: ReceivingMetrics
  authStatus: AuthenticationStatus
  warnings: HealthWarning[]
  healthScore: number
  healthStatus: 'excellent' | 'good' | 'fair' | 'poor' | 'critical'
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
  enable2FA: (method: string) =>
    api.post<{ secret?: string; qrCode?: string }>('/api/v1/auth/2fa/enable', { method }),

  verify2FA: (code: string) => api.post('/api/v1/auth/2fa/verify', { code }),

  disable2FA: (code: string) => api.post('/api/v1/auth/2fa/disable', { code }),

  // AWS Setup
  validateAWSCredentials: (data: { region: string; accessKeyId: string; secretAccessKey: string }) =>
    api.post<{ valid: boolean; message: string; error?: string }>('/api/v1/settings/aws/validate', data),

  provisionAWS: (data: { region: string; accessKeyId: string; secretAccessKey: string }) =>
    api.post<AWSProvisioningResult>('/api/v1/settings/aws/provision', data),

  getAWSStatus: () =>
    api.get<AWSProvisioningStatus>('/api/v1/settings/aws/status'),
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

export interface AWSProvisioningResult {
  success: boolean
  resources?: {
    s3BucketName: string
    s3BucketArn: string
    lambdaFunctionArn: string
    lambdaRoleArn: string
    snsTopicArn: string
    receiptRuleSetName: string
    region: string
  }
  nextSteps?: string[]
  error?: string
  warning?: string
}

export interface AWSProvisioningStatus {
  provisioned: boolean
  resources?: {
    region: string
    s3Bucket: string
    lambdaArn: string
    snsTopicArn: string
    receiptRuleSet: string
  }
  message?: string
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

  test: (uuid: string) => api.post(`/api/v1/webhooks/${uuid}/test`),
}

// ============ Received Inbox Types ============

export interface ReceivedEmail {
  id: number
  uuid: string
  orgId: number
  domainId: number
  identityId: number
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
  unread: number
  starred: number
  sent: number
  drafts: number
  spam: number
  trash: number
  labels?: Record<string, number>
}

export interface EmailLabel {
  id: number
  uuid: string
  name: string
  color: string
  createdAt: string
  updatedAt: string
}

export interface InboxFilter {
  id: number
  uuid: string
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
  operator: 'contains' | 'equals' | 'startsWith' | 'endsWith' | 'regex'
  value: string
}

// ============ Received Inbox API ============

export const receivedInboxApi = {
  // List emails
  // If identityId is 0 or omitted, returns emails from all identities (unified inbox)
  list: (identityId: number, options?: {
    folder?: string
    isRead?: boolean
    isStarred?: boolean
    search?: string
    labels?: string[]
    page?: number
    pageSize?: number
    sortBy?: string
    sortOrder?: 'asc' | 'desc'
  }) => {
    const params = new URLSearchParams()
    // Only set identityId if > 0, otherwise API returns all identities
    if (identityId > 0) {
      params.set('identityId', identityId.toString())
    }
    if (options?.folder) params.set('folder', options.folder)
    if (options?.isRead !== undefined) params.set('isRead', options.isRead.toString())
    if (options?.isStarred !== undefined) params.set('isStarred', options.isStarred.toString())
    if (options?.search) params.set('search', options.search)
    if (options?.labels?.length) params.set('labels', options.labels.join(','))
    if (options?.page) params.set('page', options.page.toString())
    if (options?.pageSize) params.set('pageSize', options.pageSize.toString())
    if (options?.sortBy) params.set('sortBy', options.sortBy)
    if (options?.sortOrder) params.set('sortOrder', options.sortOrder)
    return api.get<InboxListResponse>(`/api/v1/inbox/received?${params}`)
  },

  // Get single email
  get: (uuid: string) => api.get<ReceivedEmail>(`/api/v1/inbox/received/${uuid}`),

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

export const filterApi = {
  list: () => api.get<InboxFilter[]>('/api/v1/inbox/filters'),

  create: (data: {
    name: string
    identityId?: number
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

  update: (uuid: string, data: Partial<InboxFilter>) =>
    api.put<InboxFilter>(`/api/v1/inbox/filters/${uuid}`, data),

  delete: (uuid: string) => api.delete(`/api/v1/inbox/filters/${uuid}`),
}

// ============ SSE (Server-Sent Events) ============

export class InboxSSE {
  private eventSource: EventSource | null = null
  private reconnectTimeout: number | null = null
  private reconnectDelay = 1000
  private maxReconnectDelay = 30000

  connect(handlers: {
    onNewEmail?: (data: ReceivedEmail) => void
    onEmailUpdate?: (data: { uuid: string; updates: Record<string, unknown> }) => void
    onEmailDeleted?: (data: { uuids: string[] }) => void
    onCountsUpdate?: (data: InboxCounts & { identityId: number }) => void
    onConnected?: (data: { clientId: string }) => void
    onError?: (error: Event) => void
  }) {
    const token = api.getToken()
    if (!token) {
      console.error('Cannot connect to SSE: No auth token')
      return
    }

    const baseUrl = import.meta.env.VITE_API_URL || ''
    const url = `${baseUrl}/api/v1/sse/connect?token=${encodeURIComponent(token)}`

    this.eventSource = new EventSource(url)

    this.eventSource.addEventListener('connected', (event) => {
      this.reconnectDelay = 1000
      const data = JSON.parse(event.data)
      handlers.onConnected?.(data.data)
    })

    this.eventSource.addEventListener('new_email', (event) => {
      const data = JSON.parse(event.data)
      handlers.onNewEmail?.(data.data)
    })

    this.eventSource.addEventListener('email_update', (event) => {
      const data = JSON.parse(event.data)
      handlers.onEmailUpdate?.(data.data)
    })

    this.eventSource.addEventListener('email_deleted', (event) => {
      const data = JSON.parse(event.data)
      handlers.onEmailDeleted?.(data.data)
    })

    this.eventSource.addEventListener('counts_update', (event) => {
      const data = JSON.parse(event.data)
      handlers.onCountsUpdate?.({ ...data.data, identityId: data.identityId })
    })

    this.eventSource.onerror = (error) => {
      handlers.onError?.(error)
      this.scheduleReconnect(handlers)
    }
  }

  private scheduleReconnect(handlers: Parameters<typeof this.connect>[0]) {
    if (this.reconnectTimeout) {
      clearTimeout(this.reconnectTimeout)
    }

    this.reconnectTimeout = window.setTimeout(() => {
      console.log(`Reconnecting to SSE in ${this.reconnectDelay}ms...`)
      this.disconnect()
      this.connect(handlers)
      this.reconnectDelay = Math.min(this.reconnectDelay * 2, this.maxReconnectDelay)
    }, this.reconnectDelay)
  }

  disconnect() {
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
