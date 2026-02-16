/**
 * Main Mailat client class
 */

import { EmailsResource } from './resources/emails'
import { ContactsResource } from './resources/contacts'
import { CampaignsResource } from './resources/campaigns'
import { DomainsResource } from './resources/domains'
import { WebhooksResource } from './resources/webhooks'
import { TemplatesResource } from './resources/templates'
import type {
  MailatConfig,
  ApiResponse,
  MailatError,
  AuthenticationError,
  RateLimitError
} from './types'

const DEFAULT_BASE_URL = 'https://api.mailat.co/api/v1'
const DEFAULT_TIMEOUT = 30000
const DEFAULT_RETRIES = 3

export class Mailat {
  private apiKey: string
  private baseUrl: string
  private timeout: number
  private retries: number

  // Resources
  public emails: EmailsResource
  public contacts: ContactsResource
  public campaigns: CampaignsResource
  public domains: DomainsResource
  public webhooks: WebhooksResource
  public templates: TemplatesResource

  constructor(config: MailatConfig) {
    if (!config.apiKey) {
      throw new Error('API key is required')
    }

    this.apiKey = config.apiKey
    this.baseUrl = config.baseUrl || DEFAULT_BASE_URL
    this.timeout = config.timeout || DEFAULT_TIMEOUT
    this.retries = config.retries ?? DEFAULT_RETRIES

    // Initialize resources
    this.emails = new EmailsResource(this)
    this.contacts = new ContactsResource(this)
    this.campaigns = new CampaignsResource(this)
    this.domains = new DomainsResource(this)
    this.webhooks = new WebhooksResource(this)
    this.templates = new TemplatesResource(this)
  }

  /**
   * Make an HTTP request to the API
   */
  async request<T>(
    method: 'GET' | 'POST' | 'PUT' | 'DELETE',
    endpoint: string,
    options: {
      body?: unknown
      params?: Record<string, string | number | boolean | undefined>
      headers?: Record<string, string>
    } = {}
  ): Promise<T> {
    const url = new URL(endpoint, this.baseUrl)

    // Add query parameters
    if (options.params) {
      Object.entries(options.params).forEach(([key, value]) => {
        if (value !== undefined) {
          url.searchParams.set(key, String(value))
        }
      })
    }

    const headers: Record<string, string> = {
      'Authorization': `Bearer ${this.apiKey}`,
      'Content-Type': 'application/json',
      'User-Agent': 'mailat-js/1.0.0',
      ...options.headers
    }

    let lastError: Error | null = null

    for (let attempt = 0; attempt <= this.retries; attempt++) {
      try {
        const controller = new AbortController()
        const timeoutId = setTimeout(() => controller.abort(), this.timeout)

        const response = await fetch(url.toString(), {
          method,
          headers,
          body: options.body ? JSON.stringify(options.body) : undefined,
          signal: controller.signal
        })

        clearTimeout(timeoutId)

        // Parse response
        const data = await response.json() as ApiResponse<T>

        // Handle errors
        if (!response.ok) {
          if (response.status === 401) {
            throw new AuthenticationError(data.message || 'Invalid API key')
          }
          if (response.status === 429) {
            const retryAfter = parseInt(response.headers.get('Retry-After') || '60')
            if (attempt < this.retries) {
              await this.sleep(retryAfter * 1000)
              continue
            }
            throw new RateLimitError(data.message || 'Rate limit exceeded', retryAfter)
          }
          throw new MailatError(data.message || 'API request failed', response.status, data)
        }

        return data.data
      } catch (error) {
        lastError = error as Error

        // Don't retry on auth errors
        if (error instanceof AuthenticationError) {
          throw error
        }

        // Retry on network errors
        if (attempt < this.retries && this.isRetryableError(error as Error)) {
          await this.sleep(Math.pow(2, attempt) * 1000)
          continue
        }

        throw error
      }
    }

    throw lastError
  }

  /**
   * GET request helper
   */
  async get<T>(endpoint: string, params?: Record<string, string | number | boolean | undefined>): Promise<T> {
    return this.request<T>('GET', endpoint, { params })
  }

  /**
   * POST request helper
   */
  async post<T>(endpoint: string, body?: unknown): Promise<T> {
    return this.request<T>('POST', endpoint, { body })
  }

  /**
   * PUT request helper
   */
  async put<T>(endpoint: string, body?: unknown): Promise<T> {
    return this.request<T>('PUT', endpoint, { body })
  }

  /**
   * DELETE request helper
   */
  async delete<T>(endpoint: string): Promise<T> {
    return this.request<T>('DELETE', endpoint)
  }

  private isRetryableError(error: Error): boolean {
    return (
      error.name === 'AbortError' ||
      error.name === 'TypeError' ||
      (error instanceof RateLimitError)
    )
  }

  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms))
  }
}

// Import error types for re-export
class MailatError extends Error {
  public code: number
  public response?: unknown

  constructor(message: string, code: number, response?: unknown) {
    super(message)
    this.name = 'MailatError'
    this.code = code
    this.response = response
  }
}

class AuthenticationError extends MailatError {
  constructor(message = 'Invalid API key') {
    super(message, 401)
    this.name = 'AuthenticationError'
  }
}

class RateLimitError extends MailatError {
  public retryAfter?: number

  constructor(message = 'Rate limit exceeded', retryAfter?: number) {
    super(message, 429)
    this.name = 'RateLimitError'
    this.retryAfter = retryAfter
  }
}
