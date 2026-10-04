// Configuration
export interface MailatConfig {
  apiKey: string;
  baseUrl?: string;
  timeout?: number;
}

// Common types
export interface ApiResponse<T> {
  code: number;
  message?: string;
  data: T;
}

export interface PaginatedResponse<T> {
  success: boolean;
  data: T[];
  total: number;
  page: number;
  pageSize: number;
}

// Email types
export interface SendEmailRequest {
  from: string;
  to: string[];
  cc?: string[];
  bcc?: string[];
  replyTo?: string;
  subject: string;
  html?: string;
  text?: string;
  templateId?: string;
  variables?: Record<string, string>;
  attachments?: Attachment[];
  tags?: string[];
  metadata?: Record<string, string>;
  scheduledFor?: string; // RFC3339 timestamp
  idempotencyKey?: string;
}

export interface Attachment {
  name: string;
  content: string; // Base64 encoded
  type: string;
  disposition?: 'attachment' | 'inline';
  cid?: string; // Content-ID for inline attachments
}

export interface SendEmailResponse {
  id: string;
  messageId: string;
  status: EmailStatus;
  acceptedAt: string;
}

export interface BatchSendRequest {
  emails: SendEmailRequest[];
}

export interface BatchSendResponse {
  results: BatchEmailResult[];
}

export interface BatchEmailResult {
  index: number;
  id?: string;
  messageId?: string;
  status: string;
  error?: string;
}

export interface EmailStatusResponse {
  id: string;
  messageId: string;
  from: string;
  to: string[];
  subject: string;
  status: EmailStatus;
  events: DeliveryEvent[];
  createdAt: string;
  sentAt?: string;
  deliveredAt?: string;
}

export type EmailStatus =
  | 'accepted'
  | 'unknown'
  | 'complained'
  | 'queued'
  | 'sending'
  | 'sent'
  | 'delivered'
  | 'bounced'
  | 'failed'
  | 'cancelled';

export interface DeliveryEvent {
  id: number;
  emailId: number;
  eventType: string;
  timestamp: string;
  details?: string;
  ipAddress?: string;
  userAgent?: string;
}

// Template types
export interface CreateTemplateRequest {
  name: string;
  description?: string;
  subject: string;
  html: string;
  text?: string;
}

export interface UpdateTemplateRequest {
  name?: string;
  description?: string;
  subject?: string;
  html?: string;
  text?: string;
  isActive?: boolean;
}

