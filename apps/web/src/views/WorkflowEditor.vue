<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { VueFlow, Handle, Position, MarkerType, useVueFlow } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import { MiniMap } from '@vue-flow/minimap'
import type { Node, Edge, Connection, NodeMouseEvent } from '@vue-flow/core'
import {
  Mail,
  Clock,
  GitBranch,
  UserCheck,
  Tag,
  Zap,
  Trash2,
  Play,
  Save,
  X,
  ArrowLeft,
  Settings2,
  GripVertical,
  ChevronRight,
  AlertCircle,
  Webhook,
  Filter,
  Users,
  CheckCircle2
} from 'lucide-vue-next'

// Import VueFlow styles
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import '@vue-flow/controls/dist/style.css'
import '@vue-flow/minimap/dist/style.css'

// Types
type WorkflowNodeType = 'trigger' | 'email' | 'delay' | 'condition' | 'action' | 'webhook'

interface WorkflowNodeData {
  label: string
  type: WorkflowNodeType
  config: Record<string, unknown>
}

interface Automation {
  id: number
  uuid: string
  name: string
  description: string
  status: string
  triggerType: string
  triggerConfig: Record<string, unknown>
  workflow: {
    nodes: Node[]
    edges: Edge[]
  }
}

const route = useRoute()
const router = useRouter()
const { onConnect } = useVueFlow()

// State
const isLoading = ref(true)
const isSaving = ref(false)
const automation = ref<Automation | null>(null)
const automationName = ref('New Automation')
const automationDescription = ref('')
const selectedNode = ref<Node | null>(null)
const isNewAutomation = computed(() => route.name === 'automation-new')

// Nodes and Edges
const nodes = ref<Node[]>([])
const edges = ref<Edge[]>([])

// Node palette items
const nodeTypes = [
  {
    category: 'Triggers',
    items: [
      { type: 'trigger' as WorkflowNodeType, label: 'Contact Added', icon: Users, description: 'When a contact is added to a list', color: 'yellow' },
      { type: 'trigger' as WorkflowNodeType, label: 'Form Submitted', icon: CheckCircle2, description: 'When a form is submitted', color: 'yellow' },
      { type: 'trigger' as WorkflowNodeType, label: 'Tag Added', icon: Tag, description: 'When a tag is added to contact', color: 'yellow' },
    ]
  },
  {
    category: 'Actions',
    items: [
      { type: 'email' as WorkflowNodeType, label: 'Send Email', icon: Mail, description: 'Send an email to the contact', color: 'blue' },
      { type: 'action' as WorkflowNodeType, label: 'Add Tag', icon: Tag, description: 'Add a tag to the contact', color: 'orange' },
      { type: 'action' as WorkflowNodeType, label: 'Update Contact', icon: UserCheck, description: 'Update contact properties', color: 'orange' },
      { type: 'webhook' as WorkflowNodeType, label: 'Webhook', icon: Webhook, description: 'Send data to a webhook URL', color: 'purple' },
    ]
  },
  {
    category: 'Flow Control',
    items: [
      { type: 'delay' as WorkflowNodeType, label: 'Wait/Delay', icon: Clock, description: 'Wait for a specified time', color: 'purple' },
      { type: 'condition' as WorkflowNodeType, label: 'If/Else', icon: GitBranch, description: 'Branch based on conditions', color: 'green' },
      { type: 'condition' as WorkflowNodeType, label: 'Filter', icon: Filter, description: 'Filter contacts by criteria', color: 'green' },
    ]
  }
]

// Load automation data
onMounted(async () => {
  if (!isNewAutomation.value && route.params.uuid) {
    await loadAutomation(route.params.uuid as string)
  } else {
    // New automation - add default trigger node
    nodes.value = [{
      id: 'trigger-1',
      type: 'workflow',
      position: { x: 400, y: 100 },
      data: {
        label: 'Contact Added',
        type: 'trigger',
        config: { event: 'contact.created' }
      }
    }]
  }
  isLoading.value = false
})

const loadAutomation = async (uuid: string) => {
  try {
    const token = localStorage.getItem('token')
    const response = await fetch(`/api/v1/automations/${uuid}`, {
      headers: { 'Authorization': `Bearer ${token}` }
    })

    if (response.ok) {
      const data = await response.json()
      automation.value = data.data
      automationName.value = data.data.name
      automationDescription.value = data.data.description || ''

      // Load workflow nodes and edges
      if (data.data.workflow) {
        nodes.value = data.data.workflow.nodes || []
        edges.value = data.data.workflow.edges || []
      }
    }
  } catch (error) {
    console.error('Failed to load automation:', error)
  }
}

