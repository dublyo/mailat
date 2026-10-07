// Role helpers shared by the router guard, navigation and settings. The API
// enforces every rule; these only keep people off pages that would fail.

export type UserRole = 'owner' | 'admin' | 'member' | 'mailbox'
type RoleHolder = { role?: string } | null | undefined

export const isOrgAdmin = (user: RoleHolder) => user?.role === 'owner' || user?.role === 'admin'
export const isMailboxUser = (user: RoleHolder) => user?.role === 'mailbox'
export const isStaff = (user: RoleHolder) => !!user && !isMailboxUser(user)

/** Who may see a page, tab or menu item. */
export type Audience = 'all' | 'staff' | 'admin'

export function canSee(user: RoleHolder, audience: Audience = 'staff') {
  if (!user) return false
  if (audience === 'admin') return isOrgAdmin(user)
  if (audience === 'staff') return isStaff(user)
  return true
}

/**
 * Where a signed-in user is sent instead of a page they may not open, or null.
 * Fail-closed: mailbox users reach only pages marked `mailbox`, and pages
 * marked `admin` need an owner or admin.
 */
export function deniedRedirect(meta: { mailbox?: unknown; admin?: unknown }, user: RoleHolder): string | null {
  if (isMailboxUser(user) && !meta.mailbox) return '/received'
  if (meta.admin && !isOrgAdmin(user)) return '/received'
  return null
}

export const visibleFor = <T extends { audience?: Audience }>(items: T[], user: RoleHolder) =>
  items.filter(item => canSee(user, item.audience))

// Settings tabs: unknown tabs default to staff, so a new tab stays hidden from
// mailbox users until it is given an audience here.
export const settingsTabAudience: Record<string, Audience> = {
  general: 'all', signature: 'all', security: 'all', notifications: 'all', appearance: 'all', filters: 'all', vacation: 'all', shared: 'all',
  team: 'admin', integrations: 'staff',
}

export function settingsTabVisible(id: string, user: RoleHolder, hasSharedMemberships: boolean) {
  if (id === 'shared' && isMailboxUser(user) && !hasSharedMemberships) return false
  return canSee(user, settingsTabAudience[id] ?? 'staff')
}

/**
 * Inbox domain-filter choices. Mailbox users cannot list domains (the API
 * denies it), so theirs come from the domains of their own identities.
 */
export function inboxDomainOptions(
  user: RoleHolder,
  domains: { id: string | number; name: string }[],
  identities: { domainId: string | number; email: string }[],
) {
  if (!isMailboxUser(user)) return domains.map(d => ({ id: String(d.id), name: d.name }))
  const byId = new Map<string, string>()
  for (const i of identities) if (!byId.has(String(i.domainId))) byId.set(String(i.domainId), i.email.slice(i.email.lastIndexOf('@') + 1))
  return [...byId].map(([id, name]) => ({ id, name }))
}
