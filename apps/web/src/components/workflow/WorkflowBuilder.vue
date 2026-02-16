<script setup lang="ts">
import { ref, onMounted, computed, nextTick } from 'vue'
import { VueFlow, Handle, Position, MarkerType } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import { MiniMap } from '@vue-flow/minimap'
import type { Node, Edge, Connection } from '@vue-flow/core'
import {
  Mail,
  Clock,
  GitBranch,
  UserCheck,
  Tag,
  Zap,
  Plus,
  Trash2,
  Play,
  Save,
  X,
  ArrowLeft,
  Settings2
} from 'lucide-vue-next'

// Import VueFlow styles
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import '@vue-flow/controls/dist/style.css'
import '@vue-flow/minimap/dist/style.css'

// Node types for the workflow
type WorkflowNodeType = 'trigger' | 'email' | 'delay' | 'condition' | 'action'

interface WorkflowNodeData {
  label: string
  type: WorkflowNodeType
  config: Record<string, unknown>
}

const props = defineProps<{
  workflowId?: string
}>()

const emit = defineEmits<{
  save: [nodes: Node[], edges: Edge[]]
  run: [workflowId: string]
}>()

// State
const isReady = ref(false)
const showNodePalette = ref(false)
const selectedNode = ref<Node | null>(null)
const showNodeConfig = ref(false)

// Initialize with demo workflow
const nodes = ref<Node[]>([
  {
    id: 'trigger-1',
    type: 'workflow',
    position: { x: 300, y: 50 },
    data: {
      label: 'Contact Subscribed',
      type: 'trigger',
      config: { event: 'contact.subscribed' }
    }
  },
  {
    id: 'email-1',
    type: 'workflow',
    position: { x: 300, y: 180 },
    data: {
      label: 'Welcome Email',
      type: 'email',
      config: { subject: 'Welcome to our newsletter!', templateId: 'welcome' }
    }
  },
  {
    id: 'delay-1',
    type: 'workflow',
    position: { x: 300, y: 310 },
    data: {
      label: 'Wait 3 Days',
      type: 'delay',
      config: { duration: 3, unit: 'days' }
    }
  },
  {
    id: 'condition-1',
    type: 'workflow',
    position: { x: 300, y: 440 },
    data: {
      label: 'Opened Email?',
      type: 'condition',
      config: { field: 'email_opened', operator: 'equals', value: 'true' }
    }
  },
  {
    id: 'email-2',
    type: 'workflow',
    position: { x: 100, y: 570 },
    data: {
      label: 'Follow-up Email',
      type: 'email',
      config: { subject: 'Did you check out our features?', templateId: 'followup' }
    }
  },
  {
    id: 'action-1',
    type: 'workflow',
    position: { x: 500, y: 570 },
    data: {
      label: 'Add VIP Tag',
      type: 'action',
      config: { action: 'add_tag', value: 'engaged' }
    }
  }
])

const edges = ref<Edge[]>([
  {
    id: 'e1',
    source: 'trigger-1',
    target: 'email-1',
    type: 'smoothstep',
    animated: true,
    style: { stroke: '#6366f1', strokeWidth: 2 },
    markerEnd: { type: MarkerType.ArrowClosed, color: '#6366f1' }
  },
  {
    id: 'e2',
    source: 'email-1',
    target: 'delay-1',
    type: 'smoothstep',
    animated: true,
    style: { stroke: '#6366f1', strokeWidth: 2 },
    markerEnd: { type: MarkerType.ArrowClosed, color: '#6366f1' }
  },
  {
    id: 'e3',
    source: 'delay-1',
    target: 'condition-1',
    type: 'smoothstep',
    animated: true,
    style: { stroke: '#6366f1', strokeWidth: 2 },
    markerEnd: { type: MarkerType.ArrowClosed, color: '#6366f1' }
  },
  {
    id: 'e4',
    source: 'condition-1',
    target: 'email-2',
    type: 'smoothstep',
    animated: true,
    label: 'No',
    labelStyle: { fill: '#ef4444', fontWeight: 600 },
    style: { stroke: '#ef4444', strokeWidth: 2 },
    markerEnd: { type: MarkerType.ArrowClosed, color: '#ef4444' }
  },
  {
    id: 'e5',
    source: 'condition-1',
    target: 'action-1',
    type: 'smoothstep',
    animated: true,
    label: 'Yes',
    labelStyle: { fill: '#22c55e', fontWeight: 600 },
    style: { stroke: '#22c55e', strokeWidth: 2 },
    markerEnd: { type: MarkerType.ArrowClosed, color: '#22c55e' }
  }
])

