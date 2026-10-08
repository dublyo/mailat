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
const libFile = `${webRoot}/src/lib/domainReadiness.ts`
function evaluate(code) { const module = { exports: {} }; new Function('require', 'module', 'exports', code)(require, module, module.exports); return module.exports }
const lib = evaluate((await build({ entryPoints: [libFile], bundle: true, write: false, platform: 'node', format: 'cjs' })).outputFiles[0].text)

const source = await readFile(`${webRoot}/src/components/settings/DomainApiReadiness.vue`, 'utf8')
const { descriptor } = parse(source, { filename: 'DomainApiReadiness.vue' })
const compiled = compileScript(descriptor, { id: 'api-readiness-test', inlineTemplate: true })
const output = await build({
  stdin: { contents: compiled.content, resolveDir: webRoot, loader: 'ts' },
  bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
  plugins: [{ name: 'readiness-fixture', setup(builder) {
    builder.onResolve({ filter: /^@\/lib\/domainReadiness$/ }, () => ({ path: libFile }))
    builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
    builder.onResolve({ filter: /^lucide-vue-next$/ }, () => ({ path: 'icons', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ contents: args.path === 'api'
      ? 'export const { api, domainApi, identityApi } = globalThis.__readinessFixture'
      : 'export const CheckCircle = () => null; export const Circle = CheckCircle, AlertCircle = CheckCircle, RefreshCw = CheckCircle, Copy = CheckCircle', loader: 'js' }))
  } }],
})

const item = (key, status, fix = '', extra = {}) => ({ key, label: key, status, state: '', optional: key === 'receiving', detail: '', fix, value: '', ...extra })
const pending = (overrides = {}) => ({
  domainUuid: 'd1', domain: 'example.test', ready: false, suggestedIdentity: 'noreply@example.test', checkedAt: '2026-10-08T08:00:00Z',
  items: [
    item('verified', 'ok'),
    item('dmarc', 'missing', 'dmarc', { value: 'v=DMARC1; p=quarantine' }),
    item('sending_resources', 'missing', 'setup_sending'),
    item('sending_identity', 'missing', 'create_identity', { detail: 'you have no sending identity on example.test' }),
    item('receiving', 'off', 'receiving', { value: '10 inbound-smtp.us-east-2.amazonaws.com' }),
  ], ...overrides,
})

test('badges cover every status', () => {
  assert.deepEqual(['ok', 'missing', 'pending', 'attention', 'off', 'unknown'].map(s => lib.readinessBadge(s).label), ['Done', 'To do', 'Pending', 'Needs attention', 'Off', 'Unknown'])
})

