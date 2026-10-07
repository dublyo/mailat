// Team (organization members) display rules.

type MemberLike = { email: string; status: string }

// A removed account whose address was re-invited keeps its mail under this
// placeholder login; it is never an address anyone can mail or sign in with.
const reassignedPlaceholder = /^removed\+[0-9a-f-]{36}@invalid$/i

/** The address to show for a member, hiding the internal placeholder. */
export function memberAddress(email: string) {
  return reassignedPlaceholder.test(email) ? 'Former account (address reassigned)' : email
}

/** Members to list: removed accounts are hidden unless asked for. */
export function visibleMembers<T extends MemberLike>(members: T[], showRemoved: boolean) {
  return showRemoved ? members : members.filter(m => m.status === 'active')
}

export function removedCount(members: MemberLike[]) {
  return members.filter(m => m.status !== 'active').length
}
