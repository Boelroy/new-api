import { useState, useEffect, useMemo } from 'react'
import { BarChart, Bar, XAxis, YAxis, Tooltip, Legend, ResponsiveContainer, CartesianGrid } from 'recharts'
import NavBar from '../components/NavBar'
import SummaryCards from '../components/SummaryCards'
import { api, LogRow } from '../api'

const COLORS = ['#2563eb','#059669','#d97706','#e11d48','#7c3aed','#ea580c','#0d9488','#c026d3','#3b82f6','#10b981']

type View = 'hourly' | 'daily' | 'key' | 'model'

function today() { return new Date().toISOString().slice(0, 10) }
function daysAgo(n: number) {
  const d = new Date(); d.setUTCDate(d.getUTCDate() - n)
  return d.toISOString().slice(0, 10)
}

function fmtCost(v: number) { return '$' + v.toFixed(4) }
function fmtNum(v: number) { return v.toLocaleString() }

export default function Report() {
  const [start, setStart] = useState(daysAgo(6))
  const [end, setEnd] = useState(today())
  const [data, setData] = useState<LogRow[]>([])
  const [loading, setLoading] = useState(false)
  const [view, setView] = useState<View>('daily')
  const [refreshedAt, setRefreshedAt] = useState('')

  const load = async (s: string, e: string) => {
    setLoading(true)
    try {
      const rows = await api.getReport(s, e)
      setData(rows)
      setRefreshedAt(new Date().toLocaleTimeString('zh-CN'))
    } catch (err) {
      console.error(err)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load(start, end) }, [])

  const summary = useMemo(() => {
    const totalCost = data.reduce((s, r) => s + r.total_cost, 0)
    const totalTokens = data.reduce((s, r) => s + r.total_tokens, 0)
    const totalReqs = data.reduce((s, r) => s + r.request_count, 0)
    const keys = new Set(data.map(r => r.token_id)).size
    return { totalCost, totalTokens, totalReqs, keys }
  }, [data])

  // daily cost chart data
  const dailyChartData = useMemo(() => {
    const map: Record<string, number> = {}
    data.forEach(r => {
      const day = r.hour.slice(0, 10)
      map[day] = (map[day] || 0) + r.total_cost
    })
    return Object.entries(map).sort(([a],[b]) => a.localeCompare(b)).map(([date, cost]) => ({ date, cost }))
  }, [data])

  // per model chart
  const modelChartData = useMemo(() => {
    const map: Record<string, number> = {}
    data.forEach(r => { map[r.model] = (map[r.model] || 0) + r.total_cost })
    return Object.entries(map).sort(([,a],[,b]) => b - a).slice(0, 10).map(([model, cost]) => ({ model: model.replace('claude-',''), cost }))
  }, [data])

  // table rows
  const tableRows = useMemo(() => {
    if (view === 'daily') {
      const map: Record<string, LogRow & { day: string }> = {}
      data.forEach(r => {
        const day = r.hour.slice(0, 10)
        const k = `${day}|${r.token_id}|${r.channel_id}`
        if (!map[k]) map[k] = { ...r, day, hour: day }
        else {
          const e = map[k]
          e.request_count += r.request_count
          e.input_tokens += r.input_tokens; e.output_tokens += r.output_tokens
          e.cache_read_tokens += r.cache_read_tokens; e.cache_write_tokens += r.cache_write_tokens
          e.total_tokens += r.total_tokens; e.total_cost += r.total_cost
        }
      })
      return Object.values(map).sort((a, b) => a.day.localeCompare(b.day))
    }
    if (view === 'hourly') {
      return [...data].sort((a, b) => a.hour.localeCompare(b.hour))
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
    // model
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
  }, [data, view])

  return (
    <div className="max-w-[1400px] mx-auto px-10 py-8">
      <h1 className="text-xl font-semibold tracking-tight mb-1">API Usage Report</h1>
      <p className="text-xs text-gray-400 mb-5">{start} ~ {end} (UTC){refreshedAt && ` · 更新于 ${refreshedAt}`}</p>
      <NavBar />

      {/* Controls */}
      <div className="flex gap-3 items-center flex-wrap mb-4 text-sm">
        <label className="text-gray-500">Start: <input type="date" value={start} onChange={e => setStart(e.target.value)} className="ml-1 border border-gray-200 rounded px-2 py-1 text-sm" /></label>
        <label className="text-gray-500">End: <input type="date" value={end} onChange={e => setEnd(e.target.value)} className="ml-1 border border-gray-200 rounded px-2 py-1 text-sm" /></label>
        <button onClick={() => load(start, end)} disabled={loading} className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-sm hover:opacity-85 disabled:opacity-50">
          {loading ? '加载中...' : '查询'}
        </button>
        <button onClick={() => api.exportCSV(start, end)} className="border border-gray-200 rounded-md px-3 py-1.5 text-sm hover:bg-gray-50">
          Export CSV
        </button>
      </div>

      <SummaryCards cards={[
        { label: 'Total Cost', value: '$' + summary.totalCost.toFixed(2), color: 'text-emerald-600' },
        { label: 'Total Tokens', value: fmtNum(summary.totalTokens) },
        { label: 'Requests', value: fmtNum(summary.totalReqs), color: 'text-amber-600' },
        { label: 'Keys', value: String(summary.keys), color: 'text-purple-600' },
      ]} />

      {/* Charts */}
      <div className="grid grid-cols-2 gap-4 mb-6">
        <div className="bg-white border border-gray-200 rounded-lg p-4">
          <h3 className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-3">Cost Over Time ($)</h3>
          <ResponsiveContainer width="100%" height={160}>
            <BarChart data={dailyChartData} margin={{ top: 0, right: 0, bottom: 0, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} />
              <YAxis tick={{ fontSize: 10 }} tickFormatter={v => '$' + v} />
              <Tooltip formatter={(v: number) => ['$' + v.toFixed(2), 'Cost']} />
              <Bar dataKey="cost" fill="#2563eb" radius={[2,2,0,0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
        <div className="bg-white border border-gray-200 rounded-lg p-4">
          <h3 className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-3">Cost By Model ($)</h3>
          <ResponsiveContainer width="100%" height={160}>
            <BarChart data={modelChartData} margin={{ top: 0, right: 0, bottom: 0, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="model" tick={{ fontSize: 9 }} />
              <YAxis tick={{ fontSize: 10 }} tickFormatter={v => '$' + v} />
              <Tooltip formatter={(v: number) => ['$' + v.toFixed(2), 'Cost']} />
              {modelChartData.map((_, i) => (
                <Bar key={i} dataKey="cost" fill={COLORS[i % COLORS.length]} radius={[2,2,0,0]} />
              ))}
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>

      {/* View tabs */}
      <div className="flex gap-0 border-b border-gray-200 mb-4">
        {(['daily','hourly','key','model'] as View[]).map(v => (
          <button key={v} onClick={() => setView(v)}
            className={`px-4 py-2 text-xs border-b-2 -mb-px transition-all ${view === v ? 'border-gray-900 text-gray-900 font-semibold' : 'border-transparent text-gray-400 hover:text-gray-700'}`}>
            {v === 'daily' ? 'Daily' : v === 'hourly' ? 'Hourly' : v === 'key' ? 'Per-Key' : 'Per-Model'}
          </button>
        ))}
      </div>

      {/* Table */}
      <div className="overflow-x-auto border border-gray-200 rounded-lg bg-white max-h-[60vh] overflow-y-auto">
        <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
          <thead>
            <tr>
              {view === 'hourly' && <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">Hour</th>}
              {view === 'daily' && <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">Date</th>}
              {(view === 'hourly' || view === 'daily') && <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">Key</th>}
              {view === 'key' && <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">Key</th>}
              {view === 'model' && <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">Model</th>}
              {view !== 'model' && view !== 'key' && <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">Model</th>}
              <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">Requests</th>
              <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">Input</th>
              <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">Output</th>
              <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">Total Tokens</th>
              <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">Cost</th>
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
            {/* Total row */}
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
  )
}
