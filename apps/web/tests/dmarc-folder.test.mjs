import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'

const require = createRequire(import.meta.url)
const { createRenderer, h, nextTick, reactive } = require('vue')
const { setActivePinia, createPinia } = require('pinia')
const webRoot = fileURLToPath(new URL('..', import.meta.url))
const evaluate = code => { const module = { exports: {} }; new Function('require', 'module', 'exports', code)(require, module, module.exports); return module.exports }
const outputs = {}
for (const [name, file] of Object.entries({ sidebar: 'components/layout/Sidebar.vue', mailbox: 'views/ReceivedInbox.vue' })) {
  const source = await readFile(`${webRoot}/src/${file}`, 'utf8')
  const { descriptor } = parse(source, { filename: file })
  const compiled = compileScript(descriptor, { id: name, inlineTemplate: true })
  const icons = [...source.matchAll(/import\s*\{([^}]+)\}\s*from 'lucide-vue-next'/g)].flatMap(match => match[1].split(',').map(s => s.trim()))
  outputs[name] = (await build({
    stdin: { contents: compiled.content, resolveDir: webRoot, loader: 'ts' },
    bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
    plugins: [{ name: 'folder-ui-fixture', setup(builder) {
      builder.onResolve({ filter: /^@\/lib\/roles$/ }, () => ({ path: `${webRoot}/src/lib/roles.ts` }))
      builder.onResolve({ filter: /^(@\/|vue-router$|lucide-vue-next$)/ }, args => ({ path: args.path, namespace: 'fixture' }))
      builder.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ loader: 'js', contents:
        args.path === 'lucide-vue-next' ? icons.map(name => `export const ${name} = () => null`).join(';') :
        args.path === 'vue-router' ? 'export const useRoute = () => globalThis.__folderFixture.route; export const useRouter = () => globalThis.__folderFixture.router' :
        args.path.endsWith('AppLayout.vue') ? 'export default { setup(_, { slots }) { return () => slots.default?.() } }' :
        args.path.endsWith('/receivedInbox') ? 'export const useReceivedInboxStore = () => globalThis.__folderFixture.mailbox' :
        args.path.endsWith('/inbox') ? 'export const useInboxStore = () => globalThis.__folderFixture.composer' :
        args.path.endsWith('/domains') ? 'export const useDomainsStore = () => globalThis.__folderFixture.domains' :
        args.path.endsWith('/auth') ? "export const useAuthStore = () => globalThis.__folderFixture.auth ?? { user: { role: 'owner' } }" :
        args.path.endsWith('/compose') ? 'export const escapeHtml = value => value' :
        args.path.endsWith('/settings') ? 'export const useSettingsStore = () => globalThis.__folderFixture.settings' :
        args.path.endsWith('/mailHtml') ? 'export const renderMessageDocument = html => ({ doc: html, remoteCount: 0 })' :
        'export const api = globalThis.__folderFixture.api; export const trustedSendersApi = {}' }))
    } }],
  })).outputFiles[0].text
}
const settingsOutput = (await build({
  entryPoints: [`${webRoot}/src/stores/settings.ts`], bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
  plugins: [{ name: 'settings-fixture', setup(builder) {
    builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ contents: 'export const api = globalThis.__folderSettingsApi; export const inboxFiltersApi = {}; export const trustedSendersApi = {}', loader: 'js' }))
  } }],
})).outputFiles[0].text

