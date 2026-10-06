import { defineStore } from 'pinia'
import { ref } from 'vue'
import {
  campaignApi,
  type Campaign,
  type CampaignCreateRequest,
  type CampaignProgress,
  type CampaignStatsResponse,
  type CampaignTestResponse,
  type CampaignUpdateRequest
} from '@/lib/api'

export const useCampaignsStore = defineStore('campaigns', () => {
  const campaigns = ref<Campaign[]>([])
  const activeCampaign = ref<Campaign | null>(null)
  const isLoading = ref(false)
  const error = ref<string | null>(null)

  const message = (e: unknown, fallback: string) => e instanceof Error ? e.message : fallback

  // Every transition returns the stored campaign, so the list never guesses a status.
  function replace(updated: Campaign) {
    const index = campaigns.value.findIndex(c => c.uuid === updated.uuid)
    if (index !== -1) campaigns.value[index] = updated
    if (activeCampaign.value?.uuid === updated.uuid) activeCampaign.value = updated
    return updated
  }

  async function run<T>(fallback: string, action: () => Promise<T>): Promise<T> {
    try {
      return await action()
    } catch (e) {
      error.value = message(e, fallback)
      throw e
    }
  }

  async function fetchCampaigns() {
    isLoading.value = true
    error.value = null
    try {
      const result = await campaignApi.list()
      campaigns.value = (result?.campaigns ?? []).filter((c): c is Campaign => c != null)
    } catch (e) {
      error.value = message(e, 'Failed to fetch campaigns')
      campaigns.value = []
    } finally {
      isLoading.value = false
    }
  }

  const getCampaign = (uuid: string) => run('Failed to fetch campaign', async () => {
    activeCampaign.value = await campaignApi.get(uuid)
    return activeCampaign.value
  })

  const createCampaign = (data: CampaignCreateRequest) => run('Failed to create campaign', async () => {
    const created = await campaignApi.create(data)
    campaigns.value.unshift(created)
    return created
  })

  const updateCampaign = (uuid: string, data: CampaignUpdateRequest) =>
    run('Failed to update campaign', async () => replace(await campaignApi.update(uuid, data)))

  const deleteCampaign = (uuid: string) => run('Failed to delete campaign', async () => {
    await campaignApi.delete(uuid)
    campaigns.value = campaigns.value.filter(c => c.uuid !== uuid)
    if (activeCampaign.value?.uuid === uuid) activeCampaign.value = null
  })

  const scheduleCampaign = (uuid: string, scheduledAt: string) =>
    run('Failed to schedule campaign', async () => replace(await campaignApi.schedule(uuid, scheduledAt)))

  const sendCampaign = (uuid: string) =>
    run('Failed to send campaign', async () => replace(await campaignApi.send(uuid)))

  const pauseCampaign = (uuid: string) =>
    run('Failed to pause campaign', async () => replace(await campaignApi.pause(uuid)))

  const resumeCampaign = (uuid: string) =>
    run('Failed to resume campaign', async () => replace(await campaignApi.resume(uuid)))

  const cancelCampaign = (uuid: string) =>
    run('Failed to cancel campaign', async () => replace(await campaignApi.cancel(uuid)))

  const sendTestCampaign = (uuid: string, emails: string[], idempotencyKey: string): Promise<CampaignTestResponse> =>
    run('Failed to send test email', () => campaignApi.sendTest(uuid, emails, idempotencyKey))

  const fetchProgress = (uuid: string): Promise<CampaignProgress> =>
    run('Failed to fetch campaign progress', () => campaignApi.progress(uuid))

  const getCampaignStats = (uuid: string): Promise<CampaignStatsResponse> =>
    run('Failed to fetch campaign stats', () => campaignApi.getStats(uuid))

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
    cancelCampaign,
    sendTestCampaign,
    fetchProgress,
    getCampaignStats
  }
})
