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
const libFile = `${webRoot}/src/lib/receiving.ts`
function evaluate(code) { const module = { exports: {} }; new Function('require', 'module', 'exports', code)(require, module, module.exports); return module.exports }
const lib = evaluate((await build({ entryPoints: [libFile], bundle: true, write: false, platform: 'node', format: 'cjs' })).outputFiles[0].text)

const source = await readFile(`${webRoot}/src/components/settings/DomainReceivingStatus.vue`, 'utf8')
const { descriptor } = parse(source, { filename: 'DomainReceivingStatus.vue' })
const compiled = compileScript(descriptor, { id: 'receiving-status-test', inlineTemplate: true })
const output = await build({
  stdin: { contents: compiled.content, resolveDir: webRoot, loader: 'ts' },
  bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
  plugins: [{ name: 'receiving-fixture', setup(builder) {
    builder.onResolve({ filter: /^@\/lib\/receiving$/ }, () => ({ path: libFile }))
    builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
    builder.onResolve({ filter: /^lucide-vue-next$/ }, () => ({ path: 'icons', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ contents: args.path === 'api'
      ? 'export const { api, domainApi, receivedInboxApi } = globalThis.__receivingFixture'
      : 'export const Check = () => null; export const Cloud = Check, Copy = Check, Inbox = Check, RefreshCw = Check', loader: 'js' }))
  } }],
})

const status = (overrides = {}) => ({
  domainUuid: 'd1', domain: 'vaybcode.com', enabled: false, mxStatus: 'not_enabled', existingMx: [], reason: '', checkedAt: '2026-10-07T08:00:00Z',
  mxRecord: { type: 'MX', host: 'vaybcode.com', name: '@', value: '10 inbound-smtp.us-east-2.amazonaws.com', priority: 10, target: 'inbound-smtp.us-east-2.amazonaws.com' },
  ...overrides,
})

test('badge labels cover every MX state and unknown is never ready', () => {
  assert.deepEqual(['not_enabled', 'missing', 'published', 'conflict', 'unknown', undefined].map(s => lib.receivingBadge(s).label),
    ['Off', 'On – MX missing', 'Published', 'Points elsewhere', 'Unknown', 'Unknown'])
  assert.equal(lib.canReceive(status({ enabled: true, mxStatus: 'published' })), true)
  for (const mxStatus of ['missing', 'conflict', 'unknown']) assert.equal(lib.canReceive(status({ enabled: true, mxStatus })), false)
  assert.equal(lib.canReceive(null), false)
})

test('copy fields give the exact dashboard record', () => {
  assert.deepEqual(lib.mxCopyFields(status()), [
    { label: 'Type', value: 'MX' }, { label: 'Name', value: '@' },
    { label: 'Mail server', value: 'inbound-smtp.us-east-2.amazonaws.com' }, { label: 'Priority', value: '10' },
  ])
})

test('mailbox reasons say off, missing and elsewhere precisely', () => {
  assert.match(lib.receivingProblem(status(), 'vaybcode.com'), /Receiving is off for vaybcode\.com/)
  assert.match(lib.receivingProblem(status({ enabled: true, mxStatus: 'missing' }), 'vaybcode.com'), /no MX record.*10 inbound-smtp\.us-east-2\.amazonaws\.com/)
  assert.match(lib.receivingProblem(status({ enabled: true, mxStatus: 'conflict', existingMx: ['aspmx.l.google.com'] }), 'vaybcode.com'), /points to aspmx\.l\.google\.com/)
  assert.match(lib.receivingProblem(status({ enabled: true, mxStatus: 'unknown' }), 'vaybcode.com'), /could not be checked/)
  // Equal priority: the Mailat MX is published but another one ties or wins.
  const tie = lib.receivingProblem(status({ enabled: true, mxStatus: 'conflict', existingMx: ['inbound-smtp.us-east-2.amazonaws.com', 'mx.other.test'] }), 'vaybcode.com')
  assert.match(tie, /publishes the Mailat MX, but mx\.other\.test has the same or a better priority/)
  assert.match(tie, /lowest priority number/)
  assert.doesNotMatch(tie, /points to inbound-smtp/)
  const tieUpper = lib.receivingProblem(status({ enabled: true, mxStatus: 'conflict', existingMx: ['INBOUND-SMTP.US-EAST-2.AMAZONAWS.COM', 'mx.other.test'] }), 'vaybcode.com')
  assert.match(tieUpper, /publishes the Mailat MX/)
  // Null MX: explained in words, never as "points to .".
  const nullMx = lib.receivingProblem(status({ enabled: true, mxStatus: 'conflict', existingMx: ['.'] }), 'vaybcode.com')
  assert.match(nullMx, /null MX, which says it accepts no mail/); assert.doesNotMatch(nullMx, /points to \./)
  assert.equal(lib.receivingProblem(status({ enabled: true, mxStatus: 'published' }), 'vaybcode.com'), '')
  // Before the lookup answers, fall back to the domain switch only.
  assert.match(lib.receivingProblem(null, 'vaybcode.com', false), /off/)
  assert.equal(lib.receivingProblem(null, 'vaybcode.com', true), '')
  assert.equal(lib.receivingFixLink('d 1'), '/domains?receiving=d%201')
})

