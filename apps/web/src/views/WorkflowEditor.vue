<script setup lang="ts">
import AppLayout from '@/components/layout/AppLayout.vue'
import { ref, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useRoute, useRouter, onBeforeRouteLeave } from 'vue-router'
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
  Zap,
  Trash2,
  Play,
  Pause,
  Save,
  X,
  ArrowLeft,
  Settings2,
  GripVertical,
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  Webhook,
  Filter,
  Users,
  Archive,
  ListChecks,
  UserPlus,
  RotateCcw,
  Ban,
  ChevronDown,
  ChevronRight
} from 'lucide-vue-next'
import {
  automationApi,
  templateApi,
  identityApi,
  domainApi,
  listApi,
  webhookApi,
  contactApi,
  type Automation,
  type AutomationStats,
  type AutomationEnrollment,
  type AutomationValidationError,
  type AutomationReentryPolicy,
  type AutomationGraphEdge,
  type EnrollmentStatus,
  type EmailTemplate,
  type Identity,
  type Domain,
  type ContactList,
  type Webhook as WebhookEndpoint,
  type ContactFull
} from '@/lib/api'
import {
  normalizeGraph,
  validateGraph,
  dominatingEmailNodes,
  errorsByNode,
  triggerLabel,
  operatorsFor,
  TRIGGER_SOURCES,
  DEFAULT_TRIGGER_SOURCES,
  CONDITION_FIELDS,
  OPERATORS,
  ACTIONS,
  type NodeKind,
  type Graph
} from '@/lib/automationGraph'

// Import VueFlow styles
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import '@vue-flow/controls/dist/style.css'
import '@vue-flow/minimap/dist/style.css'

interface WorkflowNodeData {
  label: string
  type: NodeKind
  config: Record<string, unknown>
}

type PaletteItem = { type: NodeKind; label: string; icon: unknown; description: string; color: string }

const route = useRoute()
const router = useRouter()
const { onConnect } = useVueFlow()

// State
const isLoading = ref(true)
const busy = ref<'' | 'save' | 'validate' | 'activate' | 'pause' | 'archive'>('')
const automation = ref<Automation | null>(null)
const automationName = ref('New Automation')
const automationDescription = ref('')
const reentryPolicy = ref<AutomationReentryPolicy>('never')
const selectedNodeId = ref<string | null>(null)
const banner = ref('')
const notice = ref('')
const validationErrors = ref<AutomationValidationError[]>([])

const nodes = ref<Node[]>([])
const edges = ref<Edge[]>([])

const selectedNode = computed(() => nodes.value.find(n => n.id === selectedNodeId.value) ?? null)
const cfg = computed<Record<string, any>>(() => selectedNode.value?.data.config ?? {})
const status = computed(() => automation.value?.status ?? 'draft')
const readOnly = computed(() => status.value === 'archived')
const publishedVersion = computed(() => automation.value?.publishedVersion ?? null)
const nodeErrors = computed(() => errorsByNode(validationErrors.value))

// Referenced resources for the config panels.
const templates = ref<EmailTemplate[]>([])
const identities = ref<Identity[]>([])
const domains = ref<Domain[]>([])
const lists = ref<ContactList[]>([])
const webhooks = ref<WebhookEndpoint[]>([])

const staticLists = computed(() => lists.value.filter(l => !l.type || l.type === 'static'))
const activeTemplates = computed(() => templates.value.filter(t => t.isActive))
const activeWebhooks = computed(() => webhooks.value.filter(w => w.active))
// The server only lists the current user's identities. Automations send only
// from can_send identities on active, SES-verified domains (checked again on
// publish and at send time).
const sendableIdentities = computed(() => identities.value.filter(identity => {
  if (identity.canSend === false) return false
  if (!domains.value.length) return true
  const domain = domains.value.find(d => String(d.id) === String(identity.domainId))
  return !!domain && domain.status === 'active' && domain.sesVerified
}))

const nodeTypes: { category: string; items: PaletteItem[] }[] = [
  {
    category: 'Triggers',
    items: [
      { type: 'trigger', label: 'Contact subscribed to list', icon: Users, description: 'When a contact joins a list', color: 'yellow' },
      { type: 'trigger', label: 'Contact created', icon: UserCheck, description: 'When a new contact is created', color: 'yellow' },
      { type: 'trigger', label: 'Manual enrollment', icon: Play, description: 'Enroll contacts or lists by hand or API', color: 'yellow' },
    ]
  },
  {
    category: 'Actions',
    items: [
      { type: 'email', label: 'Send email', icon: Mail, description: 'Send a template from your verified domain', color: 'blue' },
      { type: 'action', label: 'Add to list', icon: Users, description: 'Add the contact to a static list', color: 'orange' },
      { type: 'action', label: 'Remove from list', icon: Users, description: 'Remove the contact from a list', color: 'orange' },
      { type: 'action', label: 'Update contact field', icon: UserCheck, description: 'Set a contact attribute', color: 'orange' },
      { type: 'webhook', label: 'Webhook', icon: Webhook, description: 'Notify one of your signed endpoints', color: 'purple' },
    ]
  },
  {
    category: 'Flow Control',
    items: [
      { type: 'delay', label: 'Wait', icon: Clock, description: 'Wait for a set time', color: 'purple' },
      { type: 'condition', label: 'If/Else', icon: GitBranch, description: 'Branch on Yes or No', color: 'green' },
      { type: 'condition', label: 'Filter', icon: Filter, description: 'Continue only when it matches', color: 'green' },
    ]
  }
]

// ---------- Graph helpers ----------

const graph = (): Graph => ({ nodes: nodes.value as unknown as Graph['nodes'], edges: edges.value as unknown as Graph['edges'] })
const canonicalWorkflow = () => normalizeGraph(graph()).workflow

// Unsaved edits are detected against the last saved canonical draft.
const snapshot = () => JSON.stringify({ n: automationName.value, d: automationDescription.value, r: reentryPolicy.value, g: canonicalWorkflow() })
const savedSnapshot = ref('')
const isDirty = computed(() => !isLoading.value && !readOnly.value && snapshot() !== savedSnapshot.value)
const hasUnpublished = computed(() => publishedVersion.value != null && (isDirty.value || !!automation.value?.hasUnpublishedChanges))

const edgeColor = (handle?: string | null) => handle === 'yes' ? '#22c55e' : handle === 'no' ? '#ef4444' : '#6366f1'

function decorateEdge(e: AutomationGraphEdge): Edge {
  const handle = e.sourceHandle || null
  const color = edgeColor(handle)
  return {
    id: e.id || `e-${e.source}-${handle ?? 'next'}-${e.target}`,
    source: e.source,
    target: e.target,
    sourceHandle: handle,
    targetHandle: e.targetHandle || null,
    type: 'smoothstep',
    animated: true,
    label: handle === 'yes' ? 'Yes' : handle === 'no' ? 'No' : undefined,
    style: { stroke: color, strokeWidth: 2 },
    markerEnd: { type: MarkerType.ArrowClosed, color }
  }
}

function applyAutomation(a: Automation, withGraph: boolean) {
  automation.value = a
  automationName.value = a.name
  automationDescription.value = a.description || ''
  reentryPolicy.value = a.reentryPolicy || 'never'
  if (withGraph) {
    nodes.value = (a.workflow?.nodes ?? []).map(n => ({
      id: n.id,
      type: 'workflow',
      position: n.position ?? { x: 0, y: 0 },
      data: { label: n.data?.label ?? '', type: n.data?.type, config: { ...(n.data?.config ?? {}) } }
    }))
    edges.value = (a.workflow?.edges ?? []).map(decorateEdge)
  }
}

const markSaved = () => { savedSnapshot.value = snapshot() }

const errorMessage = (e: unknown, fallback: string) => e instanceof Error && e.message ? e.message : fallback

// A 400 with data.errors highlights the failing steps.
function showFailure(e: unknown, fallback: string) {
  const errors = (e as { data?: { errors?: AutomationValidationError[] } })?.data?.errors
  if (errors?.length) {
    validationErrors.value = errors
    banner.value = `${errorMessage(e, fallback)}. Fix the highlighted steps.`
  } else {
    banner.value = errorMessage(e, fallback)
  }
}

function clearMessages() {
  banner.value = ''
  notice.value = ''
}

// ---------- Loading ----------

