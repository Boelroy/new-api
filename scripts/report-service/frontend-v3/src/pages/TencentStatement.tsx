import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Download, Plus, Trash2 } from 'lucide-react'
import Layout from '../components/Layout'
import { lastBeijingMonth } from '../lib/month'
import { Badge, Button, Card, Input, Select } from '../components/ui'
import { confirmDialog, toast } from '../components/feedback'
import {
  api,
  type RemoteProfile,
  type TencentConfigResponse,
  type TencentLine,
  type TencentMonthConfig,
  type TencentPricing,
  type TencentSnapshot,
  type TencentStatementPreview,
  type TencentSummaryRow,
  type TencentVendorConfig,
} from '../api'

// 腾讯一站 · 月对账 — the statement we (长河理想) settle with Tencent each
// month, laid out like 长河理想公司10月对账单: 「汇总表」 (one row per vendor,
// 开票金额合计, the parameters) and 「API账单」 (A–M). A month with a snapshot
// shows the snapshot (what is exported); otherwise the live preview. The
// month's config — company, BOC rate, CCMax divisor and each vendor's
// pricing / discount — is edited below and inherited by later months.

const STATEMENT_HEADERS = [
  '供应商名称', '账单月份', '模型厂商名称', '模型名称', '账号ID', 'key',
  '付给官方的费用（不区分币种）', '税率', '对应折扣', '腾讯支付金额（不区分币种）',
  '参考汇率\nhttps://www.kylc.com/huilv/d-boc-usd.html', '开票金额（RMB/元）',
  '官方导出的账单名称',
]
const SUMMARY_HEADERS = ['供应商名称', '账单月份', '模型厂商名称', '付给官方的费用（不区分币种）', '开票金额（RMB/元）', '备注']

const PRICING_LABEL: Record<TencentPricing, string> = {
  usd: '美元（G×折扣×汇率）',
  ccmax: 'CCMax（G÷除数×折扣×汇率）',
  rmb: '人民币（G×折扣）',
}

// money shows an amount with 2 decimals and its currency, as the xlsx's
// number formats do.
function money(v: number, currency: 'USD' | 'RMB'): string {
  return `${v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })} ${currency}`
}
const pct = (v: number) => `${Math.round(v * 100)}%`

// The currencies of G and J by pricing: usd rows are USD throughout, CC Max
// rows turn RMB into USD in J, RMB rows stay RMB.
function currencies(p: TencentPricing): ['USD' | 'RMB', 'USD' | 'RMB'] {
  if (p === 'usd') return ['USD', 'USD']
  if (p === 'ccmax') return ['RMB', 'USD']
  return ['RMB', 'RMB']
}

