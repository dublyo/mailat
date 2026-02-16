<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  Copy, Key, Plus, Eye, EyeOff, Trash2, ChevronRight, ChevronDown,
  Send, Mail, Inbox, Globe, User, FileText, Bell, Tag, Filter,
  Play, Code, Book, Shield, Clock, Zap, Check, X, AlertCircle
} from 'lucide-vue-next'
import AppLayout from '@/components/layout/AppLayout.vue'
import Button from '@/components/common/Button.vue'
import Badge from '@/components/common/Badge.vue'
import Spinner from '@/components/common/Spinner.vue'
import { apiKeyApi, API_KEY_PERMISSIONS, type ApiKey, type CreateApiKeyRequest } from '@/lib/api'

// State
const apiKeys = ref<ApiKey[]>([])
const isLoading = ref(false)
const error = ref('')

// Create key modal state
const showCreateModal = ref(false)
const newKeyName = ref('')
const newKeyPermissions = ref<string[]>(['email:send'])
const newKeyRateLimit = ref(100)
const newKeyExpiry = ref<'never' | '30' | '90' | '365' | 'custom'>('never')
const newKeyCustomExpiry = ref('')
const isCreating = ref(false)
const createdKey = ref<string | null>(null)

// Sidebar navigation
const activeSection = ref('keys')
const expandedEndpoints = ref<Set<string>>(new Set(['transactional']))

