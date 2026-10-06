import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'

const require = createRequire(import.meta.url)
const webRoot = fileURLToPath(new URL('..', import.meta.url))
function evaluate(code) { const module = { exports: {} }; new Function('require', 'module', 'exports', code)(require, module, module.exports); return module.exports }

const graphBuild = await build({ entryPoints: [`${webRoot}/src/lib/automationGraph.ts`], bundle: true, write: false, platform: 'node', format: 'cjs' })
const graph = evaluate(graphBuild.outputFiles[0].text)
const fixtures = JSON.parse(readFileSync(new URL('./fixtures/automation-graphs.json', import.meta.url), 'utf8'))
const pairs = errs => [...new Set(errs.map(e => `${e.nodeId}/${e.field}`))].sort()
const want = list => list.map(([n, f]) => `${n}/${f}`).sort()

test('validateGraph and validateStructure match the Go rules on every shared fixture', () => {
  for (const c of fixtures.cases) {
    if (c.normalizeError) {
      assert.throws(() => graph.normalizeGraph(c.workflow, c.triggerType), graph.TriggerTypeMismatchError, c.name)
      continue
    }
    const { workflow, triggerType } = graph.normalizeGraph(c.workflow, c.triggerType ?? '')
    if (c.expectTriggerType) assert.equal(triggerType, c.expectTriggerType, c.name)
    assert.deepEqual(pairs(graph.validateStructure(workflow)), want(c.structure ?? []), `${c.name}: structure`)
    if (!c.publish) continue
    const errs = graph.validateGraph(workflow, triggerType)
    assert.deepEqual(pairs(errs), want(c.publish), `${c.name}: publish`)
    for (const e of errs) assert.ok(e.message, `${c.name}: message for ${e.nodeId}/${e.field}`)
  }
})

test('normalizeGraph maps legacy kinds and triggers, strips edge styling and keeps the input intact', () => {
  const input = {
    nodes: [
      { id: ' t ', type: 'trigger', position: { x: 1, y: 2 }, selected: true, data: { label: 'Old', config: { event: 'subscribed' } } },
      { id: 'm', type: 'email', position: { x: 3, y: 4 }, data: { label: 'Mail', type: 'email', config: { templateId: 'welcome' } } },
    ],
    edges: [{ id: 'e1', source: 't', target: 'm', sourceHandle: null, label: 'go', style: { stroke: 'red' }, markerEnd: 'arrow', animated: true, type: 'smoothstep' }],
  }
  const { workflow, triggerType, triggerConfig } = graph.normalizeGraph(input)
  assert.equal(triggerType, 'contact.subscribed')
  assert.deepEqual(triggerConfig, { event: 'contact.subscribed' })
  assert.deepEqual(workflow.nodes[0], { id: 't', type: 'workflow', position: { x: 1, y: 2 }, data: { label: 'Old', type: 'trigger', config: { event: 'contact.subscribed' } } })
  assert.equal(workflow.nodes[1].data.config.templateId, 'welcome')
  assert.deepEqual(workflow.edges, [{ id: 'e1', source: 't', target: 'm', type: 'smoothstep', animated: true }])
  assert.equal(workflow.schemaVersion, 1)
  assert.equal(input.nodes[0].type, 'trigger')
  assert.equal(input.nodes[0].data.config.event, 'subscribed')
  assert.equal(graph.normalizeGraph(null, 'contact_added').workflow.nodes[0].data.config.event, 'contact.created')
  assert.throws(() => graph.normalizeGraph(input, 'contact.created'), graph.TriggerTypeMismatchError)
})

test('dominatingEmailNodes lists only email steps every path passes', () => {
  const node = (id, type) => ({ id, type: 'workflow', position: { x: 0, y: 0 }, data: { label: id, type, config: {} } })
  const w = {
    nodes: [node('t', 'trigger'), node('m1', 'email'), node('c1', 'condition'), node('m2', 'email'), node('d', 'delay'), node('c2', 'condition'), node('loose', 'email')],
    edges: [
      { id: '1', source: 't', target: 'm1' }, { id: '2', source: 'm1', target: 'c1' },
      { id: '3', source: 'c1', sourceHandle: 'yes', target: 'm2' }, { id: '4', source: 'c1', sourceHandle: 'no', target: 'd' },
      { id: '5', source: 'm2', target: 'c2' }, { id: '6', source: 'd', target: 'c2' },
    ],
  }
  const ids = id => graph.dominatingEmailNodes(w, id).map(n => n.id)
  assert.deepEqual(ids('c1'), ['m1'])
  assert.deepEqual(ids('c2'), ['m1'])
  assert.deepEqual(ids('d'), ['m1'])
  assert.deepEqual(ids('loose'), [])
  assert.deepEqual(ids('m1'), [])
})