export default function TencentStatement() {
  const [month, setMonth] = useState(lastBeijingMonth())
  const [vendor, setVendor] = useState('')
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
  const lines = snapshot ? snapshot.lines : (preview?.lines ?? [])
  const summary = snapshot ? snapshot.summary : (preview?.summary ?? [])
  const total = snapshot ? snapshot.total_invoice_rmb : (preview?.total_invoice_rmb ?? 0)
  const config = snapshot ? snapshot.statement.meta.config : preview?.config
  const warnings = snapshot ? (snapshot.statement.warnings ?? []) : (preview?.warnings ?? [])
  const vendors = useMemo(() => summary.map((s) => s.vendor), [summary])

  async function snapshotAction(action: 'generate' | 'regenerate' | 'confirm') {
    if (action === 'confirm') {
      const ok = await confirmDialog({
        title: '确认对账单',
        message: `确认后 ${month} 的对账单不再随源数据和参数变化，也不能重新生成。确认？`,
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
    <Layout title="腾讯一站 · 月对账" subtitle="对腾讯的月对账单：按厂商计价与折扣，版式与导出的 xlsx 一致">
      <Card className="mb-4 flex flex-wrap items-end gap-3 p-4">
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">账单月份</span>
          <Input aria-label="账单月份" type="month" className="w-40" value={month} onChange={(e) => setMonth(e.target.value)} />
        </label>
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">按厂商导出</span>
          <Select aria-label="厂商" className="block" value={vendor} onChange={(e) => setVendor(e.target.value)}>
            <option value="">全部厂商</option>
            {vendors.map((name) => (
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
          <ExportLink href={snapshot ? api.tencentExportUrl(month) : ''}>导出 xlsx</ExportLink>
          {vendor && <ExportLink href={snapshot ? api.tencentExportUrl(month, vendor) : ''}>导出 {vendor}</ExportLink>}
        </div>
      </Card>

      {warnings.length > 0 && (
        <Card className="mb-4 space-y-1 border-warning/40 p-3 text-xs text-warning">
          {warnings.map((w) => (
            <div key={w}>{w}</div>
          ))}
        </Card>
      )}

      <Card className="mb-4 overflow-hidden p-0">
        <div className="border-b border-border px-4 py-2.5 text-sm font-semibold">汇总表</div>
        <SummaryTable rows={summary} total={total} />
        {config && <ParameterNotes month={month} config={config} summary={summary} />}
      </Card>

      <Card className="mb-4 overflow-hidden p-0">
        <div className="border-b border-border px-4 py-2.5 text-sm font-semibold">API账单（{lines.length} 行）</div>
        <LinesTable lines={lines} />
      </Card>

      <MonthConfigCard month={month} locked={status === 'confirmed'} onSaved={() => void load()} />
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

function SummaryTable(props: { rows: TencentSummaryRow[]; total: number }) {
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
              <tr key={s.vendor} className="border-t border-border">
                <td className="px-3 py-1.5">{s.supplier}</td>
                <td className="px-3 py-1.5 tnum">{s.bill_month}</td>
                <td className="px-3 py-1.5">{s.vendor}</td>
                <td className="whitespace-nowrap px-3 py-1.5 tnum">{money(s.official, currencies(s.pricing)[0])}</td>
                <td className="whitespace-nowrap px-3 py-1.5 tnum">{money(s.invoice_rmb, 'RMB')}</td>
                <td className="px-3 py-1.5">{s.note}</td>
              </tr>
            ))
          )}
          <tr className="border-t border-border font-semibold">
            <td className="px-3 py-1.5">开票金额合计</td>
            <td colSpan={3} />
            <td className="whitespace-nowrap px-3 py-1.5 tnum">{money(props.total, 'RMB')}</td>
            <td />
          </tr>
        </tbody>
      </table>
    </div>
  )
}

// ParameterNotes shows the 汇总表's parameter block and notes: the rate,
// divisor and discounts the amounts were priced with.
function ParameterNotes(props: { month: string; config: TencentMonthConfig; summary: TencentSummaryRow[] }) {
  const c = props.config
  const [y, m] = props.month.split('-').map(Number)
  const next = new Date(Date.UTC(y, m, 1))
  const end = `${next.getUTCFullYear()}-${String(next.getUTCMonth() + 1).padStart(2, '0')}-01`
  const lastDay = new Date(Date.UTC(y, m, 0)).getUTCDate()
  const discounts = c.vendors.filter((v) => props.summary.some((s) => s.vendor === v.vendor) || v.discount != null)
  return (
    <div className="space-y-1 border-t border-border px-4 py-3 text-xs text-muted-foreground" aria-label="参数区">
      <div className="flex flex-wrap gap-x-6 gap-y-1 text-foreground tnum">
        <span>
          {c.boc_rate_date} 现汇卖出价 <b>{c.boc_rate.toFixed(4)}</b>
        </span>
        <span>
          CCMax RMB/USD 除数 <b>{c.ccmax_divisor}</b>
        </span>
        {discounts.map((v) => (
          <span key={v.vendor}>
            {v.label || `${v.vendor} 折扣`} <b>{v.discount == null ? '未配置' : pct(v.discount)}</b>
          </span>
        ))}
      </div>
      <div>
        计费期间：{props.month}-01 00:00至{end} 00:00（北京时间，结束不含）；计算保留原精度，金额显示2位，按来源及模型厂商汇总后四舍五入到分。
      </div>
      <div>
        汇率来源：https://www.kylc.com/huilv/d-boc-usd.html（{c.boc_rate_date}）。账单期：{y}年{m}月1日—{m}月{lastDay}日（北京时间）。原始账单作为独立附件。
      </div>
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
            props.lines.map((l) => {
              const [g, j] = currencies(l.pricing)
              return (
                <tr key={l.line_key} className="border-t border-border">
                  <td className="whitespace-nowrap px-3 py-1.5">{l.supplier}</td>
                  <td className="px-3 py-1.5 tnum">{l.bill_month}</td>
                  <td className="px-3 py-1.5">{l.vendor}</td>
                  <td className="min-w-48 px-3 py-1.5 font-mono">{l.model}</td>
                  <td className="min-w-32 px-3 py-1.5 font-mono">{l.account_id}</td>
                  <td className="whitespace-nowrap px-3 py-1.5 font-mono">{l.key}</td>
                  <td className="whitespace-nowrap px-3 py-1.5 tnum">{money(l.official, g)}</td>
                  <td className="px-3 py-1.5">{l.tax_rate}</td>
                  <td className="px-3 py-1.5 tnum">{pct(l.discount)}</td>
                  <td className="whitespace-nowrap px-3 py-1.5 tnum">{money(l.tencent, j)}</td>
                  <td className="px-3 py-1.5 tnum">{l.ref_fx_rate.toFixed(4)}</td>
                  <td className="whitespace-nowrap px-3 py-1.5 tnum">{money(l.invoice_rmb, 'RMB')}</td>
                  <td className="whitespace-nowrap px-3 py-1.5">{l.bill_name}</td>
                </tr>
              )
            })
          )}
        </tbody>
      </table>
    </div>
  )
}

// The editable form of a vendor row; numbers stay text until saved.
type VendorForm = Omit<TencentVendorConfig, 'discount'> & { discount: string }

// MonthConfigCard edits the month's config. Saving stores it for this month;
// later months without their own config follow it.
function MonthConfigCard(props: { month: string; locked: boolean; onSaved: () => void }) {
  const [info, setInfo] = useState<TencentConfigResponse | null>(null)
  const [form, setForm] = useState({ company: '', rate: '', rateDate: '', divisor: '' })
  const [vendors, setVendors] = useState<VendorForm[]>([])
  const [saving, setSaving] = useState(false)

  const reset = useCallback((res: TencentConfigResponse) => {
    const c = res.config
    setInfo(res)
    setForm({ company: c.company, rate: String(c.boc_rate), rateDate: c.boc_rate_date, divisor: String(c.ccmax_divisor) })
    setVendors(c.vendors.map((v) => ({ ...v, discount: v.discount == null ? '' : String(v.discount) })))
  }, [])
  useEffect(() => {
    if (!/^\d{4}-\d{2}$/.test(props.month)) return
    api.tencentConfig(props.month).then(reset, (e) => toast.error(e))
  }, [props.month, reset])

  function setVendor(i: number, patch: Partial<VendorForm>) {
    setVendors((vs) => vs.map((v, j) => (j === i ? { ...v, ...patch } : v)))
  }

  async function save() {
    const rate = Number(form.rate)
    const divisor = Number(form.divisor)
    if (!(rate > 0 && rate <= 100) || !(divisor > 0 && divisor <= 100)) {
      toast.error('中行汇率和 CCMax 除数须为 0–100 之间的数字')
      return
    }
    const parsed: TencentVendorConfig[] = []
    for (const v of vendors) {
      const discount = v.discount.trim() === '' ? null : Number(v.discount)
      if (discount !== null && !(discount > 0 && discount <= 1)) {
        toast.error(`厂商 ${v.vendor || '（未填）'} 的折扣须为 0–1 之间的小数，如 0.73`)
        return
      }
      parsed.push({ ...v, vendor: v.vendor.trim(), discount })
    }
    setSaving(true)
    try {
      const config: TencentMonthConfig = {
        company: form.company.trim(),
        boc_rate: rate,
        boc_rate_date: form.rateDate.trim(),
        ccmax_divisor: divisor,
        vendors: parsed,
      }
      const res = await api.setTencentConfig(props.month, config)
      reset({ ...(info as TencentConfigResponse), config: res.config, from: 'month', inherited: props.month })
      toast.success('已保存到本月，草稿需「重新生成」后生效')
      props.onSaved()
    } catch (e) {
      toast.error(e)
    } finally {
      setSaving(false)
    }
  }

  const from =
    info?.from === 'month' ? '本月配置' : info?.from === 'inherit' ? `沿用 ${info.inherited} 的配置` : '默认配置（10 月参数）'
  const field = (key: keyof typeof form, label: string, width = 'w-36', placeholder?: string) => (
    <label className="space-y-1.5">
      <span className="block text-xs font-medium text-muted-foreground">{label}</span>
      <Input
        aria-label={label}
        className={width}
        value={form[key]}
        placeholder={placeholder}
        disabled={props.locked}
        onChange={(e) => setForm({ ...form, [key]: e.target.value })}
      />
    </label>
  )
  return (
    <Card className="space-y-3 p-4">
      <div className="flex flex-wrap items-baseline gap-2">
        <div className="text-sm font-semibold">{props.month} 账单参数</div>
        {info && <Badge>{from}</Badge>}
      </div>
      <div className="flex flex-wrap items-end gap-3">
        {field('company', '公司名（A 列）', 'w-40')}
        {field('rate', '中行美元现汇卖出价（K）')}
        {field('rateDate', '取价日期', 'w-36', info?.default_rate_date)}
        {field('divisor', 'CCMax 除数')}
      </div>
      {info && (
        <p className="text-xs text-muted-foreground">
          汇率取结算月次月 1 日（{info.default_rate_date}）的中行美元现汇卖出价：
          <a className="text-primary hover:underline" href={info.boc_rate_source_url} target="_blank" rel="noreferrer">
            {info.boc_rate_source_url}
          </a>
        </p>
      )}
      <div className="overflow-x-auto">
        <table className="w-full text-left text-xs">
          <thead className="text-muted-foreground">
            <tr>
              {['厂商（C 列）', '计价方式', '折扣', '税率（H 列）', '汇总表备注', '参数区名称', ''].map((h) => (
                <th key={h} className="px-2 py-1.5 font-medium">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {vendors.map((v, i) => (
              <tr key={i} className="border-t border-border">
                <td className="px-2 py-1">
                  <Input aria-label={`厂商 ${i + 1}`} className="w-32" value={v.vendor} disabled={props.locked} onChange={(e) => setVendor(i, { vendor: e.target.value })} />
                </td>
                <td className="px-2 py-1">
                  <Select
                    aria-label={`计价方式 ${v.vendor}`}
                    value={v.pricing}
                    disabled={props.locked}
                    onChange={(e) => setVendor(i, { pricing: e.target.value as TencentPricing })}
                  >
                    {(Object.keys(PRICING_LABEL) as TencentPricing[]).map((p) => (
                      <option key={p} value={p}>
                        {PRICING_LABEL[p]}
                      </option>
                    ))}
                  </Select>
                </td>
                <td className="px-2 py-1">
                  <Input
                    aria-label={`折扣 ${v.vendor}`}
                    className="w-20"
                    value={v.discount}
                    placeholder="未配置"
                    disabled={props.locked}
                    onChange={(e) => setVendor(i, { discount: e.target.value })}
                  />
                </td>
                <td className="px-2 py-1">
                  <Input aria-label={`税率 ${v.vendor}`} className="w-24" value={v.tax_text} disabled={props.locked} onChange={(e) => setVendor(i, { tax_text: e.target.value })} />
                </td>
                <td className="px-2 py-1">
                  <Input aria-label={`备注 ${v.vendor}`} className="w-72" value={v.note} disabled={props.locked} onChange={(e) => setVendor(i, { note: e.target.value })} />
                </td>
                <td className="px-2 py-1">
                  <Input aria-label={`参数区名称 ${v.vendor}`} className="w-36" value={v.label} disabled={props.locked} onChange={(e) => setVendor(i, { label: e.target.value })} />
                </td>
                <td className="px-2 py-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={`删除 ${v.vendor}`}
                    disabled={props.locked}
                    onClick={() => setVendors((vs) => vs.filter((_, j) => j !== i))}
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          variant="outline"
          disabled={props.locked}
          onClick={() =>
            setVendors((vs) => [...vs, { vendor: '', pricing: 'rmb', discount: '', tax_text: '不另加税费', note: '', label: '' }])
          }
        >
          <Plus className="size-3.5" /> 添加厂商
        </Button>
        <Button disabled={saving || props.locked} onClick={() => void save()}>
          保存到本月
        </Button>
        <span className="text-xs text-muted-foreground">
          没配折扣的厂商按 100% 计算并提示；新月份没有自己的配置时沿用上月；已确认的月份不能再改。
        </span>
      </div>
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
