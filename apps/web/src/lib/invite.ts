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

export type InvitePurpose = 'join' | 'mailbox_setup' | 'password_reset'

/** Page text for each kind of link. An unknown purpose is shown as a join invite. */
export function inviteCopy(invite: { purpose?: string; orgName: string; email: string; role: string; inviterName?: string; name?: string }) {
  if (invite.purpose === 'mailbox_setup') return {
    title: `Set a password for ${invite.email}`,
    intro: `${invite.orgName} created this mailbox for you. Mail sent to it is already waiting.`,
    askName: true, initialName: invite.name || '',
    submit: 'Set password and sign in', busy: 'Setting up…', after: '/received',
  }
  if (invite.purpose === 'password_reset') return {
    title: `Reset the password for ${invite.email}`,
    intro: 'Choose a new password. Every other session is signed out.',
    askName: false, initialName: '',
    submit: 'Set new password', busy: 'Saving…', after: '/received',
  }
  return {
    title: `Join ${invite.orgName}`,
    intro: `${invite.inviterName || 'An admin'} invited ${invite.email} as ${invite.role === 'admin' ? 'an admin' : 'a member'}.`,
    askName: true, initialName: '',
    submit: 'Accept and sign in', busy: 'Joining…', after: '/inbox',
  }
}

type AcceptResult = { token?: string; user?: User; signedIn?: boolean }

/**
 * Accepts an invite or reset link; the token leaves the URL first. Returns
 * the signed-in user, or null when the account has two-factor sign-in and
 * must log in (signedIn:false).
 */
export async function completeInvite(
  input: { token: string; name?: string; password: string },
  deps: {
    accept: (input: { token: string; name?: string; password: string }) => Promise<AcceptResult>
    setSession: (token: string, user: User) => void | Promise<void>
    clearHash: () => void
  },
) {
  const session = await deps.accept(input)
  deps.clearHash()
  if (session.signedIn === false || !session.token || !session.user) return null
  await deps.setSession(session.token, session.user)
  return session.user
}

/** Where to sign in after a reset that did not sign the user in. */
export const loginAfterReset = (email: string) => `/login?email=${encodeURIComponent(email)}&passwordReset=1`