// Node templates
const nodeTemplates = [
  {
    type: 'trigger' as WorkflowNodeType,
    label: 'Trigger',
    icon: Zap,
    description: 'Start workflow when event occurs',
    color: 'bg-yellow-500',
    borderColor: 'border-yellow-500'
  },
  {
    type: 'email' as WorkflowNodeType,
    label: 'Send Email',
    icon: Mail,
    description: 'Send an email to contacts',
    color: 'bg-blue-500',
    borderColor: 'border-blue-500'
  },
  {
    type: 'delay' as WorkflowNodeType,
    label: 'Wait/Delay',
    icon: Clock,
    description: 'Wait for a specified time',
    color: 'bg-purple-500',
    borderColor: 'border-purple-500'
  },
  {
    type: 'condition' as WorkflowNodeType,
    label: 'Condition',
    icon: GitBranch,
    description: 'Branch based on conditions',
    color: 'bg-green-500',
    borderColor: 'border-green-500'
  },
  {
    type: 'action' as WorkflowNodeType,
    label: 'Action',
    icon: UserCheck,
    description: 'Perform an action',
    color: 'bg-orange-500',
    borderColor: 'border-orange-500'
  }
]

onMounted(() => {
  // Delay to ensure container is rendered
  nextTick(() => {
    setTimeout(() => {
      isReady.value = true
    }, 100)
  })
})

// Handle connections
const onConnect = (connection: Connection) => {
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
}

// Add new node
const addNode = (template: typeof nodeTemplates[0]) => {
  const id = `${template.type}-${Date.now()}`
  const newNode: Node = {
    id,
    type: 'workflow',
    position: { x: 300, y: nodes.value.length * 130 + 50 },
    data: {
      label: template.label,
      type: template.type,
      config: getDefaultConfig(template.type)
    }
  }
  nodes.value = [...nodes.value, newNode]
  showNodePalette.value = false
}

// Get default config for node type
const getDefaultConfig = (type: WorkflowNodeType): Record<string, unknown> => {
  switch (type) {
    case 'trigger':
      return { event: 'contact.created', filters: {} }
    case 'email':
      return { templateId: '', subject: '', identityId: '' }
    case 'delay':
      return { duration: 1, unit: 'days' }
    case 'condition':
      return { field: '', operator: 'equals', value: '' }
    case 'action':
      return { action: 'add_tag', value: '' }
    default:
      return {}
  }
}

// Select node for editing
const onNodeClick = (event: any) => {
  selectedNode.value = event.node
  showNodeConfig.value = true
}

// Delete selected node
const deleteSelectedNode = () => {
  if (selectedNode.value) {
    const nodeId = selectedNode.value.id
    nodes.value = nodes.value.filter(n => n.id !== nodeId)
    edges.value = edges.value.filter(e => e.source !== nodeId && e.target !== nodeId)
    selectedNode.value = null
    showNodeConfig.value = false
  }
}

// Save workflow
const saveWorkflow = () => {
  emit('save', nodes.value, edges.value)
}

// Run workflow
const runWorkflow = () => {
  if (props.workflowId) {
    emit('run', props.workflowId)
  }
}

// Get node style based on type
const getNodeBg = (type: WorkflowNodeType) => {
  const colors: Record<WorkflowNodeType, string> = {
    trigger: 'bg-yellow-50',
    email: 'bg-blue-50',
    delay: 'bg-purple-50',
    condition: 'bg-green-50',
    action: 'bg-orange-50'
  }
  return colors[type] || 'bg-gray-50'
}

