import { useCallback, useEffect, useState } from 'react'
import Layout from '../components/Layout'
import { Button, Card, Input } from '../components/ui'
import { toast } from '../components/feedback'
import { api, type KeyhubUsageOverview } from '../api'

// 用量概览 — KHub (pd-maas) provider dashboard. Date range → summary cards +
// a raw series view. pd-maas expects unix-second start/end timestamps.

function toUnixSeconds(dateStr: string, endOfDay: boolean): number | undefined {
  if (!dateStr) return undefined
  const d = new Date(dateStr + (endOfDay ? 'T23:59:59' : 'T00:00:00'))
  const ms = d.getTime()
  if (Number.isNaN(ms)) return undefined
  return Math.floor(ms / 1000)
}

function isoDaysAgo(days: number): string {
  const d = new Date()
  d.setDate(d.getDate() - days)
  return d.toISOString().slice(0, 10)
}

function fmtNumber(v: unknown): string {
  const n = typeof v === 'number' ? v : Number(v)
  if (!Number.isFinite(n)) return '—'
  return n.toLocaleString()
}

export default function KeyhubUsageOverviewPage() {
  const [start, setStart] = useState(() => isoDaysAgo(7))
  const [end, setEnd] = useState(() => isoDaysAgo(0))
  const [data, setData] = useState<KeyhubUsageOverview | null>(null)
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.keyhubUsageOverview(toUnixSeconds(start, false), toUnixSeconds(end, true))
      if (res?.code === 'ok') {
        setData(res.data)
      } else {
        toast.error(res?.message || '加载失败')
      }
    } catch (e) {
      toast.error(e)
    } finally {
      setLoading(false)
    }
  }, [start, end])

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const summary = data?.summary
  const series = Array.isArray(data?.series) ? data!.series : []

  return (
    <Layout
      title="用量概览"
      subtitle="KHub（pd-maas）用量看板"
      actions={
        <div className="flex flex-wrap items-center gap-2">
          <Input type="date" className="h-8 w-auto" value={start} onChange={(e) => setStart(e.target.value)} />
          <span className="text-xs text-muted-foreground">至</span>
          <Input type="date" className="h-8 w-auto" value={end} onChange={(e) => setEnd(e.target.value)} />
          <Button onClick={load} disabled={loading}>
            {loading ? '加载中…' : '查询'}
          </Button>
        </div>
      }
    >
      <div className="grid gap-4 sm:grid-cols-3">
        <SummaryCard label="请求数 (requests)" value={fmtNumber(summary?.request_count)} />
        <SummaryCard label="Token 数 (tokens)" value={fmtNumber(summary?.tokens)} />
        <SummaryCard
          label="原始成本 (raw cost USD)"
          value={summary?.raw_cost_usd != null ? `$${fmtNumber(summary.raw_cost_usd)}` : '—'}
        />
      </div>

      <Card className="mt-4 space-y-3 p-4">
        <div className="flex items-center justify-between">
          <div className="text-sm font-semibold">明细序列 (series)</div>
          <span className="text-[11px] text-muted-foreground tnum">{series.length} 条</span>
        </div>
        {series.length === 0 ? (
          <p className="text-xs text-muted-foreground">所选区间无数据。</p>
        ) : (
          <pre className="max-h-[520px] overflow-auto rounded-md bg-muted p-3 text-[11px] leading-relaxed">
            {JSON.stringify(series, null, 2)}
          </pre>
        )}
      </Card>
    </Layout>
  )
}

function SummaryCard({ label, value }: { label: string; value: string }) {
  return (
    <Card className="p-4">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="mt-1.5 text-2xl font-semibold tracking-tight tnum">{value}</div>
    </Card>
  )
}
