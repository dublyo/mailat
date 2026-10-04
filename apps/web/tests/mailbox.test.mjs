import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { setActivePinia, createPinia } = require('pinia')
const storeFile = fileURLToPath(new URL('../src/stores/receivedInbox.ts', import.meta.url))
const storeBuild = await build({
  entryPoints: [storeFile], bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
  plugins: [{ name: 'mailbox-fixture', setup(builder) {
    builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'fixture', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ contents: 'export const { api, receivedInboxApi, inboxSSE } = globalThis.__mailboxFixture', loader: 'js' }))
  } }],
})
const composeBuild = await build({ entryPoints: [fileURLToPath(new URL('../src/lib/compose.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs' })
function evaluate(code) { const module = { exports: {} }; new Function('require', 'module', 'exports', code)(require, module, module.exports); return module.exports }
const { replyRecipients, replySender, prefixedSubject, composeThreadHeaders, failedRetryPayload, retainSendAttempt } = evaluate(composeBuild.outputFiles[0].text)
function deferred() { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b }); return { promise, resolve, reject } }
function email(uuid, extra = {}) { return { uuid, identityId: 1, folder: 'inbox', isRead: false, isStarred: false, ...extra } }
function result(emails) { return { emails, total: emails.length, totalPages: emails.length ? 1 : 0 } }
function fixture(overrides = {}) {
  const state = { token: 'user-one', calls: [], counts: [], connections: 0, handlers: null }
  const endpoints = {
    list: async (identity, options) => { state.calls.push([identity, options]); return result([email('a')]) },
    get: async uuid => email(uuid),
    getCounts: async identity => { state.counts.push(identity); return { inbox: 1, unread: 1 } },
    mark: async () => {}, star: async () => {}, move: async () => {}, trash: async () => {}, ...overrides,
  }
  globalThis.__mailboxFixture = { api: { getToken: () => state.token }, receivedInboxApi: endpoints, inboxSSE: { connect(handlers) { state.connections++; state.handlers = handlers }, disconnect() {} } }
  const { useReceivedInboxStore } = evaluate(storeBuild.outputFiles[0].text)
  setActivePinia(createPinia())
  return { store: useReceivedInboxStore(), state, endpoints }
}

test('All Identities search works and clearing explicitly removes old query', async () => {
  const { store, state } = fixture()
  await store.fetchEmails(0, { search: 'invoice', reset: true })
  await store.fetchEmails(0, { search: '', reset: true })
  assert.equal(state.calls[0][0], 0)
  assert.equal(state.calls[0][1].search, 'invoice')
  assert.equal(state.calls[1][1].search, '')
  assert.equal(store.searchQuery, '')
})

test('out-of-order list responses cannot replace the current filter', async () => {
  const first = deferred(), second = deferred()
  const { store } = fixture({ list: (_id, options) => options.search === 'old' ? first.promise : second.promise })
  const oldRequest = store.fetchEmails(0, { search: 'old' })
  const newRequest = store.fetchEmails(0, { search: 'new' })
  second.resolve(result([email('new')]))
  await newRequest
  first.resolve(result([email('old')]))
  await oldRequest
  assert.equal(store.emails[0].uuid, 'new')
  assert.equal(store.isLoading, false)
})

test('identical concurrent requests and fresh cached views do not fetch twice', async () => {
  const pending = deferred(); let calls = 0
  const { store } = fixture({ list: () => { calls++; return pending.promise } })
  const first = store.fetchEmails(0, { folder: 'inbox' })
  const duplicate = store.fetchEmails(0, { folder: 'inbox' })
  assert.equal(calls, 1)
  pending.resolve(result([email('cached')]))
  await Promise.all([first, duplicate])
  await store.fetchEmails(0, { folder: 'inbox' })
  assert.equal(calls, 1)
})

test('returning to cache cancels an unrelated request without stranding future refreshes', async () => {
  const pending = deferred(); let otherCalls = 0
  const { store } = fixture({ list: (_id, options) => {
    if (options.folder === 'inbox') return Promise.resolve(result([email('inbox')]))
    otherCalls++
    return otherCalls === 1 ? pending.promise : Promise.resolve(result([email('archive')]))
  } })
  await store.fetchEmails(0, { folder: 'inbox' })
  const other = store.fetchEmails(0, { folder: 'archive' })
  await store.fetchEmails(0, { folder: 'inbox' })
  pending.resolve(result([email('stale')]))
  await other
  await store.fetchEmails(0, { folder: 'archive' })
  assert.equal(otherCalls, 2)
  assert.equal(store.emails[0].uuid, 'archive')
})

test('forced refresh supersedes a pre-mutation in-flight request', async () => {
  const pending = deferred(); let calls = 0
  const { store } = fixture({ list: () => ++calls === 1 ? pending.promise : Promise.resolve(result([email('fresh')])) })
  const first = store.fetchEmails(0)
  await store.fetchEmails(0, { force: true })
  pending.resolve(result([email('stale')]))
  await first
  assert.equal(calls, 2)
  assert.equal(store.emails[0].uuid, 'fresh')
})

test('detail races and closing do not reopen a stale message or hide rows', async () => {
  const a = deferred(), b = deferred()
  const { store } = fixture({ get: uuid => uuid === 'a' ? a.promise : b.promise })
  await store.fetchEmails(0)
  const first = store.fetchEmail('a')
  const second = store.fetchEmail('b')
  b.resolve(email('b')); await second
  a.resolve(email('a')); await first
  assert.equal(store.currentEmail.uuid, 'b')
  assert.equal(store.emails[0].uuid, 'a')
  assert.equal(store.isLoading, false)
  store.closeEmail()
  assert.equal(store.currentEmail, null)
})

test('mutations refresh counts for unified identity zero and clear only successful selection', async () => {
  const { store, state, endpoints } = fixture()
  await store.fetchEmails(0)
  store.toggleSelect('a')
  await store.markAsRead(['a'], true)
  assert.ok(state.counts.includes(0))
  endpoints.trash = async () => { throw new Error('storage unavailable') }
  await assert.rejects(store.trashEmails(['a']), /storage unavailable/)
  assert.deepEqual(store.selectedEmailUuids, ['a'])
  assert.equal(store.emails.length, 1)
})

test('account changes invalidate cached mail and late responses cannot cross sessions', async () => {
  const pending = deferred()
  const { store, state, endpoints } = fixture({ list: () => pending.promise })
  const first = store.fetchEmails(0)
  state.token = 'user-two'
  endpoints.list = async () => result([email('second-user')])
  await store.fetchEmails(0)
  pending.resolve(result([email('first-user')]))
  await first
  assert.equal(store.emails[0].uuid, 'second-user')
})

const identities = [{ id: 1, email: 'me@owned.test', isDefault: true }, { id: 2, email: 'catch@aliases.test', isCatchAll: true }]
const message = { from: { email: 'sender@outside.test' }, replyToAddress: 'Support <reply@outside.test>', to: [{ email: 'ME@owned.test' }, { email: 'other@outside.test' }], cc: [{ email: 'copy@outside.test' }, { email: 'shop@aliases.test' }, { email: 'teammate@aliases.test' }, { email: 'OTHER@outside.test' }], envelopeRecipients: ['SHOP@aliases.test'] }

test('Reply-To and reply-all preserve teammates, exclude exact owned aliases, and deduplicate case-insensitively', () => {
  assert.deepEqual(replyRecipients(message, identities, false), { to: ['reply@outside.test'], cc: [] })
  assert.deepEqual(replyRecipients(message, identities, true), { to: ['reply@outside.test', 'other@outside.test'], cc: ['copy@outside.test', 'teammate@aliases.test'] })
})
test('catch-all replies prefer actual envelope alias and missing identities fall back safely', () => {
  const selected = replySender({ ...message, identityId: 2, envelopeRecipients: ['shop@aliases.test'] }, identities)
  assert.equal(selected.fromEmail, 'shop@aliases.test')
  assert.equal(Number(selected.identity.id), 2)
  assert.equal(replySender({ ...message, identityId: 999 }, identities).identity.id, 1)
})
test('reply subject prefixes do not grow on each reply', () => {
  assert.equal(prefixedSubject('Re: Existing', 'Re'), 'Re: Existing')
  assert.equal(prefixedSubject('Hello', 'Fwd'), 'Fwd: Hello')
})

test('reopened reply drafts preserve the original threading on every save and send', () => {
  const draft = { ...message, messageId: '<draft-id@owned.test>', inReplyTo: '<parent@outside.test>', references: ['<root@outside.test>', '<parent@outside.test>'] }
  assert.deepEqual(composeThreadHeaders('draft', draft), { inReplyTo: '<parent@outside.test>', references: ['<root@outside.test>', '<parent@outside.test>'] })
  assert.deepEqual(composeThreadHeaders('reply', draft), { inReplyTo: '<draft-id@owned.test>', references: ['<root@outside.test>', '<parent@outside.test>', '<draft-id@owned.test>'] })
})

test('forwarding never inherits the source reply-thread headers', () => {
  assert.deepEqual(composeThreadHeaders('forward', { ...message, messageId: '<message>', inReplyTo: '<parent>', references: ['<parent>'] }), { inReplyTo: undefined, references: undefined })
})

const attemptedPayload = { identityId: 1, fromEmail: 'alias@owned.test', to: [{ email: 'recipient@outside.test' }], subject: 'Draft', textBody: 'Saved content', inReplyTo: '<parent>', references: ['<parent>'], draftId: 'consumed-draft', draftVersion: 2, attachments: [{ blobId: 'deleted-draft-attachment', name: 'report.pdf', type: 'application/pdf' }] }
const failedCopy = { uuid: 'outbox-copy', sendStatus: 'failed', attachments: [{ uuid: 'outbox-attachment', filename: 'report.pdf', contentType: 'application/pdf', sizeBytes: 123 }] }

test('explicit failed-send retry uses Outbox attachments and removes the consumed draft', () => {
  const retry = failedRetryPayload(attemptedPayload, failedCopy)
  assert.equal('draftId' in retry, false)
  assert.equal('draftVersion' in retry, false)
  assert.deepEqual(retry.attachments, [{ blobId: 'outbox-attachment', name: 'report.pdf', type: 'application/pdf' }])
  assert.equal(retry.fromEmail, attemptedPayload.fromEmail)
  assert.equal(retry.textBody, attemptedPayload.textBody)
  assert.equal(retry.inReplyTo, '<parent>')
  assert.equal(attemptedPayload.attachments[0].blobId, 'deleted-draft-attachment')
})

test('a new send attempt requires a confirmed failed copy with all attachments', () => {
  for (const status of ['unknown', 'sending', 'sent', 'delivered']) assert.throws(() => failedRetryPayload(attemptedPayload, { ...failedCopy, sendStatus: status }), /no longer confirmed failed/)
  assert.throws(() => failedRetryPayload(attemptedPayload, { ...failedCopy, attachments: [] }), /incomplete attachments/)
})

test('uncertain send keys survive later client errors while first-attempt validation can be corrected', () => {
  for (const status of [undefined, 400, 401, 403, 409, 422, 429, 500, 503]) assert.equal(retainSendAttempt(status, true), true)
  assert.equal(retainSendAttempt(400, false), false)
  assert.equal(retainSendAttempt(409, false), false)
  assert.equal(retainSendAttempt(503, false), true)
  assert.equal(retainSendAttempt(undefined, false), true)
})

async function settleRefresh() { for (let i = 0; i < 12; i++) await Promise.resolve() }
function realtimeFixture(t, overrides = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'setInterval'] })
  const oldDocument = globalThis.document, oldWindow = globalThis.window
  const doc = new EventTarget(), win = new EventTarget()
  doc.hidden = false
  globalThis.document = doc; globalThis.window = win
  const f = fixture(overrides)
  t.after(() => { f.store.disconnectSSE(); globalThis.document = oldDocument; globalThis.window = oldWindow })
  return { ...f, doc, win }
}

