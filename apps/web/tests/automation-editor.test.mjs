import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { readFileSync, existsSync } from 'node:fs'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'

const require = createRequire(import.meta.url)
const { createRenderer, h, nextTick } = require('vue')
const webRoot = fileURLToPath(new URL('..', import.meta.url))
const srcRoot = path.join(webRoot, 'src')

// vue-flow needs a browser; the stub renders each node through the editor's
// #node-workflow slot so node markup, errors and stats badges are exercised.
const FIXTURES = {
  api: 'const f = globalThis.__automationFixture; export const { automationApi, templateApi, identityApi, domainApi, listApi, webhookApi, contactApi } = f.api',
  'vue-router': `const f = globalThis.__automationFixture
    export const useRoute = () => f.route; export const useRouter = () => f.router
    export const onBeforeRouteLeave = guard => { f.leaveGuard = guard }`,
  '@vue-flow/core': `import { h, defineComponent } from 'vue'
    export const Position = { Top: 'top', Bottom: 'bottom', Left: 'left', Right: 'right' }
    export const MarkerType = { ArrowClosed: 'arrowclosed' }
    export const useVueFlow = () => ({ onConnect: fn => { globalThis.__automationFixture.connect = fn } })
    export const Handle = defineComponent({ props: ['id', 'type', 'position'], setup: p => () => h('handle', { 'data-handle': p.id || p.type }) })
    export const VueFlow = defineComponent({ props: ['nodes', 'edges'], emits: ['node-click'], setup(props, { slots, emit }) {
      globalThis.__automationFixture.flow = { props, click: id => emit('node-click', { node: props.nodes.find(n => n.id === id) }) }
      return () => h('flow', [...(props.nodes || []).map(n => h('flow-node', { 'data-node': n.id }, slots['node-workflow']?.({ id: n.id, data: n.data }))), slots.default?.()])
    } })`,
  '@vue-flow/background': 'export const Background = () => null',
  '@vue-flow/controls': 'export const Controls = () => null',
  '@vue-flow/minimap': 'export const MiniMap = () => null',
}
const STUBS = { 'AppLayout.vue': `import { h } from 'vue'; export default { setup: (_p, { slots }) => () => h('main', slots.default?.()) }` }

async function bundle(entry) {
  const output = await build({
    entryPoints: [path.join(srcRoot, entry)], bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
    plugins: [{ name: 'automation-sfc', setup(builder) {
      builder.onResolve({ filter: /\.css$/ }, () => ({ path: 'css', namespace: 'empty' }))
      builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
      builder.onResolve({ filter: /^(vue-router|@vue-flow\/(core|background|controls|minimap))$/ }, args => ({ path: args.path, namespace: 'fixture' }))
      builder.onResolve({ filter: /\.vue$/ }, args => {
        const file = args.path.startsWith('@/') ? path.join(srcRoot, args.path.slice(2)) : path.resolve(args.resolveDir, args.path)
        const name = path.basename(file)
        return STUBS[name] ? { path: name, namespace: 'stub' } : { path: file, namespace: 'sfc' }
      })
      builder.onResolve({ filter: /^@\// }, args => {
        const base = path.join(srcRoot, args.path.slice(2))
        return { path: [`${base}.ts`, `${base}/index.ts`, base].find(existsSync) }
      })
      builder.onLoad({ filter: /.*/, namespace: 'empty' }, () => ({ contents: '', loader: 'js' }))
      builder.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ contents: FIXTURES[args.path], loader: 'js', resolveDir: webRoot }))
      builder.onLoad({ filter: /.*/, namespace: 'stub' }, args => ({ contents: STUBS[args.path], loader: 'js', resolveDir: webRoot }))
      builder.onLoad({ filter: /.*/, namespace: 'sfc' }, async args => {
        const { descriptor } = parse(await readFile(args.path, 'utf8'), { filename: args.path })
        const compiled = compileScript(descriptor, { id: path.basename(args.path), inlineTemplate: true })
        return { contents: compiled.content, loader: 'ts', resolveDir: path.dirname(args.path) }
      })
    } }],
  })
  return output.outputFiles[0].text
}