export interface Template {
  id: number;
  uuid: string;
  name: string;
  description?: string;
  subject: string;
  htmlBody: string;
  textBody?: string;
  variables?: string[];
  isActive: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface PreviewTemplateRequest {
  variables?: Record<string, string>;
}

export interface PreviewTemplateResponse {
  subject: string;
  html: string;
  text: string;
}

// Webhook types
export interface CreateWebhookRequest {
  name: string;
  url: string;
  events: WebhookEvent[];
}

export interface UpdateWebhookRequest {
  name?: string;
  url?: string;
  events?: WebhookEvent[];
  active?: boolean;
}

export interface Webhook {
  id: number;
  uuid: string;
  name: string;
  url: string;
  events: WebhookEvent[];
  active: boolean;
  secret?: string; // Only returned on creation
  successCount: number;
  failureCount: number;
  lastTriggeredAt?: string;
  lastSuccessAt?: string;
  lastFailureAt?: string;
  createdAt: string;
  updatedAt: string;
}

export type WebhookEvent =
  | 'email.received'
  | 'email.unknown'
  | 'webhook.test'
  | 'email.sent'
  | 'email.delivered'
  | 'email.bounced'
  | 'email.complained'
  | 'email.failed';

export interface WebhookCall {
  id: number;
  eventType: string;
  payload: Record<string, unknown>;
  responseStatus?: number;
  responseBody?: string;
  responseTimeMs?: number;
  status: 'pending' | 'success' | 'failed';
  attempts: number;
  error?: string;
  createdAt: string;
  completedAt?: string;
}

export interface RotateSecretResponse {
  secret: string;
}

// Domain types
export interface CreateDomainRequest {
  name: string;
}

export interface Domain {
  id: number;
  uuid: string;
  name: string;
  status: 'pending' | 'active' | 'suspended';
  verificationToken?: string;
  emailProvider: string;
  sesVerified: boolean;
  receivingEnabled: boolean;
  dkimSelector: string;
  dkimPublicKey?: string;
  mxVerified: boolean;
  spfVerified: boolean;
  dkimVerified: boolean;
  dmarcVerified: boolean;
  verifiedAt?: string;
  createdAt: string;
  updatedAt: string;
  dnsRecords?: DnsRecord[];
}

export interface DomainDetails { domain: Domain; dnsRecords: DnsRecord[] }

export interface DnsRecord {
  id: number;
  recordType: 'MX' | 'TXT' | 'CNAME';
  hostname: string;
  value: string;
  priority?: number;
  verified: boolean;
  verifiedAt?: string;
}

// Identity types
export interface CreateIdentityRequest {
  domainId: string;
  email: string;
  displayName: string;
  password?: string;
  quotaBytes?: number;
  isDefault?: boolean;
  isCatchAll?: boolean;
}

export interface Identity {
  id: number;
  uuid: string;
  email: string;
  displayName: string;
  isDefault: boolean;
  isCatchAll: boolean;
  canSend: boolean;
  canReceive: boolean;
  domainId: number;
  quotaBytes: number;
  usedBytes: number;
  status: string;
  createdAt: string;
  updatedAt: string;
}

// API Key types
export interface CreateApiKeyRequest {
  name: string;
  permissions: string[];
  rateLimit?: number;
  expiresAt?: string;
}

export interface ApiKey {
  id: number;
  uuid: string;
  name: string;
  key?: string; // Only returned on creation
  keyPrefix: string;
  permissions: string[];
  expiresAt?: string;
  lastUsedAt?: string;
  createdAt: string;
}

// Error types
export interface ApiError {
  message: string;
  code: number;
}

export class MailatError extends Error {
  public readonly status: number;
  public readonly code?: string | number;
  public readonly retryAfter?: string;

  constructor(message: string, status: number, code?: string | number, retryAfter?: string) {
    super(message);
    this.name = 'MailatError';
    this.status = status;
    this.code = code;
    this.retryAfter = retryAfter;
  }
}

// Webhook signature verification
export interface WebhookPayload {
  version: '1';
  id: string;
  type: string;
  createdAt: string;
  data: Record<string, unknown> & { messageUuid?: string };
}

export interface WebhookTestResult { eventId: string; deliveryId: string; status: 'delivered' | 'retry' | 'dead_letter' | 'pending'; httpStatus?: number; error?: string }
/** Built-in folder key. Automatic classification does not change DNS or receiving setup. */
export const DMARC_REPORTS_FOLDER = 'dmarc-reports' as const;
export type MailboxFolder = 'inbox' | 'dmarc-reports' | 'sent' | 'drafts' | 'outbox' | 'archive' | 'spam' | 'trash';
export type InboxView = MailboxFolder | 'all' | 'starred';
export type MailboxMoveDestination = 'inbox' | 'dmarc-reports' | 'archive' | 'spam' | 'trash';
export interface InboxCounts {
  inbox: number;
  inboxUnread: number;
  dmarcReports: number;
  dmarcReportsUnread: number;
  /** Global unread total; use inboxUnread for the Inbox badge. */
  unread: number;
  starred: number;
  sent: number;
  drafts: number;
  spam: number;
  trash: number;
  labels?: Record<string, number>;
}
/** GET /settings is human-session-only; API keys cannot change this preference. */
export interface DMARCReportsSettings { autoOrganizeDmarcReports: boolean; }
/** Omit to preserve the preference; false explicitly opts out of future sorting. */
export interface UpdateDMARCReportsSettings { autoOrganizeDmarcReports?: boolean; }
