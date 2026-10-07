import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'
import { readFileSync } from 'node:fs'

const out = await build({ entryPoints: [fileURLToPath(new URL('../src/lib/mailboxes.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs' })
const module = { exports: {} }
new Function('module', 'exports', out.outputFiles[0].text)(module, module.exports)
const { checkMailboxCsv, parseCsvRecords, localPartError, passwordError, onReceivingDomain, mailboxImportTemplate, importSummary, overviewRows, mailboxStatusLabel } = module.exports

const check = (text, domain = 'vayb.dev') => checkMailboxCsv(text, domain)
const results = text => check(text).rows.map(r => [r.line, r.address, r.result, r.message ?? ''])

test('the template passes the check', () => {
  const result = check(mailboxImportTemplate())
  assert.deepEqual(result.rows.map(r => [r.address, r.result]), [['ana@vayb.dev', 'ok'], ['billing@vayb.dev', 'ok']])
})

test('file-level problems reject the whole file, like the server', () => {
  assert.match(check('').error, /empty/)
  assert.match(check('local_part,name,phone\nana,Ana,1').error, /Unknown column "phone"/)
  assert.match(check('LOCAL_PART,Name,name\nana,Ana,Ana').error, /appears twice/)
  assert.match(check('name,password\nAna,secret-pass').error, /needs local_part/)
  assert.match(check('local_part,name\n').error, /no mailbox rows/)
  assert.match(check('local_part,name\nana,"Ana').error, /not valid CSV/)
  assert.match(check('local_part,name,password\nana,Ana').error, /wrong number of fields/)
  const many = 'local_part,name,password\n' + Array.from({ length: 201 }, (_, i) => `u${i},User ${i},password${i}`).join('\n')
  assert.match(check(many).error, /at most 200/)
})

test('header names are case-insensitive and a BOM is allowed', () => {
  assert.deepEqual(results('﻿Local_Part, NAME ,Password\r\nAna,Ana Lopez,long-enough\r\n'), [[2, 'ana@vayb.dev', 'ok', '']])
})

test('row errors mirror the server rules and keep their file line', () => {
  const csv = [
    'address,local_part,name,invite_email,password,may_send,may_receive',
    'ana@vayb.dev,,Ana,ana@gmail.com,,,',
    'bob@other.dev,,Bob,,long-enough,,',
    'carl@vayb.dev,dave,Carl,,long-enough,,',
    ',ed+news,Ed,,long-enough,,',
    ',fay,Fay,fay@gmail.com,long-enough,,',
    ',gus,Gus,,,,',
    ',hal,Hal,,short,,',
    ',ivy,Ivy,ivy@vayb.dev,,,',
    ',jo,J,,long-enough,,',
    ',kim,Kim,,long-enough,yes,',
    '',
    ',ANA,Ana again,,long-enough,FALSE,true',
    ',"multi",Multi,,"pass',
    'word",,',
  ].join('\n')
  assert.deepEqual(results(csv), [
    [2, 'ana@vayb.dev', 'ok', ''],
    [3, 'bob@other.dev', 'error', 'The address must be on vayb.dev'],
    [4, 'carl@vayb.dev', 'error', 'local_part and address disagree'],
    [5, 'ed+news', 'error', "The address cannot contain '+'"],
    [6, 'fay@vayb.dev', 'error', 'Give exactly one of invite_email or password'],
    [7, 'gus@vayb.dev', 'error', 'Give exactly one of invite_email or password'],
    [8, 'hal@vayb.dev', 'error', 'password must be 8 to 72 bytes long'],
    [9, 'ivy@vayb.dev', 'error', 'invite_email must be outside the new mailbox'],
    [10, 'jo@vayb.dev', 'error', 'name must be 2 to 255 characters'],
    [11, 'kim@vayb.dev', 'error', 'may_send must be true or false'],
    [13, 'ana@vayb.dev', 'error', 'Duplicate of line 2'],
    [14, 'multi@vayb.dev', 'ok', ''],
  ])
})

test('quoted fields keep commas, quotes and line breaks', () => {
  assert.deepEqual(parseCsvRecords('a,"b,c","d ""q"""\r\n"x\ny",z,\n'), [
    { line: 1, fields: ['a', 'b,c', 'd "q"'] },
    { line: 2, fields: ['x\ny', 'z', ''] },
  ])
  assert.throws(() => parseCsvRecords('a,b"c"'), /bare "/)
})

test('local parts, passwords and receiving-domain warnings', () => {
  assert.equal(localPartError('ibrahim'), '')
  assert.equal(localPartError('first.last'), '')
  assert.match(localPartError('a+b'), /'\+'/)
  assert.match(localPartError('.lead'), /valid local part/)
  assert.match(localPartError('x'.repeat(65)), /valid local part/)
  assert.match(localPartError('has space'), /valid local part/)
  assert.equal(passwordError('12345678'), '')
  assert.notEqual(passwordError('1234567'), '')
  assert.notEqual(passwordError('é'.repeat(37)), '', '74 bytes is too long even at 37 characters')
  const domains = [{ name: 'vayb.dev', receivingEnabled: true }, { name: 'send-only.dev', receivingEnabled: false }]
  assert.equal(onReceivingDomain('me@VAYB.dev', domains), true)
  assert.equal(onReceivingDomain('me@send-only.dev', domains), false)
  assert.equal(onReceivingDomain('me@gmail.com', domains), false)
})

test('summaries, the overview table and status labels', () => {
  assert.deepEqual(importSummary([{ result: 'created' }, { result: 'error' }, { result: 'created' }]), { ok: 0, created: 2, errors: 1 })
  const rows = overviewRows({ maySend: true, mayReceive: false, wildcardSender: false, isCatchAll: true, forwardsActive: false, autoReplyActive: true, twoFactor: true })
  assert.deepEqual(rows.map(r => r.label), ['May send', 'May receive', 'Wildcard sender', 'Catch-all', 'Forwarding active', 'Auto-reply active', '2FA'])
  assert.deepEqual(rows.map(r => r.value), [true, false, false, true, false, true, true])
  assert.equal(mailboxStatusLabel.invite_expired, 'Invite expired')
})

test('the admin pages only ever post the CSV, never keep it, and send-as uses a fixed suffix', () => {
  const list = readFileSync(new URL('../src/views/DomainMailboxes.vue', import.meta.url), 'utf8')
  assert.match(list, /importText\.value = '' \/\/ may hold passwords/)
  assert.doesNotMatch(list, /localStorage|sessionStorage|console\./)
  assert.match(list, /:disabled="!canCommit"/)
  const detail = readFileSync(new URL('../src/views/MailboxDetail.vue', import.meta.url), 'utf8')
  assert.match(detail, /@\{\{ domainName \}\}/)
  for (const action of ['resendInvite', 'setPassword', 'sendResetLink', 'resetTwoFactor', 'suspend', 'reactivate', 'remove', 'addAlias', 'deleteAlias']) {
    assert.match(detail, new RegExp(`mailboxAdminApi\\.${action}\\(`), action)
  }
  const domains = readFileSync(new URL('../src/views/Domains.vue', import.meta.url), 'utf8')
  assert.match(domains, /Verify the domain with SES first/)
})
