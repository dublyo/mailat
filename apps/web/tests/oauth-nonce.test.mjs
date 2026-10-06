import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const out = await build({ entryPoints: [fileURLToPath(new URL('../src/lib/oauthNonce.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs' })
const module = { exports: {} }
new Function('module', 'exports', out.outputFiles[0].text)(module, module.exports)
const { createOAuthNonce, consumeOAuthNonce } = module.exports

function memoryStorage() {
  const saved = new Map()
  return { saved, getItem: k => saved.get(k) ?? null, setItem: (k, v) => saved.set(k, String(v)), removeItem: k => saved.delete(k) }
}

test('a fragment is adopted only with the nonce this tab stored, once', () => {
  const storage = memoryStorage()
  const nonce = createOAuthNonce(storage)
  assert.match(nonce, /^[0-9a-f]{48}$/)
  assert.equal(consumeOAuthNonce(nonce, storage), true)
  assert.equal(consumeOAuthNonce(nonce, storage), false, 'single use')
})

test('a planted session link without a matching nonce is refused', () => {
  const storage = memoryStorage()
  assert.equal(consumeOAuthNonce(null, storage), false, 'no flow started')
  assert.equal(consumeOAuthNonce('attacker-chosen-value-123', storage), false)
  createOAuthNonce(storage)
  assert.equal(consumeOAuthNonce('attacker-chosen-value-123', storage), false)
  assert.equal(storage.saved.size, 0, 'a failed check still clears the nonce')
  assert.equal(consumeOAuthNonce('x', null), false, 'no storage')
})
