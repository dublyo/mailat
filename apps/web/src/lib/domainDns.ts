import type { DNSRecord, CloudflareDNSResult } from './api'

export function isDMARCRecord(record: DNSRecord): boolean {
  const host = (record.hostname || record.name || '').trim().toLowerCase().replace(/\.+$/, '')
  return host === '_dmarc' || host.startsWith('_dmarc.') || record.value.trim().toLowerCase().startsWith('v=dmarc1')
}

export function dnsExportRecords(records: DNSRecord[]): DNSRecord[] {
  // Bulk DNS imports cannot safely implement conditional DMARC creation.
  return records.filter(record => !isDMARCRecord(record))
}

export function sendingDNSRecords(domain?: { name?: string; domain?: string; emailProvider?: string; dnsRecords?: DNSRecord[] } | null): DNSRecord[] {
  // SMTP setup still exposes its explicitly manual routing instructions.
  if (domain?.emailProvider !== 'ses') return domain?.dnsRecords || []
  const root = (domain?.name || domain?.domain || '').trim().toLowerCase().replace(/\.+$/, '')
  return (domain?.dnsRecords || []).filter(record => {
    const type = (record.recordType || record.type || '').toUpperCase()
    const host = (record.hostname || record.name || '').trim().toLowerCase().replace(/\.+$/, '')
    const value = record.value.trim().toLowerCase()
    const isRoot = host === root || host === '@' || host === ''
    // Preserve other providers' root routing/SPF even in legacy records. SES
    // sending uses the bounce subdomain; DMARC requires a separate current-policy
    // check that a static preview or bulk import cannot enforce.
    if (isRoot && (type === 'MX' || (type === 'TXT' && value.startsWith('v=spf1')))) return false
    return !isDMARCRecord(record)
  })
}

export function dnsResultStatus(result: CloudflareDNSResult): NonNullable<CloudflareDNSResult['status']> {
  return result.status || (result.skipped ? 'skipped' : result.success ? 'created' : 'failed')
}
