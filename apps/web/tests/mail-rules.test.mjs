import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const out = await build({ entryPoints: [fileURLToPath(new URL('../src/lib/mailRules.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs' })
const module = { exports: {} }
new Function('module', 'exports', out.outputFiles[0].text)(module, module.exports)
const { blockedSenderValue, conditionFromLegacyText, describeFilter } = module.exports

test('blocked sender input accepts an address or @domain only', () => {
  assert.equal(blockedSenderValue(' Spam@Example.COM '), 'spam@example.com')
  assert.equal(blockedSenderValue('@Example.co.uk'), '@example.co.uk')
  for (const bad of ['example.com', 'Name <a@b.com>', '@localhost', 'a@b', '']) assert.equal(blockedSenderValue(bad), null, bad)
})

test('legacy free-text rules prefill a reviewable condition', () => {
  assert.deepEqual(conditionFromLegacyText('From: *@newsletter.*'), { field: 'from', operator: 'contains', value: '@newsletter.' })
  assert.deepEqual(conditionFromLegacyText('Subject: Invoice'), { field: 'subject', operator: 'contains', value: 'Invoice' })
  assert.deepEqual(conditionFromLegacyText('Has: attachment'), { field: 'hasAttachment', operator: 'equals', value: 'true' })
  assert.deepEqual(conditionFromLegacyText('anything else'), { field: 'subject', operator: 'contains', value: 'anything else' })
})

test('filters are summarized for the settings list', () => {
  const summary = describeFilter({ conditions: [{ field: 'from', operator: 'contains', value: 'shop' }, { field: 'hasAttachment', operator: 'equals', value: 'true' }], conditionLogic: 'any', actionFolder: 'archive', actionLabels: ['Receipts'], actionStar: true, actionMarkRead: false, actionArchive: false, actionTrash: false })
  assert.equal(summary, 'From contains "shop" or Has attachment → Move to Archive, Label "Receipts", Star')
})
