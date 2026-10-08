// Mailbox users (admin pages): display rules and a CSV pre-check that mirrors
// the server's import parser. The server re-checks everything.

export type MailboxStatus = 'active' | 'invited' | 'invite_expired' | 'suspended' | 'removed'

export const mailboxStatusLabel: Record<MailboxStatus, string> = {
  active: 'Active', invited: 'Invited', invite_expired: 'Invite expired', suspended: 'Suspended', removed: 'Removed',
}
export const mailboxStatusBadge: Record<MailboxStatus, 'success' | 'info' | 'warning' | 'error' | 'default'> = {
  active: 'success', invited: 'info', invite_expired: 'warning', suspended: 'error', removed: 'default',
}

// RFC 5322 dot-atom without '+', which stays reserved for local+tag addresses.
const LOCAL_PART = /^[a-z0-9!#$%&'*/=?^_`{|}~-]+(\.[a-z0-9!#$%&'*/=?^_`{|}~-]+)*$/
const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

/** Why a local part cannot be used, or '' when it is fine. */
export function localPartError(value: string): string {
  const lp = value.trim().toLowerCase()
  if (lp.includes('+')) return "The address cannot contain '+'"
  if (lp.length < 1 || lp.length > 64 || !LOCAL_PART.test(lp)) return 'Enter a valid local part (1-64 characters, before the @)'
  return ''
}

export function nameError(value: string): string {
  const name = value.trim()
  const n = [...name].length
  return n < 2 || n > 255 || /[\r\n]/.test(name) ? 'name must be 2 to 255 characters' : ''
}

const byteLength = (s: string) => new TextEncoder().encode(s).length
export const passwordError = (pw: string) => byteLength(pw) < 8 || byteLength(pw) > 72 ? 'password must be 8 to 72 bytes long' : ''

/** True when an invite email lands on one of the org's own receiving domains. */
export function onReceivingDomain(email: string, domains: { name: string; receivingEnabled: boolean }[]) {
  const domain = email.trim().toLowerCase().split('@')[1]
  return !!domain && domains.some(d => d.receivingEnabled && d.name.toLowerCase() === domain)
}

export const MAILBOX_IMPORT_COLUMNS = ['local_part', 'address', 'name', 'invite_email', 'password', 'may_send', 'may_receive']
export const MAILBOX_IMPORT_MAX_ROWS = 200
export const MAILBOX_IMPORT_MAX_BYTES = 1 << 20

export function mailboxImportTemplate() {
  return 'local_part,name,invite_email,password,may_send,may_receive\r\n' +
    'ana,Ana Lopez,ana.personal@example.com,,true,true\r\n' +
    'billing,Billing Desk,,choose-a-strong-password,true,false\r\n'
}

/** RFC 4180 records with the line each starts on (quoted fields, doubled quotes, CRLF or LF). Blank lines are skipped. Throws on a stray quote. */
export function parseCsvRecords(text: string): { line: number; fields: string[] }[] {
  const records: { line: number; fields: string[] }[] = []
  let field = '', fields: string[] = [], quoted = false, i = 0, line = 1, start = 1, touched = false
  const endRecord = () => {
    fields.push(field)
    if (touched || fields.length > 1 || field !== '') records.push({ line: start, fields })
    field = ''; fields = []; touched = false
  }
  while (i < text.length) {
    const c = text[i]
    if (!touched && !fields.length && field === '') start = line
    if (quoted) {
      if (c === '"' && text[i + 1] === '"') { field += '"'; i += 2; continue }
      if (c === '"') { quoted = false; i++; continue }
      if (c === '\n') line++
      field += c; i++; continue
    }
    if (c === '"') {
      if (field !== '') throw new Error(`bare " in a field on line ${line}`)
      quoted = true; touched = true; i++; continue
    }
    if (c === ',') { fields.push(field); field = ''; i++; continue }
    if (c === '\r' && text[i + 1] === '\n') { endRecord(); line++; i += 2; continue }
    if (c === '\n') { endRecord(); line++; i++; continue }
    field += c; i++
  }
  if (quoted) throw new Error('a quoted field is not closed')
  if (field !== '' || fields.length || touched) endRecord()
  return records
}

export interface ImportPreviewRow { line: number; address: string; result: 'ok' | 'error'; message?: string }

/**
 * Checks a CSV file the way the server does before it touches the database:
 * a bad file returns { error }, bad rows are reported per row. Database
 * checks (address in use, open invites) only happen in the server's dry run.
 */
export function checkMailboxCsv(raw: string, domain: string): { error: string } | { rows: ImportPreviewRow[] } {
  const text = raw.replace(/^﻿/, '')
  if (byteLength(text) > MAILBOX_IMPORT_MAX_BYTES) return { error: 'The CSV file is larger than 1 MiB' }
  let records: { line: number; fields: string[] }[]
  try { records = parseCsvRecords(text) } catch (e) { return { error: `The file is not valid CSV: ${(e as Error).message}` } }
  if (!records.length) return { error: 'The file is empty; the first line must be a header row' }
  const cols: Record<string, number> = {}
  for (const [i, h] of records[0].fields.map(h => h.trim().toLowerCase()).entries()) {
    if (!MAILBOX_IMPORT_COLUMNS.includes(h)) return { error: `Unknown column "${h}"; use local_part or address, name, invite_email, password, may_send, may_receive` }
    if (h in cols) return { error: `Column "${h}" appears twice` }
    cols[h] = i
  }
  if (!('name' in cols) || !('local_part' in cols || 'address' in cols)) return { error: 'The header needs local_part (or address) and name' }
  const body = records.slice(1)
  if (!body.length) return { error: 'The file has no mailbox rows' }
  // Like Go's encoding/csv, every record needs the header's field count.
  const short = body.find(r => r.fields.length !== records[0].fields.length)
  if (short) return { error: `The file is not valid CSV: record on line ${short.line}: wrong number of fields` }
  if (body.length > MAILBOX_IMPORT_MAX_ROWS) return { error: `Import at most ${MAILBOX_IMPORT_MAX_ROWS} mailboxes per file` }
  const lowerDomain = domain.toLowerCase()
  const seen = new Map<string, number>()
  return { rows: body.map(({ line, fields: record }) => {
    const row: ImportPreviewRow = { line, address: '', result: 'error' }
    const get = (name: string) => name in cols ? (name === 'password' ? record[cols[name]] ?? '' : (record[cols[name]] ?? '').trim()) : ''
    const fail = (message: string) => Object.assign(row, { message })
    let local = get('local_part').toLowerCase()
    const address = get('address').toLowerCase()
    row.address = address || local
    if (address) {
      const at = address.lastIndexOf('@')
      if (at < 0 || address.slice(at + 1) !== lowerDomain) return fail(`The address must be on ${domain}`)
      if (local && local !== address.slice(0, at)) return fail('local_part and address disagree')
      local = address.slice(0, at)
    }
    const lpError = localPartError(local)
    if (lpError) return fail(lpError)
    row.address = `${local}@${lowerDomain}`
    if (nameError(get('name'))) return fail(nameError(get('name')))
    const invite = get('invite_email'), pw = get('password')
    if ((invite === '') === (pw === '')) return fail('Give exactly one of invite_email or password')
    if (invite) {
      if (!EMAIL.test(invite)) return fail('invite_email is not a valid email address')
      if (invite.toLowerCase() === row.address) return fail('invite_email must be outside the new mailbox')
    } else if (passwordError(pw)) return fail(passwordError(pw))
    for (const column of ['may_send', 'may_receive']) {
      if (!['', 'true', 'false'].includes(get(column).toLowerCase())) return fail(`${column} must be true or false`)
    }
    const first = seen.get(row.address)
    if (first !== undefined) return fail(`Duplicate of line ${first}`)
    seen.set(row.address, row.line)
    row.result = 'ok'
    return row
  }) }
}

/** Import rows summarised for the result banner. */
export function importSummary(rows: { result: string }[]) {
  const count = (r: string) => rows.filter(row => row.result === r).length
  return { ok: count('ok'), created: count('created'), errors: count('error') }
}

export interface OverviewFlags {
  maySend: boolean; mayReceive: boolean; wildcardSender: boolean; isCatchAll: boolean
  forwardsActive: boolean; autoReplyActive: boolean; twoFactor: boolean
}

/** The Migadu-style yes/no overview; section names the detail section that changes each value. */
export function overviewRows(o: OverviewFlags) {
  return [
    { label: 'May send', value: o.maySend, section: 'settings' },
    { label: 'May receive', value: o.mayReceive, section: 'settings' },
    { label: 'Wildcard sender', value: o.wildcardSender, section: 'send-as' },
    { label: 'Catch-all', value: o.isCatchAll, section: 'settings' },
    { label: 'Forwarding active', value: o.forwardsActive, section: '' },
    { label: 'Auto-reply active', value: o.autoReplyActive, section: '' },
    { label: '2FA', value: o.twoFactor, section: 'access' },
  ]
}
