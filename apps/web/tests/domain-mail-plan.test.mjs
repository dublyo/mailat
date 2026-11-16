import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'

const require = createRequire(import.meta.url)
const { createRenderer, h, nextTick } = require('vue')
const webRoot = fileURLToPath(new URL('..', import.meta.url))
function evaluate(code) { const module = { exports: {} }; new Function('require', 'module', 'exports', code)(require, module, module.exports); return module.exports }

const store = new Map()
globalThis.localStorage = { getItem: k => store.has(k) ? store.get(k) : null, setItem: (k, v) => store.set(k, String(v)), removeItem: k => store.delete(k) }
const lib = evaluate((await build({ entryPoints: [`${webRoot}/src/lib/domainMailPlan.ts`], bundle: true, write: false, platform: 'node', format: 'cjs' })).outputFiles[0].text)

test('the step is offered to admins on a verified domain where they have no address yet', () => {
  const base = { canManage: true, status: 'active', sesVerified: true, myAddresses: 0, answered: false, addressesLoaded: true }
  assert.equal(lib.shouldOfferMailPlan(base), true)
  assert.equal(lib.shouldOfferMailPlan({ ...base, addressesLoaded: false }), false, 'never before the admin\'s addresses are known')
  assert.equal(lib.shouldOfferMailPlan({ ...base, canManage: false }), false, 'members cannot set it up')
  assert.equal(lib.shouldOfferMailPlan({ ...base, status: 'pending' }), false, 'only after verification')
  assert.equal(lib.shouldOfferMailPlan({ ...base, sesVerified: false }), false)
  assert.equal(lib.shouldOfferMailPlan({ ...base, myAddresses: 1 }), false, 'existing domains such as ketabaziz.com are left alone')
  assert.equal(lib.shouldOfferMailPlan({ ...base, answered: true }), false, 'already answered in this browser')
})

test('a domain anyone already gets mail on is in use', () => {
  assert.equal(lib.domainInUse({ catchAll: null, mailboxes: [], catchAllOptions: [] }), false)
  assert.equal(lib.domainInUse({ catchAll: { email: 'x@d' }, mailboxes: [], catchAllOptions: [] }), true, 'an existing catch-all is never moved')
  assert.equal(lib.domainInUse({ catchAll: null, mailboxes: [{}], catchAllOptions: [] }), true, 'private logins')
  assert.equal(lib.domainInUse({ catchAll: null, mailboxes: [], catchAllOptions: [{}] }), true, "another person's address")
  assert.equal(lib.domainInUse(null), false)
})

test('answered state is stored per domain and can be cleared', () => {
  lib.markMailPlanAnswered('Ans.Test')
  assert.equal(lib.mailPlanAnswered('ans.test'), true)
  lib.markMailPlanAnswered('ans.test', false)
  assert.equal(lib.mailPlanAnswered('ans.test'), false)
})

test('address and step wording', () => {
  assert.equal(lib.localPartError('hello'), '')
  assert.equal(lib.localPartError('first.last'), '')
  assert.match(lib.localPartError(''), /part before @/)
  assert.match(lib.localPartError('me@x.com'), /only the part before @/)
  assert.match(lib.localPartError('a..b'), /letters, numbers/)
  assert.match(lib.localPartError('-x'), /letters, numbers/)
  const me = lib.planSteps('me', { domain: 'new.test', localPart: 'Hello', catchAll: true, receivingEnabled: false })
  assert.equal(me.length, 3)
  assert.match(me[0], /Turn on receiving for new\.test/)
  assert.match(me[1], /hello@new\.test/)
  assert.match(me[2], /catch-all/)
  assert.deepEqual(lib.planSteps('private', { domain: 'new.test', localPart: '', catchAll: true, receivingEnabled: true }), ['Open Mailboxes to create the private logins (one at a time or from a CSV).'])
  assert.equal(lib.planSteps('both', { domain: 'new.test', localPart: 'hi', catchAll: false, receivingEnabled: true }).length, 2)
  assert.match(lib.planSteps('sending', { domain: 'new.test', localPart: '', catchAll: false, receivingEnabled: false })[0], /Nothing changes/)
})