test('first SSE connection catches mail missed after the initial list without losing filters or selection', async t => {
  const { store, state, endpoints } = realtimeFixture(t)
  await store.fetchEmails(0, { folder: 'inbox', domainId: 4, search: 'invoice', isRead: false, page: 2 })
  store.toggleSelect('a'); await store.fetchEmail('a')
  store.connectSSE()
  endpoints.list = async (id, options) => { state.calls.push([id, options]); return result([email('new'), email('a')]) }
  state.handlers.onConnected({ clientId: 'first' })
  t.mock.timers.tick(300); await settleRefresh()
  assert.deepEqual(store.emails.map(e => e.uuid), ['new', 'a'])
  assert.deepEqual(store.selectedEmailUuids, ['a'])
  assert.equal(store.currentEmail.uuid, 'a')
  assert.equal(state.calls.at(-1)[0], 0)
  assert.deepEqual(state.calls.at(-1)[1], { folder: 'inbox', domainId: 4, search: 'invoice', isRead: false, page: 2, pageSize: 50 })
  assert.ok(state.counts.includes(0))
})

test('SSE reconnect catches missed events and burst notifications trigger one refresh', async t => {
  const { store, state, endpoints } = realtimeFixture(t)
  await store.fetchEmails(0); store.connectSSE()
  state.handlers.onConnected({ clientId: 'first' }); t.mock.timers.tick(300); await settleRefresh()
  const before = state.calls.length
  state.handlers.onError(new Event('error'))
  endpoints.list = async (id, options) => { state.calls.push([id, options]); return result([email('while-disconnected')]) }
  state.handlers.onConnected({ clientId: 'reconnected' })
  state.handlers.onNewEmail({}); state.handlers.onCountsUpdate({}); state.handlers.onEmailUpdate({})
  t.mock.timers.tick(300); await settleRefresh()
  assert.equal(state.calls.length, before + 1)
  assert.equal(store.emails[0].uuid, 'while-disconnected')
  assert.equal(store.sseConnected, true)
})