const node = (type, text = '') => ({ type, text, props: {}, children: [], parent: null })
const renderer = createRenderer({
  createElement: type => node(type), createText: text => node('text', text), createComment: text => node('comment', text),
  setText: (item, text) => { item.text = text }, setElementText: (item, text) => { item.text = text; item.children = [] },
  patchProp: (item, key, _old, value) => { item.props[key] = value },
  insert(item, parent, anchor) {
    if (item.parent) item.parent.children.splice(item.parent.children.indexOf(item), 1)
    item.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    if (index < 0) parent.children.push(item); else parent.children.splice(index, 0, item)
  },
  remove(item) { if (item.parent) item.parent.children.splice(item.parent.children.indexOf(item), 1); item.parent = null },
  parentNode: item => item.parent, nextSibling: item => item.parent?.children[item.parent.children.indexOf(item) + 1] || null,
})
const all = item => [item, ...item.children.flatMap(all)]
const text = item => all(item).filter(n => n.type !== 'comment').map(n => n.text).join(' ').replace(/\s+/g, ' ').trim()
const flush = async () => { for (let n = 0; n < 5; n++) { await Promise.resolve(); await nextTick() } }
function uiFixture(t, view, folder = 'dmarc-reports') {
  const calls = [], moves = [], navigation = []
  const report = { id: 1, uuid: 'report', identityId: 1, folder: 'dmarc-reports', subject: 'Aggregate report', fromEmail: 'reports@example.test', toEmails: ['owner@example.test'], receivedAt: '2026-10-04T08:00:00Z', createdAt: '2026-10-04T08:00:00Z', isRead: true, hasAttachments: true, attachments: [{ uuid: 'attachment', filename: 'report.xml.gz', sizeBytes: 200, downloadUrl: '/attachment' }] }
  const mailbox = reactive({
    emails: [], currentEmail: null, selectedEmailUuids: [], counts: { inboxUnread: 2, dmarcReports: 6, dmarcReportsUnread: 4, unread: 9 }, notice: '', error: null,
    isLoading: false, isMutating: false, detailLoading: false, page: 1, pageSize: 50, total: 0, totalPages: 0,
    fetchEmails: async (id, options) => { calls.push([id, options]) }, fetchCounts: async () => {},
    closeEmail() { mailbox.currentEmail = null }, clearSelection() { mailbox.selectedEmailUuids = [] },
    connectSSE() {}, disconnectSSE() {},
    async fetchEmail() { mailbox.currentEmail = report; return report },
    async markAsRead() {}, async starEmails() {},
    async moveEmails(ids, destination) { moves.push([ids, destination]); mailbox.emails = [] },
  })
  const route = reactive({ path: '/received', fullPath: `/received?folder=${folder}`, params: {}, query: { folder } })
  globalThis.__folderFixture = { mailbox, route, router: { push: value => navigation.push(value), replace: value => navigation.push(value) }, composer: {}, settings: {}, domains: { identities: [], domains: [], fetchIdentities: async () => {}, fetchDomains: async () => {} }, api: {} }
  const root = node('root'); const app = renderer.createApp({ setup: () => () => h(evaluate(outputs[view]).default) }); app.mount(root); t.after(() => app.unmount())
  return { root, mailbox, report, route, calls, moves, navigation, button: label => all(root).find(n => n.type === 'button' && (n.props['aria-label'] === label || n.props.title === label || text(n) === label)) }
}

test('sidebar separates Inbox/report badges and keeps the global All Mail unread count', async t => {
  const f = uiFixture(t, 'sidebar')
  assert.ok(f.button('Inbox 2')); assert.ok(f.button('DMARC Reports 4')); assert.ok(f.button('All Mail 9'))
  f.button('DMARC Reports 4').props.onClick()
  assert.equal(f.navigation[0], '/received?folder=dmarc-reports')
  assert.equal(f.button('DMARC Reports 4').props['aria-current'], 'page')
  f.mailbox.counts.dmarcReportsUnread = 5; f.mailbox.counts.unread = 10; await flush()
  assert.ok(f.button('Inbox 2')); assert.ok(f.button('DMARC Reports 5')); assert.ok(f.button('All Mail 10'))
})
test('report folder has a useful empty state and bulk restore plus archive controls', async t => {
  const f = uiFixture(t, 'mailbox'); await flush()
  assert.match(text(f.root), /DMARC Reports/)
  assert.match(text(f.root), /existing DMARC policy requests reports and Mailat receives them/)
  assert.equal(f.calls[0][1].folder, 'dmarc-reports')
  f.mailbox.emails = [f.report]; f.mailbox.selectedEmailUuids = ['report']; await flush()
  assert.ok(f.button('Archive')); assert.ok(f.button('Move to Inbox'))
  await f.button('Move to Inbox').props.onClick(); await flush()
  assert.deepEqual(f.moves, [[['report'], 'inbox']])
  assert.match(text(f.root), /Messages restored to Inbox/)
})
test('Inbox offers an explicit bulk move to reports and attachment-only detail stays readable', async t => {
  const f = uiFixture(t, 'mailbox', 'inbox'); await flush()
  f.mailbox.emails = [f.report]; f.mailbox.selectedEmailUuids = ['report']; await flush()
  await f.button('Move to DMARC Reports').props.onClick(); await flush()
  assert.deepEqual(f.moves, [[['report'], 'dmarc-reports']])
  f.mailbox.emails = [f.report]; await flush()
  const open = all(f.root).find(n => n.type === 'button' && text(n).includes('Aggregate report'))
  await open.props.onClick(); await flush()
  assert.match(text(f.root), /Its content is in the attachment below/)
  assert.match(text(f.root), /report.xml.gz/)
  assert.doesNotMatch(text(f.root), /No message content available/)
})