const getNodeBorder = (type: WorkflowNodeType) => {
  const colors: Record<WorkflowNodeType, string> = {
    trigger: 'border-yellow-400',
    email: 'border-blue-400',
    delay: 'border-purple-400',
    condition: 'border-green-400',
    action: 'border-orange-400'
  }
  return colors[type] || 'border-gray-400'
}

const getIconColor = (type: WorkflowNodeType) => {
  const colors: Record<WorkflowNodeType, string> = {
    trigger: 'text-yellow-600',
    email: 'text-blue-600',
    delay: 'text-purple-600',
    condition: 'text-green-600',
    action: 'text-orange-600'
  }
  return colors[type] || 'text-gray-600'
}

// Get icon component for node type
const getNodeIcon = (type: WorkflowNodeType) => {
  const icons: Record<WorkflowNodeType, any> = {
    trigger: Zap,
    email: Mail,
    delay: Clock,
    condition: GitBranch,
    action: UserCheck
  }
  return icons[type] || Tag
}

// Get config display text
const getConfigDisplay = (data: WorkflowNodeData) => {
  switch (data.type) {
    case 'trigger':
      return data.config.event as string
    case 'email':
      return (data.config.subject as string) || 'Configure email...'
    case 'delay':
      return `Wait ${data.config.duration} ${data.config.unit}`
    case 'condition':
      if (data.config.field) {
        return `${data.config.field} ${data.config.operator} ${data.config.value}`
      }
      return 'Configure condition...'
    case 'action':
      if (data.config.value) {
        return `${data.config.action}: ${data.config.value}`
      }
      return 'Configure action...'
    default:
      return ''
  }
}

// Stats
const nodeCount = computed(() => nodes.value.length)
const edgeCount = computed(() => edges.value.length)
</script>

