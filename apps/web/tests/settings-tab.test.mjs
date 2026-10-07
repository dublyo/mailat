import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const webRoot = fileURLToPath(new URL('..', import.meta.url))
function evaluate(code) { const module = { exports: {} }; new Function('module', 'exports', 'require', code)(module, module.exports, () => { throw new Error('unbundled import') }); return module.exports }

const { useRequestedTab, vue } = evaluate((await build({
  stdin: { contents: `export { useRequestedTab } from './src/lib/settingsTab'; export * as vue from 'vue'`, resolveDir: webRoot, loader: 'ts' },
  bundle: true, write: false, platform: 'node', format: 'cjs',
  define: { 'process.env.NODE_ENV': '"production"', __VUE_OPTIONS_API__: 'true', __VUE_PROD_DEVTOOLS__: 'false', __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: 'false' },
})).outputFiles[0].text)

test('a ?tab= deep link opens a tab that appears after identities load', async () => {
  const visible = vue.ref(['general', 'signature'])
  const active = useRequestedTab('shared', visible, 'general')
  assert.equal(active.value, 'general')
  visible.value = ['general', 'signature', 'shared']
  await vue.nextTick()
  assert.equal(active.value, 'shared')
})

test('a visible deep-linked tab opens at once; unknown tabs fall back', () => {
  assert.equal(useRequestedTab('security', vue.ref(['general', 'security']), 'general').value, 'security')
  assert.equal(useRequestedTab('team', vue.ref(['general']), 'general').value, 'general')
  assert.equal(useRequestedTab(undefined, vue.ref(['general']), 'general').value, 'general')
})

test('a tab chosen before the deep-linked one appears wins', async () => {
  const visible = vue.ref(['general', 'security'])
  const active = useRequestedTab('shared', visible, 'general')
  active.value = 'security'
  await vue.nextTick()
  visible.value = ['general', 'security', 'shared']
  await vue.nextTick()
  assert.equal(active.value, 'security')
})
