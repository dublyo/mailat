import { defineStore } from 'pinia'
import { ref } from 'vue'
import { campaignApi, type Campaign, type CampaignStats } from '@/lib/api'

export const useCampaignsStore = defineStore('campaigns', () => {
  const campaigns = ref<Campaign[]>([])
  const activeCampaign = ref<Campaign | null>(null)
  const isLoading = ref(false)
  const error = ref<string | null>(null)

  async function fetchCampaigns() {
    isLoading.value = true
    error.value = null
    try {
      const result = await campaignApi.list()
      // API returns { campaigns: [...], total, page, ... }
      campaigns.value = (result?.campaigns ?? []).filter((c): c is Campaign => c != null)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch campaigns'
      campaigns.value = []
    } finally {
      isLoading.value = false
    }
  }

  async function getCampaign(uuid: string) {
    try {
      activeCampaign.value = await campaignApi.get(uuid)
      return activeCampaign.value
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch campaign'
      throw e
    }
  }

  async function createCampaign(data: {
    name: string
    subject: string
    htmlContent: string
    textContent?: string
    fromName: string
    fromEmail: string
    listId: number
    replyTo?: string
  }) {
    try {
      const newCampaign = await campaignApi.create(data)
      campaigns.value.unshift(newCampaign)
      return newCampaign
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to create campaign'
      throw e
    }
  }

  async function updateCampaign(uuid: string, data: Partial<Campaign>) {
    try {
      const updated = await campaignApi.update(uuid, data)
      const index = campaigns.value.findIndex(c => c.uuid === uuid)
      if (index !== -1) {
        campaigns.value[index] = updated
      }
      if (activeCampaign.value?.uuid === uuid) {
        activeCampaign.value = updated
      }
      return updated
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to update campaign'
      throw e
    }
  }

  async function deleteCampaign(uuid: string) {
    try {
      await campaignApi.delete(uuid)
      campaigns.value = campaigns.value.filter(c => c.uuid !== uuid)
      if (activeCampaign.value?.uuid === uuid) {
        activeCampaign.value = null
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to delete campaign'
      throw e
    }
  }

  async function scheduleCampaign(uuid: string, scheduledAt: string) {
    try {
      await campaignApi.schedule(uuid, scheduledAt)
      const index = campaigns.value.findIndex(c => c.uuid === uuid)
      if (index !== -1) {
        campaigns.value[index] = { ...campaigns.value[index], status: 'scheduled', scheduledAt }
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to schedule campaign'
      throw e
    }
  }

  async function sendCampaign(uuid: string) {
    try {
      await campaignApi.send(uuid)
      const index = campaigns.value.findIndex(c => c.uuid === uuid)
      if (index !== -1) {
        campaigns.value[index] = { ...campaigns.value[index], status: 'sending' }
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to send campaign'
      throw e
    }
  }

  async function pauseCampaign(uuid: string) {
    try {
      await campaignApi.pause(uuid)
      const index = campaigns.value.findIndex(c => c.uuid === uuid)
      if (index !== -1) {
        campaigns.value[index] = { ...campaigns.value[index], status: 'paused' }
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to pause campaign'
      throw e
    }
  }

  async function resumeCampaign(uuid: string) {
    try {
      await campaignApi.resume(uuid)
      const index = campaigns.value.findIndex(c => c.uuid === uuid)
      if (index !== -1) {
        campaigns.value[index] = { ...campaigns.value[index], status: 'sending' }
      }
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to resume campaign'
      throw e
    }
  }

  async function getCampaignStats(uuid: string): Promise<CampaignStats> {
    try {
      return await campaignApi.getStats(uuid)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch campaign stats'
      throw e
    }
  }

  return {
    campaigns,
    activeCampaign,
    isLoading,
    error,
    fetchCampaigns,
    getCampaign,
    createCampaign,
    updateCampaign,
    deleteCampaign,
    scheduleCampaign,
    sendCampaign,
    pauseCampaign,
    resumeCampaign,
    getCampaignStats
  }
})