const [editorCode, listCode] = await Promise.all([bundle('views/WorkflowEditor.vue'), bundle('views/Automations.vue')])

const node = (type, text = '') => ({ type, text, props: {}, children: [], parent: null, options: [], addEventListener() {}, removeEventListener() {} })
const renderer = createRenderer({
  createElement: type => node(type), createText: text => node('text', text), createComment: text => node('comment', text),
  setText: (item, text) => { item.text = text }, setElementText: (item, text) => { item.text = text; item.children = [] },
  patchProp: (item, key, _old, value) => { item.props[key] = value },
  insert(item, parent, anchor) {
    if (item.parent) item.parent.children.splice(item.parent.children.indexOf(item), 1)
    item.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    if (index < 0) parent.children.push(item)
    else parent.children.splice(index, 0, item)
  },
  remove(item) { if (item.parent) item.parent.children.splice(item.parent.children.indexOf(item), 1); item.parent = null },
  parentNode: item => item.parent,
  nextSibling: item => item.parent?.children[item.parent.children.indexOf(item) + 1] || null,
})
const all = item => [item, ...item.children.flatMap(all)]
const text = item => all(item).filter(n => n.type !== 'comment').map(n => n.text).join(' ').replace(/\s+/g, ' ')
const flush = async (rounds = 8) => { for (let i = 0; i < rounds; i++) { await Promise.resolve(); await nextTick() } }
const button = (root, label) => all(root).find(n => n.type === 'button' && (n.props['aria-label'] === label || text(n).trim() === label))
const click = async (root, label) => {
  const target = button(root, label)
  assert.ok(target, `button "${label}" not found in: ${text(root).slice(0, 600)}`)
  target.props.onClick?.({})
  await flush()
}
const classes = n => [n.props.class].flat(Infinity).flatMap(c => typeof c === 'string' ? c.split(' ') : c && typeof c === 'object' ? Object.keys(c).filter(k => c[k]) : [])
const flowNode = (root, id) => all(root).find(n => n.props['data-node'] === id)?.children.find(c => classes(c).includes('workflow-node'))
const selectWithOption = (root, value) => all(root).find(n => n.type === 'select' && all(n).some(o => o.type === 'option' && o.props.value === value))
const change = async (target, value) => {
  if (target.props.onChange) target.props.onChange({ target: { value, checked: value } })
  else target.props['onUpdate:modelValue'](value)
  await flush()
}
const dialog = root => all(root).find(n => n.props.role === 'dialog')
const confirmWith = async (root, label) => {
  assert.ok(dialog(root), 'confirmation dialog is open')
  await click(dialog(root), label)
}

globalThis.document = { visibilityState: 'visible', addEventListener() {}, removeEventListener() {} }

const uuid = n => `${String(n).repeat(8).slice(0, 8)}-1111-4111-8111-111111111111`
const LIST = uuid(2), TEMPLATE = uuid(3), IDENTITY = uuid(4), HOOK = uuid(5)
const wf = (nodes, edges) => ({ schemaVersion: 1, nodes: nodes.map(([id, type, config, label = id]) => ({ id, type: 'workflow', position: { x: 0, y: 0 }, data: { label, type, config } })), edges: edges.map(([s, t, h]) => ({ id: `${s}-${t}`, source: s, target: t, ...(h ? { sourceHandle: h } : {}) })) })
const validGraph = () => wf([
  ['trigger-1', 'trigger', { event: 'contact.subscribed', sources: ['signup_form'] }, 'Subscribed'],
  ['email-1', 'email', { templateUuid: TEMPLATE, identityUuid: IDENTITY, trackOpens: true, trackClicks: true }, 'Welcome mail'],
  ['cond-1', 'condition', { field: 'email_opened', emailNodeId: 'email-1', mode: 'if_else' }, 'Opened?'],
  ['hook-1', 'webhook', { webhookUuid: HOOK }, 'Notify CRM'],
], [['trigger-1', 'email-1'], ['email-1', 'cond-1'], ['cond-1', 'hook-1', 'yes']])
const automation = (over = {}) => ({
  id: 1, uuid: 'auto-1', name: 'Welcome', description: '', triggerType: 'contact.subscribed', workflow: validGraph(), status: 'draft',
  reentryPolicy: 'never', publishedVersion: null, hasUnpublishedChanges: false, activatedAt: null, archivedAt: null,
  stats: { enrolled: 0, active: 0, waiting: 0, completed: 0, exited: 0, failed: 0, cancelled: 0 }, enrolledCount: 0, completedCount: 0, inProgressCount: 0,
  createdAt: '2026-10-01T10:00:00Z', updatedAt: '2026-10-02T10:00:00Z', ...over,
})

