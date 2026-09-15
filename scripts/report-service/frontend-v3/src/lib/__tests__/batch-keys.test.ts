import { describe, expect, it } from 'vitest'
import { parseKeyRows, serializeKeyRows, keyRowsToItems } from '../batch-keys'

describe('batch Key input contract', () => {
  it('ignores comments and blank lines while preserving keys, quotas and optional notes', () => {
    expect(
      keyRowsToItems(parseKeyRows('# header\r\n\nkey-one,10,first batch\nkey-two note without quota\nkey-three')),
    ).toEqual([
      { key: 'key-one', quota_usd: 10, note: 'first batch' },
      { key: 'key-two', note: 'note without quota' },
      { key: 'key-three' },
    ])
  })
  it('preserves notes and decimal quotas through text and table round trips', () => {
    const rows = [
      { key: 'key-one', quota: '', note: 'no quota' },
      { key: 'key-two', quota: '0.25', note: 'second' },
    ]
    expect(parseKeyRows(serializeKeyRows(rows))).toEqual(rows)
  })
  it('does not truncate note text into a numeric quota', () => {
    expect(keyRowsToItems(parseKeyRows('key-one 10-credits remaining'))).toEqual([
      { key: 'key-one', note: '10-credits remaining' },
    ])
  })
  it('keeps AWS credential separators intact and never sends nonfinite quota values', () => {
    expect(keyRowsToItems([{ key: 'access|secret', quota: 'Infinity', note: '' }])).toEqual([{ key: 'access|secret' }])
  })
})
