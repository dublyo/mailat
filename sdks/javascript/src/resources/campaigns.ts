/**
 * Campaigns resource for managing marketing campaigns
 */

import type { Mailat } from '../client'
import type {
  Campaign,
  CreateCampaignOptions,
  UpdateCampaignOptions,
  CampaignStats,
  PaginatedResponse
} from '../types'

export class CampaignsResource {
  constructor(private client: Mailat) {}

  /**
   * Create a new campaign
   *
   * @example
   * ```typescript
   * const campaign = await client.campaigns.create({
   *   name: 'Summer Newsletter',
   *   subject: 'Summer is here! Check out our deals',
   *   listIds: ['newsletter-subscribers'],
   *   fromName: 'Acme Inc',
   *   fromEmail: 'marketing@acme.com',
   *   htmlContent: '<h1>Summer Deals</h1>...'
   * })
   * ```
   */
  async create(options: CreateCampaignOptions): Promise<Campaign> {
    return this.client.post<Campaign>('/campaigns', options)
  }

  /**
   * Get campaign by ID
   */
  async get(id: string): Promise<Campaign> {
    return this.client.get<Campaign>(`/campaigns/${id}`)
  }

  /**
   * Update a campaign (only draft campaigns can be updated)
   */
  async update(id: string, options: UpdateCampaignOptions): Promise<Campaign> {
    return this.client.put<Campaign>(`/campaigns/${id}`, options)
  }

  /**
   * Delete a campaign
   */
  async delete(id: string): Promise<void> {
    await this.client.delete(`/campaigns/${id}`)
  }

  /**
   * List campaigns
   */
  async list(options: {
    page?: number
    limit?: number
    status?: string
  } = {}): Promise<PaginatedResponse<Campaign>> {
    return this.client.get<PaginatedResponse<Campaign>>('/campaigns', {
      page: options.page,
      limit: options.limit,
      status: options.status
    })
  }

  /**
   * Send campaign immediately
   */
  async send(id: string): Promise<Campaign> {
    return this.client.post<Campaign>(`/campaigns/${id}/send`)
  }

  /**
   * Schedule campaign for future sending
   *
   * @example
   * ```typescript
   * const campaign = await client.campaigns.schedule('campaign-id', {
   *   scheduledAt: new Date('2024-12-25T09:00:00Z')
   * })
   * ```
   */
  async schedule(id: string, options: { scheduledAt: Date | string }): Promise<Campaign> {
    return this.client.post<Campaign>(`/campaigns/${id}/schedule`, {
      scheduledAt: new Date(options.scheduledAt).toISOString()
    })
  }

  /**
   * Pause a sending campaign
   */
  async pause(id: string): Promise<Campaign> {
    return this.client.post<Campaign>(`/campaigns/${id}/pause`)
  }

  /**
   * Resume a paused campaign
   */
  async resume(id: string): Promise<Campaign> {
    return this.client.post<Campaign>(`/campaigns/${id}/resume`)
  }

  /**
   * Cancel a scheduled or sending campaign
   */
  async cancel(id: string): Promise<Campaign> {
    return this.client.post<Campaign>(`/campaigns/${id}/cancel`)
  }

  /**
   * Get campaign statistics
   */
  async getStats(id: string): Promise<CampaignStats> {
    return this.client.get<CampaignStats>(`/campaigns/${id}/stats`)
  }

  /**
   * Preview campaign HTML
   */
  async preview(id: string, contactId?: string): Promise<{ html: string; subject: string }> {
    return this.client.post(`/campaigns/${id}/preview`, { contactId })
  }

  /**
   * Send test email for campaign
   *
   * @example
   * ```typescript
   * await client.campaigns.sendTest('campaign-id', ['test@example.com'])
   * ```
   */
  async sendTest(id: string, emails: string[]): Promise<{ sent: number }> {
    return this.client.post(`/campaigns/${id}/test`, { emails })
  }

  /**
   * Duplicate a campaign
   */
  async duplicate(id: string, name?: string): Promise<Campaign> {
    return this.client.post<Campaign>(`/campaigns/${id}/duplicate`, { name })
  }

  /**
   * Get campaign activity feed
   */
  async getActivity(id: string, options: {
    page?: number
    limit?: number
    eventType?: string
  } = {}): Promise<{
    events: {
      type: string
      contactId: string
      email: string
      timestamp: string
      data?: Record<string, unknown>
    }[]
    total: number
  }> {
    return this.client.get(`/campaigns/${id}/activity`, {
      page: options.page,
      limit: options.limit,
      eventType: options.eventType
    })
  }
}
