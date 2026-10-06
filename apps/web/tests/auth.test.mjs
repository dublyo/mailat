import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { setActivePinia, createPinia } = require('pinia')
const webRoot = fileURLToPath(new URL('..', import.meta.url))
function evaluate(code) { const module = { exports: {} }; new Function('require', 'module', 'exports', code)(require, module, module.exports); return module.exports }
function deferred() { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b }); return { promise, resolve, reject } }
const apiBuild = await build({
  entryPoints: [`${webRoot}/src/lib/api.ts`], bundle: true, write: false, platform: 'node', format: 'cjs',
  define: { 'import.meta.env.VITE_API_URL': "''" },
  plugins: [{ name: 'axios-fixture', setup(builder) {
    builder.onResolve({ filter: /^axios$/ }, () => ({ path: 'axios', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ contents: 'export default globalThis.__authAxios', loader: 'js' }))
  } }],
})
const storesBuild = await build({
  stdin: { contents: "export { useAuthStore } from './src/stores/auth'; export { useDomainsStore } from './src/stores/domains'", resolveDir: webRoot, loader: 'ts' },
  bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
  plugins: [{ name: 'auth-fixture', setup(builder) {
    builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
    builder.onResolve({ filter: /^\.\/(receivedInbox|inbox|settings)$/ }, () => ({ path: 'other-stores', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ contents: args.path === 'api'
      ? 'export const { api, authApi, domainApi, identityApi, receivedInboxApi } = globalThis.__authFixture'
      : 'export const useReceivedInboxStore = () => ({ reset() {} }); export const useInboxStore = () => ({ closeCompose() {} }); export const useSettingsStore = () => ({ clearLocalSettings(options) { globalThis.__settingsCleared = (globalThis.__settingsCleared || 0) + 1; globalThis.__settingsClearedWith = options } })', loader: 'js' }))
  } }],
})
function apiFixture() {
  let rejectResponse
  const saved = new Map()
  globalThis.localStorage = { getItem: key => saved.get(key) || null, setItem: (key, value) => saved.set(key, value), removeItem: key => saved.delete(key) }
  globalThis.window = { location: { href: '/login?redirect=%2Freceived%3Ffolder%3Dsent' } }
  globalThis.__authAxios = { create: () => ({ post: async () => ({ data: { data: { token: 'stream-ticket', expiresAt: 'later' } } }), interceptors: { request: { use() {} }, response: { use(_accept, reject) { rejectResponse = reject } } } }) }
  const { api, InboxSSE } = evaluate(apiBuild.outputFiles[0].text)
  return { api, rejectResponse, InboxSSE }
}
function storesFixture() {
  const state = { token: 'first-account' }
  const endpoints = {
    api: { getToken: () => state.token, setToken: value => { state.token = value } },
    authApi: { logout: async () => {} }, domainApi: { list: async () => [] }, identityApi: { list: async () => [] }, receivedInboxApi: {},
  }
  globalThis.__authFixture = endpoints
  const { useAuthStore, useDomainsStore } = evaluate(storesBuild.outputFiles[0].text)
  setActivePinia(createPinia())
  return { state, endpoints, auth: useAuthStore(), domains: useDomainsStore() }
}

test('invalid login credentials preserve the login URL and return an inline API error', async () => {
  const { rejectResponse } = apiFixture()
  const href = window.location.href
  await assert.rejects(rejectResponse({ config: { url: '/api/v1/auth/login' }, response: { status: 401, data: { message: 'Invalid credentials' } } }), error => error.message === 'Invalid credentials' && error.status === 401)
  assert.equal(window.location.href, href)
})

test('protected API 401 still clears the expired token and navigates to login', async () => {
  const { api, rejectResponse } = apiFixture()
  api.setToken('expired-token')
  await assert.rejects(rejectResponse({ config: { url: '/api/v1/auth/me' }, response: { status: 401, data: {} } }))
  assert.equal(api.getToken(), null)
  assert.equal(window.location.href, '/login')
})

