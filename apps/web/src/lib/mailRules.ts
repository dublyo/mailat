import type { FilterCondition, InboxFilter } from '@/lib/api'

export const FILTER_FIELDS: Array<{ value: FilterCondition['field']; label: string }> = [
  { value: 'from', label: 'From' },
  { value: 'to', label: 'To' },
  { value: 'subject', label: 'Subject' },
  { value: 'body', label: 'Body' },
  { value: 'hasAttachment', label: 'Has attachment' },
]

export const FILTER_OPERATORS: Array<{ value: FilterCondition['operator']; label: string }> = [
  { value: 'contains', label: 'contains' },
  { value: 'notContains', label: "doesn't contain" },
  { value: 'equals', label: 'is' },
  { value: 'notEquals', label: 'is not' },
  { value: 'startsWith', label: 'starts with' },
  { value: 'endsWith', label: 'ends with' },
  { value: 'regex', label: 'matches regex' },
]

const FOLDER_LABELS: Record<string, string> = { inbox: 'Inbox', archive: 'Archive', spam: 'Spam', trash: 'Trash', 'dmarc-reports': 'DMARC Reports' }

// A blocked-sender value is an exact address or an @domain (subdomains are
// not covered). Mirrors the server's validation for instant feedback.
export function blockedSenderValue(input: string): string | null {
  const value = input.trim().toLowerCase()
  if (/^@[a-z0-9.-]+\.[a-z]{2,}$/.test(value)) return value
  if (/^[^\s@<>()",;:]+@[a-z0-9.-]+\.[a-z]{2,}$/.test(value)) return value
  return null
}

export function describeFilter(filter: InboxFilter): string {
  const operators = Object.fromEntries(FILTER_OPERATORS.map(o => [o.value, o.label]))
  const fields = Object.fromEntries(FILTER_FIELDS.map(f => [f.value, f.label]))
  const conditions = filter.conditions
    .map(c => c.field === 'hasAttachment' ? (c.value === 'true' ? 'Has attachment' : 'No attachment') : `${fields[c.field] || c.field} ${operators[c.operator] || c.operator} "${c.value}"`)
    .join(filter.conditionLogic === 'any' ? ' or ' : ' and ')
  const actions: string[] = []
  if (filter.actionFolder) actions.push(`Move to ${FOLDER_LABELS[filter.actionFolder] || filter.actionFolder}`)
  if (filter.actionArchive) actions.push('Archive')
  if (filter.actionTrash) actions.push('Trash')
  for (const label of filter.actionLabels || []) actions.push(`Label "${label}"`)
  if (filter.actionStar) actions.push('Star')
  if (filter.actionMarkRead) actions.push('Mark read')
  return `${conditions} → ${actions.join(', ') || 'No action'}`
}

// Best-effort prefill for a free-text rule from older versions, e.g.
// "From: *@newsletter.*". The user reviews it in the builder before saving.
export function conditionFromLegacyText(text: string): FilterCondition {
  const match = /^\s*(from|to|subject|has)\s*:\s*(.*)$/i.exec(text || '')
  if (!match) return { field: 'subject', operator: 'contains', value: (text || '').trim() }
  const key = match[1].toLowerCase()
  const value = match[2].replace(/\*/g, ' ').trim().replace(/\s+/g, ' ')
  if (key === 'has') return { field: 'hasAttachment', operator: 'equals', value: 'true' }
  return { field: key as FilterCondition['field'], operator: 'contains', value }
}
