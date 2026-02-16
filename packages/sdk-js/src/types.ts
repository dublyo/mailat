// Configuration
export interface MailatConfig {
  apiKey: string;
  baseUrl?: string;
  timeout?: number;
}

// Common types
export interface ApiResponse<T> {
  success: boolean;
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
}

export interface Attachment {
  filename: string;
  content: string; // Base64 encoded
  contentType: string;
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
  | 'email.sent'
  | 'email.delivered'
  | 'email.bounced'
  | 'email.complained'
  | 'email.opened'
  | 'email.clicked'
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
  verificationToken: string;
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
  domainId: number;
  email: string;
  displayName: string;
  password: string;
  quotaBytes?: number;
  isDefault?: boolean;
}

export interface Identity {
  id: number;
  uuid: string;
  email: string;
  displayName: string;
  isDefault: boolean;
  quotaBytes: number;
  usedBytes: number;
  status: string;
  createdAt: string;
  updatedAt: string;
}

// API Key types
export interface CreateApiKeyRequest {
  name: string;
  permissions?: string[];
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
  success: false;
  message: string;
  code?: string;
}

export class MailatError extends Error {
  public readonly status: number;
  public readonly code?: string;

  constructor(message: string, status: number, code?: string) {
    super(message);
    this.name = 'MailatError';
    this.status = status;
    this.code = code;
  }
}

// Webhook signature verification
export interface WebhookPayload {
  type: string;
  created_at: number;
  data: Record<string, unknown>;
}
