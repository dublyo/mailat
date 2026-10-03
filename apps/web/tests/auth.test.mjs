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
    builder.onResolve({ filter: /^\.\/(receivedInbox|inbox)$/ }, () => ({ path: 'other-stores', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ contents: args.path === 'api'
      ? 'export const { api, authApi, domainApi, identityApi, receivedInboxApi } = globalThis.__authFixture'
      : 'export const useReceivedInboxStore = () => ({ reset() {} }); export const useInboxStore = () => ({ closeCompose() {} })', loader: 'js' }))
  } }],
})
function apiFixture() {
  let rejectResponse
  const saved = new Map()
  globalThis.localStorage = { getItem: key => saved.get(key) || null, setItem: (key, value) => saved.set(key, value), removeItem: key => saved.delete(key) }
  globalThis.window = { location: { href: '/login?redirect=%2Freceived%3Ffolder%3Dsent' } }
  globalThis.__authAxios = { create: () => ({ interceptors: { request: { use() {} }, response: { use(_accept, reject) { rejectResponse = reject } } } }) }
  const { api, InboxSSE } = evaluate(apiBuild.outputFiles[0].text)
  return { api, rejectResponse, InboxSSE }
}
function storesFixture() {
  const state = { token: 'first-account' }
  const endpoints = {
    api: { getToken: () => state.token, setToken: value => { state.token = value } },
    authApi: {}, domainApi: { list: async () => [] }, identityApi: { list: async () => [] }, receivedInboxApi: {},
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

test('SSE errors close native retry sources and only one controlled reconnect survives', t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const { api, InboxSSE } = apiFixture()
  window.setTimeout = setTimeout
  const sources = []
  const original = globalThis.EventSource
  globalThis.EventSource = class {
    listeners = new Map(); closed = false
    constructor() { sources.push(this) }
    addEventListener(name, listener) { this.listeners.set(name, listener) }
    close() { this.closed = true }
    emit(name) { this.listeners.get(name)?.({ data: JSON.stringify({ data: {} }) }) }
  }
  api.setToken('session-token')
  const stream = new InboxSSE(); let connections = 0, messages = 0
  t.after(() => { stream.disconnect(); globalThis.EventSource = original })
  stream.connect({ onConnected: () => connections++, onNewEmail: () => messages++ })
  sources[0].emit('connected')
  sources[0].onerror(new Event('error'))
  assert.equal(sources[0].closed, true)
  sources[0].emit('connected'); sources[0].emit('new_email')
  assert.equal(connections, 1); assert.equal(messages, 0)
  t.mock.timers.tick(1000)
  assert.equal(sources.length, 2)
  sources[1].emit('connected')
  assert.equal(connections, 2)
  sources[1].onerror(new Event('error'))
  stream.disconnect()
  t.mock.timers.tick(30000)
  assert.equal(sources.length, 2)
})
