// Spreadsheet-safe CSV export.
// Every field is quoted (embedded quotes doubled), and values a spreadsheet
// would treat as a formula (= + - @, tab, CR) get a leading apostrophe so
// imported contact data can't execute (CSV/formula injection).

const FORMULA_START = /^[=+\-@\t\r]/

export function csvField(value: unknown): string {
  let text = value == null ? '' : String(value)
  if (FORMULA_START.test(text)) text = `'${text}`
  return `"${text.replace(/"/g, '""')}"`
}

/** Builds a CSV document with a UTF-8 BOM and CRLF line endings. */
export function toCsv(rows: unknown[][], headers: string[]): string {
  const lines = [headers, ...rows].map(row => row.map(csvField).join(','))
  return '﻿' + lines.join('\r\n') + '\r\n'
}

export const CSV_MIME = 'text/csv;charset=utf-8'
