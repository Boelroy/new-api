import { useState, useEffect, useMemo } from 'react'
import Layout from '../components/Layout'
import SummaryCards from '../components/SummaryCards'
import BatchCreatePanel from '../components/BatchCreatePanel'
import { api, ChannelRow, ROLE_ADMIN, ROLE_STUDIO_OPERATOR } from '../api'

const STATUS_LABEL: Record<number, string> = { 1: '启用', 2: '手动禁用', 3: '自动禁用' }
const STATUS_CLS: Record<number, string> = {
  1: 'bg-emerald-100 text-emerald-800',
  2: 'bg-red-100 text-red-700',
  3: 'bg-amber-100 text-amber-700',
}

function today() { return new Date().toISOString().slice(0, 10) }

function exportCSV(rows: ChannelRow[], start: string, end: string) {
  const header = ['ID','名称','Key末尾','状态','单价 CNY','总已用($)','额度($)','总剩余($)']
  const csvRows = rows.map(r => {
    const quota = r.quota_usd
    const remaining = quota != null ? (quota - r.used_usd).toFixed(4) : ''
    return [
      r.id, r.name, r.key,
      STATUS_LABEL[r.status] ?? r.status,
      r.unit_price_cny != null ? r.unit_price_cny.toFixed(4) : '',
      r.used_usd.toFixed(4),
      quota != null ? quota.toFixed(2) : '',
      remaining,
    ]
  })
  const csv = [header, ...csvRows].map(r => r.join(',')).join('\n')
  const a = document.createElement('a')
  a.href = 'data:text/csv;charset=utf-8,﻿' + encodeURIComponent(csv)
  a.download = `allkeys${start ? '_' + start : ''}${end ? '_' + end : ''}.csv`
  a.click()
}

