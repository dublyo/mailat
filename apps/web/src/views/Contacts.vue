<script setup lang="ts">
import { onMounted, ref, computed, watch } from 'vue'
import {
  Plus, Search, MoreVertical, Users, Tag, Upload, Download,
  Trash2, Edit2, X, Check, AlertCircle, ChevronLeft, ChevronRight,
  FileSpreadsheet, UserPlus, RefreshCw, FolderPlus, UserCheck
} from 'lucide-vue-next'
import AppLayout from '@/components/layout/AppLayout.vue'
import Button from '@/components/common/Button.vue'
import Avatar from '@/components/common/Avatar.vue'
import Spinner from '@/components/common/Spinner.vue'
import { useContactsStore } from '@/stores/contacts'
import type { ContactFull, ImportContactRow, ContactList } from '@/lib/api'

const contactsStore = useContactsStore()
const searchQuery = ref('')
const activeTab = ref<'contacts' | 'lists'>('contacts')

// Selection
const selectedContacts = ref<Set<string>>(new Set())
const selectAll = ref(false)

// Modals
const showAddModal = ref(false)
const showEditModal = ref(false)
const showImportModal = ref(false)
const showDeleteConfirm = ref(false)
const editingContact = ref<ContactFull | null>(null)
const contactToDelete = ref<string | null>(null)

// List modals
const showListModal = ref(false)
const showDeleteListConfirm = ref(false)
const showAddToListModal = ref(false)
const showViewListContactsModal = ref(false)
const editingList = ref<ContactList | null>(null)
const listToDelete = ref<string | null>(null)
const selectedListForContacts = ref<ContactList | null>(null)
const viewingList = ref<ContactList | null>(null)
const listContacts = ref<ContactFull[]>([])
const listContactsLoading = ref(false)
const listContactsPage = ref(1)
const listContactsTotal = ref(0)
const listContactsTotalPages = ref(1)
const selectedListContacts = ref<Set<string>>(new Set())

// Add to List Modal - Enhanced
const addToListTab = ref<'select' | 'upload' | 'manual'>('select')
const addToListTarget = ref<ContactList | null>(null)
const addToListSearchQuery = ref('')
const addToListSelectedContacts = ref<Set<string>>(new Set())
const addToListLoading = ref(false)
const addToListResult = ref<{ imported: number; updated: number; skipped: number; errors?: string[] } | null>(null)

// Add to List - Manual form
const manualContactForm = ref({
  email: '',
  firstName: '',
  lastName: ''
})

// Add to List - CSV upload
const addToListCsvFile = ref<File | null>(null)
const addToListCsvHeaders = ref<string[]>([])
const addToListCsvRows = ref<string[][]>([])
const addToListCsvPreview = ref<ImportContactRow[]>([])
const addToListCsvMapping = ref({
  email: 'email',
  firstName: 'firstName',
  lastName: 'lastName'
})
const addToListUpdateExisting = ref(false)

// Quick add selected contacts to list (from contacts tab)
const showQuickAddToListModal = ref(false)
const quickAddListTarget = ref<ContactList | null>(null)

// List form
const listForm = ref({
  name: '',
  description: ''
})
const isCreatingList = ref(false)

// Add/Edit Form
const contactForm = ref({
  email: '',
  firstName: '',
  lastName: '',
  company: '',
  phone: '',
  status: 'active'
})

// Import state
const importFile = ref<File | null>(null)
const importStep = ref<'upload' | 'preview' | 'result'>('upload')
const importPreviewData = ref<ImportContactRow[]>([])
const importColumnMapping = ref({
  email: 'email',
  firstName: 'firstName',
  lastName: 'lastName'
})
const importOptions = ref({
  updateExisting: false,
  consentSource: 'csv_import'
})
const csvHeaders = ref<string[]>([])
const csvRows = ref<string[][]>([])

// Computed
const contactName = (contact: ContactFull) => {
  const parts = [contact.firstName, contact.lastName].filter(Boolean)
  return parts.length > 0 ? parts.join(' ') : contact.email
}

const hasSelection = computed(() => selectedContacts.value.size > 0)

const statusColors: Record<string, string> = {
  active: 'bg-green-100 text-green-800',
  unsubscribed: 'bg-yellow-100 text-yellow-800',
  bounced: 'bg-red-100 text-red-800',
  complained: 'bg-red-100 text-red-800'
}

// Pagination computed
const paginationInfo = computed(() => {
  const start = (contactsStore.currentPage - 1) * contactsStore.pageSize + 1
  const end = Math.min(contactsStore.currentPage * contactsStore.pageSize, contactsStore.totalContacts)
  return { start, end }
})

const pageNumbers = computed(() => {
  const pages: (number | string)[] = []
  const total = contactsStore.totalPages
  const current = contactsStore.currentPage

  if (total <= 7) {
    for (let i = 1; i <= total; i++) pages.push(i)
  } else {
    pages.push(1)
    if (current > 3) pages.push('...')
    for (let i = Math.max(2, current - 1); i <= Math.min(total - 1, current + 1); i++) {
      pages.push(i)
    }
    if (current < total - 2) pages.push('...')
    pages.push(total)
  }
  return pages
})

// Watchers
watch(selectAll, (val) => {
  if (val) {
    selectedContacts.value = new Set(contactsStore.contacts.map(c => c.uuid))
  } else {
    selectedContacts.value = new Set()
  }
})

// Lifecycle
onMounted(() => {
  contactsStore.fetchContacts()
  contactsStore.fetchLists()
})

// Methods
const handleSearch = () => {
  contactsStore.searchContacts(searchQuery.value)
}

const toggleContactSelection = (uuid: string) => {
  const newSet = new Set(selectedContacts.value)
  if (newSet.has(uuid)) {
    newSet.delete(uuid)
  } else {
    newSet.add(uuid)
  }
  selectedContacts.value = newSet
  selectAll.value = newSet.size === contactsStore.contacts.length
}

const openAddModal = () => {
  contactForm.value = {
    email: '',
    firstName: '',
    lastName: '',
    company: '',
    phone: '',
    status: 'active'
  }
  showAddModal.value = true
}

const openEditModal = (contact: ContactFull) => {
  editingContact.value = contact
  contactForm.value = {
    email: contact.email,
    firstName: contact.firstName || '',
    lastName: contact.lastName || '',
    company: (contact.attributes?.company as string) || '',
    phone: (contact.attributes?.phone as string) || '',
    status: contact.status
  }
  showEditModal.value = true
}

const saveContact = async () => {
  try {
    const data = {
      email: contactForm.value.email,
      firstName: contactForm.value.firstName,
      lastName: contactForm.value.lastName,
      attributes: {
        company: contactForm.value.company,
        phone: contactForm.value.phone
      },
      consentSource: 'manual'
    }
    await contactsStore.createContact(data)
    showAddModal.value = false
  } catch (e) {
    console.error('Failed to create contact:', e)
  }
}

const updateContact = async () => {
  if (!editingContact.value) return
  try {
    const data = {
      email: contactForm.value.email,
      firstName: contactForm.value.firstName,
      lastName: contactForm.value.lastName,
      attributes: {
        company: contactForm.value.company,
        phone: contactForm.value.phone
      },
      status: contactForm.value.status
    }
    await contactsStore.updateContact(editingContact.value.uuid, data)
    showEditModal.value = false
    editingContact.value = null
  } catch (e) {
    console.error('Failed to update contact:', e)
  }
}

const confirmDelete = (uuid: string) => {
  contactToDelete.value = uuid
  showDeleteConfirm.value = true
}

const deleteContact = async () => {
  if (!contactToDelete.value) return
  try {
    await contactsStore.deleteContact(contactToDelete.value)
    showDeleteConfirm.value = false
    contactToDelete.value = null
  } catch (e) {
    console.error('Failed to delete contact:', e)
  }
}

const deleteSelected = async () => {
  if (!hasSelection.value) return
  try {
    await contactsStore.deleteMultipleContacts(Array.from(selectedContacts.value))
    selectedContacts.value = new Set()
    selectAll.value = false
  } catch (e) {
    console.error('Failed to delete contacts:', e)
  }
}

// List functions
const openCreateListModal = () => {
  editingList.value = null
  listForm.value = { name: '', description: '' }
  showListModal.value = true
}