test('admins get one-click fixes; members only see where to look', () => {
  const admin = { canManage: true, suggestedIdentity: 'noreply@example.test' }
  const member = { canManage: false, suggestedIdentity: 'noreply@example.test' }
  assert.deepEqual(lib.readinessActions(item('sending_identity', 'missing', 'create_identity'), admin),
    [{ kind: 'create_identity', label: 'Create noreply@example.test', address: 'noreply@example.test' }, { kind: 'choose_identity', label: 'Use another address' }])
  assert.deepEqual(lib.readinessActions(item('sending_identity', 'missing', 'create_identity'), { canManage: true, suggestedIdentity: '' }), [{ kind: 'choose_identity', label: 'Add identity' }])
  assert.deepEqual(lib.readinessActions(item('sending_resources', 'missing', 'setup_sending'), admin), [{ kind: 'setup_sending', label: 'Set up sending resources' }])
  assert.deepEqual(lib.readinessActions(item('sending_resources', 'attention', 'setup_sending'), admin), [{ kind: 'setup_sending', label: 'Retry sending setup' }])
  // Waiting for SES or for the automatic setup offers nothing to click.
  assert.deepEqual(lib.readinessActions(item('sending_resources', 'pending', ''), admin), [])
  assert.deepEqual(lib.readinessActions(item('sending_identity', 'pending', 'create_identity', { state: 'automatic_setup' }), admin), [])
  assert.deepEqual(lib.readinessActions(item('verified', 'missing', 'verify'), admin), [{ kind: 'verify', label: 'Verify now' }])
  for (const fix of ['verify', 'setup_sending', 'create_identity']) {
    assert.deepEqual(lib.readinessActions(item('x', 'missing', fix), member), [])
    assert.equal(lib.needsAdmin(item('x', 'missing', fix), false), true)
  }
  // Only admins add identities: members get the hint, also before verification (no fix yet).
  assert.equal(lib.needsAdmin(item('sending_identity', 'missing', 'create_identity'), false), true)
  assert.equal(lib.needsAdmin(item('sending_identity', 'missing', ''), false), true)
  assert.equal(lib.needsAdmin(item('sending_identity', 'missing', ''), true), false)
  assert.equal(lib.needsAdmin({ ...item('sending_identity', 'pending', ''), state: 'automatic_setup' }, false), false)
  assert.equal(lib.needsAdmin(item('sending_identity', 'ok', ''), false), false)
  assert.deepEqual(lib.readinessActions(item('dmarc', 'missing', 'dmarc'), member), [{ kind: 'dmarc', label: 'Show DMARC setup' }])
  assert.deepEqual(lib.readinessActions(item('receiving', 'off', 'receiving'), member), [{ kind: 'receiving', label: 'Show receiving' }])
  assert.deepEqual(lib.readinessActions(item('dmarc', 'ok', 'dmarc'), admin), [])
  assert.equal(lib.needsAdmin(item('verified', 'ok', 'verify'), false), false)
})

test('verify outcome names the DNS records still missing', () => {
  assert.equal(lib.verifyOutcome({ status: 'active', sesVerified: true }), '')
  assert.equal(lib.verifyOutcome({ status: 'pending', sesVerified: false, dnsRecords: [{ hostname: 'a._domainkey.example.test', verified: false }, { hostname: '_dmarc.example.test', verified: true }] }),
    'Not verified yet. Still waiting for DNS: a._domainkey.example.test. New records can take a while to appear.')
  assert.match(lib.verifyOutcome({ status: 'pending', dnsRecords: [] }), /try again in a few minutes/)
})

test('summary counts required steps and receiving never blocks', () => {
  assert.equal(lib.readinessSummary(pending()), '1 of 4 required steps done')
  assert.equal(lib.readinessSummary(pending({ ready: true })), 'Ready to send with the API')
  assert.equal(lib.readinessSummary(null), '')
  assert.deepEqual(lib.readinessCopy(pending().items[1], 'example.test'), { label: 'TXT _dmarc.example.test', value: 'v=DMARC1; p=quarantine' })
  // No copyable root MX while receiving is off; once it is on and the MX is missing, there is.
  assert.equal(lib.readinessCopy(pending().items[4], 'example.test'), null)
  assert.deepEqual(lib.readinessCopy(item('receiving', 'missing', 'receiving', { value: '10 inbound-smtp.us-east-2.amazonaws.com' }), 'example.test'), { label: 'MX @', value: '10 inbound-smtp.us-east-2.amazonaws.com' })
  assert.equal(lib.readinessCopy(item('dmarc', 'ok', '', { value: 'v=DMARC1; p=none' }), 'example.test'), null)
})

// Vue's real renderer exercises bindings, events and async updates without a DOM dependency.
const node = (type, text = '') => ({ type, text, props: {}, children: [], parent: null })
const renderer = createRenderer({
  createElement: type => node(type), createText: text => node('text', text), createComment: text => node('comment', text),
  setText: (n, text) => { n.text = text }, setElementText: (n, text) => { n.text = text; n.children = [] },
  patchProp: (n, key, _old, value) => { n.props[key] = value },
  insert(n, parent, anchor) {
    if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1)
    n.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    if (index < 0) parent.children.push(n)
    else parent.children.splice(index, 0, n)
  },
  remove(n) { if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1); n.parent = null },
  parentNode: n => n.parent,
  nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1] || null,
})
const all = n => [n, ...n.children.flatMap(all)]
const text = n => all(n).filter(x => x.type !== 'comment').map(x => x.text).join(' ')
const flush = async () => { for (let i = 0; i < 3; i++) { await Promise.resolve(); await nextTick() } }

