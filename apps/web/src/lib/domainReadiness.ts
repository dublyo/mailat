import type { DomainReadiness, DomainReadinessItem, DomainReadinessStatus } from './api'

// Words and actions for the per-domain "API sending readiness" checklist.
// Kept free of Vue so the rules are tested directly.

export type ReadinessBadgeVariant = 'success' | 'warning' | 'error' | 'info' | 'default'

export function readinessBadge(status: DomainReadinessStatus): { label: string; variant: ReadinessBadgeVariant } {
  switch (status) {
    case 'ok': return { label: 'Done', variant: 'success' }
    case 'missing': return { label: 'To do', variant: 'warning' }
    case 'pending': return { label: 'Pending', variant: 'info' }
    case 'attention': return { label: 'Needs attention', variant: 'error' }
    case 'off': return { label: 'Off', variant: 'default' }
    default: return { label: 'Unknown', variant: 'default' }
  }
}

// What a button on an item does. setup_sending and create_identity run in the
// checklist; the others move to the part of the domain card that fixes them.
export type ReadinessActionKind = 'verify' | 'dmarc' | 'setup_sending' | 'create_identity' | 'choose_identity' | 'receiving'
export interface ReadinessAction { kind: ReadinessActionKind; label: string; address?: string }

export const ASK_ADMIN = 'Ask an organization owner or admin to fix this.'

// Members can see every item, but only owners and admins change domains,
// sending resources or identities. Viewing DMARC or receiving is for everyone.
export function readinessActions(item: DomainReadinessItem, opts: { canManage: boolean; suggestedIdentity: string }): ReadinessAction[] {
  // Nothing to do while the one-time setup runs or SES confirms on its own.
  if (item.status === 'ok' || !item.fix || item.state === 'automatic_setup') return []
  switch (item.fix) {
    case 'dmarc': return [{ kind: 'dmarc', label: 'Show DMARC setup' }]
    case 'receiving': return [{ kind: 'receiving', label: 'Show receiving' }]
  }
  if (!opts.canManage) return []
  switch (item.fix) {
    case 'verify': return [{ kind: 'verify', label: 'Verify now' }]
    // Named like the sending panel's button, not the card's DNS "Set up sending" wizard.
    case 'setup_sending': return [{ kind: 'setup_sending', label: item.status === 'missing' ? 'Set up sending resources' : 'Retry sending setup' }]
    case 'create_identity':
      return opts.suggestedIdentity
        ? [{ kind: 'create_identity', label: `Create ${opts.suggestedIdentity}`, address: opts.suggestedIdentity }, { kind: 'choose_identity', label: 'Use another address' }]
        : [{ kind: 'choose_identity', label: 'Add identity' }]
  }
  return []
}

// A member sees why they cannot act on an item an admin must fix. Only owners
// and admins add identities, so a missing identity always gets the hint, also
// before verification when it has no fix yet.
export function needsAdmin(item: DomainReadinessItem, canManage: boolean): boolean {
  if (canManage || item.status === 'ok' || item.state === 'automatic_setup') return false
  return item.key === 'sending_identity' || ['verify', 'setup_sending', 'create_identity'].includes(item.fix)
}

export function readinessSummary(r: DomainReadiness | null | undefined): string {
  if (!r) return ''
  if (r.ready) return 'Ready to send with the API'
  const required = r.items.filter(i => !i.optional)
  const done = required.filter(i => i.status === 'ok').length
  return `${done} of ${required.length} required steps done`
}

// The copyable value an item offers, e.g. the DMARC record to publish.
export function readinessCopy(item: DomainReadinessItem, domain: string): { label: string; value: string } | null {
  if (!item.value || item.status === 'ok') return null
  if (item.key === 'dmarc') return { label: `TXT _dmarc.${domain}`, value: item.value }
  // A root MX moves the domain's mail to Mailat; only offer it once receiving
  // is on, where the Receiving section explains what it replaces.
  if (item.key === 'receiving' && item.status !== 'off') return { label: 'MX @', value: item.value }
  return null
}

// How long to wait before each re-read while the one-time setup after
// verification runs (about two minutes in all), then stop.
export const AUTOMATIC_SETUP_POLL_MS = [2000, 5000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000]

// What to say after Verify now when the domain is still not verified.
export function verifyOutcome(domain: { status?: string; sesVerified?: boolean; dnsRecords?: { hostname: string; verified: boolean }[] } | null | undefined): string {
  if (!domain || (domain.status === 'active' && domain.sesVerified)) return ''
  const waiting = [...new Set((domain.dnsRecords || []).filter(r => !r.verified).map(r => r.hostname))]
  return waiting.length
    ? `Not verified yet. Still waiting for DNS: ${waiting.join(', ')}. New records can take a while to appear.`
    : 'Not verified yet. DNS and SES can take a while to confirm; try again in a few minutes.'
}
