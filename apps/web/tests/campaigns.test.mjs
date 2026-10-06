import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'

const require = createRequire(import.meta.url)
const { createRenderer, h, nextTick, reactive, defineComponent } = require('vue')
const { createPinia } = require('pinia')
const webRoot = fileURLToPath(new URL('..', import.meta.url))
const srcRoot = path.join(webRoot, 'src')

// Components that need a browser or heavy editors are replaced; everything else
// (wizard steps, buttons, the campaigns store) is compiled from source.
const STUBS = {
  'AppLayout.vue': `import { h } from 'vue'; export default { setup: (_p, { slots }) => () => h('main', slots.default?.()) }`,
  'WizardStepContent.vue': `import { onMounted } from 'vue'
    export default { props: ['htmlContent', 'textContent'], emits: ['update:htmlContent', 'update:textContent', 'update:valid'],
      setup(props, { emit }) { onMounted(() => { if (!props.htmlContent) emit('update:htmlContent', '<p>Hello {{firstName}}</p>'); emit('update:valid', true) }); return () => null } }`,
  'WizardStepSchedule.vue': `import { onMounted } from 'vue'
    export default { props: ['sendOption', 'scheduledAt'], emits: ['update:sendOption', 'update:scheduledAt', 'update:valid'],
      setup(_p, { emit }) { onMounted(() => emit('update:valid', true)); return () => null } }`,
  'CampaignWizard.vue': `export default { props: ['campaign'], setup(props) { globalThis.__campaignFixture.wizardCampaign = props.campaign; return () => null } }`,
}
const FIXTURES = {
  api: 'const f = globalThis.__campaignFixture; export const campaignApi = f.campaignApi; export const listApi = f.listApi',
  'stores/domains': 'export const useDomainsStore = () => globalThis.__campaignFixture.domains',
  'stores/auth': 'export const useAuthStore = () => globalThis.__campaignFixture.auth',
  'vue-router': 'export const useRoute = () => globalThis.__campaignFixture.route; export const useRouter = () => globalThis.__campaignFixture.router',
  'vue-chartjs': 'export const Bar = () => null; export const Doughnut = Bar',
  'chart.js': 'export const Chart = { register() {} }; export const CategoryScale = {}, LinearScale = {}, PointElement = {}, LineElement = {}, BarElement = {}, ArcElement = {}, Title = {}, Tooltip = {}, Legend = {}, Filler = {}',
  dompurify: 'export default { sanitize: html => html }',
}