<template>
  <div class="workflow-container">
    <!-- Loading State -->
    <div v-if="!isReady" class="flex items-center justify-center h-full bg-gray-50">
      <div class="text-center">
        <div class="animate-spin rounded-full h-12 w-12 border-b-2 border-indigo-600 mx-auto mb-4"></div>
        <p class="text-gray-500">Loading workflow builder...</p>
      </div>
    </div>

    <!-- Workflow Builder -->
    <div v-else class="workflow-wrapper">
      <!-- Toolbar -->
      <div class="toolbar">
        <button
          @click="showNodePalette = !showNodePalette"
          class="toolbar-btn primary"
        >
          <Plus class="w-4 h-4 mr-2" />
          Add Step
        </button>
        <button @click="saveWorkflow" class="toolbar-btn">
          <Save class="w-4 h-4 mr-2" />
          Save
        </button>
        <button
          v-if="workflowId"
          @click="runWorkflow"
          class="toolbar-btn success"
        >
          <Play class="w-4 h-4 mr-2" />
          Run
        </button>
        <div class="toolbar-stats">
          {{ nodeCount }} steps · {{ edgeCount }} connections
        </div>
      </div>

      <!-- Node Palette -->
      <div v-if="showNodePalette" class="node-palette">
        <div class="palette-header">
          <h3>Add Step</h3>
          <button @click="showNodePalette = false" class="close-btn">
            <X class="w-4 h-4" />
          </button>
        </div>
        <div class="palette-list">
          <button
            v-for="template in nodeTemplates"
            :key="template.type"
            @click="addNode(template)"
            class="palette-item"
          >
            <div :class="['palette-icon', template.color]">
              <component :is="template.icon" class="w-4 h-4 text-white" />
            </div>
            <div class="palette-info">
              <div class="palette-label">{{ template.label }}</div>
              <div class="palette-desc">{{ template.description }}</div>
            </div>
          </button>
        </div>
      </div>

      <!-- VueFlow Canvas -->
      <VueFlow
        v-model:nodes="nodes"
        v-model:edges="edges"
        :default-viewport="{ x: 0, y: 0, zoom: 1 }"
        :min-zoom="0.2"
        :max-zoom="2"
        fit-view-on-init
        @connect="onConnect"
        @node-click="onNodeClick"
        class="vue-flow-canvas"
      >
        <!-- Custom Node Template -->
        <template #node-workflow="{ data }">
          <div :class="['workflow-node', getNodeBg(data.type), getNodeBorder(data.type)]">
            <!-- Target Handle (top) -->
            <Handle
              v-if="data.type !== 'trigger'"
              type="target"
              :position="Position.Top"
              class="node-handle"
            />

            <div class="node-content">
              <component
                :is="getNodeIcon(data.type)"
                class="node-icon"
                :class="getIconColor(data.type)"
              />
              <div class="node-info">
                <div class="node-label">{{ data.label }}</div>
                <div class="node-config">{{ getConfigDisplay(data) }}</div>
              </div>
            </div>

            <!-- Source Handle (bottom) -->
            <Handle
              type="source"
              :position="Position.Bottom"
              class="node-handle"
            />

            <!-- Extra handles for condition nodes -->
            <template v-if="data.type === 'condition'">
              <Handle
                id="yes"
                type="source"
                :position="Position.Right"
                class="node-handle condition-yes"
              />
              <Handle
                id="no"
                type="source"
                :position="Position.Left"
                class="node-handle condition-no"
              />
            </template>
          </div>
        </template>

        <Background :gap="16" :size="1" pattern-color="#e5e7eb" />
        <Controls position="bottom-right" />
        <MiniMap position="bottom-left" />
      </VueFlow>

      <!-- Node Configuration Panel -->
      <div v-if="showNodeConfig && selectedNode" class="config-panel">
        <div class="config-header">
          <h3>Configure Step</h3>
          <div class="config-actions">
            <button @click="deleteSelectedNode" class="delete-btn" title="Delete">
              <Trash2 class="w-4 h-4" />
            </button>
            <button @click="showNodeConfig = false" class="close-btn">
              <X class="w-4 h-4" />
            </button>
          </div>
        </div>

        <div class="config-body">
          <!-- Label -->
          <div class="config-field">
            <label>Label</label>
            <input v-model="selectedNode.data.label" type="text" />
          </div>

          <!-- Trigger Config -->
          <template v-if="selectedNode.data.type === 'trigger'">
            <div class="config-field">
              <label>Trigger Event</label>
              <select v-model="selectedNode.data.config.event">
                <option value="contact.created">Contact Created</option>
                <option value="contact.updated">Contact Updated</option>
                <option value="contact.subscribed">Contact Subscribed</option>
                <option value="email.opened">Email Opened</option>
                <option value="email.clicked">Email Clicked</option>
                <option value="tag.added">Tag Added</option>
                <option value="form.submitted">Form Submitted</option>
              </select>
            </div>
          </template>

          <!-- Email Config -->
          <template v-else-if="selectedNode.data.type === 'email'">
            <div class="config-field">
              <label>Subject</label>
              <input
                v-model="selectedNode.data.config.subject"
                type="text"
                placeholder="Email subject..."
              />
            </div>
            <div class="config-field">
              <label>Template</label>
              <select v-model="selectedNode.data.config.templateId">
                <option value="">Select template...</option>
                <option value="welcome">Welcome Email</option>
                <option value="followup">Follow-up Email</option>
                <option value="reminder">Reminder Email</option>
                <option value="newsletter">Newsletter</option>
              </select>
            </div>
          </template>

          <!-- Delay Config -->
          <template v-else-if="selectedNode.data.type === 'delay'">
            <div class="config-row">
              <div class="config-field">
                <label>Duration</label>
                <input
                  v-model.number="selectedNode.data.config.duration"
                  type="number"
                  min="1"
                />
              </div>
              <div class="config-field">
                <label>Unit</label>
                <select v-model="selectedNode.data.config.unit">
                  <option value="minutes">Minutes</option>
                  <option value="hours">Hours</option>
                  <option value="days">Days</option>
                  <option value="weeks">Weeks</option>
                </select>
              </div>
            </div>
          </template>

          <!-- Condition Config -->
          <template v-else-if="selectedNode.data.type === 'condition'">
            <div class="config-field">
              <label>Field</label>
              <select v-model="selectedNode.data.config.field">
                <option value="">Select field...</option>
                <option value="email_opened">Email Opened</option>
                <option value="email_clicked">Email Clicked</option>
                <option value="tag_exists">Has Tag</option>
                <option value="engagement_score">Engagement Score</option>
              </select>
            </div>
            <div class="config-field">
              <label>Operator</label>
              <select v-model="selectedNode.data.config.operator">
                <option value="equals">Equals</option>
                <option value="not_equals">Not Equals</option>
                <option value="greater_than">Greater Than</option>
                <option value="less_than">Less Than</option>
                <option value="contains">Contains</option>
              </select>
            </div>
            <div class="config-field">
              <label>Value</label>
              <input v-model="selectedNode.data.config.value" type="text" />
            </div>
          </template>

          <!-- Action Config -->
          <template v-else-if="selectedNode.data.type === 'action'">
            <div class="config-field">
              <label>Action Type</label>
              <select v-model="selectedNode.data.config.action">
                <option value="add_tag">Add Tag</option>
                <option value="remove_tag">Remove Tag</option>
                <option value="add_to_list">Add to List</option>
                <option value="remove_from_list">Remove from List</option>
                <option value="update_field">Update Field</option>
                <option value="send_webhook">Send Webhook</option>
              </select>
            </div>
            <div class="config-field">
              <label>Value</label>
              <input
                v-model="selectedNode.data.config.value"
                type="text"
                placeholder="Tag name, list ID, etc."
              />
            </div>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.workflow-container {
  width: 100%;
  height: calc(100vh - 120px);
  min-height: 500px;
  position: relative;
  background: #f9fafb;
}

