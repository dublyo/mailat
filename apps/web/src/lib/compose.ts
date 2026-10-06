import type { Email, Identity, ComposeRequest, ReceivedEmail } from './api'

export function plainAddress(value: string): string {
  return (value.match(/<([^>]+)>/)?.[1] || value).trim()
}

export function replyRecipients(email: Email, identities: Identity[], replyAll: boolean) {
  // Envelope aliases belong to this user's delivered copy. A catch-all domain
  // can also contain teammates' explicit identities, so never exclude the whole domain.
  const owned = new Set([...identities.map(i => i.email), ...(email.envelopeRecipients || [])]
    .map(value => plainAddress(value).toLowerCase()))
  const isOwn = (value: string) => owned.has(value.toLowerCase())
  const target = plainAddress(email.replyToAddress || email.from.email)
  const to: string[] = []
  const cc: string[] = []
  const seen = new Set<string>()
  const add = (list: string[], value: string) => {
    const address = plainAddress(value)
    const key = address.toLowerCase()
    if (!address || seen.has(key) || (replyAll && isOwn(address))) return
    seen.add(key)
    list.push(address)
  }
  add(to, target)
  if (replyAll) {
    email.to.forEach(a => add(to, a.email))
    email.cc?.forEach(a => add(cc, a.email))
  }
  return { to, cc }
}

export function replySender(email: Email, identities: Identity[]) {
  const candidates = [...(email.envelopeRecipients || []), ...email.to.map(a => a.email), ...(email.cc || []).map(a => a.email)]
  const identity = identities.find(i => Number(i.id) === email.identityId && i.canSend !== false)
    || identities.find(i => i.canSend !== false && candidates.some(a => a.toLowerCase() === i.email.toLowerCase()))
    || identities.find(i => i.canSend !== false && i.isDefault)
    || identities.find(i => i.canSend !== false)
  if (!identity) return undefined
  const domain = identity.email.split('@')[1]?.toLowerCase()
  const alias = candidates.find(a => a.split('@')[1]?.toLowerCase() === domain)
  return { identity, fromEmail: alias || identity.email }
}

export function prefixedSubject(subject: string, prefix: 'Re' | 'Fwd') {
  return new RegExp(`^${prefix}:`, 'i').test(subject) ? subject : `${prefix}: ${subject}`
}

export function escapeHtml(text: string) {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;')
}

export function composeThreadHeaders(mode: string, original?: Email | null) {
  if (mode === 'draft') return { inReplyTo: original?.inReplyTo, references: original?.references }
  if ((mode === 'reply' || mode === 'replyAll') && original?.messageId) {
    return { inReplyTo: original.messageId, references: [...new Set([...(original.references || []), original.messageId])] }
  }
  // A forwarded message starts its own conversation, even when the source was a reply.
  return { inReplyTo: undefined, references: undefined }
}

export function failedRetryPayload(payload: ComposeRequest, savedAttempt: ReceivedEmail): ComposeRequest {
  if (savedAttempt.sendStatus !== 'failed') throw new Error('This attempt is no longer confirmed failed. Review its current status before sending again.')
  if ((savedAttempt.attachments?.length || 0) !== (payload.attachments?.length || 0)) {
    throw new Error('The saved attempt has incomplete attachments. Retry after its full message is available.')
  }
  // Sending consumed the draft. The durable Outbox copy now owns the attachment
  // references; old draft UUIDs must never be reused for an explicit new attempt.
  const { draftId: _draftId, draftVersion: _draftVersion, version: _version, ...message } = payload
  return { ...message, attachments: (savedAttempt.attachments || []).map(a => ({ blobId: a.uuid, name: a.filename, type: a.contentType })) }
}

export function retainSendAttempt(status: number | undefined, wasUncertain: boolean) {
  // An error during a status check cannot prove that the earlier send did not
  // happen. Keep the exact payload/key until a definitive result is obtained.
  return wasUncertain || status === undefined || status < 400 || status >= 500
}

/** Members may send only as the identity address or a +tag of it (the server enforces this). */
export function memberAliasAllowed(identityEmail: string, alias: string): boolean {
  const identity = identityEmail.trim().toLowerCase(), from = alias.trim().toLowerCase()
  if (from === identity) return true
  const at = identity.lastIndexOf('@')
  if (at < 1) return false
  const local = identity.slice(0, at), domain = identity.slice(at)
  if (!from.startsWith(`${local}+`) || !from.endsWith(domain)) return false
  const tag = from.slice(local.length + 1, from.length - domain.length)
  return tag.length > 0 && !tag.includes('@')
}