function mount(t, code, { routeName = 'automation-edit', current = automation(), api = {} } = {}) {
  const calls = []
  const record = (name, result) => (...args) => { calls.push({ name, args }); return typeof result === 'function' ? result(...args) : Promise.resolve(result) }
  const fixture = globalThis.__automationFixture = {
    route: { name: routeName, params: routeName === 'automation-new' ? {} : { uuid: 'auto-1' } },
    router: { push: record('push'), replace: record('replace') },
    api: {
      automationApi: {
        get: record('get', () => Promise.resolve(current)),
        list: record('list', { automations: [current], total: 1, page: 1, pageSize: 100 }),
        create: record('create', body => Promise.resolve(automation({ ...body, uuid: 'auto-new' }))),
        update: record('update', (_u, body) => Promise.resolve({ ...current, ...body, hasUnpublishedChanges: current.publishedVersion != null })),
        validate: record('validate', { valid: true, errors: [] }),
        activate: record('activate', () => Promise.resolve({ automation: { ...current, status: 'active', publishedVersion: (current.publishedVersion ?? 0) + 1, hasUnpublishedChanges: false }, version: (current.publishedVersion ?? 0) + 1, published: true })),
        pause: record('pause', () => Promise.resolve({ ...current, status: 'paused' })),
        archive: record('archive', () => Promise.resolve({ automation: { ...current, status: 'archived' }, cancelledEnrollments: 2 })),
        remove: record('remove'),
        stats: record('stats', { enrolled: 3, active: 1, waiting: 1, completed: 1, exited: 0, failed: 1, cancelled: 0, completionRate: 33, version: 1,
          nodes: { 'email-1': { entered: 3, waiting: 0, succeeded: 3, skipped: 0, failed: 0, yes: 0, no: 0, sent: 2, opened: 1, clicked: 0 }, 'cond-1': { entered: 2, waiting: 0, succeeded: 2, skipped: 0, failed: 0, yes: 1, no: 1, sent: 0, opened: 0, clicked: 0 } } }),
        enrollments: record('enrollments', { enrollments: [
          { uuid: 'en-1', contactUuid: 'c-1', contactEmail: 'ada@example.test', status: 'failed', exitReason: null, version: 1, currentNodeId: 'email-1', nextRunAt: null, retryCount: 5, error: 'Template is inactive', enrolledAt: '2026-10-02T10:00:00Z', completedAt: null },
          { uuid: 'en-2', contactUuid: 'c-2', contactEmail: 'bob@example.test', status: 'active', exitReason: null, version: 1, currentNodeId: 'cond-1', nextRunAt: '2026-10-02T10:00:00Z', retryCount: 0, error: null, enrolledAt: '2026-10-02T10:00:00Z', completedAt: null },
        ], total: 2, page: 1, pageSize: 25 }),
        enrollment: record('enrollment', { uuid: 'en-1', steps: [{ nodeId: 'email-1', nodeType: 'email', status: 'failed', outcome: null, error: 'Template is inactive', startedAt: '2026-10-02T10:00:00Z', finishedAt: null, resumeAt: null }] }),
        retryEnrollment: record('retryEnrollment', { uuid: 'en-1', status: 'active', error: null }),
        cancelEnrollment: record('cancelEnrollment', { uuid: 'en-2', status: 'cancelled', exitReason: 'cancelled' }),
        enroll: record('enroll', { enrolled: 4, skipped: 1 }),
        ...api,
      },
      templateApi: { list: record('templates', [{ uuid: TEMPLATE, name: 'Welcome template', isActive: true }, { uuid: uuid(9), name: 'Old', isActive: false }]) },
      identityApi: { list: record('identities', [
        { uuid: IDENTITY, email: 'hello@verified.test', displayName: 'Hello', domainId: 5, canSend: true },
        { uuid: uuid(6), email: 'x@unverified.test', displayName: '', domainId: 6, canSend: true },
      ]) },
      domainApi: { list: record('domains', [{ id: 5, status: 'active', sesVerified: true }, { id: 6, status: 'active', sesVerified: false }]) },
      listApi: { list: record('lists', [{ uuid: LIST, name: 'Newsletter', type: 'static', contactCount: 9 }, { uuid: uuid(7), name: 'Engaged', type: 'segment', contactCount: 1 }]) },
      webhookApi: { list: record('webhooks', [{ uuid: HOOK, name: 'CRM', active: true }, { uuid: uuid(8), name: 'Off', active: false }]) },
      contactApi: { search: record('search', { contacts: [{ uuid: 'c-9', email: 'cy@example.test', status: 'active' }] }) },
    },
  }
  globalThis.window = { confirm: () => fixture.confirmAnswer ?? true, addEventListener() {}, removeEventListener() {} }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', code)(require, module, module.exports)
  const root = node('root')
  const app = renderer.createApp({ setup: () => () => h(module.exports.default) })
  app.component('router-link', { setup: (_p, { slots }) => () => h('a', slots.default?.()) })
  app.mount(root)
  t.after(() => app.unmount())
  return { root, calls, fixture, named: name => calls.filter(c => c.name === name) }
}