test('visible healthy connections reconcile silently missed events and hidden tabs catch up on return', async t => {
  const { store, state, doc, win } = realtimeFixture(t)
  await store.fetchEmails(0); store.connectSSE()
  state.handlers.onConnected({}); t.mock.timers.tick(300); await settleRefresh()
  const before = state.calls.length
  doc.hidden = true
  t.mock.timers.tick(60000); await settleRefresh()
  state.handlers.onNewEmail({})
  assert.equal(state.calls.length, before)
  doc.hidden = false
  doc.dispatchEvent(new Event('visibilitychange')); win.dispatchEvent(new Event('focus'))
  t.mock.timers.tick(0); await settleRefresh()
  assert.equal(state.calls.length, before + 1)
  t.mock.timers.tick(60000); await settleRefresh()
  assert.equal(state.calls.length, before + 2)
  store.disconnectSSE()
  state.handlers.onConnected({}); win.dispatchEvent(new Event('focus'))
  t.mock.timers.tick(60000); await settleRefresh()
  assert.equal(state.calls.length, before + 2)
})

test('disconnected inboxes poll in 15 seconds and network return reconnects immediately', async t => {
  const { store, state, win } = realtimeFixture(t)
  await store.fetchEmails(0); store.connectSSE()
  state.handlers.onError(new Event('error'))
  const before = state.calls.length
  t.mock.timers.tick(15000); await settleRefresh()
  assert.equal(state.calls.length, before + 1)
  win.dispatchEvent(new Event('online'))
  t.mock.timers.tick(0); await settleRefresh()
  assert.equal(state.connections, 2)
  assert.equal(state.calls.length, before + 2)
})