.workflow-wrapper {
  width: 100%;
  height: 100%;
  position: relative;
}

.vue-flow-canvas {
  width: 100%;
  height: 100%;
}

/* Toolbar */
.toolbar {
  position: absolute;
  top: 16px;
  left: 16px;
  z-index: 10;
  display: flex;
  align-items: center;
  gap: 8px;
  background: white;
  padding: 8px;
  border-radius: 12px;
  box-shadow: 0 4px 6px -1px rgb(0 0 0 / 0.1);
}

.toolbar-btn {
  display: flex;
  align-items: center;
  padding: 8px 12px;
  border-radius: 8px;
  font-size: 14px;
  font-weight: 500;
  transition: all 0.2s;
  background: #f3f4f6;
  color: #374151;
  border: none;
  cursor: pointer;
}

.toolbar-btn:hover {
  background: #e5e7eb;
}

.toolbar-btn.primary {
  background: #6366f1;
  color: white;
}

.toolbar-btn.primary:hover {
  background: #4f46e5;
}

.toolbar-btn.success {
  background: #22c55e;
  color: white;
}

.toolbar-btn.success:hover {
  background: #16a34a;
}

.toolbar-stats {
  padding: 8px 12px;
  font-size: 12px;
  color: #6b7280;
}

/* Node Palette */
.node-palette {
  position: absolute;
  top: 80px;
  left: 16px;
  z-index: 20;
  width: 280px;
  background: white;
  border-radius: 12px;
  box-shadow: 0 10px 25px -5px rgb(0 0 0 / 0.1);
  overflow: hidden;
}

.palette-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px;
  border-bottom: 1px solid #e5e7eb;
}

.palette-header h3 {
  font-weight: 600;
  color: #111827;
}

.palette-list {
  padding: 8px;
}

.palette-item {
  display: flex;
  align-items: flex-start;
  width: 100%;
  padding: 12px;
  border-radius: 8px;
  border: 1px solid #e5e7eb;
  margin-bottom: 8px;
  text-align: left;
  background: white;
  cursor: pointer;
  transition: all 0.2s;
}