test('logout rejects late domain and identity list responses', async () => {
  const { domains, auth, endpoints } = storesFixture()
  const d = deferred(), i = deferred()
  endpoints.domainApi.list = () => d.promise
  endpoints.identityApi.list = () => i.promise
  const pending = [domains.fetchDomains(), domains.fetchIdentities()]
  auth.logout()
  d.resolve([{ uuid: 'private-domain' }]); i.resolve([{ uuid: 'private-identity' }])
  await Promise.all(pending)
  assert.deepEqual(domains.domains, [])
  assert.deepEqual(domains.identities, [])
  assert.equal(domains.isLoading, false)
})

test('account changes reject both stale success and failure without clearing new metadata', async () => {
  const { domains, state, endpoints } = storesFixture()
  const d = deferred(), i = deferred()
  endpoints.domainApi.list = () => d.promise
  endpoints.identityApi.list = () => i.promise
  const pending = [domains.fetchDomains(), domains.fetchIdentities()]
  state.token = 'second-account'
  endpoints.domainApi.list = async () => [{ uuid: 'new-domain' }]
  endpoints.identityApi.list = async () => [{ uuid: 'new-identity' }]
  await Promise.all([domains.fetchDomains(), domains.fetchIdentities()])
  d.resolve([{ uuid: 'old-domain' }]); i.reject(new Error('Old account failure'))
  await Promise.all(pending)
  assert.equal(domains.domains[0].uuid, 'new-domain')
  assert.equal(domains.identities[0].uuid, 'new-identity')
  assert.equal(domains.error, null)
})

test('logout invalidates pending metadata creates even when the token is reused', async () => {
  const { domains, auth, state, endpoints } = storesFixture()
  const d = deferred(), i = deferred()
  endpoints.domainApi.create = () => d.promise
  endpoints.identityApi.create = () => i.promise
  const pending = [domains.addDomain('private.test'), domains.createIdentity({ email: 'private@private.test', domainId: '1', displayName: 'Private' })]
  auth.logout()
  state.token = 'first-account'
  d.resolve({ uuid: 'old-domain' }); i.resolve({ uuid: 'old-identity' })
  await Promise.all(pending)
  assert.deepEqual(domains.domains, [])
  assert.deepEqual(domains.identities, [])
})

test('SSE errors close native retry sources and only one controlled reconnect survives', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const { api, InboxSSE } = apiFixture()
  window.setTimeout = setTimeout
  const sources = []
  const original = globalThis.EventSource
  globalThis.EventSource = class {
    listeners = new Map(); closed = false
    constructor(url) { this.url = url; sources.push(this) }
    addEventListener(name, listener) { this.listeners.set(name, listener) }
    close() { this.closed = true }
    emit(name) { this.listeners.get(name)?.({ data: JSON.stringify({ data: {} }) }) }
  }
  api.setToken('session-token')
  const stream = new InboxSSE(); let connections = 0, messages = 0, tickets = 0, cursor = '7'
  const post = api.post.bind(api)
  api.post = (...args) => { tickets++; return post(...args) }
  t.after(() => { stream.disconnect(); globalThis.EventSource = original; api.post = post })
  await stream.connect({ onConnected: () => connections++, onNewEmail: () => messages++ }, { cursor: () => cursor })
  assert.match(sources[0].url, /token=stream-ticket&cursor=7$/)
  cursor = '9'
  assert.ok(!sources[0].url.includes("session-token"))
  sources[0].emit('connected')
  sources[0].onerror(new Event('error'))
  assert.equal(sources[0].closed, true)
  sources[0].emit('connected'); sources[0].emit('new_email')
  assert.equal(connections, 1); assert.equal(messages, 0)
  t.mock.timers.tick(1000)
  for (let i = 0; i < 6; i++) await Promise.resolve()
  assert.equal(sources.length, 2)
  assert.equal(tickets, 2, 'every reconnect redeems a fresh single-use ticket')
  assert.match(sources[1].url, /cursor=9$/, 'a reconnect resumes from the latest applied cursor')
  sources[1].emit('connected')
  assert.equal(connections, 2)
  sources[1].onerror(new Event('error'))
  stream.disconnect()
  t.mock.timers.tick(30000)
  assert.equal(sources.length, 2)
})


