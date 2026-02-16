/**
 * Emails resource for sending and managing transactional emails
 */

import type { Mailat } from '../client'
import type {
  Email,
  SendEmailOptions,
  EmailAddress,
  EmailEvent,
  PaginatedResponse
} from '../types'

export class EmailsResource {
  constructor(private client: Mailat) {}

  /**
   * Send a transactional email
   *
   * @example
   * ```typescript
   * const email = await client.emails.send({
   *   to: 'recipient@example.com',
   *   subject: 'Hello World',
   *   html: '<h1>Hello!</h1>'
   * })
   * ```
   */
  async send(options: SendEmailOptions): Promise<Email> {
    const payload = this.normalizeEmailOptions(options)
    return this.client.post<Email>('/emails', payload)
  }

  /**
   * Send multiple emails in batch
   *
   * @example
   * ```typescript
   * const results = await client.emails.sendBatch([
   *   { to: 'user1@example.com', subject: 'Hello', html: '<p>Hi User 1</p>' },
   *   { to: 'user2@example.com', subject: 'Hello', html: '<p>Hi User 2</p>' }
   * ])
   * ```
   */
  async sendBatch(emails: SendEmailOptions[]): Promise<{ sent: Email[]; failed: { index: number; error: string }[] }> {
    const normalizedEmails = emails.map(e => this.normalizeEmailOptions(e))
    return this.client.post('/emails/batch', { emails: normalizedEmails })
  }

  /**
   * Get email by ID
   */
  async get(id: string): Promise<Email> {
    return this.client.get<Email>(`/emails/${id}`)
  }

  /**
   * Cancel a scheduled email
   */
  async cancel(id: string): Promise<Email> {
    return this.client.delete<Email>(`/emails/${id}`)
  }

  /**
   * List recent emails
   */
  async list(options: {
    page?: number
    limit?: number
    status?: string
    tag?: string
  } = {}): Promise<PaginatedResponse<Email>> {
    return this.client.get<PaginatedResponse<Email>>('/emails', {
      page: options.page,
      limit: options.limit,
      status: options.status,
      tag: options.tag
    })
  }

  /**
   * Get email events/activity
   */
  async getEvents(emailId: string): Promise<EmailEvent[]> {
    return this.client.get<EmailEvent[]>(`/emails/${emailId}/events`)
  }

  /**
   * Send email using a template
   *
   * @example
   * ```typescript
   * const email = await client.emails.sendWithTemplate({
   *   templateId: 'welcome-email',
   *   to: 'user@example.com',
   *   templateData: {
   *     name: 'John',
   *     activationLink: 'https://example.com/activate'
   *   }
   * })
   * ```
   */
  async sendWithTemplate(options: {
    templateId: string
    to: string | string[] | EmailAddress | EmailAddress[]
    cc?: string | string[] | EmailAddress | EmailAddress[]
    bcc?: string | string[] | EmailAddress | EmailAddress[]
    templateData?: Record<string, unknown>
    tags?: string[]
    metadata?: Record<string, unknown>
  }): Promise<Email> {
    return this.send({
      ...options,
      subject: '', // Will be from template
    })
  }

  private normalizeEmailOptions(options: SendEmailOptions): Record<string, unknown> {
    return {
      to: this.normalizeAddresses(options.to),
      cc: options.cc ? this.normalizeAddresses(options.cc) : undefined,
      bcc: options.bcc ? this.normalizeAddresses(options.bcc) : undefined,
      from: options.from ? this.normalizeAddress(options.from) : undefined,
      replyTo: options.replyTo ? this.normalizeAddress(options.replyTo) : undefined,
      subject: options.subject,
      textBody: options.text,
      htmlBody: options.html,
      templateId: options.templateId,
      variables: options.templateData,
      attachments: options.attachments?.map(a => ({
        filename: a.filename,
        content: typeof a.content === 'string' ? a.content : a.content.toString('base64'),
        contentType: a.contentType,
        contentId: a.contentId
      })),
      tags: options.tags,
      metadata: options.metadata,
      headers: options.headers,
      scheduledFor: options.scheduledAt ? new Date(options.scheduledAt).toISOString() : undefined,
      idempotencyKey: options.idempotencyKey
    }
  }

  private normalizeAddresses(addresses: string | string[] | EmailAddress | EmailAddress[]): EmailAddress[] {
    if (typeof addresses === 'string') {
      return [{ email: addresses }]
    }
    if (Array.isArray(addresses)) {
      return addresses.map(a => this.normalizeAddress(a))
    }
    return [this.normalizeAddress(addresses)]
  }

  private normalizeAddress(address: string | EmailAddress): EmailAddress {
    if (typeof address === 'string') {
      return { email: address }
    }
    return address
  }
}
