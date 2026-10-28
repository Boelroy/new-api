import { useCallback, useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import { Badge, Button, Card, Input, Select } from '../components/ui'
import { toast } from '../components/feedback'
import { api, type CCMaxUsersStats } from '../api'

// CC Max 用户 — super admin overview of every supplier_ccmax user and how much
// the accounts they uploaded have run (count, running, tokens, cumulative and
// month-to-date cost).

// Manager filter values besides a manager id.
const FILTER_ALL = 'all'
const FILTER_UNASSIGNED = 'none'

type SortKey = 'month_cost_rmb' | 'cost_rmb' | 'accounts' | 'serving'

const SORTS: { key: SortKey; label: string }[] = [
  { key: 'month_cost_rmb', label: '本月消耗' },
  { key: 'cost_rmb', label: '累计消耗' },
  { key: 'accounts', label: '账号数' },
  { key: 'serving', label: '正在跑' },
]

function fmtDate(epochSec: number): string {
  return epochSec ? new Date(epochSec * 1000).toLocaleString() : '—'
}

export default function CCMaxUsers() {
  const [data, setData] = useState<CCMaxUsersStats | null>(null)
  const [loading, setLoading] = useState(false)
  const [manager, setManager] = useState(FILTER_ALL)
  const [keyword, setKeyword] = useState('')
  const [sort, setSort] = useState<SortKey>('month_cost_rmb')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setData(await api.ccmaxUsers())
    } catch (e) {
      toast.error(e)
    } finally {
      setLoading(false)
    }
  }, [])
  useEffect(() => {
    void load()
  }, [load])

  const fx = data && data.fx_rate > 0 ? data.fx_rate : 7.2

  const managers = useMemo(() => {
    const byId = new Map<number, string>()
    for (const u of data?.users ?? []) for (const m of u.managers) byId.set(m.id, m.username)
    return [...byId.entries()].sort((a, b) => a[1].localeCompare(b[1]))
  }, [data])

  const rows = useMemo(() => {
    const kw = keyword.trim().toLowerCase()
    return (data?.users ?? [])
      .filter((u) => {
        if (manager === FILTER_UNASSIGNED) return u.managers.length === 0
        if (manager !== FILTER_ALL) return u.managers.some((m) => String(m.id) === manager)
        return true
      })
      .filter((u) => !kw || u.username.toLowerCase().includes(kw))
      .sort((a, b) => b[sort] - a[sort] || a.username.localeCompare(b.username))
  }, [data, manager, keyword, sort])

  const totals = useMemo(() => {
    const t = { accounts: 0, serving: 0, input: 0, output: 0, cost: 0, month: 0 }
    for (const u of rows) {
      t.accounts += u.accounts
      t.serving += u.serving
      t.input += u.input_tokens
      t.output += u.output_tokens
      t.cost += u.cost_rmb
      t.month += u.month_cost_rmb
    }
    return t
  }, [rows])

  const money = (rmb: number) => (
    <div className="space-y-0.5">
      <div className="font-semibold">${(rmb / fx).toFixed(2)}</div>
      <div className="text-[11px] text-muted-foreground">¥{rmb.toFixed(2)}</div>
    </div>
  )

  return (
    <Layout title="CC Max 用户" subtitle="所有 CC Max 号商，以及他们上的号跑了多少量">
      <Card className="overflow-hidden p-0">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-2.5">
          <div className="flex flex-wrap items-center gap-2">
            <Input
              className="w-48"
              value={keyword}
              placeholder="搜索用户名"
              onChange={(e) => setKeyword(e.target.value)}
            />
            <Select value={manager} onChange={(e) => setManager(e.target.value)}>
              <option value={FILTER_ALL}>全部 Manager</option>
              <option value={FILTER_UNASSIGNED}>未分配</option>
              {managers.map(([id, name]) => (
                <option key={id} value={String(id)}>
                  {name}
                </option>
              ))}
            </Select>
            <Select value={sort} onChange={(e) => setSort(e.target.value as SortKey)}>
              {SORTS.map((s) => (
                <option key={s.key} value={s.key}>
                  按{s.label}排序
                </option>
              ))}
            </Select>
          </div>
          <div className="flex items-center gap-2 text-[11px] text-muted-foreground tnum">
            {data && (
              <span title="本月消耗的统计区间">
                本月：{data.month_from} ~ {data.month_to}（UTC+8）
              </span>
            )}
            <Button size="sm" variant="outline" disabled={loading} onClick={() => void load()}>
              {loading ? '刷新中…' : '刷新'}
            </Button>
          </div>
        </div>

        <div className="grid grid-cols-2 gap-3 border-b border-border px-4 py-3 sm:grid-cols-3 lg:grid-cols-6">
          {[
            ['用户数', rows.length.toLocaleString()],
            ['账号数', totals.accounts.toLocaleString()],
            ['正在跑', totals.serving.toLocaleString()],
            ['输入 / 输出 tokens', `${totals.input.toLocaleString()} / ${totals.output.toLocaleString()}`],
            ['累计消耗', `$${(totals.cost / fx).toFixed(2)} / ¥${totals.cost.toFixed(2)}`],
            ['本月消耗', `$${(totals.month / fx).toFixed(2)} / ¥${totals.month.toFixed(2)}`],
          ].map(([label, value]) => (
            <div key={label} className="space-y-0.5">
              <div className="text-[11px] text-muted-foreground">{label}</div>
              <div className="text-sm font-semibold tnum">{value}</div>
            </div>
          ))}
        </div>

        {rows.length === 0 ? (
          <p className="px-4 py-8 text-center text-xs text-muted-foreground">
            {data ? '没有符合条件的用户。' : '加载中…'}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="bg-muted/50 text-muted-foreground">
                <tr>
                  {['用户名', '所属 Manager', '状态', '账号数', '正在跑', '输入 / 输出 tokens', '累计消耗', '本月消耗', '创建时间'].map(
                    (h) => (
                      <th key={h} className="whitespace-nowrap px-3 py-2 font-medium">
                        {h}
                      </th>
                    ),
                  )}
                </tr>
              </thead>
              <tbody>
                {rows.map((u) => (
                  <tr key={u.id} className="border-t border-border">
                    <td className="px-3 py-1.5 font-mono">{u.username}</td>
                    <td className="px-3 py-1.5">
                      {u.managers.length ? u.managers.map((m) => m.username).join('、') : '—'}
                    </td>
                    <td className="px-3 py-1.5">
                      <Badge tone={u.status === 1 ? 'success' : 'danger'}>{u.status === 1 ? '正常' : '已禁用'}</Badge>
                    </td>
                    <td className="px-3 py-1.5 tnum">{u.accounts}</td>
                    <td className="px-3 py-1.5 tnum">{u.serving}</td>
                    <td className="whitespace-nowrap px-3 py-1.5 tnum">
                      {u.input_tokens.toLocaleString()} / {u.output_tokens.toLocaleString()}
                    </td>
                    <td className="whitespace-nowrap px-3 py-1.5 tnum">{money(u.cost_rmb)}</td>
                    <td className="whitespace-nowrap px-3 py-1.5 tnum">{money(u.month_cost_rmb)}</td>
                    <td className="whitespace-nowrap px-3 py-1.5 tnum">{fmtDate(u.created_at)}</td>
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