test('two-factor login stores only the challenge until verification succeeds', async () => {
  const { auth, endpoints, state } = storesFixture()
  endpoints.authApi.login = async () => ({ requiresTwoFactor: true, challengeToken: 'one-use-challenge' })
  assert.equal(await auth.login('fixture@example.test', 'password'), false)
  assert.equal(auth.challengeToken, 'one-use-challenge')
  assert.equal(auth.isAuthenticated, false)
  assert.equal(state.token, null)
  endpoints.authApi.completeChallenge = async (challenge, code) => {
    assert.equal(challenge, 'one-use-challenge'); assert.equal(code, '123456')
    return { token: 'verified-session', user: { id: 1, email: 'fixture@example.test' } }
  }
  await auth.verifyChallenge('123456')
  assert.equal(auth.challengeToken, null)
  assert.equal(state.token, 'verified-session')
  assert.equal(auth.isAuthenticated, true)
})

test('invalid two-factor code stays on the verification form', async () => {
  const { rejectResponse } = apiFixture()
  const href = window.location.href
  await assert.rejects(rejectResponse({ config: { url: '/api/v1/auth/2fa/challenge' }, response: { status: 401, data: { message: 'Invalid verification code' } } }))
  assert.equal(window.location.href, href)
})

test('logout requests server revocation using the captured credential', () => {
  const { auth, endpoints, state } = storesFixture()
  let captured
  endpoints.authApi.logout = async token => { captured = token }
  auth.logout()
  assert.equal(captured, 'first-account')
  assert.equal(state.token, null)
})

test('disconnect discards an in-flight stream ticket after logout', async () => {
  const { api, InboxSSE } = apiFixture()
  const pending = deferred(); const originalPost = api.post.bind(api)
  api.post = () => pending.promise
  const original = globalThis.EventSource; let opened = 0
  globalThis.EventSource = class { constructor() { opened++ } }
  try {
    api.setToken('session-token')
    const stream = new InboxSSE()
    const connection = stream.connect({})
    stream.disconnect(); api.setToken(null)
    pending.resolve({ token: 'late-ticket' }); await connection
    assert.equal(opened, 0)
  } finally { api.post = originalPost; globalThis.EventSource = original }
})

test('session check keeps the token on a server error and offers a retry', async () => {
  const { auth, endpoints, state } = storesFixture()
  const saved = new Map([['token', 'first-account']])
  globalThis.localStorage = { getItem: key => saved.get(key) || null, setItem: (key, value) => saved.set(key, value), removeItem: key => saved.delete(key) }
  endpoints.authApi.me = async () => { throw Object.assign(new Error('Internal failure'), { status: 500 }) }
  await auth.checkAuth()
  assert.equal(state.token, 'first-account')
  assert.equal(auth.token, 'first-account')
  assert.equal(auth.isInitialized, true)
  assert.ok(auth.authError)
  endpoints.authApi.me = async () => ({ id: 1, email: 'fixture@example.test' })
  await auth.checkAuth()
  assert.equal(auth.isAuthenticated, true)
  assert.equal(auth.authError, null)
})

test('session check signs out on a rejected credential and clears local settings', async () => {
  for (const status of [401, 403]) {
    const { auth, endpoints, state } = storesFixture()
    const saved = new Map([['token', 'first-account']])
    globalThis.localStorage = { getItem: key => saved.get(key) || null, setItem: (key, value) => saved.set(key, value), removeItem: key => saved.delete(key) }
    globalThis.__settingsCleared = 0
    endpoints.authApi.me = async () => { throw Object.assign(new Error('Session expired'), { status }) }
    await auth.checkAuth()
    assert.equal(state.token, null)
    assert.equal(auth.token, null)
    assert.equal(auth.authError, null)
    assert.equal(globalThis.__settingsCleared, 1)
  }
})

test('an expired session keeps legacy browser-only mail rules; a user logout clears them', async () => {
  apiFixture()
  const { endpoints, auth } = storesFixture()
  localStorage.setItem('token', 'expired-token')
  endpoints.authApi.me = async () => { throw Object.assign(new Error('expired'), { status: 401 }) }
  await auth.checkAuth()
  assert.equal(auth.isAuthenticated, false)
  assert.deepEqual(globalThis.__settingsClearedWith, { keepLegacyRules: true })
  auth.logout()
  assert.deepEqual(globalThis.__settingsClearedWith, { keepLegacyRules: false })
})
