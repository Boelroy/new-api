import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Download } from 'lucide-react'
import Layout from '../components/Layout'
import { lastBeijingMonth } from '../lib/month'
import { Badge, Button, Card, Input, Select } from '../components/ui'
import { confirmDialog, toast } from '../components/feedback'
import {
  api,
  type TencentLine,
  type TencentMonthParam,
  type TencentSnapshot,
  type TencentStatementPreview,
  type TencentSupplierParam,
} from '../api'

// 腾讯一站 · 月对账 — the live preview of a Beijing month in the template's
// 「API账单」 (A–N) and 「汇总表」 layout, the parameters behind it (rates,
// discount, tax, RMB-priced rows, bill names) and the month-end snapshot:
// 生成 → 草稿 (重新生成 as often as needed) → 确认, then xlsx export.

const PLATFORM_LABEL: Record<string, string> = {
  third_party: '3P',
  ccmax: 'CC Max',
  proxy: 'Proxy',
  '*': '全部平台',
}

const usd = (v: number) => `$${v.toFixed(2)}`
const cny = (v: number) => `¥${v.toFixed(2)}`

function parseRate(raw: string): number | null | undefined {
  if (!raw.trim()) return null
  const v = Number(raw)
  return Number.isFinite(v) && v > 0 && v <= 100 ? v : undefined
}

