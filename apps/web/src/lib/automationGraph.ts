// Pure automation-graph helpers mirroring apps/api/internal/service/automation_graph.go.
// The server is authoritative (it also checks lists, templates, senders and
// webhooks); these run the same structural and publish rules without the
// database so the editor can show problems early. Shared fixtures in
// tests/fixtures/automation-graphs.json keep both sides in step.

export type NodeKind = 'trigger' | 'email' | 'delay' | 'condition' | 'action' | 'webhook'

export interface GraphNode {
  id: string
  type?: string
  position?: { x: number; y: number }
  data: { label?: string; type?: string; config?: Record<string, unknown> | null }
}

export interface GraphEdge {
  id?: string
  source: string
  target: string
  sourceHandle?: string | null
  targetHandle?: string | null
  type?: string
  animated?: boolean
}

export interface Graph {
  schemaVersion?: number
  nodes: GraphNode[]
  edges: GraphEdge[]
}

export interface GraphError {
  nodeId: string
  field: string
  message: string
}

export const WORKFLOW_SCHEMA_VERSION = 1
export const MAX_WORKFLOW_NODES = 100
export const MAX_WORKFLOW_EDGES = 200
export const MAX_WORKFLOW_JSON_BYTES = 256 << 10
export const MAX_AUTOMATION_VALUE = 1000
const MAX_NODE_LABEL = 200
const MAX_SUBJECT_BYTES = 998

export const TRIGGER_EVENTS = [
  { value: 'contact.subscribed', label: 'Contact subscribed to list' },
  { value: 'contact.created', label: 'Contact created' },
  { value: 'manual', label: 'Manual enrollment' },
] as const

export const TRIGGER_SOURCES = [
  { value: 'signup_form', label: 'Signup forms' },
  { value: 'api', label: 'API' },
  { value: 'import', label: 'CSV import' },
  { value: 'manual', label: 'Added by hand' },
  { value: 'preference_center', label: 'Preference center' },
  { value: 'double_opt_in', label: 'Double opt-in confirmation' },
  { value: 'automation', label: 'Added by another automation' },
  { value: 'unknown', label: 'Unknown' },
] as const

// Imports, other automations and unknown paths enroll only when an automation opts in.
export const DEFAULT_TRIGGER_SOURCES = ['signup_form', 'api', 'manual', 'preference_center', 'double_opt_in']

export const NODE_KINDS: NodeKind[] = ['trigger', 'email', 'delay', 'condition', 'action', 'webhook']

export const CONDITION_FIELDS = [
  { value: 'email_opened', label: 'Email opened' },
  { value: 'email_clicked', label: 'Link clicked' },
  { value: 'engagement_score', label: 'Engagement score' },
  { value: 'custom_field', label: 'Contact attribute' },
] as const

export const OPERATORS = [
  { value: 'equals', label: 'Equals' },
  { value: 'not_equals', label: 'Not equals' },
  { value: 'contains', label: 'Contains' },
  { value: 'greater_than', label: 'Greater than' },
  { value: 'less_than', label: 'Less than' },
] as const

const SCORE_OPERATORS = ['equals', 'not_equals', 'greater_than', 'less_than']
const FIELD_OPERATORS = ['equals', 'not_equals', 'contains', 'greater_than', 'less_than']

/** Operators a condition field accepts; opened/clicked take none. */
export function operatorsFor(field: string) {
  const allowed = field === 'engagement_score' ? SCORE_OPERATORS : field === 'custom_field' ? FIELD_OPERATORS : []
  return OPERATORS.filter(o => allowed.includes(o.value))
}

export const ACTIONS = [
  { value: 'add_to_list', label: 'Add to list' },
  { value: 'remove_from_list', label: 'Remove from list' },
  { value: 'update_field', label: 'Update contact field' },
] as const

const DELAY_MINUTES: Record<string, number> = { minutes: 1, hours: 60, days: 1440, weeks: 10080 }
const MAX_DELAY_MINUTES = 365 * 1440

