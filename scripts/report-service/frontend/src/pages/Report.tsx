import { useState, useEffect, useMemo } from 'react'
import { BarChart, Bar, Cell, XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid } from 'recharts'
import Layout from '../components/Layout'
import SummaryCards from '../components/SummaryCards'
import { api, LogRow } from '../api'
import { toast } from '../components/feedback'
import { TZ_OPTIONS, addDays, localDay, localHour, useReportTz } from '../lib/reportTz'

const COLORS = ['#2864FF','#3E8E4F','#C97A12','#e11d48','#4D83FF','#7c3aed','#0d9488','#c026d3','#8DB7FF','#D9FF43']

type View = 'daily' | 'hourly' | 'key' | 'model'

function today() { return new Date().toISOString().slice(0, 10) }
function daysAgo(n: number) {
  const d = new Date(); d.setUTCDate(d.getUTCDate() - n)
  return d.toISOString().slice(0, 10)
}

function fmtCost(v: number) { return '$' + v.toFixed(4) }
function fmtNum(v: number) { return v.toLocaleString() }

const ALL = '__all__'

export default function Report() {
  const [start, setStart] = useState(daysAgo(6))
  const [end, setEnd] = useState(today())
  const [rawData, setRawData] = useState<LogRow[]>([])
  const [loading, setLoading] = useState(false)
  const [view, setView] = useState<View>('daily')
  const [refreshedAt, setRefreshedAt] = useState('')
  const [filterUser, setFilterUser] = useState(ALL)
  const [filterToken, setFilterToken] = useState(ALL)
  const [filterGroup, setFilterGroup] = useState(ALL)
  const { tz, setTz, isSuperAdmin } = useReportTz()
  // The local-day range actually loaded (set on fetch), used to trim the ±1
  // day fetch padding so the table shows exactly [start, end] local days.
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
    const tokens = new Map<string, string>()
    const groups = new Set<string>()
    rawData.forEach(r => {
      const uid = String(r.user_id)
      if (!users.has(uid)) users.set(uid, r.username || `user#${r.user_id}`)
      const tid = String(r.token_id)
      if (!tokens.has(tid)) tokens.set(tid, r.token_name || `key#${r.token_id}`)
      groups.add(r.group || '(空)')
    })
    return {
      users: Array.from(users.entries()).sort((a, b) => a[1].localeCompare(b[1])),
      tokens: Array.from(tokens.entries()).sort((a, b) => a[1].localeCompare(b[1])),
      groups: Array.from(groups).sort(),
    }
  }, [rawData])

  const data = useMemo(() => {
    return rawData.filter(r => {
      // Trim the ±1 day fetch padding to the loaded local-day range.
      const d = localDay(r.hour, tz)
      if (d < range.start || d > range.end) return false
      if (filterUser !== ALL && String(r.user_id) !== filterUser) return false
      if (filterToken !== ALL && String(r.token_id) !== filterToken) return false
      if (filterGroup !== ALL) {
        const g = r.group || '(空)'
        if (g !== filterGroup) return false
      }
      return true
    })
  }, [rawData, filterUser, filterToken, filterGroup, tz, range])

  const filtersActive = filterUser !== ALL || filterToken !== ALL || filterGroup !== ALL
  const resetFilters = () => { setFilterUser(ALL); setFilterToken(ALL); setFilterGroup(ALL) }

  const summary = useMemo(() => {
    const totalCost = data.reduce((s, r) => s + r.total_cost, 0)
    const totalTokens = data.reduce((s, r) => s + r.total_tokens, 0)
    const totalReqs = data.reduce((s, r) => s + r.request_count, 0)
    const keys = new Set(data.map(r => r.token_id)).size
    return { totalCost, totalTokens, totalReqs, keys }
  }, [data])

  const dailyChartData = useMemo(() => {
    const map: Record<string, number> = {}
    data.forEach(r => {
      const day = localDay(r.hour, tz)
      map[day] = (map[day] || 0) + r.total_cost
    })
    return Object.entries(map).sort(([a],[b]) => a.localeCompare(b)).map(([date, cost]) => ({ date, cost }))
  }, [data, tz])

  const modelChartData = useMemo(() => {
    const map: Record<string, number> = {}
    data.forEach(r => { map[r.model] = (map[r.model] || 0) + r.total_cost })
    return Object.entries(map).sort(([,a],[,b]) => b - a).slice(0, 10).map(([model, cost]) => ({ model: model.replace('claude-',''), cost }))
  }, [data])

  const tableRows = useMemo(() => {
    if (view === 'daily') {
      const map: Record<string, LogRow & { day: string }> = {}
      data.forEach(r => {
        const day = localDay(r.hour, tz)
        const k = `${day}|${r.token_id}|${r.model}`
        if (!map[k]) map[k] = { ...r, day, hour: day }
        else {
          const e = map[k]
          e.request_count += r.request_count
          e.input_tokens += r.input_tokens; e.output_tokens += r.output_tokens
          e.cache_read_tokens += r.cache_read_tokens; e.cache_write_tokens += r.cache_write_tokens
          e.total_tokens += r.total_tokens; e.total_cost += r.total_cost
        }
      })
      return Object.values(map).sort((a, b) =>
        a.day.localeCompare(b.day) ||
        (a.token_name || '').localeCompare(b.token_name || '') ||
        a.model.localeCompare(b.model)
      )
    }
    if (view === 'hourly') {
      const map: Record<string, LogRow> = {}
      data.forEach(r => {
        const hourLabel = localHour(r.hour, tz)
        const k = `${hourLabel}|${r.token_id}|${r.model}`
        if (!map[k]) map[k] = { ...r, hour: hourLabel }
        else {
          const e = map[k]
          e.request_count += r.request_count
          e.input_tokens += r.input_tokens; e.output_tokens += r.output_tokens
          e.cache_read_tokens += r.cache_read_tokens; e.cache_write_tokens += r.cache_write_tokens
          e.total_tokens += r.total_tokens; e.total_cost += r.total_cost
        }
      })
      return Object.values(map).sort((a, b) =>
        a.hour.localeCompare(b.hour) ||
        (a.token_name || '').localeCompare(b.token_name || '') ||
        a.model.localeCompare(b.model)
      )
    }
    if (view === 'key') {
      const map: Record<number, typeof data[0]> = {}
      data.forEach(r => {
        if (!map[r.token_id]) map[r.token_id] = { ...r }
        else {
          const e = map[r.token_id]
          e.request_count += r.request_count; e.total_cost += r.total_cost
          e.input_tokens += r.input_tokens; e.output_tokens += r.output_tokens
          e.total_tokens += r.total_tokens
        }
      })
      return Object.values(map).sort((a, b) => b.total_cost - a.total_cost)
    }
    const map: Record<string, typeof data[0]> = {}
    data.forEach(r => {
      if (!map[r.model]) map[r.model] = { ...r }
      else {
        const e = map[r.model]
        e.request_count += r.request_count; e.total_cost += r.total_cost
        e.input_tokens += r.input_tokens; e.output_tokens += r.output_tokens
        e.total_tokens += r.total_tokens
      }
    })
    return Object.values(map).sort((a, b) => b.total_cost - a.total_cost)
  }, [data, view, tz])

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
      <button onClick={() => api.exportCSV(start, end)} className="border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50">
        Export CSV
      </button>
    </>
  )

  return (
    <Layout
      title="Usage Report"
      subtitle={`${range.start} ~ ${range.end} (${tz})${refreshedAt ? ` · 更新于 ${refreshedAt}` : ''}`}
      actions={actions}
    >
      <div className="bg-white border border-gray-200 rounded-xl px-3 py-3 mb-4 flex flex-wrap items-center gap-3 text-xs">
        <span className="mono-label">Filters</span>
        <label className="flex items-center gap-1.5">
          <span className="text-gray-500">用户</span>
          <select
            value={filterUser}
            onChange={e => setFilterUser(e.target.value)}
            className="border border-gray-200 rounded-md px-2 py-1 bg-white focus:outline-none focus:border-gray-900 max-w-[140px]"
          >
            <option value={ALL}>全部</option>
            {filterOptions.users.map(([id, name]) => (
              <option key={id} value={id}>{name}</option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-1.5">
          <span className="text-gray-500">Key</span>
          <select
            value={filterToken}
            onChange={e => setFilterToken(e.target.value)}
            className="border border-gray-200 rounded-md px-2 py-1 bg-white focus:outline-none focus:border-gray-900 max-w-[180px]"
          >
            <option value={ALL}>全部</option>
            {filterOptions.tokens.map(([id, name]) => (
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
            onClick={resetFilters}
            className="text-rose-600 hover:text-rose-700 underline-offset-2 hover:underline"
          >
            清除筛选
          </button>
        )}
        <span className="ml-auto text-gray-400 tabular-nums">
          {data.length} / {rawData.length} 行
        </span>
      </div>

      <SummaryCards cards={[
        { label: 'Total Cost', value: '$' + summary.totalCost.toFixed(2), color: 'text-emerald-600' },
        { label: 'Total Tokens', value: fmtNum(summary.totalTokens) },
        { label: 'Requests', value: fmtNum(summary.totalReqs), color: 'text-amber-600' },
        { label: 'Keys', value: String(summary.keys), color: 'text-purple-600' },
      ]} />

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 mb-6">
        <div className="bg-white border border-gray-200 rounded-xl p-4">
          <h3 className="mono-label mb-3">Cost Over Time ($)</h3>
          <ResponsiveContainer width="100%" height={180}>
            <BarChart data={dailyChartData} margin={{ top: 0, right: 0, bottom: 0, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#D9DDD7" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} />
              <YAxis tick={{ fontSize: 10 }} tickFormatter={v => '$' + v} />
              <Tooltip formatter={(v: number) => ['$' + v.toFixed(2), 'Cost']} />
              <Bar dataKey="cost" fill="#2864FF" radius={[3,3,0,0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
        <div className="bg-white border border-gray-200 rounded-xl p-4">
          <h3 className="mono-label mb-3">Cost By Model ($)</h3>
          <ResponsiveContainer width="100%" height={180}>
            <BarChart data={modelChartData} margin={{ top: 0, right: 0, bottom: 0, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#D9DDD7" />
              <XAxis dataKey="model" tick={{ fontSize: 9 }} />
              <YAxis tick={{ fontSize: 10 }} tickFormatter={v => '$' + v} />
              <Tooltip formatter={(v: number) => ['$' + v.toFixed(2), 'Cost']} />
              <Bar dataKey="cost" radius={[3,3,0,0]}>
                {modelChartData.map((_, i) => (
                  <Cell key={i} fill={COLORS[i % COLORS.length]} />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>

      <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
        <div className="flex gap-0 border-b border-gray-200 px-2">
          {(['daily','hourly','key','model'] as View[]).map(v => (
            <button key={v} onClick={() => setView(v)}
              className={`px-4 py-2.5 text-xs border-b-2 -mb-px transition-all ${view === v ? 'border-brand text-brand font-semibold' : 'border-transparent text-gray-400 hover:text-gray-700'}`}>
              {v === 'daily' ? 'Daily' : v === 'hourly' ? 'Hourly' : v === 'key' ? 'Per-Key' : 'Per-Model'}
            </button>
          ))}
        </div>

        <div className="overflow-x-auto max-h-[60vh] overflow-y-auto">
          <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
            <thead>
              <tr>
                {view === 'hourly' && <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">Hour</th>}
                {view === 'daily' && <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">Date</th>}
                {(view === 'hourly' || view === 'daily') && <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">Key</th>}
                {view === 'key' && <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">Key</th>}
                {view === 'model' && <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">Model</th>}
                {view !== 'model' && view !== 'key' && <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">Model</th>}
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Requests</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Input</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Output</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Total Tokens</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Cost</th>
              </tr>
            </thead>
            <tbody>
              {tableRows.map((r, i) => (
                <tr key={i} className="hover:bg-gray-50">
                  <td className="px-3 py-1.5 border-b border-gray-50">
                    {view === 'hourly' ? r.hour : view === 'daily' ? r.hour.slice(0,10) : view === 'key' ? r.token_name || `key#${r.token_id}` : r.model}
                  </td>
                  {(view === 'hourly' || view === 'daily') && <td className="px-3 py-1.5 border-b border-gray-50 font-mono text-gray-400">{r.token_name || `key#${r.token_id}`}</td>}
                  {(view === 'hourly' || view === 'daily') && <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{r.model}</td>}
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{fmtNum(r.request_count)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{fmtNum(r.input_tokens)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{fmtNum(r.output_tokens)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{fmtNum(r.total_tokens)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums font-medium">{fmtCost(r.total_cost)}</td>
                </tr>
              ))}
              <tr className="bg-emerald-50 font-semibold sticky bottom-0">
                <td className="px-3 py-1.5 border-t-2 border-emerald-200">TOTAL</td>
                {(view === 'hourly' || view === 'daily') && <td className="px-3 py-1.5 border-t-2 border-emerald-200"></td>}
                {(view === 'hourly' || view === 'daily') && <td className="px-3 py-1.5 border-t-2 border-emerald-200"></td>}
                <td className="px-3 py-1.5 border-t-2 border-emerald-200 text-right tabular-nums">{fmtNum(tableRows.reduce((s,r)=>s+r.request_count,0))}</td>
                <td className="px-3 py-1.5 border-t-2 border-emerald-200 text-right tabular-nums">{fmtNum(tableRows.reduce((s,r)=>s+r.input_tokens,0))}</td>
                <td className="px-3 py-1.5 border-t-2 border-emerald-200 text-right tabular-nums">{fmtNum(tableRows.reduce((s,r)=>s+r.output_tokens,0))}</td>
                <td className="px-3 py-1.5 border-t-2 border-emerald-200 text-right tabular-nums">{fmtNum(tableRows.reduce((s,r)=>s+r.total_tokens,0))}</td>
                <td className="px-3 py-1.5 border-t-2 border-emerald-200 text-right tabular-nums text-emerald-700">${tableRows.reduce((s,r)=>s+r.total_cost,0).toFixed(4)}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </Layout>
  )
}
