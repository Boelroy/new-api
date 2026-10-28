import { Fragment, useCallback, useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import { Button, Card, Input, cx } from '../components/ui'
import { confirmDialog, toast } from '../components/feedback'
import { api, ROLE_ADMIN, type CCMaxSettlement as SettlementData, type CCMaxSettlementParty } from '../api'
import { getCachedRole } from '../auth'

// CC Max 结算 — the portal daily bills summed over a date range per
// settlement party: a ccmax_manager (for all its 号商) or a 号商 with no
// manager. Each party's days with a bill are marked settled or not; admins
// toggle them. Zero bills never show.

export default function CCMaxSettlement() {
  const [data, setData] = useState<SettlementData | null>(null)
  const [loading, setLoading] = useState(false)
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [expanded, setExpanded] = useState<Set<number>>(new Set())
  const [busy, setBusy] = useState(false)
  const isAdmin = (getCachedRole() ?? 0) >= ROLE_ADMIN

  const load = useCallback(async (f?: string, t?: string) => {
    setLoading(true)
    try {
      const res = await api.ccmaxSettlement(f, t)
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
  const managers = useMemo(() => (data?.parties ?? []).filter((p) => p.kind === 'manager'), [data])
  const solos = useMemo(() => (data?.parties ?? []).filter((p) => p.kind === 'user'), [data])
  const totals = useMemo(() => {
    const t = { cost: 0, settled: 0, unsettled: 0 }
    for (const p of data?.parties ?? []) {
      t.cost += p.cost_rmb
      t.settled += p.settled_rmb
      t.unsettled += p.unsettled_rmb
    }
    return t
  }, [data])

  const toggle = (id: number) =>
    setExpanded((prev) => {
      const next = new Set(prev)
      if (!next.delete(id)) next.add(id)
      return next
    })

  async function mark(party: CCMaxSettlementParty, days: string[], settled: boolean) {
    if (days.length === 0) return
    const ok = await confirmDialog({
      title: settled ? '标记已结算' : '取消结算',
      message: `${party.username}：${days.join('、')} 共 ${days.length} 天${settled ? '标记为已结算' : '改回未结算'}？`,
      confirmText: settled ? '标记已结算' : '改回未结算',
      danger: !settled,
    })
    if (!ok) return
    setBusy(true)
    try {
      await api.ccmaxSettlementMark(party.id, days, settled)
      toast.success(settled ? '已标记结算' : '已改回未结算')
      await load(from, to)
    } catch (e) {
      toast.error(e)
    } finally {
      setBusy(false)
    }
  }

  async function resync() {
    setBusy(true)
    try {
      await api.ccmaxDailySync(from, to)
      toast.success('账单已重新拉取')
      await load(from, to)
    } catch (e) {
      toast.error(e)
    } finally {
      setBusy(false)
    }
  }

  function exportCSV() {
    const header = ['类型', '结算对象', '号商 / 账号', '合计 CNY', '已结算 CNY', '未结算 CNY', '未结算日期']
    const lines = (data?.parties ?? []).map((p) =>
      [
        p.kind === 'manager' ? 'Manager' : '号商',
        p.username,
        p.kind === 'manager' ? p.members.map((m) => m.username).join(' ') : p.members[0]?.accounts.length ?? 0,
        p.cost_rmb.toFixed(2),
        p.settled_rmb.toFixed(2),
        p.unsettled_rmb.toFixed(2),
        p.days
          .filter((d) => !d.settled)
          .map((d) => d.ds)
          .join(' '),
      ].join(','),
    )
    // BOM so Excel opens it as UTF-8 without garbling.
    const blob = new Blob(['﻿' + [header.join(','), ...lines].join('\n')], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `ccmax-settlement_${data?.from}_${data?.to}.csv`
    a.click()
    URL.revokeObjectURL(url)
  }

  const money = (rmb: number, className?: string) => (
    <span className={className}>
      <span className="font-semibold">¥{rmb.toFixed(2)}</span>
      <span className="ml-1.5 text-[11px] text-muted-foreground">${(rmb / fx).toFixed(2)}</span>
    </span>
  )

  function partyTable(title: string, hint: string, parties: CCMaxSettlementParty[]) {
    const isManager = parties[0]?.kind === 'manager'
    return (
      <Card className="overflow-hidden p-0">
        <div className="flex items-baseline gap-2 border-b border-border px-4 py-2.5">
          <span className="text-sm font-semibold">{title}</span>
          <span className="text-[11px] text-muted-foreground">{hint}</span>
        </div>
        {parties.length === 0 ? (
          <p className="px-4 py-6 text-center text-xs text-muted-foreground">该日期范围内没有消费。</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="bg-muted/50 text-muted-foreground">
                <tr>
                  {[isManager ? 'Manager' : '号商', isManager ? '号商数' : '账号数', '合计', '已结算', '未结算', '每日结算（点击切换）', ''].map(
                    (h) => (
                      <th key={h} className="whitespace-nowrap px-3 py-2 font-medium">
                        {h}
                      </th>
                    ),
                  )}
                </tr>
              </thead>
              <tbody>
                {parties.map((p) => {
                  const open = expanded.has(p.id)
                  const unsettledDays = p.days.filter((d) => !d.settled).map((d) => d.ds)
                  const accounts = p.members[0]?.accounts ?? []
                  return (
                    <Fragment key={p.id}>
                      <tr className="border-t border-border align-top">
                        <td className="cursor-pointer px-3 py-2 font-mono" onClick={() => toggle(p.id)}>
                          <span className="mr-1 inline-block w-3 text-muted-foreground">{open ? '▾' : '▸'}</span>
                          {p.username}
                        </td>
                        <td className="px-3 py-2 tnum">{isManager ? p.members.length : accounts.length}</td>
                        <td className="whitespace-nowrap px-3 py-2 tnum">{money(p.cost_rmb)}</td>
                        <td className="whitespace-nowrap px-3 py-2 tnum">{money(p.settled_rmb, 'text-success')}</td>
                        <td className="whitespace-nowrap px-3 py-2 tnum">
                          {money(p.unsettled_rmb, p.unsettled_rmb > 0 ? 'text-destructive' : undefined)}
                        </td>
                        <td className="px-3 py-2">
                          <div className="flex max-w-xl flex-wrap gap-1">
                            {p.days.map((d) => {
                              const changed = d.settled && Math.abs(d.settled_rmb - d.cost_rmb) >= 0.01
                              return (
                                <button
                                  key={d.ds}
                                  type="button"
                                  disabled={!isAdmin || busy}
                                  title={
                                    `${d.ds} ¥${d.cost_rmb.toFixed(2)}` +
                                    (d.settled
                                      ? `\n已结算（${new Date(d.settled_at * 1000).toLocaleString()}，结算时 ¥${d.settled_rmb.toFixed(2)}）`
                                      : '\n未结算') +
                                    (changed ? '\n结算后账单有变动' : '')
                                  }
                                  onClick={() => void mark(p, [d.ds], !d.settled)}
                                  className={cx(
                                    'rounded border px-1.5 py-0.5 text-[11px] tnum',
                                    d.settled
                                      ? 'border-success/30 bg-success/10 text-success'
                                      : 'border-destructive/30 bg-destructive/10 text-destructive',
                                    changed && 'ring-1 ring-warning',
                                    isAdmin && 'cursor-pointer hover:opacity-80',
                                  )}
                                >
                                  {d.ds.slice(5)} {d.settled ? '✓' : '✗'}
                                </button>
                              )
                            })}
                          </div>
                        </td>
                        <td className="whitespace-nowrap px-3 py-2">
                          {isAdmin && unsettledDays.length > 0 && (
                            <Button size="sm" variant="outline" disabled={busy} onClick={() => void mark(p, unsettledDays, true)}>
                              全部标记已结算
                            </Button>
                          )}
                        </td>
                      </tr>
                      {open &&
                        (isManager
                          ? p.members.map((m) => (
                              <tr key={m.id} className="bg-muted/20 text-muted-foreground">
                                <td className="px-3 py-1 pl-8 font-mono">{m.username}</td>
                                <td className="px-3 py-1 tnum">{m.accounts.length} 个账号</td>
                                <td className="whitespace-nowrap px-3 py-1 tnum" colSpan={5}>
                                  {money(m.cost_rmb)}
                                </td>
                              </tr>
                            ))
                          : accounts.map((a) => (
                              <tr key={a.id} className="bg-muted/20 text-muted-foreground">
                                <td className="px-3 py-1 pl-8 font-mono">{a.account || a.alias}</td>
                                <td className="px-3 py-1 text-[11px]">{a.alias}</td>
                                <td className="whitespace-nowrap px-3 py-1 tnum" colSpan={5}>
                                  {money(a.cost_rmb)}
                                </td>
                              </tr>
                            )))}
                    </Fragment>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    )
  }

  return (
    <Layout title="CC Max 结算" subtitle="按 portal 账单原价汇总，manager 名下的号商合并结算，其余号商单独结算">
      <div className="space-y-4">
        <Card className="overflow-hidden p-0">
          <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <Input className="w-40" type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
              <span className="text-xs text-muted-foreground">至</span>
              <Input className="w-40" type="date" value={to} onChange={(e) => setTo(e.target.value)} />
              <Button size="sm" disabled={loading || !from || !to} onClick={() => void load(from, to)}>
                查询
              </Button>
              <Button size="sm" variant="outline" disabled={loading} onClick={() => void load()}>
                本月
              </Button>
            </div>
            <div className="flex items-center gap-2">
              {isAdmin && (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy || !from || !to}
                  title="按当前日期范围重新从 portal 拉取账单（只能拉今天之前的）"
                  onClick={() => void resync()}
                >
                  重新拉取账单
                </Button>
              )}
              <Button size="sm" variant="outline" disabled={!data?.parties.length} onClick={exportCSV}>
                导出 CSV
              </Button>
              <Button size="sm" variant="outline" disabled={loading} onClick={() => void load(from, to)}>
                {loading ? '刷新中…' : '刷新'}
              </Button>
            </div>
          </div>
          <div className="flex flex-wrap items-end gap-x-8 gap-y-2 px-4 py-3">
            {[
              ['合计', totals.cost, undefined],
              ['已结算', totals.settled, 'text-success'],
              ['未结算', totals.unsettled, 'text-destructive'],
            ].map(([label, value, cls]) => (
              <div key={label as string} className="space-y-0.5">
                <div className="text-[11px] text-muted-foreground">{label}</div>
                <div className="text-sm tnum">{money(value as number, cls as string | undefined)}</div>
              </div>
            ))}
            <p className="text-[11px] text-muted-foreground">
              每天 {String(data?.bill_hour ?? 8).padStart(2, '0')}:00（UTC+8）生成前一天的 portal 账单
              {data && (data.synced_through ? `，已生成至 ${data.synced_through}` : '，尚未生成')}；消费为 0 的不计入。
            </p>
          </div>
        </Card>

        {data &&
          (data.parties.length === 0 ? (
            <Card>
              <p className="py-6 text-center text-xs text-muted-foreground">该日期范围内没有消费。</p>
            </Card>
          ) : (
            <>
              {managers.length > 0 && partyTable('Manager 汇总', '按 manager 合并其名下号商，展开看每个号商', managers)}
              {solos.length > 0 && partyTable('未分配 Manager 的号商', '每个号商单独结算，展开看每个账号', solos)}
            </>
          ))}
        {!data && <p className="py-8 text-center text-xs text-muted-foreground">加载中…</p>}
      </div>
    </Layout>
  )
}