const TRIGGER_ALIASES: Record<string, string> = {
  contact_added: 'contact.created',
  contact_created: 'contact.created',
  contact_subscribed: 'contact.subscribed',
  subscribed: 'contact.subscribed',
}

const NODE_ID_RE = /^[A-Za-z0-9_-]{1,100}$/
const ATTRIBUTE_KEY_RE = /^[A-Za-z0-9_.-]{1,64}$/
// The forms Go's uuid.Parse accepts: plain, braced, urn:uuid: and 32 hex digits.
const UUID_RE = /^([0-9a-f]{8})-?([0-9a-f]{4})-?([0-9a-f]{4})-?([0-9a-f]{4})-?([0-9a-f]{12})$/i

export class TriggerTypeMismatchError extends Error {
  constructor() { super("triggerType does not match the trigger step's event") }
}

export function normalizeTriggerType(t: string | null | undefined): string {
  const v = (t ?? '').trim()
  return TRIGGER_ALIASES[v] ?? v
}

/** Label for a trigger type, shared by the list and the editor. */
export function triggerLabel(type: string | null | undefined): string {
  const t = normalizeTriggerType(type)
  return TRIGGER_EVENTS.find(e => e.value === t)?.label ?? (t || 'No trigger')
}

const isKind = (v: unknown): v is NodeKind => typeof v === 'string' && (NODE_KINDS as string[]).includes(v)

/**
 * Returns the canonical graph saved by the API plus the trigger type and
 * config derived from the trigger node. Positions and labels are kept; edge
 * styling is dropped. Throws TriggerTypeMismatchError when triggerType
 * disagrees with the trigger node.
 */
export function normalizeGraph(w: Graph | null | undefined, triggerType = '', cfg?: Record<string, unknown>) {
  let type = normalizeTriggerType(triggerType)
  const source: Graph = w ?? {
    nodes: [{ id: 'trigger-1', position: { x: 400, y: 100 }, data: { label: 'Trigger', type: 'trigger', config: { ...(cfg ?? {}), event: type || 'contact.subscribed' } } }],
    edges: [],
  }
  let triggerConfig: Record<string, unknown> = cfg ?? {}
  let triggerSeen = false
  const nodes = (source.nodes ?? []).map(n => {
    let kind = n.data?.type ?? ''
    if (isKind(n.type) && (kind === '' || kind === n.type)) kind = n.type
    const config: Record<string, unknown> = { ...(n.data?.config ?? {}) }
    if (kind === 'trigger' && !triggerSeen) {
      triggerSeen = true
      const event = normalizeTriggerType(typeof config.event === 'string' ? config.event : '') || type || 'contact.subscribed'
      if (type !== '' && type !== event) throw new TriggerTypeMismatchError()
      config.event = event
      type = event
      triggerConfig = { ...config }
    }
    return {
      id: String(n.id ?? '').trim(),
      type: 'workflow',
      position: { x: Number(n.position?.x ?? 0), y: Number(n.position?.y ?? 0) },
      data: { label: n.data?.label ?? '', type: kind, config },
    }
  })
  const edges = (source.edges ?? []).map(e => {
    const out: GraphEdge & { id: string } = { id: e.id ?? '', source: e.source ?? '', target: e.target ?? '' }
    if (e.sourceHandle) out.sourceHandle = e.sourceHandle
    if (e.targetHandle) out.targetHandle = e.targetHandle
    if (e.type) out.type = e.type
    if (e.animated) out.animated = true
    return out
  })
  return { workflow: { schemaVersion: WORKFLOW_SCHEMA_VERSION, nodes, edges }, triggerType: type, triggerConfig }
}

const CONFIG_TYPES: Record<NodeKind, Record<string, string>> = {
  trigger: { event: 'string', listUuid: 'string', sources: 'strings' },
  email: { templateUuid: 'string', subject: 'string', identityUuid: 'string', trackOpens: 'bool', trackClicks: 'bool', templateId: 'scalar', identityId: 'scalar' },
  delay: { duration: 'number', unit: 'string' },
  condition: { field: 'string', mode: 'string', emailNodeId: 'string', attribute: 'string', operator: 'string', value: 'scalar' },
  action: { action: 'string', listUuid: 'string', attribute: 'string', value: 'scalar' },
  webhook: { webhookUuid: 'string', url: 'string', method: 'string' },
}
const TYPE_NAMES: Record<string, string> = { string: 'text', number: 'a number', bool: 'true or false', scalar: 'text or a number', strings: 'a list of text values' }