function fixture(t, canManage = true, verify) {
  const calls = [], events = [], state = { token: 'signed-in' }
  const deferred = kind => (...args) => new Promise((resolve, reject) => calls.push({ kind, args, resolve, reject }))
  globalThis.__readinessFixture = { api: { getToken: () => state.token },
    domainApi: { readiness: deferred('readiness'), setupSending: deferred('setup') }, identityApi: { create: deferred('identity') } }
  const module = { exports: {} }; new Function('require', 'module', 'exports', output.outputFiles[0].text)(require, module, module.exports)
  const props = ref({ domainUuid: 'd1', domainName: 'example.test', canManage, refreshKey: 'a', verify,
    onAction: a => events.push(['action', a.kind]), onChanged: () => events.push(['changed']) })
  const root = node('root'); const app = renderer.createApp({ setup: () => () => h(module.exports.default, props.value) }); app.mount(root); t.after(() => app.unmount())
  const buttons = () => all(root).filter(n => n.type === 'button')
  return { root, calls, events, state, props, button: label => buttons().find(n => text(n).trim() === label) }
}

test('checklist creates the suggested identity in one click and re-reads', async t => {
  const f = fixture(t)
  f.calls[0].resolve(pending()); await flush()
  assert.match(text(f.root), /1 of 4 required steps done/)
  assert.match(text(f.root), /you have no sending identity on example.test/)
  f.button('Create noreply@example.test').props.onClick(); await flush()
  assert.equal(f.calls[1].kind, 'identity')
  assert.deepEqual(f.calls[1].args[0], { displayName: 'example.test', email: 'noreply@example.test', domainId: 'd1' })
  f.calls[1].resolve({}); await flush()
  assert.deepEqual(f.events, [['changed']])
  assert.equal(f.calls[2].kind, 'readiness')
  const done = pending({ items: pending().items.map(i => i.key === 'sending_identity' ? item('sending_identity', 'ok') : i) })
  f.calls[2].resolve(done); await flush()
  assert.match(text(f.root), /2 of 4 required steps done/)
  assert.equal(f.button('Create noreply@example.test'), undefined)
})

test('setup failure is shown; navigation fixes go to the card', async t => {
  const f = fixture(t)
  f.calls[0].resolve(pending()); await flush()
  f.button('Set up sending resources').props.onClick(); await flush()
  assert.equal(f.calls[1].kind, 'setup')
  f.calls[1].reject(new Error('API_URL must be a public HTTPS URL for SNS confirmation')); await flush()
  // A failed fix re-reads so the list matches the server, and keeps the reason.
  assert.equal(f.calls[2].kind, 'readiness')
  f.calls[2].resolve(pending()); await flush()
  assert.match(text(f.root), /API_URL must be a public HTTPS URL/)
  f.button('Show DMARC setup').props.onClick(); f.button('Use another address').props.onClick(); f.button('Show receiving').props.onClick()
  assert.deepEqual(f.events, [['changed'], ['action', 'dmarc'], ['action', 'choose_identity'], ['action', 'receiving']])
  assert.match(text(f.root), /v=DMARC1; p=quarantine/)
})

test('members see the checklist without admin actions, and a refresh key re-reads', async t => {
  const f = fixture(t, false)
  f.calls[0].resolve(pending()); await flush()
  assert.equal(f.button('Set up sending resources'), undefined)
  assert.equal(f.button('Create noreply@example.test'), undefined)
  assert.match(text(f.root), /Ask an organization owner or admin to fix this/)
  f.props.value = { ...f.props.value, refreshKey: 'b' }; await flush()
  assert.equal(f.calls.length, 2)
  // A response for another account is discarded.
  f.state.token = 'other'
  f.calls[1].resolve(pending({ ready: true })); await flush()
  assert.doesNotMatch(text(f.root), /Ready to send with the API/)
})