const openEditListModal = (list: ContactList) => {
  editingList.value = list
  listForm.value = { name: list.name, description: list.description || '' }
  showListModal.value = true
}

const saveList = async () => {
  try {
    if (editingList.value) {
      await contactsStore.updateList(editingList.value.uuid, listForm.value)
    } else {
      await contactsStore.createList(listForm.value)
    }
    showListModal.value = false
    editingList.value = null
  } catch (e) {
    console.error('Failed to save list:', e)
  }
}

const saveListInline = async () => {
  if (!listForm.value.name || isCreatingList.value) return
  isCreatingList.value = true
  try {
    await contactsStore.createList(listForm.value)
    listForm.value = { name: '', description: '' }
  } catch (e) {
    console.error('Failed to create list:', e)
  } finally {
    isCreatingList.value = false
  }
}

const confirmDeleteList = (uuid: string) => {
  listToDelete.value = uuid
  showDeleteListConfirm.value = true
}

const deleteList = async () => {
  if (!listToDelete.value) return
  try {
    await contactsStore.deleteList(listToDelete.value)
    showDeleteListConfirm.value = false
    listToDelete.value = null
  } catch (e) {
    console.error('Failed to delete list:', e)
  }
}

const openAddToListModal = (list: ContactList) => {
  addToListTarget.value = list
  addToListTab.value = 'select'
  addToListSearchQuery.value = ''
  addToListSelectedContacts.value = new Set()
  addToListResult.value = null
  addToListCsvFile.value = null
  addToListCsvHeaders.value = []
  addToListCsvRows.value = []
  addToListCsvPreview.value = []
  addToListUpdateExisting.value = false
  manualContactForm.value = { email: '', firstName: '', lastName: '' }
  showAddToListModal.value = true
}

const closeAddToListModal = () => {
  showAddToListModal.value = false
  addToListTarget.value = null
  addToListResult.value = null
}

// Filter contacts for add to list modal
const filteredContactsForList = computed(() => {
  if (!addToListSearchQuery.value.trim()) {
    return contactsStore.contacts
  }
  const query = addToListSearchQuery.value.toLowerCase()
  return contactsStore.contacts.filter(c =>
    c.email.toLowerCase().includes(query) ||
    (c.firstName && c.firstName.toLowerCase().includes(query)) ||
    (c.lastName && c.lastName.toLowerCase().includes(query))
  )
})

const toggleAddToListContactSelection = (uuid: string) => {
  const newSet = new Set(addToListSelectedContacts.value)
  if (newSet.has(uuid)) {
    newSet.delete(uuid)
  } else {
    newSet.add(uuid)
  }
  addToListSelectedContacts.value = newSet
}

// Add selected contacts from existing contacts
const addSelectedContactsToList = async () => {
  if (!addToListTarget.value || addToListSelectedContacts.value.size === 0) return
  addToListLoading.value = true
  try {
    await contactsStore.addContactsToList(addToListTarget.value.uuid, Array.from(addToListSelectedContacts.value))
    addToListResult.value = { imported: addToListSelectedContacts.value.size, updated: 0, skipped: 0 }
    addToListSelectedContacts.value = new Set()
  } catch (e) {
    console.error('Failed to add contacts to list:', e)
  } finally {
    addToListLoading.value = false
  }
}

// CSV upload for add to list
const handleAddToListCsvSelect = (event: Event) => {
  const target = event.target as HTMLInputElement
  const file = target.files?.[0]
  if (file) {
    addToListCsvFile.value = file
    parseAddToListCsv(file)
  }
}

const parseAddToListCsv = (file: File) => {
  const reader = new FileReader()
  reader.onload = (e) => {
    const text = e.target?.result as string
    const lines = text.split('\n').filter(line => line.trim())
    if (lines.length < 2) return

    addToListCsvHeaders.value = parseCSVLine(lines[0])

    // Auto-detect columns
    const lowerHeaders = addToListCsvHeaders.value.map(h => h.toLowerCase())
    const emailIdx = lowerHeaders.findIndex(h => h.includes('email'))
    if (emailIdx >= 0) addToListCsvMapping.value.email = addToListCsvHeaders.value[emailIdx]
    const firstIdx = lowerHeaders.findIndex(h => h.includes('first'))
    if (firstIdx >= 0) addToListCsvMapping.value.firstName = addToListCsvHeaders.value[firstIdx]
    const lastIdx = lowerHeaders.findIndex(h => h.includes('last'))
    if (lastIdx >= 0) addToListCsvMapping.value.lastName = addToListCsvHeaders.value[lastIdx]

    addToListCsvRows.value = lines.slice(1).map(line => parseCSVLine(line))
    addToListCsvPreview.value = addToListCsvRows.value.map(row => ({
      email: row[addToListCsvHeaders.value.indexOf(addToListCsvMapping.value.email)] || '',
      firstName: row[addToListCsvHeaders.value.indexOf(addToListCsvMapping.value.firstName)] || '',
      lastName: row[addToListCsvHeaders.value.indexOf(addToListCsvMapping.value.lastName)] || ''
    }))
  }
  reader.readAsText(file)
}

const importCsvToList = async () => {
  if (!addToListTarget.value || addToListCsvPreview.value.length === 0) return
  const validContacts = addToListCsvPreview.value.filter(c => c.email && c.email.includes('@'))
  if (validContacts.length === 0) return

  addToListLoading.value = true
  try {
    const result = await contactsStore.importContactsToList(addToListTarget.value.uuid, validContacts, addToListUpdateExisting.value)
    addToListResult.value = result
    addToListCsvFile.value = null
    addToListCsvRows.value = []
    addToListCsvPreview.value = []
  } catch (e) {
    console.error('Failed to import CSV to list:', e)
  } finally {
    addToListLoading.value = false
  }
}

// Manual add contact to list
const manualAddToList = async () => {
  if (!addToListTarget.value || !manualContactForm.value.email) return
  addToListLoading.value = true
  try {
    await contactsStore.manualAddContactToList(addToListTarget.value.uuid, manualContactForm.value)
    addToListResult.value = { imported: 1, updated: 0, skipped: 0 }
    manualContactForm.value = { email: '', firstName: '', lastName: '' }
    // Refresh contacts list to include the new contact
    await contactsStore.fetchContacts()
  } catch (e) {
    console.error('Failed to add contact to list:', e)
  } finally {
    addToListLoading.value = false
  }
}

// Quick add selected contacts to list (from Contacts tab)
const openQuickAddToListModal = () => {
  quickAddListTarget.value = null
  showQuickAddToListModal.value = true
}

const quickAddSelectedToList = async () => {
  if (!quickAddListTarget.value || selectedContacts.value.size === 0) return
  try {
    await contactsStore.addContactsToList(quickAddListTarget.value.uuid, Array.from(selectedContacts.value))
    showQuickAddToListModal.value = false
    quickAddListTarget.value = null
    selectedContacts.value = new Set()
    selectAll.value = false
  } catch (e) {
    console.error('Failed to add contacts to list:', e)
  }
}

const openViewListContactsModal = async (list: ContactList) => {
  viewingList.value = list
  listContacts.value = []
  listContactsPage.value = 1
  selectedListContacts.value = new Set()
  showViewListContactsModal.value = true
  await fetchListContacts()
}

const fetchListContacts = async () => {
  if (!viewingList.value) return
  listContactsLoading.value = true
  try {
    const result = await contactsStore.getListContacts(viewingList.value.uuid, listContactsPage.value, 20)
    listContacts.value = result.contacts || []
    listContactsTotal.value = result.total || 0
    listContactsTotalPages.value = result.totalPages || 1
  } catch (e) {
    console.error('Failed to fetch list contacts:', e)
    listContacts.value = []
  } finally {
    listContactsLoading.value = false
  }
}

const toggleListContactSelection = (uuid: string) => {
  const newSet = new Set(selectedListContacts.value)
  if (newSet.has(uuid)) {
    newSet.delete(uuid)
  } else {
    newSet.add(uuid)
  }
  selectedListContacts.value = newSet
}

