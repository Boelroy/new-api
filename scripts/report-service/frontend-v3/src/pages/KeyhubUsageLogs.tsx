import { useCallback, useEffect, useMemo, useState } from 'react'
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

function cellText(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
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

  const columns = useMemo(() => {
    const seen = new Set<string>()
    const cols: string[] = []
    for (const r of rows) {
      if (r && typeof r === 'object') {
        for (const k of Object.keys(r)) {
          if (!seen.has(k)) {
            seen.add(k)
            cols.push(k)
          }
        }
      }
    }
    return cols
  }, [rows])

  const totalPages = total != null ? Math.max(1, Math.ceil(total / PAGE_SIZE)) : undefined

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

      {stat != null && (
        <Card className="mt-4 space-y-2 p-4">
          <div className="text-sm font-semibold">统计 (stat)</div>
          <pre className="max-h-56 overflow-auto rounded-md bg-muted p-3 text-[11px] leading-relaxed">
            {JSON.stringify(stat, null, 2)}
          </pre>
        </Card>
      )}

      <Card className="mt-4 overflow-hidden p-0">
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
                  {columns.map((c) => (
                    <th key={c} className="whitespace-nowrap px-3 py-2 font-medium">
                      {c}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((r, ri) => (
                  <tr key={ri} className="border-t border-border">
                    {columns.map((c) => (
                      <td key={c} className="max-w-[280px] truncate px-3 py-1.5 tnum" title={cellText(r[c])}>
                        {cellText(r[c])}
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
