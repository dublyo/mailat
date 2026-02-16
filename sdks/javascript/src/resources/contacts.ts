/**
 * Contacts resource for managing marketing contacts
 */

import type { Mailat } from '../client'
import type {
  Contact,
  CreateContactOptions,
  UpdateContactOptions,
  ContactListOptions,
  PaginatedResponse,
  ContactList
} from '../types'

export class ContactsResource {
  constructor(private client: Mailat) {}

  /**
   * Create a new contact
   *
   * @example
   * ```typescript
   * const contact = await client.contacts.create({
   *   email: 'user@example.com',
   *   firstName: 'John',
   *   lastName: 'Doe',
   *   attributes: { company: 'Acme Inc' },
   *   tags: ['vip', 'early-adopter']
   * })
   * ```
   */
  async create(options: CreateContactOptions): Promise<Contact> {
    return this.client.post<Contact>('/contacts', options)
  }

  /**
   * Get contact by ID or email
   */
  async get(idOrEmail: string): Promise<Contact> {
    return this.client.get<Contact>(`/contacts/${encodeURIComponent(idOrEmail)}`)
  }

  /**
   * Update a contact
   */
  async update(id: string, options: UpdateContactOptions): Promise<Contact> {
    return this.client.put<Contact>(`/contacts/${id}`, options)
  }

  /**
   * Delete a contact
   */
  async delete(id: string): Promise<void> {
    await this.client.delete(`/contacts/${id}`)
  }

  /**
   * List contacts with pagination and filtering
   */
  async list(options: ContactListOptions = {}): Promise<PaginatedResponse<Contact>> {
    return this.client.get<PaginatedResponse<Contact>>('/contacts', {
      page: options.page,
      limit: options.limit,
      status: options.status,
      tag: options.tag,
      search: options.search
    })
  }

  /**
   * Search contacts
   */
  async search(query: string, options: { page?: number; limit?: number } = {}): Promise<Contact[]> {
    return this.client.get<Contact[]>('/contacts/search', {
      q: query,
      page: options.page,
      limit: options.limit
    })
  }

  /**
   * Import contacts in bulk
   *
   * @example
   * ```typescript
   * const result = await client.contacts.import([
   *   { email: 'user1@example.com', firstName: 'User 1' },
   *   { email: 'user2@example.com', firstName: 'User 2' }
   * ], { listId: 'newsletter-list' })
   * ```
   */
  async import(
    contacts: CreateContactOptions[],
    options: { listId?: string; tags?: string[] } = {}
  ): Promise<{ imported: number; failed: number; errors: { email: string; error: string }[] }> {
    return this.client.post('/contacts/import', {
      contacts,
      listId: options.listId,
      tags: options.tags
    })
  }

  /**
   * Unsubscribe a contact
   */
  async unsubscribe(email: string, options: { listId?: string } = {}): Promise<Contact> {
    return this.client.post<Contact>('/contacts/unsubscribe', {
      email,
      listId: options.listId
    })
  }

  /**
   * Add tags to a contact
   */
  async addTags(id: string, tags: string[]): Promise<Contact> {
    const contact = await this.get(id)
    const existingTags = contact.tags || []
    const newTags = [...new Set([...existingTags, ...tags])]
    return this.update(id, { tags: newTags })
  }

  /**
   * Remove tags from a contact
   */
  async removeTags(id: string, tags: string[]): Promise<Contact> {
    const contact = await this.get(id)
    const existingTags = contact.tags || []
    const newTags = existingTags.filter(t => !tags.includes(t))
    return this.update(id, { tags: newTags })
  }

  /**
   * Get lists a contact belongs to
   */
  async getLists(id: string): Promise<ContactList[]> {
    return this.client.get<ContactList[]>(`/contacts/${id}/lists`)
  }

  /**
   * Get contact activity/events
   */
  async getActivity(id: string, options: { page?: number; limit?: number } = {}): Promise<{
    events: {
      type: string
      timestamp: string
      data?: Record<string, unknown>
    }[]
    total: number
  }> {
    return this.client.get(`/contacts/${id}/activity`, {
      page: options.page,
      limit: options.limit
    })
  }
}