const removeSelectedFromList = async () => {
  if (!viewingList.value || selectedListContacts.value.size === 0) return
  try {
    await contactsStore.removeContactsFromList(viewingList.value.uuid, Array.from(selectedListContacts.value))
    selectedListContacts.value = new Set()
    // Refresh the list contacts
    await fetchListContacts()
    // Update the member count in the list view
    if (viewingList.value) {
      viewingList.value = { ...viewingList.value, contactCount: listContactsTotal.value }
    }
  } catch (e) {
    console.error('Failed to remove contacts from list:', e)
  }
}

const closeViewListContactsModal = () => {
  showViewListContactsModal.value = false
  viewingList.value = null
  listContacts.value = []
  selectedListContacts.value = new Set()
}

const addSelectedToList = async () => {
  if (!selectedListForContacts.value || selectedContacts.value.size === 0) return
  try {
    // Note: The backend expects contact IDs, not UUIDs
    // For now we'll use UUIDs and the backend should handle it
    const contactIds = Array.from(selectedContacts.value)
    await contactsStore.addContactsToList(selectedListForContacts.value.uuid, contactIds)
    showAddToListModal.value = false
    selectedListForContacts.value = null
    selectedContacts.value = new Set()
    selectAll.value = false
    // Refresh lists to update member count
    await contactsStore.fetchLists()
  } catch (e) {
    console.error('Failed to add contacts to list:', e)
  }
}

// Import functions
const openImportModal = () => {
  importFile.value = null
  importStep.value = 'upload'
  importPreviewData.value = []
  csvHeaders.value = []
  csvRows.value = []
  contactsStore.clearImportResult()
  showImportModal.value = true
}

const handleFileSelect = (event: Event) => {
  const target = event.target as HTMLInputElement
  const file = target.files?.[0]
  if (file) {
    importFile.value = file
    parseCSV(file)
  }
}

const handleFileDrop = (event: DragEvent) => {
  event.preventDefault()
  const file = event.dataTransfer?.files[0]
  if (file && (file.type === 'text/csv' || file.name.endsWith('.csv'))) {
    importFile.value = file
    parseCSV(file)
  }
}

const parseCSV = (file: File) => {
  const reader = new FileReader()
  reader.onload = (e) => {
    const text = e.target?.result as string
    const lines = text.split('\n').filter(line => line.trim())
    if (lines.length < 2) {
      alert('CSV file must have a header row and at least one data row')
      return
    }

    // Parse headers
    csvHeaders.value = parseCSVLine(lines[0])

    // Auto-detect column mapping
    autoDetectColumns(csvHeaders.value)

    // Parse all data rows
    const allRows = lines.slice(1).map(line => parseCSVLine(line))
    csvRows.value = allRows

    // Generate preview data
    importPreviewData.value = allRows.map(row => rowToContact(row, csvHeaders.value))

    importStep.value = 'preview'
  }
  reader.readAsText(file)
}

const parseCSVLine = (line: string): string[] => {
  const result: string[] = []
  let current = ''
  let inQuotes = false

  for (let i = 0; i < line.length; i++) {
    const char = line[i]
    if (char === '"') {
      inQuotes = !inQuotes
    } else if (char === ',' && !inQuotes) {
      result.push(current.trim())
      current = ''
    } else {
      current += char
    }
  }
  result.push(current.trim())
  return result
}

const autoDetectColumns = (headers: string[]) => {
  const lowerHeaders = headers.map(h => h.toLowerCase())

  // Detect email column
  const emailIdx = lowerHeaders.findIndex(h =>
    h.includes('email') || h === 'e-mail' || h === 'mail'
  )
  if (emailIdx >= 0) importColumnMapping.value.email = headers[emailIdx]

  // Detect first name column
  const firstNameIdx = lowerHeaders.findIndex(h =>
    h.includes('first') || h === 'firstname' || h === 'first_name' || h === 'given'
  )
  if (firstNameIdx >= 0) importColumnMapping.value.firstName = headers[firstNameIdx]

  // Detect last name column
  const lastNameIdx = lowerHeaders.findIndex(h =>
    h.includes('last') || h === 'lastname' || h === 'last_name' || h === 'surname' || h === 'family'
  )
  if (lastNameIdx >= 0) importColumnMapping.value.lastName = headers[lastNameIdx]
}

const rowToContact = (row: string[], headers: string[]): ImportContactRow => {
  const getColumnValue = (mapping: string) => {
    const idx = headers.indexOf(mapping)
    return idx >= 0 ? row[idx] || '' : ''
  }

  return {
    email: getColumnValue(importColumnMapping.value.email),
    firstName: getColumnValue(importColumnMapping.value.firstName),
    lastName: getColumnValue(importColumnMapping.value.lastName)
  }
}

const executeImport = async () => {
  const contacts = importPreviewData.value.filter(c => c.email && c.email.includes('@'))

  if (contacts.length === 0) {
    alert('No valid contacts to import. Make sure email column is correctly mapped.')
    return
  }

  try {
    await contactsStore.importContacts({
      contacts,
      updateExisting: importOptions.value.updateExisting,
      consentSource: importOptions.value.consentSource
    })
    importStep.value = 'result'
  } catch (e) {
    console.error('Import failed:', e)
  }
}

const closeImportModal = () => {
  showImportModal.value = false
  contactsStore.clearImportResult()
}

