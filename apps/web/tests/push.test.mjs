import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const out = await build({ entryPoints: [fileURLToPath(new URL('../src/lib/push.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs' })
const module = { exports: {} }
new Function('module', 'exports', out.outputFiles[0].text)(module, module.exports)
const { base64UrlToBytes, bytesToBase64Url, subscriptionBody, pushState, deviceLabel } = module.exports

// A 65-byte uncompressed P-256 point as the server hands it out (raw base64url).
const publicKey = 'BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U'

test('keys round-trip through unpadded base64url', () => {
  const bytes = base64UrlToBytes(publicKey)
  assert.equal(bytes.length, 65)
  assert.equal(bytes[0], 4)
  assert.equal(bytesToBase64Url(bytes), publicKey)
  assert.equal(bytesToBase64Url(new Uint8Array([251, 255, 191]).buffer), '-_-_')
  assert.equal(bytesToBase64Url(null), '')
})

test('the subscribe body carries base64url keys and a clipped device name', () => {
  const keys = { p256dh: base64UrlToBytes(publicKey).buffer, auth: new Uint8Array(16).fill(255).buffer }
  const body = subscriptionBody({ endpoint: 'https://fcm.googleapis.com/fcm/send/x', getKey: name => keys[name] }, 'd'.repeat(300))
  assert.equal(body.p256dhKey, publicKey)
  assert.equal(body.authKey, '_____________________w')
  assert.ok(!/[+/=]/.test(body.p256dhKey + body.authKey))
  assert.equal(body.deviceName.length, 100)
})

test('push state reflects server config, permission, and key rotation', () => {
  const subscription = { endpoint: 'https://push.example/1', getKey: () => null, options: { applicationServerKey: base64UrlToBytes(publicKey).buffer } }
  const base = { supported: true, enabled: true, publicKey, permission: 'granted', subscription, serverEndpoints: ['https://push.example/1'] }
  assert.equal(pushState(base), 'on')
  assert.equal(pushState({ ...base, enabled: false, publicKey: '' }), 'disabled')
  assert.equal(pushState({ ...base, supported: false }), 'unsupported')
  assert.equal(pushState({ ...base, permission: 'denied' }), 'denied')
  assert.equal(pushState({ ...base, subscription: null }), 'off')
  assert.equal(pushState({ ...base, publicKey: 'BOther' + publicKey.slice(6) }), 'stale', 'a rotated server key needs re-enabling')
  assert.equal(pushState({ ...base, serverEndpoints: [] }), 'stale', 'a subscription the server deactivated needs re-enabling')
})

test('device labels name the browser and platform', () => {
  assert.equal(deviceLabel('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36'), 'Chrome on macOS')
  assert.equal(deviceLabel('Mozilla/5.0 (Windows NT 10.0; rv:131.0) Gecko/20100101 Firefox/131.0'), 'Firefox on Windows')
  assert.equal(deviceLabel(''), 'Browser')
})