// Handle node connections
onConnect((connection: Connection) => {
  const newEdge: Edge = {
    id: `e-${connection.source}-${connection.target}-${Date.now()}`,
    source: connection.source!,
    target: connection.target!,
    sourceHandle: connection.sourceHandle,
    targetHandle: connection.targetHandle,
    type: 'smoothstep',
    animated: true,
    style: { stroke: '#6366f1', strokeWidth: 2 },
    markerEnd: { type: MarkerType.ArrowClosed, color: '#6366f1' }
  }
  edges.value = [...edges.value, newEdge]
})

// Add node from palette
const addNode = (item: typeof nodeTypes[0]['items'][0]) => {
  const id = `${item.type}-${Date.now()}`
  const newNode: Node = {
    id,
    type: 'workflow',
    position: { x: 400, y: nodes.value.length * 150 + 100 },
    data: {
      label: item.label,
      type: item.type,
      config: getDefaultConfig(item.type, item.label)
    }
  }
  nodes.value = [...nodes.value, newNode]
}

// Drag and drop support
const onDragStart = (event: DragEvent, item: typeof nodeTypes[0]['items'][0]) => {
  if (event.dataTransfer) {
    event.dataTransfer.setData('application/json', JSON.stringify(item))
    event.dataTransfer.effectAllowed = 'move'
  }
}

const onDrop = (event: DragEvent) => {
  event.preventDefault()
  if (event.dataTransfer) {
    const data = event.dataTransfer.getData('application/json')
    if (data) {
      const item = JSON.parse(data)
      const bounds = (event.target as HTMLElement).closest('.vue-flow')?.getBoundingClientRect()
      if (bounds) {
        const id = `${item.type}-${Date.now()}`
        const newNode: Node = {
          id,
          type: 'workflow',
          position: {
            x: event.clientX - bounds.left - 90,
            y: event.clientY - bounds.top - 30
          },
          data: {
            label: item.label,
            type: item.type,
            config: getDefaultConfig(item.type, item.label)
          }
        }
        nodes.value = [...nodes.value, newNode]
      }
    }
  }
}

const onDragOver = (event: DragEvent) => {
  event.preventDefault()
  if (event.dataTransfer) {
    event.dataTransfer.dropEffect = 'move'
  }
}

// Get default config for node type
const getDefaultConfig = (type: WorkflowNodeType, label: string): Record<string, unknown> => {
  switch (type) {
    case 'trigger':
      return { event: label === 'Tag Added' ? 'tag.added' : label === 'Form Submitted' ? 'form.submitted' : 'contact.created' }
    case 'email':
      return { templateId: '', subject: '', identityId: '' }
    case 'delay':
      return { duration: 1, unit: 'days' }
    case 'condition':
      return { field: '', operator: 'equals', value: '' }
    case 'action':
      return { action: label === 'Add Tag' ? 'add_tag' : 'update_field', value: '' }
    case 'webhook':
      return { url: '', method: 'POST' }
    default:
      return {}
  }
}

// Node click handler
const onNodeClick = ({ node }: NodeMouseEvent) => {
  selectedNode.value = node
}

// Delete selected node
const deleteSelectedNode = () => {
  if (selectedNode.value) {
    const nodeId = selectedNode.value.id
    nodes.value = nodes.value.filter(n => n.id !== nodeId)
    edges.value = edges.value.filter(e => e.source !== nodeId && e.target !== nodeId)
    selectedNode.value = null
  }
}

// Close config panel
const closeConfigPanel = () => {
  selectedNode.value = null
}

