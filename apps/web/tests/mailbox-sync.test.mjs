import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const out = await build({ entryPoints: [fileURLToPath(new URL('../src/lib/mailboxSync.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs' })
const module = { exports: {} }
new Function('module', 'exports', out.outputFiles[0].text)(module, module.exports)
const { applyMailboxEvent, matchesView } = module.exports

const at = minute => `2026-10-07T10:${String(minute).padStart(2, '0')}:00Z`
const mail = (uuid, minute, extra = {}) => ({ id: minute, uuid, identityId: 1, domainId: 1, folder: 'inbox', isRead: false, isStarred: false, isArchived: false, isTrashed: false, isSpam: false, hasAttachments: false, receivedAt: at(minute), ...extra })
const inbox = { folder: 'inbox', identityId: 0, page: 1, pageSize: 50 }
const state = { emails: [mail('c', 30), mail('a', 10)] }
const created = (summary, cursor = '5') => ({ type: 'new_email', cursor, uuid: summary.uuid, identityId: summary.identityId, summary })
const updated = (summary, cursor = '6') => ({ type: 'email_update', cursor, uuid: summary.uuid, summary })
const ids = result => result.emails.map(e => e.uuid)

test('new mail is inserted in server order on the first page', () => {
  const result = applyMailboxEvent(state, created(mail('b', 20)), inbox)
  assert.deepEqual(ids(result), ['c', 'b', 'a'])
  assert.equal(result.totalDelta, 1)
  assert.equal(result.needsRefresh, false)
  assert.equal(result.newMailPill, false)
  const full = applyMailboxEvent(state, created(mail('old', 1)), { ...inbox, pageSize: 2 })
  assert.deepEqual(ids(full), ['c', 'a'], 'a full page keeps its size')
})

test('updates patch rows and moves leave or enter the folder', () => {
  const read = applyMailboxEvent(state, updated(mail('a', 10, { isRead: true })), inbox)
  assert.deepEqual(ids(read), ['c', 'a'])
  assert.equal(read.emails[1].isRead, true)
  assert.equal(read.totalDelta, 0)
  const archived = applyMailboxEvent(state, updated(mail('a', 10, { isArchived: true })), inbox)
  assert.deepEqual(ids(archived), ['c'])
  assert.equal(archived.totalDelta, -1)
  const restored = applyMailboxEvent({ emails: [mail('c', 30)] }, updated(mail('a', 10)), inbox)
  assert.deepEqual(ids(restored), ['c', 'a'])
  assert.equal(restored.totalDelta, 1)
  const spam = applyMailboxEvent({ emails: [] }, updated(mail('s', 5, { folder: 'inbox', isSpam: true })), { ...inbox, folder: 'spam' })
  assert.deepEqual(ids(spam), ['s'], 'is_spam counts as Spam like the server query')
})

test('deletes remove every listed row', () => {
  const result = applyMailboxEvent(state, { type: 'email_deleted', cursor: '7', uuids: ['a', 'c', 'gone'] }, inbox)
  assert.deepEqual(ids(result), [])
  assert.equal(result.totalDelta, -2)
})

test('identity, domain and flag filters are matched like the list query', () => {
  assert.equal(matchesView(mail('x', 1, { identityId: 2 }), { ...inbox, identityId: 1 }), false)
  assert.equal(matchesView(mail('x', 1, { domainId: 3 }), { ...inbox, domainId: 4 }), false)
  assert.equal(matchesView(mail('x', 1, { isRead: true }), { ...inbox, isRead: false }), false)
  assert.equal(matchesView(mail('x', 1, { labels: ['work'] }), { ...inbox, labels: ['work'] }), true)
  assert.equal(matchesView(mail('x', 1, { isTrashed: true }), { ...inbox, folder: 'all' }), false)
  assert.equal(matchesView(mail('x', 1, { isStarred: true, folder: 'archive', isArchived: true }), { ...inbox, folder: 'starred' }), true)
  const other = applyMailboxEvent(state, created(mail('r', 40, { folder: 'dmarc-reports' })), inbox)
  assert.equal(other.emails, state.emails, 'mail filed elsewhere leaves the view untouched')
  assert.equal(other.newMailPill, false)
})

test('an active search patches shown rows and asks for a refresh for new matches', () => {
  const view = { ...inbox, search: 'invoice' }
  const result = applyMailboxEvent(state, created(mail('n', 50)), view)
  assert.equal(result.needsRefresh, true)
  assert.equal(result.emails, state.emails)
  const patched = applyMailboxEvent(state, updated(mail('a', 10, { isStarred: true })), view)
  assert.equal(patched.needsRefresh, false)
  assert.equal(patched.emails[1].isStarred, true)
})

test('later pages and sender or date filters show the new-mail pill instead', () => {
  for (const view of [{ ...inbox, page: 2 }, { ...inbox, sender: 'boss' }, { ...inbox, dateFrom: '2026-10-01' }]) {
    const result = applyMailboxEvent(state, created(mail('n', 50)), view)
    assert.equal(result.emails, state.emails)
    assert.equal(result.newMailPill, true)
  }
  const moved = applyMailboxEvent(state, updated(mail('n', 50)), { ...inbox, page: 2 })
  assert.equal(moved.newMailPill, false, 'a move into the folder is not new mail')
})