test('mailbox overview says why it receives no mail', () => {
  assert.equal(lib.receivesMailLabel(status({ enabled: true, mxStatus: 'missing' }), true).value, 'No — domain has no MX')
  assert.equal(lib.receivesMailLabel(status(), true).value, 'No — receiving is off for this domain')
  assert.equal(lib.receivesMailLabel(status({ enabled: true, mxStatus: 'conflict' }), true).value, 'No — domain MX points elsewhere')
  assert.deepEqual(lib.receivesMailLabel(status({ enabled: true, mxStatus: 'published' }), true), { value: 'Yes', ok: true })
  assert.equal(lib.receivesMailLabel(status({ enabled: true, mxStatus: 'published' }), false).value, 'No — turned off for this mailbox')
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
const flush = async () => { for (let i = 0; i < 4; i++) { await Promise.resolve(); await nextTick() } }

function fixture(t, overrides = {}) {
  const calls = [], state = { token: 'signed-in' }, changed = []
  const pending = kind => (...args) => new Promise((resolve, reject) => calls.push({ kind, args, resolve, reject }))
  globalThis.__receivingFixture = {
    api: { getToken: () => state.token },
    domainApi: { receivingStatus: pending('status'), addDNSToCloudflare: pending('cloudflare') },
    receivedInboxApi: { setupReceiving: pending('setup') },
  }
  const module = evaluate(output.outputFiles[0].text)
  const props = ref({ domainUuid: 'd1', domainName: 'vaybcode.com', domainId: 7, receivingEnabled: false, canManage: true, canEnable: true, onChanged: () => changed.push(1), ...overrides })
  const root = node('root'); const app = renderer.createApp({ setup: () => () => h(module.default, props.value) }); app.mount(root); t.after(() => app.unmount())
  const find = label => all(root).find(n => (n.type === 'button' || n.type === 'form') && n.props['aria-label'] === label)
  return { root, calls, state, props, changed, find, last: kind => calls.filter(c => c.kind === kind).at(-1) }
}

test('every card shows the record; enabling needs an explicit confirm', async t => {
  const f = fixture(t)
  f.last('status').resolve(status({ existingMx: ['mx.old-provider.test'] })); await flush()
  assert.match(text(f.root), /Receiving \(MX\)/); assert.match(text(f.root), /Off/)
  assert.match(text(f.root), /Sending works without this record; receiving mail and mailboxes need it/)
  assert.match(text(f.root), /inbound-smtp\.us-east-2\.amazonaws\.com/); assert.ok(f.find('Copy Mail server')); assert.ok(f.find('Copy Priority'))
  assert.equal(f.calls.filter(c => c.kind === 'setup').length, 0)
  f.find('Enable receiving for vaybcode.com').props.onClick(); await flush()
  assert.equal(f.calls.filter(c => c.kind === 'setup').length, 0, 'opening the confirm step must not enable receiving')
  assert.match(text(f.root), /does not send a copy to your existing inbox provider/)
  assert.match(text(f.root), /currently goes to mx\.old-provider\.test/)
  const done = f.find('Confirm enable receiving').props.onClick(); await flush()
  assert.deepEqual(f.last('setup').args, [7])
  f.last('setup').resolve({ success: true }); await flush()
  assert.equal(f.changed.length, 1)
  assert.equal(f.last('status').args[1], true, 'enabling re-checks without the cache')
  f.last('status').resolve(status({ enabled: true, mxStatus: 'missing' })); await done; await flush()
  assert.match(text(f.root), /On – MX missing/); assert.match(text(f.root), /Mailat did not change your DNS/)
})

test('missing MX offers Cloudflare for just that record and reports conflicts', async t => {
  const f = fixture(t, { receivingEnabled: true })
  f.last('status').resolve(status({ enabled: true, mxStatus: 'missing' })); await flush()
  f.find('Add MX to Cloudflare').props.onClick(); await flush()
  const form = f.find('Add MX to Cloudflare')
  assert.equal(form.type, 'form')
  // No token: nothing is sent.
  await form.props.onSubmit({ preventDefault() {} }); await flush()
  assert.equal(f.calls.filter(c => c.kind === 'cloudflare').length, 0); assert.match(text(f.root), /Enter your Cloudflare API token/)
  const input = all(f.root).find(n => n.type === 'input' && n.props.type === 'password')
  input.props.onInput({ target: { value: 'cf-token' } }); await flush()
  const submitted = form.props.onSubmit({ preventDefault() {} }); await flush()
  assert.deepEqual(f.last('cloudflare').args, ['d1', 'cf-token', undefined, 'receiving-mx'])
  f.last('cloudflare').resolve({ results: [{ type: 'MX', hostname: 'vaybcode.com', value: '10 x', success: false, skipped: true, status: 'conflict', reason: 'The domain root already has another MX record.' }] }); await flush()
  f.last('status').resolve(status({ enabled: true, mxStatus: 'conflict', existingMx: ['aspmx.l.google.com'] })); await submitted; await flush()
  assert.match(text(f.root), /already has another MX record/); assert.match(text(f.root), /Points elsewhere/)
  assert.match(text(f.root), /never replaces another provider's MX/)
  assert.equal(f.find('Add MX to Cloudflare'), undefined)
})

test('null MX is shown in words and copy success is announced', async t => {
  const f = fixture(t, { receivingEnabled: true })
  f.last('status').resolve(status({ enabled: true, mxStatus: 'conflict', existingMx: ['.'] })); await flush()
  assert.match(text(f.root), /null MX \(accepts no mail\)/); assert.doesNotMatch(text(f.root), /Current public MX:\s+\.\s/)
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'navigator')
  Object.defineProperty(globalThis, 'navigator', { value: { clipboard: { writeText: async () => {} } }, configurable: true })
  t.after(() => { if (previous) Object.defineProperty(globalThis, 'navigator', previous); else delete globalThis.navigator })
  const button = f.find('Copy Mail server')
  assert.match(button.props.class, /min-h-11/); assert.match(button.props.class, /focus-visible:outline/)
  await button.props.onClick(); await flush()
  assert.ok(f.find('Copied Mail server'), 'button label says Copied')
  const live = all(f.root).find(n => n.props['aria-live'] === 'polite')
  assert.match(text(live), /Copied Mail server/)
})

test('members see the status and record but no actions; failures are unknown', async t => {
  const f = fixture(t, { canManage: false })
  f.last('status').reject(new Error('lookup unavailable')); await flush()
  assert.match(text(f.root), /Unknown/); assert.match(text(f.root), /lookup unavailable/)
  assert.doesNotMatch(text(f.root), /Published/)
  const recheck = f.find('Re-check MX for vaybcode.com').props.onClick()
  f.last('status').resolve(status({ enabled: true, mxStatus: 'published', existingMx: ['inbound-smtp.us-east-2.amazonaws.com'] })); await recheck; await flush()
  assert.match(text(f.root), /Published/)
  assert.equal(f.find('Enable receiving for vaybcode.com'), undefined); assert.equal(f.find('Add MX to Cloudflare'), undefined)
})

test('answers for an older domain or account are discarded', async t => {
  const f = fixture(t)
  f.props.value = { ...f.props.value, domainUuid: 'd2', domainName: 'second.test' }; await flush()
  f.calls[1].resolve(status({ domainUuid: 'd2', domain: 'second.test', enabled: true, mxStatus: 'missing' })); await flush()
  f.calls[0].resolve(status({ enabled: true, mxStatus: 'published' })); await flush()
  assert.match(text(f.root), /On – MX missing/); assert.doesNotMatch(text(f.root), /Published/)
  f.find('Re-check MX for second.test').props.onClick(); f.state.token = 'other-account'
  f.last('status').resolve(status({ domainUuid: 'd2', enabled: true, mxStatus: 'published' })); await flush()
  assert.doesNotMatch(text(f.root), /Published/)
})

test('mailbox screens link to the domain card and warn in the New mailbox form', async () => {
  const mailboxes = await readFile(`${webRoot}/src/views/DomainMailboxes.vue`, 'utf8')
  assert.match(mailboxes, /Fix receiving/); assert.match(mailboxes, /This domain cannot receive mail yet/)
  const detail = await readFile(`${webRoot}/src/views/MailboxDetail.vue`, 'utf8')
  assert.match(detail, /Receives mail/)
  const domains = await readFile(`${webRoot}/src/views/Domains.vue`, 'utf8')
  assert.match(domains, /<DomainReceivingStatus/); assert.match(domains, /route\.query\.receiving/)
  // Manual SMTP domains have their own MX row; the SES receiving row is only for SES domains.
  assert.match(domains, /<DomainReceivingStatus\s+v-if="domain\.emailProvider === 'ses'"/)
})