test('a new automation offers only supported steps and saves the canonical graph', async t => {
  const f = mount(t, editorCode, { routeName: 'automation-new' })
  await flush()
  const page = text(f.root)
  for (const unsupported of ['Tag', 'Form Submitted', 'Email opened trigger', 'Webhook URL', 'HTTP Method']) assert.ok(!page.includes(unsupported), unsupported)
  for (const supported of ['Contact subscribed to list', 'Contact created', 'Manual enrollment', 'Send email', 'Add to list', 'Remove from list', 'Update contact field', 'Webhook', 'Wait', 'If/Else', 'Filter']) assert.ok(page.includes(supported), supported)
  assert.equal(f.named('get').length, 0)

  f.fixture.flow.click('trigger-1'); await flush()
  assert.match(text(f.root), /Enroll contacts added by/)
  const importBox = all(f.root).find(n => n.type === 'label' && text(n).includes('CSV import')).children.find(c => c.type === 'input')
  assert.equal(importBox.props.checked, false, 'CSV imports are opt-in')
  await change(importBox, true)
  assert.match(text(f.root), /CSV imports enroll every imported contact/)

  await click(f.root, 'Save')
  const [body] = f.named('create')[0].args
  assert.equal(body.workflow.nodes[0].type, 'workflow')
  assert.equal(body.workflow.nodes[0].data.config.event, 'contact.subscribed')
  assert.deepEqual(body.workflow.nodes[0].data.config.sources, ['signup_form', 'api', 'import', 'manual', 'preference_center', 'double_opt_in'])
  assert.deepEqual(f.named('replace')[0].args, ['/automations/auto-new'])
  assert.equal(f.fixture.leaveGuard(), true, 'saved edits need no leave confirmation')
})

