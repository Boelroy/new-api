import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Download } from 'lucide-react'
import Layout from '../components/Layout'
import { lastBeijingMonth } from '../lib/month'
import { Badge, Button, Card, Input, Select } from '../components/ui'
import { confirmDialog, toast } from '../components/feedback'
import { api, type TencentLine, type TencentMonthParam, type TencentSnapshot, type TencentStatementPreview, type TencentSummaryRow, type RemoteProfile } from '../api'

// 腾讯一站 · 月对账 — the month's bill itself: the 「汇总表」 and 「API账单」
// sheets of the customer template (same columns), then 生成 → 草稿 → 确认
// and the xlsx export. A month with a snapshot shows the snapshot (what is
// exported); otherwise the live preview. The month's rates, discount and tax
// rate are the only inputs.

// The template's header rows; K carries the BOC rate page on a second line.
const STATEMENT_HEADERS = [
  '供应商名称', '账单月份', '模型厂商名称', '模型名称', '账号ID', 'key',
  '付给官方的费用（不区分币种）', '税率', '对应折扣', '腾讯支付金额（不区分币种）',
  '参考汇率\nhttps://www.kylc.com/huilv/d-boc-usd.html', '开票金额（RMB/元）',
  '官方导出的账单名称', '来源系统 / 上号人',
]
const SUMMARY_HEADERS = ['供应商名称', '账单月份', '模型厂商名称', '付给官方的费用（不区分币种）', '开票金额（RMB/元）', '备注']

const usd = (v: number) => `${v.toFixed(2)} USD`
const rmb = (v: number) => v.toFixed(2)

