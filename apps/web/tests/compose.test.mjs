import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const webRoot = fileURLToPath(new URL('..', import.meta.url))
const out = await build({ entryPoints: [`${webRoot}/src/lib/compose.ts`], bundle: true, write: false, platform: 'node', format: 'cjs' })
const module = { exports: {} }
new Function('module', 'exports', out.outputFiles[0].text)(module, module.exports)
const { senderAllowed, senderSuggestions, senderHint, bodyWithSignature } = module.exports

const me = { id: 1, email: 'ibrahim@vayb.dev', kind: 'personal', sendAliases: ['sales@vayb.dev'], wildcardSender: false }
const teammate = { id: 2, email: 'ceo@vayb.dev', kind: 'personal', sendAliases: ['press@vayb.dev'] }
const shared = { id: 3, email: 'team@vayb.dev', kind: 'shared', shared: true, wildcardSender: true }
const known = [me, teammate, shared]

test('senderAllowed mirrors the server send-as rule (F8)', () => {
  for (const from of ['ibrahim@vayb.dev', 'IBRAHIM@vayb.dev', 'ibrahim+news@vayb.dev', 'sales@vayb.dev', ' Sales@VAYB.dev ']) {
    assert.equal(senderAllowed(me, from, known), true, from)
  }
  // Aliases are exact: no +tag forms. Other addresses need the wildcard switch.
  for (const from of ['sales+x@vayb.dev', 'random@vayb.dev', 'ceo@vayb.dev', 'ibrahim@other.test', 'Ibrahim <ibrahim@vayb.dev>', 'ibrahim+@vayb.dev', '']) {
    assert.equal(senderAllowed(me, from, known), false, from)
  }
})

test('the wildcard switch allows free addresses only', () => {
  const wild = { ...me, wildcardSender: true }
  assert.equal(senderAllowed(wild, 'random@vayb.dev', known), true)
  assert.equal(senderAllowed(wild, 'sales@vayb.dev', known), true)
  for (const from of ['ceo@vayb.dev', 'ceo+x@vayb.dev', 'press@vayb.dev', 'press+x@vayb.dev', 'team@vayb.dev', 'team+x@vayb.dev', 'random@other.test']) {
    assert.equal(senderAllowed(wild, from, known), false, from)
  }
  // The identity itself is not "another identity", even as a separate object.
  assert.equal(senderAllowed(wild, 'ibrahim+x@vayb.dev', [{ ...me }]), true)
  // Shared identities never get the wildcard, whatever the flag says.
  assert.equal(senderAllowed(shared, 'random@vayb.dev', known), false)
  assert.equal(senderAllowed(shared, 'team+x@vayb.dev', known), true)
})

test('From suggestions and hints', () => {
  assert.deepEqual(senderSuggestions(me), ['ibrahim@vayb.dev', 'sales@vayb.dev'])
  assert.deepEqual(senderSuggestions(undefined), [])
  assert.match(senderHint(me), /ibrahim\+news@vayb\.dev.*send-as/)
  assert.doesNotMatch(senderHint({ ...me, sendAliases: [] }), /send-as/)
  assert.match(senderHint({ ...me, wildcardSender: true }), /Wildcard sending is on.*@vayb\.dev/)
  assert.doesNotMatch(senderHint(shared), /Wildcard/)
})

test('signature goes below a blank line and above the quote', () => {
  assert.equal(bodyWithSignature(''), '<p></p>')
  assert.equal(bodyWithSignature('<p>Ibrahim</p>'), '<p></p><p></p><p>Ibrahim</p>')
  assert.equal(bodyWithSignature('<p>Ibrahim</p>', '<p>On …</p><blockquote>x</blockquote>'), '<p></p><p></p><p>Ibrahim</p><p>On …</p><blockquote>x</blockquote>')
})

test('ComposeModal wires the datalist, hint and signature swap', async () => {
  const source = await readFile(`${webRoot}/src/components/inbox/ComposeModal.vue`, 'utf8')
  assert.match(source, /list="compose-from-options"/)
  assert.match(source, /<datalist id="compose-from-options"><option v-for="address in fromSuggestions"/)
  assert.match(source, /restrictAlias = computed\(\(\) => !isOrgAdmin\(authStore\.user\)\)/)
  // Only an untouched body gets the new identity's signature; drafts keep theirs.
  assert.match(source, /composeMode !== 'draft' && html\.value === pristineHtml\.value\) setBody\(bodyWithSignature\(signatureHtml\(selectedIdentity\.value\)/)
  assert.ok(!source.includes('memberAliasAllowed'), 'compose uses senderAllowed')
})
