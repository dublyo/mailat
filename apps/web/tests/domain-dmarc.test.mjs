import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'

const require = createRequire(import.meta.url)
const { createRenderer, h, nextTick, ref } = require('vue')
const webRoot = fileURLToPath(new URL('..', import.meta.url))
const source = await readFile(`${webRoot}/src/components/settings/DomainDmarcStatus.vue`, 'utf8')
const { descriptor } = parse(source, { filename: 'DomainDmarcStatus.vue' })
const compiled = compileScript(descriptor, { id: 'dmarc-status-test', inlineTemplate: true })
const output = await build({
  stdin: { contents: compiled.content, resolveDir: webRoot, loader: 'ts' },
  bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
  plugins: [{ name: 'dmarc-api-fixture', setup(builder) {
    builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
    builder.onResolve({ filter: /^lucide-vue-next$/ }, () => ({ path: 'icons', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ contents: args.path === 'api'
      ? 'export const { api, domainApi } = globalThis.__dmarcFixture'
      : 'export const Check = () => null; export const Copy = Check, RefreshCw = Check, Shield = Check', loader: 'js' }))
  } }],
})

// Vue's real renderer exercises bindings, events and async updates without a DOM dependency.
const node = (type, text = '') => ({ type, text, props: {}, children: [], parent: null })
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
const text = item => all(item).filter(n => n.type !== 'comment').map(n => n.text).join(' ')
const status = (overrides = {}) => ({ status: 'absent', hostname: '_dmarc.example.test', policyHostname: '', value: '', policy: '', reason: 'No policy applies.', checkedAt: '2026-10-04T08:00:00Z', canCreate: true, verified: false, suggestedValue: 'v=DMARC1; p=quarantine;', ...overrides })
const flush = async () => { await Promise.resolve(); await nextTick() }

function fixture(t) {
  const calls = [], copied = []
  const state = { token: 'signed-in' }
  globalThis.__dmarcFixture = {
    api: { getToken: () => state.token },
    domainApi: { inspectDMARC: (uuid, signal) => new Promise((resolve, reject) => calls.push({ uuid, signal, resolve, reject })) },
  }
  const previousNavigator = Object.getOwnPropertyDescriptor(globalThis, 'navigator')
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: { clipboard: { writeText: async value => copied.push(value) } } })
  const module = { exports: {} }
  new Function('require', 'module', 'exports', output.outputFiles[0].text)(require, module, module.exports)
  const props = ref({ domainUuid: 'first-domain', domainName: 'example.test' })
  const root = node('root')
  const app = renderer.createApp({ setup: () => () => h(module.exports.default, props.value) })
  app.mount(root)
  t.after(() => {
    app.unmount()
    if (previousNavigator) Object.defineProperty(globalThis, 'navigator', previousNavigator)
    else delete globalThis.navigator
  })
  return {
    root, calls, copied, state, props,
    button: name => all(root).find(n => n.type === 'button' && n.props['aria-label'] === name),
    copyButtons: () => all(root).filter(n => n.type === 'button' && n.props['aria-label']?.startsWith('Copy DMARC')),
    refresh: () => all(root).find(n => n.type === 'button' && n.props['aria-label']?.startsWith('Refresh DMARC')).props.onClick(),
  }
}

test('loading hides suggestions; successful absence enables accessible hostname and value copying', async t => {
  const f = fixture(t)
  assert.match(text(f.root), /Checking DMARC/)
  assert.equal(f.copyButtons().length, 0)
  assert.equal(all(f.root).find(n => n.type === 'section').props['aria-busy'], true)
  f.calls[0].resolve(status()); await flush()
  assert.match(text(f.root), /Ready to add/)
  assert.match(text(f.root), /Quarantine applies to all senders/)
  assert.equal(f.copyButtons().length, 2)
  await f.button('Copy DMARC hostname').props.onClick()
  await f.button('Copy DMARC value').props.onClick(); await flush()
  assert.deepEqual(f.copied, ['_dmarc.example.test', 'v=DMARC1; p=quarantine;'])
  assert.match(text(f.root), /Value copied/)
})

