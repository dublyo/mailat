/**
 * Type definitions for mailat.co SDK
 */

// ============ Configuration ============

export interface MailatConfig {
  apiKey: string
  baseUrl?: string
  timeout?: number
  retries?: number
}

// ============ API Response ============

export interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export interface PaginatedResponse<T> {
  items: T[]
  total: number
  page: number
  limit: number
  hasMore: boolean
}

// ============ Email Types ============

export interface EmailAddress {
  name?: string
  email: string
}

export interface Attachment {
  filename: string
  content: string | Buffer
  contentType?: string
  contentId?: string
}

export interface SendEmailOptions {
  to: string | string[] | EmailAddress | EmailAddress[]
  cc?: string | string[] | EmailAddress | EmailAddress[]
  bcc?: string | string[] | EmailAddress | EmailAddress[]
  from?: string | EmailAddress
  replyTo?: string | EmailAddress
  subject: string
  text?: string
  html?: string
  templateId?: string
  templateData?: Record<string, unknown>
  attachments?: Attachment[]
  tags?: string[]
  metadata?: Record<string, unknown>
  headers?: Record<string, string>
  scheduledAt?: Date | string
  idempotencyKey?: string
}

export interface Email {
  id: string
  uuid: string
  messageId: string
  status: EmailStatus
  from: EmailAddress
  to: EmailAddress[]
  cc?: EmailAddress[]
  bcc?: EmailAddress[]
  subject: string
  tags?: string[]
  metadata?: Record<string, unknown>
  sentAt?: string
  deliveredAt?: string
  openedAt?: string
  clickedAt?: string
  bouncedAt?: string
  createdAt: string
}

export type EmailStatus =
  | 'queued'
  | 'sending'
  | 'sent'
  | 'delivered'
  | 'opened'
  | 'clicked'
  | 'bounced'
  | 'complained'
  | 'failed'
  | 'cancelled'

export interface EmailEvent {
  id: string
  emailId: string
  eventType: string
  timestamp: string
  data?: Record<string, unknown>
}

// ============ Contact Types ============

export interface Contact {
  id: string
  uuid: string
  email: string
  firstName?: string
  lastName?: string
  status: ContactStatus
  attributes?: Record<string, unknown>
  tags?: string[]
  engagementScore?: number
  createdAt: string
  updatedAt: string
}

export type ContactStatus = 'active' | 'unsubscribed' | 'bounced' | 'complained'

export interface CreateContactOptions {
  email: string
  firstName?: string
  lastName?: string
  attributes?: Record<string, unknown>
  tags?: string[]
  listIds?: string[]
}

export interface UpdateContactOptions {
  firstName?: string
  lastName?: string
  attributes?: Record<string, unknown>
  tags?: string[]
}

export interface ContactListOptions {
  page?: number
  limit?: number
  status?: ContactStatus
  tag?: string
  search?: string
}

// ============ List Types ============

export interface ContactList {
  id: string
  uuid: string
  name: string
  description?: string
  type: 'static' | 'dynamic'
  contactCount: number
  createdAt: string
  updatedAt: string
}

export interface CreateListOptions {
  name: string
  description?: string
  type?: 'static' | 'dynamic'
  segmentRules?: SegmentRule[]
}

export interface SegmentRule {
  field: string
  operator: 'equals' | 'not_equals' | 'contains' | 'greater_than' | 'less_than'
  value: unknown
}

// ============ Campaign Types ============

export interface Campaign {
  id: string
  uuid: string
  name: string
  subject: string
  status: CampaignStatus
  listId: string
  templateId?: string
  htmlContent?: string
  textContent?: string
  fromName: string
  fromEmail: string
  replyTo?: string
  scheduledAt?: string
  sentAt?: string
  stats: CampaignStats
  createdAt: string
  updatedAt: string
}

export type CampaignStatus =
  | 'draft'
  | 'scheduled'
  | 'sending'
  | 'sent'
  | 'paused'
  | 'cancelled'

export interface CampaignStats {
  total: number
  sent: number
  delivered: number
  opened: number
  clicked: number
  bounced: number
  unsubscribed: number
  complained: number
  openRate: number
  clickRate: number
}