// Export function
const exportContacts = async () => {
  try {
    const contacts = await contactsStore.exportContacts()

    // Convert to CSV
    const headers = ['email', 'firstName', 'lastName', 'status', 'createdAt']
    const csvContent = [
      headers.join(','),
      ...contacts.map(c => [
        `"${c.email}"`,
        `"${c.firstName || ''}"`,
        `"${c.lastName || ''}"`,
        `"${c.status}"`,
        `"${c.createdAt}"`
      ].join(','))
    ].join('\n')

    // Download
    const blob = new Blob([csvContent], { type: 'text/csv' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `contacts-export-${new Date().toISOString().split('T')[0]}.csv`
    a.click()
    URL.revokeObjectURL(url)
  } catch (e) {
    console.error('Export failed:', e)
  }
}

// Pagination
const goToPage = (page: number | string) => {
  if (typeof page !== 'number') return
  if (page < 1 || page > contactsStore.totalPages) return
  contactsStore.fetchContacts(page, contactsStore.pageSize)
  selectedContacts.value = new Set()
  selectAll.value = false
}

const formatDate = (dateStr: string) => {
  return new Date(dateStr).toLocaleDateString()
}
</script>

<template>
  <AppLayout>
    <div class="flex-1 flex flex-col p-6 min-h-0">
      <!-- Header -->
      <div class="flex items-center justify-between mb-6 flex-shrink-0">
        <div>
          <h1 class="text-2xl font-medium">Contacts</h1>
          <p class="text-gmail-gray">Manage your contacts and lists</p>
        </div>
        <div class="flex items-center gap-2">
          <Button variant="secondary" @click="exportContacts" :disabled="contactsStore.isExporting">
            <Download class="w-4 h-4" />
            Export CSV
          </Button>
          <Button variant="secondary" @click="openImportModal">
            <Upload class="w-4 h-4" />
            Import CSV
          </Button>
          <Button @click="activeTab === 'contacts' ? openAddModal() : openCreateListModal()">
            <template v-if="activeTab === 'contacts'">
              <UserPlus class="w-4 h-4" />
              Add Contact
            </template>
            <template v-else>
              <FolderPlus class="w-4 h-4" />
              Create List
            </template>
          </Button>
        </div>
      </div>

      <!-- Tabs -->
      <div class="flex gap-4 mb-6 flex-shrink-0">
        <button
          @click="activeTab = 'contacts'"
          :class="[
            'flex items-center gap-2 px-4 py-2 rounded-full text-sm font-medium transition-colors',
            activeTab === 'contacts'
              ? 'bg-gmail-selected text-gmail-blue'
              : 'text-gmail-gray hover:bg-gmail-hover'
          ]"
        >
          <Users class="w-4 h-4" />
          Contacts ({{ contactsStore.totalContacts }})
        </button>
        <button
          @click="activeTab = 'lists'"
          :class="[
            'flex items-center gap-2 px-4 py-2 rounded-full text-sm font-medium transition-colors',
            activeTab === 'lists'
              ? 'bg-gmail-selected text-gmail-blue'
              : 'text-gmail-gray hover:bg-gmail-hover'
          ]"
        >
          <Tag class="w-4 h-4" />
          Lists ({{ contactsStore.lists.length }})
        </button>
      </div>

      <!-- Search & Bulk Actions -->
      <div class="flex items-center gap-4 mb-4 flex-shrink-0">
        <div class="relative flex-1 max-w-md">
          <Search class="w-5 h-5 text-gmail-gray absolute left-3 top-1/2 -translate-y-1/2" />
          <input
            v-model="searchQuery"
            @input="handleSearch"
            type="text"
            :placeholder="activeTab === 'contacts' ? 'Search contacts...' : 'Search lists...'"
            class="w-full pl-10 pr-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
          />
        </div>
        <div v-if="hasSelection && activeTab === 'contacts'" class="flex items-center gap-2">
          <span class="text-sm text-gmail-gray">{{ selectedContacts.size }} selected</span>
          <Button variant="secondary" size="sm" @click="openQuickAddToListModal" v-if="contactsStore.lists.length > 0">
            <UserCheck class="w-4 h-4" />
            Add to List
          </Button>
          <Button variant="danger" size="sm" @click="deleteSelected">
            <Trash2 class="w-4 h-4" />
            Delete
          </Button>
        </div>
      </div>

      <!-- Loading -->
      <div v-if="contactsStore.isLoading" class="flex-1 flex items-center justify-center">
        <Spinner size="lg" />
      </div>

      <!-- Contacts Tab -->
      <template v-else-if="activeTab === 'contacts'">
        <!-- Empty state -->
        <div
          v-if="contactsStore.contacts.length === 0"
          class="flex-1 flex flex-col items-center justify-center text-gmail-gray"
        >
          <Users class="w-16 h-16 mb-4 opacity-50" />
          <p class="text-lg mb-2">No contacts yet</p>
          <p class="text-sm mb-4">Add contacts manually or import from a CSV file</p>
          <div class="flex gap-2">
            <Button variant="secondary" @click="openImportModal">
              <Upload class="w-4 h-4" />
              Import CSV
            </Button>
            <Button @click="openAddModal">
              <Plus class="w-4 h-4" />
              Add Contact
            </Button>
          </div>
        </div>

        <!-- Contact list -->
        <div v-else class="flex-1 flex flex-col min-h-0">
          <!-- Table container with scroll -->
          <div class="flex-1 bg-white rounded-lg border border-gmail-border overflow-hidden flex flex-col min-h-0">
            <div class="overflow-auto flex-1">
              <table class="w-full">
                <thead class="bg-gmail-lightGray sticky top-0">
                  <tr>
                    <th class="w-10 px-4 py-3">
                      <input
                        type="checkbox"
                        v-model="selectAll"
                        class="rounded border-gmail-border"
                      />
                    </th>
                    <th class="text-left px-4 py-3 text-sm font-medium text-gmail-gray">Name</th>
                    <th class="text-left px-4 py-3 text-sm font-medium text-gmail-gray">Email</th>
                    <th class="text-left px-4 py-3 text-sm font-medium text-gmail-gray">Status</th>
                    <th class="text-left px-4 py-3 text-sm font-medium text-gmail-gray">Created</th>
                    <th class="w-20"></th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-gmail-border">
                  <tr
                    v-for="contact in contactsStore.contacts"
                    :key="contact.uuid"
                    class="hover:bg-gmail-hover"
                  >
                    <td class="px-4 py-3">
                      <input
                        type="checkbox"
                        :checked="selectedContacts.has(contact.uuid)"
                        @change="toggleContactSelection(contact.uuid)"
                        class="rounded border-gmail-border"
                      />
                    </td>
                    <td class="px-4 py-3">
                      <div class="flex items-center gap-3">
                        <Avatar :name="contactName(contact)" :email="contact.email" size="sm" />
                        <div>
                          <div class="font-medium">{{ contactName(contact) }}</div>
                          <div v-if="contact.attributes?.company" class="text-xs text-gmail-gray">
                            {{ contact.attributes.company }}
                          </div>
                        </div>
                      </div>
                    </td>
                    <td class="px-4 py-3 text-sm text-gmail-gray">
                      {{ contact.email }}
                    </td>
                    <td class="px-4 py-3">
                      <span
                        :class="[
                          'px-2 py-1 text-xs font-medium rounded-full',
                          statusColors[contact.status] || 'bg-gray-100 text-gray-800'
                        ]"
                      >
                        {{ contact.status }}
                      </span>
                    </td>
                    <td class="px-4 py-3 text-sm text-gmail-gray">
                      {{ formatDate(contact.createdAt) }}
                    </td>
                    <td class="px-4 py-3">
                      <div class="flex items-center gap-1">
                        <button
                          @click="openEditModal(contact)"
                          class="p-1 hover:bg-gmail-hover rounded"
                          title="Edit"
                        >
                          <Edit2 class="w-4 h-4 text-gmail-gray" />
                        </button>
                        <button
                          @click="confirmDelete(contact.uuid)"
                          class="p-1 hover:bg-red-50 rounded"
                          title="Delete"
                        >
                          <Trash2 class="w-4 h-4 text-red-500" />
                        </button>
                      </div>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <!-- Pagination - Always visible -->
          <div class="flex items-center justify-between mt-4 pt-4 border-t border-gmail-border flex-shrink-0">
            <div class="text-sm text-gmail-gray">
              <span v-if="contactsStore.totalContacts > 0">
                Showing {{ paginationInfo.start }} - {{ paginationInfo.end }} of {{ contactsStore.totalContacts }} contacts
              </span>
            </div>
            <div class="flex items-center gap-1">
              <button
                @click="goToPage(contactsStore.currentPage - 1)"
                :disabled="contactsStore.currentPage <= 1"
                class="p-2 rounded hover:bg-gmail-hover disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <ChevronLeft class="w-5 h-5" />
              </button>

              <template v-for="page in pageNumbers" :key="page">
                <span v-if="page === '...'" class="px-2 text-gmail-gray">...</span>
                <button
                  v-else
                  @click="goToPage(page)"
                  :class="[
                    'px-3 py-1 rounded text-sm',
                    contactsStore.currentPage === page
                      ? 'bg-gmail-blue text-white'
                      : 'hover:bg-gmail-hover'
                  ]"
                >
                  {{ page }}
                </button>
              </template>

              <button
                @click="goToPage(contactsStore.currentPage + 1)"
                :disabled="contactsStore.currentPage >= contactsStore.totalPages"
                class="p-2 rounded hover:bg-gmail-hover disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <ChevronRight class="w-5 h-5" />
              </button>
            </div>
          </div>
        </div>
      </template>

      <!-- Lists Tab -->
      <template v-else>
        <!-- Inline Create Form + Lists grid -->
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 overflow-auto">
          <!-- Inline Create New List Card -->
          <div class="bg-white rounded-lg border-2 border-dashed border-gmail-border p-4 hover:border-gmail-blue transition-colors">
            <div class="flex items-center gap-2 mb-3 text-gmail-blue">
              <FolderPlus class="w-5 h-5" />
              <span class="font-medium">Create New List</span>
            </div>
            <div class="space-y-3">
              <input
                v-model="listForm.name"
                type="text"
                placeholder="List name *"
                class="w-full px-3 py-2 text-sm border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
                @keyup.enter="saveListInline"
              />
              <input
                v-model="listForm.description"
                type="text"
                placeholder="Description (optional)"
                class="w-full px-3 py-2 text-sm border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
                @keyup.enter="saveListInline"
              />
              <Button
                size="sm"
                class="w-full"
                @click="saveListInline"
                :disabled="!listForm.name || isCreatingList"
                :loading="isCreatingList"
              >
                <Plus class="w-4 h-4" />
                Create List
              </Button>
            </div>
          </div>

          <!-- Existing Lists -->
          <div
            v-for="list in contactsStore.lists"
            :key="list.uuid"
            class="bg-white rounded-lg border border-gmail-border p-4 hover:shadow-md transition-shadow"
          >
            <div class="flex items-center justify-between mb-2">
              <h3 class="font-medium">{{ list.name }}</h3>
              <div class="flex items-center gap-1">
                <button
                  @click="openViewListContactsModal(list)"
                  class="p-1 hover:bg-gmail-hover rounded"
                  title="View contacts"
                >
                  <Users class="w-4 h-4 text-gmail-gray" />
                </button>
                <button
                  @click="openAddToListModal(list)"
                  class="p-1 hover:bg-gmail-hover rounded"
                  title="Add contacts"
                >
                  <UserPlus class="w-4 h-4 text-gmail-gray" />
                </button>
                <button
                  @click="openEditListModal(list)"
                  class="p-1 hover:bg-gmail-hover rounded"
                  title="Edit"
                >
                  <Edit2 class="w-4 h-4 text-gmail-gray" />
                </button>
                <button
                  @click="confirmDeleteList(list.uuid)"
                  class="p-1 hover:bg-red-50 rounded"
                  title="Delete"
                >
                  <Trash2 class="w-4 h-4 text-red-500" />
                </button>
              </div>
            </div>
            <p v-if="list.description" class="text-sm text-gmail-gray mb-3 line-clamp-2">
              {{ list.description }}
            </p>
            <div class="flex items-center gap-1 text-sm text-gmail-gray">
              <Users class="w-4 h-4" />
              <span>{{ list.contactCount }} contacts</span>
            </div>
          </div>
        </div>
      </template>
    </div>

    <!-- Add Contact Modal -->
    <div v-if="showAddModal" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div class="bg-white rounded-lg shadow-xl w-full max-w-md mx-4">
        <div class="flex items-center justify-between p-4 border-b">
          <h2 class="text-lg font-medium">Add Contact</h2>
          <button @click="showAddModal = false" class="p-1 hover:bg-gmail-hover rounded">
            <X class="w-5 h-5" />
          </button>
        </div>
        <div class="p-4 space-y-4">
          <div>
            <label class="block text-sm font-medium mb-1">Email *</label>
            <input
              v-model="contactForm.email"
              type="email"
              class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
              placeholder="email@example.com"
            />
          </div>
          <div class="grid grid-cols-2 gap-4">
            <div>
              <label class="block text-sm font-medium mb-1">First Name</label>
              <input
                v-model="contactForm.firstName"
                type="text"
                class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
              />
            </div>
            <div>
              <label class="block text-sm font-medium mb-1">Last Name</label>
              <input
                v-model="contactForm.lastName"
                type="text"
                class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
              />
            </div>
          </div>
          <div>
            <label class="block text-sm font-medium mb-1">Company</label>
            <input
              v-model="contactForm.company"
              type="text"
              class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            />
          </div>
          <div>
            <label class="block text-sm font-medium mb-1">Phone</label>
            <input
              v-model="contactForm.phone"
              type="text"
              class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            />
          </div>
        </div>
        <div class="flex justify-end gap-2 p-4 border-t">
          <Button variant="secondary" @click="showAddModal = false">Cancel</Button>
          <Button @click="saveContact" :disabled="!contactForm.email">Save Contact</Button>
        </div>
      </div>
    </div>

    <!-- Edit Contact Modal -->
    <div v-if="showEditModal" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div class="bg-white rounded-lg shadow-xl w-full max-w-md mx-4">
        <div class="flex items-center justify-between p-4 border-b">
          <h2 class="text-lg font-medium">Edit Contact</h2>
          <button @click="showEditModal = false" class="p-1 hover:bg-gmail-hover rounded">
            <X class="w-5 h-5" />
          </button>
        </div>
        <div class="p-4 space-y-4">
          <div>
            <label class="block text-sm font-medium mb-1">Email *</label>
            <input
              v-model="contactForm.email"
              type="email"
              class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            />
          </div>
          <div class="grid grid-cols-2 gap-4">
            <div>
              <label class="block text-sm font-medium mb-1">First Name</label>
              <input
                v-model="contactForm.firstName"
                type="text"
                class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
              />
            </div>
            <div>
              <label class="block text-sm font-medium mb-1">Last Name</label>
              <input
                v-model="contactForm.lastName"
                type="text"
                class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
              />
            </div>
          </div>
          <div>
            <label class="block text-sm font-medium mb-1">Company</label>
            <input
              v-model="contactForm.company"
              type="text"
              class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            />
          </div>
          <div>
            <label class="block text-sm font-medium mb-1">Phone</label>
            <input
              v-model="contactForm.phone"
              type="text"
              class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            />
          </div>
          <div>
            <label class="block text-sm font-medium mb-1">Status</label>
            <select
              v-model="contactForm.status"
              class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
            >
              <option value="active">Active</option>
              <option value="unsubscribed">Unsubscribed</option>
              <option value="bounced">Bounced</option>
              <option value="complained">Complained</option>
            </select>
          </div>
        </div>
        <div class="flex justify-end gap-2 p-4 border-t">
          <Button variant="secondary" @click="showEditModal = false">Cancel</Button>
          <Button @click="updateContact" :disabled="!contactForm.email">Save Changes</Button>
        </div>
      </div>
    </div>

    <!-- Delete Contact Confirmation Modal -->
    <div v-if="showDeleteConfirm" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div class="bg-white rounded-lg shadow-xl w-full max-w-sm mx-4">
        <div class="p-6 text-center">
          <AlertCircle class="w-12 h-12 text-red-500 mx-auto mb-4" />
          <h2 class="text-lg font-medium mb-2">Delete Contact?</h2>
          <p class="text-gmail-gray mb-6">This action cannot be undone.</p>
          <div class="flex justify-center gap-2">
            <Button variant="secondary" @click="showDeleteConfirm = false">Cancel</Button>
            <Button variant="danger" @click="deleteContact">Delete</Button>
          </div>
        </div>
      </div>
    </div>

    <!-- List Modal (Create/Edit) -->
    <div v-if="showListModal" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div class="bg-white rounded-lg shadow-xl w-full max-w-md mx-4">
        <div class="flex items-center justify-between p-4 border-b">
          <h2 class="text-lg font-medium">{{ editingList ? 'Edit List' : 'Create List' }}</h2>
          <button @click="showListModal = false" class="p-1 hover:bg-gmail-hover rounded">
            <X class="w-5 h-5" />
          </button>
        </div>
        <div class="p-4 space-y-4">
          <div>
            <label class="block text-sm font-medium mb-1">List Name *</label>
            <input
              v-model="listForm.name"
              type="text"
              class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
              placeholder="e.g., Newsletter Subscribers"
            />
          </div>
          <div>
            <label class="block text-sm font-medium mb-1">Description</label>
            <textarea
              v-model="listForm.description"
              rows="3"
              class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue resize-none"
              placeholder="Optional description for this list..."
            ></textarea>
          </div>
        </div>
        <div class="flex justify-end gap-2 p-4 border-t">
          <Button variant="secondary" @click="showListModal = false">Cancel</Button>
          <Button @click="saveList" :disabled="!listForm.name">
            {{ editingList ? 'Save Changes' : 'Create List' }}
          </Button>
        </div>
      </div>
    </div>

    <!-- Delete List Confirmation Modal -->
    <div v-if="showDeleteListConfirm" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div class="bg-white rounded-lg shadow-xl w-full max-w-sm mx-4">
        <div class="p-6 text-center">
          <AlertCircle class="w-12 h-12 text-red-500 mx-auto mb-4" />
          <h2 class="text-lg font-medium mb-2">Delete List?</h2>
          <p class="text-gmail-gray mb-6">This will remove the list but not the contacts in it.</p>
          <div class="flex justify-center gap-2">
            <Button variant="secondary" @click="showDeleteListConfirm = false">Cancel</Button>
            <Button variant="danger" @click="deleteList">Delete</Button>
          </div>
        </div>
      </div>
    </div>

    <!-- Add to List Modal - Enhanced -->
    <div v-if="showAddToListModal" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div class="bg-white rounded-lg shadow-xl w-full max-w-2xl mx-4 max-h-[85vh] flex flex-col">
        <div class="flex items-center justify-between p-4 border-b flex-shrink-0">
          <div>
            <h2 class="text-lg font-medium">Add Contacts to {{ addToListTarget?.name }}</h2>
            <p class="text-sm text-gmail-gray">{{ addToListTarget?.contactCount }} contacts currently</p>
          </div>
          <button @click="closeAddToListModal" class="p-1 hover:bg-gmail-hover rounded">
            <X class="w-5 h-5" />
          </button>
        </div>

        <!-- Result view -->
        <div v-if="addToListResult" class="p-6">
          <div class="text-center mb-6">
            <div class="w-16 h-16 bg-green-100 rounded-full flex items-center justify-center mx-auto mb-4">
              <Check class="w-8 h-8 text-green-600" />
            </div>
            <h3 class="text-xl font-medium mb-2">Contacts Added</h3>
          </div>
          <div class="space-y-3 mb-6">
            <div class="flex justify-between p-3 bg-green-50 rounded-lg">
              <span>New contacts added</span>
              <span class="font-medium text-green-600">{{ addToListResult.imported }}</span>
            </div>
            <div v-if="addToListResult.updated > 0" class="flex justify-between p-3 bg-blue-50 rounded-lg">
              <span>Existing contacts updated</span>
              <span class="font-medium text-blue-600">{{ addToListResult.updated }}</span>
            </div>
            <div v-if="addToListResult.skipped > 0" class="flex justify-between p-3 bg-yellow-50 rounded-lg">
              <span>Skipped (duplicates)</span>
              <span class="font-medium text-yellow-600">{{ addToListResult.skipped }}</span>
            </div>
            <div v-if="addToListResult.errors?.length" class="p-3 bg-red-50 rounded-lg">
              <p class="font-medium text-red-600 mb-2">Errors ({{ addToListResult.errors.length }})</p>
              <ul class="text-sm text-red-600 list-disc list-inside max-h-24 overflow-auto">
                <li v-for="(err, idx) in addToListResult.errors.slice(0, 5)" :key="idx">{{ err }}</li>
              </ul>
            </div>
          </div>
          <div class="flex justify-center gap-2">
            <Button variant="secondary" @click="addToListResult = null">Add More</Button>
            <Button @click="closeAddToListModal">Done</Button>
          </div>
        </div>

        <!-- Main content -->
        <template v-else>
          <!-- Tabs -->
          <div class="flex border-b flex-shrink-0">
            <button
              @click="addToListTab = 'select'"
              :class="[
                'flex-1 px-4 py-3 text-sm font-medium border-b-2 transition-colors',
                addToListTab === 'select'
                  ? 'border-gmail-blue text-gmail-blue'
                  : 'border-transparent text-gmail-gray hover:text-gray-700'
              ]"
            >
              <Users class="w-4 h-4 inline mr-2" />
              From Contacts
            </button>
            <button
              @click="addToListTab = 'upload'"
              :class="[
                'flex-1 px-4 py-3 text-sm font-medium border-b-2 transition-colors',
                addToListTab === 'upload'
                  ? 'border-gmail-blue text-gmail-blue'
                  : 'border-transparent text-gmail-gray hover:text-gray-700'
              ]"
            >
              <Upload class="w-4 h-4 inline mr-2" />
              Upload CSV
            </button>
            <button
              @click="addToListTab = 'manual'"
              :class="[
                'flex-1 px-4 py-3 text-sm font-medium border-b-2 transition-colors',
                addToListTab === 'manual'
                  ? 'border-gmail-blue text-gmail-blue'
                  : 'border-transparent text-gmail-gray hover:text-gray-700'
              ]"
            >
              <UserPlus class="w-4 h-4 inline mr-2" />
              Manual Entry
            </button>
          </div>

          <!-- Tab: Select from existing contacts -->
          <div v-if="addToListTab === 'select'" class="flex-1 flex flex-col min-h-0 overflow-hidden">
            <div class="p-4 border-b flex-shrink-0">
              <div class="relative">
                <Search class="w-5 h-5 text-gmail-gray absolute left-3 top-1/2 -translate-y-1/2" />
                <input
                  v-model="addToListSearchQuery"
                  type="text"
                  placeholder="Search contacts..."
                  class="w-full pl-10 pr-4 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
                />
              </div>
              <div v-if="addToListSelectedContacts.size > 0" class="mt-2 text-sm text-gmail-blue">
                {{ addToListSelectedContacts.size }} contact(s) selected
              </div>
            </div>
            <div class="flex-1 overflow-auto min-h-0">
              <div v-if="filteredContactsForList.length === 0" class="p-8 text-center text-gmail-gray">
                <Users class="w-12 h-12 mx-auto mb-3 opacity-50" />
                <p>No contacts found</p>
              </div>
              <div v-else class="divide-y divide-gmail-border">
                <label
                  v-for="contact in filteredContactsForList"
                  :key="contact.uuid"
                  class="flex items-center gap-3 p-3 hover:bg-gmail-hover cursor-pointer"
                >
                  <input
                    type="checkbox"
                    :checked="addToListSelectedContacts.has(contact.uuid)"
                    @change="toggleAddToListContactSelection(contact.uuid)"
                    class="rounded border-gmail-border"
                  />
                  <Avatar :name="contactName(contact)" :email="contact.email" size="sm" />
                  <div class="flex-1 min-w-0">
                    <div class="font-medium truncate">{{ contactName(contact) }}</div>
                    <div class="text-sm text-gmail-gray truncate">{{ contact.email }}</div>
                  </div>
                </label>
              </div>
            </div>
            <div class="flex justify-end gap-2 p-4 border-t flex-shrink-0">
              <Button variant="secondary" @click="closeAddToListModal">Cancel</Button>
              <Button
                @click="addSelectedContactsToList"
                :disabled="addToListSelectedContacts.size === 0 || addToListLoading"
                :loading="addToListLoading"
              >
                Add {{ addToListSelectedContacts.size }} Contact(s)
              </Button>
            </div>
          </div>

          <!-- Tab: Upload CSV -->
          <div v-if="addToListTab === 'upload'" class="flex-1 flex flex-col min-h-0 overflow-hidden">
            <div v-if="!addToListCsvFile" class="flex-1 p-6">
              <div class="border-2 border-dashed border-gmail-border rounded-lg p-8 text-center hover:border-gmail-blue transition-colors">
                <Upload class="w-12 h-12 text-gmail-gray mx-auto mb-4" />
                <p class="text-lg font-medium mb-2">Upload a CSV file</p>
                <p class="text-gmail-gray mb-4">CSV should contain email, firstName, lastName columns</p>
                <input
                  type="file"
                  accept=".csv"
                  @change="handleAddToListCsvSelect"
                  class="hidden"
                  id="add-to-list-csv"
                />
                <label
                  for="add-to-list-csv"
                  class="inline-flex items-center gap-2 px-4 py-2 bg-gmail-blue text-white rounded-lg cursor-pointer hover:bg-blue-600"
                >
                  <Upload class="w-4 h-4" />
                  Select CSV File
                </label>
              </div>
            </div>
            <template v-else>
              <div class="p-4 border-b bg-gmail-lightGray flex items-center justify-between flex-shrink-0">
                <div class="flex items-center gap-2 text-green-600">
                  <Check class="w-5 h-5" />
                  <span class="font-medium">{{ addToListCsvFile.name }}</span>
                  <span class="text-gmail-gray">({{ addToListCsvPreview.length }} contacts)</span>
                </div>
                <button @click="addToListCsvFile = null; addToListCsvRows = []; addToListCsvPreview = []" class="text-gmail-gray hover:text-red-500">
                  <X class="w-5 h-5" />
                </button>
              </div>
              <div class="p-4 border-b flex-shrink-0">
                <h4 class="font-medium mb-3">Column Mapping</h4>
                <div class="grid grid-cols-3 gap-3">
                  <div>
                    <label class="block text-sm text-gmail-gray mb-1">Email *</label>
                    <select v-model="addToListCsvMapping.email" class="w-full px-3 py-2 border border-gmail-border rounded-lg text-sm">
                      <option v-for="h in addToListCsvHeaders" :key="h" :value="h">{{ h }}</option>
                    </select>
                  </div>
                  <div>
                    <label class="block text-sm text-gmail-gray mb-1">First Name</label>
                    <select v-model="addToListCsvMapping.firstName" class="w-full px-3 py-2 border border-gmail-border rounded-lg text-sm">
                      <option value="">-- Skip --</option>
                      <option v-for="h in addToListCsvHeaders" :key="h" :value="h">{{ h }}</option>
                    </select>
                  </div>
                  <div>
                    <label class="block text-sm text-gmail-gray mb-1">Last Name</label>
                    <select v-model="addToListCsvMapping.lastName" class="w-full px-3 py-2 border border-gmail-border rounded-lg text-sm">
                      <option value="">-- Skip --</option>
                      <option v-for="h in addToListCsvHeaders" :key="h" :value="h">{{ h }}</option>
                    </select>
                  </div>
                </div>
                <label class="flex items-center gap-2 mt-3">
                  <input type="checkbox" v-model="addToListUpdateExisting" class="rounded border-gmail-border" />
                  <span class="text-sm">Update existing contacts</span>
                </label>
              </div>
              <div class="flex-1 overflow-auto min-h-0 p-4">
                <h4 class="font-medium mb-3">Preview (first 10 rows)</h4>
                <table class="w-full text-sm border border-gmail-border">
                  <thead class="bg-gmail-lightGray">
                    <tr>
                      <th class="px-3 py-2 text-left border-b">Email</th>
                      <th class="px-3 py-2 text-left border-b">First Name</th>
                      <th class="px-3 py-2 text-left border-b">Last Name</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="(row, idx) in addToListCsvPreview.slice(0, 10)" :key="idx" class="border-b">
                      <td class="px-3 py-2">{{ row.email || '-' }}</td>
                      <td class="px-3 py-2">{{ row.firstName || '-' }}</td>
                      <td class="px-3 py-2">{{ row.lastName || '-' }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </template>
            <div class="flex justify-end gap-2 p-4 border-t flex-shrink-0">
              <Button variant="secondary" @click="closeAddToListModal">Cancel</Button>
              <Button
                v-if="addToListCsvFile"
                @click="importCsvToList"
                :disabled="addToListCsvPreview.length === 0 || addToListLoading"
                :loading="addToListLoading"
              >
                Import {{ addToListCsvPreview.filter(c => c.email).length }} Contacts
              </Button>
            </div>
          </div>

          <!-- Tab: Manual Entry -->
          <div v-if="addToListTab === 'manual'" class="flex-1 p-6">
            <div class="space-y-4">
              <div>
                <label class="block text-sm font-medium mb-1">Email *</label>
                <input
                  v-model="manualContactForm.email"
                  type="email"
                  class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
                  placeholder="email@example.com"
                />
              </div>
              <div class="grid grid-cols-2 gap-4">
                <div>
                  <label class="block text-sm font-medium mb-1">First Name</label>
                  <input
                    v-model="manualContactForm.firstName"
                    type="text"
                    class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
                  />
                </div>
                <div>
                  <label class="block text-sm font-medium mb-1">Last Name</label>
                  <input
                    v-model="manualContactForm.lastName"
                    type="text"
                    class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue"
                  />
                </div>
              </div>
              <p class="text-sm text-gmail-gray">
                If this email already exists, the contact will be added to the list without creating a duplicate.
              </p>
            </div>
            <div class="flex justify-end gap-2 mt-6">
              <Button variant="secondary" @click="closeAddToListModal">Cancel</Button>
              <Button
                @click="manualAddToList"
                :disabled="!manualContactForm.email || addToListLoading"
                :loading="addToListLoading"
              >
                Add Contact
              </Button>
            </div>
          </div>
        </template>
      </div>
    </div>

    <!-- View List Contacts Modal -->
    <div v-if="showViewListContactsModal" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div class="bg-white rounded-lg shadow-xl w-full max-w-2xl mx-4 max-h-[80vh] flex flex-col">
        <div class="flex items-center justify-between p-4 border-b flex-shrink-0">
          <div>
            <h2 class="text-lg font-medium">{{ viewingList?.name }}</h2>
            <p class="text-sm text-gmail-gray">{{ listContactsTotal }} contacts in this list</p>
          </div>
          <button @click="closeViewListContactsModal" class="p-1 hover:bg-gmail-hover rounded">
            <X class="w-5 h-5" />
          </button>
        </div>

        <!-- Actions bar -->
        <div v-if="selectedListContacts.size > 0" class="p-3 border-b bg-gmail-lightGray flex items-center gap-3 flex-shrink-0">
          <span class="text-sm text-gmail-gray">{{ selectedListContacts.size }} selected</span>
          <Button variant="danger" size="sm" @click="removeSelectedFromList">
            <Trash2 class="w-4 h-4" />
            Remove from List
          </Button>
        </div>

        <!-- Loading state -->
        <div v-if="listContactsLoading" class="flex-1 flex items-center justify-center p-8">
          <Spinner size="lg" />
        </div>

        <!-- Empty state -->
        <div v-else-if="listContacts.length === 0" class="flex-1 flex flex-col items-center justify-center p-8 text-gmail-gray">
          <Users class="w-12 h-12 mb-3 opacity-50" />
          <p class="mb-4">No contacts in this list</p>
          <Button size="sm" @click="showViewListContactsModal = false; openAddToListModal(viewingList!)">
            <UserPlus class="w-4 h-4" />
            Add Contacts
          </Button>
        </div>

        <!-- Contacts list -->
        <div v-else class="flex-1 overflow-auto min-h-0">
          <table class="w-full">
            <thead class="bg-gmail-lightGray sticky top-0">
              <tr>
                <th class="w-10 px-4 py-3"></th>
                <th class="text-left px-4 py-3 text-sm font-medium text-gmail-gray">Name</th>
                <th class="text-left px-4 py-3 text-sm font-medium text-gmail-gray">Email</th>
                <th class="text-left px-4 py-3 text-sm font-medium text-gmail-gray">Status</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gmail-border">
              <tr
                v-for="contact in listContacts"
                :key="contact.uuid"
                class="hover:bg-gmail-hover"
              >
                <td class="px-4 py-3">
                  <input
                    type="checkbox"
                    :checked="selectedListContacts.has(contact.uuid)"
                    @change="toggleListContactSelection(contact.uuid)"
                    class="rounded border-gmail-border"
                  />
                </td>
                <td class="px-4 py-3">
                  <div class="flex items-center gap-3">
                    <Avatar :name="contactName(contact)" :email="contact.email" size="sm" />
                    <span class="font-medium">{{ contactName(contact) }}</span>
                  </div>
                </td>
                <td class="px-4 py-3 text-sm text-gmail-gray">{{ contact.email }}</td>
                <td class="px-4 py-3">
                  <span
                    :class="[
                      'px-2 py-1 text-xs font-medium rounded-full',
                      statusColors[contact.status] || 'bg-gray-100 text-gray-800'
                    ]"
                  >
                    {{ contact.status }}
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Pagination -->
        <div v-if="listContactsTotalPages > 1" class="flex items-center justify-between p-4 border-t flex-shrink-0">
          <span class="text-sm text-gmail-gray">
            Page {{ listContactsPage }} of {{ listContactsTotalPages }}
          </span>
          <div class="flex items-center gap-2">
            <Button
              variant="secondary"
              size="sm"
              :disabled="listContactsPage <= 1"
              @click="listContactsPage--; fetchListContacts()"
            >
              <ChevronLeft class="w-4 h-4" />
              Previous
            </Button>
            <Button
              variant="secondary"
              size="sm"
              :disabled="listContactsPage >= listContactsTotalPages"
              @click="listContactsPage++; fetchListContacts()"
            >
              Next
              <ChevronRight class="w-4 h-4" />
            </Button>
          </div>
        </div>
      </div>
    </div>

    <!-- Quick Add to List Modal (from Contacts tab) -->
    <div v-if="showQuickAddToListModal" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div class="bg-white rounded-lg shadow-xl w-full max-w-md mx-4">
        <div class="flex items-center justify-between p-4 border-b">
          <h2 class="text-lg font-medium">Add to List</h2>
          <button @click="showQuickAddToListModal = false" class="p-1 hover:bg-gmail-hover rounded">
            <X class="w-5 h-5" />
          </button>
        </div>
        <div class="p-4">
          <p class="text-gmail-gray mb-4">
            Select a list to add {{ selectedContacts.size }} contact(s) to:
          </p>
          <div class="space-y-2 max-h-64 overflow-auto">
            <label
              v-for="list in contactsStore.lists"
              :key="list.uuid"
              class="flex items-center gap-3 p-3 rounded-lg border border-gmail-border hover:bg-gmail-hover cursor-pointer"
              :class="{ 'border-gmail-blue bg-gmail-selected': quickAddListTarget?.uuid === list.uuid }"
            >
              <input
                type="radio"
                :value="list"
                v-model="quickAddListTarget"
                class="text-gmail-blue"
              />
              <div class="flex-1">
                <div class="font-medium">{{ list.name }}</div>
                <div class="text-xs text-gmail-gray">{{ list.contactCount }} contacts</div>
              </div>
            </label>
          </div>
        </div>
        <div class="flex justify-end gap-2 p-4 border-t">
          <Button variant="secondary" @click="showQuickAddToListModal = false">Cancel</Button>
          <Button @click="quickAddSelectedToList" :disabled="!quickAddListTarget">
            Add to List
          </Button>
        </div>
      </div>
    </div>

    <!-- Import CSV Modal -->
    <div v-if="showImportModal" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div class="bg-white rounded-lg shadow-xl w-full max-w-2xl mx-4 max-h-[90vh] flex flex-col">
        <div class="flex items-center justify-between p-4 border-b flex-shrink-0">
          <div class="flex items-center gap-3">
            <FileSpreadsheet class="w-6 h-6 text-gmail-blue" />
            <h2 class="text-lg font-medium">Import Contacts from CSV</h2>
          </div>
          <button @click="closeImportModal" class="p-1 hover:bg-gmail-hover rounded">
            <X class="w-5 h-5" />
          </button>
        </div>

        <!-- Step 1: Upload -->
        <div v-if="importStep === 'upload'" class="p-6">
          <div
            @drop="handleFileDrop"
            @dragover.prevent
            @dragenter.prevent
            class="border-2 border-dashed border-gmail-border rounded-lg p-8 text-center hover:border-gmail-blue transition-colors"
          >
            <Upload class="w-12 h-12 text-gmail-gray mx-auto mb-4" />
            <p class="text-lg font-medium mb-2">Drop your CSV file here</p>
            <p class="text-gmail-gray mb-4">or click to browse</p>
            <input
              type="file"
              accept=".csv"
              @change="handleFileSelect"
              class="hidden"
              id="csv-upload"
            />
            <label
              for="csv-upload"
              class="inline-flex items-center gap-2 px-4 py-2 bg-gmail-blue text-white rounded-lg cursor-pointer hover:bg-blue-600"
            >
              <Upload class="w-4 h-4" />
              Select File
            </label>
          </div>
          <div class="mt-4 text-sm text-gmail-gray">
            <p class="font-medium mb-2">CSV Format Requirements:</p>
            <ul class="list-disc list-inside space-y-1">
              <li>First row must contain column headers</li>
              <li>Must include an "email" column</li>
              <li>Optional: firstName, lastName columns</li>
              <li>Maximum 1,000 contacts per import</li>
            </ul>
          </div>
        </div>

        <!-- Step 2: Preview & Mapping -->
        <div v-else-if="importStep === 'preview'" class="flex-1 overflow-hidden flex flex-col min-h-0">
          <div class="p-4 border-b bg-gmail-lightGray flex-shrink-0">
            <div class="flex items-center gap-4">
              <div class="flex items-center gap-2 text-green-600">
                <Check class="w-5 h-5" />
                <span class="font-medium">{{ importFile?.name }}</span>
              </div>
              <span class="text-gmail-gray">
                {{ importPreviewData.length }} contacts found
              </span>
            </div>
          </div>

          <!-- Column Mapping -->
          <div class="p-4 border-b flex-shrink-0">
            <h3 class="font-medium mb-3">Column Mapping</h3>
            <div class="grid grid-cols-3 gap-4">
              <div>
                <label class="block text-sm font-medium mb-1">Email Column *</label>
                <select
                  v-model="importColumnMapping.email"
                  class="w-full px-3 py-2 border border-gmail-border rounded-lg text-sm"
                >
                  <option v-for="h in csvHeaders" :key="h" :value="h">{{ h }}</option>
                </select>
              </div>
              <div>
                <label class="block text-sm font-medium mb-1">First Name Column</label>
                <select
                  v-model="importColumnMapping.firstName"
                  class="w-full px-3 py-2 border border-gmail-border rounded-lg text-sm"
                >
                  <option value="">-- Skip --</option>
                  <option v-for="h in csvHeaders" :key="h" :value="h">{{ h }}</option>
                </select>
              </div>
              <div>
                <label class="block text-sm font-medium mb-1">Last Name Column</label>
                <select
                  v-model="importColumnMapping.lastName"
                  class="w-full px-3 py-2 border border-gmail-border rounded-lg text-sm"
                >
                  <option value="">-- Skip --</option>
                  <option v-for="h in csvHeaders" :key="h" :value="h">{{ h }}</option>
                </select>
              </div>
            </div>
          </div>

          <!-- Options -->
          <div class="p-4 border-b flex-shrink-0">
            <h3 class="font-medium mb-3">Import Options</h3>
            <label class="flex items-center gap-2">
              <input
                type="checkbox"
                v-model="importOptions.updateExisting"
                class="rounded border-gmail-border"
              />
              <span class="text-sm">Update existing contacts (match by email)</span>
            </label>
          </div>

          <!-- Preview Table -->
          <div class="flex-1 overflow-auto p-4 min-h-0">
            <h3 class="font-medium mb-3">Preview (first 10 rows)</h3>
            <table class="w-full text-sm border border-gmail-border">
              <thead class="bg-gmail-lightGray sticky top-0">
                <tr>
                  <th class="px-3 py-2 text-left border-b">Email</th>
                  <th class="px-3 py-2 text-left border-b">First Name</th>
                  <th class="px-3 py-2 text-left border-b">Last Name</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(row, idx) in csvRows.slice(0, 10)" :key="idx" class="border-b">
                  <td class="px-3 py-2">{{ row[csvHeaders.indexOf(importColumnMapping.email)] || '-' }}</td>
                  <td class="px-3 py-2">{{ row[csvHeaders.indexOf(importColumnMapping.firstName)] || '-' }}</td>
                  <td class="px-3 py-2">{{ row[csvHeaders.indexOf(importColumnMapping.lastName)] || '-' }}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <div class="flex justify-between p-4 border-t flex-shrink-0">
            <Button variant="secondary" @click="importStep = 'upload'">
              <ChevronLeft class="w-4 h-4" />
              Back
            </Button>
            <Button @click="executeImport" :disabled="contactsStore.isImporting">
              <template v-if="contactsStore.isImporting">
                <RefreshCw class="w-4 h-4 animate-spin" />
                Importing...
              </template>
              <template v-else>
                Import {{ importPreviewData.length }} Contacts
              </template>
            </Button>
          </div>
        </div>

        <!-- Step 3: Result -->
        <div v-else-if="importStep === 'result'" class="p-6">
          <div class="text-center mb-6">
            <div class="w-16 h-16 bg-green-100 rounded-full flex items-center justify-center mx-auto mb-4">
              <Check class="w-8 h-8 text-green-600" />
            </div>
            <h3 class="text-xl font-medium mb-2">Import Complete</h3>
          </div>

          <div v-if="contactsStore.importResult" class="space-y-3 mb-6">
            <div class="flex justify-between p-3 bg-green-50 rounded-lg">
              <span>New contacts imported</span>
              <span class="font-medium text-green-600">{{ contactsStore.importResult.imported }}</span>
            </div>
            <div class="flex justify-between p-3 bg-blue-50 rounded-lg">
              <span>Existing contacts updated</span>
              <span class="font-medium text-blue-600">{{ contactsStore.importResult.updated }}</span>
            </div>
            <div class="flex justify-between p-3 bg-yellow-50 rounded-lg">
              <span>Skipped (duplicates)</span>
              <span class="font-medium text-yellow-600">{{ contactsStore.importResult.skipped }}</span>
            </div>
            <div v-if="contactsStore.importResult.errors?.length" class="p-3 bg-red-50 rounded-lg">
              <p class="font-medium text-red-600 mb-2">Errors ({{ contactsStore.importResult.errors.length }})</p>
              <ul class="text-sm text-red-600 list-disc list-inside max-h-32 overflow-auto">
                <li v-for="(err, idx) in contactsStore.importResult.errors" :key="idx">{{ err }}</li>
              </ul>
            </div>
          </div>

          <div class="flex justify-center">
            <Button @click="closeImportModal">Done</Button>
          </div>
        </div>
      </div>
    </div>
  </AppLayout>
</template>
