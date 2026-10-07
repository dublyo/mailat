import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const out = await build({ entryPoints: [fileURLToPath(new URL('../src/lib/forwards.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs' })
const module = { exports: {} }
new Function('module', 'exports', out.outputFiles[0].text)(module, module.exports)
const { forwardActions } = module.exports

test('a suspended forward can be verified again; other states keep their actions', () => {
  const actions = (status, verified = true) => forwardActions({ status, verified }).map(a => `${a.action}:${a.label}`)
  assert.deepEqual(actions('suspended'), ['resend:Re-verify'])
  assert.deepEqual(actions('pending', false), ['resend:Resend link'])
  assert.deepEqual(actions('active'), ['pause:Pause'])
  assert.deepEqual(actions('paused'), ['resume:Resume'])
  assert.deepEqual(actions('paused', false), [])
})
