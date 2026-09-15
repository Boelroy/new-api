import { useState, useEffect, useMemo, ReactNode } from 'react'
import Layout from '../components/Layout'
import SummaryCards from '../components/SummaryCards'
import { api, SupplierBillRow, SupplierBillGroup } from '../api'
import { toast } from '../components/feedback'
import { Button, cx } from '../components/ui'
import { TZ_OPTIONS, addDays, localDay, downloadCSV, useReportTz } from '../lib/reportTz'

function today() { return new Date().toISOString().slice(0, 10) }
function daysAgo(n: number) {
  const d = new Date(); d.setUTCDate(d.getUTCDate() - n)
  return d.toISOString().slice(0, 10)
}
function fmtCost(v: number) { return '$' + v.toFixed(4) }
function fmtNum(v: number) { return v.toLocaleString() }

const UNGROUPED = '(未分组)'

type Metrics = { req: number; input: number; output: number; total: number; cost: number }
function zero(): Metrics { return { req: 0, input: 0, output: 0, total: 0, cost: 0 } }
function add(a: Metrics, r: SupplierBillRow) {
  a.req += r.request_count; a.input += r.input_tokens; a.output += r.output_tokens
  a.total += r.total_tokens; a.cost += r.total_cost
}
function merge(a: Metrics, b: Metrics) {
  a.req += b.req; a.input += b.input; a.output += b.output; a.total += b.total; a.cost += b.cost
}

// One group's per-day breakdown: day -> studio -> metrics, plus day totals.
type GroupView = {
  name: string
  studios: string[]
  days: { day: string; total: Metrics; byStudio: { studio: string; m: Metrics }[] }[]
  rangeTotal: Metrics
}

