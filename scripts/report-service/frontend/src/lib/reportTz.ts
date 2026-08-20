import { useEffect, useState } from 'react'
import { api, ROLE_SUPER_ADMIN } from '../api'
import { withBase } from '../basePath'
import { toast } from '../components/feedback'

// Shared statistics-timezone utilities for the Usage Report and Billing pages.
//
// The backend aggregate (report_daily_agg) is bucketed in UTC; each row's
// `hour` is "YYYY-MM-DD HH:00". We re-bucket to the configured zone in the
// browser via Intl (DST-safe). The zone itself is a DEPLOYMENT-WIDE server
// setting (report_config.report_timezone) read from /api/auth/config; only a
// super admin can change it.

export const TZ_OPTIONS = [
  'UTC', 'Asia/Tokyo', 'Asia/Shanghai', 'Asia/Singapore', 'Asia/Hong_Kong',
  'Asia/Kolkata', 'Europe/London', 'America/New_York', 'America/Los_Angeles',
]

// Shift a YYYY-MM-DD string by whole days (UTC-safe). Used to widen the
// fetched UTC window by ±1 day so timezone re-bucketing never clips an edge
// local-day (max TZ offset is ±14h < 24h).
export function addDays(s: string, n: number): string {
  const d = new Date(s + 'T00:00:00Z'); d.setUTCDate(d.getUTCDate() + n)
  return d.toISOString().slice(0, 10)
}

const dayFmtCache: Record<string, Intl.DateTimeFormat> = {}
const hourFmtCache: Record<string, Intl.DateTimeFormat> = {}
function hourToUtcDate(hour: string): Date { return new Date(hour.replace(' ', 'T') + ':00Z') }

// Local calendar day ("YYYY-MM-DD") of a UTC hour bucket in the given zone.
export function localDay(hour: string, tz: string): string {
  if (tz === 'UTC') return hour.slice(0, 10)
  if (!dayFmtCache[tz]) {
    dayFmtCache[tz] = new Intl.DateTimeFormat('en-CA', { timeZone: tz, year: 'numeric', month: '2-digit', day: '2-digit' })
  }
  return dayFmtCache[tz].format(hourToUtcDate(hour))
}

// Local "YYYY-MM-DD HH:00" label of a UTC hour bucket in the given zone.
export function localHour(hour: string, tz: string): string {
  if (tz === 'UTC') return hour
  if (!hourFmtCache[tz]) {
    hourFmtCache[tz] = new Intl.DateTimeFormat('en-GB', { timeZone: tz, hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  }
  return `${localDay(hour, tz)} ${hourFmtCache[tz].format(hourToUtcDate(hour))}`
}

// Minimal CSV cell escaping.
export function csvCell(v: string | number): string {
  const s = String(v)
  return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s
}

// Triggers a client-side CSV download.
export function downloadCSV(filename: string, header: (string | number)[], rows: (string | number)[][]): void {
  const csv = '﻿' + [header, ...rows].map(row => row.map(csvCell).join(',')).join('\n')
  const blob = new Blob([csv], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

// useReportTz loads the deployment statistics timezone from the server and,
// for a super admin, persists changes back. Non-super-admins get a read-only
// view of the server value.
export function useReportTz() {
  const [tz, setTzState] = useState('Asia/Tokyo')
  const [isSuperAdmin, setIsSuperAdmin] = useState(false)
  const [loaded, setLoaded] = useState(false)

  useEffect(() => {
    void (async () => {
      try {
        const cfg = await fetch(withBase('/api/auth/config')).then(r => r.json())
        if (cfg?.report_timezone) setTzState(cfg.report_timezone)
      } catch { /* keep default */ }
      try {
        const me = await api.getAuthMe()
        setIsSuperAdmin(typeof me?.role === 'number' && me.role >= ROLE_SUPER_ADMIN)
      } catch { /* treat as non-super-admin */ }
      setLoaded(true)
    })()
  }, [])

  const setTz = async (next: string) => {
    const prev = tz
    setTzState(next)
    if (!isSuperAdmin) return
    try {
      await api.setReportTimezone(next)
      toast.success(`统计时区已保存为 ${next}`)
    } catch (err) {
      setTzState(prev)
      toast.error(err)
    }
  }

  return { tz, setTz, isSuperAdmin, loaded }
}