function hasType(v: unknown, want: string) {
  switch (want) {
    case 'string': return typeof v === 'string'
    case 'number': return typeof v === 'number'
    case 'bool': return typeof v === 'boolean'
    case 'scalar': return typeof v === 'string' || typeof v === 'number'
    case 'strings': return Array.isArray(v) && v.every(s => typeof s === 'string')
  }
  return false
}

const err = (nodeId: string, field: string, message: string): GraphError => ({ nodeId, field, message })
const byteLength = (s: string) => new TextEncoder().encode(s).length

/** The limits checked on every save; an incomplete draft passes. */
export function validateStructure(w: Graph | null | undefined): GraphError[] {
  if (!w) return [err('', 'workflow', 'Workflow is required')]
  const errs: GraphError[] = []
  if (w.nodes.length > MAX_WORKFLOW_NODES) errs.push(err('', 'nodes', `A workflow can have at most ${MAX_WORKFLOW_NODES} steps`))
  if (w.edges.length > MAX_WORKFLOW_EDGES) errs.push(err('', 'edges', `A workflow can have at most ${MAX_WORKFLOW_EDGES} connections`))
  if (byteLength(JSON.stringify(w)) > MAX_WORKFLOW_JSON_BYTES) errs.push(err('', 'workflow', 'Workflow is larger than 256 KiB'))
  if (errs.length) return errs
  const seen = new Set<string>()
  for (const n of w.nodes) {
    if (!NODE_ID_RE.test(n.id)) { errs.push(err(n.id, 'id', "Step ids may contain only letters, digits, '-' and '_' (1-100 characters)")); continue }
    if (seen.has(n.id)) { errs.push(err(n.id, 'id', 'Step id is used more than once')); continue }
    seen.add(n.id)
    if ([...(n.data.label ?? '')].length > MAX_NODE_LABEL) errs.push(err(n.id, 'label', `Step name is longer than ${MAX_NODE_LABEL} characters`))
    const kind = n.data.type
    if (!isKind(kind)) { errs.push(err(n.id, 'type', `Unknown step type "${String(kind ?? '').slice(0, 40)}"`)); continue }
    const config = n.data.config ?? {}
    for (const k of Object.keys(config).sort()) {
      const want = CONFIG_TYPES[kind][k]
      const v = config[k]
      if (!want || v == null || hasType(v, want)) continue
      errs.push(err(n.id, k, `${k} must be ${TYPE_NAMES[want]}`))
    }
  }
  if (w.edges.some(e => (e.id ?? '').length > 200 || e.source.length > 100 || e.target.length > 100 || (e.sourceHandle ?? '').length > 100 || (e.targetHandle ?? '').length > 100)) {
    errs.push(err('', 'edges', 'Connection fields are too long'))
  }
  return errs
}

type Succ = (id: string) => string[]

function reachableFrom(start: string, skip: string, succ: Succ) {
  const seen = new Set([start])
  const stack = [start]
  while (stack.length) {
    for (const t of succ(stack.pop()!)) {
      if (t !== skip && !seen.has(t)) { seen.add(t); stack.push(t) }
    }
  }
  return seen
}

function successors(w: Graph): Succ {
  const out = new Map<string, string[]>()
  for (const e of w.edges) out.set(e.source, [...(out.get(e.source) ?? []), e.target])
  return id => out.get(id) ?? []
}

const str = (c: Record<string, unknown>, k: string) => (typeof c[k] === 'string' ? (c[k] as string).trim() : '')
const scalar = (c: Record<string, unknown>, k: string) => (typeof c[k] === 'string' ? (c[k] as string) : typeof c[k] === 'number' ? String(c[k]) : '')

