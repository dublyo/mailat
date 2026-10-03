import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import { parse, compileScript } from '@vue/compiler-sfc'

const require = createRequire(import.meta.url)
const { createSSRApp } = require('vue')
const { renderToString } = require('@vue/server-renderer')
const { createPinia, setActivePinia } = require('pinia')
const webRoot = fileURLToPath(new URL('..', import.meta.url))
const source = await readFile(`${webRoot}/src/views/Health.vue`, 'utf8')
const { descriptor } = parse(source, { filename: 'Health.vue' })
const compiled = compileScript(descriptor, { id: 'health-contract-test', inlineTemplate: true, templateOptions: { ssr: true } })
const output = await build({
  stdin: { contents: compiled.content, resolveDir: webRoot, loader: 'ts' },
  bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external',
  plugins: [{ name: 'health-api-fixture', setup(builder) {
    builder.onResolve({ filter: /^@\/stores\/health$/ }, () => ({ path: `${webRoot}/src/stores/health.ts` }))
    builder.onResolve({ filter: /^@\/lib\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
    builder.onResolve({ filter: /AppLayout\.vue$/ }, () => ({ path: 'layout', namespace: 'fixture' }))
    builder.onResolve({ filter: /^lucide-vue-next$/ }, () => ({ path: 'icons', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ contents: args.path === 'api'
      ? 'export const healthApi = { getSummary: async () => globalThis.__healthJSON, getAlerts: async () => [] }'
      : args.path === 'layout' ? "export default { setup(_, { slots }) { return () => slots.default?.() } }"
        : 'export const RefreshCw = () => null; export const Activity = RefreshCw, Cloud = RefreshCw, Mail = RefreshCw, Inbox = RefreshCw, Shield = RefreshCw, AlertTriangle = RefreshCw', loader: 'js' }))
  } }],
})
const original = JSON.parse(await readFile(new URL('./fixtures/health-summary.json', import.meta.url), 'utf8'))
async function render(summary) {
  globalThis.__healthJSON = summary
  const module = { exports: {} }
  new Function('require', 'module', 'exports', output.outputFiles[0].text)(require, module, module.exports)
  const pinia = createPinia()
  setActivePinia(pinia)
  const app = createSSRApp(module.exports.default)
  app.use(pinia)
  // The view's onMounted load does not execute during SSR. Render once to create
  // its real store, then load through the store's API contract before asserting.
  await renderToString(app)
  await pinia._s.get('health').fetchHealthSummary()
  return renderToString(createSSRApp(module.exports.default).use(pinia))
}
function card(html, label, value) {
  const escaped = label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  assert.match(html, new RegExp(`${escaped}</p><p[^>]*>${value}</p>`))
}

test('actual Health JSON zero totals render as 0 and warning action is visible', async () => {
  const html = await render(structuredClone(original))
  for (const label of ['Accepted for sending', 'Confirmed deliveries', 'Bounced', 'Complaints', 'Failed sends', 'Received · Last 30 days', 'Read · Last 30 days', 'Spam flagged', 'Virus flagged']) card(html, label, '0')
  assert.doesNotMatch(html, /Unavailable|Received today/)
  assert.match(html, /Request production access from AWS\./)
  assert.match(html, /No sending data/)
})

test('Health cards bind distinct backend totals rather than invented aliases', async () => {
  const data = structuredClone(original)
  Object.assign(data.sendingMetrics, { totalSent: 20, totalDelivered: 17, totalBounced: 2, totalComplaints: 1, totalFailed: 3 })
  Object.assign(data.receivingMetrics, { totalReceived: 13, totalRead: 9, totalSpam: 4, totalVirus: 2 })
  const html = await render(data)
  for (const [label, value] of [['Accepted for sending',20], ['Confirmed deliveries',17], ['Bounced',2], ['Complaints',1], ['Failed sends',3], ['Received · Last 30 days',13], ['Read · Last 30 days',9], ['Spam flagged',4], ['Virus flagged',2]]) card(html, label, String(value))
})

test('missing Health metrics remain unavailable instead of becoming invented zeroes', async () => {
  const data = structuredClone(original)
  delete data.receivingMetrics.totalRead
  const html = await render(data)
  card(html, 'Read · Last 30 days', 'Unavailable')
  card(html, 'Virus flagged', '0')
})
