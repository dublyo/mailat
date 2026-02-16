import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { contactApi, listApi, type ContactFull, type ContactList, type ImportContactsRequest, type ImportContactsResponse, type ListContactsResponse, type ImportContactRow, type ImportToListResponse } from '@/lib/api'

export const useContactsStore = defineStore('contacts', () => {
  const contacts = ref<ContactFull[]>([])
  const lists = ref<ContactList[]>([])
  const totalContacts = ref(0)
  const currentPage = ref(1)
  const totalPages = ref(1)
  const pageSize = ref(50)
  const isLoading = ref(false)
  const isImporting = ref(false)
  const isExporting = ref(false)
  const error = ref<string | null>(null)
  const importResult = ref<ImportContactsResponse | null>(null)

  // Computed
  const hasContacts = computed(() => contacts.value.length > 0)
  const hasLists = computed(() => lists.value.length > 0)

  async function fetchContacts(page = 1, limit = 50) {
    isLoading.value = true
    error.value = null
    try {
      const result = await contactApi.list(page, limit)
      contacts.value = (result?.contacts ?? []).filter((c): c is ContactFull => c != null)
      totalContacts.value = result?.total ?? 0
      currentPage.value = result?.page ?? 1
      totalPages.value = result?.totalPages ?? 1
      pageSize.value = result?.pageSize ?? limit
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch contacts'
      contacts.value = []
    } finally {
      isLoading.value = false
    }
  }

  async function searchContacts(query: string) {
    if (!query.trim()) {
      await fetchContacts()
      return
    }

    isLoading.value = true
    try {
      const result = await contactApi.search(query)
      // Map old Contact type to ContactFull
      contacts.value = (result ?? []).map(c => ({
        id: parseInt(c.id) || 0,
        uuid: c.uuid,
        orgId: 0,
        email: c.email,
        firstName: c.name?.split(' ')[0] || '',
        lastName: c.name?.split(' ').slice(1).join(' ') || '',
        attributes: { company: c.company, phone: c.phone },
        status: 'active' as const,
        engagementScore: 0,
        createdAt: c.createdAt,
        updatedAt: c.createdAt,
      }))
      totalContacts.value = contacts.value.length
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to search contacts'
      contacts.value = []
    } finally {
      isLoading.value = false
    }
  }

  async function createContact(data: { email: string; firstName?: string; lastName?: string; attributes?: Record<string, unknown>; listIds?: number[]; consentSource?: string }) {
    try {
      const newContact = await contactApi.create(data)
      contacts.value.unshift(newContact)
      totalContacts.value++
      return newContact
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to create contact'
      throw e
    }
  }

  async function updateContact(uuid: string, data: { email?: string; firstName?: string; lastName?: string; attributes?: Record<string, unknown>; status?: string }) {
    try {
      const updated = await contactApi.update(uuid, data)
      const index = contacts.value.findIndex(c => c.uuid === uuid)
      if (index !== -1) {
        contacts.value[index] = updated
      }
      return updated
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to update contact'
      throw e
    }
  }

  async function deleteContact(uuid: string) {
    try {
      await contactApi.delete(uuid)
      contacts.value = contacts.value.filter(c => c.uuid !== uuid)
      totalContacts.value--
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to delete contact'
      throw e
    }
  }

  async function deleteMultipleContacts(uuids: string[]) {
    try {
      await Promise.all(uuids.map(uuid => contactApi.delete(uuid)))
      contacts.value = contacts.value.filter(c => !uuids.includes(c.uuid))
      totalContacts.value -= uuids.length
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to delete contacts'
      throw e
    }
  }

  async function importContacts(data: ImportContactsRequest): Promise<ImportContactsResponse> {
    isImporting.value = true
    error.value = null
    importResult.value = null
    try {
      const result = await contactApi.import(data)
      importResult.value = result
      // Refresh contacts list after import
      await fetchContacts(1, pageSize.value)
      return result
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to import contacts'
      throw e
    } finally {
      isImporting.value = false
    }
  }

  async function exportContacts(listIds?: number[], status?: string[]): Promise<ContactFull[]> {
    isExporting.value = true
    error.value = null
    try {
      const result = await contactApi.export({ listIds, status })
      return result
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to export contacts'
      throw e
    } finally {
      isExporting.value = false
    }
  }

  // Lists
  async function fetchLists() {
    try {
      const result = await listApi.list()
      lists.value = (result ?? []).filter((l): l is ContactList => l != null)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch lists'
      lists.value = []
    }
  }

  async function createList(data: { name: string; description?: string }) {
    try {
      const newList = await listApi.create(data)
      lists.value.push(newList)
      return newList
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to create list'
      throw e
    }
  }

  async function updateList(uuid: string, data: { name?: string; description?: string }) {
    try {
      const updated = await listApi.update(uuid, data)
      const index = lists.value.findIndex(l => l.uuid === uuid)
      if (index !== -1) {
        lists.value[index] = updated
      }
      return updated
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to update list'
      throw e
    }
  }

  async function deleteList(uuid: string) {
    try {
      await listApi.delete(uuid)
      lists.value = lists.value.filter(l => l.uuid !== uuid)
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to delete list'
      throw e
    }
  }

  async function addContactsToList(listUuid: string, contactUuids: string[]) {
    try {
      await listApi.addContacts(listUuid, contactUuids)
      // Refresh lists to get updated member count
      await fetchLists()
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to add contacts to list'
      throw e
    }
  }

  async function removeContactsFromList(listUuid: string, contactUuids: string[]) {
    try {
      await listApi.removeContacts(listUuid, contactUuids)
      // Refresh lists to get updated member count
      await fetchLists()
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to remove contacts from list'
      throw e
    }
  }

  async function getListContacts(listUuid: string, page = 1, pageSize = 50): Promise<ListContactsResponse> {
    try {
      const result = await listApi.getContacts(listUuid, page, pageSize)
      return result
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to fetch list contacts'
      throw e
    }
  }

  async function importContactsToList(listUuid: string, contacts: ImportContactRow[], updateExisting = false): Promise<ImportToListResponse> {
    try {
      const result = await listApi.importContacts(listUuid, { contacts, updateExisting, consentSource: 'csv_import' })
      // Refresh lists to get updated member count
      await fetchLists()
      return result
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to import contacts to list'
      throw e
    }
  }

  async function manualAddContactToList(listUuid: string, data: { email: string; firstName?: string; lastName?: string }): Promise<ContactFull> {
    try {
      const result = await listApi.manualAddContact(listUuid, data)
      // Refresh lists to get updated member count
      await fetchLists()
      return result
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to add contact to list'
      throw e
    }
  }

  function clearError() {
    error.value = null
  }

  function clearImportResult() {
    importResult.value = null
  }

  return {
    // State
    contacts,
    lists,
    totalContacts,
    currentPage,
    totalPages,
    pageSize,
    isLoading,
    isImporting,
    isExporting,
    error,
    importResult,
    // Computed
    hasContacts,
    hasLists,
    // Contacts actions
    fetchContacts,
    searchContacts,
    createContact,
    updateContact,
    deleteContact,
    deleteMultipleContacts,
    importContacts,
    exportContacts,
    // Lists actions
    fetchLists,
    createList,
    updateList,
    deleteList,
    addContactsToList,
    removeContactsFromList,
    getListContacts,
    importContactsToList,
    manualAddContactToList,
    // Utility
    clearError,
    clearImportResult,
  }
})
