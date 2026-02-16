/**
 * Domains resource for managing email domains
 */

import type { Mailat } from '../client'
import type { Domain, DomainVerificationResult } from '../types'

export class DomainsResource {
  constructor(private client: Mailat) {}

  /**
   * Add a new domain
   *
   * @example
   * ```typescript
   * const domain = await client.domains.create('example.com')
   * console.log(domain.dnsRecords) // DNS records to add
   * ```
   */
  async create(domain: string): Promise<Domain> {
    return this.client.post<Domain>('/domains', { domain })
  }

  /**
   * Get domain by ID
   */
  async get(id: string): Promise<Domain> {
    return this.client.get<Domain>(`/domains/${id}`)
  }

  /**
   * Delete a domain
   */
  async delete(id: string): Promise<void> {
    await this.client.delete(`/domains/${id}`)
  }

  /**
   * List all domains
   */
  async list(): Promise<Domain[]> {
    return this.client.get<Domain[]>('/domains')
  }

  /**
   * Verify domain DNS records
   *
   * @example
   * ```typescript
   * const result = await client.domains.verify('domain-id')
   * if (result.verified) {
   *   console.log('Domain is verified!')
   * } else {
   *   console.log('Missing records:', result.records.filter(r => !r.verified))
   * }
   * ```
   */
  async verify(id: string): Promise<DomainVerificationResult> {
    return this.client.post<DomainVerificationResult>(`/domains/${id}/verify`)
  }

  /**
   * Get DNS records that need to be configured
   */
  async getDnsRecords(id: string): Promise<{
    records: {
      type: string
      name: string
      value: string
      verified: boolean
      priority?: number
    }[]
  }> {
    const domain = await this.get(id)
    return { records: domain.dnsRecords }
  }

  /**
   * Check if a domain is fully verified
   */
  async isVerified(id: string): Promise<boolean> {
    const domain = await this.get(id)
    return domain.verified && domain.mxVerified && domain.spfVerified && domain.dkimVerified
  }

  /**
   * Get verification status breakdown
   */
  async getVerificationStatus(id: string): Promise<{
    domain: string
    verified: boolean
    mx: boolean
    spf: boolean
    dkim: boolean
    dmarc: boolean
  }> {
    const domain = await this.get(id)
    return {
      domain: domain.name,
      verified: domain.verified,
      mx: domain.mxVerified,
      spf: domain.spfVerified,
      dkim: domain.dkimVerified,
      dmarc: domain.dmarcVerified
    }
  }
}