// API sections for documentation
const apiSections = [
  {
    id: 'keys',
    label: 'API Keys',
    icon: Key,
    description: 'Manage your API keys'
  },
  {
    id: 'getting-started',
    label: 'Getting Started',
    icon: Book,
    description: 'Quick start guide'
  },
  {
    id: 'transactional',
    label: 'Transactional Email',
    icon: Send,
    endpoints: [
      { method: 'POST', path: '/api/v1/emails', name: 'Send Email', description: 'Send a transactional email' },
      { method: 'POST', path: '/api/v1/emails/batch', name: 'Batch Send', description: 'Send up to 100 emails' },
      { method: 'GET', path: '/api/v1/emails/:id', name: 'Get Email', description: 'Get email status and events' },
      { method: 'DELETE', path: '/api/v1/emails/:id', name: 'Cancel Email', description: 'Cancel a scheduled email' },
    ]
  },
  {
    id: 'inbox',
    label: 'Inbox (Receiving)',
    icon: Inbox,
    endpoints: [
      { method: 'GET', path: '/api/v1/inbox/received', name: 'List Emails', description: 'List received emails with filters' },
      { method: 'GET', path: '/api/v1/inbox/received/:uuid', name: 'Get Email', description: 'Get single email with content' },
      { method: 'GET', path: '/api/v1/inbox/received/counts', name: 'Get Counts', description: 'Get folder counts' },
      { method: 'POST', path: '/api/v1/inbox/received/mark', name: 'Mark Read/Unread', description: 'Mark emails as read or unread' },
      { method: 'POST', path: '/api/v1/inbox/received/star', name: 'Star/Unstar', description: 'Star or unstar emails' },
      { method: 'POST', path: '/api/v1/inbox/received/move', name: 'Move Emails', description: 'Move emails to folder' },
      { method: 'POST', path: '/api/v1/inbox/received/trash', name: 'Trash/Delete', description: 'Trash or permanently delete' },
    ]
  },
  {
    id: 'compose',
    label: 'Compose & Send',
    icon: Mail,
    endpoints: [
      { method: 'POST', path: '/api/v1/compose/send', name: 'Send Email', description: 'Send email via AWS SES' },
      { method: 'POST', path: '/api/v1/compose/draft', name: 'Save Draft', description: 'Save email as draft' },
      { method: 'PUT', path: '/api/v1/compose/draft/:uuid', name: 'Update Draft', description: 'Update existing draft' },
      { method: 'DELETE', path: '/api/v1/compose/draft/:uuid', name: 'Delete Draft', description: 'Delete a draft' },
    ]
  },
  {
    id: 'domains',
    label: 'Domains',
    icon: Globe,
    endpoints: [
      { method: 'POST', path: '/api/v1/domains', name: 'Add Domain', description: 'Add a new domain' },
      { method: 'GET', path: '/api/v1/domains', name: 'List Domains', description: 'List all domains' },
      { method: 'GET', path: '/api/v1/domains/:uuid', name: 'Get Domain', description: 'Get domain details with DNS' },
      { method: 'POST', path: '/api/v1/domains/:uuid/verify', name: 'Verify DNS', description: 'Verify DNS records' },
      { method: 'POST', path: '/api/v1/domains/:uuid/setup-receiving', name: 'Setup Receiving', description: 'Setup email receiving' },
      { method: 'DELETE', path: '/api/v1/domains/:uuid', name: 'Delete Domain', description: 'Delete a domain' },
    ]
  },
  {
    id: 'identities',
    label: 'Identities',
    icon: User,
    endpoints: [
      { method: 'POST', path: '/api/v1/identities', name: 'Create Identity', description: 'Create a new identity' },
      { method: 'GET', path: '/api/v1/identities', name: 'List Identities', description: 'List all identities' },
      { method: 'GET', path: '/api/v1/identities/:uuid', name: 'Get Identity', description: 'Get identity details' },
      { method: 'PUT', path: '/api/v1/identities/:uuid', name: 'Update Identity', description: 'Update identity' },
      { method: 'POST', path: '/api/v1/identities/:uuid/catch-all', name: 'Set Catch-All', description: 'Set as catch-all' },
      { method: 'DELETE', path: '/api/v1/identities/:uuid', name: 'Delete Identity', description: 'Delete an identity' },
    ]
  },
  {
    id: 'templates',
    label: 'Templates',
    icon: FileText,
    endpoints: [
      { method: 'POST', path: '/api/v1/templates', name: 'Create Template', description: 'Create a new template' },
      { method: 'GET', path: '/api/v1/templates', name: 'List Templates', description: 'List all templates' },
      { method: 'GET', path: '/api/v1/templates/:uuid', name: 'Get Template', description: 'Get template details' },
      { method: 'PUT', path: '/api/v1/templates/:uuid', name: 'Update Template', description: 'Update a template' },
      { method: 'DELETE', path: '/api/v1/templates/:uuid', name: 'Delete Template', description: 'Delete a template' },
      { method: 'POST', path: '/api/v1/templates/:uuid/preview', name: 'Preview Template', description: 'Preview with variables' },
    ]
  },
  {
    id: 'webhooks',
    label: 'Webhooks',
    icon: Bell,
    endpoints: [
      { method: 'POST', path: '/api/v1/webhooks', name: 'Create Webhook', description: 'Create webhook endpoint' },
      { method: 'GET', path: '/api/v1/webhooks', name: 'List Webhooks', description: 'List all webhooks' },
      { method: 'PUT', path: '/api/v1/webhooks/:uuid', name: 'Update Webhook', description: 'Update webhook' },
      { method: 'DELETE', path: '/api/v1/webhooks/:uuid', name: 'Delete Webhook', description: 'Delete webhook' },
      { method: 'POST', path: '/api/v1/webhooks/:uuid/test', name: 'Test Webhook', description: 'Send test webhook' },
    ]
  },
  {
    id: 'labels',
    label: 'Labels & Filters',
    icon: Tag,
    endpoints: [
      { method: 'GET', path: '/api/v1/labels', name: 'List Labels', description: 'List user labels' },
      { method: 'POST', path: '/api/v1/labels', name: 'Create Label', description: 'Create a label' },
      { method: 'DELETE', path: '/api/v1/labels/:uuid', name: 'Delete Label', description: 'Delete a label' },
    ]
  },
]

// Load API keys
async function loadApiKeys() {
  isLoading.value = true
  error.value = ''
  try {
    const result = await apiKeyApi.list()
    apiKeys.value = result ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to load API keys'
    apiKeys.value = []
  } finally {
    isLoading.value = false
  }
}