test('a 409 on Create noreply refreshes the stale checklist', async t => {
  const f = fixture(t)
  f.calls[0].resolve(pending()); await flush()
  f.button('Create noreply@example.test').props.onClick(); await flush()
  f.calls[1].reject(new Error('identity already exists')); await flush()
  assert.deepEqual(f.events, [['changed']])
  const done = pending({ suggestedIdentity: '', items: pending().items.map(i => i.key === 'sending_identity' ? item('sending_identity', 'ok') : i) })
  f.calls[2].resolve(done); await flush()
  assert.match(text(f.root), /identity already exists/)
  assert.equal(f.button('Create noreply@example.test'), undefined)
})

test('Verify now runs in the checklist, shows progress and what is still missing', async t => {
  let finish
  const f = fixture(t, true, () => new Promise((resolve, reject) => { finish = { resolve, reject } }))
  f.calls[0].resolve(pending({ items: pending().items.map(i => i.key === 'verified' ? item('verified', 'missing', 'verify') : i) })); await flush()
  f.button('Verify now').props.onClick(); await flush()
  assert.equal(f.button('Working…').props.disabled, true)
  f.button('Working…').props.onClick(); await flush()
  finish.resolve({ status: 'pending', sesVerified: false, dnsRecords: [{ hostname: 'x._domainkey.example.test', verified: false }] }); await flush()
  assert.deepEqual(f.events, [['changed']])
  assert.equal(f.calls[1].kind, 'readiness')
  f.calls[1].resolve(pending()); await flush()
  assert.match(text(f.root), /Still waiting for DNS: x._domainkey.example.test/)
})

test('Verify errors are shown', async t => {
  const f = fixture(t, true, () => Promise.reject(new Error('DNS lookup failed')))
  f.calls[0].resolve(pending({ items: pending().items.map(i => i.key === 'verified' ? item('verified', 'missing', 'verify') : i) })); await flush()
  f.button('Verify now').props.onClick(); await flush()
  assert.match(text(f.root), /DNS lookup failed/)
  assert.equal(f.calls.length, 1)
})

test('re-reads while the automatic setup runs, then refreshes identities once', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const f = fixture(t)
  const running = pending({ automaticSetup: true, items: pending().items.map(i => i.key === 'sending_identity' ? item('sending_identity', 'pending', '', { state: 'automatic_setup', detail: 'Creating noreply@example.test for you automatically…' }) : i) })
  f.calls[0].resolve(running); await flush()
  assert.match(text(f.root), /Creating noreply@example.test for you automatically/)
  assert.equal(f.button('Create noreply@example.test'), undefined)
  t.mock.timers.tick(2000); await flush()
  assert.equal(f.calls.length, 2)
  f.calls[1].resolve(running); await flush()
  t.mock.timers.tick(4999); await flush()
  assert.equal(f.calls.length, 2)
  t.mock.timers.tick(1); await flush()
  assert.equal(f.calls.length, 3)
  const done = pending({ items: pending().items.map(i => i.key === 'sending_identity' ? item('sending_identity', 'ok') : i) })
  f.calls[2].resolve(done); await flush()
  assert.deepEqual(f.events, [['changed']])
  t.mock.timers.tick(60000); await flush()
  assert.equal(f.calls.length, 3)
})

test('Re-check asks the server to refresh DNS', async t => {
  const f = fixture(t)
  f.calls[0].resolve(pending()); await flush()
  assert.equal(f.calls[0].args[2], false)
  all(f.root).find(n => n.type === 'button' && n.props['aria-label'] === 'Refresh API sending readiness for example.test').props.onClick(); await flush()
  assert.equal(f.calls[1].args[2], true)
})
