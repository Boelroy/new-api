import { useCallback, useEffect, useState } from 'react'
import { RefreshCw } from 'lucide-react'
import Layout from '../components/Layout'
import { Badge, Button, Card } from '../components/ui'
import { toast } from '../components/feedback'
import { api, apiErrorMessage, ROLE_ADMIN, type GynhUsage as GynhUsageData } from '../api'
import { getCachedRole, loadRole } from '../auth'

// 供给台用量 — each uploaded key's cumulative spend since upload, from the
// portal's Key 花费 view (report-service filters it to the caller's keys and
// re-totals). The portal backfills spend day by day, so a fresh key shows
// partial progress until 已汇总. RPM/TPM are the portal's last-60s values.

const STATUS_TONE: Record<string, 'success' | 'warning' | 'danger' | 'neutral'> = {
  ok: 'success',
  warn: 'warning',
  bad: 'danger',
}

const PLATFORMS = ['2.0', '1.0']

function cny(v: string | undefined) {
  return `¥${Number(v ?? 0).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
}

function usd(v: string | undefined) {
  return `$${Number(v ?? 0).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
}

function rate(v: number | null | undefined) {
  return v == null ? '—' : v.toLocaleString('zh-CN')
}

export default function GynhUsage() {
  const [role, setRole] = useState<number | null>(getCachedRole())
  useEffect(() => {
    if (role !== null) return
    void loadRole().then(setRole)
  }, [role])
  const isAdmin = role !== null && role >= ROLE_ADMIN

  const [data, setData] = useState<GynhUsageData | null>(null)
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setData(await api.gynhUsage())
    } catch (e) {
      toast.error(apiErrorMessage(e))
    } finally {
      setLoading(false)
    }
  }, [])

  // Refresh every 30s while the page is open, like the portal's own view.
  useEffect(() => {
    void load()
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible') void load()
    }, 30000)
    return () => clearInterval(timer)
  }, [load])

  const rows = data?.accounts ?? []

  return (
    <Layout title="供给台用量" subtitle="每个 Key 自上号起的累计花费">
      <div className="mb-4 grid gap-4 sm:grid-cols-4">
        <Card className="p-4">
          <div className="text-xs text-muted-foreground">合计人民币</div>
          <div className="mt-1 text-xl font-semibold tnum">{cny(data?.totals.cny)}</div>
        </Card>
        <Card className="p-4">
          <div className="text-xs text-muted-foreground">合计美元</div>
          <div className="mt-1 text-xl font-semibold tnum">{usd(data?.totals.usd)}</div>
        </Card>
        <Card className="p-4">
          <div className="text-xs text-muted-foreground">Key 数</div>
          <div className="mt-1 text-xl font-semibold tnum">{data?.totals.keys ?? 0}</div>
        </Card>
        <Card className="p-4">
          <div className="text-xs text-muted-foreground">汇率</div>
          <div className="mt-1 text-xl font-semibold tnum">{data?.rate ? `1 USD = ${data.rate} CNY` : '—'}</div>
        </Card>
      </div>

      <Card className="space-y-3 p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="text-xs text-muted-foreground">
            {data?.generated_at
              ? `最近更新 ${new Date(data.generated_at * 1000).toLocaleString('zh-CN', { hour12: false })}，每 30 秒自动刷新`
              : '加载中…'}
          </div>
          <Button variant="outline" size="sm" onClick={load} disabled={loading}>
            <RefreshCw className="size-4" />
            刷新
          </Button>
        </div>
        <div className="overflow-x-auto rounded-lg border border-border">
          <table className="w-full text-left text-xs">
            <thead className="bg-muted/50 text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">Key / 上号 ID</th>
                <th className="px-3 py-2 font-medium">厂商</th>
                {isAdmin && <th className="px-3 py-2 font-medium">上传人</th>}
                <th className="px-3 py-2 font-medium">上号时间</th>
                <th className="px-3 py-2 font-medium">平台状态</th>
                <th className="px-3 py-2 text-right font-medium">RPM</th>
                <th className="px-3 py-2 text-right font-medium">TPM</th>
                {PLATFORMS.map((p) => (
                  <th key={p} className="px-3 py-2 text-right font-medium">
                    {p} 平台
                  </th>
                ))}
                <th className="px-3 py-2 text-right font-medium">合计</th>
                <th className="px-3 py-2 font-medium">汇总状态</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((a) => (
                <tr key={a.id} className="border-t border-border align-top">
                  <td className="px-3 py-2">
                    <div className="font-medium">{a.alias || `上号 ID ${a.upstream_id ?? '—'}`}</div>
                    <div className="font-mono text-muted-foreground">上号 ID {a.upstream_id ?? '—'}</div>
                  </td>
                  <td className="px-3 py-2">{a.provider}</td>
                  {isAdmin && <td className="px-3 py-2">{a.uploader || '—'}</td>}
                  <td className="whitespace-nowrap px-3 py-2 text-muted-foreground">
                    {a.created_at ? new Date(a.created_at).toLocaleString('zh-CN', { hour12: false }) : '—'}
                  </td>
                  <td className="px-3 py-2">
                    <Badge tone={STATUS_TONE[a.status?.tone ?? ''] ?? 'neutral'}>{a.status?.label || '暂无数据'}</Badge>
                  </td>
                  <td className="px-3 py-2 text-right tnum">{rate(a.rpm)}</td>
                  <td className="px-3 py-2 text-right tnum">{rate(a.tpm)}</td>
                  {PLATFORMS.map((p) => {
                    const item = a.platforms?.[p]
                    return (
                      <td key={p} className="whitespace-nowrap px-3 py-2 text-right tnum">
                        {item ? (
                          <>
                            <div>{cny(item.cny)}</div>
                            <div className="text-muted-foreground">{usd(item.usd)}</div>
                          </>
                        ) : (
                          '—'
                        )}
                      </td>
                    )
                  })}
                  <td className="whitespace-nowrap px-3 py-2 text-right tnum">
                    <div className="font-medium">{cny(a.cny)}</div>
                    <div className="text-muted-foreground">{usd(a.usd)}</div>
                  </td>
                  <td className="px-3 py-2">
                    {a.error ? (
                      <Badge tone="danger">{a.error}</Badge>
                    ) : a.complete ? (
                      <Badge tone="success">已汇总</Badge>
                    ) : (
                      <Badge tone="warning">
                        {a.days_done ?? 0}/{a.days_expected ?? 0} 天
                      </Badge>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {rows.length === 0 && (
            <p className="p-6 text-center text-xs text-muted-foreground">
              {loading ? '加载中…' : '还没有成功上号的 Key'}
            </p>
          )}
        </div>
      </Card>
    </Layout>
  )
}
