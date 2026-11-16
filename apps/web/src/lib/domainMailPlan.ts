// "Who reads mail on this domain?" — the step after a domain is verified.
// Kept free of Vue so the rules are tested directly.
//
// me       one inbox: the admin's own address on the domain (optionally the
//          catch-all, so every other address on the domain arrives there too)
// private  separate logins: each person signs in and sees only their mailbox
// both     the admin's address (and catch-all) plus private logins
// sending  nobody reads mail here; the domain only sends

export type MailPlan = 'me' | 'private' | 'both' | 'sending'

export const MAIL_PLANS: { value: MailPlan; title: string; detail: string }[] = [
  { value: 'me', title: 'Me, in my one inbox', detail: 'Mail for this domain arrives in your Inbox next to your other domains.' },
  { value: 'private', title: 'Private logins', detail: 'Each person gets their own login and sees only their own mailbox. You cannot read their mail.' },
  { value: 'both', title: 'Both', detail: 'Your address (and everything not claimed by someone else) comes to you; named people get private logins.' },
  { value: 'sending', title: 'Nobody, sending only', detail: 'This domain only sends (for example from your app). No mail is received here.' },
]

export const includesMe = (plan: MailPlan) => plan === 'me' || plan === 'both'
export const includesPrivate = (plan: MailPlan) => plan === 'private' || plan === 'both'

// Offer the step to owners and admins on a verified domain where they have no
// address yet, once their addresses are known, unless this browser already
// answered it (set up, "sending only" or "not now"). The panel itself then
// checks the organization-wide state with domainInUse before showing.
export function shouldOfferMailPlan(input: { canManage: boolean; status?: string; sesVerified?: boolean; myAddresses: number; answered: boolean; addressesLoaded: boolean }): boolean {
  return input.canManage && input.addressesLoaded && input.status === 'active' && !!input.sesVerified && input.myAddresses === 0 && !input.answered
}

// Someone already gets mail on the domain: a private login, another person's
// address, or a catch-all. The step is for unused domains only, so it never
// moves an existing catch-all or second-guesses an existing setup.
export function domainInUse(list: { catchAll: unknown; mailboxes: unknown[]; catchAllOptions: unknown[] } | null | undefined): boolean {
  return !!list && (!!list.catchAll || list.mailboxes.length > 0 || list.catchAllOptions.length > 0)
}

const LOCAL_PART = /^[a-z0-9](?:[a-z0-9._+-]{0,62}[a-z0-9])?$/

// The part before @ for the admin's own address; '' when valid.
export function localPartError(value: string): string {
  const v = value.trim().toLowerCase()
  if (!v) return 'Enter the part before @, for example hello.'
  if (v.includes('@')) return 'Enter only the part before @.'
  if (!LOCAL_PART.test(v) || v.includes('..')) return 'Use letters, numbers, dots, dashes or underscores.'
  return ''
}

// What "Set up" will do, in order, so the admin sees it before clicking.
export function planSteps(plan: MailPlan, opts: { domain: string; localPart: string; catchAll: boolean; receivingEnabled: boolean }): string[] {
  if (plan === 'sending') return ["Nothing changes. To receive mail later, add an address or turn on receiving on this domain's page."]
  const steps: string[] = []
  if (!opts.receivingEnabled) steps.push(`Turn on receiving for ${opts.domain} (you then publish one MX record, shown below).`)
  if (includesMe(plan)) {
    steps.push(`Create ${opts.localPart.trim().toLowerCase() || '…'}@${opts.domain} in your Inbox.`)
    if (opts.catchAll) steps.push(`Send every other address on ${opts.domain} to it (catch-all). Private logins always get their own mail first.`)
  }
  if (includesPrivate(plan)) steps.push('Open Mailboxes to create the private logins (one at a time or from a CSV).')
  return steps
}

// The answer chosen in Add Domain, remembered per browser so the step after
// verification starts with it. A per-browser convenience only.
const hintKey = (domain: string) => `mailat.mailPlan.${domain.toLowerCase()}`
// Whether this browser already answered the step for a domain.
const answeredKey = (domain: string) => `mailat.mailPlanAnswered.${domain.toLowerCase()}`
export function markMailPlanAnswered(domain: string, answered = true) {
  try { if (answered) localStorage.setItem(answeredKey(domain), '1'); else localStorage.removeItem(answeredKey(domain)) } catch { /* not essential */ }
}
export function mailPlanAnswered(domain: string): boolean {
  try { return localStorage.getItem(answeredKey(domain)) === '1' } catch { return false }
}
export function rememberMailPlan(domain: string, plan: MailPlan) {
  try { localStorage.setItem(hintKey(domain), plan) } catch { /* not essential */ }
}
export function recalledMailPlan(domain: string): MailPlan | '' {
  try {
    const value = localStorage.getItem(hintKey(domain))
    return value === 'me' || value === 'private' || value === 'both' || value === 'sending' ? value : ''
  } catch { return '' }
}