async function loadResources() {
  const [t, i, d, l, w] = await Promise.allSettled([templateApi.list(), identityApi.list(), domainApi.list(), listApi.list(), webhookApi.list()])
  if (t.status === 'fulfilled') templates.value = t.value ?? []
  if (i.status === 'fulfilled') identities.value = i.value ?? []
  if (d.status === 'fulfilled') domains.value = (d.value ?? []).filter(Boolean)
  if (l.status === 'fulfilled') lists.value = l.value ?? []
  if (w.status === 'fulfilled') webhooks.value = w.value ?? []
  if ([t, i, l, w].some(r => r.status === 'rejected')) banner.value = 'Some templates, senders, lists or webhooks could not be loaded.'
}

onMounted(async () => {
  const resources = loadResources()
  if (route.name !== 'automation-new' && route.params.uuid) {
    try {
      applyAutomation(await automationApi.get(route.params.uuid as string), true)
    } catch (e) {
      banner.value = errorMessage(e, 'Could not load the automation')
    }
  } else {
    nodes.value = [{
      id: 'trigger-1',
      type: 'workflow',
      position: { x: 400, y: 100 },
      data: {
        label: 'Contact subscribed to list',
        type: 'trigger',
        config: { event: 'contact.subscribed', sources: [...DEFAULT_TRIGGER_SOURCES] }
      }
    }]
  }
  await resources
  isLoading.value = false
  markSaved()
})

// ---------- Editing ----------

onConnect((connection: Connection) => {
  if (readOnly.value || !connection.source || !connection.target) return
  edges.value = [...edges.value, decorateEdge({
    id: `e-${connection.source}-${connection.sourceHandle || 'next'}-${connection.target}-${Date.now()}`,
    source: connection.source,
    target: connection.target,
    sourceHandle: connection.sourceHandle,
    targetHandle: connection.targetHandle
  })]
})

const TRIGGER_LABELS = ['Contact subscribed to list', 'Contact created', 'Manual enrollment', 'Trigger', '']

// Switching the event drops options the new event does not accept.
function setTriggerEvent(node: Node, event: string) {
  const config: Record<string, unknown> = node.data.config ??= {}
  config.event = event
  if (event === 'manual') {
    delete config.listUuid
    delete config.sources
  } else {
    if (event !== 'contact.subscribed') delete config.listUuid
    if (!Array.isArray(config.sources)) config.sources = [...DEFAULT_TRIGGER_SOURCES]
  }
  if (TRIGGER_LABELS.includes(node.data.label)) node.data.label = triggerLabel(event)
}

const getDefaultConfig = (type: NodeKind, label: string): Record<string, unknown> => {
  switch (type) {
    case 'email':
      return { templateUuid: '', identityUuid: '', subject: '', trackOpens: true, trackClicks: true }
    case 'delay':
      return { duration: 1, unit: 'days' }
    case 'condition':
      return { field: '', mode: label === 'Filter' ? 'filter' : 'if_else' }
    case 'action':
      return label === 'Update contact field' ? { action: 'update_field', attribute: '', value: '' }
        : { action: label === 'Remove from list' ? 'remove_from_list' : 'add_to_list', listUuid: '' }
    case 'webhook':
      return { webhookUuid: '' }
    default:
      return {}
  }
}

const eventForLabel = (label: string) => label === 'Contact created' ? 'contact.created' : label === 'Manual enrollment' ? 'manual' : 'contact.subscribed'

// An automation has one trigger: picking a trigger replaces its event.
function addNode(item: PaletteItem, position?: { x: number; y: number }) {
  if (readOnly.value) return
  if (item.type === 'trigger') {
    const trigger = nodes.value.find(n => n.data.type === 'trigger')
    if (trigger) {
      setTriggerEvent(trigger, eventForLabel(item.label))
      selectedNodeId.value = trigger.id
      return
    }
  }
  const id = `${item.type}-${Date.now()}`
  const config = item.type === 'trigger' ? { event: eventForLabel(item.label) } : getDefaultConfig(item.type, item.label)
  nodes.value = [...nodes.value, {
    id,
    type: 'workflow',
    position: position ?? { x: 400, y: nodes.value.length * 150 + 100 },
    data: { label: item.label, type: item.type, config }
  }]
  if (item.type === 'trigger') setTriggerEvent(nodes.value[nodes.value.length - 1], eventForLabel(item.label))
  selectedNodeId.value = id
}

const onDragStart = (event: DragEvent, item: PaletteItem) => {
  if (event.dataTransfer) {
    event.dataTransfer.setData('application/json', JSON.stringify({ type: item.type, label: item.label }))
    event.dataTransfer.effectAllowed = 'move'
  }
}

const onDrop = (event: DragEvent) => {
  event.preventDefault()
  const raw = event.dataTransfer?.getData('application/json')
  if (!raw) return
  const { type, label } = JSON.parse(raw) as { type: NodeKind; label: string }
  const item = nodeTypes.flatMap(c => c.items).find(i => i.type === type && i.label === label)
  const bounds = (event.target as HTMLElement).closest('.vue-flow')?.getBoundingClientRect()
  if (item) addNode(item, bounds ? { x: event.clientX - bounds.left - 90, y: event.clientY - bounds.top - 30 } : undefined)
}

const onDragOver = (event: DragEvent) => {
  event.preventDefault()
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
}

const onNodeClick = ({ node }: NodeMouseEvent) => {
  selectedNodeId.value = node.id
}

const deleteSelectedNode = () => {
  const node = selectedNode.value
  if (!node || readOnly.value || node.data.type === 'trigger') return
  nodes.value = nodes.value.filter(n => n.id !== node.id)
  edges.value = edges.value.filter(e => e.source !== node.id && e.target !== node.id)
  selectedNodeId.value = null
}

// Writes config keys on the selected step and drops the ones that no longer apply.
function patchConfig(patch: Record<string, unknown>, remove: string[] = []) {
  const node = selectedNode.value
  if (!node || readOnly.value) return
  const config: Record<string, unknown> = node.data.config ??= {}
  Object.assign(config, patch)
  for (const k of remove) delete config[k]
}

const inputValue = (e: Event) => (e.target as HTMLInputElement | HTMLSelectElement).value
const inputChecked = (e: Event) => (e.target as HTMLInputElement).checked

// Trigger
const triggerSources = computed<string[]>(() => Array.isArray(cfg.value.sources) ? cfg.value.sources : DEFAULT_TRIGGER_SOURCES)
function toggleSource(source: string, on: boolean) {
  const chosen = new Set(triggerSources.value)
  if (on) chosen.add(source)
  else chosen.delete(source)
  patchConfig({ sources: TRIGGER_SOURCES.map(s => s.value as string).filter(s => chosen.has(s)) })
}
const triggerListUuid = computed(() => {
  const trigger = nodes.value.find(n => n.data.type === 'trigger')
  return (trigger?.data.config?.listUuid as string | undefined) ?? ''
})

// Condition
const conditionEmailOptions = computed(() => selectedNode.value ? dominatingEmailNodes(graph(), selectedNode.value.id) : [])
function setConditionField(field: string) {
  if (field === 'email_opened' || field === 'email_clicked') {
    const only = conditionEmailOptions.value.length === 1 ? conditionEmailOptions.value[0].id : (cfg.value.emailNodeId ?? '')
    patchConfig({ field, emailNodeId: only }, ['attribute', 'operator', 'value'])
  } else if (field === 'engagement_score') {
    patchConfig({ field, operator: 'greater_than', value: cfg.value.value ?? '' }, ['emailNodeId', 'attribute'])
  } else {
    patchConfig({ field, operator: 'equals', attribute: cfg.value.attribute ?? '', value: cfg.value.value ?? '' }, ['emailNodeId'])
  }
}
function setConditionMode(mode: string) {
  patchConfig({ mode })
  const id = selectedNode.value?.id
  if (mode === 'filter' && id) edges.value = edges.value.filter(e => !(e.source === id && e.sourceHandle === 'no'))
}

// Action
function setAction(action: string) {
  if (action === 'update_field') patchConfig({ action, attribute: cfg.value.attribute ?? '', value: '' }, ['listUuid'])
  else patchConfig({ action, listUuid: cfg.value.listUuid ?? '' }, ['attribute', 'value'])
}

// ---------- Labels ----------