// Create API key
async function createApiKey() {
  if (!newKeyName.value.trim()) {
    error.value = 'Please enter a name for the API key'
    return
  }

  isCreating.value = true
  error.value = ''

  try {
    // Calculate expiry date
    let expiresAt: string | undefined
    if (newKeyExpiry.value !== 'never') {
      const days = newKeyExpiry.value === 'custom'
        ? 0 // Will use custom date
        : parseInt(newKeyExpiry.value)

      if (newKeyExpiry.value === 'custom' && newKeyCustomExpiry.value) {
        expiresAt = new Date(newKeyCustomExpiry.value).toISOString()
      } else if (days > 0) {
        const date = new Date()
        date.setDate(date.getDate() + days)
        expiresAt = date.toISOString()
      }
    }

    const result = await apiKeyApi.create({
      name: newKeyName.value,
      permissions: newKeyPermissions.value,
      rateLimit: newKeyRateLimit.value,
      expiresAt,
    })

    createdKey.value = result.key || null
    await loadApiKeys()
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to create API key'
  } finally {
    isCreating.value = false
  }
}

// Delete API key
async function deleteApiKey(uuid: string) {
  if (!confirm('Are you sure you want to delete this API key? This action cannot be undone.')) {
    return
  }

  try {
    await apiKeyApi.delete(uuid)
    await loadApiKeys()
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to delete API key'
  }
}

// Copy to clipboard
async function copyToClipboard(text: string) {
  await navigator.clipboard.writeText(text)
}

// Reset create modal
function resetCreateModal() {
  showCreateModal.value = false
  newKeyName.value = ''
  newKeyPermissions.value = ['email:send']
  newKeyRateLimit.value = 100
  newKeyExpiry.value = 'never'
  newKeyCustomExpiry.value = ''
  createdKey.value = null
  error.value = ''
}

// Toggle permission
function togglePermission(permission: string) {
  const index = newKeyPermissions.value.indexOf(permission)
  if (index >= 0) {
    newKeyPermissions.value.splice(index, 1)
  } else {
    newKeyPermissions.value.push(permission)
  }
}

// Toggle endpoint section
function toggleEndpointSection(sectionId: string) {
  if (expandedEndpoints.value.has(sectionId)) {
    expandedEndpoints.value.delete(sectionId)
  } else {
    expandedEndpoints.value.add(sectionId)
  }
}

// Format date
function formatDate(dateStr: string | undefined): string {
  if (!dateStr) return 'Never'
  return new Date(dateStr).toLocaleDateString('en-US', {
    year: 'numeric',
    month: 'short',
    day: 'numeric'
  })
}

// Get method badge color
function getMethodColor(method: string): string {
  switch (method) {
    case 'GET': return 'bg-blue-100 text-blue-700'
    case 'POST': return 'bg-green-100 text-green-700'
    case 'PUT': return 'bg-yellow-100 text-yellow-700'
    case 'DELETE': return 'bg-red-100 text-red-700'
    default: return 'bg-gray-100 text-gray-700'
  }
}

// Get base URL
const baseUrl = computed(() => {
  const host = window.location.origin
  return host.includes('localhost') ? 'http://localhost:8000' : host
})

// Code examples for copying
const curlExample = computed(() => {
  return `curl -X POST ${baseUrl.value}/api/v1/emails \\
  -H "Authorization: Bearer ue_your_api_key" \\
  -H "Content-Type: application/json" \\
  -d '{
    "from": "hello@yourdomain.com",
    "to": ["user@example.com"],
    "subject": "Welcome!",
    "html": "<h1>Hello World</h1>"
  }'`
})

const nodeExample = computed(() => {
  return `const response = await fetch('${baseUrl.value}/api/v1/emails', {
  method: 'POST',
  headers: {
    'Authorization': 'Bearer ue_your_api_key',
    'Content-Type': 'application/json'
  },
  body: JSON.stringify({
    from: 'hello@yourdomain.com',
    to: ['user@example.com'],
    subject: 'Welcome!',
    html: '<h1>Hello World</h1>'
  })
});`
})

const pythonExample = computed(() => {
  return `import requests

response = requests.post(
    '${baseUrl.value}/api/v1/emails',
    headers={
        'Authorization': 'Bearer ue_your_api_key',
        'Content-Type': 'application/json'
    },
    json={
        'from': 'hello@yourdomain.com',
        'to': ['user@example.com'],
        'subject': 'Welcome!',
        'html': '<h1>Hello World</h1>'
    }
)`
})

