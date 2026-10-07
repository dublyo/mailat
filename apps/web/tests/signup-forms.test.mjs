import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const webRoot = fileURLToPath(new URL('..', import.meta.url))
const out = await build({
  entryPoints: [`${webRoot}/src/lib/signupForms.ts`], bundle: true, write: false, platform: 'node', format: 'cjs',
  plugins: [{ name: 'api-fixture', setup(builder) {
    builder.onResolve({ filter: /^\.\/api$/ }, () => ({ path: 'api', namespace: 'fixture' }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, () => ({ contents: 'export const api = globalThis.__signupApi', loader: 'js' }))
  } }],
})
const calls = []
globalThis.__signupApi = { delete: async url => { calls.push(['DELETE', url]); return null } }
const module = { exports: {} }
new Function('module', 'exports', out.outputFiles[0].text)(module, module.exports)
const { signupFormsApi, deleteFormPrompt } = module.exports

test('remove sends DELETE for the form uuid', async () => {
  await signupFormsApi.remove('3f2b8c1e-9d4a-4b7e-8f60-1a2b3c4d5e6f')
  assert.deepEqual(calls, [['DELETE', '/api/v1/signup-forms/3f2b8c1e-9d4a-4b7e-8f60-1a2b3c4d5e6f']])
})

test('the delete prompt names the form and what is kept', () => {
  const live = deleteFormPrompt({ name: 'Blog newsletter', published: true })
  assert.match(live, /^Delete "Blog newsletter"\?/)
  assert.match(live, /hosted page, website embeds and outstanding confirmation links stop working/)
  assert.match(live, /signup history is removed/)
  assert.match(live, /keep their subscription and consent records/)
  assert.doesNotMatch(deleteFormPrompt({ name: 'Draft', published: false }), /hosted page/)
})

test('each form card offers Delete behind a confirmation', () => {
  const view = readFileSync(`${webRoot}/src/views/SignupForms.vue`, 'utf8')
  assert.match(view, /if \(!confirm\(deleteFormPrompt\(f\)\)\) return/)
  assert.match(view, /signupFormsApi\.remove\(f\.uuid\)/)
  assert.match(view, /@click="remove\(form\)">Delete<\/Button>/)
})
