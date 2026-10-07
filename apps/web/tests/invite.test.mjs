import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { readFileSync } from 'node:fs'

const require = createRequire(import.meta.url)
const { setActivePinia, createPinia } = require('pinia')
function evaluate(code) { const module = { exports: {} }; new Function('require', 'module', 'exports', code)(require, module, module.exports); return module.exports }
const lib = evaluate((await build({ entryPoints: [fileURLToPath(new URL('../src/lib/invite.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs' })).outputFiles[0].text)
const { inviteTokenFromHash, forwardVerificationFromHash, forwardConfirmation, clearFragment, completeInvite } = lib

const token = 'Q2hhbmdlTWVJbnZpdGVUb2tlbl9fMDEyMzQ1Njc4OWFi'
const forwardId = '0d6c1c1e-8b7a-4b8e-9e2f-6a1f2b3c4d5e'

test('invite and forward tokens are read from the fragment only when well formed', () => {
  assert.equal(inviteTokenFromHash(`#token=${token}`), token)
  assert.equal(inviteTokenFromHash(''), '')
  assert.equal(inviteTokenFromHash('#token=short'), '')
  assert.equal(inviteTokenFromHash('#token=<script>alert(1)</script>xxxxxxxx'), '')
  assert.deepEqual(forwardVerificationFromHash(`#id=${forwardId}&token=${token}`), { uuid: forwardId, token })
  assert.equal(forwardVerificationFromHash(`#id=not-a-uuid&token=${token}`), null)
  assert.equal(forwardVerificationFromHash(`#id=${forwardId}`), null)
})

test('clearing the fragment keeps the path and query', () => {
  const calls = []
  clearFragment({ location: { pathname: '/invite', search: '?x=1' }, history: { state: { k: 1 }, replaceState: (...args) => calls.push(args) } })
  assert.deepEqual(calls, [[{ k: 1 }, '', '/invite?x=1']])
})

test('accepting clears the hash and then adopts the issued session', async () => {
  const order = []
  const user = { id: 7, email: 'new@acme.test', role: 'member' }
  const result = await completeInvite({ token, name: 'New Member', password: 'long-password' }, {
    accept: async input => { order.push(['accept', input.token]); return { token: 'session-7', user } },
    clearHash: () => order.push(['clear']),
    setSession: (session, who) => order.push(['session', session, who.email]),
  })
  assert.deepEqual(order, [['accept', token], ['clear'], ['session', 'session-7', 'new@acme.test']])
  assert.equal(result, user)
  const failed = []
  await assert.rejects(completeInvite({ token, name: 'x', password: 'y' }, {
    accept: async () => { throw new Error('This invite link is invalid or has expired') },
    clearHash: () => failed.push('clear'), setSession: () => failed.push('session'),
  }), /invalid or has expired/)
  assert.deepEqual(failed, [], 'a failed accept keeps the link usable for a retry')
})

const storesBuild = await build({
  stdin: { contents: "export { useAuthStore } from './src/stores/auth'", resolveDir: fileURLToPath(new URL('..', import.meta.url)), loader: 'ts' },
  bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
  plugins: [{ name: 'auth-fixture', setup(builder) {
    builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
    builder.onResolve({ filter: /^\.\/(receivedInbox|inbox|settings|domains)$/ }, () => ({ path: 'stores', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ contents: args.path === 'api'
      ? 'export const { api, authApi, pushApi } = globalThis.__inviteFixture'
      : 'export const useReceivedInboxStore = () => ({ reset() { globalThis.__inviteFixture.resets++ } }); export const useInboxStore = () => ({ closeCompose() {} }); export const useDomainsStore = () => ({ reset() {} }); export const useSettingsStore = () => ({ clearLocalSettings() {} })', loader: 'js' }))
  } }],
})

test('setSession signs in the invited user and signs out another account first', async () => {
  const state = { token: 'other-account', revoked: [] }
  globalThis.__inviteFixture = { resets: 0, api: { getToken: () => state.token, setToken: value => { state.token = value } }, authApi: { logout: async t => { state.revoked.push(t) } }, pushApi: { unsubscribe: async () => {} } }
  const { useAuthStore } = evaluate(storesBuild.outputFiles[0].text)
  setActivePinia(createPinia())
  const auth = useAuthStore()
  auth.setSession('invite-session', { id: 9, email: 'new@acme.test', role: 'member' })
  await new Promise(resolve => setTimeout(resolve, 0))
  assert.deepEqual(state.revoked, ['other-account'])
  assert.equal(globalThis.__inviteFixture.resets, 1)
  assert.equal(state.token, 'invite-session')
  assert.equal(auth.isAuthenticated, true)
  assert.equal(auth.isInitialized, true)
  assert.equal(auth.user.role, 'member')
})

test('opening a forward link confirms nothing until a click, and sends the token once', async () => {
  const calls = []
  let cleared = 0
  const link = forwardConfirmation(`#id=${forwardId}&token=${token}`, { verify: async (uuid, t) => { calls.push([uuid, t]) }, clearHash: () => { cleared++ } })
  assert.equal(link.valid, true)
  assert.equal(cleared, 1, 'the token leaves the address bar on open')
  assert.deepEqual(calls, [], 'a scanner that only opens the page activates nothing')
  await Promise.all([link.confirm(), link.confirm()])
  assert.deepEqual(calls, [[forwardId, token]])

  const broken = forwardConfirmation('#id=nope', { verify: async () => { calls.push('bad') }, clearHash: () => { cleared++ } })
  assert.equal(broken.valid, false)
  await assert.rejects(broken.confirm())
  assert.equal(calls.length, 1)
})

test('the forward confirmation page verifies from a button, not on load', () => {
  const source = readFileSync(new URL('../src/views/VerifyForward.vue', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /onMounted/)
  assert.match(source, /@click="confirm"/)
  assert.match(source, /forwardConfirmation\(window\.location\.hash/)
})