function getEndpointExample(method: string, path: string): string {
  return `curl -X ${method} ${baseUrl.value}${path} \\
  -H "Authorization: Bearer ue_your_api_key"`
}

onMounted(() => {
  loadApiKeys()
})
</script>

<template>
  <AppLayout>
    <div class="flex-1 flex overflow-hidden">
      <!-- Sidebar Navigation -->
      <div class="w-64 border-r border-gray-200 bg-gray-50 overflow-y-auto flex-shrink-0">
        <div class="p-4">
          <h2 class="text-lg font-semibold text-gray-900 mb-4">API Documentation</h2>

          <nav class="space-y-1">
            <button
              v-for="section in apiSections"
              :key="section.id"
              @click="activeSection = section.id; section.endpoints && toggleEndpointSection(section.id)"
              :class="[
                'w-full flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors text-left',
                activeSection === section.id
                  ? 'bg-blue-100 text-blue-700'
                  : 'text-gray-700 hover:bg-gray-100'
              ]"
            >
              <component :is="section.icon" class="w-4 h-4" />
              <span class="flex-1">{{ section.label }}</span>
              <ChevronRight
                v-if="section.endpoints"
                :class="[
                  'w-4 h-4 transition-transform',
                  expandedEndpoints.has(section.id) ? 'rotate-90' : ''
                ]"
              />
            </button>

            <!-- Expanded endpoints -->
            <template v-for="section in apiSections" :key="'endpoints-' + section.id">
              <div
                v-if="section.endpoints && expandedEndpoints.has(section.id)"
                class="ml-7 space-y-1"
              >
                <button
                  v-for="endpoint in section.endpoints"
                  :key="endpoint.path"
                  class="w-full flex items-center gap-2 px-3 py-1.5 text-xs text-gray-600 hover:text-gray-900 hover:bg-gray-100 rounded"
                >
                  <span :class="['px-1.5 py-0.5 rounded text-[10px] font-medium', getMethodColor(endpoint.method)]">
                    {{ endpoint.method }}
                  </span>
                  <span class="truncate">{{ endpoint.name }}</span>
                </button>
              </div>
            </template>
          </nav>
        </div>
      </div>

      <!-- Main Content -->
      <div class="flex-1 overflow-y-auto p-6">
        <!-- API Keys Section -->
        <div v-if="activeSection === 'keys'">
          <div class="flex items-center justify-between mb-6">
            <div>
              <h1 class="text-2xl font-semibold text-gray-900">API Keys</h1>
              <p class="text-gray-500 mt-1">Manage your API keys for programmatic access</p>
            </div>
            <Button @click="showCreateModal = true">
              <Plus class="w-4 h-4 mr-2" />
              Create API Key
            </Button>
          </div>

          <!-- Error -->
          <div v-if="error" class="mb-4 p-4 bg-red-50 border border-red-200 rounded-lg text-red-700 text-sm">
            {{ error }}
          </div>

          <!-- Loading -->
          <div v-if="isLoading" class="flex items-center justify-center py-12">
            <Spinner size="lg" />
          </div>

          <!-- API Keys List -->
          <div v-else-if="apiKeys.length > 0" class="bg-white rounded-lg border border-gray-200 divide-y divide-gray-200">
            <div
              v-for="key in apiKeys"
              :key="key.uuid"
              class="p-4"
            >
              <div class="flex items-start justify-between">
                <div class="flex-1">
                  <div class="flex items-center gap-3">
                    <h3 class="font-medium text-gray-900">{{ key.name }}</h3>
                    <span class="text-xs px-2 py-0.5 bg-gray-100 text-gray-600 rounded font-mono">
                      {{ key.keyPrefix }}••••••••
                    </span>
                  </div>

                  <div class="flex items-center gap-4 mt-2 text-sm text-gray-500">
                    <span class="flex items-center gap-1">
                      <Clock class="w-3.5 h-3.5" />
                      Created {{ formatDate(key.createdAt) }}
                    </span>
                    <span v-if="key.lastUsedAt" class="flex items-center gap-1">
                      <Zap class="w-3.5 h-3.5" />
                      Last used {{ formatDate(key.lastUsedAt) }}
                    </span>
                    <span class="flex items-center gap-1">
                      <Shield class="w-3.5 h-3.5" />
                      {{ key.rateLimit }} req/min
                    </span>
                    <span v-if="key.expiresAt" class="flex items-center gap-1">
                      <AlertCircle class="w-3.5 h-3.5" />
                      Expires {{ formatDate(key.expiresAt) }}
                    </span>
                  </div>

                  <div class="flex flex-wrap gap-1.5 mt-2">
                    <span
                      v-for="perm in key.permissions"
                      :key="perm"
                      class="text-xs px-2 py-0.5 bg-blue-50 text-blue-700 rounded"
                    >
                      {{ perm }}
                    </span>
                    <span v-if="key.permissions.length === 0" class="text-xs text-gray-400">
                      No permissions
                    </span>
                  </div>
                </div>

                <button
                  @click="deleteApiKey(key.uuid)"
                  class="p-2 text-gray-400 hover:text-red-500 transition-colors"
                  title="Delete API key"
                >
                  <Trash2 class="w-4 h-4" />
                </button>
              </div>
            </div>
          </div>

          <!-- Empty State -->
          <div v-else class="text-center py-12 bg-white rounded-lg border border-gray-200">
            <Key class="w-12 h-12 text-gray-300 mx-auto mb-4" />
            <h3 class="text-lg font-medium text-gray-900 mb-2">No API keys yet</h3>
            <p class="text-gray-500 mb-4">Create an API key to start using the API programmatically</p>
            <Button @click="showCreateModal = true">
              <Plus class="w-4 h-4 mr-2" />
              Create API Key
            </Button>
          </div>
        </div>

        <!-- Getting Started Section -->
        <div v-else-if="activeSection === 'getting-started'">
          <h1 class="text-2xl font-semibold text-gray-900 mb-6">Getting Started</h1>

          <div class="space-y-6">
            <!-- Authentication -->
            <div class="bg-white rounded-lg border border-gray-200 p-6">
              <h2 class="text-lg font-medium text-gray-900 mb-4">Authentication</h2>
              <p class="text-gray-600 mb-4">
                All API requests require authentication using an API key. Include your API key in the Authorization header:
              </p>
              <div class="bg-gray-900 rounded-lg p-4 font-mono text-sm text-gray-100">
                <code>Authorization: Bearer ue_your_api_key_here</code>
              </div>
            </div>

            <!-- Quick Start -->
            <div class="bg-white rounded-lg border border-gray-200 p-6">
              <h2 class="text-lg font-medium text-gray-900 mb-4">Send Your First Email</h2>
              <p class="text-gray-600 mb-4">
                Here's a quick example to send a transactional email:
              </p>

              <!-- cURL Example -->
              <div class="mb-4">
                <div class="flex items-center justify-between mb-2">
                  <span class="text-sm font-medium text-gray-700">cURL</span>
                  <button
                    @click="copyToClipboard(curlExample)"
                    class="text-xs text-blue-600 hover:text-blue-700 flex items-center gap-1"
                  >
                    <Copy class="w-3 h-3" />
                    Copy
                  </button>
                </div>
                <div class="bg-gray-900 rounded-lg p-4 font-mono text-sm text-gray-100 overflow-x-auto">
                  <pre>{{ curlExample }}</pre>
                </div>
              </div>

              <!-- Node.js Example -->
              <div class="mb-4">
                <div class="flex items-center justify-between mb-2">
                  <span class="text-sm font-medium text-gray-700">Node.js</span>
                  <button
                    @click="copyToClipboard(nodeExample)"
                    class="text-xs text-blue-600 hover:text-blue-700 flex items-center gap-1"
                  >
                    <Copy class="w-3 h-3" />
                    Copy
                  </button>
                </div>
                <div class="bg-gray-900 rounded-lg p-4 font-mono text-sm text-gray-100 overflow-x-auto">
                  <pre>{{ nodeExample }}</pre>
                </div>
              </div>

              <!-- Python Example -->
              <div>
                <div class="flex items-center justify-between mb-2">
                  <span class="text-sm font-medium text-gray-700">Python</span>
                  <button
                    @click="copyToClipboard(pythonExample)"
                    class="text-xs text-blue-600 hover:text-blue-700 flex items-center gap-1"
                  >
                    <Copy class="w-3 h-3" />
                    Copy
                  </button>
                </div>
                <div class="bg-gray-900 rounded-lg p-4 font-mono text-sm text-gray-100 overflow-x-auto">
                  <pre>{{ pythonExample }}</pre>
                </div>
              </div>
            </div>

            <!-- Response Format -->
            <div class="bg-white rounded-lg border border-gray-200 p-6">
              <h2 class="text-lg font-medium text-gray-900 mb-4">Response Format</h2>
              <p class="text-gray-600 mb-4">
                All API responses follow a consistent JSON format:
              </p>
              <div class="bg-gray-900 rounded-lg p-4 font-mono text-sm text-gray-100 overflow-x-auto">
                <pre>{
  "code": 0,
  "message": "Success",
  "data": {
    // Response data here
  }
}</pre>
              </div>
              <p class="text-gray-600 mt-4">
                A <code class="bg-gray-100 px-1 py-0.5 rounded text-sm">code</code> of 0 indicates success.
                Non-zero codes indicate errors with details in the <code class="bg-gray-100 px-1 py-0.5 rounded text-sm">message</code> field.
              </p>
            </div>
          </div>
        </div>

        <!-- API Endpoints Section -->
        <div v-else>
          <h1 class="text-2xl font-semibold text-gray-900 mb-2">
            {{ apiSections.find(s => s.id === activeSection)?.label }}
          </h1>
          <p class="text-gray-500 mb-6">
            {{ apiSections.find(s => s.id === activeSection)?.description }}
          </p>

          <div class="space-y-4">
            <div
              v-for="endpoint in apiSections.find(s => s.id === activeSection)?.endpoints"
              :key="endpoint.path"
              class="bg-white rounded-lg border border-gray-200 overflow-hidden"
            >
              <div class="p-4 border-b border-gray-100">
                <div class="flex items-center gap-3">
                  <span :class="['px-2 py-1 rounded text-xs font-semibold', getMethodColor(endpoint.method)]">
                    {{ endpoint.method }}
                  </span>
                  <code class="text-sm font-mono text-gray-700">{{ endpoint.path }}</code>
                </div>
                <p class="text-gray-600 text-sm mt-2">{{ endpoint.description }}</p>
              </div>

              <div class="p-4 bg-gray-50">
                <div class="flex items-center justify-between mb-2">
                  <span class="text-xs font-medium text-gray-500">Example Request</span>
                  <button
                    @click="copyToClipboard(getEndpointExample(endpoint.method, endpoint.path))"
                    class="text-xs text-blue-600 hover:text-blue-700 flex items-center gap-1"
                  >
                    <Copy class="w-3 h-3" />
                    Copy
                  </button>
                </div>
                <div class="bg-gray-900 rounded p-3 font-mono text-xs text-gray-100 overflow-x-auto">
                  <pre class="whitespace-pre-wrap">{{ getEndpointExample(endpoint.method, endpoint.path) }}</pre>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Create API Key Modal -->
    <Teleport to="body">
      <div
        v-if="showCreateModal"
        class="fixed inset-0 bg-black/50 flex items-center justify-center z-50"
        @click.self="!createdKey && resetCreateModal()"
      >
        <div class="bg-white rounded-xl shadow-xl w-full max-w-lg max-h-[90vh] overflow-y-auto">
          <!-- Created Key Success -->
          <template v-if="createdKey">
            <div class="p-6">
              <div class="flex items-center justify-center w-12 h-12 bg-green-100 rounded-full mx-auto mb-4">
                <Check class="w-6 h-6 text-green-600" />
              </div>
              <h2 class="text-xl font-semibold text-center text-gray-900 mb-2">API Key Created</h2>
              <p class="text-center text-gray-500 mb-6">
                Copy your API key now. You won't be able to see it again!
              </p>

              <div class="bg-gray-100 rounded-lg p-4 mb-6">
                <div class="flex items-center justify-between">
                  <code class="font-mono text-sm text-gray-800 break-all">{{ createdKey }}</code>
                  <button
                    @click="copyToClipboard(createdKey!)"
                    class="ml-2 p-2 text-gray-500 hover:text-gray-700 flex-shrink-0"
                  >
                    <Copy class="w-4 h-4" />
                  </button>
                </div>
              </div>

              <Button @click="resetCreateModal()" class="w-full">
                Done
              </Button>
            </div>
          </template>

          <!-- Create Form -->
          <template v-else>
            <div class="p-6 border-b border-gray-200">
              <div class="flex items-center justify-between">
                <h2 class="text-xl font-semibold text-gray-900">Create API Key</h2>
                <button @click="resetCreateModal()" class="text-gray-400 hover:text-gray-600">
                  <X class="w-5 h-5" />
                </button>
              </div>
            </div>

            <div class="p-6 space-y-6">
              <!-- Error -->
              <div v-if="error" class="p-3 bg-red-50 border border-red-200 rounded-lg text-red-700 text-sm">
                {{ error }}
              </div>

              <!-- Name -->
              <div>
                <label class="block text-sm font-medium text-gray-700 mb-1">Name</label>
                <input
                  v-model="newKeyName"
                  type="text"
                  placeholder="e.g., Production API Key"
                  class="w-full px-3 py-2 border border-gray-300 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-blue-500"
                />
              </div>

              <!-- Permissions -->
              <div>
                <label class="block text-sm font-medium text-gray-700 mb-2">Permissions</label>
                <div class="space-y-2 max-h-48 overflow-y-auto">
                  <label
                    v-for="perm in API_KEY_PERMISSIONS"
                    :key="perm.value"
                    class="flex items-start gap-3 p-3 border border-gray-200 rounded-lg cursor-pointer hover:bg-gray-50"
                    :class="{ 'border-blue-500 bg-blue-50': newKeyPermissions.includes(perm.value) }"
                  >
                    <input
                      type="checkbox"
                      :checked="newKeyPermissions.includes(perm.value)"
                      @change="togglePermission(perm.value)"
                      class="mt-0.5"
                    />
                    <div>
                      <div class="font-medium text-sm text-gray-900">{{ perm.label }}</div>
                      <div class="text-xs text-gray-500">{{ perm.description }}</div>
                    </div>
                  </label>
                </div>
              </div>

              <!-- Rate Limit -->
              <div>
                <label class="block text-sm font-medium text-gray-700 mb-1">Rate Limit (requests per minute)</label>
                <select
                  v-model="newKeyRateLimit"
                  class="w-full px-3 py-2 border border-gray-300 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-blue-500"
                >
                  <option :value="60">60 req/min</option>
                  <option :value="100">100 req/min (default)</option>
                  <option :value="500">500 req/min</option>
                  <option :value="1000">1,000 req/min</option>
                  <option :value="5000">5,000 req/min</option>
                  <option :value="10000">10,000 req/min</option>
                </select>
              </div>

              <!-- Expiration -->
              <div>
                <label class="block text-sm font-medium text-gray-700 mb-1">Expiration</label>
                <select
                  v-model="newKeyExpiry"
                  class="w-full px-3 py-2 border border-gray-300 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-blue-500"
                >
                  <option value="never">Never expires</option>
                  <option value="30">30 days</option>
                  <option value="90">90 days</option>
                  <option value="365">1 year</option>
                  <option value="custom">Custom date</option>
                </select>

                <input
                  v-if="newKeyExpiry === 'custom'"
                  v-model="newKeyCustomExpiry"
                  type="date"
                  class="w-full mt-2 px-3 py-2 border border-gray-300 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-blue-500"
                />
              </div>
            </div>

            <div class="p-6 border-t border-gray-200 flex gap-3">
              <Button variant="ghost" @click="resetCreateModal()" class="flex-1">
                Cancel
              </Button>
              <Button @click="createApiKey" :disabled="isCreating" class="flex-1">
                <Spinner v-if="isCreating" size="sm" class="mr-2" />
                Create Key
              </Button>
            </div>
          </template>
        </div>
      </div>
    </Teleport>
  </AppLayout>
</template>
