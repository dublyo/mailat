import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const out = await build({ entryPoints: [fileURLToPath(new URL('../src/lib/csv.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs' })
const module = { exports: {} }
new Function('module', 'exports', out.outputFiles[0].text)(module, module.exports)
const { toCsv, csvField, CSV_MIME } = module.exports

test('every field is quoted and embedded quotes are doubled', () => {
  assert.equal(csvField('plain'), '"plain"')
  assert.equal(csvField('say "hi"'), '"say ""hi"""')
  assert.equal(csvField('a,b\nc'), '"a,b\nc"')
  assert.equal(csvField(null), '""')
  assert.equal(csvField(undefined), '""')
})

test('formula-looking values are neutralized with a leading apostrophe', () => {
  for (const value of ['=1+1', '+1', '-2', '@SUM(A1)', '\tx', '\rx', '=HYPERLINK("http://evil","x")']) {
    assert.ok(csvField(value).startsWith(`"'`), value)
  }
  assert.equal(csvField('=HYPERLINK("u")'), `"'=HYPERLINK(""u"")"`)
  assert.equal(csvField('a=1'), '"a=1"')
  assert.equal(csvField('user@example.com'), '"user@example.com"')
})

test('document has BOM, CRLF line endings and a UTF-8 MIME type', () => {
  const csv = toCsv([['a@x.test', 'Zoë'], ['b@x.test', '-1']], ['email', 'firstName'])
  assert.ok(csv.startsWith('﻿'))
  assert.equal(csv.slice(1), '"email","firstName"\r\n"a@x.test","Zoë"\r\n"b@x.test","\'-1"\r\n')
  assert.equal(CSV_MIME, 'text/csv;charset=utf-8')
})
