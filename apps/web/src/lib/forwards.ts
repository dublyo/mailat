// Which actions a forward offers in settings, by its status.
import type { ForwardStatus } from './api'

export type ForwardAction = 'resend' | 'pause' | 'resume'

/**
 * A pending forward can resend its link. A suspended one (complaint, permanent
 * bounce or suppressed destination) runs again only after a fresh
 * confirmation, so it offers re-verification rather than resume.
 */
export function forwardActions(forward: { status: ForwardStatus; verified: boolean }): { action: ForwardAction; label: string }[] {
  switch (forward.status) {
    case 'pending': return [{ action: 'resend', label: 'Resend link' }]
    case 'suspended': return [{ action: 'resend', label: 'Re-verify' }]
    case 'active': return [{ action: 'pause', label: 'Pause' }]
    case 'paused': return forward.verified ? [{ action: 'resume', label: 'Resume' }] : []
  }
  return []
}
