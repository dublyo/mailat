import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'
import { readFileSync } from 'node:fs'

function evaluate(code) { const module = { exports: {} }; new Function('module', 'exports', code)(module, module.exports); return module.exports }
const lib = evaluate((await build({ entryPoints: [fileURLToPath(new URL('../src/lib/loginPin.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs' })).outputFiles[0].text)
const { pinProblem, pinDigits, deviceTokenFor, rememberDevice, forgetDevice, pinErrorMessage } = lib

function memoryStorage() {
  const saved = new Map()
  return { getItem: key => saved.get(key) ?? null, setItem: (key, value) => saved.set(key, String(value)), saved }
}

test('PIN rules match the server: 4 digits, no repeats, no runs', () => {
  for (const pin of ['2580', '1357', '0102', '7890', '1123']) assert.equal(pinProblem(pin), '', pin)
  for (const pin of ['', '123', '12345', '12a4', '0000', '9999', '1234', '0123', '6789', '4321', '3210']) assert.notEqual(pinProblem(pin), '', pin)
  assert.match(pinProblem('1111'), /same digit/)
  assert.match(pinProblem('9876'), /runs/)
})

test('pasted text keeps at most four digits', () => {
  assert.deepEqual(pinDigits(' 2 5-8 0 9'), ['2', '5', '8', '0'])
  assert.deepEqual(pinDigits('abc'), [])
})

test('remembered devices are keyed by the lowercased email and expire', () => {
  const storage = memoryStorage()
  const now = Date.parse('2026-11-21T00:00:00Z')
  rememberDevice('Owner@Example.test ', 'token-1', '2026-12-21T00:00:00Z', storage, now)
  assert.equal(deviceTokenFor('owner@example.test', storage, now), 'token-1')
  assert.equal(deviceTokenFor('other@example.test', storage, now), undefined)
  assert.equal(deviceTokenFor('owner@example.test', storage, Date.parse('2026-12-22T00:00:00Z')), undefined)
  rememberDevice('other@example.test', 'token-2', '2026-12-01T00:00:00Z', storage, now)
  // Expired entries are pruned on the next write.
  rememberDevice('third@example.test', 'token-3', '2027-01-01T00:00:00Z', storage, Date.parse('2026-12-25T00:00:00Z'))
  assert.deepEqual(Object.keys(JSON.parse(storage.saved.get('mailat.trustedDevices'))), ['third@example.test'])
  forgetDevice('THIRD@example.test', storage)
  assert.equal(deviceTokenFor('third@example.test', storage), undefined)
})

test('broken or blocked storage never breaks sign-in', () => {
  const broken = { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') } }
  assert.equal(deviceTokenFor('a@b.test', broken), undefined)
  assert.doesNotThrow(() => rememberDevice('a@b.test', 't', undefined, broken))
  assert.equal(deviceTokenFor('a@b.test', { getItem: () => 'not json', setItem() {} }), undefined)
})

test('PIN errors explain attempts, locks and expiry', () => {
  assert.equal(pinErrorMessage({ status: 401, message: 'Wrong PIN. 2 attempts left.', data: { attemptsLeft: 2 } }), 'Wrong PIN. 2 attempts left.')
  assert.match(pinErrorMessage({ status: 401, message: 'Wrong PIN.', data: { attemptsLeft: 0 } }), /Sign in again/)
  assert.match(pinErrorMessage({ status: 429, message: 'Too many wrong PIN attempts. Try again later.' }), /Too many/)
  assert.match(pinErrorMessage({ status: 401, message: 'This sign-in step expired. Sign in again.' }), /expired/)
})

test('the PIN boxes are numeric one-time-code inputs with paste and Backspace handling', () => {
  const source = readFileSync(new URL('../src/components/common/PinInput.vue', import.meta.url), 'utf8')
  assert.match(source, /inputmode="numeric"/)
  assert.match(source, /autocomplete="one-time-code"/)
  assert.match(source, /@paste=/)
  assert.match(source, /event\.key === 'Backspace'/)
  const login = readFileSync(new URL('../src/views/Login.vue', import.meta.url), 'utf8')
  assert.match(login, /Remember this device for 30 days/)
  assert.match(login, /const trustDevice = ref\(true\)/)
})
