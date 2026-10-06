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
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ contents: 'export const api = globalThis.__settingsApi', loader: 'js' }))
  } }],
})

function fixture() {
  const calls = []
  const saved = new Map([['userSettings', '{"theme":"dark"}'], ['userFilters', '[]'], ['blockedSenders', '[]'], ['token', 'session']])
  globalThis.localStorage = { getItem: key => saved.get(key) ?? null, setItem: (key, value) => saved.set(key, value), removeItem: key => saved.delete(key) }
  globalThis.document = { documentElement: { classList: { add() {}, remove() {} } } }
  globalThis.window = { matchMedia: () => ({ matches: false }) }
  const responses = {}
  globalThis.__settingsApi = {
    getToken: () => 'session',
    get: async url => { calls.push(['GET', url]); return responses[url] },
    post: async (url, body) => { calls.push(['POST', url, body]); if (responses[url] instanceof Error) throw responses[url]; return responses[url] },
    put: async () => ({}), delete: async () => ({}),
  }
  const { useSettingsStore } = evaluate(storeBuild.outputFiles[0].text)
  setActivePinia(createPinia())
  return { store: useSettingsStore(), calls, saved, responses }
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
