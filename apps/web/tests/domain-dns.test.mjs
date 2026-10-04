import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const output = await build({ entryPoints: [fileURLToPath(new URL('../src/lib/domainDns.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs' })
const module = { exports: {} }
new Function('require', 'module', 'exports', output.outputFiles[0].text)(require, module, module.exports)
const { sendingDNSRecords, dnsResultStatus, dnsExportRecords } = module.exports
const record = (recordType, hostname, value) => ({ recordType, hostname, value })

test('sending previews exclude legacy root MX and SPF while retaining MAIL FROM and ownership records', () => {
  const bounceMX = record('MX', 'bounce.example.test', '10 feedback-smtp.us-east-2.amazonses.com')
  const bounceSPF = record('TXT', 'bounce.example.test', 'v=spf1 include:amazonses.com ~all')
  const ownership = record('TXT', '_amazonses.example.test', 'proof')
  const domain = { name: 'example.test', emailProvider: 'ses', dnsRecords: [record('MX', 'EXAMPLE.TEST.', '10 inbound-smtp.us-east-2.amazonaws.com'), record('MX', '@', '10 other-provider.test'), record('MX', '', '10 legacy.test'), record('TXT', '@', 'V=SPF1 include:legacy.test ~all'), bounceMX, bounceSPF, ownership] }
  assert.deepEqual(sendingDNSRecords(domain), [bounceMX, bounceSPF, ownership])
  assert.equal(domain.dnsRecords.length, 7)
})

test('SES sending previews and imports exclude DMARC policies that need a current-policy check', () => {
  const dkim = record('CNAME', 'token._domainkey.example.test', 'token.dkim.amazonses.com')
  const dmarc = record('TXT', '_dmarc.example.test', 'v=DMARC1; p=quarantine;')
  const domain = { name: 'example.test', emailProvider: 'ses', dnsRecords: [dkim, dmarc] }
  assert.deepEqual(sendingDNSRecords(domain), [dkim])
  assert.equal(domain.dnsRecords[1].value, 'v=DMARC1; p=quarantine;')
})

test('all bulk exports omit DMARC owners even when their values are delegated, invalid, or legacy', () => {
  const ownership = record('TXT', '_verification.example.test', 'proof')
  const records = [
    ownership,
    record('TXT', '_DMARC.EXAMPLE.TEST.', 'invalid-policy'),
    record('CNAME', '_dmarc', 'policy.example.net'),
    record('TXT', 'legacy-owner.example.test', 'V=DMARC1; p=none'),
  ]
  assert.deepEqual(dnsExportRecords(records), [ownership])
  assert.equal(records.length, 4)
})

test('a separately configured receiving subdomain keeps its own root MX out of sending setup', () => {
  const bounce = record('MX', 'bounce.inbox.example.test', '10 feedback-smtp.us-east-2.amazonses.com')
  assert.deepEqual(sendingDNSRecords({ domain: 'inbox.example.test', emailProvider: 'ses', dnsRecords: [{ type: 'MX', name: 'inbox.example.test.', value: '10 inbound-smtp.us-east-2.amazonaws.com' }, bounce] }), [bounce])
  assert.deepEqual(sendingDNSRecords(), [])
})

test('Cloudflare preservation and policy skips are distinct from failures', () => {
  for (const status of ['created', 'preserved', 'conflict', 'skipped', 'failed']) assert.equal(dnsResultStatus({ status }), status)
  assert.equal(dnsResultStatus({ success: false, skipped: true }), 'skipped')
  assert.equal(dnsResultStatus({ success: false }), 'failed')
  assert.equal(dnsResultStatus({ success: true }), 'created')
})

test('manual SMTP records retain their routing and policy instructions', () => {
  const records = [record('MX', '@', '10 mail.example.test'), record('TXT', '@', 'v=spf1 mx ~all'), record('TXT', '_dmarc.example.test', 'v=DMARC1; p=quarantine;')]
  assert.deepEqual(sendingDNSRecords({ name: 'example.test', emailProvider: 'smtp', dnsRecords: records }), records)
  assert.deepEqual(dnsExportRecords(sendingDNSRecords({ name: 'example.test', emailProvider: 'smtp', dnsRecords: records })), records.slice(0, 2))
})