test('an event arriving during refresh queues one trailing refresh instead of aborting repeatedly', async t => {
  const { store, state, endpoints } = realtimeFixture(t)
  await store.fetchEmails(0); store.connectSSE()
  const pending = deferred(); let calls = 0
  endpoints.list = async () => { calls++; return calls === 1 ? pending.promise : result([email('latest')]) }
  state.handlers.onConnected({}); t.mock.timers.tick(300); await settleRefresh()
  state.handlers.onNewEmail({}); state.handlers.onNewEmail({}); state.handlers.onCountsUpdate({})
  t.mock.timers.tick(1000); await settleRefresh()
  assert.equal(calls, 1)
  pending.resolve(result([email('older')])); await settleRefresh()
  t.mock.timers.tick(300); await settleRefresh()
  assert.equal(calls, 2)
  assert.equal(store.emails[0].uuid, 'latest')
})

test('forced post-event counts supersede pre-event requests and their stale cached result', async () => {
  const old = deferred(); let calls = 0
  const { store } = fixture({ getCounts: () => ++calls === 1 ? old.promise : Promise.resolve({ inbox: 2, unread: 2 }) })
  const first = store.fetchCounts(0)
  await store.fetchCounts(0, true)
  old.resolve({ inbox: 1, unread: 1 }); await first
  await store.fetchCounts(0)
  assert.equal(calls, 2)
  assert.equal(store.counts.unread, 2)
})


test('DMARC report arrival refreshes folder counts without an Inbox arrival notice', async t => {
  const { store, state, endpoints } = realtimeFixture(t)
  await store.fetchEmails(0, { folder: 'inbox' }); store.connectSSE()
  endpoints.getCounts = async () => ({ inbox: 3, inboxUnread: 1, unread: 7, dmarcReports: 6, dmarcReportsUnread: 6 })
  state.handlers.onNewEmail(email('report', { folder: 'dmarc-reports' }))
  t.mock.timers.tick(300); await settleRefresh()
  assert.equal(store.currentFolder, 'inbox')
  assert.equal(store.counts.inboxUnread, 1)
  assert.equal(store.counts.dmarcReportsUnread, 6)
  assert.equal(store.unreadCount, 7)
  assert.equal(store.notice, '')
  assert.equal(state.calls.at(-1)[1].folder, 'inbox')
})

test('DMARC folder and All Mail search retain normal filtering and move semantics', async () => {
  const moves = []
  const { store, state } = fixture({ move: async (ids, folder) => { moves.push([ids, folder]) } })
  await store.fetchEmails(0, { folder: 'dmarc-reports', search: 'example.test', domainId: 2, isRead: false })
  assert.equal(state.calls.at(-1)[1].folder, 'dmarc-reports')
  await store.moveEmails(['a'], 'inbox')
  assert.deepEqual(moves, [[['a'], 'inbox']])
  await store.fetchEmails(0, { folder: 'all', search: 'example.test' })
  assert.equal(state.calls.at(-1)[1].folder, 'all')
  assert.equal(state.calls.at(-1)[1].search, 'example.test')
  await store.moveEmails(['a'], 'dmarc-reports')
  assert.deepEqual(moves.at(-1), [['a'], 'dmarc-reports'])
})
