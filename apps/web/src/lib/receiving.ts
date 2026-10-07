import type { DomainReceivingStatus, ReceivingMXStatus } from './api'

// Receiving is opt-in per domain. These helpers turn the live MX status into
// the words every screen uses, so the domain card and mailbox pages agree.

export const RECEIVING_EXPLAINER = 'Sending works without this record; receiving mail and mailboxes need it.'

export type ReceivingBadgeVariant = 'success' | 'warning' | 'error' | 'default'

export function receivingBadge(status?: ReceivingMXStatus | null): { label: string; variant: ReceivingBadgeVariant } {
  switch (status) {
    case 'not_enabled': return { label: 'Off', variant: 'default' }
    case 'missing': return { label: 'On – MX missing', variant: 'warning' }
    case 'published': return { label: 'Published', variant: 'success' }
    case 'conflict': return { label: 'Points elsewhere', variant: 'error' }
    default: return { label: 'Unknown', variant: 'default' }
  }
}

// Only a confirmed lookup counts; unknown is never treated as able to receive.
export function canReceive(status?: DomainReceivingStatus | null): boolean {
  return !!status && status.enabled && status.mxStatus === 'published'
}

// The exact record, split the way DNS dashboards ask for it.
export function mxCopyFields(status: Pick<DomainReceivingStatus, 'mxRecord'>): Array<{ label: string; value: string }> {
  return [
    { label: 'Type', value: 'MX' },
    { label: 'Name', value: '@' },
    { label: 'Mail server', value: status.mxRecord.target },
    { label: 'Priority', value: String(status.mxRecord.priority) },
  ]
}

// Why mail to this domain's mailboxes will not arrive, or '' when it will.
// Without a status yet, fall back to the domain's receiving switch.
export function receivingProblem(status: DomainReceivingStatus | null | undefined, domainName: string, receivingEnabled?: boolean): string {
  const name = status?.domain || domainName
  if (!status) return receivingEnabled === false ? `Receiving is off for ${name}, so its mailboxes get no mail.` : ''
  switch (status.mxStatus) {
    case 'published': return ''
    case 'not_enabled': return `Receiving is off for ${name}, so its mailboxes get no mail.`
    case 'missing': return `Receiving is on, but ${name} has no MX record, so other servers (such as Gmail) bounce mail to its mailboxes. Publish MX @ ${status.mxRecord.value}.`
    case 'conflict': return conflictProblem(status, name)
    default: return `The MX record for ${name} could not be checked, so mail may not arrive. Re-check it on the domain card.`
  }
}

function conflictProblem(status: DomainReceivingStatus, name: string): string {
  const existing = status.existingMx || []
  const target = status.mxRecord.target.toLowerCase()
  if (existing.length && existing.every(host => host === '.')) {
    return `${name} publishes a null MX, which says it accepts no mail, so other servers bounce mail to these mailboxes. Replace it with MX @ ${status.mxRecord.value}.`
  }
  const others = existing.filter(host => host.toLowerCase() !== target && host !== '.')
  if (others.length < existing.length && existing.some(host => host.toLowerCase() === target)) {
    return `${name} publishes the Mailat MX, but ${others.join(', ') || 'another MX'} has the same or a better priority, so some mail goes there. Give the Mailat MX the lowest priority number or remove the other record.`
  }
  return `The MX record for ${name} points to ${others.join(', ') || 'another server'}, so mail goes there instead of these mailboxes.`
}

// Overview value for one mailbox, e.g. "No — domain has no MX".
export function receivesMailLabel(status: DomainReceivingStatus | null | undefined, mayReceive: boolean): { value: string; ok: boolean } {
  if (!mayReceive) return { value: 'No — turned off for this mailbox', ok: false }
  if (!status) return { value: 'Checking…', ok: false }
  switch (status.mxStatus) {
    case 'published': return { value: 'Yes', ok: true }
    case 'not_enabled': return { value: 'No — receiving is off for this domain', ok: false }
    case 'missing': return { value: 'No — domain has no MX', ok: false }
    case 'conflict': return { value: 'No — domain MX points elsewhere', ok: false }
    default: return { value: 'Unknown — MX could not be checked', ok: false }
  }
}

// Link from mailbox pages straight to the domain card's receiving section.
export function receivingFixLink(domainUuid: string): string {
  return `/domains?receiving=${encodeURIComponent(domainUuid)}`
}