test('activation runs the graph rules first and highlights failing steps', async t => {
  const broken = automation({ workflow: wf([['trigger-1', 'trigger', { event: 'contact.subscribed' }], ['email-1', 'email', { templateId: 'welcome' }, 'Welcome mail']], [['trigger-1', 'email-1']]) })
  const f = mount(t, editorCode, { current: broken })
  await flush()
  await click(f.root, 'Activate')
  assert.match(text(f.root), /enrolled in version 1/)
  await confirmWith(f.root, 'Activate')
  assert.equal(f.named('activate').length, 0)
  assert.ok(classes(flowNode(f.root, 'email-1')).includes('has-error'))
  assert.match(text(f.root), /2 problems/)
  const problem = all(f.root).find(n => n.type === 'button' && text(n).includes('Choose the template again'))
  problem.props.onClick({}); await flush()
  assert.match(text(f.root), /Configure email/)
  // Only senders on active, SES-verified domains are offered.
  const sender = selectWithOption(f.root, IDENTITY)
  assert.ok(!all(sender).some(o => o.props.value === uuid(6)))
  await change(sender, IDENTITY)
  assert.equal(f.fixture.flow.props.nodes[1].data.config.identityUuid, IDENTITY)
  assert.equal(f.fixture.flow.props.nodes[1].data.config.identityId, undefined)
})

test('server validation errors from activate are shown on the graph', async t => {
  const errors = [{ nodeId: 'hook-1', field: 'webhookUuid', message: 'Choose one of your own webhook endpoints' }]
  const f = mount(t, editorCode, { api: { activate: () => Promise.reject(Object.assign(new Error('Automation is not valid'), { status: 400, data: { errors } })) } })
  await flush()
  await click(f.root, 'Activate')
  await confirmWith(f.root, 'Activate')
  assert.match(text(f.root), /Automation is not valid\. Fix the highlighted steps\./)
  assert.ok(classes(flowNode(f.root, 'hook-1')).includes('has-error'))
  assert.ok(!classes(flowNode(f.root, 'email-1')).includes('has-error'))
})

test('a live automation shows its version, stats and publishes changes with a confirmation', async t => {
  const f = mount(t, editorCode, { current: automation({ status: 'active', publishedVersion: 1, hasUnpublishedChanges: true }) })
  await flush()
  const page = text(f.root)
  assert.match(page, /Version 1 live/)
  assert.match(page, /Unpublished changes/)
  assert.match(text(flowNode(f.root, 'email-1')), /3 entered.*2 sent · 1 opened · 0 clicked/)
  assert.match(text(flowNode(f.root, 'cond-1')), /1 yes · 1 no/)
  assert.ok(all(flowNode(f.root, 'cond-1')).some(n => n.props['data-handle'] === 'no'))
  assert.ok(!all(flowNode(f.root, 'cond-1')).some(n => n.props['data-handle'] === 'source'), 'conditions have no plain source handle')
  await click(f.root, 'Publish changes')
  assert.match(text(f.root), /contacts already in progress continue on version 1/)
  await confirmWith(f.root, 'Publish')
  assert.deepEqual(f.named('activate')[0].args, ['auto-1', { publishDraft: true }])
  assert.match(text(f.root), /Version 2 is live/)
  await click(f.root, 'Pause')
  await confirmWith(f.root, 'Pause')
  assert.equal(f.named('pause').length, 1)
})

test('a paused automation with changes can resume without publishing', async t => {
  const f = mount(t, editorCode, { current: automation({ status: 'paused', publishedVersion: 2, hasUnpublishedChanges: true }) })
  await flush()
  await click(f.root, 'Resume without publishing')
  await confirmWith(f.root, 'Resume')
  assert.deepEqual(f.named('activate')[0].args, ['auto-1', { publishDraft: false }])
  assert.equal(f.named('update').length, 0)
})

test('archived automations are read-only', async t => {
  const f = mount(t, editorCode, { current: automation({ status: 'archived', publishedVersion: 1 }) })
  await flush()
  assert.match(text(f.root), /archived and read-only/)
  for (const label of ['Save', 'Validate', 'Activate', 'Archive']) assert.equal(button(f.root, label), undefined, label)
  assert.ok(!text(f.root).includes('Add Steps'))
  assert.equal(f.fixture.flow.props.nodes.length, 4)
})

