import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { setActivePinia, createPinia } = require('pinia')
const webRoot = fileURLToPath(new URL('..', import.meta.url))
function evaluate(code) { const module = { exports: {} }; new Function('require', 'module', 'exports', code)(require, module, module.exports); return module.exports }

const storeBuild = await build({
  entryPoints: [`${webRoot}/src/stores/settings.ts`], bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
  plugins: [{ name: 'api-fixture', setup(builder) {
    builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ contents: 'export const api = globalThis.__settingsApi; export const inboxFiltersApi = globalThis.__settingsApi.inboxFiltersApi; export const trustedSendersApi = globalThis.__settingsApi.trustedSendersApi', loader: 'js' }))
  } }],
})

function fixture(local = {}) {
  const calls = []
  const saved = new Map([['userSettings', '{"theme":"dark"}'], ['userFilters', '[]'], ['blockedSenders', '[]'], ['token', 'session'], ...Object.entries(local)])
  globalThis.localStorage = { getItem: key => saved.get(key) ?? null, setItem: (key, value) => saved.set(key, value), removeItem: key => saved.delete(key) }
  globalThis.document = { documentElement: { classList: { add() {}, remove() {} } } }
  globalThis.window = { matchMedia: () => ({ matches: false }) }
  const responses = {}
  globalThis.__settingsApi = {
    getToken: () => 'session',
    get: async url => { calls.push(['GET', url]); return responses[url] },
    post: async (url, body) => { calls.push(['POST', url, body]); if (responses[url] instanceof Error) throw responses[url]; return responses[url] },
    put: async () => ({}), delete: async url => { calls.push(['DELETE', url]); return {} },
  }
  let nextId = 1
  const failSenders = new Set()
  globalThis.__settingsApi.inboxFiltersApi = {
    list: async kind => { calls.push(['GET', `/api/v1/inbox/filters?kind=${kind}`]); return [] },
    blockSender: async (sender, folder = 'spam') => {
      calls.push(['POST', '/api/v1/inbox/filters', { kind: 'blocked_sender', sender, folder }])
      if (failSenders.has(sender)) throw new Error('server unavailable')
      return { uuid: `u-${nextId++}`, kind: 'blocked_sender', conditions: [{ field: 'from', value: sender }] }
    },
    delete: async uuid => { calls.push(['DELETE', `/api/v1/inbox/filters/${uuid}`]) },
  }
  globalThis.__settingsApi.trustedSendersApi = { list: async () => [], add: async sender => ({ uuid: 't-1', sender }), delete: async () => {} }
  const { useSettingsStore } = evaluate(storeBuild.outputFiles[0].text)
  setActivePinia(createPinia())
  return { store: useSettingsStore(), calls, saved, responses, failSenders }
}

test('disable2FA sends the password and the code', async () => {
  const { store, calls } = fixture()
  assert.equal(await store.disable2FA('current-password', '123456'), true)
  assert.deepEqual(calls.at(-1), ['POST', '/api/v1/security/2fa/disable', { password: 'current-password', code: '123456' }])
  assert.equal(store.twoFactor.enabled, false)
})

test('2FA setup uses the security endpoints and returns backup codes once verified', async () => {
  const { store, calls, responses } = fixture()
  responses['/api/v1/security/2fa/setup'] = { secret: 'S', qrCodeUrl: 'otpauth://totp/x', qrCodeDataUrl: 'data:image/png;base64,AA', manualCode: 'SSSS' }
  responses['/api/v1/security/2fa/verify'] = { backupCodes: ['A-B-C', 'D-E-F'] }
  const setup = await store.enable2FA()
  assert.equal(setup.qrCodeDataUrl, 'data:image/png;base64,AA')
  assert.deepEqual(await store.verify2FA('654321'), ['A-B-C', 'D-E-F'])
  assert.deepEqual(calls.map(c => c[1]), ['/api/v1/security/2fa/setup', '/api/v1/security/2fa/verify'])
  assert.deepEqual(store.twoFactor, { enabled: true, backupCodesCount: 2 })
  responses['/api/v1/security/2fa/verify'] = new Error('invalid verification code')
  assert.equal(await store.verify2FA('000000'), null)
  assert.equal(store.error, 'invalid verification code')
})