export default function SupplierBill({ tabBar }: { tabBar?: ReactNode }) {
  const [start, setStart] = useState(daysAgo(6))
  const [end, setEnd] = useState(today())
  const [range, setRange] = useState({ start: daysAgo(6), end: today() })
  const [rawData, setRawData] = useState<SupplierBillRow[]>([])
  const [loading, setLoading] = useState(false)
  const [refreshedAt, setRefreshedAt] = useState('')
  const { tz, setTz, isSuperAdmin } = useReportTz()

  const [groups, setGroups] = useState<SupplierBillGroup[]>([])
  const [allStudios, setAllStudios] = useState<string[]>([])
  const [showManager, setShowManager] = useState(false)
  const [draft, setDraft] = useState<SupplierBillGroup[]>([])
  const [saving, setSaving] = useState(false)

  const [openGroups, setOpenGroups] = useState<Set<string>>(new Set())
  const [openDays, setOpenDays] = useState<Set<string>>(new Set())

  const load = async (s: string, e: string) => {
    setLoading(true)
    try {
      // Widen the UTC window ±1 day so timezone re-bucketing has full edge days.
      const rows = await api.getSupplierBill(addDays(s, -1), addDays(e, 1))
      setRawData(rows)
      setRange({ start: s, end: e })
      setRefreshedAt(new Date().toLocaleTimeString('zh-CN'))
    } catch (err) {
      console.error(err); toast.error(err)
    } finally {
      setLoading(false)
    }
  }

  const loadGroups = async () => {
    try {
      const res = await api.getSupplierBillGroups()
      setGroups(res.groups || [])
    } catch (err) { console.error(err); toast.error(err) }
  }

  useEffect(() => {
    void load(start, end)
    void loadGroups()
    void api.listStudios().then(r => setAllStudios(r.studios || [])).catch(() => {})
  }, [])

  // studio -> day -> metrics, in the selected timezone and within range.
  const studioDay = useMemo(() => {
    const m = new Map<string, Map<string, Metrics>>()
    rawData.forEach(r => {
      const day = localDay(r.hour, tz)
      if (day < range.start || day > range.end) return
      const studio = r.studio || '(未标记)'
      let byDay = m.get(studio)
      if (!byDay) { byDay = new Map(); m.set(studio, byDay) }
      let e = byDay.get(day)
      if (!e) { e = zero(); byDay.set(day, e) }
      add(e, r)
    })
    return m
  }, [rawData, tz, range])

  // All studios that actually have usage in the window — candidates for grouping.
  const studiosWithData = useMemo(() => Array.from(studioDay.keys()).sort(), [studioDay])

  // Build the display views: one per configured group + a virtual "(未分组)"
  // catch-all for studios with usage that aren't in any group.
  const views = useMemo<GroupView[]>(() => {
    const assigned = new Set<string>()
    groups.forEach(g => g.studios.forEach(s => assigned.add(s)))

    const build = (name: string, studios: string[]): GroupView => {
      const dayMap = new Map<string, { total: Metrics; byStudio: Map<string, Metrics> }>()
      const rangeTotal = zero()
      studios.forEach(studio => {
        const byDay = studioDay.get(studio)
        if (!byDay) return
        byDay.forEach((m, day) => {
          let d = dayMap.get(day)
          if (!d) { d = { total: zero(), byStudio: new Map() }; dayMap.set(day, d) }
          merge(d.total, m)
          const cur = d.byStudio.get(studio) || zero()
          merge(cur, m); d.byStudio.set(studio, cur)
          merge(rangeTotal, m)
        })
      })
      const days = Array.from(dayMap.entries())
        .sort((a, b) => a[0].localeCompare(b[0]))
        .map(([day, d]) => ({
          day,
          total: d.total,
          byStudio: Array.from(d.byStudio.entries())
            .map(([studio, m]) => ({ studio, m }))
            .sort((a, b) => b.m.cost - a.m.cost),
        }))
      return { name, studios, days, rangeTotal }
    }

    const out = groups.map(g => build(g.name, g.studios))
    const ungrouped = studiosWithData.filter(s => !assigned.has(s))
    if (ungrouped.length) out.push(build(UNGROUPED, ungrouped))
    return out.filter(v => v.days.length > 0)
  }, [groups, studioDay, studiosWithData])

  const summary = useMemo(() => {
    const total = zero()
    const days = new Set<string>()
    views.forEach(v => { merge(total, v.rangeTotal); v.days.forEach(d => days.add(d.day)) })
    return {
      totalCost: total.cost,
      groups: groups.length,
      days: days.size,
      studios: studiosWithData.length,
    }
  }, [views, groups, studiosWithData])

  const toggle = (set: Set<string>, key: string, setter: (s: Set<string>) => void) => {
    const next = new Set(set)
    next.has(key) ? next.delete(key) : next.add(key)
    setter(next)
  }

  const exportCSV = () => {
    const rows: (string | number)[][] = []
    views.forEach(v => {
      v.days.forEach(d => {
        rows.push([v.name, '*', d.day, d.total.req, d.total.input, d.total.output, d.total.total, d.total.cost.toFixed(6)])
        d.byStudio.forEach(({ studio, m }) => {
          rows.push([v.name, studio, d.day, m.req, m.input, m.output, m.total, m.cost.toFixed(6)])
        })
      })
    })
    downloadCSV(
      `supplier_bill_${range.start}_to_${range.end}_${tz.replace(/\//g, '-')}.csv`,
      ['分组', '工作室', '日期', 'Requests', 'Input', 'Output', 'Total Tokens', 'Cost(USD)'],
      rows,
    )
  }

  // ---- group manager ----
  const openManager = () => {
    setDraft(groups.map(g => ({ name: g.name, studios: [...g.studios] })))
    setShowManager(true)
  }
  const studioChoices = useMemo(() => {
    const s = new Set<string>([...allStudios, ...studiosWithData])
    return Array.from(s).filter(Boolean).sort()
  }, [allStudios, studiosWithData])

  const saveGroups = async () => {
    setSaving(true)
    try {
      const res = await api.saveSupplierBillGroups(draft)
      setGroups(res.groups || [])
      toast.success('分组已保存')
      setShowManager(false)
    } catch (err) { toast.error(err) } finally { setSaving(false) }
  }

  const actions = (
    <>
      <select
        value={tz}
        onChange={e => setTz(e.target.value)}
        disabled={!isSuperAdmin}
        title={isSuperAdmin ? '统计时区（保存为本站默认）' : '统计时区（本站默认，仅超级管理员可改）'}
        className="border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-white focus:outline-none focus:border-brand disabled:opacity-70 disabled:cursor-not-allowed"
      >
        {TZ_OPTIONS.map(z => <option key={z} value={z}>{z}</option>)}
      </select>
      <input type="date" value={start} onChange={e => setStart(e.target.value)} className="border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-white" />
      <span className="text-gray-300 text-xs">→</span>
      <input type="date" value={end} onChange={e => setEnd(e.target.value)} className="border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-white" />
      <button onClick={() => load(start, end)} disabled={loading} className="bg-brand text-white rounded-md px-3 py-1.5 text-xs hover:bg-brand-700 disabled:opacity-50">
        {loading ? '加载中...' : '查询'}
      </button>
      <button onClick={exportCSV} disabled={!views.length} className="border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50 disabled:opacity-50">
        导出 CSV
      </button>
    </>
  )

  return (
    <Layout
      title="供应商账单"
      subtitle={`按 ${tz} 时区，分组×天统计（可展开到工作室）· ${range.start} ~ ${range.end}${refreshedAt ? ` · 更新于 ${refreshedAt}` : ''}`}
      actions={actions}
    >
      {tabBar}

      <div className="bg-white border border-gray-200 rounded-xl mb-4">
        <div className="flex items-center gap-3 px-3 py-2.5 text-xs">
          <span className="mono-label">分组管理</span>
          <span className="text-gray-400">
            {groups.length ? groups.map(g => `${g.name}(${g.studios.length})`).join(' · ') : '尚未配置分组'}
          </span>
          <button
            onClick={() => (showManager ? setShowManager(false) : openManager())}
            className="ml-auto border border-gray-200 rounded-md px-3 py-1 bg-white hover:bg-gray-50"
          >
            {showManager ? '收起' : '编辑分组'}
          </button>
        </div>

        {showManager && (
          <div className="border-t border-gray-100 px-3 py-3 space-y-3">
            {draft.map((g, gi) => (
              <div key={gi} className="border border-gray-200 rounded-lg p-3">
                <div className="flex items-center gap-2 mb-2">
                  <input
                    value={g.name}
                    onChange={e => setDraft(d => d.map((x, i) => i === gi ? { ...x, name: e.target.value } : x))}
                    placeholder="分组名称，如 alice"
                    className="border border-gray-200 rounded-md px-2 py-1 text-xs w-48 focus:outline-none focus:border-brand"
                  />
                  <span className="text-[11px] text-gray-400">{g.studios.length} 个工作室</span>
                  <button
                    onClick={() => setDraft(d => d.filter((_, i) => i !== gi))}
                    className="ml-auto text-rose-600 hover:text-rose-700 text-xs"
                  >
                    删除分组
                  </button>
                </div>
                <div className="flex flex-wrap gap-1.5 max-h-40 overflow-y-auto">
                  {studioChoices.map(s => {
                    const on = g.studios.includes(s)
                    return (
                      <button
                        key={s}
                        onClick={() => setDraft(d => d.map((x, i) => i === gi
                          ? { ...x, studios: on ? x.studios.filter(y => y !== s) : [...x.studios, s] }
                          : x))}
                        className={cx(
                          'px-2 py-0.5 rounded-full text-[11px] border',
                          on ? 'bg-brand text-white border-brand' : 'bg-white text-gray-600 border-gray-200 hover:border-gray-400',
                        )}
                      >
                        {s}
                      </button>
                    )
                  })}
                </div>
              </div>
            ))}
            <div className="flex items-center gap-2">
              <Button variant="outline" size="sm" onClick={() => setDraft(d => [...d, { name: '', studios: [] }])}>+ 新建分组</Button>
              <Button variant="primary" size="sm" onClick={saveGroups} disabled={saving}>{saving ? '保存中…' : '保存分组'}</Button>
              <span className="text-[11px] text-gray-400">空名称或无成员的分组不会被保存</span>
            </div>
          </div>
        )}
      </div>

      <SummaryCards cards={[
        { label: 'Total Cost', value: '$' + summary.totalCost.toFixed(2), color: 'text-emerald-600' },
        { label: '分组数', value: String(summary.groups) },
        { label: '天数', value: String(summary.days), color: 'text-amber-600' },
        { label: '工作室数', value: String(summary.studios), color: 'text-purple-600' },
      ]} />

      <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
        <div className="overflow-x-auto max-h-[68vh] overflow-y-auto">
          <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
            <thead>
              <tr>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">分组 / 工作室</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">日期 ({tz})</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Requests</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Input</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Output</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Total Tokens</th>
                <th className="sticky top-0 bg-gray-50 px-3 py-2 text-right mono-label border-b border-gray-200">Cost</th>
              </tr>
            </thead>
            <tbody>
              {views.map(v => {
                const gOpen = openGroups.has(v.name)
                return (
                  <ExpandableGroup
                    key={v.name}
                    view={v}
                    open={gOpen}
                    onToggle={() => toggle(openGroups, v.name, setOpenGroups)}
                    openDays={openDays}
                    onToggleDay={(day) => toggle(openDays, `${v.name}|${day}`, setOpenDays)}
                  />
                )
              })}
              {views.length === 0 && (
                <tr><td colSpan={7} className="px-4 py-6 text-center text-gray-400">{loading ? '加载中…' : '无数据'}</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </Layout>
  )
}

function Row({ label, day, m, cls, indent, onClick, caret }: {
  label: ReactNode; day: string; m: Metrics; cls?: string; indent?: number; onClick?: () => void; caret?: '▸' | '▾' | ''
}) {
  return (
    <tr className={cx('border-b border-gray-50', onClick && 'cursor-pointer hover:bg-gray-50', cls)} onClick={onClick}>
      <td className="px-3 py-1.5" style={indent ? { paddingLeft: 12 + indent * 16 } : undefined}>
        {caret ? <span className="inline-block w-3 text-gray-400">{caret}</span> : null}{label}
      </td>
      <td className="px-3 py-1.5 font-mono text-gray-600">{day}</td>
      <td className="px-3 py-1.5 text-right tabular-nums">{fmtNum(m.req)}</td>
      <td className="px-3 py-1.5 text-right tabular-nums">{fmtNum(m.input)}</td>
      <td className="px-3 py-1.5 text-right tabular-nums">{fmtNum(m.output)}</td>
      <td className="px-3 py-1.5 text-right tabular-nums">{fmtNum(m.total)}</td>
      <td className="px-3 py-1.5 text-right tabular-nums font-medium">{fmtCost(m.cost)}</td>
    </tr>
  )
}

function ExpandableGroup({ view, open, onToggle, openDays, onToggleDay }: {
  view: GroupView; open: boolean; onToggle: () => void
  openDays: Set<string>; onToggleDay: (day: string) => void
}) {
  return (
    <>
      <Row
        label={<span className="font-semibold text-gray-800">{view.name}</span>}
        day="全部"
        m={view.rangeTotal}
        cls="bg-gray-50/60"
        caret={open ? '▾' : '▸'}
        onClick={onToggle}
      />
      {open && view.days.map(d => {
        const dOpen = openDays.has(`${view.name}|${d.day}`)
        const canExpand = d.byStudio.length > 1
        return (
          <FragmentDay
            key={d.day}
            name={view.name}
            d={d}
            open={dOpen}
            canExpand={canExpand}
            onToggle={() => canExpand && onToggleDay(d.day)}
          />
        )
      })}
    </>
  )
}

function FragmentDay({ d, open, canExpand, onToggle }: {
  name: string
  d: { day: string; total: Metrics; byStudio: { studio: string; m: Metrics }[] }
  open: boolean; canExpand: boolean; onToggle: () => void
}) {
  return (
    <>
      <Row
        label={<span className="text-gray-500">{canExpand ? '' : '·'} 当日合计</span>}
        day={d.day}
        m={d.total}
        indent={1}
        caret={canExpand ? (open ? '▾' : '▸') : ''}
        onClick={canExpand ? onToggle : undefined}
      />
      {open && d.byStudio.map(({ studio, m }) => (
        <Row key={studio} label={<span className="font-mono text-gray-500">{studio}</span>} day={d.day} m={m} indent={2} />
      ))}
    </>
  )
}
