import { useState, useEffect, useMemo } from 'react'
import Layout from '../components/Layout'
import SummaryCards from '../components/SummaryCards'
import { api, LogRow } from '../api'
import { toast } from '../components/feedback'
import { TZ_OPTIONS, addDays, localDay, downloadCSV, useReportTz } from '../lib/reportTz'

function today() { return new Date().toISOString().slice(0, 10) }
function daysAgo(n: number) {
  const d = new Date(); d.setUTCDate(d.getUTCDate() - n)
  return d.toISOString().slice(0, 10)
}
function fmtCost(v: number) { return '$' + v.toFixed(4) }
function fmtNum(v: number) { return v.toLocaleString() }

const ALL = '__all__'

// One row per (user, local day, key), aggregated across models — the daily bill.
type BillRow = {
  user_id: number; username: string; day: string
  token_id: number; token_name: string
  request_count: number; input_tokens: number; output_tokens: number
  total_tokens: number; total_cost: number
}

export default function Billing() {
  const [start, setStart] = useState(daysAgo(6))
  const [end, setEnd] = useState(today())
  const [rawData, setRawData] = useState<LogRow[]>([])
  const [loading, setLoading] = useState(false)
  const [refreshedAt, setRefreshedAt] = useState('')
  const [filterUser, setFilterUser] = useState(ALL)
  const [filterGroup, setFilterGroup] = useState(ALL)
  const { tz, setTz, isSuperAdmin } = useReportTz()
  const [range, setRange] = useState({ start: daysAgo(6), end: today() })

  const load = async (s: string, e: string) => {
    setLoading(true)
    try {
      // Widen the UTC window ±1 day so timezone re-bucketing has full edge days.
      const rows = await api.getReport(addDays(s, -1), addDays(e, 1))
      setRawData(rows)
      setRange({ start: s, end: e })
      setRefreshedAt(new Date().toLocaleTimeString('zh-CN'))
    } catch (err) {
      console.error(err)
      toast.error(err)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load(start, end) }, [])

  const filterOptions = useMemo(() => {
    const users = new Map<string, string>()
    const groups = new Set<string>()
    rawData.forEach(r => {
      const uid = String(r.user_id)
      if (!users.has(uid)) users.set(uid, r.username || `user#${r.user_id}`)
      groups.add(r.group || '(空)')
    })
    return {
      users: Array.from(users.entries()).sort((a, b) => a[1].localeCompare(b[1])),
      groups: Array.from(groups).sort(),
    }
  }, [rawData])

  const data = useMemo(() => {
    return rawData.filter(r => {
      const d = localDay(r.hour, tz)
      if (d < range.start || d > range.end) return false
      if (filterUser !== ALL && String(r.user_id) !== filterUser) return false
      if (filterGroup !== ALL) {
        const g = r.group || '(空)'
        if (g !== filterGroup) return false
      }
      return true
    })
  }, [rawData, filterUser, filterGroup, tz, range])

  const rows = useMemo<BillRow[]>(() => {
    const map: Record<string, BillRow> = {}
    data.forEach(r => {
      const day = localDay(r.hour, tz)
      const k = `${r.user_id}|${day}|${r.token_id}`
      if (!map[k]) {
        map[k] = {
          user_id: r.user_id, username: r.username || `user#${r.user_id}`, day,
          token_id: r.token_id, token_name: r.token_name || `key#${r.token_id}`,
          request_count: 0, input_tokens: 0, output_tokens: 0, total_tokens: 0, total_cost: 0,
        }
      }
      const e = map[k]
      e.request_count += r.request_count
      e.input_tokens += r.input_tokens
      e.output_tokens += r.output_tokens
      e.total_tokens += r.total_tokens
      e.total_cost += r.total_cost
    })
    return Object.values(map).sort((a, b) =>
      a.username.localeCompare(b.username) ||
      a.day.localeCompare(b.day) ||
      a.token_name.localeCompare(b.token_name)
    )
  }, [data, tz])

  const summary = useMemo(() => {
    const totalCost = rows.reduce((s, r) => s + r.total_cost, 0)
    const users = new Set(rows.map(r => r.user_id)).size
    const days = new Set(rows.map(r => r.day)).size
    return { totalCost, users, days, lines: rows.length }
  }, [rows])

  const exportCSV = () => {
    downloadCSV(
      `billing_${range.start}_to_${range.end}_${tz.replace(/\//g, '-')}.csv`,
      ['User', 'User ID', 'Date', 'Key', 'Key ID', 'Requests', 'Input', 'Output', 'Total Tokens', 'Cost(USD)'],
      rows.map(r => [r.username, r.user_id, r.day, r.token_name, r.token_id, r.request_count, r.input_tokens, r.output_tokens, r.total_tokens, r.total_cost.toFixed(6)]),
    )
  }

  const filtersActive = filterUser !== ALL || filterGroup !== ALL

  const actions = (
    <>
      <select
        value={tz}
        onChange={e => setTz(e.target.value)}
        disabled={!isSuperAdmin}
        title={isSuperAdmin ? '统计时区（保存为本站默认）' : '统计时区（本站默认，仅超级管理员可改）'}
        className="border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-white focus:outline-none focus:border-brand disabled:opacity-70 disabled:cursor-not-allowed"
      >
        {TZ_OPTIONS.map(z => <option key={z} value={z}>{z}</option>)}
      </select>
      <input type="date" value={start} onChange={e => setStart(e.target.value)} className="border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-white" />
      <span className="text-gray-300 text-xs">→</span>
      <input type="date" value={end} onChange={e => setEnd(e.target.value)} className="border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-white" />
      <button onClick={() => load(start, end)} disabled={loading} className="bg-brand text-white rounded-md px-3 py-1.5 text-xs hover:bg-brand-700 disabled:opacity-50">
        {loading ? '加载中...' : '查询'}
      </button>
      <button onClick={exportCSV} disabled={!rows.length} className="border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50 disabled:opacity-50">
        导出账单 CSV
      </button>
    </>
  )

  return (
    <Layout
      title="用户账单"
      subtitle={`按 ${tz} 时区，每个用户每天每个 Key 一条 · ${range.start} ~ ${range.end}${refreshedAt ? ` · 更新于 ${refreshedAt}` : ''}`}
      actions={actions}
    >
      <div className="bg-white border border-gray-200 rounded-xl px-3 py-3 mb-4 flex flex-wrap items-center gap-3 text-xs">
        <span className="mono-label">Filters</span>
        <label className="flex items-center gap-1.5">
          <span className="text-gray-500">用户</span>
          <select
            value={filterUser}
            onChange={e => setFilterUser(e.target.value)}
            className="border border-gray-200 rounded-md px-2 py-1 bg-white focus:outline-none focus:border-gray-900 max-w-[160px]"
          >
            <option value={ALL}>全部</option>
            {filterOptions.users.map(([id, name]) => (
              <option key={id} value={id}>{name}</option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-1.5">
          <span className="text-gray-500">分组</span>
          <select
            value={filterGroup}
            onChange={e => setFilterGroup(e.target.value)}
            className="border border-gray-200 rounded-md px-2 py-1 bg-white focus:outline-none focus:border-gray-900 max-w-[140px]"
          >
            <option value={ALL}>全部</option>
            {filterOptions.groups.map(g => (
              <option key={g} value={g}>{g}</option>
            ))}
          </select>
        </label>
        {filtersActive && (
          <button
            onClick={() => { setFilterUser(ALL); setFilterGroup(ALL) }}
            className="text-rose-600 hover:text-rose-700 underline-offset-2 hover:underline"
          >
            清除筛选
          </button>
        )}
        <span className="ml-auto text-gray-400 tabular-nums">{rows.length} 条</span>
      </div>

      <SummaryCards cards={[
        { label: 'Total Cost', value: '$' + summary.totalCost.toFixed(2), color: 'text-emerald-600' },
        { label: '用户数', value: String(summary.users) },
        { label: '天数', value: String(summary.days), color: 'text-amber-600' },
        { label: '账单行', value: String(summary.lines), color: 'text-purple-600' },
      ]} />

      <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
        <div className="overflow-x-auto max-h-[68vh] overflow-y-auto">
          <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
            <thead>
              <tr>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">用户</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">日期 ({tz})</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">Key</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Requests</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Input</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Output</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Total Tokens</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Cost</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => (
                <tr key={i} className="hover:bg-gray-50">
                  <td className="px-3 py-1.5 border-b border-gray-50">{r.username}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 font-mono text-gray-600">{r.day}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 font-mono text-gray-500">{r.token_name}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{fmtNum(r.request_count)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{fmtNum(r.input_tokens)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{fmtNum(r.output_tokens)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{fmtNum(r.total_tokens)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums font-medium">{fmtCost(r.total_cost)}</td>
                </tr>
              ))}
              {rows.length === 0 && (
                <tr><td colSpan={8} className="px-4 py-6 text-center text-gray-400">{loading ? '加载中…' : '无数据'}</td></tr>
              )}
              <tr className="bg-emerald-50 font-semibold sticky bottom-0">
                <td className="px-3 py-1.5 border-t-2 border-emerald-200" colSpan={3}>TOTAL</td>
                <td className="px-3 py-1.5 border-t-2 border-emerald-200 text-right tabular-nums">{fmtNum(rows.reduce((s,r)=>s+r.request_count,0))}</td>
                <td className="px-3 py-1.5 border-t-2 border-emerald-200 text-right tabular-nums">{fmtNum(rows.reduce((s,r)=>s+r.input_tokens,0))}</td>
                <td className="px-3 py-1.5 border-t-2 border-emerald-200 text-right tabular-nums">{fmtNum(rows.reduce((s,r)=>s+r.output_tokens,0))}</td>
                <td className="px-3 py-1.5 border-t-2 border-emerald-200 text-right tabular-nums">{fmtNum(rows.reduce((s,r)=>s+r.total_tokens,0))}</td>
                <td className="px-3 py-1.5 border-t-2 border-emerald-200 text-right tabular-nums text-emerald-700">${rows.reduce((s,r)=>s+r.total_cost,0).toFixed(4)}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </Layout>
  )
}