const getNodeColors = (type: NodeKind) => {
  const colors: Record<NodeKind, { bg: string; border: string; icon: string }> = {
    trigger: { bg: 'bg-yellow-50', border: 'border-yellow-400', icon: 'text-yellow-600' },
    email: { bg: 'bg-blue-50', border: 'border-blue-400', icon: 'text-blue-600' },
    delay: { bg: 'bg-purple-50', border: 'border-purple-400', icon: 'text-purple-600' },
    condition: { bg: 'bg-green-50', border: 'border-green-400', icon: 'text-green-600' },
    action: { bg: 'bg-orange-50', border: 'border-orange-400', icon: 'text-orange-600' },
    webhook: { bg: 'bg-indigo-50', border: 'border-indigo-400', icon: 'text-indigo-600' }
  }
  return colors[type] || { bg: 'bg-gray-50', border: 'border-gray-400', icon: 'text-gray-600' }
}

const getNodeIcon = (type: NodeKind) => {
  const icons: Record<NodeKind, unknown> = { trigger: Zap, email: Mail, delay: Clock, condition: GitBranch, action: UserCheck, webhook: Webhook }
  return icons[type] || AlertCircle
}

const listName = (uuid: unknown) => lists.value.find(l => l.uuid === uuid)?.name
const nodeLabel = (id: string | null | undefined) => {
  if (!id) return ''
  const node = nodes.value.find(n => n.id === id)
  return node?.data.label || id
}
const operatorLabel = (op: unknown) => OPERATORS.find(o => o.value === op)?.label.toLowerCase() ?? ''

const getConfigSummary = (data: WorkflowNodeData) => {
  const c = data.config ?? {}
  switch (data.type) {
    case 'trigger':
      return triggerLabel(c.event as string) + (c.listUuid ? ` · ${listName(c.listUuid) ?? 'a list'}` : '')
    case 'email':
      return templates.value.find(t => t.uuid === c.templateUuid)?.name || (c.templateUuid ? 'Template' : 'Choose a template...')
    case 'delay':
      return `Wait ${c.duration} ${c.unit}`
    case 'condition': {
      const field = CONDITION_FIELDS.find(f => f.value === c.field)?.label
      if (!field) return 'Choose what to check...'
      if (c.field === 'email_opened' || c.field === 'email_clicked') return c.emailNodeId ? `${field} · ${nodeLabel(c.emailNodeId as string)}` : field
      if (c.field === 'custom_field') return `${c.attribute || 'attribute'} ${operatorLabel(c.operator)} ${c.value ?? ''}`
      return `${field} ${operatorLabel(c.operator)} ${c.value ?? ''}`
    }
    case 'action':
      if (c.action === 'update_field') return c.attribute ? `Set ${c.attribute} = ${c.value ?? ''}` : 'Choose a field...'
      return `${c.action === 'remove_from_list' ? 'Remove from' : 'Add to'} ${listName(c.listUuid) ?? 'a list...'}`
    case 'webhook':
      return webhooks.value.find(w => w.uuid === c.webhookUuid)?.name || 'Choose an endpoint...'
    default:
      return ''
  }
}

const nodeCount = computed(() => nodes.value.length)
const edgeCount = computed(() => edges.value.length)

// ---------- Save, validate and lifecycle ----------

function selectError(error: AutomationValidationError) {
  if (error.nodeId && nodes.value.some(n => n.id === error.nodeId)) selectedNodeId.value = error.nodeId
}

async function save(): Promise<boolean> {
  if (readOnly.value) return false
  busy.value = 'save'
  clearMessages()
  const body = {
    name: automationName.value,
    description: automationDescription.value,
    reentryPolicy: reentryPolicy.value,
    workflow: canonicalWorkflow()
  }
  try {
    if (!automation.value) {
      const created = await automationApi.create(body)
      applyAutomation(created, false)
      markSaved()
      await router.replace(`/automations/${created.uuid}`)
    } else {
      applyAutomation(await automationApi.update(automation.value.uuid, body), false)
      markSaved()
    }
    return true
  } catch (e) {
    showFailure(e, 'Could not save the automation')
    return false
  } finally {
    busy.value = ''
  }
}

// The local rules catch graph problems at once; the server also checks that
// lists, templates, senders and webhooks exist and belong to you.
function localCheck(): boolean {
  const errors = validateGraph(canonicalWorkflow())
  validationErrors.value = errors
  if (errors.length) banner.value = `${errors.length} problem${errors.length === 1 ? '' : 's'} to fix before this automation can run.`
  return errors.length === 0
}

async function saveIfNeeded() {
  return automation.value && !isDirty.value ? true : save()
}

async function validate() {
  if (!(await saveIfNeeded()) || !automation.value) return
  clearMessages()
  if (!localCheck()) return
  busy.value = 'validate'
  try {
    const result = await automationApi.validate(automation.value.uuid)
    validationErrors.value = result.errors ?? []
    if (result.valid) notice.value = 'No problems found. This automation is ready to activate.'
    else banner.value = `${validationErrors.value.length} problem${validationErrors.value.length === 1 ? '' : 's'} to fix before this automation can run.`
  } catch (e) {
    showFailure(e, 'Could not validate the automation')
  } finally {
    busy.value = ''
  }
}

async function activate(publishDraft: boolean) {
  if (publishDraft) {
    if (!(await saveIfNeeded())) return
    clearMessages()
    if (!localCheck()) return
  }
  if (!automation.value) return
  busy.value = 'activate'
  clearMessages()
  try {
    const result = await automationApi.activate(automation.value.uuid, { publishDraft })
    applyAutomation(result.automation, false)
    markSaved()
    validationErrors.value = []
    notice.value = result.published ? `Version ${result.version} is live.` : `Version ${result.version} is running again.`
  } catch (e) {
    showFailure(e, 'Could not activate the automation')
  } finally {
    busy.value = ''
  }
}

async function pause() {
  if (!automation.value) return
  busy.value = 'pause'
  clearMessages()
  try {
    applyAutomation(await automationApi.pause(automation.value.uuid), false)
    notice.value = 'Paused. Contacts in progress wait here until you resume.'
  } catch (e) {
    showFailure(e, 'Could not pause the automation')
  } finally {
    busy.value = ''
  }
}

async function archive() {
  if (!automation.value) return
  busy.value = 'archive'
  clearMessages()
  try {
    const result = await automationApi.archive(automation.value.uuid)
    applyAutomation(result.automation, false)
    markSaved()
    selectedNodeId.value = null
    notice.value = `Archived. ${result.cancelledEnrollments} contact${result.cancelledEnrollments === 1 ? ' was' : 's were'} in progress and cancelled.`
  } catch (e) {
    showFailure(e, 'Could not archive the automation')
  } finally {
    busy.value = ''
  }
}

// Confirmation dialog for lifecycle actions.
const confirmState = ref<{ title: string; message: string; confirmLabel: string; danger?: boolean; run: () => unknown } | null>(null)
function ask(title: string, message: string, confirmLabel: string, run: () => unknown, danger = false) {
  confirmState.value = { title, message, confirmLabel, run, danger }
}
async function confirmRun() {
  const state = confirmState.value
  confirmState.value = null
  await state?.run()
}

const primaryAction = computed(() => {
  if (readOnly.value) return null
  if (status.value === 'draft') return { label: 'Activate', icon: Play }
  if (status.value === 'paused') return { label: 'Resume', icon: Play }
  if (status.value === 'active' && hasUnpublished.value) return { label: 'Publish changes', icon: CheckCircle2 }
  return null
})

function askPrimary() {
  const v = publishedVersion.value
  // Paused with no version: a legacy automation from before versioning.
  if (status.value === 'draft' || v == null) {
    ask(status.value === 'draft' ? 'Activate automation?' : 'Publish and resume?', 'Contacts who meet the trigger from now on are enrolled in version 1. Contacts who met it earlier are not.',
      status.value === 'draft' ? 'Activate' : 'Publish and resume', () => activate(true))
  } else if (status.value === 'paused' && !hasUnpublished.value) {
    ask('Resume automation?', `Contacts continue on version ${v}. Waits that came due while paused run right away.`, 'Resume', () => activate(true))
  } else {
    ask(status.value === 'paused' ? 'Publish changes and resume?' : 'Publish changes?',
      `New enrollments use the new version; contacts already in progress continue on version ${v}.`,
      status.value === 'paused' ? 'Publish and resume' : 'Publish', () => activate(true))
  }
}

