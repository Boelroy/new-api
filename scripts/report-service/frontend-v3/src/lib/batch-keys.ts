export type KeyRow = { key: string; quota: string; note: string }

// Preserve notes even when no quota was supplied. Only a complete numeric
// token is a quota; parseFloat would silently truncate e.g. "10-credits".
export function parseKeyRows(text: string): KeyRow[] {
  return text.split(/\r?\n/).flatMap(line => {
    const value = line.trim()
    if (!value || value.startsWith('#')) return []
    const [key, second = '', ...rest] = value.split(/[\s,]+/)
    const numeric = second !== '' && Number.isFinite(Number(second))
    return [
      {
        key,
        quota: numeric ? second : '',
        note: (numeric ? rest : [second, ...rest]).join(' ').trim(),
      },
    ]
  })
}

export function serializeKeyRows(rows: KeyRow[]): string {
  return rows
    .filter(row => row.key.trim())
    .map(row => [row.key.trim(), row.quota.trim(), row.note.trim()].filter(Boolean).join(' '))
    .join('\n')
}

export function keyRowsToItems(rows: KeyRow[]): { key: string; quota_usd?: number; note?: string }[] {
  return rows
    .filter(row => row.key.trim())
    .map(row => ({
      key: row.key.trim(),
      ...(Number(row.quota) > 0 && Number.isFinite(Number(row.quota)) ? { quota_usd: Number(row.quota) } : {}),
      ...(row.note.trim() ? { note: row.note.trim() } : {}),
    }))
}