async function bundle(entry, stubbed) {
  const output = await build({
    entryPoints: [path.join(srcRoot, entry)], bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
    plugins: [{ name: 'campaign-sfc', setup(builder) {
      builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
      builder.onResolve({ filter: /^@\/stores\/(domains|auth)$/ }, args => ({ path: args.path.slice(2), namespace: 'fixture' }))
      builder.onResolve({ filter: /^(vue-router|vue-chartjs|chart\.js|dompurify)$/ }, args => ({ path: args.path, namespace: 'fixture' }))
      builder.onResolve({ filter: /\.vue$/ }, args => {
        const file = args.path.startsWith('@/') ? path.join(srcRoot, args.path.slice(2)) : path.resolve(args.resolveDir, args.path)
        const name = path.basename(file)
        return stubbed.includes(name) ? { path: name, namespace: 'stub' } : { path: file, namespace: 'sfc' }
      })
      builder.onResolve({ filter: /^@\// }, args => {
        const base = path.join(srcRoot, args.path.slice(2))
        return { path: [`${base}.ts`, `${base}/index.ts`, base].find(existsSync) }
      })
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

const [listCode, detailCode, wizardCode] = await Promise.all([
  bundle('views/Campaigns.vue', ['AppLayout.vue', 'CampaignWizard.vue']),
  bundle('views/CampaignDetail.vue', ['AppLayout.vue', 'CampaignWizard.vue']),
  bundle('components/campaigns/CampaignWizard.vue', ['WizardStepContent.vue', 'WizardStepSchedule.vue']),
])

// Vue's real renderer exercises bindings, events and async updates without a DOM.
// v-model directives touch listeners and select options, so nodes carry inert versions.
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
const flush = async (rounds = 6) => { for (let i = 0; i < rounds; i++) { await Promise.resolve(); await nextTick() } }
const byTest = (root, id) => all(root).find(n => n.props['data-test'] === id)
const button = (root, label) => all(root).find(n => n.type === 'button' && (n.props['aria-label'] === label || text(n).trim() === label))
const click = async (root, label) => {
  const target = button(root, label)
  assert.ok(target, `button "${label}" not found in: ${text(root).slice(0, 400)}`)
  target.props.onClick?.({})
  await flush()
}

globalThis.document = { hidden: false, addEventListener() {}, removeEventListener() {} }

const campaign = (overrides = {}) => ({
  id: 7, uuid: 'camp-1', name: 'October News', subject: 'Hello there', htmlContent: '<p>Hi {{firstName}}</p>', textContent: 'Hi',
  fromName: 'News', fromEmail: 'News@Example.test', replyTo: '', listId: 3, listName: 'Customers', listType: 'static',
  status: 'draft', statusReason: null, preparedAt: null, throttledUntil: null, totalRecipients: 0, sentCount: 0,
  deliveredCount: 0, openCount: 0, clickCount: 0, bounceCount: 0, unsubscribeCount: 0, complaintCount: 0,
  failedCount: 0, skippedCount: 0, unknownCount: 0, trackOpens: true, trackClicks: true, createdAt: '2026-10-01T10:00:00Z',
  ...overrides,
})
const statsFor = c => ({ campaign: c, openRate: c.sentCount ? (c.openCount / c.sentCount) * 100 : 0, clickRate: 0, clickToOpenRate: 0,
  bounceRate: 0, unsubscribeRate: 0, complaintRate: 0, deliveredRate: 0, clicksByLink: [], opensByHour: [] })

function mount(t, code, props = {}, fixture = {}) {
  const calls = []
  const record = (name, result) => (...args) => { calls.push({ name, args }); return typeof result === 'function' ? result(...args) : Promise.resolve(result) }
  globalThis.__campaignFixture = {
    route: { params: { uuid: 'camp-1' } }, router: { push: record('push') },
    listApi: { list: record('lists', [{ id: '3', uuid: 'list-3', name: 'Customers', type: 'static', contactCount: 12 }]) },
    domains: reactive({
      identities: [
        { id: 11, uuid: 'i-11', email: 'news@example.test', displayName: '', domainId: 5, isDefault: true, canSend: true },
        { id: 12, uuid: 'i-12', email: 'other@unverified.test', displayName: 'Other', domainId: 6, isDefault: false, canSend: true },
      ],
      domains: [{ id: 5, status: 'active', sesVerified: true }, { id: 6, status: 'active', sesVerified: false }],
      fetchIdentities: record('fetchIdentities'), fetchDomains: record('fetchDomains'),
    }),
    auth: { user: { role: 'member' } },
    ...fixture,
    campaignApi: { getSettings: record('getSettings', { postalAddress: '1 Main St' }), ...fixture.campaignApi },
  }
  const module = { exports: {} }
  new Function('require', 'module', 'exports', code)(require, module, module.exports)
  const root = node('root')
  const app = renderer.createApp({ setup: () => () => h(module.exports.default, props) })
  app.use(createPinia())
  app.mount(root)
  t.after(() => app.unmount())
  return { root, calls, app, named: name => calls.filter(c => c.name === name) }
}

test('campaign list shows flat sentCount and rates, reasons and progress', async t => {
  const rows = [
    campaign({ uuid: 'a', name: 'Sent one', status: 'sent', sentCount: 200, totalRecipients: 200, openCount: 50, clickCount: 10, preparedAt: '2026-10-01T10:00:00Z' }),
    campaign({ uuid: 'b', name: 'Sending one', status: 'sending', sentCount: 30, failedCount: 5, unknownCount: 5, skippedCount: 10, totalRecipients: 100, preparedAt: '2026-10-01T10:00:00Z' }),
    campaign({ uuid: 'c', name: 'Paused one', status: 'paused', statusReason: 'bounce_rate_high', trackOpens: false }),
  ]
  const f = mount(t, listCode, {}, { campaignApi: { list: () => Promise.resolve({ campaigns: rows, total: 3 }) } })
  await flush()
  const page = text(f.root)
  assert.match(page, /200/)
  assert.match(page, /25\.0%/) // 50 unique opens / 200 sent
  assert.match(page, /5\.0%/) // 10 unique clicks / 200 sent
  assert.match(page, /Bounce rate too high/)
  assert.match(text(byTest(f.root, 'list-progress')), /50%/) // (30+5+5+10)/100
  assert.ok(all(f.root).some(n => n.props['data-test'] === 'open-rate' && text(n).trim() === '-'), 'untracked opens show no rate')
})

test('duplicating a campaign resets counters and sending state but keeps tracking toggles', async t => {
  const original = campaign({ status: 'sent', statusReason: 'no_eligible_recipients', sentCount: 90, openCount: 40, totalRecipients: 100,
    preparedAt: '2026-10-01T10:00:00Z', throttledUntil: '2026-10-01T11:00:00Z', startedAt: '2026-10-01T10:00:00Z', trackClicks: false })
  const f = mount(t, listCode, {}, { campaignApi: { list: () => Promise.resolve({ campaigns: [original], total: 1 }) } })
  await flush()
  await click(f.root, 'Actions for October News')
  await click(f.root, 'Duplicate')
  const copy = globalThis.__campaignFixture.wizardCampaign
  assert.equal(copy.uuid, '')
  assert.equal(copy.status, 'draft')
  for (const key of ['sentCount', 'openCount', 'totalRecipients', 'failedCount', 'unknownCount', 'skippedCount']) assert.equal(copy[key], 0, key)
  assert.equal(copy.statusReason, null); assert.equal(copy.preparedAt, null); assert.equal(copy.throttledUntil, null)
  assert.equal(copy.trackOpens, true); assert.equal(copy.trackClicks, false)
  assert.equal(copy.htmlContent, original.htmlContent)
})

test('campaign detail polls progress, never opens a token stream, and refreshes on completion', async t => {
  t.mock.timers.enable({ apis: ['setInterval'] })
  const sending = campaign({ status: 'sending', preparedAt: null, totalRecipients: 0 })
  let current = sending
  let progress = { status: 'sending', statusReason: null, preparing: true, total: 0, pending: 0, inFlight: 0, sent: 0, failed: 0, unknown: 0, skipped: 0, cancelled: 0, throttledUntil: null, percent: 0 }
  const urls = []
  const f = mount(t, detailCode, {}, { campaignApi: {
    get: () => { urls.push('get'); return Promise.resolve(current) },
    getStats: () => Promise.resolve(statsFor(current)),
    preview: () => Promise.resolve({ subject: 'Hello there', html: '<p>Hi Ada</p>', text: 'Hi Ada', unknownVariables: [] }),
    recipients: () => Promise.resolve({ recipients: [], total: 0 }),
    progress: uuid => { urls.push(`progress:${uuid}`); return Promise.resolve(progress) },
  } })
  await flush()
  assert.equal(globalThis.EventSource, undefined)
  assert.match(text(f.root), /Preparing audience/)
  assert.match(text(byTest(f.root, 'preview-html')), /^\s*$/) // rendered via v-html, not text
  assert.equal(byTest(f.root, 'preview-html').props.innerHTML, '<p>Hi Ada</p>')

  progress = { ...progress, preparing: false, total: 4, sent: 1, pending: 3, percent: 25, throttledUntil: '2026-10-06T12:00:00Z' }
  current = campaign({ status: 'sending', preparedAt: '2026-10-06T10:00:00Z', totalRecipients: 4, sentCount: 1 })
  t.mock.timers.tick(5000); await flush()
  assert.ok(urls.includes('progress:camp-1'))
  assert.match(text(f.root), /Waiting for SES quota until/)
  assert.match(text(byTest(f.root, 'send-progress')), /25%/)

  progress = { ...progress, status: 'sent', pending: 0, sent: 4, percent: 100, throttledUntil: null }
  current = campaign({ status: 'sent', preparedAt: '2026-10-06T10:00:00Z', totalRecipients: 4, sentCount: 4 })
  const getsBefore = urls.filter(u => u === 'get').length
  t.mock.timers.tick(5000); await flush()
  assert.ok(urls.filter(u => u === 'get').length > getsBefore, 'terminal status refreshes the campaign')
  assert.equal(text(byTest(f.root, 'summary-sent')).trim(), '4')
  const polls = urls.filter(u => u.startsWith('progress')).length
  t.mock.timers.tick(15000); await flush()
  assert.equal(urls.filter(u => u.startsWith('progress')).length, polls, 'polling stops on a terminal status')
  assert.ok(urls.every(u => !u.includes('token=')))
})

test('polling stops on unmount', async t => {
  t.mock.timers.enable({ apis: ['setInterval'] })
  let polls = 0
  const c = campaign({ status: 'sending', preparedAt: '2026-10-06T10:00:00Z', totalRecipients: 2 })
  const f = mount(t, detailCode, {}, { campaignApi: {
    get: () => Promise.resolve(c), getStats: () => Promise.resolve(statsFor(c)),
    preview: () => Promise.resolve({ subject: '', html: '', text: '', unknownVariables: [] }),
    recipients: () => Promise.resolve({ recipients: [], total: 0 }),
    progress: () => { polls++; return Promise.resolve({ status: 'sending', preparing: false, total: 2, sent: 0, failed: 0, unknown: 0, skipped: 0, percent: 0 }) },
  } })
  await flush()
  t.mock.timers.tick(5000); await flush()
  assert.equal(polls, 1)
  f.app.unmount()
  t.mock.timers.tick(20000); await flush()
  assert.equal(polls, 1)
})

test('paused detail shows the reason banner, resume and a confirmed cancel; untracked opens hide the rate', async t => {
  let c = campaign({ status: 'paused', statusReason: 'monthly_quota_exceeded', preparedAt: '2026-10-06T10:00:00Z', totalRecipients: 3, sentCount: 1, unknownCount: 1, trackOpens: false })
  const f = mount(t, detailCode, {}, { campaignApi: {
    get: () => Promise.resolve(c), getStats: () => Promise.resolve(statsFor(c)),
    preview: () => Promise.resolve({ subject: '', html: '', text: 'Hi', unknownVariables: ['plan'] }),
    recipients: () => Promise.resolve({ recipients: [
      { email: 'a@example.test', status: 'sent', skipReason: null, deliveryStatus: 'delivered', sentAt: '2026-10-06T10:01:00Z', openCount: 0, clickCount: 1, unsubscribedAt: null, error: null },
      { email: 'b@example.test', status: 'unknown', skipReason: null, deliveryStatus: null, sentAt: null, openCount: 0, clickCount: 0, unsubscribedAt: null, error: null },
    ], total: 2 }),
    progress: () => Promise.reject(new Error('not polled')),
    resume: () => { c = { ...c, status: 'sending', statusReason: null }; return Promise.resolve(c) },
    cancel: () => { c = { ...c, status: 'cancelled', statusReason: null }; return Promise.resolve(c) },
  } })
  await flush()
  const page = text(f.root)
  assert.match(page, /Monthly send quota reached/)
  assert.match(page, /Opens not tracked/)
  assert.equal(byTest(f.root, 'open-rate-card'), undefined)
  assert.ok(byTest(f.root, 'click-rate-card'))
  assert.match(text(byTest(f.root, 'recipients-table')), /Outcome uncertain — not retried/)
  assert.match(page, /Unknown variables render empty: plan/)

  await click(f.root, 'Cancel')
  assert.match(text(f.root), /Cancel this campaign\?/)
  await click(f.root, 'Cancel Campaign')
  await flush()
  assert.match(text(f.root), /Cancelled/)
})

function wizardFixture(overrides = {}) {
  const store = { created: 0, saved: null }
  const api = {
    create: data => { store.created++; store.saved = { ...campaign(), ...data, uuid: 'new-uuid', warnings: [] }; return Promise.resolve(store.saved) },
    update: (uuid, data) => { store.saved = { ...campaign({ uuid }), ...data }; return Promise.resolve(store.saved) },
    audience: () => Promise.resolve({ listType: 'static', eligible: 10, excludedInactive: 1, excludedSuppressed: 1, warnings: ['dmarc_missing'] }),
    getSettings: () => Promise.resolve({ postalAddress: '' }),
    ...overrides,
  }
  return { store, api }
}

async function fillNewCampaign(root) {
  const input = placeholder => all(root).find(n => n.type === 'input' && n.props.placeholder === placeholder)
  input('e.g., January Newsletter, Product Launch').props.onInput({ target: { value: 'Launch' } })
  input('e.g., Your weekly update is here!').props.onInput({ target: { value: 'Big launch today' } })
  await flush()
  await click(root, 'Next')
  all(root).find(n => n.type === 'div' && n.props.onClick && text(n).includes('Customers')).props.onClick()
  await flush()
  await click(root, 'Next')
  await click(root, 'Next')
}

test('wizard edit maps the flat campaign: html, list and sender identity by email', async t => {
  const { api } = wizardFixture()
  const original = campaign({ status: 'draft', trackClicks: false, replyTo: 'reply@example.test' })
  const calls = []
  api.update = (uuid, data) => { calls.push({ uuid, data }); return Promise.resolve({ ...original, ...data }) }
  api.create = () => { throw new Error('edit must not create') }
  const f = mount(t, wizardCode, { campaign: original }, { campaignApi: api, auth: { user: { role: 'owner' } } })
  await flush()
  const select = all(f.root).find(n => n.type === 'select')
  assert.equal(select.props.value, 11, 'News@Example.test matches the identity case-insensitively')
  assert.equal(all(select).filter(n => n.type === 'option' && n.props.value === 12).length, 0, 'identities on non-SES domains are not offered')
  assert.equal(all(f.root).find(n => n.props['aria-label'] === 'Track clicks').props.checked, false)
  await click(f.root, 'Next'); await click(f.root, 'Next'); await click(f.root, 'Next')
  assert.match(text(f.root), /unsubscribe link, your postal address and one-click unsubscribe headers are always added/)
  assert.ok(byTest(f.root, 'postal-address'), 'missing postal address prompts the admin')
  assert.ok(all(f.root).find(n => n.type === 'textarea' && n.props['aria-label'] === 'Organization postal address'))
  await click(f.root, 'Save Draft')
  assert.equal(calls.length, 1)
  assert.equal(calls[0].uuid, 'camp-1')
  assert.equal(calls[0].data.listId, 3)
  assert.equal(calls[0].data.htmlContent, '<p>Hi {{firstName}}</p>')
  assert.equal(calls[0].data.fromEmail, 'news@example.test')
  assert.equal(calls[0].data.fromName, 'News')
  assert.equal(calls[0].data.trackClicks, false)
  assert.equal(calls[0].data.replyTo, 'reply@example.test')
})

test('wizard toggles default on, test send uses the campaign endpoint with an Idempotency-Key, and members are asked to contact an admin', async t => {
  const { store, api } = wizardFixture()
  const tests = []
  api.sendTest = (uuid, emails, key) => { tests.push({ uuid, emails, key }); return Promise.resolve({ status: 'sent', results: [{ email: emails[0], status: 'sent' }] }) }
  const f = mount(t, wizardCode, {}, { campaignApi: api })
  await flush()
  assert.equal(all(f.root).find(n => n.props['aria-label'] === 'Track opens').props.checked, true)
  assert.equal(all(f.root).find(n => n.props['aria-label'] === 'Track clicks').props.checked, true)
  await fillNewCampaign(f.root)
  assert.match(text(byTest(f.root, 'postal-address')), /Ask an admin to set the postal address/)

  const testInput = all(f.root).find(n => n.type === 'input' && n.props.placeholder === 'your@email.com')
  testInput.props['onUpdate:modelValue']('me@example.test')
  await flush()
  await click(f.root, 'Send Test')
  await flush()
  assert.equal(store.created, 1)
  assert.equal(store.saved.trackOpens, true)
  assert.equal(store.saved.trackClicks, true)
  assert.equal(store.saved.fromName, 'news', 'empty display name falls back to the local part')
  assert.equal(tests.length, 1)
  assert.equal(tests[0].uuid, 'new-uuid')
  assert.deepEqual(tests[0].emails, ['me@example.test'])
  assert.match(tests[0].key, /^[0-9a-f-]{36}$/)
  assert.match(text(f.root), /Test email sent/)
  assert.match(text(byTest(f.root, 'audience')), /10 eligible/)
  assert.match(text(byTest(f.root, 'audience')), /no verified DMARC record/)
})

test('after a failed send, Send now updates and sends the saved campaign instead of creating another', async t => {
  const { store, api } = wizardFixture()
  const sends = [], updates = []
  let failNext = true
  api.update = (uuid, data) => { updates.push(uuid); return Promise.resolve({ ...store.saved, ...data }) }
  api.send = uuid => {
    sends.push(uuid)
    if (failNext) { failNext = false; return Promise.reject(Object.assign(new Error('Set your organization postal address before sending campaigns'), { status: 400 })) }
    return Promise.resolve({ ...store.saved, status: 'sending' })
  }
  const f = mount(t, wizardCode, {}, { campaignApi: api })
  await flush()
  await fillNewCampaign(f.root)
  await click(f.root, 'Next')
  await click(f.root, 'Send Now')
  await flush()
  assert.match(text(f.root), /Set your organization postal address before sending campaigns/, '400 messages are shown verbatim')
  await click(f.root, 'Send Now')
  await flush()
  assert.equal(store.created, 1, 'create runs once')
  assert.deepEqual(updates, ['new-uuid'])
  assert.deepEqual(sends, ['new-uuid', 'new-uuid'])
})

test('every statusReason the API documents has a readable label', async () => {
  const spec = JSON.parse(await readFile(path.join(webRoot, '../api/internal/apidocs/openapi.json'), 'utf8'))
  const description = spec.components.schemas['model.Campaign'].properties.statusReason.description
  const reasons = description.split(':')[1].match(/[a-z]+(?:_[a-z]+)+/g)
  assert.ok(reasons.length >= 15, description)
  const module = { exports: {} }
  new Function('require', 'module', 'exports', await bundle('lib/campaignStatus.ts', []))(require, module, module.exports)
  const { STATUS_REASON_LABELS, statusReasonLabel } = module.exports
  for (const reason of reasons) {
    assert.ok(STATUS_REASON_LABELS[reason], `no label for ${reason}`)
    assert.equal(statusReasonLabel(reason), STATUS_REASON_LABELS[reason])
  }
})