function askResumeWithoutPublishing() {
  ask('Resume without publishing?', `Version ${publishedVersion.value} keeps running and your changes stay as an unpublished draft. Waits that came due while paused run right away.`, 'Resume', () => activate(false))
}

function askPause() {
  ask('Pause automation?', 'New contacts are not enrolled, and contacts in progress stop where they are until you resume.', 'Pause', pause)
}

function askArchive() {
  ask('Archive automation?', 'Every contact in progress is cancelled, and the automation becomes read-only. This cannot be undone.', 'Archive', archive, true)
}

const goBack = () => {
  router.push('/automations')
}

// ---------- Stats overlay ----------

const stats = ref<AutomationStats | null>(null)
const showStats = computed(() => !!automation.value && automation.value.status !== 'draft' && publishedVersion.value != null)
let statsTimer: ReturnType<typeof setInterval> | undefined

async function loadStats() {
  if (!showStats.value || !automation.value || document.visibilityState === 'hidden') return
  try {
    stats.value = await automationApi.stats(automation.value.uuid)
  } catch {
    // Keep the last counts; the next poll retries.
  }
}

watch(showStats, on => {
  clearInterval(statsTimer)
  statsTimer = undefined
  if (!on) {
    stats.value = null
    return
  }
  loadStats()
  statsTimer = setInterval(loadStats, 30_000)
}, { immediate: true })

const onVisibility = () => { if (document.visibilityState === 'visible') loadStats() }
const nodeStats = (id: string) => stats.value?.nodes?.[id]

// ---------- Enrollments drawer ----------

const drawerOpen = ref(false)
const enrollmentFilter = ref<'' | EnrollmentStatus | 'waiting'>('')
const enrollmentPage = ref(1)
const enrollmentPageSize = 25
const enrollments = ref<AutomationEnrollment[]>([])
const enrollmentTotal = ref(0)
const enrollmentsLoading = ref(false)
const drawerError = ref('')
const expandedEnrollment = ref<string | null>(null)
const enrollmentDetails = ref<Record<string, AutomationEnrollment>>({})

async function loadEnrollments() {
  if (!automation.value || !drawerOpen.value) return
  enrollmentsLoading.value = true
  drawerError.value = ''
  try {
    const result = await automationApi.enrollments(automation.value.uuid, { status: enrollmentFilter.value, page: enrollmentPage.value, pageSize: enrollmentPageSize })
    enrollments.value = result.enrollments ?? []
    enrollmentTotal.value = result.total ?? 0
  } catch (e) {
    drawerError.value = errorMessage(e, 'Could not load enrollments')
  } finally {
    enrollmentsLoading.value = false
  }
}

watch([drawerOpen, enrollmentPage], loadEnrollments)
watch(enrollmentFilter, () => {
  if (enrollmentPage.value !== 1) enrollmentPage.value = 1
  else loadEnrollments()
})

async function toggleEnrollment(e: AutomationEnrollment) {
  expandedEnrollment.value = expandedEnrollment.value === e.uuid ? null : e.uuid
  if (expandedEnrollment.value && automation.value) {
    try {
      enrollmentDetails.value = { ...enrollmentDetails.value, [e.uuid]: await automationApi.enrollment(automation.value.uuid, e.uuid) }
    } catch (err) {
      drawerError.value = errorMessage(err, 'Could not load the enrollment')
    }
  }
}

async function enrollmentAction(e: AutomationEnrollment, action: 'cancel' | 'retry') {
  if (!automation.value) return
  drawerError.value = ''
  try {
    const updated = action === 'cancel'
      ? await automationApi.cancelEnrollment(automation.value.uuid, e.uuid)
      : await automationApi.retryEnrollment(automation.value.uuid, e.uuid)
    enrollments.value = enrollments.value.map(x => x.uuid === e.uuid ? { ...x, ...updated } : x)
    const { [e.uuid]: _stale, ...rest } = enrollmentDetails.value
    enrollmentDetails.value = rest
    if (expandedEnrollment.value === e.uuid) expandedEnrollment.value = null
    loadStats()
  } catch (err) {
    drawerError.value = errorMessage(err, `Could not ${action} the enrollment`)
  }
}

const isWaiting = (e: AutomationEnrollment) => e.status === 'active' && !!e.nextRunAt && new Date(e.nextRunAt).getTime() > Date.now()
const enrollmentStatusLabel = (e: AutomationEnrollment) => isWaiting(e) ? 'waiting' : e.status
const formatTime = (value: string | null | undefined) => value ? new Date(value).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : ''
const enrollmentPages = computed(() => Math.max(1, Math.ceil(enrollmentTotal.value / enrollmentPageSize)))

// Enroll dialog
const enrollOpen = ref(false)
const enrollMode = ref<'contact' | 'list'>('contact')
const contactQuery = ref('')
const contactResults = ref<ContactFull[]>([])
const chosenContact = ref<ContactFull | null>(null)
const chosenList = ref('')
const enrollBusy = ref(false)
const enrollError = ref('')
const enrollResult = ref('')
let searchTimer: ReturnType<typeof setTimeout> | undefined
let searchSeq = 0

function openEnroll() {
  enrollOpen.value = true
  enrollError.value = ''
  enrollResult.value = ''
  chosenContact.value = null
  contactQuery.value = ''
  contactResults.value = []
}

watch(contactQuery, q => {
  clearTimeout(searchTimer)
  const query = q.trim()
  if (query.length < 2) {
    contactResults.value = []
    return
  }
  searchTimer = setTimeout(async () => {
    const seq = ++searchSeq
    try {
      const result = await contactApi.search(query, 1, 10)
      if (seq === searchSeq) contactResults.value = result.contacts ?? []
    } catch (e) {
      if (seq === searchSeq) enrollError.value = errorMessage(e, 'Could not search contacts')
    }
  }, 300)
})

async function submitEnroll() {
  if (!automation.value) return
  const target = enrollMode.value === 'contact' ? (chosenContact.value ? { contactUuid: chosenContact.value.uuid } : null) : (chosenList.value ? { listUuid: chosenList.value } : null)
  if (!target) {
    enrollError.value = enrollMode.value === 'contact' ? 'Choose a contact' : 'Choose a list'
    return
  }
  enrollBusy.value = true
  enrollError.value = ''
  try {
    const result = await automationApi.enroll(automation.value.uuid, target)
    enrollResult.value = `Enrolled ${result.enrolled}; skipped ${result.skipped} (already enrolled, inactive, suppressed or blocked by the re-entry rule).`
    loadEnrollments()
    loadStats()
  } catch (e) {
    enrollError.value = errorMessage(e, 'Could not enroll')
  } finally {
    enrollBusy.value = false
  }
}

// ---------- Leave guard ----------

onBeforeRouteLeave(() => !isDirty.value || window.confirm('You have unsaved changes. Leave without saving?'))

const onBeforeUnload = (e: BeforeUnloadEvent) => {
  if (!isDirty.value) return
  e.preventDefault()
  e.returnValue = ''
}

onMounted(() => {
  window.addEventListener('beforeunload', onBeforeUnload)
  document.addEventListener('visibilitychange', onVisibility)
})

onBeforeUnmount(() => {
  clearInterval(statsTimer)
  clearTimeout(searchTimer)
  window.removeEventListener('beforeunload', onBeforeUnload)
  document.removeEventListener('visibilitychange', onVisibility)
})
</script>

