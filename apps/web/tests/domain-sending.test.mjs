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
const source = await readFile(`${webRoot}/src/components/settings/DomainSendingReadiness.vue`, 'utf8')
const { descriptor } = parse(source, { filename: 'DomainSendingReadiness.vue' })
const compiled = compileScript(descriptor, { id: 'sending-readiness-test', inlineTemplate: true })
const output = await build({
  stdin: { contents: compiled.content, resolveDir: webRoot, loader: 'ts' },
  bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
  plugins: [{ name: 'sending-api-fixture', setup(builder) {
    builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
    builder.onResolve({ filter: /^lucide-vue-next$/ }, () => ({ path: 'icons', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ contents: args.path === 'api'
      ? 'export const { api, domainApi } = globalThis.__sendingFixture'
      : 'export const Send = () => null; export const RefreshCw = Send', loader: 'js' }))
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
const flush = async () => { await Promise.resolve(); await nextTick() }
const stateResult = (overrides = {}) => ({ domainUuid: 'first-domain', storageReady: false, feedbackConfigured: false, subscriptionStatus: 'not_configured', feedbackReady: false, reason: 'Set up resources', checkedAt: '2026-10-04T08:00:00Z', ...overrides })
function fixture(t, canSetup = true) {
  const calls = [], state = { token: 'signed-in' }
  globalThis.__sendingFixture = { api: { getToken: () => state.token }, domainApi: {
    sendingStatus: (uuid, signal) => new Promise((resolve, reject) => calls.push({ kind: 'get', uuid, signal, resolve, reject })),
    setupSending: uuid => new Promise((resolve, reject) => calls.push({ kind: 'setup', uuid, resolve, reject })),
  } }
  const module = { exports: {} }; new Function('require', 'module', 'exports', output.outputFiles[0].text)(require, module, module.exports)
  const props = ref({ domainUuid: 'first-domain', domainName: 'example.test', canSetup })
  const root = node('root'); const app = renderer.createApp({ setup: () => () => h(module.exports.default, props.value) }); app.mount(root); t.after(() => app.unmount())
  return { root, calls, state, props, button: name => all(root).find(n => n.type === 'button' && n.props['aria-label'] === name) }
}
test('sending status shows storage-only HTTP 200 progress without claiming feedback ready', async t => {
  const f = fixture(t); assert.match(text(f.root), /Checking sending resources/)
  f.calls[0].resolve(stateResult({ storageReady: true, reason: 'Existing SES feedback destination needs review' })); await flush()
  assert.match(text(f.root), /Storage ready/); assert.match(text(f.root), /Sending setup needs attention/)
  assert.match(text(f.root), /Existing SES feedback destination needs review/)
  assert.doesNotMatch(text(f.root), /Attachments and delivery feedback ready/)
  assert.match(text(f.root), /preserves your root MX records/)
})
test('setup waits for actual subscription confirmation and refresh establishes readiness', async t => {
  const f = fixture(t); f.calls[0].resolve(stateResult()); await flush()
  const pending = f.button('Set up sending resources').props.onClick(); await flush()
  assert.equal(f.calls[1].kind, 'setup'); assert.equal(f.button('Set up sending resources').props.disabled, true)
  f.calls[1].resolve(stateResult({ storageReady: true, feedbackConfigured: true, subscriptionStatus: 'pending', reason: 'Wait for confirmation' })); await pending; await flush()
  assert.match(text(f.root), /Awaiting delivery subscription confirmation/)
  assert.doesNotMatch(text(f.root), /Attachments and delivery feedback ready/)
  const refresh = f.button('Refresh sending resources for example.test').props.onClick()
  f.calls[2].resolve(stateResult({ storageReady: true, feedbackConfigured: true, subscriptionStatus: 'active', feedbackReady: true, reason: '' })); await refresh; await flush()
  assert.match(text(f.root), /Attachments and delivery feedback ready/)
  assert.equal(f.button('Set up sending resources'), undefined)
})
test('unverified domain blocks setup and failed lookups expose a retry action', async t => {
  const f = fixture(t, false); f.calls[0].reject(new Error('AWS lookup unavailable')); await flush()
  assert.match(text(f.root), /AWS lookup unavailable/); assert.equal(f.button('Set up sending resources').props.disabled, true)
  await f.button('Set up sending resources').props.onClick(); assert.equal(f.calls.length, 1)
  assert.match(text(f.root), /Verify this SES domain/)
})
test('older domain and account responses cannot expose stale readiness', async t => {
  const f = fixture(t); f.props.value = { domainUuid: 'second-domain', domainName: 'second.test', canSetup: true }; await flush()
  assert.equal(f.calls[0].signal.aborted, true)
  f.calls[1].resolve(stateResult({ domainUuid: 'second-domain', reason: 'Current domain' })); await flush()
  f.calls[0].resolve(stateResult({ reason: 'Private old domain' })); await flush()
  assert.match(text(f.root), /Current domain/); assert.doesNotMatch(text(f.root), /Private old domain/)
  f.button('Refresh sending resources for second.test').props.onClick(); f.state.token = 'other-account'
  f.calls[2].resolve(stateResult({ domainUuid: 'second-domain', reason: 'Old account data' })); await flush()
  assert.doesNotMatch(text(f.root), /Old account data/)
})