export default function TencentStatement() {
  const [month, setMonth] = useState(lastBeijingMonth())
  const [supplier, setSupplier] = useState('')
  const [preview, setPreview] = useState<TencentStatementPreview | null>(null)
  const [snapshot, setSnapshot] = useState<TencentSnapshot | null>(null)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const validMonth = /^\d{4}-\d{2}$/.test(month)

  const load = useCallback(async () => {
    if (!validMonth) return
    setLoading(true)
    try {
      // 404: the month has no snapshot yet.
      const [p, snap] = await Promise.all([api.tencentStatementPreview(month), api.tencentSnapshot(month).catch(() => null)])
      setPreview(p)
      setSnapshot(snap)
    } catch (e) {
      toast.error(e)
    } finally {
      setLoading(false)
    }
  }, [month, validMonth])
  useEffect(() => {
    void load()
  }, [load])

  const status = snapshot?.statement.status
  const allLines = snapshot ? snapshot.lines : (preview?.lines ?? [])
  const suppliers = useMemo(() => [...new Set(allLines.map((l) => l.supplier))].sort(), [allLines])
  const lines = useMemo(() => allLines.filter((l) => !supplier || l.supplier === supplier), [allLines, supplier])
  const summary = useMemo(
    () => (snapshot ? snapshot.summary : (preview?.summary ?? [])).filter((s) => !supplier || s.supplier === supplier),
    [snapshot, preview, supplier],
  )

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
    <Layout title="腾讯一站 · 月对账" subtitle="老台子 / CCMax / New API proxy 三个来源，按北京时间自然月出账，与对账单 Excel 同格式">
      <Card className="mb-4 flex flex-wrap items-end gap-3 p-4">
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">账单月份</span>
          <Input aria-label="账单月份" type="month" className="w-40" value={month} onChange={(e) => setMonth(e.target.value)} />
        </label>
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">供应商</span>
          <Select aria-label="供应商" className="block" value={supplier} onChange={(e) => setSupplier(e.target.value)}>
            <option value="">全部供应商</option>
            {suppliers.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </Select>
        </label>
        <Button variant="outline" disabled={loading} onClick={() => void load()}>
          {loading ? '加载中…' : '刷新'}
        </Button>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <Badge tone={status === 'confirmed' ? 'success' : status === 'draft' ? 'warning' : 'neutral'}>
            {status === 'confirmed' ? '已确认' : status === 'draft' ? '草稿' : '未生成（实时预览）'}
          </Badge>
          {!snapshot ? (
            <Button disabled={busy || !validMonth} onClick={() => void snapshotAction('generate')}>
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
          {supplier && <ExportLink href={snapshot ? api.tencentExportUrl(month, supplier) : ''}>导出 {supplier}</ExportLink>}
        </div>
      </Card>

      {preview && preview.warnings.length > 0 && !snapshot && (
        <Card className="mb-4 space-y-1 border-warning/40 p-3 text-xs text-warning">
          {preview.warnings.map((w) => (
            <div key={w}>{w}</div>
          ))}
        </Card>
      )}

      <Card className="mb-4 overflow-hidden p-0">
        <div className="border-b border-border px-4 py-2.5 text-sm font-semibold">汇总表</div>
        <SummaryTable rows={summary} />
      </Card>

      <Card className="mb-4 overflow-hidden p-0">
        <div className="border-b border-border px-4 py-2.5 text-sm font-semibold">API账单（{lines.length} 行）</div>
        <LinesTable lines={lines} />
      </Card>

      <MonthParamsCard month={month} preview={preview} locked={status === 'confirmed'} onSaved={() => void load()} />
      <ProxyUpstreamCard onSaved={() => void load()} />
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

function HeaderRow(props: { headers: string[] }) {
  return (
    <thead className="bg-muted/50 text-muted-foreground">
      <tr>
        {props.headers.map((h) => (
          <th key={h} className="whitespace-pre-line px-3 py-2 align-top font-medium">
            {h}
          </th>
        ))}
      </tr>
    </thead>
  )
}

function SummaryTable(props: { rows: TencentSummaryRow[] }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-xs">
        <HeaderRow headers={SUMMARY_HEADERS} />
        <tbody>
          {props.rows.length === 0 ? (
            <tr>
              <td colSpan={SUMMARY_HEADERS.length} className="px-3 py-6 text-center text-muted-foreground">
                本月没有账单。
              </td>
            </tr>
          ) : (
            props.rows.map((s) => (
              <tr key={`${s.supplier}|${s.vendor}|${s.source}`} className="border-t border-border">
                <td className="px-3 py-1.5">{s.supplier}</td>
                <td className="px-3 py-1.5 tnum">{s.bill_month}</td>
                <td className="px-3 py-1.5">{s.vendor}</td>
                <td className="px-3 py-1.5 tnum">{usd(s.official_usd)}</td>
                <td className="px-3 py-1.5 tnum">{rmb(s.invoice_rmb)}</td>
                <td className="px-3 py-1.5" />
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  )
}

function LinesTable(props: { lines: TencentLine[] }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-xs">
        <HeaderRow headers={STATEMENT_HEADERS} />
        <tbody>
          {props.lines.length === 0 ? (
            <tr>
              <td colSpan={STATEMENT_HEADERS.length} className="px-3 py-6 text-center text-muted-foreground">
                本月没有账单。
              </td>
            </tr>
          ) : (
            props.lines.map((l) => (
              <tr key={l.line_key} className="border-t border-border">
                <td className="whitespace-nowrap px-3 py-1.5">{l.supplier}</td>
                <td className="px-3 py-1.5 tnum">{l.bill_month}</td>
                <td className="px-3 py-1.5">{l.vendor}</td>
                <td className="min-w-48 px-3 py-1.5 font-mono">{l.model}</td>
                <td className="px-3 py-1.5 font-mono">{l.account_id}</td>
                <td className="whitespace-nowrap px-3 py-1.5 font-mono">{l.key}</td>
                <td className="whitespace-nowrap px-3 py-1.5 tnum">{usd(l.official_usd)}</td>
                <td className="px-3 py-1.5">{l.tax_rate}</td>
                <td className="px-3 py-1.5 tnum">{Math.round(l.discount * 100)}%</td>
                <td className="whitespace-nowrap px-3 py-1.5 tnum">{usd(l.tencent_usd)}</td>
                <td className="px-3 py-1.5 tnum">{l.ref_fx_rate}</td>
                <td className="px-3 py-1.5 tnum">{rmb(l.invoice_rmb)}</td>
                <td className="whitespace-nowrap px-3 py-1.5">{l.bill_name}</td>
                <td className="whitespace-nowrap px-3 py-1.5">{l.source}</td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  )
}

// MonthParamsCard edits the month's only inputs. Empty means the default
// shown as the placeholder.
function MonthParamsCard(props: { month: string; preview: TencentStatementPreview | null; locked: boolean; onSaved: () => void }) {
  const [saved, setSaved] = useState<TencentMonthParam | null>(null)
  const [form, setForm] = useState({ fx: '', ref: '', discount: '', tax: '' })
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api.tencentMonthParams().then(
      (res) => setSaved(res.list.find((p) => p.month === props.month) ?? null),
      (e) => toast.error(e),
    )
  }, [props.month])
  useEffect(() => {
    setForm({
      fx: saved?.fx_rate != null ? String(saved.fx_rate) : '',
      ref: saved?.ref_fx_rate != null ? String(saved.ref_fx_rate) : '',
      discount: saved?.discount != null ? String(saved.discount) : '',
      tax: saved?.tax_rate ?? '',
    })
  }, [saved])

  async function save() {
    const num = (raw: string, maxValue: number) => {
      if (!raw.trim()) return null
      const v = Number(raw)
      return Number.isFinite(v) && v > 0 && v <= maxValue ? v : undefined
    }
    const fx = num(form.fx, 100)
    const ref = num(form.ref, 100)
    const discount = num(form.discount, 1)
    if (fx === undefined || ref === undefined) {
      toast.error('汇率须为 0–100 之间的数字，留空表示用默认值')
      return
    }
    if (discount === undefined) {
      toast.error('折扣须为 0–1 之间的小数（如 0.88），留空表示用默认值')
      return
    }
    setSaving(true)
    try {
      const payload = { month: props.month, fx_rate: fx, ref_fx_rate: ref, discount, tax_rate: form.tax.trim() || null }
      await api.setTencentMonthParam(payload)
      setSaved({ ...payload, note: '', updated_at: Date.now() / 1000 })
      toast.success('已保存，草稿需「重新生成」后生效')
      props.onSaved()
    } catch (e) {
      toast.error(e)
    } finally {
      setSaving(false)
    }
  }

  const p = props.preview
  const field = (key: keyof typeof form, label: string, placeholder: string) => (
    <label className="space-y-1.5">
      <span className="block text-xs font-medium text-muted-foreground">{label}</span>
      <Input
        aria-label={label}
        className="w-36"
        value={form[key]}
        placeholder={placeholder}
        disabled={props.locked}
        onChange={(e) => setForm({ ...form, [key]: e.target.value })}
      />
    </label>
  )
  return (
    <Card className="space-y-3 p-4">
      <div className="text-sm font-semibold">{props.month} 账单参数</div>
      <div className="flex flex-wrap items-end gap-3">
        {field('fx', '汇率（G = RMB ÷ 汇率）', p ? `默认 ${p.fx_rate}` : '默认系统汇率')}
        {field('ref', '参考汇率（K）', p ? `默认 ${p.ref_fx_rate}` : '默认同汇率')}
        {field('discount', '折扣（I）', p ? `默认 ${p.discount}` : '默认 0.88')}
        {field('tax', '税率（H）', p ? `默认 ${p.tax_rate}` : '默认 含票')}
        <Button disabled={saving || props.locked} onClick={() => void save()}>
          保存参数
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">留空用默认值；已确认的月份不能再改。</p>
    </Card>
  )
}

// ProxyUpstreamCard picks the remote New API profile that newapi-proxy
// forwards to; proxy rows query its consume logs with that profile's token.
function ProxyUpstreamCard(props: { onSaved: () => void }) {
  const [profiles, setProfiles] = useState<RemoteProfile[]>([])
  const [selected, setSelected] = useState('0')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    Promise.all([api.tencentSettings(), api.remoteProfiles()]).then(
      ([settings, res]) => {
        setProfiles(res.profiles)
        setSelected(String(settings.proxy_profile_id))
      },
      (e) => toast.error(e),
    )
  }, [])

  async function save() {
    setSaving(true)
    try {
      const res = await api.setTencentSettings(Number(selected))
      setSelected(String(res.proxy_profile_id))
      toast.success('已保存')
      props.onSaved()
    } catch (e) {
      toast.error(e)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card className="mt-4 space-y-3 p-4">
      <div className="text-sm font-semibold">New API proxy 上游</div>
      <div className="flex flex-wrap items-end gap-3">
        <label className="space-y-1.5">
          <span className="block text-xs font-medium text-muted-foreground">上游实例（远端 NewAPI 配置）</span>
          <Select aria-label="上游实例" className="block" value={selected} onChange={(e) => setSelected(e.target.value)}>
            <option value="0">未选择</option>
            {profiles.map((p) => (
              <option key={p.id} value={String(p.id)}>
                {p.name}
                {p.host ? `（${p.host}）` : ''}
              </option>
            ))}
          </Select>
        </label>
        <Button disabled={saving} onClick={() => void save()}>
          保存上游
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">按 proxy 渠道归属查该实例当月消费日志出账，地址、令牌、代理沿用该实例的配置。</p>
    </Card>
  )
}
