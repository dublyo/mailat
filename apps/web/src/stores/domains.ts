import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, domainApi, identityApi, receivedInboxApi, type Domain, type Identity, type CloudflareZone, type CloudflareDNSResult } from '@/lib/api'

export const useDomainsStore = defineStore('domains', () => {
  const domains = ref<Domain[]>([])
  const identities = ref<Identity[]>([])
  const isLoading = ref(false)
  const error = ref<string | null>(null)
  let generation = 0
  let ownerToken = api.getToken()

  function reset() {
    generation++
    ownerToken = api.getToken()
    domains.value = []
    identities.value = []
    isLoading.value = false
    error.value = null
  }

  function beginRequest() {
    const token = api.getToken()
    if (ownerToken !== token) reset()
    const requestGeneration = generation
    // Logout invalidates even a request whose token is later reused. Checking
    // the token also rejects old responses before the next account loads data.
    return () => requestGeneration === generation && token === api.getToken()
  }

  async function fetchDomains() {
    const isCurrent = beginRequest()
    isLoading.value = true
    error.value = null
    try {
      const result = await domainApi.list()
      if (isCurrent()) domains.value = (result ?? []).filter((d): d is Domain => d != null)
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to fetch domains'
      if (isCurrent()) domains.value = []
    } finally {
      if (isCurrent()) isLoading.value = false
    }
  }

  async function addDomain(domain: string) {
    const isCurrent = beginRequest()
    isLoading.value = true
    error.value = null
    try {
      const newDomain = await domainApi.create(domain)
      if (isCurrent()) domains.value.push(newDomain)
      return newDomain
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to add domain'
      throw e
    } finally {
      if (isCurrent()) isLoading.value = false
    }
  }

  async function verifyDomain(uuid: string) {
    const isCurrent = beginRequest()
    try {
      const updated = await domainApi.verify(uuid)
      const index = domains.value.findIndex(d => d.uuid === uuid)
      if (isCurrent() && index !== -1) {
        // Preserve dnsRecords if not returned
        domains.value[index] = {
          ...domains.value[index],
          ...updated,
          dnsRecords: updated.dnsRecords || domains.value[index].dnsRecords || []
        }
      }
      // Refresh full list to get updated data
      if (isCurrent()) await fetchDomains()
      return updated
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to verify domain'
      throw e
    }
  }

  async function deleteDomain(uuid: string) {
    const isCurrent = beginRequest()
    try {
      await domainApi.delete(uuid)
      if (isCurrent()) domains.value = domains.value.filter(d => d.uuid !== uuid)
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to delete domain'
      throw e
    }
  }

  async function fetchIdentities() {
    const isCurrent = beginRequest()
    try {
      const result = await identityApi.list()
      if (isCurrent()) identities.value = (result ?? []).filter((i): i is Identity => i != null)
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to fetch identities'
      if (isCurrent()) identities.value = []
    }
  }

  async function createIdentity(data: { displayName: string; email: string; domainId: string; password?: string; isCatchAll?: boolean }) {
    const isCurrent = beginRequest()
    try {
      const newIdentity = await identityApi.create(data)
      if (isCurrent()) identities.value.push(newIdentity)
      return newIdentity
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to create identity'
      throw e
    }
  }

  async function updateIdentity(uuid: string, data: { name?: string; signature?: string; isDefault?: boolean }) {
    const isCurrent = beginRequest()
    try {
      const updated = await identityApi.update(uuid, data)
      const index = identities.value.findIndex(i => i.uuid === uuid)
      if (isCurrent() && index !== -1) {
        identities.value[index] = updated
      }
      return updated
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to update identity'
      throw e
    }
  }

  async function deleteIdentity(uuid: string) {
    const isCurrent = beginRequest()
    try {
      await identityApi.delete(uuid)
      if (isCurrent()) identities.value = identities.value.filter(i => i.uuid !== uuid)
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to delete identity'
      throw e
    }
  }

  // SES Integration
  async function initiateSESVerification(uuid: string) {
    const isCurrent = beginRequest()
    try {
      const result = await domainApi.initiateSES(uuid)
      const index = domains.value.findIndex(d => d.uuid === uuid)
      if (isCurrent() && index !== -1) {
        domains.value[index] = {
          ...domains.value[index],
          ...result.domain,
          dnsRecords: result.dnsRecords || domains.value[index].dnsRecords || []
        }
      }
      if (isCurrent()) await fetchDomains()
      return result
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to initiate SES verification'
      throw e
    }
  }

  async function checkSESStatus(uuid: string) {
    const isCurrent = beginRequest()
    try {
      const result = await domainApi.checkSESStatus(uuid)
      const index = domains.value.findIndex(d => d.uuid === uuid)
      if (isCurrent() && index !== -1) {
        domains.value[index] = {
          ...domains.value[index],
          ...result.domain
        }
      }
      return result.sesStatus
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to check SES status'
      throw e
    }
  }

  // Cloudflare Integration
  async function getCloudflareZones(apiToken: string): Promise<CloudflareZone[]> {
    const isCurrent = beginRequest()
    try {
      return await domainApi.getCloudflareZones(apiToken)
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to fetch Cloudflare zones'
      throw e
    }
  }

  async function addDNSToCloudflare(uuid: string, apiToken: string, zoneId?: string): Promise<CloudflareDNSResult[]> {
    const isCurrent = beginRequest()
    try {
      const result = await domainApi.addDNSToCloudflare(uuid, apiToken, zoneId)
      if (isCurrent()) await fetchDomains()
      return result.results
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to add DNS to Cloudflare'
      throw e
    }
  }

  // Email Receiving Setup
  async function setupReceiving(domainId: number) {
    const isCurrent = beginRequest()
    try {
      const result = await receivedInboxApi.setupReceiving(domainId)
      if (isCurrent()) await fetchDomains()
      return result
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to setup email receiving'
      throw e
    }
  }

  // Set identity as catch-all
  async function setCatchAll(identityUuid: string, isCatchAll: boolean) {
    const isCurrent = beginRequest()
    try {
      const result = await receivedInboxApi.setCatchAll(identityUuid, isCatchAll)
      if (isCurrent()) await fetchIdentities()
      return result
    } catch (e) {
      if (isCurrent()) error.value = e instanceof Error ? e.message : 'Failed to update catch-all setting'
      throw e
    }
  }

  return {
    domains,
    identities,
    isLoading,
    error,
    reset,
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
