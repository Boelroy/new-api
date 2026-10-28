import { Fragment, useCallback, useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import { Button, Card, Input, Select } from '../components/ui'
import { toast } from '../components/feedback'
import { api, ROLE_ADMIN, type CCMaxDaily as CCMaxDailyData, type CCMaxDailyRow } from '../api'
import { getCachedRole } from '../auth'

// CC Max 日结算 — each UTC+8 day's portal bill per 号商 at original price,
// pulled every morning for the previous day. Admins see every 号商 (and can
// re-pull a range), a ccmax_manager their members, a supplier only themselves.

const FILTER_ALL = 'all'

type UserDay = { userId: number; username: string; cost: number; accounts: CCMaxDailyRow[] }
type Day = { ds: string; users: UserDay[]; cost: number }

export default function CCMaxDaily() {
  const [data, setData] = useState<CCMaxDailyData | null>(null)
  const [loading, setLoading] = useState(false)
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [user, setUser] = useState(FILTER_ALL)
  const [hideIdle, setHideIdle] = useState(false)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [syncing, setSyncing] = useState(false)
  const isAdmin = (getCachedRole() ?? 0) >= ROLE_ADMIN

  const load = useCallback(async (f?: string, t?: string) => {
    setLoading(true)
    try {
      const res = await api.ccmaxDaily(f, t)
      setData(res)
      setFrom(res.from)
      setTo(res.to)
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

  // Every generated day of the range (newest first) × every 号商 in scope, 0
  // when idle. Days after synced_through have no bill yet.
  const days = useMemo<Day[]>(() => {
    if (!data?.synced_through) return []
    const users = data.users.filter((u) => user === FILTER_ALL || String(u.id) === user)
    const byKey = new Map<string, CCMaxDailyRow[]>()
    for (const r of data.rows) {
      const key = `${r.ds}|${r.user_id}`
      byKey.set(key, [...(byKey.get(key) ?? []), r])
    }
    const out: Day[] = []
    const end = data.to < data.synced_through ? data.to : data.synced_through
    for (let d = new Date(end + 'T00:00:00Z'); d.toISOString().slice(0, 10) >= data.from; d.setUTCDate(d.getUTCDate() - 1)) {
      const ds = d.toISOString().slice(0, 10)
      const rows = users
        .map((u) => {
          const accounts = (byKey.get(`${ds}|${u.id}`) ?? []).sort((a, b) => b.cost_rmb - a.cost_rmb)
          return { userId: u.id, username: u.username, accounts, cost: accounts.reduce((s, a) => s + a.cost_rmb, 0) }
        })
        .filter((u) => !hideIdle || u.cost > 0)
      out.push({ ds, users: rows, cost: rows.reduce((s, u) => s + u.cost, 0) })
    }
    return out
  }, [data, user, hideIdle])

  const total = days.reduce((s, d) => s + d.cost, 0)

  const toggle = (key: string) =>
    setExpanded((prev) => {
      const next = new Set(prev)
      if (!next.delete(key)) next.add(key)
      return next
    })

  async function resync() {
    setSyncing(true)
    try {
      await api.ccmaxDailySync(from, to)
      toast.success('账单已重新拉取')
      await load(from, to)
    } catch (e) {
      toast.error(e)
    } finally {
      setSyncing(false)
    }
  }

  function exportCSV() {
    const header = ['日期', '号商', '有用量账号数', '用量 USD', '用量 CNY']
    const lines: string[] = []
    for (const d of days) {
      for (const u of d.users) {
        lines.push([d.ds, u.username, u.accounts.length, (u.cost / fx).toFixed(2), u.cost.toFixed(2)].join(','))
      }
      lines.push([d.ds, '小计', '', (d.cost / fx).toFixed(2), d.cost.toFixed(2)].join(','))
    }
    // BOM so Excel opens it as UTF-8 without garbling.
    const blob = new Blob(['﻿' + [header.join(','), ...lines].join('\n')], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `ccmax-daily_${data?.from}_${data?.to}.csv`
    a.click()
    URL.revokeObjectURL(url)
  }

  const money = (rmb: number) => (
    <span>
      <span className="font-semibold">${(rmb / fx).toFixed(2)}</span>
      <span className="ml-1.5 text-[11px] text-muted-foreground">¥{rmb.toFixed(2)}</span>
    </span>
  )

  return (
    <Layout title="CC Max 日结算" subtitle="按北京时间自然日，每个号商上的号在 portal 账单里的消费（原价）">
      <Card className="overflow-hidden p-0">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-2.5">
          <div className="flex flex-wrap items-center gap-2">
            <Input className="w-40" type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
            <span className="text-xs text-muted-foreground">至</span>
            <Input className="w-40" type="date" value={to} onChange={(e) => setTo(e.target.value)} />
            <Button size="sm" disabled={loading || !from || !to} onClick={() => void load(from, to)}>
              查询
            </Button>
            {data && (
              <Button
                size="sm"
                variant="outline"
                disabled={loading}
                onClick={() => void load()}
              >
                本月
              </Button>
            )}
            {(data?.users.length ?? 0) > 1 && (
              <Select value={user} onChange={(e) => setUser(e.target.value)}>
                <option value={FILTER_ALL}>全部号商</option>
                {data!.users.map((u) => (
                  <option key={u.id} value={String(u.id)}>
                    {u.username}
                  </option>
                ))}
              </Select>
            )}
            <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <input type="checkbox" checked={hideIdle} onChange={(e) => setHideIdle(e.target.checked)} />
              隐藏 0 用量
            </label>
          </div>
          <div className="flex items-center gap-2">
            <Button size="sm" variant="outline" disabled={days.length === 0} onClick={exportCSV}>
              导出 CSV
            </Button>
            {isAdmin && (
              <Button
                size="sm"
                variant="outline"
                disabled={syncing || !from || !to}
                title="按当前日期范围重新从 portal 拉取账单（只能拉今天之前的）"
                onClick={() => void resync()}
              >
                {syncing ? '拉取中…' : '重新拉取账单'}
              </Button>
            )}
            <Button size="sm" variant="outline" disabled={loading} onClick={() => void load(from, to)}>
              {loading ? '刷新中…' : '刷新'}
            </Button>
          </div>
        </div>

        <div className="flex flex-wrap items-baseline gap-x-6 gap-y-1 border-b border-border px-4 py-3">
          <div className="space-y-0.5">
            <div className="text-[11px] text-muted-foreground">区间合计</div>
            <div className="text-sm tnum">{money(total)}</div>
          </div>
          <p className="text-[11px] text-muted-foreground">
            数据来自 portal 账单，每天 {String(data?.bill_hour ?? 8).padStart(2, '0')}:00（UTC+8）生成前一天的账单
            {data && (data.synced_through ? `，已生成至 ${data.synced_through}` : '，尚未生成')}。
          </p>
        </div>

        {days.length === 0 ? (
          <p className="px-4 py-8 text-center text-xs text-muted-foreground">{data ? '该日期范围内还没有生成的账单。' : '加载中…'}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="bg-muted/50 text-muted-foreground">
                <tr>
                  {['日期', '号商', '有用量账号数', '用量'].map((h) => (
                    <th key={h} className="whitespace-nowrap px-3 py-2 font-medium">
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {days.map((d) => (
                  <Fragment key={d.ds}>
                    {d.users.map((u, i) => {
                      const key = `${d.ds}|${u.userId}`
                      const open = expanded.has(key)
                      return (
                        <Fragment key={key}>
                          <tr
                            className={`border-t border-border ${u.accounts.length ? 'cursor-pointer hover:bg-muted/30' : ''}`}
                            onClick={() => u.accounts.length && toggle(key)}
                          >
                            <td className="whitespace-nowrap px-3 py-1.5 tnum">
                              {i === 0 && d.ds}
                            </td>
                            <td className="px-3 py-1.5 font-mono">
                              {u.accounts.length > 0 && (
                                <span className="mr-1 inline-block w-3 text-muted-foreground">{open ? '▾' : '▸'}</span>
                              )}
                              {u.username}
                            </td>
                            <td className="px-3 py-1.5 tnum">{u.accounts.length}</td>
                            <td className="whitespace-nowrap px-3 py-1.5 tnum">{money(u.cost)}</td>
                          </tr>
                          {open &&
                            u.accounts.map((a) => (
                              <tr key={a.account_id} className="bg-muted/20 text-muted-foreground">
                                <td />
                                <td className="px-3 py-1 pl-8 font-mono" colSpan={2}>
                                  {a.account || a.alias}
                                  {a.alias && a.account && <span className="ml-2 text-[11px]">{a.alias}</span>}
                                </td>
                                <td className="whitespace-nowrap px-3 py-1 tnum">{money(a.cost_rmb)}</td>
                              </tr>
                            ))}
                        </Fragment>
                      )
                    })}
                    <tr className="border-t border-border bg-muted/40 font-medium">
                      <td className="whitespace-nowrap px-3 py-1.5 tnum">{d.users.length === 0 ? d.ds : ''}</td>
                      <td className="px-3 py-1.5 text-destructive">小计</td>
                      <td className="px-3 py-1.5 tnum">{d.users.reduce((s, u) => s + u.accounts.length, 0)}</td>
                      <td className="whitespace-nowrap px-3 py-1.5 tnum text-destructive">{money(d.cost)}</td>
                    </tr>
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </Layout>
  )
}