/** Canonical lower-case UUID, or '' when s is not a UUID. */
export function canonicalUuid(s: string) {
  let v = s
  if (v.length === 45 && v.slice(0, 9).toLowerCase() === 'urn:uuid:') v = v.slice(9)
  else if (v.length === 38 && v[0] === '{' && v[37] === '}') v = v.slice(1, 37)
  // Length 36 forces all four hyphens and 32 none.
  const m = v.length === 36 || v.length === 32 ? UUID_RE.exec(v) : null
  return m ? m.slice(1, 6).join('-').toLowerCase() : ''
}

const notSupported = (id: string, field: string, value: unknown) => err(id, field, `${String(value)} is not supported`)

function checkConfig(n: GraphNode, errs: GraphError[]) {
  const c = n.data.config ?? {}
  const id = n.id
  const uuidAt = (field: string, value: string, missing: string) => {
    const u = canonicalUuid(value)
    if (!u) errs.push(err(id, field, missing))
    return u
  }
  switch (n.data.type) {
    case 'trigger': {
      const event = normalizeTriggerType(str(c, 'event'))
      if (event === 'contact.subscribed' || event === 'contact.created') {
        if (Array.isArray(c.sources)) {
          const allowed = TRIGGER_SOURCES.map(s => s.value as string)
          for (const s of c.sources) if (!allowed.includes(s as string)) errs.push(notSupported(id, 'sources', s))
          if (c.sources.length === 0) errs.push(err(id, 'sources', 'Choose at least one source'))
        }
        if (str(c, 'listUuid')) {
          if (event !== 'contact.subscribed') errs.push(err(id, 'listUuid', 'Only the subscribed trigger can filter by list'))
          else uuidAt('listUuid', str(c, 'listUuid'), 'Choose a list')
        }
      } else if (event === 'manual') {
        if (str(c, 'listUuid')) errs.push(err(id, 'listUuid', 'Manual enrollment has no list filter'))
      } else if (event === '') {
        errs.push(err(id, 'event', 'Choose a trigger'))
      } else {
        errs.push(notSupported(id, 'event', event))
      }
      break
    }
    case 'email': {
      if (!str(c, 'templateUuid') && scalar(c, 'templateId')) errs.push(err(id, 'templateUuid', 'Choose the template again; this step refers to a template that does not exist'))
      else uuidAt('templateUuid', str(c, 'templateUuid'), 'Choose a template')
      if (!str(c, 'identityUuid') && scalar(c, 'identityId')) errs.push(err(id, 'identityUuid', 'Choose the sender again; this step refers to a sender that does not exist'))
      else uuidAt('identityUuid', str(c, 'identityUuid'), 'Choose a sender')
      const subject = str(c, 'subject')
      if (byteLength(subject) > MAX_SUBJECT_BYTES || /[\r\n]/.test(subject)) errs.push(err(id, 'subject', `Subject must be one line of at most ${MAX_SUBJECT_BYTES} bytes`))
      break
    }
    case 'delay': {
      const d = c.duration
      const unit = str(c, 'unit')
      const okDuration = typeof d === 'number' && Number.isInteger(d) && d >= 1 && d <= 1e6
      if (!okDuration) errs.push(err(id, 'duration', 'Duration must be a whole number of at least 1'))
      if (!(unit in DELAY_MINUTES)) errs.push(err(id, 'unit', 'Unit must be minutes, hours, days or weeks'))
      else if (okDuration && (d as number) * DELAY_MINUTES[unit] > MAX_DELAY_MINUTES) errs.push(err(id, 'duration', 'A wait must be between 1 minute and 365 days'))
      break
    }
    case 'condition': {
      const mode = str(c, 'mode')
      if (mode && mode !== 'if_else' && mode !== 'filter') errs.push(notSupported(id, 'mode', mode))
      const field = str(c, 'field')
      const operator = str(c, 'operator')
      if (field === 'email_opened' || field === 'email_clicked') {
        if (!str(c, 'emailNodeId')) errs.push(err(id, 'emailNodeId', 'Choose an email step'))
      } else if (field === 'engagement_score') {
        if (!SCORE_OPERATORS.includes(operator)) errs.push(err(id, 'operator', 'Choose equals, not equals, greater than or less than'))
        const v = scalar(c, 'value').trim()
        if (v === '' || !Number.isFinite(Number(v))) errs.push(err(id, 'value', 'Engagement score must be a number'))
      } else if (field === 'custom_field') {
        if (!ATTRIBUTE_KEY_RE.test(str(c, 'attribute'))) errs.push(err(id, 'attribute', "Attribute names use letters, digits, '.', '-' and '_' (1-64 characters)"))
        if (!FIELD_OPERATORS.includes(operator)) errs.push(err(id, 'operator', 'Choose an operator'))
        if (byteLength(scalar(c, 'value')) > MAX_AUTOMATION_VALUE) errs.push(err(id, 'value', `Value is longer than ${MAX_AUTOMATION_VALUE} bytes`))
      } else if (field === '') {
        errs.push(err(id, 'field', 'Choose what to check'))
      } else {
        errs.push(notSupported(id, 'field', field))
      }
      break
    }
    case 'action': {
      const action = str(c, 'action')
      if (action === 'add_to_list' || action === 'remove_from_list') {
        uuidAt('listUuid', str(c, 'listUuid'), 'Choose a list')
      } else if (action === 'update_field') {
        const attr = str(c, 'attribute')
        if (!ATTRIBUTE_KEY_RE.test(attr)) errs.push(err(id, 'attribute', "Attribute names use letters, digits, '.', '-' and '_' (1-64 characters)"))
        else if (['email', 'status'].includes(attr.toLowerCase())) errs.push(err(id, 'attribute', `Automations cannot change a contact's ${attr.toLowerCase()}`))
        if (byteLength(scalar(c, 'value')) > MAX_AUTOMATION_VALUE) errs.push(err(id, 'value', `Value is longer than ${MAX_AUTOMATION_VALUE} bytes`))
      } else if (action === '') {
        errs.push(err(id, 'action', 'Choose an action'))
      } else {
        errs.push(notSupported(id, 'action', action))
      }
      break
    }
    case 'webhook': {
      for (const k of ['url', 'method']) {
        if (str(c, k)) errs.push(err(id, k, `Webhook ${k} is not supported; choose an endpoint from Settings → Webhooks`))
      }
      uuidAt('webhookUuid', str(c, 'webhookUuid'), 'Choose a webhook endpoint')
      break
    }
  }
}

