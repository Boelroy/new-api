import { useState, useEffect, useMemo } from 'react'
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid, Cell } from 'recharts'
import Layout from '../components/Layout'
import SummaryCards from '../components/SummaryCards'
import { api, ChannelRow, DownstreamPricing, FXRate, ProfitSummary } from '../api'

const COLORS = ['#2563eb','#059669','#d97706','#e11d48','#7c3aed','#ea580c','#0d9488','#c026d3','#3b82f6','#10b981']

function today() { return new Date().toISOString().slice(0, 10) }
function daysAgo(n: number) {
  const d = new Date(); d.setUTCDate(d.getUTCDate() - n)
  return d.toISOString().slice(0, 10)
}

function fmtUSD(v: number) { return '$' + v.toFixed(2) }
function fmtPct(v: number) { return (v * 100).toFixed(2) + '%' }

export default function Profit() {
  const [start, setStart] = useState(daysAgo(6))
  const [end, setEnd] = useState(today())
  const [profit, setProfit] = useState<ProfitSummary | null>(null)
  const [keys, setKeys] = useState<ChannelRow[]>([])
  const [downstream, setDownstream] = useState<DownstreamPricing[]>([])
  const [fxRates, setFxRates] = useState<FXRate[]>([])
  const [defaultFxRate, setDefaultFxRate] = useState(6.79)
  const [defaultFxEdit, setDefaultFxEdit] = useState<string>('')
  const [loading, setLoading] = useState(false)
  const [refreshedAt, setRefreshedAt] = useState('')

  // Local edits buffer for per-key prices.
  const [keyEdits, setKeyEdits] = useState<Record<number, { price?: string; note?: string }>>({})
  const [keyOnlyUnpriced, setKeyOnlyUnpriced] = useState(false)
  // Local edits for downstream group prices.
  const [dsEdits, setDsEdits] = useState<Record<string, { price?: string; note?: string }>>({})
  const [dsNewGroup, setDsNewGroup] = useState('')
  // FX rate edits.
  const [fxEdits, setFxEdits] = useState<Record<string, string>>({})
  const [fxNewDate, setFxNewDate] = useState(today())
  const [fxNewRate, setFxNewRate] = useState('')
  // Bulk-import textarea state.
  const [bulkText, setBulkText] = useState('')
  const [bulkResult, setBulkResult] = useState<{ saved: number; not_found: string[]; errors: { line: number; reason: string }[] } | null>(null)
  const [bulkSubmitting, setBulkSubmitting] = useState(false)

  const load = async () => {
    setLoading(true)
    try {
      const [p, k, d, fx] = await Promise.all([
        api.getProfitDaily(start, end),
        api.getAllKeys(),
        api.getDownstreamPricing(),
        api.getFXRates(),
      ])
      setProfit(p)
      setKeys(k.sort((a, b) => a.id - b.id))
      setDownstream(d)
      setFxRates(fx.rates)
      setDefaultFxRate(fx.default_rate)
      setDefaultFxEdit('')
      setRefreshedAt(new Date().toLocaleTimeString('zh-CN'))
      setKeyEdits({})
      setDsEdits({})
      setFxEdits({})
    } catch (err) {
      console.error(err)
      alert('加载失败：' + (err as Error).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load() }, [])

  const dailyChart = useMemo(() => {
    if (!profit) return []
    return profit.daily.map(d => ({ date: d.date.slice(5), profit: d.profit_usd, rev: d.revenue_usd, cost: d.cost_usd }))
  }, [profit])

  const keyChart = useMemo(() => {
    if (!profit) return []
    return [...profit.by_key]
      .sort((a, b) => b.cost_usd - a.cost_usd)
      .slice(0, 10)
      .map(k => ({ name: k.channel_name || `#${k.channel_id}`, cost: k.cost_usd, tag: k.tag }))
  }, [profit])

  // Profit report is scoped to Claude-serving channels on the backend
  // (14 Anthropic direct / 33 AWS Bedrock). Mirror that filter here.
  const anthropicKeys = useMemo(() => keys.filter(k => k.type === 14 || k.type === 33), [keys])
  const filteredKeys = useMemo(() => {
    if (!keyOnlyUnpriced) return anthropicKeys
    return anthropicKeys.filter(k => k.unit_price_cny == null)
  }, [anthropicKeys, keyOnlyUnpriced])

  const unpricedKeyCount = useMemo(() => anthropicKeys.filter(k => k.unit_price_cny == null).length, [anthropicKeys])

  const missing = profit?.missing_pricing
  const hasMissing = !!(missing && ((missing.channel_ids && missing.channel_ids.length) || (missing.groups && missing.groups.length)))

  const submitKeyPricing = async () => {
    const payload = Object.entries(keyEdits)
      .map(([id, e]) => {
        const channel_id = Number(id)
        const out: { channel_id: number; unit_price_cny?: number; note?: string } = { channel_id }
        if (e.price !== undefined && e.price !== '') {
          const v = parseFloat(e.price)
          if (!Number.isNaN(v)) out.unit_price_cny = v
        }
        if (e.note !== undefined) out.note = e.note
        return out
      })
      .filter(p => p.unit_price_cny !== undefined || p.note !== undefined)
    if (payload.length === 0) {
      alert('没有改动')
      return
    }
    try {
      await api.saveKeyPricing(payload)
      await load()
    } catch (err) {
      alert('保存失败：' + (err as Error).message)
    }
  }

  const submitBulk = async () => {
    if (!bulkText.trim()) {
      alert('请粘贴 key 和单价（每行 "key 价格"）')
      return
    }
    setBulkSubmitting(true)
    try {
      const r = await api.bulkSaveKeyPricing(bulkText)
      setBulkResult(r)
      if (r.saved > 0) {
        await load()
        setBulkText('')
      }
    } catch (err) {
      alert('批量导入失败：' + (err as Error).message)
    } finally {
      setBulkSubmitting(false)
    }
  }

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
    </>
  )

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
          <h3 className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-3">Top Keys by Cost (¥)</h3>
          <ResponsiveContainer width="100%" height={200}>
            <BarChart data={keyChart} margin={{ top: 0, right: 0, bottom: 0, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="name" tick={{ fontSize: 9 }} />
              <YAxis tick={{ fontSize: 10 }} tickFormatter={v => '¥' + v} />
              <Tooltip formatter={(v: number) => ['¥' + v.toFixed(2), 'Cost']} />
              <Bar dataKey="cost" radius={[3,3,0,0]}>
                {keyChart.map((_, i) => (
                  <Cell key={i} fill={COLORS[i % COLORS.length]} />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>

      <div className="bg-white border border-gray-200 rounded-xl mb-4">
        <div className="flex items-center justify-between px-4 py-3 border-b border-gray-100">
          <div>
            <div className="text-sm font-semibold">按供应商（Tag）分组</div>
            <div className="text-[10px] text-gray-400 uppercase tracking-wider mt-0.5">每个上游供应商的用量与成本</div>
          </div>
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
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">占比</th>
              </tr>
            </thead>
            <tbody>
              {profit && [...profit.by_tag].sort((a, b) => b.cost_usd - a.cost_usd).map((t, i) => {
                const share = profit.cost_usd > 0 ? t.cost_usd / profit.cost_usd : 0
                return (
                  <tr key={i} className="hover:bg-gray-50 border-t border-gray-100">
                    <td className="px-3 py-1.5">
                      <span className={`px-1.5 py-0.5 rounded text-[10px] ${t.source === 'pipi' ? 'bg-violet-100 text-violet-700' : 'bg-blue-100 text-blue-700'}`}>{t.source}</span>
                    </td>
                    <td className="px-3 py-1.5 font-mono">{t.tag || <span className="text-gray-400">(无)</span>}</td>
                    <td className="px-3 py-1.5 text-right tabular-nums text-gray-500">{t.key_count}</td>
                    <td className="px-3 py-1.5 text-right tabular-nums">{t.used_usd.toFixed(2)}</td>
                    <td className="px-3 py-1.5 text-right tabular-nums text-rose-600">{t.cost_usd.toFixed(4)}</td>
                    <td className="px-3 py-1.5 text-right tabular-nums">{fmtPct(share)}</td>
                  </tr>
                )
              })}
              {(!profit || profit.by_tag.length === 0) && (
                <tr><td colSpan={6} className="px-3 py-3 text-center text-gray-400 text-[11px]">暂无数据</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

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

      <div className="bg-white border border-gray-200 rounded-xl mb-4">
        <div className="flex items-center justify-between px-4 py-3 border-b border-gray-100">
          <div>
            <div className="text-sm font-semibold">批量导入上游单价</div>
            <div className="text-[10px] text-gray-400 uppercase tracking-wider mt-0.5">每行 "key 价格"，按 channel.key 精确匹配</div>
          </div>
          <button onClick={submitBulk} disabled={bulkSubmitting} className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-50">
            {bulkSubmitting ? '导入中...' : '导入'}
          </button>
        </div>
        <div className="p-4 space-y-3">
          <textarea
            value={bulkText}
            onChange={ev => setBulkText(ev.target.value)}
            placeholder={`sk-ant-api03-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx    4.1\nsk-ant-api03-yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy    4.3\n# 以 # 开头为注释`}
            rows={8}
            className="w-full border border-gray-200 rounded-md px-3 py-2 font-mono text-[11px] resize-y focus:outline-none focus:border-gray-400"
          />
          {bulkResult && (
            <div className="text-xs bg-gray-50 border border-gray-200 rounded-md px-3 py-2 space-y-1">
              <div>
                <span className="text-emerald-600 font-medium">已保存 {bulkResult.saved}</span>
                {bulkResult.not_found.length > 0 && <span className="ml-3 text-amber-700">未匹配 {bulkResult.not_found.length}</span>}
                {bulkResult.errors.length > 0 && <span className="ml-3 text-rose-600">错误 {bulkResult.errors.length}</span>}
              </div>
              {bulkResult.not_found.length > 0 && (
                <details className="text-amber-700">
                  <summary className="cursor-pointer">未找到的 key</summary>
                  <ul className="ml-4 mt-1 font-mono text-[10px] space-y-0.5">
                    {bulkResult.not_found.map((k, i) => <li key={i}>{k}</li>)}
                  </ul>
                </details>
              )}
              {bulkResult.errors.length > 0 && (
                <details className="text-rose-600">
                  <summary className="cursor-pointer">解析错误</summary>
                  <ul className="ml-4 mt-1 text-[10px] space-y-0.5">
                    {bulkResult.errors.map((e, i) => <li key={i}>第 {e.line} 行：{e.reason}</li>)}
                  </ul>
                </details>
              )}
            </div>
          )}
        </div>
      </div>

      <div className="bg-white border border-gray-200 rounded-xl mb-4">
        <div className="flex items-center justify-between px-4 py-3 border-b border-gray-100 gap-3 flex-wrap">
          <div>
            <div className="text-sm font-semibold">上游单价配置</div>
            <div className="text-[10px] text-gray-400 uppercase tracking-wider mt-0.5">Per-Key CNY / USD of usage</div>
          </div>
          <div className="flex items-center gap-3">
            <label className="flex items-center gap-1.5 text-xs text-gray-600">
              <input
                type="checkbox"
                checked={keyOnlyUnpriced}
                onChange={ev => setKeyOnlyUnpriced(ev.target.checked)}
                className="rounded border-gray-300"
              />
              仅显示未配价（{unpricedKeyCount}）
            </label>
            <span className="text-[10px] text-gray-400 tabular-nums">{filteredKeys.length}/{keys.length}</span>
            <button onClick={submitKeyPricing} className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85">保存改动</button>
          </div>
        </div>
        <div className="overflow-x-auto max-h-[400px] overflow-y-auto">
          <table className="w-full text-xs whitespace-nowrap">
            <thead className="sticky top-0 bg-gray-50">
              <tr>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">ID</th>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">名称</th>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">Key</th>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">Tag</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">已用 USD</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">额度 USD</th>
                <th className="px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-gray-400">单价 CNY</th>
                <th className="px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400">备注</th>
              </tr>
            </thead>
            <tbody>
              {filteredKeys.map(k => {
                const e = keyEdits[k.id] || {}
                const priceVal = e.price !== undefined ? e.price : (k.unit_price_cny != null ? String(k.unit_price_cny) : '')
                const noteVal = e.note !== undefined ? e.note : k.note
                const isMissing = missingChIDs.has(k.id)
                return (
                  <tr key={k.id} className={`border-t border-gray-100 hover:bg-gray-50 ${isMissing ? 'bg-amber-50/50' : ''}`}>
                    <td className="px-3 py-1.5 font-mono text-gray-500">{k.id}</td>
                    <td className="px-3 py-1.5">{k.name}</td>
                    <td className="px-3 py-1.5 font-mono text-[10px] text-gray-500">{k.key}</td>
                    <td className="px-3 py-1.5">
                      {k.tag && <span className="px-1.5 py-0.5 rounded bg-gray-100 text-gray-700 text-[10px]">{k.tag}</span>}
                    </td>
                    <td className="px-3 py-1.5 text-right tabular-nums">{k.used_usd.toFixed(2)}</td>
                    <td className="px-3 py-1.5 text-right tabular-nums">{k.quota_usd != null ? k.quota_usd.toFixed(2) : '-'}</td>
                    <td className="px-3 py-1.5 text-right">
                      <input
                        type="number"
                        step="0.01"
                        value={priceVal}
                        onChange={ev => setKeyEdits(prev => ({ ...prev, [k.id]: { ...prev[k.id], price: ev.target.value } }))}
                        placeholder={isMissing ? '缺' : '4.30'}
                        className={`w-20 border rounded px-1.5 py-0.5 text-right text-xs ${isMissing ? 'border-amber-300 bg-white' : 'border-gray-200'}`}
                      />
                    </td>
                    <td className="px-3 py-1.5">
                      <input
                        type="text"
                        value={noteVal}
                        onChange={ev => setKeyEdits(prev => ({ ...prev, [k.id]: { ...prev[k.id], note: ev.target.value } }))}
                        className="w-40 border border-gray-200 rounded px-1.5 py-0.5 text-xs"
                      />
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
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
    </Layout>
  )
}