test('clearLocalSettings removes the three browser-local keys and resets state', () => {
  const { store, saved } = fixture()
  assert.equal(store.settings.theme, 'dark')
  store.clearLocalSettings()
  for (const key of ['userSettings', 'userFilters', 'blockedSenders']) assert.equal(saved.has(key), false, key)
  assert.equal(saved.get('token'), 'session')
  assert.equal(store.settings.theme, 'light')
  assert.equal(store.settingsLoaded, false)
})

const legacyBlocked = JSON.stringify([{ id: '1', email: 'spam@example.com', blockedAt: '2025-01-01' }, { id: '2', email: '@junk.example' }])
const legacyFilters = JSON.stringify([{ id: 'f1', name: 'News', conditions: 'From: *@news.*', actions: 'Label: News', enabled: true }])

test('local rules are detected but never applied or uploaded silently', () => {
  const { store, calls } = fixture({ blockedSenders: legacyBlocked, userFilters: legacyFilters })
  assert.equal(store.localRules.blockedSenders.length, 2)
  assert.equal(store.localRules.filters.length, 1)
  assert.equal(calls.length, 0)
})

test('import POSTs every local blocked sender and removes the key', async () => {
  const { store, calls, saved } = fixture({ blockedSenders: legacyBlocked, userFilters: legacyFilters })
  const result = await store.importLocalMailRules()
  assert.deepEqual(result, { imported: 2, failed: [] })
  assert.deepEqual(calls.filter(c => c[0] === 'POST').map(c => c[2].sender), ['spam@example.com', '@junk.example'])
  assert.equal(saved.has('blockedSenders'), false)
  assert.equal(saved.has('userFilters'), true, 'free-text filters wait for Recreate')
  assert.equal(store.blockedSenders.length, 2)
  assert.equal(store.localRules.blockedSenders.length, 0)
})

test('a partial import failure keeps the browser key', async () => {
  const { store, saved, failSenders } = fixture({ blockedSenders: legacyBlocked })
  failSenders.add('@junk.example')
  const result = await store.importLocalMailRules()
  assert.deepEqual(result, { imported: 1, failed: ['@junk.example'] })
  assert.equal(saved.get('blockedSenders'), legacyBlocked)
  assert.match(store.rulesError, /could not be imported/)
})

test('discard clears the local rules; recreate forgets one filter', () => {
  const { store, saved } = fixture({ blockedSenders: legacyBlocked, userFilters: JSON.stringify([...JSON.parse(legacyFilters), { id: 'f2', name: 'Other', conditions: 'x', actions: 'y' }]) })
  store.forgetLocalFilter('f1')
  assert.deepEqual(JSON.parse(saved.get('userFilters')).map(f => f.id), ['f2'])
  store.discardLocalMailRules()
  assert.equal(saved.has('blockedSenders'), false)
  assert.equal(saved.has('userFilters'), false)
  assert.deepEqual(store.localRules, { filters: [], blockedSenders: [] })
})

test('fetchSessions surfaces errors instead of inventing a session', async () => {
  const { store } = fixture()
  globalThis.__settingsApi.get = async () => { throw new Error('offline') }
  await store.fetchSessions()
  assert.deepEqual(store.sessions, [])
  assert.equal(store.sessionsError, 'offline')
})

test('fetchMailRules loads filters and blocked senders separately by kind', async () => {
  const { store, calls } = fixture()
  await store.fetchMailRules()
  assert.deepEqual(calls.map(c => c[1]).sort(), ['/api/v1/inbox/filters?kind=blocked_sender', '/api/v1/inbox/filters?kind=filter'])
})