function settingsFixture(t, options = {}) {
  const storage = new Map(), requests = [], state = { token: 'owner', failGet: false, failSave: false, ...options }
  const original = { localStorage: globalThis.localStorage, document: globalThis.document }
  globalThis.localStorage = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value) }
  globalThis.document = { documentElement: { classList: { add() {}, remove() {} } } }
  t.after(() => { globalThis.localStorage = original.localStorage; globalThis.document = original.document })
  t.mock.timers.enable({ apis: ['setTimeout'] })
  globalThis.__folderSettingsApi = { getToken: () => state.token, get: async () => { if (state.failGet) throw Error('unavailable'); return state.response ?? {} }, put: async (path, body) => { requests.push([path, { ...body }]); if (state.saveWait) await state.saveWait; if (state.failSave) throw Error('unavailable') } }
  setActivePinia(createPinia())
  const store = evaluate(settingsOutput).useSettingsStore()
  return { store, state, requests, storage }
}
test('DMARC setting defaults on, loads explicit false, and persists false with an optional-field PUT', async t => {
  const f = settingsFixture(t)
  assert.equal(f.store.settings.autoOrganizeDmarcReports, true)
  await f.store.fetchSettings(); assert.equal(f.store.settings.autoOrganizeDmarcReports, true)
  f.store.settings.autoOrganizeDmarcReports = false
  assert.equal(await f.store.saveDmarcOrganization(), true)
  assert.deepEqual(f.requests, [['/api/v1/settings', { autoOrganizeDmarcReports: false }]])
  f.state.response = { autoOrganizeDmarcReports: false }; await f.store.fetchSettings()
  assert.equal(f.store.settings.autoOrganizeDmarcReports, false)
})
test('DMARC failed load/save never claims a local-only success and permits retry', async t => {
  const f = settingsFixture(t, { failGet: true })
  await f.store.fetchSettings()
  assert.equal(f.store.settingsLoaded, false); assert.equal(await f.store.saveDmarcOrganization(), false); assert.equal(f.requests.length, 0)
  f.state.failGet = false; await f.store.fetchSettings()
  f.store.settings.autoOrganizeDmarcReports = false; f.state.failSave = true
  assert.equal(await f.store.saveDmarcOrganization(), false)
  assert.equal(f.store.saveSuccess, false); assert.match(f.store.error, /not saved/)
  assert.equal(JSON.parse(f.storage.get('userSettings')).autoOrganizeDmarcReports, true)
  f.state.failSave = false; assert.equal(await f.store.saveDmarcOrganization(), true)
  assert.equal(JSON.parse(f.storage.get('userSettings')).autoOrganizeDmarcReports, false)
})
test('settings loaded for a previous account cannot save the next account preference', async t => {
  const f = settingsFixture(t); await f.store.fetchSettings(); f.state.token = 'another-owner'
  assert.equal(await f.store.saveDmarcOrganization(), false); assert.equal(f.requests.length, 0)
})


test('general settings saves omit an unsaved DMARC preference', async t => {
  const f = settingsFixture(t); await f.store.fetchSettings()
  f.store.settings.autoOrganizeDmarcReports = false
  f.store.settings.density = 'compact'
  assert.equal(await f.store.saveSettings(), true)
  assert.equal(f.requests.length, 1)
  assert.equal(f.requests[0][1].density, 'compact')
  assert.equal(Object.hasOwn(f.requests[0][1], 'autoOrganizeDmarcReports'), false)
})
test('general settings saves require current-account settings and reject failed fetch defaults', async t => {
  const f = settingsFixture(t, { failGet: true })
  assert.equal(await f.store.saveSettings(), false)
  await f.store.fetchSettings()
  assert.equal(await f.store.saveSettings(), false)
  assert.equal(f.requests.length, 0)
  f.state.failGet = false; await f.store.fetchSettings(); f.state.token = 'another-owner'
  assert.equal(await f.store.saveSettings(), false)
  assert.equal(f.requests.length, 0)
  assert.equal(f.store.saveSuccess, false)
})
test('a late general settings save cannot report success or cache data after an account switch', async t => {
  const f = settingsFixture(t); await f.store.fetchSettings()
  let resolve
  f.state.saveWait = new Promise(done => { resolve = done })
  f.store.settings.density = 'compact'
  const saving = f.store.saveSettings()
  assert.equal(f.requests.length, 1)
  f.state.token = 'another-owner'
  const stored = f.storage.get('userSettings')
  resolve(); assert.equal(await saving, false)
  assert.equal(f.store.saveSuccess, false)
  assert.equal(f.storage.get('userSettings'), stored)
})
