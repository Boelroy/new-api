import { useState, useEffect, useMemo } from 'react'
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid, Cell } from 'recharts'
import Layout from '../components/Layout'
import SummaryCards from '../components/SummaryCards'
import { api, DownstreamDaily, DownstreamPricing, FXRate, ProfitSummary, setProfitApiKey } from '../api'

const COLORS = ['#2563eb','#059669','#d97706','#e11d48','#7c3aed','#ea580c','#0d9488','#c026d3','#3b82f6','#10b981']

function today() { return new Date().toISOString().slice(0, 10) }
function firstOfMonth() {
  const d = new Date()
  d.setUTCDate(1)
  return d.toISOString().slice(0, 10)
}

function fmtUSD(v: number) { return '$' + v.toFixed(2) }
function fmtPct(v: number) { return (v * 100).toFixed(2) + '%' }

export default function Profit() {
  const [start, setStart] = useState(firstOfMonth())
  const [end, setEnd] = useState(today())
  const [profit, setProfit] = useState<ProfitSummary | null>(null)
  const [downstream, setDownstream] = useState<DownstreamPricing[]>([])
  const [fxRates, setFxRates] = useState<FXRate[]>([])
  const [defaultFxRate, setDefaultFxRate] = useState(6.79)
  const [defaultFxEdit, setDefaultFxEdit] = useState<string>('')
  const [pipiStatus, setPipiStatus] = useState<{ configured: boolean; start?: string; end?: string; status?: string; last_sync_at?: number } | null>(null)
  const [pipiSyncing, setPipiSyncing] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  // Feature flag: configChecked → still fetching /api/auth/config;
  // featureEnabled → server says profit_enabled=true.
  const [configChecked, setConfigChecked] = useState(false)
  const [featureEnabled, setFeatureEnabled] = useState(false)
  const [loading, setLoading] = useState(false)
  const [refreshedAt, setRefreshedAt] = useState('')

  // Local edits for downstream group prices.
  const [dsEdits, setDsEdits] = useState<Record<string, { price?: string; note?: string }>>({})
  const [dsNewGroup, setDsNewGroup] = useState('')
  // FX rate edits.
  const [fxEdits, setFxEdits] = useState<Record<string, string>>({})
  const [fxNewDate, setFxNewDate] = useState(today())
  const [fxNewRate, setFxNewRate] = useState('')

  // Main-side per-group per-day downstream discounts. Same pattern as the
  // Remote block below but keyed by (group, date). Bootstrap rows for the
  // 1970-01-01 sentinel are hidden — operators only care about real dates.
  const [ddsRows, setDdsRows] = useState<DownstreamDaily[]>([])
  const [ddsGroup, setDdsGroup] = useState<string>('')
  const [ddsNewDate, setDdsNewDate] = useState(today())
  const [ddsNewDiscount, setDdsNewDiscount] = useState('')
  const [ddsNewNote, setDdsNewNote] = useState('')
  const [ddsSaving, setDdsSaving] = useState(false)
  const [ddsExpanded, setDdsExpanded] = useState(false)

  // Remote per-profile per-day downstream discounts.
  const [rdsRows, setRdsRows] = useState<{ profile_id: number; date: string; discount: number; note: string }[]>([])
  const [rdsProfiles, setRdsProfiles] = useState<{ id: number; name: string }[]>([])
  const [rdsProfileID, setRdsProfileID] = useState<number | null>(null)
  const [rdsNewDate, setRdsNewDate] = useState(today())
  const [rdsNewDiscount, setRdsNewDiscount] = useState('')
  const [rdsNewNote, setRdsNewNote] = useState('')
  const [rdsSaving, setRdsSaving] = useState(false)

  // Remote profit view: collapse channels into per-profile rows by default.
  // Individual channel rows expand under each profile when the operator
  // clicks the ▸ chevron.
  const [expandedProfiles, setExpandedProfiles] = useState<Set<number>>(new Set())

  const reloadRds = async () => {
    try {
      const [profRes, itemsRes] = await Promise.all([
        api.remoteProfiles(),
        api.remoteDownstreamDailyList(),
      ])
      setRdsProfiles(profRes.profiles.map(p => ({ id: p.id, name: p.name })))
      setRdsRows(itemsRes.items)
      if (rdsProfileID == null && profRes.profiles.length > 0) {
        setRdsProfileID(profRes.profiles[0].id)
      }
    } catch (e) {
      console.warn('rds reload', e)
    }
  }

  useEffect(() => { if (featureEnabled) void reloadRds() /* eslint-disable-line react-hooks/exhaustive-deps */ }, [featureEnabled])

  const reloadDds = async () => {
    try {
      const res = await api.listDownstreamDaily()
      // Hide the historical baseline row per group so operators only see
      // dates they'd actually care about editing.
      setDdsRows(res.items.filter(r => r.date !== '1970-01-01'))
    } catch (e) {
      console.warn('dds reload', e)
    }
  }

  useEffect(() => { if (featureEnabled) void reloadDds() /* eslint-disable-line react-hooks/exhaustive-deps */ }, [featureEnabled])

  const ddsGroupsAvailable = useMemo(() => {
    const set = new Set<string>()
    for (const d of downstream) set.add(d.group)
    for (const r of ddsRows) set.add(r.group)
    return Array.from(set).sort()
  }, [downstream, ddsRows])

  const submitDds = async () => {
    const group = (ddsGroup || ddsGroupsAvailable[0] || '').trim()
    if (!group) { alert('请选择 group'); return }
    const v = parseFloat(ddsNewDiscount.trim())
    if (Number.isNaN(v) || v < 0) { alert('折扣必须是 ≥ 0 的数字'); return }
    setDdsSaving(true)
    try {
      await api.saveDownstreamDaily([{ group, date: ddsNewDate, discount: v, note: ddsNewNote.trim() }])
      setDdsNewDiscount('')
      setDdsNewNote('')
      await reloadDds()
      await load()
    } catch (e: any) {
      alert('保存失败: ' + (e?.message || e))
    } finally {
      setDdsSaving(false)
    }
  }

  const deleteDds = async (group: string, date: string) => {
    if (!confirm(`删除 ${group} 在 ${date} 的分段价格？删除后 profit 会回退到更早的分段。`)) return
    try {
      await api.deleteDownstreamDaily(group, date)
      await reloadDds()
      await load()
    } catch (e: any) {
      alert('删除失败: ' + (e?.message || e))
    }
  }

  const submitRds = async () => {
    if (!rdsProfileID) return
    const v = parseFloat(rdsNewDiscount.trim())
    if (isNaN(v) || v < 0) {
      alert('discount 必须是非负数字')
      return
    }
    setRdsSaving(true)
    try {
      await api.remoteDownstreamDailyUpsert({
        profile_id: rdsProfileID,
        date: rdsNewDate,
        discount: v,
        note: rdsNewNote.trim(),
      })
      setRdsNewDiscount('')
      setRdsNewNote('')
      await reloadRds()
    } catch (e: any) {
      alert('保存失败: ' + (e?.message || e))
    } finally {
      setRdsSaving(false)
    }
  }

  const deleteRds = async (pid: number, date: string) => {
    if (!confirm(`删除 profile ${pid} 在 ${date} 的下游折扣？`)) return
    try {
      await api.remoteDownstreamDailyDelete(pid, date)
      await reloadRds()
    } catch (e: any) {
      alert('删除失败: ' + (e?.message || e))
    }
  }

  const load = async () => {
    setLoading(true)
    try {
      const [p, d, fx, ps] = await Promise.all([
        api.getProfitDaily(start, end),
        api.getDownstreamPricing(),
        api.getFXRates(),
        api.getPipiStatus().catch(() => null),
      ])
      setProfit(p)
      setDownstream(d)
      setFxRates(fx.rates)
      setDefaultFxRate(fx.default_rate)
      setDefaultFxEdit('')
      setPipiStatus(ps)
      setRefreshedAt(new Date().toLocaleTimeString('zh-CN'))
      setDsEdits({})
      setFxEdits({})
    } catch (err) {
      console.error(err)
      alert('加载失败：' + (err as Error).message)
    } finally {
      setLoading(false)
    }
  }

  // Check server-side feature flag before doing anything else.
  useEffect(() => {
    // Accept ?key=... once for service-to-service callers and clean it from the URL.
    const params = new URLSearchParams(window.location.search)
    const k = params.get('key')
    if (k) {
      setProfitApiKey(k)
      params.delete('key')
      const newSearch = params.toString()
      window.history.replaceState({}, '', window.location.pathname + (newSearch ? '?' + newSearch : ''))
    }

    void (async () => {
      try {
        const cfg = await fetch('/api/auth/config').then(r => r.json())
        setFeatureEnabled(cfg.profit_enabled === true)
      } catch { /* default off */ }
      setConfigChecked(true)
    })()
  }, [])

  useEffect(() => {
    if (featureEnabled) load()
  }, [featureEnabled])

  const dailyChart = useMemo(() => {
    if (!profit) return []
    return profit.daily.map(d => ({ date: d.date.slice(5), profit: d.profit_usd, rev: d.revenue_usd, cost: d.cost_usd }))
  }, [profit])

  const tagProfitChart = useMemo(() => {
    if (!profit) return []
    return [...profit.by_tag]
      .filter(t => t.revenue_usd > 0 || t.cost_usd > 0)
      .sort((a, b) => b.profit_usd - a.profit_usd)
      .map(t => ({
        name: (t.tag || '(无)') + (t.source === 'pipi' ? ' ·pipi' : t.source === 'remote' ? ' ·remote' : ''),
        profit: t.profit_usd,
      }))
  }, [profit])


  const missing = profit?.missing_pricing
  const hasMissing = !!(missing && ((missing.channel_ids && missing.channel_ids.length) || (missing.groups && missing.groups.length)))

  const submitDownstream = async () => {
    const payload: { group: string; discount: number; note: string }[] = []
    const seen = new Set<string>()

    // Existing rows with edits
    for (const d of downstream) {
      const e = dsEdits[d.group]
      if (!e) continue
      const discount = e.price !== undefined ? parseFloat(e.price) : d.discount
      if (Number.isNaN(discount)) continue
      payload.push({
        group: d.group,
        discount,
        note: e.note ?? d.note,
      })
      seen.add(d.group)
    }

    // Edits on groups not in `downstream` (e.g. missing-pricing rows from the warning banner)
    for (const [g, e] of Object.entries(dsEdits)) {
      if (g === '__new__' || seen.has(g)) continue
      if (e.price === undefined || e.price === '') continue
      const discount = parseFloat(e.price)
      if (Number.isNaN(discount)) continue
      payload.push({ group: g, discount, note: e.note ?? '' })
    }

    // New row (manual add)
    const newG = dsNewGroup.trim()
    if (newG) {
      const e = dsEdits['__new__']
      const discount = e?.price !== undefined ? parseFloat(e.price) : NaN
      if (!Number.isNaN(discount)) {
        payload.push({ group: newG, discount, note: e?.note ?? '' })
      }
    }

    if (payload.length === 0) {
      alert('没有改动（请检查价格是否填写）')
      return
    }
    try {
      const res = await api.saveDownstreamPricing(payload)
      setDsNewGroup('')
      await load()
      alert(`已保存 ${res.saved} 条`)
    } catch (err) {
      alert('保存失败：' + (err as Error).message)
    }
  }

  const submitFXRates = async () => {
    const payload: { date: string; rate: number }[] = []
    // Edited existing rows
    for (const f of fxRates) {
      const v = fxEdits[f.date]
      if (v === undefined) continue
      const rate = parseFloat(v)
      if (Number.isNaN(rate) || rate <= 0) continue
      payload.push({ date: f.date, rate })
    }
    // New entry
    const newRate = parseFloat(fxNewRate)
    if (fxNewDate && !Number.isNaN(newRate) && newRate > 0) {
      payload.push({ date: fxNewDate, rate: newRate })
    }
    if (payload.length === 0) {
      alert('没有改动')
      return
    }
    try {
      const res = await api.saveFXRates(payload)
      setFxNewRate('')
      await load()
      alert(`已保存 ${res.saved} 条`)
    } catch (err) {
      alert('保存失败：' + (err as Error).message)
    }
  }

  const runRefresh = async () => {
    if (refreshing) return
    setRefreshing(true)
    try {
      const r = await api.refreshToday()
      await load()
      const lines = [`已刷新 ${r.date}（总计 ${(r.elapsed_ms / 1000).toFixed(1)}s）`]
      if (r.local_elapsed_ms !== undefined) {
        lines.push(`  本地聚合 ${(r.local_elapsed_ms / 1000).toFixed(1)}s`)
      }
      if (r.pipi_refresh_elapsed_ms !== undefined) {
        lines.push(`  pipi refresh ${(r.pipi_refresh_elapsed_ms / 1000).toFixed(1)}s${r.pipi_refresh_error ? ' ⚠ ' + r.pipi_refresh_error : ''}`)
      }
      if (r.pipi_sync_elapsed_ms !== undefined) {
        lines.push(`  pipi sync ${(r.pipi_sync_elapsed_ms / 1000).toFixed(1)}s${r.pipi_sync_error ? ' ⚠ ' + r.pipi_sync_error : ''}`)
      }
      alert(lines.join('\n'))
    } catch (err) {
      const msg = (err as Error).message
      if (msg.includes('already running')) {
        alert('刷新已在进行，请稍候')
      } else {
        alert('刷新失败：' + msg)
      }
    } finally {
      setRefreshing(false)
    }
  }

  const runPipiSync = async (full = false) => {
    setPipiSyncing(true)
    try {
      // Default to backfilling the queried [start, end] window so the chart
      // matches; "full" flag does 30-day rolling default.
      const payload = full ? {} : { start, end }
      const res = await api.syncPipi(payload)
      await load()
      alert(`pipi 同步完成：${res.start} → ${res.end}`)
    } catch (err) {
      alert('pipi 同步失败：' + (err as Error).message)
    } finally {
      setPipiSyncing(false)
    }
  }

  const saveDefaultFx = async () => {
    const rate = parseFloat(defaultFxEdit)
    if (Number.isNaN(rate) || rate <= 0) {
      alert('请输入有效汇率')
      return
    }
    try {
      await api.saveDefaultFXRate(rate)
      await load()
      alert('默认汇率已保存')
    } catch (err) {
      alert('保存失败：' + (err as Error).message)
    }
  }

  const deleteFXRate = async (date: string) => {
    if (!confirm(`删除 ${date} 的汇率？`)) return
    try {
      await api.deleteFXRate(date)
      await load()
    } catch (err) {
      alert('删除失败：' + (err as Error).message)
    }
  }

  const deleteDownstream = async (group: string) => {
    if (!confirm(`删除 ${group} 的下游售价？`)) return
    try {
      await api.deleteDownstreamPricing(group)
      await load()
    } catch (err) {
      alert('删除失败：' + (err as Error).message)
    }
  }

  // Lookup helpers for warning display on per-key table
  const missingChIDs = new Set(missing?.channel_ids || [])
  const missingGroupSet = new Set(missing?.groups || [])

  const actions = (
    <>
      <input type="date" value={start} onChange={e => setStart(e.target.value)} className="border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-white" />
      <span className="text-gray-300 text-xs">→</span>
      <input type="date" value={end} onChange={e => setEnd(e.target.value)} className="border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-white" />
      <button onClick={load} disabled={loading} className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-50">
        {loading ? '加载中...' : '查询'}
      </button>
      <button
        onClick={runRefresh}
        disabled={refreshing || loading}
        title="重新聚合今日 logs 到 report_daily_agg"
        className="border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50 disabled:opacity-50 disabled:cursor-not-allowed"
      >
        {refreshing ? '刷新中…' : '刷新今日'}
      </button>
    </>
  )

  // Feature flag gate — the entire profit feature is off on this deployment.
  if (!configChecked) {
    return (
      <Layout title="Profit Report">
        <div className="text-center text-gray-400 text-xs py-12">加载中…</div>
      </Layout>
    )
  }
  if (!featureEnabled) {
    return (
      <Layout title="Profit Report" subtitle="此部署未启用该功能">
        <div className="max-w-sm mx-auto bg-white border border-gray-200 rounded-xl p-6 mt-6 text-center">
          <div className="text-sm font-semibold mb-2">Profit Report is disabled</div>
          <div className="text-[11px] text-gray-400">设置 PROFIT_ENABLED=true 后重启服务可启用</div>
        </div>
      </Layout>
    )
  }

  return (
    <Layout
      title="Profit Report"
      subtitle={`${start} ~ ${end} (UTC)${refreshedAt ? ` · 更新于 ${refreshedAt}` : ''}`}
      actions={actions}
    >
      {hasMissing && (
        <div className="bg-amber-50 border border-amber-200 rounded-xl px-4 py-2.5 mb-4 text-xs text-amber-900">
          <span className="font-semibold">⚠ 缺少定价</span>
          {missing!.channel_ids && missing!.channel_ids.length > 0 && (
            <span className="ml-3">渠道未配上游单价：{missing!.channel_ids.slice(0, 8).join(', ')}{missing!.channel_ids.length > 8 ? `… 共 ${missing!.channel_ids.length} 个` : ''}</span>
          )}
          {missing!.groups && missing!.groups.length > 0 && (
            <span className="ml-3">下游 group 未配售价：{missing!.groups.slice(0, 8).join(', ')}{missing!.groups.length > 8 ? `… 共 ${missing!.groups.length} 个` : ''}</span>
          )}
        </div>
      )}

      {profit && (
        <SummaryCards cards={[
          { label: '总用量', value: fmtUSD(profit.used_usd) },
          { label: '上游成本', value: fmtUSD(profit.cost_usd), color: 'text-rose-600' },
          { label: '下游收入', value: fmtUSD(profit.revenue_usd), color: 'text-blue-600' },
          { label: '毛利', value: fmtUSD(profit.profit_usd), color: 'text-emerald-600' },
          { label: '毛利率', value: fmtPct(profit.profit_rate), color: 'text-emerald-600' },
          { label: '默认汇率', value: defaultFxRate.toFixed(2) + ' CNY/USD', color: 'text-gray-500' },
        ]} />
      )}

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 mb-6">
        <div className="bg-white border border-gray-200 rounded-xl p-4">
          <h3 className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-3">Daily Profit ($)</h3>
          <ResponsiveContainer width="100%" height={200}>
            <BarChart data={dailyChart} margin={{ top: 0, right: 0, bottom: 0, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} />
              <YAxis tick={{ fontSize: 10 }} tickFormatter={v => '$' + v} />
              <Tooltip formatter={(v: number) => ['$' + v.toFixed(2), 'Profit']} />
              <Bar dataKey="profit" fill="#10b981" radius={[3,3,0,0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
        <div className="bg-white border border-gray-200 rounded-xl p-4">
          <h3 className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-3">Profit by Tag ($)</h3>
          <ResponsiveContainer width="100%" height={200}>
            <BarChart data={tagProfitChart} margin={{ top: 0, right: 0, bottom: 0, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="name" tick={{ fontSize: 9 }} />
              <YAxis tick={{ fontSize: 10 }} tickFormatter={v => '$' + v.toFixed(0)} />
              <Tooltip formatter={(v: number) => ['$' + v.toFixed(2), '毛利']} />
              <Bar dataKey="profit" radius={[3,3,0,0]}>
                {tagProfitChart.map((d, i) => (
                  <Cell key={i} fill={d.profit >= 0 ? '#10b981' : '#e11d48'} />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>

      <div className="bg-white border border-gray-200 rounded-xl mb-4">
        <div className="flex items-center justify-between px-4 py-3 border-b border-gray-100 gap-3 flex-wrap">
          <div>
            <div className="text-sm font-semibold">按供应商（Tag）分组</div>
            <div className="text-[10px] text-gray-400 uppercase tracking-wider mt-0.5">每个上游供应商的用量与成本</div>
          </div>
          {pipiStatus?.configured && (
            <div className="flex items-center gap-2">
              {pipiStatus.last_sync_at ? (
                <span className="text-[10px] text-gray-400 tabular-nums">
                  pipi {pipiStatus.start}~{pipiStatus.end}
                  {pipiStatus.status === 'ok' ? '' : ` · ${pipiStatus.status ?? ''}`}
                  · {new Date(pipiStatus.last_sync_at * 1000).toLocaleString('zh-CN', { hour12: false })}
                </span>
              ) : (
                <span className="text-[10px] text-gray-400">pipi 未同步</span>
              )}
              <button
                onClick={() => runPipiSync(false)}
                disabled={pipiSyncing}
                className="border border-gray-200 rounded-md px-2.5 py-1 text-[11px] bg-white hover:bg-gray-50 disabled:opacity-40"
              >
                {pipiSyncing ? '同步中…' : '同步当前区间'}
              </button>
              <button
                onClick={() => runPipiSync(true)}
                disabled={pipiSyncing}
                className="border border-gray-200 rounded-md px-2.5 py-1 text-[11px] bg-white hover:bg-gray-50 disabled:opacity-40"
                title="同步近 30 天"
              >
                30天回填
              </button>
            </div>
          )}
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-xs whitespace-nowrap">
            <thead>
              <tr className="bg-gray-50">
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">来源</th>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">Tag</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">Key 数</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">用量 USD</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">上游成本 USD</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">下游收入 USD</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">毛利 USD</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">毛利率</th>
              </tr>
            </thead>
            <tbody>
              {profit && [...profit.by_tag].sort((a, b) => b.cost_usd - a.cost_usd).map((t, i) => (
                <tr key={i} className="hover:bg-gray-50 border-t border-gray-100">
                  <td className="px-3 py-1.5">
                    <span className={`px-1.5 py-0.5 rounded text-[10px] ${t.source === 'pipi' ? 'bg-violet-100 text-violet-700' : t.source === 'remote' ? 'bg-amber-100 text-amber-700' : 'bg-blue-100 text-blue-700'}`}>{t.source}</span>
                  </td>
                  <td className="px-3 py-1.5 font-mono">{t.tag || <span className="text-gray-400">(无)</span>}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums text-gray-500">{t.key_count}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{t.used_usd.toFixed(2)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums text-rose-600">{t.cost_usd.toFixed(4)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums text-blue-600">{t.revenue_usd.toFixed(4)}</td>
                  <td className={`px-3 py-1.5 text-right tabular-nums font-medium ${t.profit_usd >= 0 ? 'text-emerald-600' : 'text-rose-700'}`}>{t.profit_usd.toFixed(4)}</td>
                  <td className={`px-3 py-1.5 text-right tabular-nums ${t.profit_rate >= 0 ? '' : 'text-rose-700'}`}>{fmtPct(t.profit_rate)}</td>
                </tr>
              ))}
              {(!profit || profit.by_tag.length === 0) && (
                <tr><td colSpan={8} className="px-3 py-3 text-center text-gray-400 text-[11px]">暂无数据</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      {/* Remote Channels — one row per (profile, channel) with local
          upstream (unit_price_cny) vs configured downstream (downstream_cny)
          applied to daily deltas from remote_channel_snapshot. */}
      {profit && profit.by_remote_channel && profit.by_remote_channel.length > 0 && (() => {
        // Collapse channels into per-profile rows so 196 rows don't drown
        // the section. Each aggregate row shows totals; expanding reveals
        // the underlying channel rows sorted by profit desc.
        type Agg = {
          profileID: number
          profileName: string
          channelCount: number
          usedUSD: number
          costUSD: number
          revenueUSD: number
          profitUSD: number
          discount: number | null
        }
        const byProfile = new Map<number, Agg>()
        for (const r of profit.by_remote_channel!) {
          let a = byProfile.get(r.profile_id)
          if (!a) {
            a = {
              profileID: r.profile_id,
              profileName: r.profile_name || String(r.profile_id),
              channelCount: 0,
              usedUSD: 0,
              costUSD: 0,
              revenueUSD: 0,
              profitUSD: 0,
              discount: null,
            }
            byProfile.set(r.profile_id, a)
          }
          a.channelCount += 1
          a.usedUSD += r.used_usd
          a.costUSD += r.cost_usd
          a.revenueUSD += r.revenue_usd
          a.profitUSD += r.profit_usd
          // Prefer the largest discount seen so the collapsed row shows
          // the operator-configured multiplier when it's consistent.
          if (r.downstream_discount != null) {
            a.discount = a.discount == null ? r.downstream_discount : Math.max(a.discount, r.downstream_discount)
          }
        }
        const aggRows = Array.from(byProfile.values()).sort((a, b) => b.profitUSD - a.profitUSD)
        const toggleProfile = (pid: number) => {
          setExpandedProfiles(prev => {
            const next = new Set(prev)
            if (next.has(pid)) next.delete(pid)
            else next.add(pid)
            return next
          })
        }
        return (
        <div className="bg-white border border-gray-200 rounded-xl mb-4">
          <div className="flex items-center justify-between px-4 py-3 border-b border-gray-100">
            <div>
              <div className="text-sm font-semibold">Remote Channels 毛利 <span className="text-[10px] text-gray-400 font-normal">（已计入顶部合计）</span></div>
              <div className="text-[10px] text-gray-400 uppercase tracking-wider mt-0.5">
                {profit.by_remote_channel!.length} channels across {aggRows.length} profile{aggRows.length === 1 ? '' : 's'} · used = ${(profit.remote_used_usd ?? 0).toFixed(2)} · cost = ${(profit.remote_cost_usd ?? 0).toFixed(2)} · revenue = ${(profit.remote_revenue_usd ?? 0).toFixed(2)} · profit = <span className={(profit.remote_profit_usd ?? 0) >= 0 ? 'text-emerald-600' : 'text-rose-700'}>${(profit.remote_profit_usd ?? 0).toFixed(2)}</span>
              </div>
            </div>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead className="bg-gray-50">
                <tr>
                  <th className="px-2 py-2 w-6"></th>
                  <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">Profile</th>
                  <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">渠道数</th>
                  <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">用量 USD</th>
                  <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400" title="profile 当日下游折扣 (最大值)">下游 ×</th>
                  <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">上游成本 USD</th>
                  <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">下游收入 USD</th>
                  <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">毛利 USD</th>
                  <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">毛利率</th>
                </tr>
              </thead>
              <tbody>
                {aggRows.map(a => {
                  const isOpen = expandedProfiles.has(a.profileID)
                  const profitRate = a.usedUSD > 0 ? (a.revenueUSD - a.costUSD) / a.usedUSD : 0
                  return (
                    <>
                      <tr key={`p-${a.profileID}`} className="hover:bg-gray-50 border-t border-gray-100 font-medium">
                        <td className="px-2 py-1.5 text-center">
                          <button
                            onClick={() => toggleProfile(a.profileID)}
                            className={`text-[10px] ${isOpen ? 'text-emerald-600' : 'text-gray-400 hover:text-gray-700'}`}
                          >
                            {isOpen ? '▾' : '▸'}
                          </button>
                        </td>
                        <td className="px-3 py-1.5">{a.profileName}</td>
                        <td className="px-3 py-1.5 text-right tabular-nums text-gray-500">{a.channelCount}</td>
                        <td className="px-3 py-1.5 text-right tabular-nums">{a.usedUSD.toFixed(2)}</td>
                        <td className="px-3 py-1.5 text-right tabular-nums text-blue-600">{a.discount != null ? '×' + a.discount.toFixed(4) : '—'}</td>
                        <td className="px-3 py-1.5 text-right tabular-nums text-rose-600">{a.costUSD.toFixed(4)}</td>
                        <td className="px-3 py-1.5 text-right tabular-nums text-blue-600">{a.revenueUSD.toFixed(4)}</td>
                        <td className={`px-3 py-1.5 text-right tabular-nums ${a.profitUSD >= 0 ? 'text-emerald-600' : 'text-rose-700'}`}>{a.profitUSD.toFixed(4)}</td>
                        <td className={`px-3 py-1.5 text-right tabular-nums ${profitRate >= 0 ? '' : 'text-rose-700'}`}>{fmtPct(profitRate)}</td>
                      </tr>
                      {isOpen && profit.by_remote_channel!
                        .filter(r => r.profile_id === a.profileID)
                        .sort((x, y) => y.profit_usd - x.profit_usd)
                        .map(r => (
                          <tr key={`p-${a.profileID}-c-${r.channel_id}`} className="bg-gray-50/40 hover:bg-gray-50 border-t border-gray-100">
                            <td className="px-2 py-1.5"></td>
                            <td className="px-3 py-1.5 font-mono text-[11px] text-gray-500 truncate max-w-[280px]" title={r.channel_name} colSpan={2}>
                              ↳ {r.channel_name || `#${r.channel_id}`}
                            </td>
                            <td className="px-3 py-1.5 text-right tabular-nums">{r.used_usd.toFixed(2)}</td>
                            <td className="px-3 py-1.5 text-right tabular-nums text-blue-600">{r.downstream_discount != null ? '×' + r.downstream_discount.toFixed(4) : '—'}</td>
                            <td className="px-3 py-1.5 text-right tabular-nums text-rose-600">{r.cost_usd.toFixed(4)}</td>
                            <td className="px-3 py-1.5 text-right tabular-nums text-blue-600">{r.revenue_usd.toFixed(4)}</td>
                            <td className={`px-3 py-1.5 text-right tabular-nums ${r.profit_usd >= 0 ? 'text-emerald-600' : 'text-rose-700'}`}>{r.profit_usd.toFixed(4)}</td>
                            <td className={`px-3 py-1.5 text-right tabular-nums ${r.profit_rate >= 0 ? '' : 'text-rose-700'}`}>{fmtPct(r.profit_rate)}</td>
                          </tr>
                        ))}
                    </>
                  )
                })}
              </tbody>
            </table>
          </div>
          <div className="px-4 py-2 text-[10px] text-gray-400 border-t border-gray-100">
            用量 = last snapshot(day D) − last snapshot(day D−1)（15min cron 采样）· 上游/下游未配置时该项按 0 处理 · 点 ▸ 展开看渠道明细
          </div>
        </div>
        )
      })()}

      <div className="bg-white border border-gray-200 rounded-xl mb-4">
        <div className="flex items-center justify-between px-4 py-3 border-b border-gray-100">
          <div>
            <div className="text-sm font-semibold">每日毛利明细</div>
            <div className="text-[10px] text-gray-400 uppercase tracking-wider mt-0.5">Daily Breakdown</div>
          </div>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-xs whitespace-nowrap">
            <thead>
              <tr className="bg-gray-50">
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">日期</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">汇率</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">用量 USD</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">上游成本 USD</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">下游收入 USD</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">毛利 USD</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">毛利率</th>
              </tr>
            </thead>
            <tbody>
              {profit?.daily.map(d => (
                <tr key={d.date} className="hover:bg-gray-50 border-t border-gray-100">
                  <td className="px-3 py-1.5">{d.date}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums text-gray-500">{d.fx_rate.toFixed(2)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{d.used_usd.toFixed(2)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums text-rose-600">{d.cost_usd.toFixed(4)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums text-blue-600">{d.revenue_usd.toFixed(4)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums font-medium text-emerald-600">{d.profit_usd.toFixed(4)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{fmtPct(d.profit_rate)}</td>
                </tr>
              ))}
              {profit && (
                <tr className="bg-emerald-50 font-semibold border-t-2 border-emerald-200">
                  <td className="px-3 py-1.5">TOTAL</td>
                  <td className="px-3 py-1.5"></td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{profit.used_usd.toFixed(2)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{profit.cost_usd.toFixed(4)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{profit.revenue_usd.toFixed(4)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums text-emerald-700">{profit.profit_usd.toFixed(4)}</td>
                  <td className="px-3 py-1.5 text-right tabular-nums">{fmtPct(profit.profit_rate)}</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      <div className="bg-white border border-gray-200 rounded-xl mb-4">
        <div className="flex items-center justify-between px-4 py-3 border-b border-gray-100 gap-3 flex-wrap">
          <div>
            <div className="text-sm font-semibold">每日汇率</div>
            <div className="text-[10px] text-gray-400 uppercase tracking-wider mt-0.5">CNY per USD · 缺日期回退到默认</div>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-[11px] text-gray-500">默认</span>
            <input
              type="number"
              step="0.01"
              value={defaultFxEdit === '' ? String(defaultFxRate) : defaultFxEdit}
              onChange={ev => setDefaultFxEdit(ev.target.value)}
              className="w-20 border border-gray-200 rounded px-1.5 py-0.5 text-right text-xs"
            />
            <button
              onClick={saveDefaultFx}
              disabled={defaultFxEdit === '' || parseFloat(defaultFxEdit) === defaultFxRate}
              className="border border-gray-200 rounded-md px-2.5 py-1 text-[11px] bg-white hover:bg-gray-50 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              更新默认
            </button>
            <button onClick={submitFXRates} className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85">保存改动</button>
          </div>
        </div>
        <div className="overflow-x-auto max-h-[260px] overflow-y-auto">
          <table className="w-full text-xs whitespace-nowrap">
            <thead className="sticky top-0 bg-gray-50">
              <tr>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">日期</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">汇率</th>
                <th className="px-3 py-2 w-12"></th>
              </tr>
            </thead>
            <tbody>
              <tr className="border-t border-gray-100 bg-gray-50/60">
                <td className="px-3 py-1.5">
                  <input
                    type="date"
                    value={fxNewDate}
                    onChange={ev => setFxNewDate(ev.target.value)}
                    className="border border-gray-200 rounded px-1.5 py-0.5 text-xs"
                  />
                </td>
                <td className="px-3 py-1.5 text-right">
                  <input
                    type="number"
                    step="0.01"
                    value={fxNewRate}
                    onChange={ev => setFxNewRate(ev.target.value)}
                    placeholder={defaultFxRate.toFixed(2)}
                    className="w-24 border border-gray-200 rounded px-1.5 py-0.5 text-right text-xs"
                  />
                </td>
                <td className="px-3 py-1.5 text-[10px] text-gray-400">新增</td>
              </tr>
              {fxRates.map(f => (
                <tr key={f.date} className="border-t border-gray-100 hover:bg-gray-50">
                  <td className="px-3 py-1.5 font-mono">{f.date}</td>
                  <td className="px-3 py-1.5 text-right">
                    <input
                      type="number"
                      step="0.01"
                      value={fxEdits[f.date] ?? String(f.rate)}
                      onChange={ev => setFxEdits(prev => ({ ...prev, [f.date]: ev.target.value }))}
                      className="w-24 border border-gray-200 rounded px-1.5 py-0.5 text-right text-xs"
                    />
                  </td>
                  <td className="px-3 py-1.5">
                    <button onClick={() => deleteFXRate(f.date)} className="text-rose-500 hover:text-rose-700 text-[11px]">删</button>
                  </td>
                </tr>
              ))}
              {fxRates.length === 0 && (
                <tr><td colSpan={3} className="px-3 py-3 text-center text-gray-400 text-[11px]">尚未配置任何汇率，全部使用默认 {defaultFxRate.toFixed(2)}</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      <div className="bg-amber-50 border border-amber-200 rounded-xl px-4 py-2.5 mb-4 text-xs text-amber-900">
        💡 上游 key 的单价配置已移至 <a href="/allkeys" className="underline hover:text-amber-700">All Keys</a> 页面
      </div>

      <div className="bg-white border border-gray-200 rounded-xl">
        <div className="flex items-center justify-between px-4 py-3 border-b border-gray-100">
          <div>
            <div className="text-sm font-semibold">下游售价配置</div>
            <div className="text-[10px] text-gray-400 uppercase tracking-wider mt-0.5">Per-Group discount (revenue_usd = used_usd × discount)</div>
          </div>
          <button onClick={submitDownstream} className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85">保存改动</button>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-xs whitespace-nowrap">
            <thead className="bg-gray-50">
              <tr>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">Group</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">折扣</th>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">备注</th>
                <th className="px-3 py-2 w-12"></th>
              </tr>
            </thead>
            <tbody>
              {downstream.map(d => {
                const e = dsEdits[d.group] || {}
                const priceVal = e.price !== undefined ? e.price : String(d.discount)
                const noteVal = e.note !== undefined ? e.note : d.note
                const isMissing = missingGroupSet.has(d.group)
                return (
                  <tr key={d.group} className={`border-t border-gray-100 hover:bg-gray-50 ${isMissing ? 'bg-amber-50/50' : ''}`}>
                    <td className="px-3 py-1.5 font-mono">{d.group || '(空)'}</td>
                    <td className="px-3 py-1.5 text-right">
                      <input
                        type="number"
                        step="0.01"
                        value={priceVal}
                        onChange={ev => setDsEdits(prev => ({ ...prev, [d.group]: { ...prev[d.group], price: ev.target.value } }))}
                        className="w-20 border border-gray-200 rounded px-1.5 py-0.5 text-right text-xs"
                      />
                    </td>
                    <td className="px-3 py-1.5">
                      <input
                        type="text"
                        value={noteVal}
                        onChange={ev => setDsEdits(prev => ({ ...prev, [d.group]: { ...prev[d.group], note: ev.target.value } }))}
                        className="w-40 border border-gray-200 rounded px-1.5 py-0.5 text-xs"
                      />
                    </td>
                    <td className="px-3 py-1.5">
                      <button onClick={() => deleteDownstream(d.group)} className="text-rose-500 hover:text-rose-700 text-[11px]">删</button>
                    </td>
                  </tr>
                )
              })}
              {/* Show missing groups (referenced in logs but no pricing row) */}
              {missing?.groups?.filter(g => !downstream.some(d => d.group === g)).map(g => {
                const e = dsEdits[g] || {}
                return (
                  <tr key={'missing-' + g} className="border-t border-gray-100 bg-amber-50/50">
                    <td className="px-3 py-1.5 font-mono">{g || '(空)'}</td>
                    <td className="px-3 py-1.5 text-right">
                      <input
                        type="number"
                        step="0.01"
                        value={e.price ?? ''}
                        onChange={ev => setDsEdits(prev => ({ ...prev, [g]: { ...prev[g], price: ev.target.value } }))}
                        placeholder="缺"
                        className="w-20 border border-amber-300 bg-white rounded px-1.5 py-0.5 text-right text-xs"
                      />
                    </td>
                    <td className="px-3 py-1.5">
                      <input
                        type="text"
                        value={e.note ?? ''}
                        onChange={ev => setDsEdits(prev => ({ ...prev, [g]: { ...prev[g], note: ev.target.value } }))}
                        placeholder="备注（新增）"
                        className="w-40 border border-gray-200 rounded px-1.5 py-0.5 text-xs"
                      />
                    </td>
                    <td></td>
                  </tr>
                )
              })}
              {/* New row */}
              <tr className="border-t border-gray-100">
                <td className="px-3 py-1.5">
                  <input
                    type="text"
                    value={dsNewGroup}
                    onChange={ev => setDsNewGroup(ev.target.value)}
                    placeholder="新 group 名"
                    className="w-32 border border-gray-200 rounded px-1.5 py-0.5 text-xs"
                  />
                </td>
                <td className="px-3 py-1.5 text-right">
                  <input
                    type="number"
                    step="0.01"
                    value={dsEdits['__new__']?.price ?? ''}
                    onChange={ev => setDsEdits(prev => ({ ...prev, __new__: { ...prev['__new__'], price: ev.target.value } }))}
                    placeholder="0.85"
                    className="w-20 border border-gray-200 rounded px-1.5 py-0.5 text-right text-xs"
                  />
                </td>
                <td className="px-3 py-1.5">
                  <input
                    type="text"
                    value={dsEdits['__new__']?.note ?? ''}
                    onChange={ev => setDsEdits(prev => ({ ...prev, __new__: { ...prev['__new__'], note: ev.target.value } }))}
                    className="w-40 border border-gray-200 rounded px-1.5 py-0.5 text-xs"
                  />
                </td>
                <td></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      {/* Main-side per-group per-day downstream pricing. Same lookup rule
          as remote: "latest date ≤ report day" wins. Used to record price
          changes with a fixed effective date so historical profit stays
          consistent instead of re-costing after every price bump. */}
      <div className="bg-white border border-gray-200 rounded-xl mt-4">
        <button
          type="button"
          onClick={() => setDdsExpanded(v => !v)}
          className={`w-full flex items-center justify-between px-4 py-3 text-left ${ddsExpanded ? 'border-b border-gray-100' : ''}`}
        >
          <div>
            <div className="text-sm font-semibold">下游分段价格历史</div>
            <div className="text-[10px] text-gray-400 uppercase tracking-wider mt-0.5">
              Per-group per-day discount · 每天自动从前一日 carry-forward · 手动改此表覆盖当日
            </div>
          </div>
          <span className={`text-xs ${ddsExpanded ? 'text-emerald-600' : 'text-gray-400'}`}>
            {ddsExpanded ? '▾' : '▸'}
          </span>
        </button>
        {ddsExpanded && (
          <>
        <div className="p-4 border-b border-gray-100 flex flex-wrap items-end gap-3">
          <div>
            <label className="block text-[10px] text-gray-400 uppercase tracking-wider mb-1">Group</label>
            <select
              value={ddsGroup}
              onChange={e => setDdsGroup(e.target.value)}
              className="border border-gray-300 rounded px-2 py-1 text-xs bg-white"
            >
              <option value="">（选择 group）</option>
              {ddsGroupsAvailable.map(g => <option key={g} value={g}>{g}</option>)}
            </select>
          </div>
          <div>
            <label className="block text-[10px] text-gray-400 uppercase tracking-wider mb-1">生效日期 (UTC)</label>
            <input
              type="date"
              value={ddsNewDate}
              onChange={e => setDdsNewDate(e.target.value)}
              className="border border-gray-300 rounded px-2 py-1 text-xs bg-white"
            />
          </div>
          <div>
            <label className="block text-[10px] text-gray-400 uppercase tracking-wider mb-1">折扣 (×)</label>
            <input
              type="number"
              step="0.0001"
              min="0"
              value={ddsNewDiscount}
              onChange={e => setDdsNewDiscount(e.target.value)}
              placeholder="例如 0.73"
              className="w-24 border border-gray-300 rounded px-2 py-1 text-xs tabular-nums"
            />
          </div>
          <div className="flex-1 min-w-[120px]">
            <label className="block text-[10px] text-gray-400 uppercase tracking-wider mb-1">备注</label>
            <input
              type="text"
              value={ddsNewNote}
              onChange={e => setDdsNewNote(e.target.value)}
              placeholder="可选"
              className="w-full border border-gray-300 rounded px-2 py-1 text-xs"
            />
          </div>
          <button
            onClick={submitDds}
            disabled={ddsSaving}
            className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-40"
          >
            {ddsSaving ? '保存中…' : '保存 / 覆盖'}
          </button>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-xs whitespace-nowrap">
            <thead className="bg-gray-50">
              <tr>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">Group</th>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">生效日期</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">折扣</th>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">备注</th>
                <th className="px-3 py-2 w-12"></th>
              </tr>
            </thead>
            <tbody>
              {ddsRows.length === 0 && (
                <tr><td colSpan={5} className="px-3 py-3 text-center text-gray-400 text-[11px]">
                  暂无分段。填上面表单保存即可 —— 未配置的日期沿用最近一次。
                </td></tr>
              )}
              {ddsRows
                .filter(r => !ddsGroup || r.group === ddsGroup)
                .map(row => (
                  <tr key={`${row.group}-${row.date}`} className="border-t border-gray-100 hover:bg-gray-50">
                    <td className="px-3 py-1.5">{row.group}</td>
                    <td className="px-3 py-1.5 font-mono">{row.date}</td>
                    <td className="px-3 py-1.5 text-right tabular-nums text-blue-600">×{row.discount.toFixed(4)}</td>
                    <td className="px-3 py-1.5 text-gray-500">
                      {row.note ? row.note : <span className="text-gray-300">—</span>}
                    </td>
                    <td className="px-3 py-1.5 text-right">
                      <button onClick={() => void deleteDds(row.group, row.date)} className="text-[10px] text-rose-500 hover:text-rose-700">删除</button>
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
          </>
        )}
      </div>

      {/* Remote per-profile per-day downstream discount. Rows are looked
          up as "latest date ≤ report day" — set once, next day inherits. */}
      <div className="bg-white border border-gray-200 rounded-xl mt-4">
        <div className="flex items-center justify-between px-4 py-3 border-b border-gray-100">
          <div>
            <div className="text-sm font-semibold">Remote 每日下游折扣</div>
            <div className="text-[10px] text-gray-400 uppercase tracking-wider mt-0.5">
              Per-profile per-day multiplier · revenue_usd = used_usd × discount · 未配置日期沿用上一次
            </div>
          </div>
        </div>
        <div className="p-4 border-b border-gray-100 flex flex-wrap items-end gap-3">
          <div>
            <label className="block text-[10px] text-gray-400 uppercase tracking-wider mb-1">Profile</label>
            <select
              value={rdsProfileID ?? ''}
              onChange={e => setRdsProfileID(e.target.value ? Number(e.target.value) : null)}
              className="border border-gray-300 rounded px-2 py-1 text-xs bg-white"
            >
              {rdsProfiles.length === 0 && <option value="">（无 profile）</option>}
              {rdsProfiles.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
          </div>
          <div>
            <label className="block text-[10px] text-gray-400 uppercase tracking-wider mb-1">生效日期</label>
            <input
              type="date"
              value={rdsNewDate}
              onChange={e => setRdsNewDate(e.target.value)}
              className="border border-gray-300 rounded px-2 py-1 text-xs bg-white"
            />
          </div>
          <div>
            <label className="block text-[10px] text-gray-400 uppercase tracking-wider mb-1">折扣 (×)</label>
            <input
              type="number"
              step="0.001"
              min="0"
              value={rdsNewDiscount}
              onChange={e => setRdsNewDiscount(e.target.value)}
              placeholder="例如 1.5"
              className="w-24 border border-gray-300 rounded px-2 py-1 text-xs tabular-nums"
            />
          </div>
          <div className="flex-1 min-w-[120px]">
            <label className="block text-[10px] text-gray-400 uppercase tracking-wider mb-1">备注</label>
            <input
              type="text"
              value={rdsNewNote}
              onChange={e => setRdsNewNote(e.target.value)}
              placeholder="可选"
              className="w-full border border-gray-300 rounded px-2 py-1 text-xs"
            />
          </div>
          <button
            onClick={submitRds}
            disabled={rdsSaving || rdsProfileID == null}
            className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-40"
          >
            {rdsSaving ? '保存中…' : '保存 / 覆盖'}
          </button>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-xs whitespace-nowrap">
            <thead className="bg-gray-50">
              <tr>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">Profile</th>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">生效日期</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">折扣</th>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">备注</th>
                <th className="px-3 py-2 w-12"></th>
              </tr>
            </thead>
            <tbody>
              {rdsRows.length === 0 && (
                <tr><td colSpan={5} className="px-3 py-3 text-center text-gray-400 text-[11px]">
                  暂无配置。填上面表单保存即可，未配置的天沿用最近一次。
                </td></tr>
              )}
              {rdsRows.map(row => {
                const pname = rdsProfiles.find(p => p.id === row.profile_id)?.name ?? row.profile_id
                return (
                  <tr key={`${row.profile_id}-${row.date}`} className="border-t border-gray-100 hover:bg-gray-50">
                    <td className="px-3 py-1.5">{pname}</td>
                    <td className="px-3 py-1.5 font-mono">{row.date}</td>
                    <td className="px-3 py-1.5 text-right tabular-nums text-blue-600">×{row.discount.toFixed(4)}</td>
                    <td className="px-3 py-1.5 text-gray-500">{row.note || <span className="text-gray-300">—</span>}</td>
                    <td className="px-3 py-1.5 text-right">
                      <button onClick={() => void deleteRds(row.profile_id, row.date)} className="text-[10px] text-rose-500 hover:text-rose-700">删除</button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </div>
    </Layout>
  )
}
