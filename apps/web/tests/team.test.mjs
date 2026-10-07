import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const out = await build({ entryPoints: [fileURLToPath(new URL('../src/lib/team.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs' })
const module = { exports: {} }
new Function('module', 'exports', out.outputFiles[0].text)(module, module.exports)
const { memberAddress, visibleMembers, removedCount } = module.exports

test('a removed account whose address was reassigned never shows its placeholder', () => {
  assert.equal(memberAddress('removed+6f1c2a7e-1b2c-4d5e-8f90-0123456789ab@invalid'), 'Former account (address reassigned)')
  assert.equal(memberAddress('removed+someone@acme.test'), 'removed+someone@acme.test')
  assert.equal(memberAddress('ana@acme.test'), 'ana@acme.test')
})

test('removed members are hidden until asked for', () => {
  const members = [{ email: 'a@acme.test', status: 'active' }, { email: 'b@acme.test', status: 'disabled' }]
  assert.deepEqual(visibleMembers(members, false).map(m => m.email), ['a@acme.test'])
  assert.deepEqual(visibleMembers(members, true).map(m => m.email), ['a@acme.test', 'b@acme.test'])
  assert.equal(removedCount(members), 1)
})