<template>
  <AppLayout>
  <div class="workflow-editor">
    <!-- Header -->
    <header class="editor-header">
      <div class="header-left">
        <button @click="goBack" class="back-btn" aria-label="Back to automations">
          <ArrowLeft class="w-4 h-4" />
        </button>
        <div class="header-info">
          <input
            v-model="automationName"
            type="text"
            class="name-input"
            placeholder="Automation name..."
            :readonly="readOnly"
            aria-label="Automation name"
          />
          <div class="header-meta">
            <span class="meta-item">{{ nodeCount }} steps</span>
            <span class="meta-divider">·</span>
            <span class="meta-item">{{ edgeCount }} connections</span>
            <span class="meta-divider">·</span>
            <span :class="['status-badge', `status-${status}`]">{{ status }}</span>
            <template v-if="publishedVersion != null">
              <span class="meta-divider">·</span>
              <span class="meta-item">Version {{ publishedVersion }}{{ status === 'active' ? ' live' : '' }}</span>
            </template>
            <span v-if="hasUnpublished && !readOnly" class="changes-badge">Unpublished changes</span>
            <span v-else-if="isDirty" class="changes-badge">Unsaved changes</span>
          </div>
        </div>
      </div>
      <div class="header-actions">
        <button v-if="automation && status !== 'draft'" @click="drawerOpen = !drawerOpen" class="btn btn-secondary">
          <ListChecks class="w-4 h-4 mr-2" />
          Enrollments
        </button>
        <template v-if="!readOnly">
          <button @click="save" :disabled="!!busy" class="btn btn-secondary">
            <Save class="w-4 h-4 mr-2" />
            {{ busy === 'save' ? 'Saving...' : 'Save' }}
          </button>
          <button @click="validate" :disabled="!!busy" class="btn btn-secondary">
            <CheckCircle2 class="w-4 h-4 mr-2" />
            {{ busy === 'validate' ? 'Checking...' : 'Validate' }}
          </button>
          <button v-if="status === 'paused' && hasUnpublished" @click="askResumeWithoutPublishing" :disabled="!!busy" class="btn btn-secondary">
            Resume without publishing
          </button>
          <button v-if="status === 'active'" @click="askPause" :disabled="!!busy" class="btn btn-secondary">
            <Pause class="w-4 h-4 mr-2" />
            Pause
          </button>
          <button v-if="automation" @click="askArchive" :disabled="!!busy" class="btn btn-secondary">
            <Archive class="w-4 h-4 mr-2" />
            Archive
          </button>
          <button v-if="primaryAction" @click="askPrimary" :disabled="!!busy" class="btn btn-primary">
            <component :is="primaryAction.icon" class="w-4 h-4 mr-2" />
            {{ busy === 'activate' ? 'Working...' : primaryAction.label }}
          </button>
        </template>
      </div>
    </header>

    <div v-if="banner" role="alert" class="editor-banner banner-error">
      <AlertCircle class="w-4 h-4" />
      <span>{{ banner }}</span>
      <button @click="banner = ''" aria-label="Dismiss"><X class="w-4 h-4" /></button>
    </div>
    <div v-else-if="notice" role="status" class="editor-banner banner-info">
      <CheckCircle2 class="w-4 h-4" />
      <span>{{ notice }}</span>
      <button @click="notice = ''" aria-label="Dismiss"><X class="w-4 h-4" /></button>
    </div>
    <div v-if="readOnly" class="editor-banner banner-muted">
      <Archive class="w-4 h-4" />
      <span>This automation is archived and read-only.</span>
    </div>

    <!-- Main Content -->
    <div class="editor-content">
      <!-- Left Sidebar: Node Palette -->
      <aside v-if="!readOnly" class="left-sidebar">
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
          :nodes-draggable="!readOnly"
          :nodes-connectable="!readOnly"
          :edges-updatable="!readOnly"
          :delete-key-code="readOnly ? null : 'Backspace'"
          fit-view-on-init
          @node-click="onNodeClick"
          class="workflow-canvas"
        >
          <!-- Custom Node -->
          <template #node-workflow="{ id, data }">
            <div :class="['workflow-node', getNodeColors(data.type).bg, getNodeColors(data.type).border, { 'has-error': nodeErrors[id], 'is-selected': selectedNodeId === id }]">
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
                <AlertCircle v-if="nodeErrors[id]" class="w-4 h-4 text-red-500 shrink-0" :title="nodeErrors[id][0].message" />
              </div>

              <div v-if="nodeStats(id)" class="node-stats">
                <span>{{ nodeStats(id)!.entered }} entered</span>
                <span>· {{ nodeStats(id)!.waiting }} waiting</span>
                <span :class="{ 'text-red-600': nodeStats(id)!.failed }">· {{ nodeStats(id)!.failed }} failed</span>
                <div v-if="data.type === 'email'" class="node-stats-line">
                  {{ nodeStats(id)!.sent }} sent · {{ nodeStats(id)!.opened }} opened · {{ nodeStats(id)!.clicked }} clicked
                </div>
                <div v-else-if="data.type === 'condition'" class="node-stats-line">
                  {{ nodeStats(id)!.yes }} yes · {{ nodeStats(id)!.no }} no
                </div>
              </div>

              <Handle
                v-if="data.type !== 'condition'"
                type="source"
                :position="Position.Bottom"
                class="node-handle"
              />

              <!-- Conditions connect only through Yes/No -->
              <template v-if="data.type === 'condition'">
                <Handle id="yes" type="source" :position="Position.Right" class="node-handle handle-yes" title="Yes" />
                <span class="handle-label handle-label-yes">Yes</span>
                <Handle v-if="data.config?.mode !== 'filter'" id="no" type="source" :position="Position.Left" class="node-handle handle-no" title="No" />
                <span v-if="data.config?.mode !== 'filter'" class="handle-label handle-label-no">No</span>
              </template>
            </div>
          </template>

          <Background :gap="20" :size="1" pattern-color="#e5e7eb" />
          <Controls position="bottom-right" />
          <MiniMap position="bottom-left" />
        </VueFlow>

        <!-- Enrollments drawer -->
        <aside v-if="drawerOpen && automation" class="enrollments-drawer" aria-label="Enrollments">
          <div class="drawer-header">
            <h3>Enrollments</h3>
            <div class="drawer-actions">
              <button
                @click="openEnroll"
                :disabled="status !== 'active'"
                :title="status === 'active' ? 'Enroll contacts' : 'Only active automations accept enrollments'"
                class="btn btn-primary btn-sm"
              >
                <UserPlus class="w-4 h-4 mr-1" />
                Enroll contacts
              </button>
              <button @click="drawerOpen = false" class="close-btn" aria-label="Close enrollments"><X class="w-4 h-4" /></button>
            </div>
          </div>
          <div class="drawer-filter">
            <select v-model="enrollmentFilter" class="form-select" aria-label="Filter by status">
              <option value="">All statuses</option>
              <option value="active">Active</option>
              <option value="waiting">Waiting</option>
              <option value="completed">Completed</option>
              <option value="exited">Exited</option>
              <option value="failed">Failed</option>
              <option value="cancelled">Cancelled</option>
            </select>
            <span class="drawer-count">{{ enrollmentTotal }} total</span>
          </div>
          <p v-if="drawerError" role="alert" class="drawer-error">{{ drawerError }}</p>
          <div class="drawer-body">
            <p v-if="enrollmentsLoading && !enrollments.length" class="drawer-empty">Loading...</p>
            <p v-else-if="!enrollments.length" class="drawer-empty">No enrollments yet.</p>
            <div v-for="e in enrollments" :key="e.uuid" class="enrollment-row">
              <div class="enrollment-main" @click="toggleEnrollment(e)">
                <component :is="expandedEnrollment === e.uuid ? ChevronDown : ChevronRight" class="w-4 h-4 text-gray-400 shrink-0" />
                <div class="enrollment-info">
                  <div class="enrollment-email">{{ e.contactEmail }}</div>
                  <div class="enrollment-meta">
                    <span :class="['enrollment-status', `enrollment-${enrollmentStatusLabel(e)}`]">{{ enrollmentStatusLabel(e) }}</span>
                    <span v-if="e.status === 'active' && e.currentNodeId">· {{ nodeLabel(e.currentNodeId) }}</span>
                    <span v-if="e.exitReason">· {{ e.exitReason.replace(/_/g, ' ') }}</span>
                    <span v-if="e.version">· v{{ e.version }}</span>
                  </div>
                  <div v-if="e.status === 'active' && e.nextRunAt" class="enrollment-meta">Next step {{ formatTime(e.nextRunAt) }}</div>
                  <div v-if="e.error" class="enrollment-error">{{ e.error }}</div>
                </div>
                <div class="enrollment-buttons" @click.stop>
                  <button v-if="e.status === 'active'" @click="enrollmentAction(e, 'cancel')" class="row-btn" title="Cancel this enrollment">
                    <Ban class="w-3.5 h-3.5" /> Cancel
                  </button>
                  <button v-if="e.status === 'failed' && status === 'active'" @click="enrollmentAction(e, 'retry')" class="row-btn" title="Retry the failed step">
                    <RotateCcw class="w-3.5 h-3.5" /> Retry
                  </button>
                </div>
              </div>
              <ol v-if="expandedEnrollment === e.uuid" class="step-timeline">
                <li v-if="!enrollmentDetails[e.uuid]" class="drawer-empty">Loading steps...</li>
                <li v-else-if="!enrollmentDetails[e.uuid].steps?.length" class="drawer-empty">No steps have run yet.</li>
                <li v-for="step in enrollmentDetails[e.uuid]?.steps ?? []" :key="step.nodeId + step.startedAt" class="timeline-step">
                  <span :class="['timeline-dot', `step-${step.status}`]" />
                  <div>
                    <div class="timeline-title">{{ nodeLabel(step.nodeId) }} <span class="timeline-status">{{ step.status }}{{ step.outcome ? ` · ${step.outcome}` : '' }}</span></div>
                    <div class="enrollment-meta">{{ formatTime(step.finishedAt || step.startedAt) }}<template v-if="step.resumeAt"> · resumes {{ formatTime(step.resumeAt) }}</template></div>
                    <div v-if="step.error" class="enrollment-error">{{ step.error }}</div>
                  </div>
                </li>
              </ol>
            </div>
          </div>
          <div v-if="enrollmentPages > 1" class="drawer-pager">
            <button :disabled="enrollmentPage <= 1" @click="enrollmentPage--" class="row-btn">Previous</button>
            <span>Page {{ enrollmentPage }} of {{ enrollmentPages }}</span>
            <button :disabled="enrollmentPage >= enrollmentPages" @click="enrollmentPage++" class="row-btn">Next</button>
          </div>
        </aside>
      </main>

      <!-- Right Sidebar: Node Configuration and problems -->
      <aside :class="['right-sidebar', { 'is-open': selectedNode || validationErrors.length }]">
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
              <button v-if="!readOnly && selectedNode.data.type !== 'trigger'" @click="deleteSelectedNode" class="delete-btn" title="Delete step">
                <Trash2 class="w-4 h-4" />
              </button>
              <button @click="selectedNodeId = null" class="close-btn" aria-label="Close">
                <X class="w-4 h-4" />
              </button>
            </div>
          </div>

          <fieldset class="config-body" :disabled="readOnly">
            <ul v-if="nodeErrors[selectedNode.id]" class="node-error-list" role="alert">
              <li v-for="(e, i) in nodeErrors[selectedNode.id]" :key="i">{{ e.message }}</li>
            </ul>

            <!-- Common: Label -->
            <div class="form-group">
              <label>Step Name</label>
              <input v-model="selectedNode.data.label" type="text" class="form-input" maxlength="200" />
            </div>

            <!-- Trigger Config -->
            <template v-if="selectedNode.data.type === 'trigger'">
              <div class="form-group">
                <label>Trigger Event</label>
                <select :value="cfg.event" @change="setTriggerEvent(selectedNode, inputValue($event))" class="form-select">
                  <option value="contact.subscribed">Contact subscribed to list</option>
                  <option value="contact.created">Contact created</option>
                  <option value="manual">Manual enrollment</option>
                </select>
              </div>
              <div v-if="cfg.event === 'contact.subscribed'" class="form-group">
                <label>List</label>
                <select :value="cfg.listUuid ?? ''" @change="inputValue($event) ? patchConfig({ listUuid: inputValue($event) }) : patchConfig({}, ['listUuid'])" class="form-select">
                  <option value="">Any list</option>
                  <option v-if="cfg.listUuid && !staticLists.some(l => l.uuid === cfg.listUuid)" :value="cfg.listUuid">Unavailable list</option>
                  <option v-for="l in staticLists" :key="l.uuid" :value="l.uuid">{{ l.name }}</option>
                </select>
              </div>
              <div v-if="cfg.event !== 'manual'" class="form-group">
                <label>Enroll contacts added by</label>
                <div class="checkbox-list">
                  <label v-for="s in TRIGGER_SOURCES" :key="s.value" class="checkbox-row">
                    <input type="checkbox" :checked="triggerSources.includes(s.value)" @change="toggleSource(s.value, inputChecked($event))" />
                    <span>{{ s.label }}</span>
                  </label>
                </div>
              </div>
              <div v-if="cfg.event !== 'manual' && triggerSources.includes('import')" class="form-warning">
                <AlertTriangle class="w-4 h-4" />
                <span>CSV imports enroll every imported contact. Only keep this on when those contacts agreed to these emails.</span>
              </div>
              <div v-if="cfg.event === 'contact.created'" class="form-warning">
                <AlertTriangle class="w-4 h-4" />
                <span>Every new contact from the selected sources is enrolled, whichever list they join.</span>
              </div>
              <div class="form-group">
                <label>Re-entry</label>
                <select v-model="reentryPolicy" class="form-select">
                  <option value="never">A contact runs this automation once</option>
                  <option value="after_exit">Again after finishing (at most once per 24 hours)</option>
                </select>
              </div>
              <div class="form-info">
                <AlertCircle class="w-4 h-4 text-blue-500" />
                <span v-if="cfg.event === 'manual'">Contacts enter only when you enroll them here or through the API.</span>
                <span v-else>Only contacts who meet the trigger after activation are enrolled. Any active automation also accepts manual enrollment.</span>
              </div>
            </template>

            <!-- Email Config -->
            <template v-else-if="selectedNode.data.type === 'email'">
              <div class="form-group">
                <label>Email Template</label>
                <select :value="cfg.templateUuid ?? ''" @change="patchConfig({ templateUuid: inputValue($event) }, ['templateId'])" class="form-select">
                  <option value="">Select a template...</option>
                  <option v-if="cfg.templateUuid && !activeTemplates.some(t => t.uuid === cfg.templateUuid)" :value="cfg.templateUuid">Unavailable template</option>
                  <option v-for="t in activeTemplates" :key="t.uuid" :value="t.uuid">{{ t.name }}</option>
                </select>
              </div>
              <div class="form-group">
                <label>Subject (optional)</label>
                <input v-model="selectedNode.data.config.subject" type="text" class="form-input" maxlength="998" placeholder="Uses the template's subject" />
              </div>
              <div class="form-group">
                <label>Send From</label>
                <select :value="cfg.identityUuid ?? ''" @change="patchConfig({ identityUuid: inputValue($event) }, ['identityId'])" class="form-select">
                  <option value="">Select a sender...</option>
                  <option v-if="cfg.identityUuid && !sendableIdentities.some(i => i.uuid === cfg.identityUuid)" :value="cfg.identityUuid">Unavailable sender</option>
                  <option v-for="i in sendableIdentities" :key="i.uuid" :value="i.uuid">{{ i.displayName ? `${i.displayName} <${i.email}>` : i.email }}</option>
                </select>
                <p v-if="!sendableIdentities.length" class="form-hint">You need a sending identity on an active, SES-verified domain.</p>
              </div>
              <div class="checkbox-list">
                <label class="checkbox-row">
                  <input type="checkbox" :checked="cfg.trackOpens !== false" @change="patchConfig({ trackOpens: inputChecked($event) })" />
                  <span>Track opens</span>
                </label>
                <label class="checkbox-row">
                  <input type="checkbox" :checked="cfg.trackClicks !== false" @change="patchConfig({ trackClicks: inputChecked($event) })" />
                  <span>Track clicks</span>
                </label>
              </div>
              <div class="form-info">
                <Mail class="w-4 h-4 text-blue-500" />
                <span>An unsubscribe link and List-Unsubscribe headers are always added.</span>
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
                    step="1"
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
              <p class="form-hint">Between 1 minute and 365 days.</p>
              <div class="form-info">
                <Clock class="w-4 h-4 text-purple-500" />
                <span>Contacts wait here before the next step. While the automation is paused, waits that come due run right after you resume.</span>
              </div>
            </template>

            <!-- Condition Config -->
            <template v-else-if="selectedNode.data.type === 'condition'">
              <div class="form-group">
                <label>Type</label>
                <select :value="cfg.mode || 'if_else'" @change="setConditionMode(inputValue($event))" class="form-select">
                  <option value="if_else">If/Else (Yes and No branches)</option>
                  <option value="filter">Filter (continue only on Yes)</option>
                </select>
              </div>
              <div class="form-group">
                <label>Check</label>
                <select :value="cfg.field ?? ''" @change="setConditionField(inputValue($event))" class="form-select">
                  <option value="">Select what to check...</option>
                  <option v-for="f in CONDITION_FIELDS" :key="f.value" :value="f.value">{{ f.label }}</option>
                </select>
              </div>
              <div v-if="cfg.field === 'email_opened' || cfg.field === 'email_clicked'" class="form-group">
                <label>Email step</label>
                <select :value="cfg.emailNodeId ?? ''" @change="patchConfig({ emailNodeId: inputValue($event) })" class="form-select">
                  <option value="">Select an email step...</option>
                  <option v-if="cfg.emailNodeId && !conditionEmailOptions.some(n => n.id === cfg.emailNodeId)" :value="cfg.emailNodeId">{{ nodeLabel(cfg.emailNodeId) }} (not on every path)</option>
                  <option v-for="n in conditionEmailOptions" :key="n.id" :value="n.id">{{ n.data.label || n.id }}</option>
                </select>
                <p v-if="!conditionEmailOptions.length" class="form-hint">Connect an email step before this condition on every path.</p>
              </div>
              <div v-if="cfg.field === 'custom_field'" class="form-group">
                <label>Attribute</label>
                <input v-model="selectedNode.data.config.attribute" type="text" class="form-input" maxlength="64" placeholder="e.g. plan" />
              </div>
              <template v-if="cfg.field === 'engagement_score' || cfg.field === 'custom_field'">
                <div class="form-group">
                  <label>Operator</label>
                  <select v-model="selectedNode.data.config.operator" class="form-select">
                    <option v-for="o in operatorsFor(cfg.field)" :key="o.value" :value="o.value">{{ o.label }}</option>
                  </select>
                </div>
                <div class="form-group">
                  <label>Value</label>
                  <input v-model="selectedNode.data.config.value" type="text" class="form-input" maxlength="1000" :placeholder="cfg.field === 'engagement_score' ? 'A number' : 'Value'" />
                </div>
              </template>
              <div class="form-info">
                <GitBranch class="w-4 h-4 text-green-500" />
                <span>Evaluated when the contact reaches this step; add a Wait before it.</span>
              </div>
              <p v-if="cfg.field === 'email_opened'" class="form-hint">Some mail apps (such as Apple Mail) report opens automatically, so opens are approximate.</p>
            </template>

            <!-- Action Config -->
            <template v-else-if="selectedNode.data.type === 'action'">
              <div class="form-group">
                <label>Action</label>
                <select :value="cfg.action ?? ''" @change="setAction(inputValue($event))" class="form-select">
                  <option v-for="a in ACTIONS" :key="a.value" :value="a.value">{{ a.label }}</option>
                </select>
              </div>
              <div v-if="cfg.action === 'add_to_list' || cfg.action === 'remove_from_list'" class="form-group">
                <label>List</label>
                <select :value="cfg.listUuid ?? ''" @change="patchConfig({ listUuid: inputValue($event) })" class="form-select">
                  <option value="">Select a list...</option>
                  <option v-if="cfg.listUuid && !staticLists.some(l => l.uuid === cfg.listUuid)" :value="cfg.listUuid">Unavailable list</option>
                  <option
                    v-for="l in staticLists"
                    :key="l.uuid"
                    :value="l.uuid"
                    :disabled="cfg.action === 'add_to_list' && l.uuid === triggerListUuid"
                  >{{ l.name }}{{ cfg.action === 'add_to_list' && l.uuid === triggerListUuid ? ' (trigger list)' : '' }}</option>
                </select>
              </div>
              <template v-else-if="cfg.action === 'update_field'">
                <div class="form-group">
                  <label>Attribute</label>
                  <input v-model="selectedNode.data.config.attribute" type="text" class="form-input" maxlength="64" placeholder="e.g. nurture" />
                </div>
                <div class="form-group">
                  <label>Value</label>
                  <input v-model="selectedNode.data.config.value" type="text" class="form-input" maxlength="1000" />
                </div>
                <p class="form-hint">Automations cannot change a contact's email or status.</p>
              </template>
            </template>

            <!-- Webhook Config -->
            <template v-else-if="selectedNode.data.type === 'webhook'">
              <div class="form-group">
                <label>Endpoint</label>
                <select :value="cfg.webhookUuid ?? ''" @change="patchConfig({ webhookUuid: inputValue($event) }, ['url', 'method'])" class="form-select">
                  <option value="">Select an endpoint...</option>
                  <option v-if="cfg.webhookUuid && !activeWebhooks.some(w => w.uuid === cfg.webhookUuid)" :value="cfg.webhookUuid">Unavailable endpoint</option>
                  <option v-for="w in activeWebhooks" :key="w.uuid" :value="w.uuid">{{ w.name }}</option>
                </select>
                <p class="form-hint">
                  <router-link to="/settings?tab=integrations" class="form-link">Manage endpoints in Settings → Webhooks</router-link>
                </p>
              </div>
              <div v-if="cfg.url || cfg.method" class="form-warning">
                <AlertTriangle class="w-4 h-4" />
                <span>This step has an old URL setting. Choose an endpoint to replace it.</span>
              </div>
              <div class="form-info">
                <Webhook class="w-4 h-4 text-indigo-500" />
                <span>Sends a signed automation.webhook event with the contact's id, email and name. Failed deliveries are retried.</span>
              </div>
            </template>
          </fieldset>
        </div>

        <!-- Problems list -->
        <div v-else-if="validationErrors.length" class="config-panel">
          <div class="config-header">
            <div class="config-title">
              <AlertCircle class="w-5 h-5 text-red-500" />
              <span>{{ validationErrors.length }} problem{{ validationErrors.length === 1 ? '' : 's' }}</span>
            </div>
            <button @click="validationErrors = []" class="close-btn" aria-label="Clear problems"><X class="w-4 h-4" /></button>
          </div>
          <ul class="problem-list">
            <li v-for="(e, i) in validationErrors" :key="i">
              <button :disabled="!e.nodeId" @click="selectError(e)" class="problem-item">
                <span class="problem-step">{{ e.nodeId ? nodeLabel(e.nodeId) : 'Workflow' }}</span>
                <span>{{ e.message }}</span>
              </button>
            </li>
          </ul>
        </div>

        <!-- Empty state when no node selected -->
        <div v-else class="config-empty">
          <Settings2 class="w-8 h-8 text-gray-300" />
          <p>Select a step to configure</p>
        </div>
      </aside>
    </div>

    <!-- Confirm dialog -->
    <div v-if="confirmState" class="modal-backdrop" @click.self="confirmState = null">
      <div class="modal" role="dialog" aria-modal="true" :aria-label="confirmState.title">
        <h3>{{ confirmState.title }}</h3>
        <p>{{ confirmState.message }}</p>
        <div class="modal-actions">
          <button @click="confirmState = null" class="btn btn-secondary">Cancel</button>
          <button @click="confirmRun" :class="['btn', confirmState.danger ? 'btn-danger' : 'btn-primary']">{{ confirmState.confirmLabel }}</button>
        </div>
      </div>
    </div>

    <!-- Enroll dialog -->
    <div v-if="enrollOpen" class="modal-backdrop" @click.self="enrollOpen = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="Enroll contacts">
        <h3>Enroll contacts</h3>
        <div class="modal-tabs">
          <button :class="['modal-tab', { active: enrollMode === 'contact' }]" @click="enrollMode = 'contact'">One contact</button>
          <button :class="['modal-tab', { active: enrollMode === 'list' }]" @click="enrollMode = 'list'">A list</button>
        </div>
        <template v-if="enrollMode === 'contact'">
          <input v-model="contactQuery" type="search" class="form-input" placeholder="Search by email or name" aria-label="Search contacts" />
          <ul v-if="contactResults.length" class="contact-results">
            <li v-for="c in contactResults" :key="c.uuid">
              <button :class="['contact-result', { active: chosenContact?.uuid === c.uuid }]" @click="chosenContact = c">
                <span>{{ c.email }}</span>
                <span class="enrollment-meta">{{ [c.firstName, c.lastName].filter(Boolean).join(' ') }} · {{ c.status }}</span>
              </button>
            </li>
          </ul>
        </template>
        <template v-else>
          <select v-model="chosenList" class="form-select" aria-label="List">
            <option value="">Select a list...</option>
            <option v-for="l in staticLists" :key="l.uuid" :value="l.uuid">{{ l.name }} ({{ l.contactCount }})</option>
          </select>
          <p class="form-hint">Active, unsuppressed members are enrolled (up to 50,000).</p>
        </template>
        <p v-if="enrollError" role="alert" class="drawer-error">{{ enrollError }}</p>
        <p v-if="enrollResult" role="status" class="enroll-result">{{ enrollResult }}</p>
        <div class="modal-actions">
          <button @click="enrollOpen = false" class="btn btn-secondary">Close</button>
          <button @click="submitEnroll" :disabled="enrollBusy" class="btn btn-primary">{{ enrollBusy ? 'Enrolling...' : 'Enroll' }}</button>
        </div>
      </div>
    </div>
  </div>
  </AppLayout>