test('trigger labels, operators and source defaults', () => {
  assert.equal(graph.triggerLabel('contact.subscribed'), 'Contact subscribed to list')
  assert.equal(graph.triggerLabel('contact_added'), 'Contact created')
  assert.equal(graph.triggerLabel('manual'), 'Manual enrollment')
  assert.equal(graph.triggerLabel('tag.added'), 'tag.added')
  assert.deepEqual(graph.operatorsFor('email_opened'), [])
  assert.deepEqual(graph.operatorsFor('engagement_score').map(o => o.value), ['equals', 'not_equals', 'greater_than', 'less_than'])
  assert.ok(graph.operatorsFor('custom_field').some(o => o.value === 'contains'))
  // Owner default: CSV imports, other automations and unknown paths are opt-in.
  for (const off of ['import', 'automation', 'unknown']) assert.ok(!graph.DEFAULT_TRIGGER_SOURCES.includes(off), off)
  assert.equal(graph.canonicalUuid('{11111111-1111-4111-8111-11111111111A}'), '11111111-1111-4111-8111-11111111111a')
  assert.equal(graph.canonicalUuid('1111111111114111811111111111111a'), '11111111-1111-4111-8111-11111111111a')
  assert.equal(graph.canonicalUuid('welcome'), '')
})

// automationApi and templateApi through the real ApiClient with a fake axios.
const apiBuild = await build({
  entryPoints: [`${webRoot}/src/lib/api.ts`], bundle: true, write: false, platform: 'node', format: 'cjs',
  define: { 'import.meta.env.VITE_API_URL': "''" },
  plugins: [{ name: 'axios-fixture', setup(builder) {
    builder.onResolve({ filter: /^axios$/ }, () => ({ path: 'axios', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ contents: 'export default globalThis.__automationAxios', loader: 'js' }))
  } }],
})

function apiFixture() {
  const calls = []
  let reject
  globalThis.localStorage = { getItem: () => 'token', setItem() {}, removeItem() {} }
  globalThis.window = { location: { href: '/automations' } }
  const respond = (method) => async (url, a, b) => {
    calls.push({ method, url, body: method === 'post' || method === 'put' ? a : method === 'delete' ? a?.data : undefined })
    return { data: { data: { ok: true } } }
  }
  globalThis.__automationAxios = { create: () => ({
    get: respond('get'), post: respond('post'), put: respond('put'), delete: respond('delete'),
    interceptors: { request: { use() {} }, response: { use(_ok, r) { reject = r } } },
  }) }
  const mod = evaluate(apiBuild.outputFiles[0].text)
  return { ...mod, calls, reject: e => reject(e) }
}

test('automationApi and templateApi call the documented paths and methods', async () => {
  const { automationApi, templateApi, calls, API_KEY_PERMISSIONS } = apiFixture()
  const u = 'a-1', e = 'en-1'
  await automationApi.list({ page: 2, pageSize: 10, status: 'archived' })
  await automationApi.get(u)
  await automationApi.create({ name: 'Welcome', workflow: { nodes: [], edges: [] } })
  await automationApi.update(u, { name: 'Renamed' })
  await automationApi.remove(u)
  await automationApi.validate(u)
  await automationApi.activate(u)
  await automationApi.activate(u, { publishDraft: false })
  await automationApi.pause(u)
  await automationApi.archive(u)
  await automationApi.stats(u)
  await automationApi.stats(u, 2)
  await automationApi.enroll(u, { listUuid: 'l-1' })
  await automationApi.enrollments(u, { status: 'waiting' })
  await automationApi.enrollment(u, e)
  await automationApi.cancelEnrollment(u, e)
  await automationApi.retryEnrollment(u, e)
  await templateApi.list()
  const base = '/api/v1/automations'
  assert.deepEqual(calls.map(c => `${c.method.toUpperCase()} ${c.url}`), [
    `GET ${base}?page=2&pageSize=10&status=archived`, `GET ${base}/${u}`, `POST ${base}`, `PUT ${base}/${u}`, `DELETE ${base}/${u}`,
    `POST ${base}/${u}/validate`, `POST ${base}/${u}/activate`, `POST ${base}/${u}/activate`, `POST ${base}/${u}/pause`, `POST ${base}/${u}/archive`,
    `GET ${base}/${u}/stats`, `GET ${base}/${u}/stats?version=2`, `POST ${base}/${u}/enroll`, `GET ${base}/${u}/enrollments?page=1&pageSize=50&status=waiting`,
    `GET ${base}/${u}/enrollments/${e}`, `POST ${base}/${u}/enrollments/${e}/cancel`, `POST ${base}/${u}/enrollments/${e}/retry`, 'GET /api/v1/templates',
  ])
  assert.deepEqual(calls[6].body, { publishDraft: true })
  assert.deepEqual(calls[7].body, { publishDraft: false })
  assert.deepEqual(calls[12].body, { listUuid: 'l-1' })
  const scopes = API_KEY_PERMISSIONS.map(p => p.value)
  assert.ok(scopes.includes('automations:read') && scopes.includes('automations:enroll'))
  assert.match(API_KEY_PERMISSIONS.find(p => p.value === 'automations:read').description, /contact emails/)
})

test('a 400 passes validation errors[] through to the caller', async () => {
  const { reject } = apiFixture()
  const errors = [{ nodeId: 'email-1', field: 'templateUuid', message: 'Choose a template' }]
  await assert.rejects(
    reject({ config: { url: '/api/v1/automations/a-1/activate' }, response: { status: 400, data: { code: 400, message: 'Automation is not valid', data: { errors } } } }),
    e => e.message === 'Automation is not valid' && e.status === 400 && e.data.errors[0].nodeId === 'email-1',
  )
})