// Save automation
const saveAutomation = async () => {
  isSaving.value = true
  try {
    const token = localStorage.getItem('token')
    const workflow = { nodes: nodes.value, edges: edges.value }

    const body = {
      name: automationName.value,
      description: automationDescription.value,
      triggerType: nodes.value.find(n => n.data.type === 'trigger')?.data.config?.event || 'contact_added',
      triggerConfig: {},
      workflow
    }

    let response
    if (isNewAutomation.value) {
      response = await fetch('/api/v1/automations', {
        method: 'POST',
        headers: {
          'Authorization': `Bearer ${token}`,
          'Content-Type': 'application/json'
        },
        body: JSON.stringify(body)
      })
    } else {
      response = await fetch(`/api/v1/automations/${route.params.uuid}`, {
        method: 'PUT',
        headers: {
          'Authorization': `Bearer ${token}`,
          'Content-Type': 'application/json'
        },
        body: JSON.stringify(body)
      })
    }

    if (response.ok) {
      const data = await response.json()
      if (isNewAutomation.value) {
        // Redirect to the new automation
        router.replace(`/automations/${data.data.uuid}`)
      }
    }
  } catch (error) {
    console.error('Failed to save automation:', error)
  } finally {
    isSaving.value = false
  }
}

// Activate automation
const activateAutomation = async () => {
  if (!route.params.uuid) return
  try {
    const token = localStorage.getItem('token')
    await fetch(`/api/v1/automations/${route.params.uuid}/activate`, {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (automation.value) {
      automation.value.status = 'active'
    }
  } catch (error) {
    console.error('Failed to activate automation:', error)
  }
}

// Go back
const goBack = () => {
  router.push('/automations')
}

// Get node colors
const getNodeColors = (type: WorkflowNodeType) => {
  const colors: Record<WorkflowNodeType, { bg: string; border: string; icon: string }> = {
    trigger: { bg: 'bg-yellow-50', border: 'border-yellow-400', icon: 'text-yellow-600' },
    email: { bg: 'bg-blue-50', border: 'border-blue-400', icon: 'text-blue-600' },
    delay: { bg: 'bg-purple-50', border: 'border-purple-400', icon: 'text-purple-600' },
    condition: { bg: 'bg-green-50', border: 'border-green-400', icon: 'text-green-600' },
    action: { bg: 'bg-orange-50', border: 'border-orange-400', icon: 'text-orange-600' },
    webhook: { bg: 'bg-indigo-50', border: 'border-indigo-400', icon: 'text-indigo-600' }
  }
  return colors[type] || { bg: 'bg-gray-50', border: 'border-gray-400', icon: 'text-gray-600' }
}

const getNodeIcon = (type: WorkflowNodeType) => {
  const icons: Record<WorkflowNodeType, any> = {
    trigger: Zap,
    email: Mail,
    delay: Clock,
    condition: GitBranch,
    action: UserCheck,
    webhook: Webhook
  }
  return icons[type] || Tag
}

const getConfigSummary = (data: WorkflowNodeData) => {
  switch (data.type) {
    case 'trigger':
      return data.config.event as string
    case 'email':
      return (data.config.subject as string) || 'Configure email...'
    case 'delay':
      return `Wait ${data.config.duration} ${data.config.unit}`
    case 'condition':
      return data.config.field ? `${data.config.field} ${data.config.operator} ${data.config.value}` : 'Set condition...'
    case 'action':
      return data.config.value ? `${data.config.action}: ${data.config.value}` : 'Configure action...'
    case 'webhook':
      return (data.config.url as string) || 'Set webhook URL...'
    default:
      return ''
  }
}

// Stats
const nodeCount = computed(() => nodes.value.length)
const edgeCount = computed(() => edges.value.length)
</script>

<template>
  <div class="workflow-editor">
    <!-- Header -->
    <header class="editor-header">
      <div class="header-left">
        <button @click="goBack" class="back-btn">
          <ArrowLeft class="w-4 h-4" />
        </button>
        <div class="header-info">
          <input
            v-model="automationName"
            type="text"
            class="name-input"
            placeholder="Automation name..."
          />
          <div class="header-meta">
            <span class="meta-item">{{ nodeCount }} steps</span>
            <span class="meta-divider">·</span>
            <span class="meta-item">{{ edgeCount }} connections</span>
            <template v-if="automation">
              <span class="meta-divider">·</span>
              <span :class="['status-badge', automation.status === 'active' ? 'status-active' : automation.status === 'paused' ? 'status-paused' : 'status-draft']">
                {{ automation.status }}
              </span>
            </template>
          </div>
        </div>
      </div>
      <div class="header-actions">
        <button @click="saveAutomation" :disabled="isSaving" class="btn btn-secondary">
          <Save class="w-4 h-4 mr-2" />
          {{ isSaving ? 'Saving...' : 'Save' }}
        </button>
        <button v-if="!isNewAutomation" @click="activateAutomation" class="btn btn-primary">
          <Play class="w-4 h-4 mr-2" />
          Activate
        </button>
      </div>
    </header>

    <!-- Main Content -->
    <div class="editor-content">
      <!-- Left Sidebar: Node Palette -->
      <aside class="left-sidebar">
        <div class="sidebar-header">
          <h3>Add Steps</h3>
          <p>Drag and drop to canvas</p>
        </div>
        <div class="node-palette">
          <div v-for="category in nodeTypes" :key="category.category" class="node-category">
            <h4 class="category-title">{{ category.category }}</h4>
            <div class="category-items">
              <div
                v-for="item in category.items"
                :key="item.label"
                class="palette-item"
                draggable="true"
                @dragstart="onDragStart($event, item)"
                @click="addNode(item)"
              >
                <div :class="['item-icon', `bg-${item.color}-100`]">
                  <component :is="item.icon" :class="['w-4 h-4', `text-${item.color}-600`]" />
                </div>
                <div class="item-info">
                  <span class="item-label">{{ item.label }}</span>
                  <span class="item-desc">{{ item.description }}</span>
                </div>
                <GripVertical class="w-4 h-4 text-gray-300 drag-handle" />
              </div>
            </div>
          </div>
        </div>
      </aside>

      <!-- Canvas -->
      <main class="canvas-area" @drop="onDrop" @dragover="onDragOver">
        <div v-if="isLoading" class="loading-overlay">
          <div class="animate-spin rounded-full h-10 w-10 border-b-2 border-indigo-600"></div>
          <p>Loading workflow...</p>
        </div>

        <VueFlow
          v-else
          v-model:nodes="nodes"
          v-model:edges="edges"
          :default-viewport="{ x: 0, y: 0, zoom: 0.9 }"
          :min-zoom="0.2"
          :max-zoom="2"
          fit-view-on-init
          @node-click="onNodeClick"
          class="workflow-canvas"
        >
          <!-- Custom Node -->
          <template #node-workflow="{ data }">
            <div :class="['workflow-node', getNodeColors(data.type).bg, getNodeColors(data.type).border]">
              <Handle
                v-if="data.type !== 'trigger'"
                type="target"
                :position="Position.Top"
                class="node-handle"
              />

              <div class="node-body">
                <component
                  :is="getNodeIcon(data.type)"
                  :class="['node-icon', getNodeColors(data.type).icon]"
                />
                <div class="node-text">
                  <div class="node-label">{{ data.label }}</div>
                  <div class="node-config">{{ getConfigSummary(data) }}</div>
                </div>
              </div>

              <Handle
                type="source"
                :position="Position.Bottom"
                class="node-handle"
              />

              <!-- Condition node extra handles -->
              <template v-if="data.type === 'condition'">
                <Handle id="yes" type="source" :position="Position.Right" class="node-handle handle-yes" />
                <Handle id="no" type="source" :position="Position.Left" class="node-handle handle-no" />
              </template>
            </div>
          </template>

          <Background :gap="20" :size="1" pattern-color="#e5e7eb" />
          <Controls position="bottom-right" />
          <MiniMap position="bottom-left" />
        </VueFlow>
      </main>

      <!-- Right Sidebar: Node Configuration -->
      <aside :class="['right-sidebar', { 'is-open': selectedNode }]">
        <div v-if="selectedNode" class="config-panel">
          <div class="config-header">
            <div class="config-title">
              <component
                :is="getNodeIcon(selectedNode.data.type)"
                :class="['w-5 h-5', getNodeColors(selectedNode.data.type).icon]"
              />
              <span>Configure {{ selectedNode.data.type }}</span>
            </div>
            <div class="config-actions">
              <button @click="deleteSelectedNode" class="delete-btn" title="Delete step">
                <Trash2 class="w-4 h-4" />
              </button>
              <button @click="closeConfigPanel" class="close-btn">
                <X class="w-4 h-4" />
              </button>
            </div>
          </div>

          <div class="config-body">
            <!-- Common: Label -->
            <div class="form-group">
              <label>Step Name</label>
              <input v-model="selectedNode.data.label" type="text" class="form-input" />
            </div>

            <!-- Trigger Config -->
            <template v-if="selectedNode.data.type === 'trigger'">
              <div class="form-group">
                <label>Trigger Event</label>
                <select v-model="selectedNode.data.config.event" class="form-select">
                  <option value="contact.created">Contact Created</option>
                  <option value="contact.subscribed">Contact Subscribed</option>
                  <option value="tag.added">Tag Added</option>
                  <option value="form.submitted">Form Submitted</option>
                  <option value="email.opened">Email Opened</option>
                  <option value="email.clicked">Link Clicked</option>
                </select>
              </div>
              <div class="form-info">
                <AlertCircle class="w-4 h-4 text-blue-500" />
                <span>This automation will start when this event occurs.</span>
              </div>
            </template>

            <!-- Email Config -->
            <template v-else-if="selectedNode.data.type === 'email'">
              <div class="form-group">
                <label>Email Subject</label>
                <input
                  v-model="selectedNode.data.config.subject"
                  type="text"
                  class="form-input"
                  placeholder="Enter subject line..."
                />
              </div>
              <div class="form-group">
                <label>Email Template</label>
                <select v-model="selectedNode.data.config.templateId" class="form-select">
                  <option value="">Select a template...</option>
                  <option value="welcome">Welcome Email</option>
                  <option value="followup">Follow-up Email</option>
                  <option value="reminder">Reminder Email</option>
                  <option value="newsletter">Newsletter</option>
                </select>
              </div>
              <div class="form-group">
                <label>Send From</label>
                <select v-model="selectedNode.data.config.identityId" class="form-select">
                  <option value="">Default identity</option>
                  <option value="1">hello@mailat.co</option>
                  <option value="2">support@mailat.co</option>
                </select>
              </div>
            </template>

            <!-- Delay Config -->
            <template v-else-if="selectedNode.data.type === 'delay'">
              <div class="form-row">
                <div class="form-group">
                  <label>Duration</label>
                  <input
                    v-model.number="selectedNode.data.config.duration"
                    type="number"
                    min="1"
                    class="form-input"
                  />
                </div>
                <div class="form-group">
                  <label>Unit</label>
                  <select v-model="selectedNode.data.config.unit" class="form-select">
                    <option value="minutes">Minutes</option>
                    <option value="hours">Hours</option>
                    <option value="days">Days</option>
                    <option value="weeks">Weeks</option>
                  </select>
                </div>
              </div>
              <div class="form-info">
                <Clock class="w-4 h-4 text-purple-500" />
                <span>Contacts will wait here before moving to the next step.</span>
              </div>
            </template>

            <!-- Condition Config -->
            <template v-else-if="selectedNode.data.type === 'condition'">
              <div class="form-group">
                <label>Condition Field</label>
                <select v-model="selectedNode.data.config.field" class="form-select">
                  <option value="">Select a field...</option>
                  <option value="email_opened">Email Opened</option>
                  <option value="email_clicked">Link Clicked</option>
                  <option value="tag_exists">Has Tag</option>
                  <option value="engagement_score">Engagement Score</option>
                  <option value="custom_field">Custom Field</option>
                </select>
              </div>
              <div class="form-group">
                <label>Operator</label>
                <select v-model="selectedNode.data.config.operator" class="form-select">
                  <option value="equals">Equals</option>
                  <option value="not_equals">Not Equals</option>
                  <option value="greater_than">Greater Than</option>
                  <option value="less_than">Less Than</option>
                  <option value="contains">Contains</option>
                </select>
              </div>
              <div class="form-group">
                <label>Value</label>
                <input v-model="selectedNode.data.config.value" type="text" class="form-input" placeholder="Enter value..." />
              </div>
              <div class="form-info">
                <GitBranch class="w-4 h-4 text-green-500" />
                <span>Contacts will branch based on this condition.</span>
              </div>
            </template>

            <!-- Action Config -->
            <template v-else-if="selectedNode.data.type === 'action'">
              <div class="form-group">
                <label>Action Type</label>
                <select v-model="selectedNode.data.config.action" class="form-select">
                  <option value="add_tag">Add Tag</option>
                  <option value="remove_tag">Remove Tag</option>
                  <option value="add_to_list">Add to List</option>
                  <option value="remove_from_list">Remove from List</option>
                  <option value="update_field">Update Field</option>
                </select>
              </div>
              <div class="form-group">
                <label>Value</label>
                <input
                  v-model="selectedNode.data.config.value"
                  type="text"
                  class="form-input"
                  placeholder="Tag name, list ID, etc."
                />
              </div>
            </template>

            <!-- Webhook Config -->
            <template v-else-if="selectedNode.data.type === 'webhook'">
              <div class="form-group">
                <label>Webhook URL</label>
                <input
                  v-model="selectedNode.data.config.url"
                  type="url"
                  class="form-input"
                  placeholder="https://..."
                />
              </div>
              <div class="form-group">
                <label>HTTP Method</label>
                <select v-model="selectedNode.data.config.method" class="form-select">
                  <option value="POST">POST</option>
                  <option value="GET">GET</option>
                  <option value="PUT">PUT</option>
                </select>
              </div>
            </template>
          </div>
        </div>

        <!-- Empty state when no node selected -->
        <div v-else class="config-empty">
          <Settings2 class="w-8 h-8 text-gray-300" />
          <p>Select a step to configure</p>
        </div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.workflow-editor {
  display: flex;
  flex-direction: column;
  height: 100%;
  width: 100%;
  background: #f9fafb;
}

/* Header */
.editor-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 20px;
  background: white;
  border-bottom: 1px solid #e5e7eb;
  flex-shrink: 0;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 16px;
}

.back-btn {
  width: 36px;
  height: 36px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  color: #6b7280;
  transition: all 0.2s;
}

.back-btn:hover {
  background: #f3f4f6;
  color: #374151;
}

.header-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.name-input {
  font-size: 18px;
  font-weight: 600;
  color: #111827;
  border: none;
  background: transparent;
  padding: 0;
  outline: none;
  width: 300px;
}

.name-input:focus {
  outline: none;
}

.header-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: #6b7280;
}

