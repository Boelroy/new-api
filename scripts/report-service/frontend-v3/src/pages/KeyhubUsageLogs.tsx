import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { SlidersHorizontal } from 'lucide-react'
import Layout from '../components/Layout'
import { Button, Card, Input, Select } from '../components/ui'
import { toast } from '../components/feedback'
import {
  api,
  type KeyhubFilterOption,
  type KeyhubFilterOptions,
  type KeyhubLogQuery,
  type KeyhubLogRow,
} from '../api'

// 使用日志 — KHub (pd-maas) provider usage logs. The upstream REQUIRES at least
// one import_batch_id (400 「至少选择一个导入批次」 otherwise), so the batch
// picker gates the query.

function optionLabel(o: KeyhubFilterOption): string {
  if (o.label) return o.label
  if (o.value != null) return String(o.value)
  return ''
}
function optionValue(o: KeyhubFilterOption): string {
  return o.value != null ? String(o.value) : ''
}

// Batches come from pd-maas shaped as {id, category_label, key_count, tag,
// note, owner_name, ...} — no value/label — so the generic helpers render blank
// chips. Map id → value and a human note/tag (+ category + count) → label.
function batchValue(b: KeyhubFilterOption): string {
  const id = b.id ?? b.value
  return id != null ? String(id) : ''
}
function batchLabel(b: KeyhubFilterOption): string {
  const name = (b.note as string) || (b.tag as string) || ''
  const cat = (b.category_label as string) || (b.category_code as string) || ''
  const cnt = b.key_count != null ? `${b.key_count} key` : ''
  const idTail = typeof b.id === 'string' ? b.id.slice(-6) : ''
  const head = name || idTail || '(批次)'
  const meta = [cat, cnt].filter(Boolean).join(' · ')
  return meta ? `${head}（${meta}）` : head
}

function isoDaysAgo(days: number): string {
  const d = new Date()
  d.setDate(d.getDate() - days)
  return d.toISOString().slice(0, 10)
}
function toUnixSeconds(dateStr: string, endOfDay: boolean): number | undefined {
  if (!dateStr) return undefined
  const ms = new Date(dateStr + (endOfDay ? 'T23:59:59' : 'T00:00:00')).getTime()
  return Number.isNaN(ms) ? undefined : Math.floor(ms / 1000)
}

// Pull the row array out of the upstream data envelope, which may be either
// { items: [...] } or a bare array.
function extractRows(data: unknown): KeyhubLogRow[] {
  if (Array.isArray(data)) return data as KeyhubLogRow[]
  if (data && typeof data === 'object') {
    const items = (data as { items?: unknown }).items
    if (Array.isArray(items)) return items as KeyhubLogRow[]
  }
  return []
}
function extractTotal(data: unknown): number | undefined {
  if (data && typeof data === 'object') {
    const t = (data as { total?: unknown }).total
    if (typeof t === 'number') return t
  }
  return undefined
}

// ---- log table (curated, pretty columns instead of a raw key dump) ----

