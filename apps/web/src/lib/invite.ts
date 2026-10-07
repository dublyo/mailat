// Invite and forward-verification links carry their token in the URL fragment,
// which browsers never send to a server or in a Referer header.
import type { User } from './api'

const TOKEN = /^[A-Za-z0-9_-]{16,256}$/
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

function fragment(hash: string) {
  return new URLSearchParams(hash.replace(/^#/, ''))
}

/** The token from /invite#token=…, or '' when it is missing or malformed. */
export function inviteTokenFromHash(hash: string): string {
  const token = fragment(hash).get('token') || ''
  return TOKEN.test(token) ? token : ''
}

/** The forward id and token from /forwards/verify#id=…&token=…, or null. */
export function forwardVerificationFromHash(hash: string): { uuid: string; token: string } | null {
  const params = fragment(hash)
  const uuid = params.get('id') || ''
  const token = params.get('token') || ''
  return UUID.test(uuid) && TOKEN.test(token) ? { uuid, token } : null
}

/**
 * Reads a forward confirmation link and removes it from the address bar at
 * once. Nothing is confirmed until confirm() runs from a person's click: mail
 * scanners and link previews that open the page must not start forwarding
 * without the destination's consent. confirm() sends the token at most once.
 */
export function forwardConfirmation(
  hash: string,
  deps: { verify: (uuid: string, token: string) => Promise<unknown>; clearHash: () => void },
) {
  const link = forwardVerificationFromHash(hash)
  if (hash) deps.clearHash()
  let sent: Promise<unknown> | null = null
  return {
    valid: link !== null,
    confirm() {
      if (!link) return Promise.reject(new Error('Open the complete confirmation link from the email.'))
      sent ??= deps.verify(link.uuid, link.token)
      return sent
    },
  }
}

/** Removes the fragment so the token does not stay in the address bar or history. */
export function clearFragment(win: Pick<Window, 'location' | 'history'> = window) {
  win.history.replaceState(win.history.state, '', win.location.pathname + win.location.search)
}

/** Accepts an invite and signs the new member in; the token leaves the URL first. */
export async function completeInvite(
  input: { token: string; name: string; password: string },
  deps: {
    accept: (input: { token: string; name: string; password: string }) => Promise<{ token: string; user: User }>
    setSession: (token: string, user: User) => void
    clearHash: () => void
  },
) {
  const session = await deps.accept(input)
  deps.clearHash()
  deps.setSession(session.token, session.user)
  return session.user
}
