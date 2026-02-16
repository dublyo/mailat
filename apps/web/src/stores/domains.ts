import { defineStore } from 'pinia'
import { ref } from 'vue'
import { domainApi, identityApi, receivedInboxApi, type Domain, type Identity, type CloudflareZone, type CloudflareDNSResult } from '@/lib/api'

export const useDomainsStore = defineStore('domains', () => {
  const domains = ref<Domain[]>([])
  const identities = ref<Identity[]>([])
  const isLoading = ref(false)
  const error = ref<string | null>(null)

  async function fetchDomains() {
    isLoading.value = true
    error.value = null
    try {
      const result = await domainApi.list()
      domains.value = (result ?? []).filter((d): d is Domain => d != null)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch domains'
      domains.value = []
    } finally {
      isLoading.value = false
    }
  }

  async function addDomain(domain: string) {
    isLoading.value = true
    error.value = null
    try {
      const newDomain = await domainApi.create(domain)
      domains.value.push(newDomain)
      return newDomain
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to add domain'
      throw e
    } finally {
      isLoading.value = false
    }
  }

  async function verifyDomain(uuid: string) {
    try {
      const updated = await domainApi.verify(uuid)
      const index = domains.value.findIndex(d => d.uuid === uuid)
      if (index !== -1) {
        // Preserve dnsRecords if not returned
        domains.value[index] = {
          ...domains.value[index],
          ...updated,
          dnsRecords: updated.dnsRecords || domains.value[index].dnsRecords || []
        }
      }
      // Refresh full list to get updated data
      await fetchDomains()
      return updated
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to verify domain'
      throw e
    }
  }

  async function deleteDomain(uuid: string) {
    try {
      await domainApi.delete(uuid)
      domains.value = domains.value.filter(d => d.uuid !== uuid)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to delete domain'
      throw e
    }
  }

  async function fetchIdentities() {
    try {
      const result = await identityApi.list()
      identities.value = (result ?? []).filter((i): i is Identity => i != null)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch identities'
      identities.value = []
    }
  }

  async function createIdentity(data: { displayName: string; email: string; domainId: string; password: string; isCatchAll?: boolean }) {
    try {
      const newIdentity = await identityApi.create(data)
      identities.value.push(newIdentity)
      return newIdentity
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to create identity'
      throw e
    }
  }

  async function updateIdentity(uuid: string, data: { name?: string; signature?: string; isDefault?: boolean }) {
    try {
      const updated = await identityApi.update(uuid, data)
      const index = identities.value.findIndex(i => i.uuid === uuid)
      if (index !== -1) {
        identities.value[index] = updated
      }
      return updated
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to update identity'
      throw e
    }
  }

  async function deleteIdentity(uuid: string) {
    try {
      await identityApi.delete(uuid)
      identities.value = identities.value.filter(i => i.uuid !== uuid)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to delete identity'
      throw e
    }
  }

  // SES Integration
  async function initiateSESVerification(uuid: string) {
    try {
      const result = await domainApi.initiateSES(uuid)
      const index = domains.value.findIndex(d => d.uuid === uuid)
      if (index !== -1) {
        domains.value[index] = {
          ...domains.value[index],
          ...result.domain,
          dnsRecords: result.dnsRecords || domains.value[index].dnsRecords || []
        }
      }
      await fetchDomains()
      return result
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to initiate SES verification'
      throw e
    }
  }

  async function checkSESStatus(uuid: string) {
    try {
      const result = await domainApi.checkSESStatus(uuid)
      const index = domains.value.findIndex(d => d.uuid === uuid)
      if (index !== -1) {
        domains.value[index] = {
          ...domains.value[index],
          ...result.domain
        }
      }
      return result.sesStatus
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to check SES status'
      throw e
    }
  }

  // Cloudflare Integration
  async function getCloudflareZones(apiToken: string): Promise<CloudflareZone[]> {
    try {
      return await domainApi.getCloudflareZones(apiToken)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch Cloudflare zones'
      throw e
    }
  }

  async function addDNSToCloudflare(uuid: string, apiToken: string, zoneId?: string): Promise<CloudflareDNSResult[]> {
    try {
      const result = await domainApi.addDNSToCloudflare(uuid, apiToken, zoneId)
      await fetchDomains()
      return result.results
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to add DNS to Cloudflare'
      throw e
    }
  }

  // Email Receiving Setup
  async function setupReceiving(domainId: number) {
    try {
      const result = await receivedInboxApi.setupReceiving(domainId)
      await fetchDomains()
      return result
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to setup email receiving'
      throw e
    }
  }

  // Set identity as catch-all
  async function setCatchAll(identityUuid: string, isCatchAll: boolean) {
    try {
      const result = await receivedInboxApi.setCatchAll(identityUuid, isCatchAll)
      await fetchIdentities()
      return result
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to update catch-all setting'
      throw e
    }
  }

  return {
    domains,
    identities,
    isLoading,
    error,
    fetchDomains,
    addDomain,
    verifyDomain,
    deleteDomain,
    fetchIdentities,
    createIdentity,
    updateIdentity,
    deleteIdentity,
    initiateSESVerification,
    checkSESStatus,
    getCloudflareZones,
    addDNSToCloudflare,
    setupReceiving,
    setCatchAll
  }
})