/**
 * Full validation without the database, mirroring CompileForPublish: one
 * trigger, a reachable DAG, Yes/No handles only on conditions, typed configs,
 * dominance of a condition's email step and the loop guards.
 */
export function validateGraph(input: Graph | null | undefined, triggerType = ''): GraphError[] {
  let w: Graph
  try {
    w = normalizeGraph(input, triggerType).workflow
  } catch (e) {
    return [err('', 'triggerType', (e as Error).message)]
  }
  const structure = validateStructure(w)
  if (structure.length) return structure

  const errs: GraphError[] = []
  const byId = new Map(w.nodes.map(n => [n.id, n]))
  const order = w.nodes.map(n => n.id)
  const triggers = w.nodes.filter(n => n.data.type === 'trigger').map(n => n.id)
  if (triggers.length === 0) return [err('', 'trigger', 'Add a trigger')]
  if (triggers.length > 1) return triggers.slice(1).map(id => err(id, 'type', 'An automation has exactly one trigger'))
  const triggerId = triggers[0]

  const out = new Map<string, GraphEdge[]>()
  const indeg = new Map<string, number>()
  const valid: GraphEdge[] = []
  for (const e of w.edges) {
    if (!byId.has(e.source) || !byId.has(e.target)) { errs.push(err(e.source, 'edges', 'A connection points to a step that does not exist')); continue }
    out.set(e.source, [...(out.get(e.source) ?? []), e])
    indeg.set(e.target, (indeg.get(e.target) ?? 0) + 1)
    valid.push(e)
  }
  if ((indeg.get(triggerId) ?? 0) > 0) errs.push(err(triggerId, 'edges', 'Nothing can connect into the trigger'))
  for (const id of order) {
    const n = byId.get(id)!
    const edges = out.get(id) ?? []
    if (n.data.type === 'condition') {
      let yes = 0, no = 0
      for (const e of edges) {
        if (e.sourceHandle === 'yes') yes++
        else if (e.sourceHandle === 'no') no++
        else errs.push(err(id, 'edges', 'Connect a condition from its Yes or No handle'))
      }
      if (yes > 1 || no > 1) errs.push(err(id, 'edges', 'A condition has at most one Yes and one No connection'))
      else if (str(n.data.config ?? {}, 'mode') === 'filter' && no > 0) errs.push(err(id, 'edges', 'A filter continues only on Yes'))
      else if (yes + no === 0) errs.push(err(id, 'edges', "Connect the condition's Yes or No branch"))
      continue
    }
    if (edges.length > 1) errs.push(err(id, 'edges', 'This step can have only one next step'))
    for (const e of edges) if (e.sourceHandle) errs.push(err(id, 'edges', 'Only conditions have Yes/No connections'))
  }
  if ((out.get(triggerId) ?? []).length !== 1) errs.push(err(triggerId, 'edges', 'Connect the trigger to exactly one first step'))
  if (errs.length) return errs

  const succ = successors({ ...w, edges: valid })
  const remaining = new Map(indeg)
  const queue = order.filter(id => !remaining.get(id))
  let visited = 0
  while (queue.length) {
    const id = queue.shift()!
    visited++
    for (const t of succ(id)) {
      remaining.set(t, (remaining.get(t) ?? 0) - 1)
      if (remaining.get(t) === 0) queue.push(t)
    }
  }
  if (visited !== order.length) return order.filter(id => (remaining.get(id) ?? 0) > 0).map(id => err(id, 'edges', 'Steps cannot loop back'))
  const reach = reachableFrom(triggerId, '', succ)
  for (const id of order) if (!reach.has(id)) errs.push(err(id, 'edges', 'This step cannot be reached from the trigger'))
  if (errs.length) return errs

  for (const n of w.nodes) checkConfig(n, errs)
  if (errs.length) return errs

  const triggerList = canonicalUuid(str(byId.get(triggerId)!.data.config ?? {}, 'listUuid'))
  for (const n of w.nodes) {
    const c = n.data.config ?? {}
    if (n.data.type === 'action' && str(c, 'action') === 'add_to_list' && triggerList && canonicalUuid(str(c, 'listUuid')) === triggerList) {
      errs.push(err(n.id, 'listUuid', "Adding contacts to the trigger's own list would enroll them again"))
    }
    if (n.data.type === 'condition') {
      const field = str(c, 'field')
      const emailNodeId = field === 'email_opened' || field === 'email_clicked' ? str(c, 'emailNodeId') : ''
      if (!emailNodeId) continue
      if (byId.get(emailNodeId)?.data.type !== 'email') errs.push(err(n.id, 'emailNodeId', 'Choose an email step'))
      else if (reachableFrom(triggerId, emailNodeId, succ).has(n.id)) errs.push(err(n.id, 'emailNodeId', 'Every path to this condition must pass the chosen email step'))
    }
  }
  return errs
}

/**
 * Email steps that every path from the trigger to nodeId passes; a condition
 * on opens or clicks may only reference one of these.
 */
export function dominatingEmailNodes(w: Graph, nodeId: string): GraphNode[] {
  const trigger = w.nodes.find(n => n.data.type === 'trigger')
  if (!trigger) return []
  const succ = successors(w)
  if (!reachableFrom(trigger.id, '', succ).has(nodeId)) return []
  return w.nodes.filter(n => n.data.type === 'email' && n.id !== nodeId && !reachableFrom(trigger.id, n.id, succ).has(nodeId))
}

/** Errors grouped by step id; graph-wide errors are under ''. */
export function errorsByNode(errors: GraphError[]) {
  const map: Record<string, GraphError[]> = {}
  for (const e of errors) (map[e.nodeId] ??= []).push(e)
  return map
}
