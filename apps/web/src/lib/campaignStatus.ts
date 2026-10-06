import type { Campaign } from '@/lib/api'

// Readable labels for campaigns.status_reason (why a campaign paused, throttled or was reset).
export const STATUS_REASON_LABELS: Record<string, string> = {
  user_paused: 'Paused by a user',
  monthly_quota_exceeded: 'Monthly send quota reached',
  sender_unavailable: 'Sender identity or domain is no longer valid',
  provider_paused: 'Amazon SES paused sending for this account',
  provider_rejected: 'SES rejected several messages in a row',
  bounce_rate_high: 'Bounce rate too high',
  complaint_rate_high: 'Complaint rate too high',
  invalid_segment: 'Segment rules are invalid',
  ses_daily_quota: 'Waiting for the SES daily quota',
  legacy_requires_review: 'Needs review before sending',
  no_eligible_recipients: 'No eligible recipients',
}

export const statusReasonLabel = (reason: string | null | undefined) =>
  reason ? STATUS_REASON_LABELS[reason] || reason.replace(/_/g, ' ') : ''

export const AUDIENCE_WARNING_LABELS: Record<string, string> = {
  dmarc_missing: 'The sending domain has no verified DMARC record; some inboxes may reject or junk this campaign.',
  sandbox_mode: 'Your SES account is in sandbox mode: only verified addresses will receive mail.',
  no_postal_address: 'Set your organization postal address before sending.',
  feedback_not_ready: 'Finish sending setup (bounce/complaint feedback) for the sender domain before sending.',
}

export const audienceWarningLabel = (warning: string) => AUDIENCE_WARNING_LABELS[warning] || warning.replace(/_/g, ' ')

const rate = (count: number, sent: number) => (sent > 0 ? (count / sent) * 100 : 0)

export const openRate = (c: Pick<Campaign, 'openCount' | 'sentCount'>) => rate(c.openCount, c.sentCount)
export const clickRate = (c: Pick<Campaign, 'clickCount' | 'sentCount'>) => rate(c.clickCount, c.sentCount)

// Share of the materialised audience that reached a final outcome.
export const campaignProgressPercent = (c: Pick<Campaign, 'sentCount' | 'failedCount' | 'unknownCount' | 'skippedCount' | 'totalRecipients'>) => {
  if (!c.totalRecipients) return 0
  const done = c.sentCount + c.failedCount + c.unknownCount + c.skippedCount
  return Math.min(100, Math.round((done / c.totalRecipients) * 100))
}
