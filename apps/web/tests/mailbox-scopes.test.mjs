import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'

const require = createRequire(import.meta.url)
const webRoot = fileURLToPath(new URL('..', import.meta.url))
function evaluate(code) { const module = { exports: {} }; new Function('require', 'module', 'exports', code)(require, module, module.exports); return module.exports }
const lib = evaluate((await build({ entryPoints: [`${webRoot}/src/lib/mailboxScopes.ts`], bundle: true, write: false, platform: 'node', format: 'cjs' })).outputFiles[0].text)

const scopes = {
  total: 5, unread: 3,
  domains: [
    { domainId: 7, name: 'ketabaziz.com', total: 4, unread: 2, identities: [{ identityId: 10, uuid: 'a', email: 'info@ketabaziz.com', displayName: '', color: '#f00', kind: 'personal', isCatchAll: true, total: 4, unread: 2 }] },
    { domainId: 3, name: 'vayb.dev', total: 1, unread: 1, identities: [{ identityId: 2, uuid: 'b', email: 'hello@vayb.dev', displayName: '', color: '', kind: 'personal', isCatchAll: false, total: 1, unread: 1 }] },
  ],
}

test('scope from the URL: an address wins over a domain; junk is ignored', () => {
  assert.deepEqual(lib.scopeFromQuery({}), { identityId: 0, domainId: 0 })
  assert.deepEqual(lib.scopeFromQuery({ domain: '7' }), { identityId: 0, domainId: 7 })
  assert.deepEqual(lib.scopeFromQuery({ domain: '7', identity: '10' }), { identityId: 10, domainId: 0 })
  assert.deepEqual(lib.scopeFromQuery({ domain: '-1', identity: 'x' }), { identityId: 0, domainId: 0 })
})

test('the tree shows only when there is more than one address', () => {
  assert.equal(lib.showScopeTree(null), false)
  assert.equal(lib.showScopeTree(scopes), true)
  assert.equal(lib.showScopeTree({ ...scopes, domains: [scopes.domains[0]] }), false)
})

test('folder links keep the selected domain or address', () => {
  assert.deepEqual(lib.folderLocation('/received?folder=sent', { domain: '7', folder: 'inbox' }), { path: '/received', query: { folder: 'sent', domain: '7' } })
  assert.deepEqual(lib.folderLocation('/received', { identity: '10' }), { path: '/received', query: { identity: '10' } })
  assert.deepEqual(lib.folderLocation('/received?folder=trash', { q: 'invoice' }), { path: '/received', query: { folder: 'trash' } }, 'search and other filters are not carried over')
})

test('tree links open that inbox', () => {
  assert.deepEqual(lib.scopeLocation({ domainId: 7 }), { path: '/received', query: { domain: '7' } })
  assert.deepEqual(lib.scopeLocation({ identityId: 2, domainId: 7 }), { path: '/received', query: { identity: '2' } })
  assert.deepEqual(lib.scopeLocation({}), { path: '/received', query: {} })
})

test('the toolbar names the open scope', () => {
  assert.equal(lib.scopeLabel(scopes, {}), '')
  assert.equal(lib.scopeLabel(scopes, { domain: '7' }), 'ketabaziz.com')
  assert.equal(lib.scopeLabel(scopes, { identity: '2' }), 'hello@vayb.dev')
  assert.equal(lib.scopeLabel(scopes, { domain: '999' }), '')
  assert.equal(lib.scopeLabel(null, { domain: '7' }), '')
})

test('delivered-to shows the address the sender wrote to', () => {
  assert.equal(lib.deliveredTo({ envelopeRecipients: ['support@ketabaziz.com'], identityEmail: 'info@ketabaziz.com' }), 'support@ketabaziz.com', 'catch-all mail')
  assert.equal(lib.deliveredTo({ envelopeRecipients: ['INFO@ketabaziz.com', 'info+news@ketabaziz.com'], identityEmail: 'info@ketabaziz.com' }), 'info+news@ketabaziz.com', '+tag mail')
  assert.equal(lib.deliveredTo({ envelopeRecipients: ['info@ketabaziz.com'], identityEmail: 'info@ketabaziz.com' }), 'info@ketabaziz.com')
  assert.equal(lib.deliveredTo({ envelopeRecipients: [], identityEmail: 'info@ketabaziz.com' }), 'info@ketabaziz.com')
  assert.equal(lib.deliveredTo({}), '')
})

test('the sidebar tree, the inbox title and scoped counts are wired up', async () => {
  const sidebar = await readFile(`${webRoot}/src/components/layout/Sidebar.vue`, 'utf8')
  assert.match(sidebar, /All inboxes/)
  assert.match(sidebar, /folderLocation\(item\.route, route\.query\)/, 'folder links keep the scope')
  assert.match(sidebar, /v-else-if="sharedIdentities\.length"/, 'shared mailboxes keep their own list when the tree is hidden')
  const inbox = await readFile(`${webRoot}/src/views/ReceivedInbox.vue`, 'utf8')
  assert.match(inbox, /mailbox\.fetchCounts\(identityId\.value, force, domainId\.value\)/)
  assert.match(inbox, /Delivered to:/)
  assert.match(inbox, /mailbox\.scopes\s*\n?\s*\? mailbox\.scopes\.domains\.map/)
})
