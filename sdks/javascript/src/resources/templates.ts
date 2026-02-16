/**
 * Templates resource for managing email templates
 */

import type { Mailat } from '../client'
import type { Template, CreateTemplateOptions, UpdateTemplateOptions } from '../types'

export class TemplatesResource {
  constructor(private client: Mailat) {}

  /**
   * Create a new template
   *
   * @example
   * ```typescript
   * const template = await client.templates.create({
   *   name: 'Welcome Email',
   *   subject: 'Welcome to {{company_name}}, {{first_name}}!',
   *   htmlBody: '<h1>Welcome {{first_name}}!</h1><p>Thanks for joining us.</p>'
   * })
   * ```
   */
  async create(options: CreateTemplateOptions): Promise<Template> {
    return this.client.post<Template>('/templates', options)
  }

  /**
   * Get template by ID
   */
  async get(id: string): Promise<Template> {
    return this.client.get<Template>(`/templates/${id}`)
  }

  /**
   * Update a template
   */
  async update(id: string, options: UpdateTemplateOptions): Promise<Template> {
    return this.client.put<Template>(`/templates/${id}`, options)
  }

  /**
   * Delete a template
   */
  async delete(id: string): Promise<void> {
    await this.client.delete(`/templates/${id}`)
  }

  /**
   * List all templates
   */
  async list(options: {
    page?: number
    limit?: number
    search?: string
  } = {}): Promise<Template[]> {
    return this.client.get<Template[]>('/templates', {
      page: options.page,
      limit: options.limit,
      search: options.search
    })
  }

  /**
   * Preview template with sample data
   *
   * @example
   * ```typescript
   * const preview = await client.templates.preview('template-id', {
   *   first_name: 'John',
   *   company_name: 'Acme Inc'
   * })
   * console.log(preview.html) // Rendered HTML
   * console.log(preview.subject) // Rendered subject
   * ```
   */
  async preview(id: string, variables?: Record<string, unknown>): Promise<{
    html: string
    text?: string
    subject: string
  }> {
    return this.client.post(`/templates/${id}/preview`, { variables })
  }

  /**
   * Get template variables
   * Returns list of variable names used in the template
   */
  async getVariables(id: string): Promise<string[]> {
    const template = await this.get(id)
    return template.variables || []
  }

  /**
   * Validate template syntax
   * Checks for valid Handlebars/Mustache syntax
   */
  async validate(options: {
    htmlBody: string
    subject?: string
    textBody?: string
  }): Promise<{
    valid: boolean
    errors?: {
      field: string
      message: string
      line?: number
    }[]
    variables: string[]
  }> {
    return this.client.post('/templates/validate', options)
  }

  /**
   * Duplicate a template
   */
  async duplicate(id: string, name?: string): Promise<Template> {
    return this.client.post<Template>(`/templates/${id}/duplicate`, { name })
  }

  /**
   * Enable a template
   */
  async enable(id: string): Promise<Template> {
    return this.update(id, { isActive: true })
  }

  /**
   * Disable a template
   */
  async disable(id: string): Promise<Template> {
    return this.update(id, { isActive: false })
  }

  /**
   * Extract variables from template content
   * Utility function that parses Handlebars/Mustache variables
   */
  extractVariables(content: string): string[] {
    const regex = /\{\{([^}]+)\}\}/g
    const variables = new Set<string>()
    let match

    while ((match = regex.exec(content)) !== null) {
      // Clean up variable name (remove #, /, ^, etc.)
      const variable = match[1].trim().replace(/^[#/^]/, '').split(' ')[0]
      if (variable && !variable.startsWith('!')) {
        variables.add(variable)
      }
    }

    return Array.from(variables)
  }
}