test('refresh removes a stale default immediately and preserves the newly discovered policy verbatim', async t => {
  const f = fixture(t)
  f.calls[0].resolve(status()); await flush()
  const refresh = f.refresh(); await flush()
  assert.equal(f.copyButtons().length, 0)
  assert.doesNotMatch(text(f.root), /v=DMARC1; p=quarantine;/)
  const value = 'v=DMARC1; p=none; rua=mailto:reports@provider.test; adkim=s; x-vendor=original'
  f.calls[1].resolve(status({ status: 'existing', canCreate: false, verified: true, value, policy: 'none', policyHostname: '_dmarc.example.test' }))
  await refresh; await flush()
  assert.match(text(f.root), /Existing policy preserved/)
  assert.ok(text(f.root).includes(value))
  assert.match(text(f.root), /Effective policy: none/)
  assert.equal(f.copyButtons().length, 0)
})

test('inherited policy names its source and never suggests a second policy', async t => {
  const f = fixture(t)
  f.calls[0].resolve(status({ status: 'inherited', policyHostname: '_dmarc.parent.test', value: 'v=DMARC1; p=reject; sp=quarantine;', policy: 'quarantine', canCreate: false, verified: true }))
  await flush()
  assert.match(text(f.root), /Inherited policy preserved/)
  assert.match(text(f.root), /_dmarc.parent.test/)
  assert.match(text(f.root), /Effective policy: quarantine/)
  assert.equal(f.copyButtons().length, 0)
})

test('Cloudflare-confirmed policy blocks duplicate suggestions while public DNS still reports absence', async t => {
  const f = fixture(t)
  f.props.value = { ...f.props.value, knownConfigured: status({ status: 'existing', canCreate: false, verified: false, policyHostname: '_dmarc.example.test', value: 'v=DMARC1; p=quarantine;', policy: 'quarantine' }) }
  f.calls[0].resolve(status()); await flush()
  assert.match(text(f.root), /Policy configured · awaiting DNS confirmation/)
  assert.match(text(f.root), /Do not add another record/)
  assert.match(text(f.root), /v=DMARC1; p=quarantine;/)
  assert.equal(f.copyButtons().length, 0)
  const refresh = f.refresh()
  f.calls[1].resolve(status({ status: 'existing', canCreate: false, verified: true, value: 'v=DMARC1; p=quarantine;', policy: 'quarantine' }))
  await refresh; await flush()
  assert.match(text(f.root), /Existing policy preserved/)
  assert.doesNotMatch(text(f.root), /awaiting DNS confirmation/)
  assert.equal(f.copyButtons().length, 0)
})

test('conflict and unknown states suppress default copying even if a response incorrectly allows creation', async t => {
  const f = fixture(t)
  f.calls[0].resolve(status({ status: 'conflict', reason: 'Multiple DMARC policies found.' })); await flush()
  assert.match(text(f.root), /Needs review/)
  assert.match(text(f.root), /Multiple DMARC policies found/)
  assert.equal(f.copyButtons().length, 0)
  const refresh = f.refresh()
  f.calls[1].resolve(status({ status: 'unknown', reason: 'DNS lookup timed out.' }))
  await refresh; await flush()
  assert.match(text(f.root), /Could not check DMARC/)
  assert.match(text(f.root), /DNS lookup timed out/)
  assert.equal(f.copyButtons().length, 0)
})

test('failed checks announce an error without a default and allow a successful retry', async t => {
  const f = fixture(t)
  f.calls[0].reject(new Error('DNS resolver unavailable')); await flush()
  assert.match(text(f.root), /DNS resolver unavailable/)
  assert.ok(all(f.root).some(n => n.props.role === 'alert'))
  assert.equal(f.copyButtons().length, 0)
  const refresh = f.refresh()
  f.calls[1].resolve(status()); await refresh; await flush()
  assert.match(text(f.root), /Ready to add/)
  assert.equal(f.copyButtons().length, 2)
})

test('a domain change aborts and rejects stale responses even if the transport ignores cancellation', async t => {
  const f = fixture(t)
  f.props.value = { domainUuid: 'second-domain', domainName: 'second.test' }; await flush()
  assert.equal(f.calls[0].signal.aborted, true)
  assert.equal(f.calls[1].uuid, 'second-domain')
  f.calls[1].resolve(status({ hostname: '_dmarc.second.test' })); await flush()
  f.calls[0].resolve(status({ status: 'existing', value: 'private-old-policy' })); await flush()
  assert.match(text(f.root), /_dmarc.second.test/)
  assert.doesNotMatch(text(f.root), /private-old-policy/)
})

test('account changes cannot expose the previous account lookup result', async t => {
  const f = fixture(t)
  f.state.token = 'other-account'
  f.calls[0].resolve(status({ status: 'existing', value: 'private-old-policy' })); await flush()
  assert.doesNotMatch(text(f.root), /private-old-policy/)
  assert.equal(f.copyButtons().length, 0)
})