export default function TencentStatement() {
  const [month, setMonth] = useState(lastBeijingMonth())
  const [supplier, setSupplier] = useState('')
  const [preview, setPreview] = useState<TencentStatementPreview | null>(null)
  const [snapshot, setSnapshot] = useState<TencentSnapshot | null>(null)
  const [monthParams, setMonthParams] = useState<TencentMonthParam[]>([])
  const [supplierParams, setSupplierParams] = useState<TencentSupplierParam[]>([])
  const [uploadDiscount, setUploadDiscount] = useState(0)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)

  const loadPreview = useCallback(async () => {
    if (!/^\d{4}-\d{2}$/.test(month)) return
    setLoading(true)
    try {
      const [p, mp, sp] = await Promise.all([
        api.tencentStatementPreview(month),
        api.tencentMonthParams(),
        api.tencentSupplierParams(),
      ])
      setPreview(p)
      setMonthParams(mp.list)
      setSupplierParams(sp.list)
      setUploadDiscount(sp.upload_discount_pct)
    } catch (e) {
      toast.error(e)
    } finally {
      setLoading(false)
    }
  }, [month])

  const loadSnapshot = useCallback(async () => {
    if (!/^\d{4}-\d{2}$/.test(month)) return
    try {
      setSnapshot(await api.tencentSnapshot(month))
    } catch {
      // 404: the month has no snapshot yet.
      setSnapshot(null)
    }
  }, [month])

  useEffect(() => {
    void loadPreview()
    void loadSnapshot()
  }, [loadPreview, loadSnapshot])

  const suppliers = useMemo(() => {
    const byName = new Map<string, number>()
    for (const l of preview?.lines ?? []) byName.set(l.supplier, l.user_id)
    for (const l of snapshot?.lines ?? []) if (!byName.has(l.supplier)) byName.set(l.supplier, l.user_id)
    return [...byName.entries()].sort((a, b) => a[0].localeCompare(b[0]))
  }, [preview, snapshot])

  const lines = useMemo(
    () => (preview?.lines ?? []).filter((l) => !supplier || l.supplier === supplier),
    [preview, supplier],
  )
  const summary = useMemo(
    () => (preview?.summary ?? []).filter((s) => !supplier || s.supplier === supplier),
    [preview, supplier],
  )
  const totals = useMemo(() => {
    let g = 0
    let l = 0
    for (const s of summary) {
      g += s.official_usd
      l += s.invoice_rmb
    }
    return { g, l }
  }, [summary])

  const status = snapshot?.statement.status
  async function snapshotAction(action: 'generate' | 'regenerate' | 'confirm') {
    if (action === 'confirm') {
      const ok = await confirmDialog({
        title: '确认对账单',
        message: `确认后 ${month} 的对账单不再随源数据变化，也不能重新生成。确认？`,
        confirmText: '确认对账单',
      })
      if (!ok) return
    }
    setBusy(true)
    try {
      setSnapshot(await api.tencentSnapshotAction(month, action))
      toast.success(action === 'confirm' ? '已确认' : '对账单已生成（草稿）')
    } catch (e) {
      toast.error(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Layout title="腾讯一站 · 月对账" subtitle="按北京时间自然月汇总 3P / CC Max / Proxy 明细，列与模板「API账单」一致">
      <Card className="mb-4 flex flex-wrap items-end gap-3 p-4">
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">账单月份</span>
          <Input aria-label="账单月份" type="month" className="w-40" value={month} onChange={(e) => setMonth(e.target.value)} />
        </label>
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">供应商</span>
          <Select aria-label="供应商" className="block" value={supplier} onChange={(e) => setSupplier(e.target.value)}>
            <option value="">全部供应商</option>
            {suppliers.map(([name]) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </Select>
        </label>
        <Button variant="outline" disabled={loading} onClick={() => void loadPreview()}>
          {loading ? '加载中…' : '刷新预览'}
        </Button>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <Badge tone={status === 'confirmed' ? 'success' : status === 'draft' ? 'warning' : 'neutral'}>
            {status === 'confirmed' ? '已确认' : status === 'draft' ? '草稿' : '未生成'}
          </Badge>
          {!snapshot ? (
            <Button disabled={busy} onClick={() => void snapshotAction('generate')}>
              生成对账单
            </Button>
          ) : (
            <Button
              variant="outline"
              disabled={busy || status === 'confirmed'}
              title={status === 'confirmed' ? '已确认的月份不能重新生成' : undefined}
              onClick={() => void snapshotAction('regenerate')}
            >
              重新生成
            </Button>
          )}
          <Button variant="confirm" disabled={busy || status !== 'draft'} onClick={() => void snapshotAction('confirm')}>
            确认
          </Button>
          <ExportLink href={snapshot ? api.tencentExportUrl(month) : ''}>导出全部 xlsx</ExportLink>
          {supplier && (
            <ExportLink href={snapshot ? api.tencentExportUrl(month, supplier) : ''}>导出 {supplier}</ExportLink>
          )}
        </div>
      </Card>

      {preview && preview.warnings.length > 0 && (
        <Card className="mb-4 space-y-1 border-warning/40 p-3 text-xs text-warning">
          {preview.warnings.map((w) => (
            <div key={w}>{w}</div>
          ))}
        </Card>
      )}

      <div className="mb-4 grid gap-4 lg:grid-cols-2">
        <MonthParamsCard
          month={month}
          preview={preview}
          saved={monthParams.find((p) => p.month === month) ?? null}
          onSaved={() => void loadPreview()}
        />
        <SupplierParamsCard
          suppliers={suppliers}
          params={supplierParams}
          uploadDiscount={uploadDiscount}
          onSaved={() => void loadPreview()}
        />
      </div>

      <Card className="mb-4 overflow-hidden p-0">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-2.5">
          <div className="text-sm font-semibold">汇总表（实时预览）</div>
          <div className="text-xs tnum text-muted-foreground">
            付给官方合计 <span className="font-semibold text-foreground">{usd(totals.g)}</span> · 开票合计{' '}
            <span className="font-semibold text-foreground">{cny(totals.l)}</span>
            {snapshot && (
              <span className="ml-2">
                （快照 {snapshot.statement.lines} 行）
              </span>
            )}
          </div>
        </div>
        <SummaryTable rows={summary} />
      </Card>

      <Card className="overflow-hidden p-0">
        <div className="border-b border-border px-4 py-2.5 text-sm font-semibold">
          API账单明细（{lines.length} 行，勾选「RMB」表示该行按人民币计价：L = J）
        </div>
        <LinesTable headers={preview?.headers ?? []} lines={lines} onChanged={() => void loadPreview()} />
      </Card>
    </Layout>
  )
}

function ExportLink(props: { href: string; children: ReactNode }) {
  if (!props.href) {
    return (
      <Button variant="outline" disabled title="先生成对账单">
        <Download className="size-3.5" /> {props.children}
      </Button>
    )
  }
  return (
    <a
      href={props.href}
      download
      className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-background px-3 text-sm font-medium hover:bg-muted"
    >
      <Download className="size-3.5" /> {props.children}
    </a>
  )
}

function MonthParamsCard(props: {
  month: string
  preview: TencentStatementPreview | null
  saved: TencentMonthParam | null
  onSaved: () => void
}) {
  const [fx, setFx] = useState('')
  const [ref, setRef] = useState('')
  const [saving, setSaving] = useState(false)
  useEffect(() => {
    setFx(props.saved?.fx_rate != null ? String(props.saved.fx_rate) : '')
    setRef(props.saved?.ref_fx_rate != null ? String(props.saved.ref_fx_rate) : '')
  }, [props.saved, props.month])

  async function save() {
    const fxRate = parseRate(fx)
    const refRate = parseRate(ref)
    if (fxRate === undefined || refRate === undefined) {
      toast.error('汇率须为 0–100 之间的数字，留空表示用默认值')
      return
    }
    setSaving(true)
    try {
      await api.setTencentMonthParam({ month: props.month, fx_rate: fxRate, ref_fx_rate: refRate })
      toast.success('已保存，预览已刷新')
      props.onSaved()
    } catch (e) {
      toast.error(e)
    } finally {
      setSaving(false)
    }
  }

  const p = props.preview
  return (
    <Card className="space-y-3 p-4">
      <div className="text-sm font-semibold">{props.month} 汇率</div>
      <div className="flex flex-wrap items-end gap-3">
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">系统汇率（G = CNY ÷ 汇率）</span>
          <Input
            aria-label="系统汇率"
            className="w-36"
            value={fx}
            placeholder={p ? `默认 ${p.fx_rate}` : '默认'}
            onChange={(e) => setFx(e.target.value)}
          />
        </label>
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">参考汇率 K（中行 USD）</span>
          <Input
            aria-label="参考汇率"
            className="w-36"
            value={ref}
            placeholder={p ? `默认 ${p.ref_fx_rate}` : '默认'}
            onChange={(e) => setRef(e.target.value)}
          />
        </label>
        <Button disabled={saving} onClick={() => void save()}>
          保存汇率
        </Button>
      </div>
      {p && (
        <p className="text-xs text-muted-foreground tnum">
          当前使用：系统汇率 {p.fx_rate}（{p.fx_rate_from === 'month' ? '本月设置' : '系统默认'}）· 参考汇率 {p.ref_fx_rate}（
          {p.ref_fx_rate_from === 'month' ? '本月设置' : '同系统汇率'}）
        </p>
      )}
    </Card>
  )
}

function SupplierParamsCard(props: {
  suppliers: [string, number][]
  params: TencentSupplierParam[]
  uploadDiscount: number
  onSaved: () => void
}) {
  const [userId, setUserId] = useState(0)
  const [platform, setPlatform] = useState('*')
  const [discount, setDiscount] = useState('')
  const [tax, setTax] = useState('')
  const [saving, setSaving] = useState(false)

  async function save() {
    const uid = userId || props.suppliers[0]?.[1] || 0
    if (!uid) {
      toast.error('请选择供应商')
      return
    }
    const pct = discount.trim() ? Number(discount) : null
    if (pct !== null && !(pct > 0 && pct <= 100)) {
      toast.error('折扣须为 0–100 之间的百分比，留空表示用默认值')
      return
    }
    setSaving(true)
    try {
      await api.setTencentSupplierParam({ user_id: uid, platform, discount_pct: pct, tax_rate: tax.trim() })
      toast.success('已保存，预览已刷新')
      setDiscount('')
      setTax('')
      props.onSaved()
    } catch (e) {
      toast.error(e)
    } finally {
      setSaving(false)
    }
  }

  async function remove(p: TencentSupplierParam) {
    try {
      await api.deleteTencentSupplierParam(p.user_id, p.platform)
      props.onSaved()
    } catch (e) {
      toast.error(e)
    }
  }

  return (
    <Card className="space-y-3 p-4">
      <div className="text-sm font-semibold">供应商折扣 / 税率</div>
      <div className="flex flex-wrap items-end gap-3">
        <Select aria-label="折扣供应商" value={String(userId)} onChange={(e) => setUserId(Number(e.target.value))}>
          {props.suppliers.map(([name, id]) => (
            <option key={name} value={String(id)}>
              {name}
            </option>
          ))}
        </Select>
        <Select aria-label="折扣平台" value={platform} onChange={(e) => setPlatform(e.target.value)}>
          {['*', 'third_party', 'ccmax', 'proxy'].map((p) => (
            <option key={p} value={p}>
              {PLATFORM_LABEL[p]}
            </option>
          ))}
        </Select>
        <Input aria-label="折扣" className="w-28" value={discount} placeholder="折扣 %" onChange={(e) => setDiscount(e.target.value)} />
        <Input aria-label="税率" className="w-28" value={tax} placeholder="税率，如 6%" onChange={(e) => setTax(e.target.value)} />
        <Button disabled={saving} onClick={() => void save()}>
          保存折扣
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        折扣取值顺序：供应商 × 平台 → 供应商全部平台 → 上号折扣（3P 普通号 {props.uploadDiscount}%）→ 100%。
      </p>
      {props.params.length > 0 && (
        <ul className="space-y-1 text-xs">
          {props.params.map((p) => (
            <li key={`${p.user_id}-${p.platform}`} className="flex items-center gap-2">
              <span className="font-medium">{p.studio || p.username}</span>
              <Badge>{PLATFORM_LABEL[p.platform] ?? p.platform}</Badge>
              <span className="tnum">{p.discount_pct != null ? `${p.discount_pct}%` : '默认折扣'}</span>
              {p.tax_rate && <span>税率 {p.tax_rate}</span>}
              <Button size="sm" variant="ghost" onClick={() => void remove(p)}>
                删除
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}

function SummaryTable(props: { rows: TencentStatementPreview['summary'] }) {
  if (props.rows.length === 0) return <p className="px-4 py-6 text-center text-xs text-muted-foreground">本月没有明细。</p>
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-xs">
        <thead className="bg-muted/50 text-muted-foreground">
          <tr>
            {['供应商名称', '账单月份', '模型厂商名称', '来源', '付给官方的费用', '开票金额', '行数'].map((h) => (
              <th key={h} className="whitespace-nowrap px-3 py-2 font-medium">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {props.rows.map((s) => (
            <tr key={`${s.supplier}|${s.vendor}|${s.source}`} className="border-t border-border">
              <td className="px-3 py-1.5">{s.supplier}</td>
              <td className="px-3 py-1.5 tnum">{s.bill_month}</td>
              <td className="px-3 py-1.5">{s.vendor}</td>
              <td className="px-3 py-1.5">{s.source}</td>
              <td className="px-3 py-1.5 tnum">{usd(s.official_usd)}</td>
              <td className="px-3 py-1.5 tnum">{cny(s.invoice_rmb)}</td>
              <td className="px-3 py-1.5 tnum">{s.lines}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function LinesTable(props: { headers: string[]; lines: TencentLine[]; onChanged: () => void }) {
  async function saveFlag(l: TencentLine, rmb: boolean, billName: string) {
    if (rmb === l.rmb_priced && billName.trim() === l.bill_name) return
    try {
      await api.setTencentLineFlag({ line_key: l.line_key, rmb_priced: rmb, bill_name: billName.trim() })
      props.onChanged()
    } catch (e) {
      toast.error(e)
    }
  }

  if (props.lines.length === 0) return <p className="px-4 py-6 text-center text-xs text-muted-foreground">本月没有明细。</p>
  // Headers come from the template; K carries a URL on a second line.
  const headers = props.headers.map((h) => h.split('\n')[0])
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-xs">
        <thead className="bg-muted/50 text-muted-foreground">
          <tr>
            {headers.map((h, i) => (
              <th key={h} className="whitespace-nowrap px-3 py-2 font-medium">
                {String.fromCharCode(65 + i)} {h}
              </th>
            ))}
            <th className="whitespace-nowrap px-3 py-2 font-medium">RMB</th>
          </tr>
        </thead>
        <tbody>
          {props.lines.map((l) => (
            <tr key={l.line_key} className="border-t border-border">
              <td className="whitespace-nowrap px-3 py-1.5">{l.supplier}</td>
              <td className="px-3 py-1.5 tnum">{l.bill_month}</td>
              <td className="px-3 py-1.5">{l.vendor}</td>
              <td className="whitespace-nowrap px-3 py-1.5 font-mono">{l.model}</td>
              <td className="px-3 py-1.5 font-mono">{l.account_id}</td>
              <td className="px-3 py-1.5 font-mono">{l.key}</td>
              <td className="px-3 py-1.5 tnum">{l.rmb_priced ? cny(l.official_usd) : usd(l.official_usd)}</td>
              <td className="px-3 py-1.5">{l.tax_rate}</td>
              <td className="px-3 py-1.5 tnum" title={`来源：${l.discount_from}`}>
                {Math.round(l.discount * 10000) / 100}%
              </td>
              <td className="px-3 py-1.5 tnum">{l.rmb_priced ? cny(l.tencent_usd) : usd(l.tencent_usd)}</td>
              <td className="px-3 py-1.5 tnum">{l.ref_fx_rate}</td>
              <td className="px-3 py-1.5 tnum">{cny(l.invoice_rmb)}</td>
              <td className="px-3 py-1.5">
                <Input
                  aria-label={`账单名称 ${l.line_key}`}
                  className="h-7 w-40"
                  defaultValue={l.bill_name}
                  onBlur={(e) => void saveFlag(l, l.rmb_priced, e.target.value)}
                />
              </td>
              <td className="whitespace-nowrap px-3 py-1.5">{l.source}</td>
              <td className="px-3 py-1.5">
                <Input
                  type="checkbox"
                  aria-label={`人民币计价 ${l.line_key}`}
                  checked={l.rmb_priced}
                  onChange={(e) => void saveFlag(l, e.target.checked, l.bill_name)}
                />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