.palette-item:hover {
  border-color: #6366f1;
  background: #f5f3ff;
}

.palette-icon {
  padding: 8px;
  border-radius: 8px;
  margin-right: 12px;
  flex-shrink: 0;
}

.palette-info {
  flex: 1;
}

.palette-label {
  font-weight: 500;
  color: #111827;
}

.palette-desc {
  font-size: 12px;
  color: #6b7280;
  margin-top: 2px;
}

/* Workflow Node */
.workflow-node {
  padding: 12px 16px;
  border-radius: 8px;
  border: 2px solid;
  min-width: 180px;
  box-shadow: 0 2px 4px rgb(0 0 0 / 0.1);
  transition: all 0.2s;
}

.workflow-node:hover {
  box-shadow: 0 4px 8px rgb(0 0 0 / 0.15);
  transform: translateY(-1px);
}

.node-content {
  display: flex;
  align-items: flex-start;
  gap: 10px;
}

.node-icon {
  width: 20px;
  height: 20px;
  flex-shrink: 0;
  margin-top: 2px;
}

.node-info {
  flex: 1;
  min-width: 0;
}

.node-label {
  font-weight: 600;
  color: #111827;
  font-size: 14px;
}

.node-config {
  font-size: 11px;
  color: #6b7280;
  margin-top: 4px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* Node Handles */
.node-handle {
  width: 12px !important;
  height: 12px !important;
  background: #6366f1 !important;
  border: 2px solid white !important;
  border-radius: 50%;
}

.node-handle:hover {
  transform: scale(1.3);
}

.condition-yes {
  background: #22c55e !important;
}

.condition-no {
  background: #ef4444 !important;
}

/* Config Panel */
.config-panel {
  position: absolute;
  top: 16px;
  right: 16px;
  z-index: 20;
  width: 320px;
  background: white;
  border-radius: 12px;
  box-shadow: 0 10px 25px -5px rgb(0 0 0 / 0.1);
  overflow: hidden;
}

.config-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px;
  border-bottom: 1px solid #e5e7eb;
}

.config-header h3 {
  font-weight: 600;
  color: #111827;
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
  border: none;
  cursor: pointer;
}

.delete-btn:hover {
  background: #fee2e2;
}

.close-btn {
  padding: 6px;
  border-radius: 6px;
  color: #6b7280;
  background: transparent;
  border: none;
  cursor: pointer;
}

.close-btn:hover {
  background: #f3f4f6;
}

.config-body {
  padding: 16px;
  max-height: 400px;
  overflow-y: auto;
}

.config-field {
  margin-bottom: 16px;
}

.config-field label {
  display: block;
  font-size: 13px;
  font-weight: 500;
  color: #374151;
  margin-bottom: 6px;
}

.config-field input,
.config-field select {
  width: 100%;
  padding: 10px 12px;
  border: 1px solid #d1d5db;
  border-radius: 8px;
  font-size: 14px;
  transition: all 0.2s;
}

.config-field input:focus,
.config-field select:focus {
  outline: none;
  border-color: #6366f1;
  box-shadow: 0 0 0 3px rgb(99 102 241 / 0.1);
}

.config-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
</style>

<style>
/* Global VueFlow styles */
.vue-flow__minimap {
  background: white;
  border-radius: 8px;
  box-shadow: 0 2px 4px rgb(0 0 0 / 0.1);
}

.vue-flow__controls {
  background: white;
  border-radius: 8px;
  box-shadow: 0 2px 4px rgb(0 0 0 / 0.1);
}

.vue-flow__controls-button {
  background: white;
  border: none;
  width: 28px;
  height: 28px;
}

.vue-flow__controls-button:hover {
  background: #f3f4f6;
}

.vue-flow__edge-path {
  stroke-width: 2;
}

.vue-flow__edge-textbg {
  fill: white;
}
</style>