.meta-divider {
  color: #d1d5db;
}

.status-badge {
  padding: 2px 8px;
  border-radius: 9999px;
  font-size: 11px;
  font-weight: 500;
  text-transform: capitalize;
}

.status-active { background: #dcfce7; color: #166534; }
.status-paused { background: #fef3c7; color: #92400e; }
.status-draft { background: #f3f4f6; color: #4b5563; }

.header-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.btn {
  display: flex;
  align-items: center;
  padding: 8px 16px;
  border-radius: 8px;
  font-size: 14px;
  font-weight: 500;
  transition: all 0.2s;
  cursor: pointer;
}

.btn-secondary {
  background: #f3f4f6;
  color: #374151;
}

.btn-secondary:hover {
  background: #e5e7eb;
}

.btn-primary {
  background: #6366f1;
  color: white;
}

.btn-primary:hover {
  background: #4f46e5;
}

/* Content Layout */
.editor-content {
  display: flex;
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

/* Left Sidebar */
.left-sidebar {
  width: 280px;
  background: white;
  border-right: 1px solid #e5e7eb;
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
}

.sidebar-header {
  padding: 16px 20px;
  border-bottom: 1px solid #f3f4f6;
}

.sidebar-header h3 {
  font-size: 14px;
  font-weight: 600;
  color: #111827;
  margin-bottom: 2px;
}

.sidebar-header p {
  font-size: 12px;
  color: #9ca3af;
}

.node-palette {
  flex: 1;
  overflow-y: auto;
  padding: 12px;
}

.node-category {
  margin-bottom: 20px;
}

.category-title {
  font-size: 11px;
  font-weight: 600;
  color: #9ca3af;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  padding: 0 8px;
  margin-bottom: 8px;
}

.category-items {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.palette-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  background: #fafafa;
  border: 1px solid #e5e7eb;
  border-radius: 10px;
  cursor: grab;
  transition: all 0.2s;
}

.palette-item:hover {
  border-color: #6366f1;
  background: white;
  box-shadow: 0 2px 8px rgb(99 102 241 / 0.1);
}

.palette-item:active {
  cursor: grabbing;
}

.item-icon {
  width: 36px;
  height: 36px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  flex-shrink: 0;
}

.item-info {
  flex: 1;
  min-width: 0;
}

.item-label {
  font-size: 13px;
  font-weight: 500;
  color: #374151;
  display: block;
}

.item-desc {
  font-size: 11px;
  color: #9ca3af;
  display: block;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.drag-handle {
  opacity: 0.5;
}

/* Canvas */
.canvas-area {
  flex: 1;
  position: relative;
  min-width: 0;
}

.loading-overlay {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  background: rgba(249, 250, 251, 0.9);
  z-index: 10;
  gap: 16px;
  color: #6b7280;
}

.workflow-canvas {
  width: 100%;
  height: 100%;
}

/* Workflow Node */
.workflow-node {
  padding: 14px 18px;
  border-radius: 10px;
  border: 2px solid;
  min-width: 200px;
  box-shadow: 0 2px 8px rgb(0 0 0 / 0.08);
  transition: all 0.2s;
  background: white;
}

.workflow-node:hover {
  box-shadow: 0 4px 12px rgb(0 0 0 / 0.12);
  transform: translateY(-1px);
}

.node-body {
  display: flex;
  align-items: flex-start;
  gap: 12px;
}

.node-icon {
  width: 22px;
  height: 22px;
  flex-shrink: 0;
  margin-top: 1px;
}

.node-text {
  flex: 1;
  min-width: 0;
}

.node-label {
  font-weight: 600;
  font-size: 14px;
  color: #111827;
  margin-bottom: 3px;
}

.node-config {
  font-size: 11px;
  color: #6b7280;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.node-handle {
  width: 14px !important;
  height: 14px !important;
  background: #6366f1 !important;
  border: 3px solid white !important;
  border-radius: 50%;
  box-shadow: 0 1px 3px rgb(0 0 0 / 0.2);
}

.node-handle:hover {
  transform: scale(1.2);
}

.handle-yes {
  background: #22c55e !important;
}

.handle-no {
  background: #ef4444 !important;
}

/* Right Sidebar */
.right-sidebar {
  width: 0;
  background: white;
  border-left: 1px solid #e5e7eb;
  transition: width 0.3s ease;
  overflow: hidden;
  flex-shrink: 0;
}

.right-sidebar.is-open {
  width: 340px;
}

.config-panel {
  width: 340px;
  height: 100%;
  display: flex;
  flex-direction: column;
}

.config-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid #e5e7eb;
  flex-shrink: 0;
}

.config-title {
  display: flex;
  align-items: center;
  gap: 10px;
  font-weight: 600;
  color: #111827;
  text-transform: capitalize;
}

.config-actions {
  display: flex;
  gap: 8px;
}

.delete-btn {
  padding: 6px;
  border-radius: 6px;
  color: #ef4444;
  background: #fef2f2;
  transition: all 0.2s;
}

.delete-btn:hover {
  background: #fee2e2;
}

.close-btn {
  padding: 6px;
  border-radius: 6px;
  color: #6b7280;
  transition: all 0.2s;
}

.close-btn:hover {
  background: #f3f4f6;
}

.config-body {
  flex: 1;
  overflow-y: auto;
  padding: 20px;
}

.form-group {
  margin-bottom: 18px;
}

.form-group label {
  display: block;
  font-size: 13px;
  font-weight: 500;
  color: #374151;
  margin-bottom: 6px;
}

.form-input,
.form-select {
  width: 100%;
  padding: 10px 14px;
  border: 1px solid #d1d5db;
  border-radius: 8px;
  font-size: 14px;
  transition: all 0.2s;
  background: white;
}

.form-input:focus,
.form-select:focus {
  outline: none;
  border-color: #6366f1;
  box-shadow: 0 0 0 3px rgb(99 102 241 / 0.1);
}

.form-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.form-info {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 12px;
  background: #f9fafb;
  border-radius: 8px;
  font-size: 12px;
  color: #6b7280;
  margin-top: 16px;
}

.form-info svg {
  flex-shrink: 0;
  margin-top: 1px;
}

.config-empty {
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  color: #9ca3af;
  font-size: 14px;
  gap: 12px;
  padding: 40px;
  text-align: center;
}
</style>

<style>
/* Global VueFlow overrides */
.vue-flow__minimap {
  background: white;
  border-radius: 8px;
  box-shadow: 0 2px 8px rgb(0 0 0 / 0.1);
  overflow: hidden;
}

.vue-flow__controls {
  background: white;
  border-radius: 8px;
  box-shadow: 0 2px 8px rgb(0 0 0 / 0.1);
  overflow: hidden;
}

.vue-flow__controls-button {
  background: white;
  border: none;
  width: 30px;
  height: 30px;
}

.vue-flow__controls-button:hover {
  background: #f3f4f6;
}
</style>
