// lastBeijingMonth is the month before the current one in UTC+8, as YYYY-MM:
// the month statements and their pushes default to.
export function lastBeijingMonth(now = Date.now()): string {
  const bj = new Date(now + 8 * 3600_000)
  const d = new Date(Date.UTC(bj.getUTCFullYear(), bj.getUTCMonth() - 1, 1))
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}`
}