export interface CreateCampaignOptions {
  name: string
  subject: string
  listIds: string[]
  fromName: string
  fromEmail: string
  replyTo?: string
  templateId?: string
  htmlContent?: string
  textContent?: string
}

export interface UpdateCampaignOptions {
  name?: string
  subject?: string
  htmlContent?: string
  textContent?: string
}

// ============ Domain Types ============

export interface Domain {
  id: string
  uuid: string
  name: string
  verified: boolean
  status: DomainStatus
  mxVerified: boolean
  spfVerified: boolean
  dkimVerified: boolean
  dmarcVerified: boolean
  dnsRecords: DnsRecord[]
  createdAt: string
  updatedAt: string
}

export type DomainStatus = 'pending' | 'active' | 'suspended'

export interface DnsRecord {
  type: 'MX' | 'TXT' | 'CNAME'
  name: string
  value: string
  verified: boolean
  lastCheckedAt?: string
}

export interface DomainVerificationResult {
  verified: boolean
  records: {
    type: string
    status: 'verified' | 'pending' | 'failed'
    expectedValue: string
    actualValue?: string
  }[]
}

// ============ Template Types ============

export interface Template {
  id: string
  uuid: string
  name: string
  description?: string
  subject: string
  htmlBody: string
  textBody?: string
  variables?: string[]
  isActive: boolean
  createdAt: string
  updatedAt: string
}

export interface CreateTemplateOptions {
  name: string
  subject: string
  htmlBody: string
  textBody?: string
  description?: string
}

export interface UpdateTemplateOptions {
  name?: string
  subject?: string
  htmlBody?: string
  textBody?: string
  description?: string
  isActive?: boolean
}

// ============ Webhook Types ============

export interface Webhook {
  id: string
  uuid: string
  name: string
  url: string
  events: WebhookEvent[]
  active: boolean
  secret?: string
  successCount: number
  failureCount: number
  lastTriggeredAt?: string
  createdAt: string
}

export type WebhookEvent =
  | 'email.sent'
  | 'email.delivered'
  | 'email.opened'
  | 'email.clicked'
  | 'email.bounced'
  | 'email.complained'
  | 'contact.created'
  | 'contact.updated'
  | 'contact.unsubscribed'
  | 'campaign.sent'
  | 'campaign.completed'

export interface CreateWebhookOptions {
  name: string
  url: string
  events: WebhookEvent[]
}

export interface UpdateWebhookOptions {
  name?: string
  url?: string
  events?: WebhookEvent[]
  active?: boolean
}

export interface WebhookCall {
  id: string
  eventType: string
  payload: Record<string, unknown>
  responseStatus?: number
  responseTimeMs?: number
  status: 'pending' | 'success' | 'failed'
  attempts: number
  error?: string
  createdAt: string
}

// ============ Identity Types ============

export interface Identity {
  id: string
  uuid: string
  email: string
  displayName?: string
  domainId: string
  isDefault: boolean
  signature?: string
  createdAt: string
}

export interface CreateIdentityOptions {
  email: string
  displayName?: string
  domainId: string
  password?: string
  signature?: string
}

// ============ Error Types ============

export class MailatError extends Error {
  public code: number
  public response?: unknown

  constructor(message: string, code: number, response?: unknown) {
    super(message)
    this.name = 'MailatError'
    this.code = code
    this.response = response
  }
}

export class AuthenticationError extends MailatError {
  constructor(message = 'Invalid API key') {
    super(message, 401)
    this.name = 'AuthenticationError'
  }
}

export class RateLimitError extends MailatError {
  public retryAfter?: number

  constructor(message = 'Rate limit exceeded', retryAfter?: number) {
    super(message, 429)
    this.name = 'RateLimitError'
    this.retryAfter = retryAfter
  }
}

export class ValidationError extends MailatError {
  public errors?: Record<string, string[]>

  constructor(message: string, errors?: Record<string, string[]>) {
    super(message, 400)
    this.name = 'ValidationError'
    this.errors = errors
  }
}
