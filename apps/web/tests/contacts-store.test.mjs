import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { setActivePinia, createPinia } = require('pinia')
const webRoot = fileURLToPath(new URL('..', import.meta.url))
function evaluate(code) { const module = { exports: {} }; new Function('require', 'module', 'exports', code)(require, module, module.exports); return module.exports }

// Real contactApi over a recording axios stub, so the request URL is asserted.
const apiBuild = await build({
  entryPoints: [`${webRoot}/src/lib/api.ts`], bundle: true, write: false, platform: 'node', format: 'cjs',
  define: { 'import.meta.env.VITE_API_URL': "''" },
  plugins: [{ name: 'axios-fixture', setup(builder) {
    builder.onResolve({ filter: /^axios$/ }, () => ({ path: 'axios', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ contents: 'export default globalThis.__contactsAxios', loader: 'js' }))
  } }],
})
const storeBuild = await build({
  entryPoints: [`${webRoot}/src/stores/contacts.ts`], bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
  plugins: [{ name: 'api-fixture', setup(builder) {
    builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ contents: 'export const { contactApi, listApi } = globalThis.__contactsApi', loader: 'js' }))
  } }],
})

function fixture() {
  const urls = []
  globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} }
  globalThis.window = { location: { href: '/contacts' } }
  globalThis.__contactsAxios = { create: () => ({
    get: async url => { urls.push(url); return { data: { data: { contacts: [{ uuid: 'c1', email: 'deal@x.test' }], total: 1, page: 2, pageSize: 50, totalPages: 3 } } } },
    interceptors: { request: { use() {} }, response: { use() {} } },
  }) }
  const { contactApi, listApi } = evaluate(apiBuild.outputFiles[0].text)
  globalThis.__contactsApi = { contactApi, listApi }
  const { useContactsStore } = evaluate(storeBuild.outputFiles[0].text)
  setActivePinia(createPinia())
  return { store: useContactsStore(), urls }
}

test('search calls the paginated contacts list with an encoded query', async () => {
  const { store, urls } = fixture()
  await store.searchContacts(' 50% off ')
  assert.equal(urls.at(-1), '/api/v1/contacts?query=50%25%20off&page=1&pageSize=50')
  assert.equal(store.contacts[0].email, 'deal@x.test')
  assert.equal(store.totalContacts, 1)
  assert.equal(store.totalPages, 3)
})

test('paging keeps the active search and clearing it lists everything', async () => {
  const { store, urls } = fixture()
  await store.searchContacts('ann')
  await store.fetchContacts(2, 50)
  assert.equal(urls.at(-1), '/api/v1/contacts?query=ann&page=2&pageSize=50')
  await store.searchContacts('')
  assert.equal(urls.at(-1), '/api/v1/contacts?page=1&pageSize=50')
})