export default function AllKeys() {
  const [rows, setRows] = useState<ChannelRow[]>([])
  const [start, setStart] = useState(today())
  const [end, setEnd] = useState(today())
  const [loading, setLoading] = useState(false)
  const [refreshedAt, setRefreshedAt] = useState('')

  // role gates the pricing-edit UI. /api/keys/pricing is admin-only on the
  // backend, so non-admins would just hit 403 — surface that by hiding the
  // controls entirely. studio scopes the Studio Operator's batch-create
  // panel to their bound studio.
  const [role, setRole] = useState<number | null>(null)
  const [studio, setStudio] = useState<string>('')
  const canEditPricing = role !== null && role >= ROLE_ADMIN
  const canBatchCreate = role !== null && (role >= ROLE_ADMIN || role === ROLE_STUDIO_OPERATOR)
  const isStudioOperator = role === ROLE_STUDIO_OPERATOR

  // Per-row inline price edits (channel_id -> raw input string).
  const [priceEdits, setPriceEdits] = useState<Record<number, string>>({})
  const [onlyUnpriced, setOnlyUnpriced] = useState(false)
  const [saving, setSaving] = useState(false)

  // Bulk import textarea state.
  const [bulkText, setBulkText] = useState('')
  const [bulkResult, setBulkResult] = useState<{ saved: number; not_found: string[]; errors: { line: number; reason: string }[] } | null>(null)
  const [bulkSubmitting, setBulkSubmitting] = useState(false)

  useEffect(() => {
    void (async () => {
      try {
        const me = await api.getAuthMe()
        setRole(me.role)
        setStudio(me.studio ?? '')
      } catch {
        setRole(0)
      }
    })()
  }, [])

  const load = async (s?: string, e?: string) => {
    setLoading(true)
    try {
      const data = await api.getAllKeys(s, e)
      setRows(data.sort((a, b) => a.id - b.id))
      setRefreshedAt(new Date().toLocaleTimeString('zh-CN'))
      setPriceEdits({})
    } catch (err) { console.error(err) }
    finally { setLoading(false) }
  }

  useEffect(() => { load(start, end) }, [])

  const handleQuery = () => load(start, end)
  const handleAll = () => { setStart(''); setEnd(''); load() }

  const summary = useMemo(() => {
    const totalUsed = rows.reduce((s, r) => s + r.used_usd, 0)
    const totalQuota = rows.reduce((s, r) => s + (r.quota_usd ?? 0), 0)
    const totalRemaining = rows.reduce((s, r) => r.quota_usd != null ? s + Math.max(0, r.quota_usd - r.used_usd) : s, 0)
    const unpriced = rows.filter(r => r.unit_price_cny == null).length
    return { count: rows.length, totalUsed, totalQuota, totalRemaining, unpriced }
  }, [rows])

  const filteredRows = useMemo(() => {
    if (!onlyUnpriced) return rows
    return rows.filter(r => r.unit_price_cny == null)
  }, [rows, onlyUnpriced])

  const submitPrices = async () => {
    const payload = Object.entries(priceEdits)
      .map(([id, v]) => {
        const channel_id = Number(id)
        if (v === '') return null
        const price = parseFloat(v)
        if (Number.isNaN(price)) return null
        return { channel_id, unit_price_cny: price }
      })
      .filter((p): p is { channel_id: number; unit_price_cny: number } => p !== null)
    if (payload.length === 0) {
      alert('没有改动')
      return
    }
    setSaving(true)
    try {
      const res = await api.saveKeyPricing(payload)
      await load(start, end)
      alert(`已保存 ${res.saved} 条`)
    } catch (err) {
      alert('保存失败：' + (err as Error).message)
    } finally {
      setSaving(false)
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
        await load(start, end)
        setBulkText('')
      }
    } catch (err) {
      alert('批量导入失败：' + (err as Error).message)
    } finally {
      setBulkSubmitting(false)
    }
  }

  const actions = (
    <>
      <input type="date" value={start} onChange={e => setStart(e.target.value)} className="border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-white" />
      <span className="text-gray-300 text-xs">→</span>
      <input type="date" value={end} onChange={e => setEnd(e.target.value)} className="border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-white" />
      <button onClick={handleQuery} disabled={loading} className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-50">
        {loading ? '加载中...' : '查询'}
      </button>
      <button onClick={handleAll} className="border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50">全部</button>
      <button onClick={() => exportCSV(rows, start, end)} className="border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50">导出 CSV</button>
    </>
  )

  return (
    <Layout
      title="All Keys"
      subtitle={`所有 Key 的总用量与容量（按创建时间筛选）${refreshedAt ? ` · 更新于 ${refreshedAt}` : ''}`}
      actions={actions}
    >
      <SummaryCards cards={[
        { label: 'Key 总数', value: String(summary.count), color: 'text-blue-600' },
        { label: '未配单价', value: String(summary.unpriced), color: 'text-amber-600' },
        { label: '总已用', value: '$' + summary.totalUsed.toFixed(2), color: 'text-rose-600' },
        { label: '总额度', value: summary.totalQuota ? '$' + summary.totalQuota.toFixed(2) : '未配置' },
        { label: '总剩余', value: summary.totalRemaining ? '$' + summary.totalRemaining.toFixed(2) : '—', color: 'text-emerald-600' },
      ]} />

      {canBatchCreate && (
        <div className="mb-4">
          <BatchCreatePanel
            onCreated={() => load(start, end)}
            lockedStudio={isStudioOperator ? studio : undefined}
            canConfigureModels={!isStudioOperator}
          />
        </div>
      )}

      {canEditPricing && (
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
            rows={6}
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
      )}

      <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
        <div className="flex items-center justify-between px-4 py-2.5 border-b border-gray-100 gap-3 flex-wrap">
          <div className="flex items-center gap-3 text-xs">
            <label className="flex items-center gap-1.5 text-gray-600">
              <input
                type="checkbox"
                checked={onlyUnpriced}
                onChange={ev => setOnlyUnpriced(ev.target.checked)}
                className="rounded border-gray-300"
              />
              仅显示未配单价（{summary.unpriced}）
            </label>
            <span className="text-[10px] text-gray-400 tabular-nums">{filteredRows.length}/{rows.length}</span>
          </div>
          {canEditPricing && (
            <button
              onClick={submitPrices}
              disabled={saving || Object.keys(priceEdits).length === 0}
              className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {saving ? '保存中…' : '保存价格改动'}
            </button>
          )}
        </div>
        <div className="overflow-x-auto max-h-[70vh] overflow-y-auto">
          <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
            <thead>
              <tr>
                {['ID','名称','Key 末尾','状态','单价 CNY','总已用 ($)','额度 ($)','总剩余 ($)','剩余%'].map(h => (
                  <th key={h} className="sticky top-0 bg-gray-50 px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {filteredRows.map(r => {
                const quota = r.quota_usd
                const remaining = quota != null ? quota - r.used_usd : null
                const pct = quota && quota > 0 ? (remaining! / quota) * 100 : null
                const barColor = pct != null ? (pct > 20 ? 'bg-emerald-500' : pct > 5 ? 'bg-amber-500' : 'bg-rose-500') : ''
                const priceVal = priceEdits[r.id] !== undefined
                  ? priceEdits[r.id]
                  : (r.unit_price_cny != null ? String(r.unit_price_cny) : '')
                const isMissingPrice = r.unit_price_cny == null
                return (
                  <tr key={r.id} className={`hover:bg-gray-50 ${isMissingPrice ? 'bg-amber-50/40' : ''}`}>
                    <td className="px-3 py-1.5 border-b border-gray-50">{r.id}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50">{r.name}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 font-mono text-gray-400">{r.key}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50">
                      <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold ${STATUS_CLS[r.status] ?? 'bg-gray-100 text-gray-600'}`}>
                        {STATUS_LABEL[r.status] ?? r.status}
                      </span>
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-right">
                      {canEditPricing ? (
                        <input
                          type="number"
                          step="0.01"
                          value={priceVal}
                          onChange={ev => setPriceEdits(prev => ({ ...prev, [r.id]: ev.target.value }))}
                          placeholder={isMissingPrice ? '缺' : '4.30'}
                          className={`w-20 border rounded px-1.5 py-0.5 text-right text-xs tabular-nums ${isMissingPrice ? 'border-amber-300 bg-white' : 'border-gray-200'}`}
                        />
                      ) : (
                        <span className={`tabular-nums ${isMissingPrice ? 'text-gray-300' : ''}`}>
                          {isMissingPrice ? '—' : Number(priceVal).toFixed(2)}
                        </span>
                      )}
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">${r.used_usd.toFixed(4)}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{quota != null ? '$' + quota.toFixed(2) : <span className="text-gray-300">未设置</span>}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{remaining != null ? '$' + remaining.toFixed(4) : '—'}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50">
                      {pct != null ? (
                        <div className="flex items-center gap-2">
                          <span className="w-12 text-right tabular-nums">{pct.toFixed(1)}%</span>
                          <div className="w-20 h-1.5 bg-gray-200 rounded-full overflow-hidden">
                            <div className={`h-full ${barColor} rounded-full`} style={{ width: `${Math.min(100, Math.max(0, pct))}%` }} />
                          </div>
                        </div>
                      ) : <span className="text-gray-300">—</span>}
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
