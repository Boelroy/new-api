import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  ResponsiveContainer,
  LineChart,
  Line,
  XAxis,
  YAxis,
  Tooltip,
  CartesianGrid,
  Legend,
} from 'recharts'
import Layout from '../components/Layout'
import SummaryCards from '../components/SummaryCards'
import { api, type CacheStatsResponse } from '../api'

function today(offsetDays = 0): string {
  const d = new Date()
  d.setUTCDate(d.getUTCDate() + offsetDays)
  return d.toISOString().slice(0, 10)
}

function formatNumber(n: number): string {
  if (n >= 1_000_000_000) return (n / 1_000_000_000).toFixed(2) + 'B'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n)
}

function formatBucket(bucket: string, mode: 'hour' | 'day'): string {
  // Backend now returns date strings verbatim from report_daily_agg:
  //   day  bucket → "2026-07-01"
  //   hour bucket → "2026-07-01 14:00"
  if (!bucket) return bucket
  if (mode === 'day') return bucket.slice(5) // MM-DD
  const parts = bucket.split(' ')
  if (parts.length === 2) return parts[0].slice(5) + ' ' + parts[1] // MM-DD HH:mm
  return bucket
}

export default function CacheReport() {
  const [bucketMode, setBucketMode] = useState<'hour' | 'day'>('hour')
  const [start, setStart] = useState<string>('')
  const [end, setEnd] = useState<string>('')
  const [modelFilter, setModelFilter] = useState<string>('claude')
  const [data, setData] = useState<CacheStatsResponse | null>(null)
  const [loading, setLoading] = useState(false)
  const [refreshedAt, setRefreshedAt] = useState('')

  const load = useCallback(async (s?: string, e?: string, b?: 'hour' | 'day', m?: string) => {
    setLoading(true)
    try {
      const res = await api.getCacheStats({
        start: s || undefined,
        end: e || undefined,
        bucket: b ?? bucketMode,
        model: m ?? modelFilter,
      })
      setData(res)
      setRefreshedAt(new Date().toLocaleTimeString('zh-CN'))
    } catch (err) {
      console.error(err)
      setData(null)
    } finally {
      setLoading(false)
    }
  }, [bucketMode, modelFilter])

  useEffect(() => { void load() }, [])

  const applyQuery = () => void load(start, end, bucketMode, modelFilter)

  const applyPreset = (preset: '24h' | '7d' | '30d') => {
    setBucketMode(preset === '24h' ? 'hour' : 'day')
    let s = ''
    let e = today()
    if (preset === '24h') { s = today(-1); e = today() }
    else if (preset === '7d') { s = today(-6); e = today() }
    else if (preset === '30d') { s = today(-29); e = today() }
    setStart(s); setEnd(e)
    void load(s, e, preset === '24h' ? 'hour' : 'day', modelFilter)
  }

  const chartData = useMemo(() => {
    if (!data) return []
    return data.buckets.map(b => ({
      label: formatBucket(b.bucket, data.range.bucket),
      hit_pct: b.hit_pct,
      reuse_x: b.reuse_x,
      requests: b.requests,
      cache_read_m: Number((b.cache_read_tokens / 1_000_000).toFixed(2)),
      cache_write_m: Number((b.cache_write_tokens / 1_000_000).toFixed(2)),
    }))
  }, [data])

  const s = data?.summary

  const actions = (
    <>
      <button onClick={() => applyPreset('24h')} className="border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50">近 24h</button>
      <button onClick={() => applyPreset('7d')}  className="border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50">近 7 天</button>
      <button onClick={() => applyPreset('30d')} className="border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50">近 30 天</button>
      <select
        value={bucketMode}
        onChange={ev => setBucketMode(ev.target.value as 'hour' | 'day')}
        className="border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-white"
      >
        <option value="hour">按小时</option>
        <option value="day">按天</option>
      </select>
      <input
        value={modelFilter}
        onChange={ev => setModelFilter(ev.target.value)}
        placeholder="model 前缀 / all"
        className="border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-white w-32"
      />
      <input type="date" value={start} onChange={ev => setStart(ev.target.value)} className="border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-white" />
      <span className="text-gray-300 text-xs">→</span>
      <input type="date" value={end} onChange={ev => setEnd(ev.target.value)} className="border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-white" />
      <button onClick={applyQuery} disabled={loading} className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-50">
        {loading ? '加载中...' : '查询'}
      </button>
    </>
  )

  return (
    <Layout
      title="Cache Report"
      subtitle={`Anthropic prompt cache 命中率 / 复用倍数${refreshedAt ? ` · 更新于 ${refreshedAt}` : ''}${s ? ` · 模型前缀 ${data?.range.model}` : ''}`}
      actions={actions}
    >
      <SummaryCards cards={[
        { label: '请求数', value: s ? formatNumber(s.requests) : '—', color: 'text-blue-600' },
        { label: '命中率', value: s ? s.hit_pct.toFixed(2) + '%' : '—', color: s && s.hit_pct >= 90 ? 'text-emerald-600' : 'text-amber-600' },
        { label: '复用倍数', value: s ? s.reuse_x.toFixed(2) + '×' : '—', color: 'text-indigo-600' },
        { label: 'Cache Read', value: s ? formatNumber(s.cache_read_tokens) : '—', color: 'text-emerald-600' },
        { label: 'Cache Write', value: s ? formatNumber(s.cache_write_tokens) : '—', color: 'text-rose-600' },
        { label: 'Prompt Tokens', value: s ? formatNumber(s.prompt_tokens) : '—', color: 'text-gray-700' },
      ]} />

      <div className="bg-white border border-gray-200 rounded-xl p-4 mb-4">
        <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-2">命中率 (%) 与 复用倍数 (×)</div>
        <div className="w-full h-72">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={chartData} margin={{ top: 10, right: 20, left: 0, bottom: 5 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#eee" />
              <XAxis dataKey="label" tick={{ fontSize: 10 }} />
              <YAxis yAxisId="pct" domain={[0, 100]} tick={{ fontSize: 10 }} label={{ value: '命中率 %', angle: -90, position: 'insideLeft', fontSize: 10 }} />
              <YAxis yAxisId="reuse" orientation="right" tick={{ fontSize: 10 }} label={{ value: 'reuse ×', angle: 90, position: 'insideRight', fontSize: 10 }} />
              <Tooltip wrapperStyle={{ fontSize: 11 }} />
              <Legend wrapperStyle={{ fontSize: 11 }} />
              <Line yAxisId="pct" type="monotone" dataKey="hit_pct" name="命中率 %" stroke="#10b981" dot={false} strokeWidth={2} />
              <Line yAxisId="reuse" type="monotone" dataKey="reuse_x" name="reuse ×" stroke="#6366f1" dot={false} strokeWidth={2} />
            </LineChart>
          </ResponsiveContainer>
        </div>
      </div>

      <div className="bg-white border border-gray-200 rounded-xl p-4 mb-4">
        <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-2">Cache tokens (M)</div>
        <div className="w-full h-56">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={chartData} margin={{ top: 10, right: 20, left: 0, bottom: 5 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#eee" />
              <XAxis dataKey="label" tick={{ fontSize: 10 }} />
              <YAxis tick={{ fontSize: 10 }} label={{ value: 'M tokens', angle: -90, position: 'insideLeft', fontSize: 10 }} />
              <Tooltip wrapperStyle={{ fontSize: 11 }} />
              <Legend wrapperStyle={{ fontSize: 11 }} />
              <Line type="monotone" dataKey="cache_read_m"  name="Read"  stroke="#059669" dot={false} strokeWidth={2} />
              <Line type="monotone" dataKey="cache_write_m" name="Write" stroke="#dc2626" dot={false} strokeWidth={2} />
            </LineChart>
          </ResponsiveContainer>
        </div>
      </div>

      <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
        <div className="overflow-x-auto max-h-[60vh] overflow-y-auto">
          <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
            <thead>
              <tr>
                {['时段','请求数','命中率','reuse ×','Prompt Tokens','Cache Read','Cache Write','Completion'].map(h => (
                  <th key={h} className="sticky top-0 bg-gray-50 px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data?.buckets.length ? data.buckets.map(b => (
                <tr key={b.bucket} className="hover:bg-gray-50">
                  <td className="px-3 py-1.5 border-b border-gray-50 font-mono text-gray-600">{formatBucket(b.bucket, data.range.bucket)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{b.requests.toLocaleString()}</td>
                  <td className={`px-3 py-1.5 border-b border-gray-50 text-right tabular-nums font-medium ${b.hit_pct >= 90 ? 'text-emerald-600' : 'text-amber-600'}`}>{b.hit_pct.toFixed(2)}%</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{b.reuse_x.toFixed(2)}×</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums text-gray-500">{formatNumber(b.prompt_tokens)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums text-emerald-600">{formatNumber(b.cache_read_tokens)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums text-rose-600">{formatNumber(b.cache_write_tokens)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums text-gray-500">{formatNumber(b.completion_tokens)}</td>
                </tr>
              )) : (
                <tr><td colSpan={8} className="px-4 py-6 text-center text-gray-400">{loading ? '加载中…' : '无数据'}</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </Layout>
  )
}
