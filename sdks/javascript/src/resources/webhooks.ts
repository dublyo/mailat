/**
 * Webhooks resource for managing webhook endpoints
 */

import type { Mailat } from '../client'
import type {
  Webhook,
  CreateWebhookOptions,
  UpdateWebhookOptions,
  WebhookCall,
  WebhookEvent
} from '../types'

export class WebhooksResource {
  constructor(private client: Mailat) {}

  /**
   * Create a new webhook
   *
   * @example
   * ```typescript
   * const webhook = await client.webhooks.create({
   *   name: 'My Webhook',
   *   url: 'https://example.com/webhook',
   *   events: ['email.delivered', 'email.bounced']
   * })
   * ```
   */
  async create(options: CreateWebhookOptions): Promise<Webhook> {
    return this.client.post<Webhook>('/webhooks', options)
  }

  /**
   * Get webhook by ID
   */
  async get(id: string): Promise<Webhook> {
    return this.client.get<Webhook>(`/webhooks/${id}`)
  }

  /**
   * Update a webhook
   */
  async update(id: string, options: UpdateWebhookOptions): Promise<Webhook> {
    return this.client.put<Webhook>(`/webhooks/${id}`, options)
  }

  /**
   * Delete a webhook
   */
  async delete(id: string): Promise<void> {
    await this.client.delete(`/webhooks/${id}`)
  }

  /**
   * List all webhooks
   */
  async list(): Promise<Webhook[]> {
    return this.client.get<Webhook[]>('/webhooks')
  }

  /**
   * Enable a webhook
   */
  async enable(id: string): Promise<Webhook> {
    return this.update(id, { active: true })
  }

  /**
   * Disable a webhook
   */
  async disable(id: string): Promise<Webhook> {
    return this.update(id, { active: false })
  }

  /**
   * Rotate webhook secret
   * Returns the new secret (shown only once)
   */
  async rotateSecret(id: string): Promise<{ secret: string }> {
    return this.client.post(`/webhooks/${id}/rotate-secret`)
  }

  /**
   * Test a webhook by sending a test payload
   */
  async test(id: string): Promise<{
    success: boolean
    statusCode?: number
    responseTime?: number
    error?: string
  }> {
    return this.client.post(`/webhooks/${id}/test`)
  }

  /**
   * Get recent webhook calls/deliveries
   */
  async getCalls(id: string, options: {
    page?: number
    limit?: number
    status?: 'success' | 'failed' | 'pending'
  } = {}): Promise<WebhookCall[]> {
    return this.client.get<WebhookCall[]>(`/webhooks/${id}/calls`, {
      page: options.page,
      limit: options.limit,
      status: options.status
    })
  }

  /**
   * Get available webhook event types
   */
  getEventTypes(): WebhookEvent[] {
    return [
      'email.sent',
      'email.delivered',
      'email.opened',
      'email.clicked',
      'email.bounced',
      'email.complained',
      'contact.created',
      'contact.updated',
      'contact.unsubscribed',
      'campaign.sent',
      'campaign.completed'
    ]
  }

  /**
   * Verify webhook signature (for receiving webhooks)
   *
   * @example
   * ```typescript
   * const isValid = client.webhooks.verifySignature(
   *   requestBody,
   *   request.headers['x-webhook-signature'],
   *   webhookSecret
   * )
   * ```
   */
  verifySignature(payload: string | Buffer, signature: string, secret: string): boolean {
    // Use Web Crypto API or Node.js crypto
    if (typeof globalThis.crypto !== 'undefined' && globalThis.crypto.subtle) {
      // Browser environment - signature verification should be done server-side
      console.warn('Webhook signature verification should be done server-side')
      return false
    }

    // Node.js environment
    try {
      const crypto = require('crypto')
      const expectedSignature = crypto
        .createHmac('sha256', secret)
        .update(payload)
        .digest('hex')

      const providedSignature = signature.replace('sha256=', '')
      return crypto.timingSafeEqual(
        Buffer.from(expectedSignature),
        Buffer.from(providedSignature)
      )
    } catch {
      return false
    }
  }
}