</template>

<style scoped>
.workflow-editor {
  flex: 1;
  min-width: 0;
  overflow: auto;
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

/* M4 additions */
.editor-header { gap: 16px; flex-wrap: wrap; }
.header-meta { flex-wrap: wrap; row-gap: 2px; }
.meta-item, .btn, .changes-badge { white-space: nowrap; }
.header-actions { flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
.status-archived { background: #e5e7eb; color: #374151; }

.changes-badge {
  padding: 2px 8px;
  border-radius: 9999px;
  font-size: 11px;
  font-weight: 500;
  background: #e0e7ff;
  color: #3730a3;
}

.btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.btn-sm {
  padding: 6px 10px;
  font-size: 13px;
}

.btn-danger {
  background: #dc2626;
  color: white;
}

.btn-danger:hover {
  background: #b91c1c;
}

.editor-banner {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 20px;
  font-size: 13px;
  flex-shrink: 0;
}

.editor-banner span {
  flex: 1;
}

.banner-error { background: #fef2f2; color: #991b1b; border-bottom: 1px solid #fecaca; }
.banner-info { background: #f0fdf4; color: #166534; border-bottom: 1px solid #bbf7d0; }
.banner-muted { background: #f3f4f6; color: #4b5563; border-bottom: 1px solid #e5e7eb; }

.workflow-node {
  position: relative;
}

.workflow-node.has-error {
  box-shadow: 0 0 0 3px #ef4444;
}

.workflow-node.is-selected {
  outline: 2px solid #6366f1;
  outline-offset: 2px;
}

.node-stats {
  margin-top: 8px;
  padding-top: 6px;
  border-top: 1px dashed rgb(0 0 0 / 0.1);
  font-size: 11px;
  color: #4b5563;
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.node-stats-line {
  width: 100%;
  color: #6b7280;
}

.handle-label {
  position: absolute;
  top: 50%;
  transform: translateY(-160%);
  font-size: 10px;
  font-weight: 600;
}

.handle-label-yes { right: -4px; color: #16a34a; }
.handle-label-no { left: -2px; color: #dc2626; }

fieldset.config-body {
  border: 0;
  margin: 0;
  min-width: 0;
}

.form-hint {
  font-size: 12px;
  color: #6b7280;
  margin-top: 6px;
}

.form-link {
  color: #4f46e5;
  text-decoration: underline;
}

.form-warning {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 12px;
  margin-bottom: 16px;
  background: #fffbeb;
  border-radius: 8px;
  font-size: 12px;
  color: #92400e;
}

.form-warning svg {
  flex-shrink: 0;
  margin-top: 1px;
}

.checkbox-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 12px;
}

.checkbox-row,
.form-group .checkbox-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: #374151;
  font-weight: 400 !important;
  margin: 0 !important;
}

.node-error-list {
  margin-bottom: 16px;
  padding: 10px 12px 10px 28px;
  list-style: disc;
  background: #fef2f2;
  color: #991b1b;
  border-radius: 8px;
  font-size: 12px;
}

.problem-list {
  flex: 1;
  overflow-y: auto;
  padding: 12px;
}

.problem-item {
  width: 100%;
  text-align: left;
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px 12px;
  border-radius: 8px;
  font-size: 13px;
  color: #374151;
}

.problem-item:hover:not(:disabled) {
  background: #fef2f2;
}

.problem-step {
  font-size: 11px;
  font-weight: 600;
  color: #b91c1c;
}

/* Enrollments drawer */
.enrollments-drawer {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  width: min(440px, 100%);
  background: white;
  border-left: 1px solid #e5e7eb;
  box-shadow: -4px 0 16px rgb(0 0 0 / 0.08);
  display: flex;
  flex-direction: column;
  z-index: 20;
}

.drawer-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 16px;
  border-bottom: 1px solid #e5e7eb;
}

.drawer-header h3 {
  font-weight: 600;
  color: #111827;
}

.drawer-actions {
  display: flex;
  gap: 8px;
  align-items: center;
}

.drawer-filter {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 16px;
  border-bottom: 1px solid #f3f4f6;
}

.drawer-filter .form-select {
  width: auto;
  padding: 6px 10px;
}

.drawer-count {
  font-size: 12px;
  color: #6b7280;
}

.drawer-body {
  flex: 1;
  overflow-y: auto;
}

.drawer-empty {
  padding: 16px;
  font-size: 13px;
  color: #9ca3af;
}

.drawer-error {
  margin: 8px 16px;
  padding: 8px 10px;
  border-radius: 6px;
  background: #fef2f2;
  color: #991b1b;
  font-size: 12px;
}

.drawer-pager {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 16px;
  border-top: 1px solid #e5e7eb;
  font-size: 12px;
  color: #6b7280;
}

.enrollment-row {
  border-bottom: 1px solid #f3f4f6;
}

.enrollment-main {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 10px 16px;
  cursor: pointer;
}

.enrollment-main:hover {
  background: #f9fafb;
}

.enrollment-info {
  flex: 1;
  min-width: 0;
}

.enrollment-email {
  font-size: 13px;
  font-weight: 500;
  color: #111827;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.enrollment-meta {
  font-size: 11px;
  color: #6b7280;
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.enrollment-error {
  font-size: 11px;
  color: #b91c1c;
  margin-top: 2px;
}

.enrollment-status {
  text-transform: capitalize;
  font-weight: 600;
}

.enrollment-active { color: #2563eb; }
.enrollment-waiting { color: #7c3aed; }
.enrollment-completed { color: #16a34a; }
.enrollment-exited { color: #6b7280; }
.enrollment-failed { color: #dc2626; }
.enrollment-cancelled { color: #9ca3af; }

.enrollment-buttons {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.row-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  font-size: 12px;
  border-radius: 6px;
  color: #374151;
  background: #f3f4f6;
}

.row-btn:hover:not(:disabled) {
  background: #e5e7eb;
}

.row-btn:disabled {
  opacity: 0.5;
}

.step-timeline {
  padding: 4px 16px 12px 40px;
}

.timeline-step {
  display: flex;
  gap: 10px;
  padding: 6px 0;
}

.timeline-dot {
  width: 8px;
  height: 8px;
  border-radius: 9999px;
  margin-top: 5px;
  flex-shrink: 0;
  background: #9ca3af;
}

.step-succeeded { background: #22c55e; }
.step-waiting { background: #8b5cf6; }
.step-failed { background: #ef4444; }
.step-skipped { background: #d1d5db; }

.timeline-title {
  font-size: 12px;
  font-weight: 500;
  color: #111827;
}

.timeline-status {
  font-weight: 400;
  color: #6b7280;
}

/* Dialogs */
.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgb(0 0 0 / 0.35);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 50;
  padding: 16px;
}

.modal {
  width: min(460px, 100%);
  background: white;
  border-radius: 12px;
  padding: 20px;
  box-shadow: 0 20px 40px rgb(0 0 0 / 0.2);
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.modal h3 {
  font-size: 16px;
  font-weight: 600;
  color: #111827;
}

.modal p {
  font-size: 14px;
  color: #4b5563;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 4px;
}

.modal-tabs {
  display: flex;
  gap: 4px;
}

.modal-tab {
  padding: 6px 12px;
  font-size: 13px;
  border-radius: 6px;
  color: #6b7280;
}

.modal-tab.active {
  background: #eef2ff;
  color: #4f46e5;
}

.contact-results {
  max-height: 220px;
  overflow-y: auto;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
}

.contact-result {
  width: 100%;
  text-align: left;
  display: flex;
  flex-direction: column;
  padding: 8px 12px;
  font-size: 13px;
}

.contact-result:hover,
.contact-result.active {
  background: #eef2ff;
}

.enroll-result {
  padding: 8px 10px;
  border-radius: 6px;
  background: #f0fdf4;
  color: #166534;
  font-size: 13px !important;
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
