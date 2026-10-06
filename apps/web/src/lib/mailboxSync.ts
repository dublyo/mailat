// Applies one live mailbox event to the visible message list without a
// refetch. Pure, so the matching rules can mirror the server's list query and
// be tested directly.
import type { MailboxEvent, ReceivedEmail } from './api'

export interface MailboxView {
  folder: string
  identityId: number
  page: number
  pageSize: number
  domainId?: number
  isRead?: boolean
  isStarred?: boolean
  hasAttachments?: boolean
  labels?: string[]
  search?: string
  sender?: string
  dateFrom?: string
  dateTo?: string
}

export interface MailboxSyncResult {
  emails: ReceivedEmail[]
  /** Change in the view's total message count. */
  totalDelta: number
  /** The view cannot be patched (an active search); reload it. */
  needsRefresh: boolean
  countsChanged: boolean
  /** New matching mail arrived that is not inserted here (later page or sender/date filter). */
  newMailPill: boolean
}

// Mirrors receivedListQuery's folder predicates (internal/service/inbox.go).
export function inFolder(email: ReceivedEmail, folder: string): boolean {
  switch (folder || 'inbox') {
    case 'inbox': return email.folder === 'inbox' && !email.isTrashed && !email.isArchived
    case 'sent': case 'drafts': case 'outbox': return email.folder === folder && !email.isTrashed
    case 'dmarc-reports': return email.folder === folder && !email.isTrashed && !email.isSpam && !email.isArchived
    case 'spam': return (email.folder === 'spam' || email.isSpam) && !email.isTrashed
    case 'trash': return email.isTrashed
    case 'starred': return email.isStarred && !email.isTrashed
    case 'archive': return email.isArchived && !email.isTrashed
    case 'all': return !email.isTrashed
    default: return false
  }
}

/** Whether the message belongs in the view, ignoring search, sender and date filters. */
export function matchesView(email: ReceivedEmail, view: MailboxView): boolean {
  if (view.identityId > 0 && Number(email.identityId) !== view.identityId) return false
  if (view.domainId && Number(email.domainId) !== view.domainId) return false
  if (view.isRead !== undefined && email.isRead !== view.isRead) return false
  if (view.isStarred !== undefined && email.isStarred !== view.isStarred) return false
  if (view.hasAttachments !== undefined && email.hasAttachments !== view.hasAttachments) return false
  if (view.labels?.length && !view.labels.every(label => email.labels?.includes(label))) return false
  return inFolder(email, view.folder)
}

// Server order: received_at DESC, then id DESC.
function newerFirst(a: ReceivedEmail, b: ReceivedEmail) {
  const diff = new Date(b.receivedAt).getTime() - new Date(a.receivedAt).getTime()
  return diff || Number(b.id) - Number(a.id)
}

function insert(emails: ReceivedEmail[], email: ReceivedEmail, pageSize: number) {
  const next = [...emails, email].sort(newerFirst)
  return next.length > pageSize ? next.slice(0, pageSize) : next
}

export function applyMailboxEvent(state: { emails: ReceivedEmail[] }, event: MailboxEvent, view: MailboxView): MailboxSyncResult {
  const result: MailboxSyncResult = { emails: state.emails, totalDelta: 0, needsRefresh: false, countsChanged: true, newMailPill: false }
  const searching = !!view.search?.trim()
  const narrowed = !!(view.sender?.trim() || view.dateFrom || view.dateTo)
  const insertable = view.page <= 1 && !narrowed

  if (event.type === 'email_deleted') {
    const gone = new Set(event.uuids)
    result.emails = state.emails.filter(e => !gone.has(e.uuid))
    result.totalDelta = result.emails.length - state.emails.length
    return result
  }

  const summary = event.summary
  const index = state.emails.findIndex(e => e.uuid === event.uuid)
  const matches = matchesView(summary, view)
  if (searching) {
    // Text matching is the server's; patch what is shown and reload for the rest.
    if (index >= 0 && matches) result.emails = state.emails.map((e, i) => i === index ? { ...e, ...summary } : e)
    else if (index >= 0) { result.emails = state.emails.filter((_, i) => i !== index); result.totalDelta = -1 }
    result.needsRefresh = index < 0 && matches
    return result
  }
  if (index >= 0) {
    if (matches) result.emails = state.emails.map((e, i) => i === index ? { ...e, ...summary } : e)
    else { result.emails = state.emails.filter((_, i) => i !== index); result.totalDelta = -1 }
    return result
  }
  if (!matches) return result
  if (insertable) {
    result.emails = insert(state.emails, summary, view.pageSize)
    result.totalDelta = 1
  } else if (event.type === 'new_email') {
    result.newMailPill = true
  }
  return result
}