// use_time is a unix timestamp (seconds); render it in local time. Falls back to
// the raw string for ISO / unexpected shapes.
function fmtLogTime(v: unknown): string {
  let d: Date | null = null
  if (typeof v === 'number') d = new Date(v < 1e12 ? v * 1000 : v)
  else if (typeof v === 'string' && v.trim() !== '') {
    const n = Number(v)
    d = !Number.isNaN(n) ? new Date(n < 1e12 ? n * 1000 : n) : new Date(v)
  }
  if (!d || Number.isNaN(d.getTime())) return typeof v === 'string' ? v : ''
  const p = (x: number) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// tokens: prompt / completion when available, else the combined total.
function tokensText(r: Record<string, unknown>): string {
  const pt = statNum(r.prompt_tokens)
  const ct = statNum(r.completion_tokens)
  if (pt != null || ct != null) return `${pt ?? 0} / ${ct ?? 0}`
  const t = statNum(r.tokens)
  return t == null ? '—' : t.toLocaleString()
}

function Muted() {
  return <span className="text-muted-foreground/50">—</span>
}
function Pill({ children, tone = 'default' }: { children: ReactNode; tone?: 'default' | 'green' | 'muted' }) {
  const cls =
    tone === 'green'
      ? 'bg-emerald-50 text-emerald-700 ring-emerald-200'
      : tone === 'muted'
        ? 'bg-muted text-muted-foreground ring-border'
        : 'bg-background text-foreground ring-border'
  return <span className={'inline-flex items-center rounded-md px-1.5 py-0.5 text-[11px] ring-1 ' + cls}>{children}</span>
}

function cellText(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

type LogColumn = {
  key: string
  label: string
  defaultVisible: boolean
  render: (r: Record<string, unknown>) => ReactNode
}

// Curated columns. The first block (defaultVisible: true) mirrors the pd-maas
// native usage view; the rest ship hidden and are toggled on via 显示列. Any raw
// row key not covered here is discovered at runtime and appended as an optional
// (hidden-by-default) column, so nothing is lost.
const KNOWN_COLUMNS: LogColumn[] = [
  {
    key: 'use_time',
    label: '时间',
    defaultVisible: true,
    render: (r) => <span className="tnum whitespace-nowrap text-muted-foreground">{fmtLogTime(r.use_time)}</span>,
  },
  {
    key: 'remote_token_name',
    label: '令牌',
    defaultVisible: true,
    render: (r) => (r.remote_token_name ? <Pill tone="muted">{String(r.remote_token_name)}</Pill> : <Muted />),
  },
  {
    key: 'key_hint',
    label: 'Key',
    defaultVisible: true,
    render: (r) => (r.key_hint ? <span className="font-mono text-[11px]">{String(r.key_hint)}</span> : <Muted />),
  },
  {
    key: 'category',
    label: '类别',
    defaultVisible: true,
    render: (r) => {
      const t = (r.category_label as string) || (r.category_code as string) || ''
      return t ? <span>{t}</span> : <Muted />
    },
  },
  {
    key: 'model_name',
    label: '模型',
    defaultVisible: true,
    render: (r) => (r.model_name ? <Pill>{String(r.model_name)}</Pill> : <Muted />),
  },
  {
    key: 'is_stream',
    label: '流',
    defaultVisible: true,
    render: (r) => <span className="text-muted-foreground">{r.is_stream ? '流' : '非流'}</span>,
  },
  {
    key: 'tokens',
    label: 'Tokens',
    defaultVisible: true,
    render: (r) => <span className="tnum whitespace-nowrap">{tokensText(r)}</span>,
  },
  {
    key: 'raw_cost_usd',
    label: '费用',
    defaultVisible: true,
    render: (r) => <Pill tone="green">{fmtUSD(r.raw_cost_usd)}</Pill>,
  },
  {
    // content is often a long error string; cap the width and single-line
    // truncate so it doesn't blow the table out. Full text on hover.
    key: 'content',
    label: '内容',
    defaultVisible: true,
    render: (r) => {
      const t = cellText(r.content)
      return t ? (
        <span className="block max-w-[360px] truncate text-muted-foreground" title={t}>
          {t}
        </span>
      ) : (
        <Muted />
      )
    },
  },
  // --- optional (hidden by default) ---
  {
    key: 'category_code',
    label: '类别代码',
    defaultVisible: false,
    render: (r) => (r.category_code ? <span className="tnum">{String(r.category_code)}</span> : <Muted />),
  },
  {
    key: 'quota_per_unit',
    label: '单位配额',
    defaultVisible: false,
    render: (r) => <span className="tnum">{fmtInt(r.quota_per_unit)}</span>,
  },
  {
    key: '_token_id',
    label: '令牌 ID',
    defaultVisible: false,
    render: (r) => (r._token_id != null ? <span className="tnum">{String(r._token_id)}</span> : <Muted />),
  },
]

// Field names already surfaced (directly or derived) by KNOWN_COLUMNS, so they
// aren't re-added as generic discovered columns.
const CONSUMED_KEYS = new Set<string>([
  'use_time',
  'remote_token_name',
  'key_hint',
  'category',
  'category_label',
  'category_code',
  'model_name',
  'is_stream',
  'tokens',
  'prompt_tokens',
  'completion_tokens',
  'raw_cost_usd',
  'quota_per_unit',
  '_token_id',
  'content',
])

// v2: content promoted to a default column — bump the key so returning browsers
// pick up the new defaults instead of a stale stored set.
const COLS_STORAGE_KEY = 'keyhub_log_visible_cols_v2'

// ---- stat panel ----

type KeyhubStat = {
  quota?: number
  raw_cost_usd?: string | number
  request_count?: number
  tokens?: number
  rpm?: number
  tpm?: number
  sampled_at?: string
  rate_window_start?: string
  partial?: boolean
  failed_platform_count?: number
}

function statNum(v: unknown): number | undefined {
  if (typeof v === 'number') return v
  if (typeof v === 'string' && v.trim() !== '' && !Number.isNaN(Number(v))) return Number(v)
  return undefined
}
function fmtInt(v: unknown): string {
  const n = statNum(v)
  return n == null ? '—' : n.toLocaleString()
}
function fmtUSD(v: unknown): string {
  const n = statNum(v)
  if (n == null) return '—'
  if (n === 0) return '$0.00'
  return '$' + (n < 0.01 ? n.toFixed(6) : n.toFixed(2))
}
function fmtTime(v: unknown): string {
  if (typeof v !== 'string' || !v) return ''
  return v.replace('T', ' ').replace(/(\+|-)\d{2}:\d{2}$/, '').replace(/Z$/, '')
}

// StatPanel renders the usage stat object as labelled metric tiles instead of
// a raw JSON dump. Unknown/missing fields fall back to «—».
function StatPanel({ stat }: { stat: KeyhubStat }) {
  const tiles: { label: string; value: string; hint?: string }[] = [
    { label: '请求数', value: fmtInt(stat.request_count) },
    { label: 'Tokens', value: fmtInt(stat.tokens) },
    { label: '花费', value: fmtUSD(stat.raw_cost_usd), hint: 'USD' },
    { label: 'RPM', value: fmtInt(stat.rpm), hint: '每分钟请求' },
    { label: 'TPM', value: fmtInt(stat.tpm), hint: '每分钟 tokens' },
  ]
  const sampled = fmtTime(stat.sampled_at)
  const windowStart = fmtTime(stat.rate_window_start)
  const failed = statNum(stat.failed_platform_count) ?? 0
  return (
    <Card className="mt-4 space-y-3 p-4">
      <div className="flex items-center gap-2">
        <div className="text-sm font-semibold">统计</div>
        {stat.partial === true && (
          <span className="rounded-full bg-amber-100 px-2 py-0.5 text-[11px] text-amber-700">数据不完整</span>
        )}
        {failed > 0 && (
          <span className="rounded-full bg-red-100 px-2 py-0.5 text-[11px] text-red-700">
            {failed} 个平台采集失败
          </span>
        )}
      </div>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
        {tiles.map((t) => (
          <div key={t.label} className="rounded-lg border border-border bg-muted/30 px-3 py-2.5">
            <div className="text-[11px] text-muted-foreground">{t.label}</div>
            <div className="mt-0.5 text-lg font-semibold tnum text-foreground">{t.value}</div>
            {t.hint && <div className="text-[10px] text-muted-foreground/70">{t.hint}</div>}
          </div>
        ))}
      </div>
      {(sampled || windowStart) && (
        <div className="text-[11px] text-muted-foreground">
          {windowStart && sampled ? `速率窗口 ${windowStart} → ${sampled}` : `采样时间 ${sampled || windowStart}`}
        </div>
      )}
    </Card>
  )
}

const PAGE_SIZE = 20

export default function KeyhubUsageLogs() {
  const [options, setOptions] = useState<KeyhubFilterOptions | null>(null)
  const [selectedBatches, setSelectedBatches] = useState<string[]>([])
  const [model, setModel] = useState('')
  const [group, setGroup] = useState('')
  const [start, setStart] = useState(() => isoDaysAgo(7))
  const [end, setEnd] = useState(() => isoDaysAgo(0))
  const [page, setPage] = useState(1)
  const [rows, setRows] = useState<KeyhubLogRow[]>([])
  const [total, setTotal] = useState<number | undefined>(undefined)
  const [stat, setStat] = useState<unknown>(null)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    api
      .keyhubUsageFilterOptions()
      .then((res) => {
        if (res?.code === 'ok') setOptions(res.data)
        else toast.error(res?.message || '筛选项加载失败')
      })
      .catch((e) => toast.error(e))
  }, [])

  const batches = useMemo(() => (Array.isArray(options?.batches) ? options!.batches! : []), [options])
  const models = useMemo(() => (Array.isArray(options?.models) ? options!.models! : []), [options])
  const groups = useMemo(() => (Array.isArray(options?.groups) ? options!.groups! : []), [options])

  const buildQuery = useCallback(
    (forPage: number): KeyhubLogQuery => ({
      start_timestamp: toUnixSeconds(start, false),
      end_timestamp: toUnixSeconds(end, true),
      page: forPage,
      page_size: PAGE_SIZE,
      import_batch_id: selectedBatches,
      model_name: model || undefined,
      group: group || undefined,
    }),
    [start, end, selectedBatches, model, group],
  )

  const load = useCallback(
    async (forPage: number) => {
      if (selectedBatches.length === 0) {
        toast.error('请至少选择一个导入批次')
        return
      }
      setLoading(true)
      try {
        const [logsRes, statRes] = await Promise.all([
          api.keyhubUsageLogs(buildQuery(forPage)),
          api.keyhubUsageLogStat(buildQuery(forPage)),
        ])
        if (logsRes?.code === 'ok') {
          setRows(extractRows(logsRes.data))
          setTotal(extractTotal(logsRes.data))
          setPage(forPage)
        } else {
          toast.error(logsRes?.message || '日志加载失败')
        }
        if (statRes?.code === 'ok') setStat(statRes.data)
      } catch (e) {
        toast.error(e)
      } finally {
        setLoading(false)
      }
    },
    [buildQuery, selectedBatches],
  )

  const totalPages = total != null ? Math.max(1, Math.ceil(total / PAGE_SIZE)) : undefined

  // Column visibility: curated defaults + any extra raw fields discovered in the
  // rows (appended hidden). Choice is persisted per browser.
  const [visibleCols, setVisibleCols] = useState<Set<string>>(() => {
    try {
      const raw = localStorage.getItem(COLS_STORAGE_KEY)
      if (raw) {
        const arr = JSON.parse(raw)
        if (Array.isArray(arr)) return new Set(arr.map(String))
      }
    } catch {
      /* ignore malformed storage */
    }
    return new Set(KNOWN_COLUMNS.filter((c) => c.defaultVisible).map((c) => c.key))
  })
  useEffect(() => {
    try {
      localStorage.setItem(COLS_STORAGE_KEY, JSON.stringify([...visibleCols]))
    } catch {
      /* ignore */
    }
  }, [visibleCols])
  const toggleCol = useCallback((key: string) => {
    setVisibleCols((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }, [])

  const allColumns = useMemo<LogColumn[]>(() => {
    const extra: LogColumn[] = []
    const seen = new Set<string>()
    const known = new Set(KNOWN_COLUMNS.map((c) => c.key))
    for (const r of rows) {
      if (r && typeof r === 'object') {
        for (const k of Object.keys(r)) {
          if (CONSUMED_KEYS.has(k) || known.has(k) || seen.has(k)) continue
          seen.add(k)
          extra.push({
            key: k,
            label: k,
            defaultVisible: false,
            render: (row) => {
              const t = cellText(row[k])
              return t ? <span className="tnum">{t}</span> : <Muted />
            },
          })
        }
      }
    }
    return [...KNOWN_COLUMNS, ...extra]
  }, [rows])

  const shownColumns = useMemo(() => {
    const cols = allColumns.filter((c) => visibleCols.has(c.key))
    return cols.length > 0 ? cols : allColumns.filter((c) => c.defaultVisible)
  }, [allColumns, visibleCols])

  return (
    <Layout title="使用日志" subtitle="KHub（pd-maas）调用日志">
      <Card className="space-y-4 p-4">
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">开始日期</span>
            <Input type="date" value={start} onChange={(e) => setStart(e.target.value)} />
          </label>
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">结束日期</span>
            <Input type="date" value={end} onChange={(e) => setEnd(e.target.value)} />
          </label>
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">模型 (model)</span>
            <Select className="w-full" value={model} onChange={(e) => setModel(e.target.value)}>
              <option value="">全部</option>
              {models.map((m, i) => (
                <option key={i} value={optionValue(m)}>
                  {optionLabel(m)}
                </option>
              ))}
            </Select>
          </label>
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">分组 (group)</span>
            <Select className="w-full" value={group} onChange={(e) => setGroup(e.target.value)}>
              <option value="">全部</option>
              {groups.map((g, i) => (
                <option key={i} value={optionValue(g)}>
                  {optionLabel(g)}
                </option>
              ))}
            </Select>
          </label>
        </div>

        <div className="space-y-1.5">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-muted-foreground">导入批次 (import_batch_id，必选，可多选)</span>
            <span className="text-[11px] text-muted-foreground tnum">已选 {selectedBatches.length}</span>
          </div>
          {batches.length === 0 ? (
            <p className="text-xs text-muted-foreground">暂无可选批次。</p>
          ) : (
            <div className="flex max-h-40 flex-wrap gap-1.5 overflow-y-auto rounded-lg border border-border p-2">
              {batches.map((b, i) => {
                const val = batchValue(b)
                const active = selectedBatches.includes(val)
                return (
                  <button
                    key={i}
                    type="button"
                    onClick={() =>
                      setSelectedBatches((prev) =>
                        active ? prev.filter((v) => v !== val) : [...prev, val],
                      )
                    }
                    className={
                      'rounded-full border px-2.5 py-0.5 text-xs transition-colors ' +
                      (active
                        ? 'border-primary bg-primary text-primary-foreground'
                        : 'border-border bg-background text-muted-foreground hover:bg-muted')
                    }
                  >
                    {batchLabel(b)}
                  </button>
                )
              })}
            </div>
          )}
        </div>

        <div className="flex items-center justify-end">
          <Button onClick={() => load(1)} disabled={loading || selectedBatches.length === 0}>
            {loading ? '查询中…' : '查询'}
          </Button>
        </div>
      </Card>

      {stat != null && <StatPanel stat={stat as KeyhubStat} />}

      <div className="mt-4 flex items-center justify-end">
        <details className="relative">
          <summary className="flex cursor-pointer list-none items-center gap-1 rounded-md border border-border bg-background px-2.5 py-1 text-xs text-foreground hover:bg-muted [&::-webkit-details-marker]:hidden">
            <SlidersHorizontal className="size-3.5" />
            显示列
          </summary>
          <div className="absolute right-0 z-20 mt-1 max-h-72 w-44 overflow-y-auto rounded-lg border border-border bg-background p-1.5 shadow-lg">
            {allColumns.map((c) => (
              <label
                key={c.key}
                className="flex cursor-pointer items-center gap-2 rounded px-2 py-1 text-xs hover:bg-muted"
              >
                <input
                  type="checkbox"
                  className="size-3.5 accent-primary"
                  checked={visibleCols.has(c.key)}
                  onChange={() => toggleCol(c.key)}
                />
                <span className="min-w-0 flex-1 truncate">{c.label}</span>
              </label>
            ))}
          </div>
        </details>
      </div>

      <Card className="mt-2 overflow-hidden p-0">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <div className="text-sm font-semibold">日志</div>
          <div className="flex items-center gap-2 text-[11px] text-muted-foreground tnum">
            {total != null && <span>共 {total} 条</span>}
            {totalPages != null && (
              <>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={loading || page <= 1}
                  onClick={() => load(page - 1)}
                >
                  上一页
                </Button>
                <span>
                  {page} / {totalPages}
                </span>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={loading || page >= totalPages}
                  onClick={() => load(page + 1)}
                >
                  下一页
                </Button>
              </>
            )}
          </div>
        </div>
        {rows.length === 0 ? (
          <p className="px-4 py-8 text-center text-xs text-muted-foreground">
            选择导入批次后点击查询。
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="bg-muted/50 text-muted-foreground">
                <tr>
                  {shownColumns.map((c) => (
                    <th key={c.key} className="whitespace-nowrap px-3 py-2 font-medium">
                      {c.label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((r, ri) => (
                  <tr key={ri} className="border-t border-border transition-colors hover:bg-muted/30">
                    {shownColumns.map((c) => (
                      <td key={c.key} className="px-3 py-2 align-middle">
                        {c.render(r as Record<string, unknown>)}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </Layout>
  )
}
