import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const webRoot = fileURLToPath(new URL('..', import.meta.url))

// The server ceremonies are off by default (WEBAUTHN_ENABLED) and do not verify
// signatures yet, so Settings offers no security key button.
test('Settings has no security key / passkey option', async () => {
  const source = await readFile(`${webRoot}/src/views/Settings.vue`, 'utf8')
  assert.doesNotMatch(source, />\s*Add Key\s*</)
  assert.doesNotMatch(source, /<p[^>]*>[^<]*(Security Keys|Passkey)/i)
  assert.doesNotMatch(source, /webauthn\/register/)
})
