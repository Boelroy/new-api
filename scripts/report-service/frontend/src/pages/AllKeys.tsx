import { useState, useEffect, useMemo } from 'react'
import NavBar from '../components/NavBar'
import SummaryCards from '../components/SummaryCards'
import { api, ChannelRow } from '../api'

const STATUS_LABEL: Record<number, string> = { 1: '启用', 2: '手动禁用', 3: '自动禁用' }
const STATUS_CLS: Record<number, string> = {
  1: 'bg-emerald-100 text-emerald-800',
  2: 'bg-red-100 text-red-700',
  3: 'bg-amber-100 text-amber-700',
}

function today() { return new Date().toISOString().slice(0, 10) }

function exportCSV(rows: ChannelRow[], start: string, end: string) {
  const header = ['ID','名称','Key末尾','状态','总已用($)','额度($)','总剩余($)']
  const csvRows = rows.map(r => {
    const quota = r.quota_usd
    const remaining = quota != null ? (quota - r.used_usd).toFixed(4) : ''
    return [r.id, r.name, r.key, STATUS_LABEL[r.status] ?? r.status, r.used_usd.toFixed(4), quota != null ? quota.toFixed(2) : '', remaining]
  })
  const csv = [header, ...csvRows].map(r => r.join(',')).join('\n')
  const a = document.createElement('a')
  a.href = 'data:text/csv;charset=utf-8,﻿' + encodeURIComponent(csv)
  a.download = `allkeys${start ? '_' + start : ''}${end ? '_' + end : ''}.csv`
  a.click()
}

export default function AllKeys() {
  const [rows, setRows] = useState<ChannelRow[]>([])
  const [start, setStart] = useState(today())
  const [end, setEnd] = useState(today())
  const [loading, setLoading] = useState(false)
  const [refreshedAt, setRefreshedAt] = useState('')

  const load = async (s?: string, e?: string) => {
    setLoading(true)
    try {
      const data = await api.getAllKeys(s, e)
      setRows(data.sort((a, b) => a.id - b.id))
      setRefreshedAt(new Date().toLocaleTimeString('zh-CN'))
    } catch (err) { console.error(err) }
    finally { setLoading(false) }
  }

  useEffect(() => { load(start, end) }, [])

  const handleQuery = () => load(start, end)
  const handleAll = () => { setStart(''); setEnd(''); load() }

  const summary = useMemo(() => {
    const totalUsed = rows.reduce((s, r) => s + r.used_usd, 0)
    const totalQuota = rows.reduce((s, r) => s + (r.quota_usd ?? 0), 0)
    const totalRemaining = rows.reduce((s, r) => r.quota_usd != null ? s + Math.max(0, r.quota_usd - r.used_usd) : s, 0)
    return { count: rows.length, totalUsed, totalQuota, totalRemaining }
  }, [rows])

  return (
    <div className="max-w-[1400px] mx-auto px-10 py-8">
      <h1 className="text-xl font-semibold tracking-tight mb-1">All Keys</h1>
      <p className="text-xs text-gray-400 mb-5">所有 Key 的总用量与容量（按创建时间筛选）</p>
      <NavBar />

      <div className="flex gap-3 items-center flex-wrap mb-4 text-sm">
        <label className="text-gray-500">创建时间 起：<input type="date" value={start} onChange={e => setStart(e.target.value)} className="ml-1 border border-gray-200 rounded px-2 py-1 text-sm" /></label>
        <label className="text-gray-500">止：<input type="date" value={end} onChange={e => setEnd(e.target.value)} className="ml-1 border border-gray-200 rounded px-2 py-1 text-sm" /></label>
        <button onClick={handleQuery} disabled={loading} className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-sm hover:opacity-85 disabled:opacity-50">
          {loading ? '加载中...' : '查询'}
        </button>
        <button onClick={handleAll} className="border border-gray-200 rounded-md px-3 py-1.5 text-sm hover:bg-gray-50">全部</button>
        <button onClick={() => exportCSV(rows, start, end)} className="border border-gray-200 rounded-md px-3 py-1.5 text-sm hover:bg-gray-50">导出 CSV</button>
        {refreshedAt && <span className="text-xs text-gray-400">更新于 {refreshedAt}</span>}
      </div>

      <SummaryCards cards={[
        { label: 'Key 总数', value: String(summary.count), color: 'text-blue-600' },
        { label: '总已用', value: '$' + summary.totalUsed.toFixed(2), color: 'text-rose-600' },
        { label: '总额度', value: summary.totalQuota ? '$' + summary.totalQuota.toFixed(2) : '未配置' },
        { label: '总剩余', value: summary.totalRemaining ? '$' + summary.totalRemaining.toFixed(2) : '—', color: 'text-emerald-600' },
      ]} />

      <div className="overflow-x-auto border border-gray-200 rounded-lg bg-white max-h-[70vh] overflow-y-auto">
        <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
          <thead>
            <tr>
              {['ID','名称','Key 末尾','状态','总已用 ($)','额度 ($)','总剩余 ($)','剩余%'].map(h => (
                <th key={h} className="sticky top-0 bg-gray-50 px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map(r => {
              const quota = r.quota_usd
              const remaining = quota != null ? quota - r.used_usd : null
              const pct = quota && quota > 0 ? (remaining! / quota) * 100 : null
              const barColor = pct != null ? (pct > 20 ? 'bg-emerald-500' : pct > 5 ? 'bg-amber-500' : 'bg-rose-500') : ''
              return (
                <tr key={r.id} className="hover:bg-gray-50">
                  <td className="px-3 py-1.5 border-b border-gray-50">{r.id}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50">{r.name}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 font-mono text-gray-400">{r.key}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50">
                    <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold ${STATUS_CLS[r.status] ?? 'bg-gray-100 text-gray-600'}`}>
                      {STATUS_LABEL[r.status] ?? r.status}
                    </span>
                  </td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">${r.used_usd.toFixed(4)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{quota != null ? '$' + quota.toFixed(2) : <span className="text-gray-300">未设置</span>}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{remaining != null ? '$' + remaining.toFixed(4) : '—'}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50">
                    {pct != null ? (
                      <div className="flex items-center gap-2">
                        <span className="w-12 text-right tabular-nums">{pct.toFixed(1)}%</span>
                        <div className="w-20 h-1.5 bg-gray-200 rounded-full overflow-hidden">
                          <div className={`h-full ${barColor} rounded-full`} style={{ width: `${Math.min(100, Math.max(0, pct))}%` }} />
                        </div>
                      </div>
                    ) : <span className="text-gray-300">—</span>}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}