test('the enrollments drawer filters, retries and shows the step timeline', async t => {
  const f = mount(t, editorCode, { current: automation({ status: 'active', publishedVersion: 1 }) })
  await flush()
  await click(f.root, 'Enrollments')
  const drawer = text(f.root)
  assert.match(drawer, /ada@example\.test/)
  assert.match(drawer, /Template is inactive/)
  assert.match(drawer, /bob@example\.test.*Opened\?/)
  await click(f.root, 'Retry')
  assert.deepEqual(f.named('retryEnrollment')[0].args, ['auto-1', 'en-1'])
  const row = all(f.root).find(n => classes(n).includes('enrollment-main') && text(n).includes('bob@'))
  row.props.onClick({}); await flush()
  assert.deepEqual(f.named('enrollment')[0].args, ['auto-1', 'en-2'])
  await change(selectWithOption(f.root, 'waiting'), 'waiting')
  assert.deepEqual(f.named('enrollments').at(-1).args, ['auto-1', { status: 'waiting', page: 1, pageSize: 25 }])
  await click(f.root, 'Enroll contacts')
  await click(f.root, 'A list')
  await change(all(dialog(f.root)).find(n => n.type === 'select'), LIST)
  await click(dialog(f.root), 'Enroll')
  assert.deepEqual(f.named('enroll')[0].args, ['auto-1', { listUuid: LIST }])
  assert.match(text(f.root), /Enrolled 4; skipped 1/)
})

test('leaving with unsaved edits asks first', async t => {
  const f = mount(t, editorCode)
  await flush()
  assert.equal(f.fixture.leaveGuard(), true)
  all(f.root).find(n => n.type === 'input' && n.props['aria-label'] === 'Automation name').props['onUpdate:modelValue']('Renamed')
  await flush()
  f.fixture.confirmAnswer = false
  assert.equal(f.fixture.leaveGuard(), false)
})

test('the list shows live counts, archive and delete only for draft or archived', async t => {
  const rows = [
    automation({ uuid: 'a', name: 'Live one', status: 'active', publishedVersion: 1, stats: { enrolled: 12, active: 4, waiting: 2, completed: 7, exited: 1, failed: 0, cancelled: 0 } }),
    automation({ uuid: 'b', name: 'Old one', status: 'archived', triggerType: 'manual' }),
  ]
  const f = mount(t, listCode, { api: { list: () => Promise.resolve({ automations: rows, total: 2, page: 1, pageSize: 100 }) } })
  await flush()
  const page = text(f.root)
  assert.match(page, /12 Enrolled 4 In Progress 7 Completed/)
  assert.match(page, /Manual enrollment/)
  assert.ok(button(f.root, 'Archived'))
  assert.ok(button(f.root, 'Delete Old one'))
  assert.equal(button(f.root, 'Delete Live one'), undefined)
  await click(f.root, 'Archive Live one')
  await confirmWith(f.root, 'Archive')
  assert.equal(f.named('archive').length, 1)
})

test('pausing or resuming from the list uses the server response and surfaces validation errors', async t => {
  const errors = [{ nodeId: 'email-1', field: 'templateUuid', message: 'Choose a template' }]
  const rows = [automation({ uuid: 'p', name: 'Paused one', status: 'paused', publishedVersion: 1 })]
  const f = mount(t, listCode, { api: {
    list: () => Promise.resolve({ automations: rows, total: 1, page: 1, pageSize: 100 }),
    activate: () => Promise.reject(Object.assign(new Error('Automation is not valid'), { status: 400, data: { errors } })),
  } })
  await flush()
  await click(f.root, 'Resume Paused one')
  assert.match(text(f.root), /Automation is not valid\s*: Choose a template/)
  assert.ok(button(f.root, 'Open in editor'))
})

test('WorkflowBuilder.vue is gone and nothing references it', () => {
  assert.ok(!existsSync(path.join(srcRoot, 'components/workflow/WorkflowBuilder.vue')))
  const editor = readFileSync(path.join(srcRoot, 'views/WorkflowEditor.vue'), 'utf8')
  assert.ok(!editor.includes('localStorage'), 'the editor uses automationApi, not raw fetch with a stored token')
  assert.ok(!editor.includes('fetch('))
  assert.ok(!editor.includes("'contact_added'"))
})