test('the Add Domain choice is remembered per domain and validated on read', () => {
  lib.rememberMailPlan('New.Test', 'both')
  assert.equal(lib.recalledMailPlan('new.test'), 'both')
  store.set('mailat.mailPlan.bad.test', 'everyone')
  assert.equal(lib.recalledMailPlan('bad.test'), '')
  assert.equal(lib.recalledMailPlan('unknown.test'), '')
})

// The panel, rendered with Vue's real renderer and fake API calls.
const source = await readFile(`${webRoot}/src/components/settings/DomainMailPlan.vue`, 'utf8')
const { descriptor } = parse(source, { filename: 'DomainMailPlan.vue' })
const compiled = compileScript(descriptor, { id: 'mail-plan-test', inlineTemplate: true })
const output = await build({
  stdin: { contents: compiled.content, resolveDir: webRoot, loader: 'ts' },
  bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
  plugins: [{ name: 'mail-plan-fixture', setup(builder) {
    builder.onResolve({ filter: /^@\/lib\/domainMailPlan$/ }, () => ({ path: `${webRoot}/src/lib/domainMailPlan.ts` }))
    builder.onResolve({ filter: /^(@\/lib\/api|vue-router|lucide-vue-next)$/ }, args => ({ path: args.path, namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ loader: 'js', contents:
      args.path === '@/lib/api' ? 'export const { identityApi, mailboxAdminApi, receivedInboxApi } = globalThis.__planFixture.api' :
      args.path === 'vue-router' ? 'export const useRouter = () => globalThis.__planFixture.router' :
      'export const Users = () => null; export const CheckCircle = Users' }))
  } }],
})
// Minimal elements: enough for v-model, which listens for input/change events.
const node = (type, text = '') => {
  const listeners = {}
  return { type, text, props: {}, children: [], parent: null, listeners,
    addEventListener(name, fn) { (listeners[name] ||= []).push(fn) },
    removeEventListener(name, fn) { listeners[name] = (listeners[name] || []).filter(f => f !== fn) } }
}
const fire = (el, name) => (el.listeners[name] || []).forEach(fn => fn({ target: el }))
const renderer = createRenderer({
  createElement: type => node(type), createText: text => node('text', text), createComment: text => node('comment', text),
  setText: (item, text) => { item.text = text }, setElementText: (item, text) => { item.text = text; item.children = [] },
  patchProp: (item, key, _old, value) => { item.props[key] = value; if (key === 'value' || key === 'checked') item[key] = value },
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
const flush = async () => { for (let i = 0; i < 6; i++) { await Promise.resolve(); await nextTick() } }
const freeDomain = { catchAll: null, mailboxes: [], catchAllOptions: [] }
function fixture(t, { receivingEnabled = false, failOn = '', failMessage = '', list = async () => freeDomain, gate = null } = {}) {
  store.clear() // each panel starts without a remembered Add Domain answer
  const calls = [], pushes = [], events = []
  const step = (name, result) => async (...args) => {
    calls.push([name, ...args])
    if (gate && name === gate.step) await gate.promise
    if (failOn === name) throw new Error(failMessage || `${name} failed`)
    return result
  }
  globalThis.__planFixture = {
    api: {
      receivedInboxApi: { setupReceiving: step('setupReceiving', {}) },
      identityApi: { create: step('createIdentity', { uuid: 'identity-uuid' }) },
      mailboxAdminApi: { setCatchAll: step('setCatchAll', {}), list },
    },
    router: { push: value => pushes.push(value) },
  }
  const module = evaluate(output.outputFiles[0].text)
  const root = node('root')
  const app = renderer.createApp({ setup: () => () => h(module.default, { domainId: 9, domainUuid: 'dom-uuid', domainName: 'new.test', receivingEnabled, ownerName: 'Ibrahim',
    onChanged: () => events.push('changed'), onFinished: result => events.push('finished:' + result.plan + (result.error ? ':error' : '')), onClose: () => events.push('close'), onInUse: () => events.push('inUse') }) })
  app.mount(root); t.after(() => app.unmount())
  const button = label => all(root).find(n => n.type === 'button' && text(n).trim() === label)
  const radio = value => all(root).find(n => n.type === 'input' && n.props.value === value)
  const choose = value => radio(value).props.onChange()
  return { root, calls, pushes, events, button, radio, choose }
}

test('"Me" enables receiving, creates my address and makes it the catch-all, in that order', async t => {
  const f = fixture(t); await flush()
  assert.equal(f.radio('me').props.checked, true, 'the default choice shows as selected')
  assert.equal(f.radio('private').props.checked, false)
  await f.button('Set up').props.onClick(); await flush()
  assert.deepEqual(f.calls, [
    ['setupReceiving', 9],
    ['createIdentity', { displayName: 'Ibrahim', email: 'hello@new.test', domainId: 'dom-uuid' }],
    ['setCatchAll', 'dom-uuid', 'identity-uuid'],
  ])
  assert.deepEqual(f.events, ['finished:me', 'changed'])
  assert.deepEqual(f.pushes, [], 'no private logins were asked for')
  assert.match(text(f.root), /new\.test is set up/)
  assert.match(text(f.root), /hello@new\.test now arrives in your Inbox/)
})

test('a failure part-way stops there, and the summary keeps what was done and why', async t => {
  const f = fixture(t, { failOn: 'createIdentity' }); await flush()
  await f.button('Set up').props.onClick(); await flush()
  assert.deepEqual(f.calls.map(c => c[0]), ['setupReceiving', 'createIdentity'], 'no catch-all without the address')
  assert.match(text(f.root), /partly set up/)
  assert.match(text(f.root), /createIdentity failed/)
  assert.match(text(f.root), /Receiving is on for new\.test/)
  assert.deepEqual(f.events, ['finished:me:error', 'changed'], 'the page keeps the result while it reloads')
})

test('a domain someone already uses is never offered, so its catch-all cannot be taken', async t => {
  const f = fixture(t, { list: async () => ({ catchAll: { email: 'info@new.test' }, mailboxes: [], catchAllOptions: [] }) }); await flush()
  assert.deepEqual(f.events, ['inUse'])
  assert.equal(f.button('Set up'), undefined, 'nothing is rendered')
  assert.deepEqual(f.calls, [])
})

test('when the check fails the catch-all is not preselected', async t => {
  const f = fixture(t, { receivingEnabled: true, list: async () => { throw new Error('offline') } }); await flush()
  await f.button('Set up').props.onClick(); await flush()
  assert.deepEqual(f.calls.map(c => c[0]), ['createIdentity'])
})

test('receiving turned on meanwhile counts as done', async t => {
  const f = fixture(t, { failOn: 'setupReceiving', failMessage: 'receiving is already enabled for this domain' }); await flush()
  await f.button('Set up').props.onClick(); await flush()
  assert.deepEqual(f.calls.map(c => c[0]), ['setupReceiving', 'createIdentity', 'setCatchAll'])
  assert.match(text(f.root), /new\.test is set up/)
})

test('choices are fixed once Set up is clicked', async t => {
  let release; const gate = { step: 'setupReceiving', promise: new Promise(r => { release = r }) }
  const f = fixture(t, { gate }); await flush()
  const running = f.button('Set up').props.onClick(); await flush()
  f.choose('private'); await flush()
  release(); await running; await flush()
  assert.deepEqual(f.calls.map(c => c[0]), ['setupReceiving', 'createIdentity', 'setCatchAll'], 'the run kept "Me"')
  assert.deepEqual(f.pushes, [])
})

test('"Private logins" skips my address and opens New mailbox', async t => {
  const f = fixture(t, { receivingEnabled: true }); await flush()
  f.choose('private'); await flush()
  await f.button('Continue to private logins').props.onClick(); await flush()
  assert.deepEqual(f.calls, [], 'receiving was already on; no address is created')
  assert.deepEqual(f.pushes, [{ path: '/domains/dom-uuid/mailboxes', query: { new: '1' } }])
})

test('"Sending only" changes nothing and remembers the answer', async t => {
  const f = fixture(t); await flush()
  f.choose('sending'); await flush()
  await f.button('Sending only').props.onClick(); await flush()
  assert.deepEqual(f.calls, [])
  assert.deepEqual(f.events, ['close'])
  assert.equal(lib.recalledMailPlan('new.test'), 'sending')
})
