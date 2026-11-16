// The unified Inbox split by domain and by address ("All inboxes" → domain →
// address), kept free of Vue so the rules are tested directly.

export interface MailboxScopeIdentity {
  identityId: number
  uuid: string
  email: string
  displayName: string
  color: string
  kind: 'personal' | 'shared' | string
  isCatchAll: boolean
  total: number
  unread: number
}
export interface MailboxScopeDomain {
  domainId: number
  name: string
  total: number
  unread: number
  identities: MailboxScopeIdentity[]
}
export interface MailboxScopes {
  total: number
  unread: number
  domains: MailboxScopeDomain[]
}

type Query = Record<string, unknown>

const positive = (value: unknown) => {
  const n = Number(Array.isArray(value) ? value[0] : value)
  return Number.isInteger(n) && n > 0 ? n : 0
}

// The scope selected in the mailbox URL: ?identity=<id> wins over ?domain=<id>.
export function scopeFromQuery(query: Query): { identityId: number; domainId: number } {
  const identityId = positive(query.identity)
  return { identityId, domainId: identityId ? 0 : positive(query.domain) }
}

// Only worth showing when there is more than one address to choose between.
export function showScopeTree(scopes: MailboxScopes | null | undefined): boolean {
  if (!scopes) return false
  return scopes.domains.reduce((n, d) => n + d.identities.length, 0) > 1
}

// A folder link keeps the selected domain or address, so Sent, Archive or
// Trash open within the same scope instead of jumping to every inbox.
export function folderLocation(itemRoute: string, current: Query): { path: string; query: Record<string, string> } {
  const [path, search = ''] = itemRoute.split('?')
  const query: Record<string, string> = Object.fromEntries(new URLSearchParams(search))
  const { identityId, domainId } = scopeFromQuery(current)
  if (identityId) query.identity = String(identityId)
  else if (domainId) query.domain = String(domainId)
  return { path, query }
}

// A tree entry opens that scope's Inbox; the folder links then move within it.
export function scopeLocation(scope: { identityId?: number; domainId?: number }): { path: string; query: Record<string, string> } {
  const query: Record<string, string> = {}
  if (scope.identityId) query.identity = String(scope.identityId)
  else if (scope.domainId) query.domain = String(scope.domainId)
  return { path: '/received', query }
}

// Words for the toolbar title: "ketabaziz.com" or "info@ketabaziz.com".
export function scopeLabel(scopes: MailboxScopes | null | undefined, query: Query): string {
  const { identityId, domainId } = scopeFromQuery(query)
  if (!scopes || (!identityId && !domainId)) return ''
  for (const domain of scopes.domains) {
    if (domainId && domain.domainId === domainId) return domain.name
    const identity = domain.identities.find(i => i.identityId === identityId)
    if (identity) return identity.email
  }
  return ''
}

// The address the sender actually wrote to. Catch-all and +tag mail reaches an
// identity under a different address; show that address, not the identity's.
export function deliveredTo(email: { envelopeRecipients?: string[] | null; identityEmail?: string; toEmails?: string[] | null }): string {
  const identity = (email.identityEmail || '').toLowerCase()
  const recipients = (email.envelopeRecipients || []).filter(Boolean)
  return recipients.find(r => r.toLowerCase() !== identity) || recipients[0] || email.identityEmail || ''
}
