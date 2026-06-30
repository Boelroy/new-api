import { useState, useEffect, useCallback } from 'react'
import Layout from '../components/Layout'
import SummaryCards from '../components/SummaryCards'
import { api, ChannelRow } from '../api'

function fmtETA(hours: number | null): { text: string; cls: string } {
  if (hours === null) return { text: '—', cls: 'text-gray-400' }
  if (hours === Infinity) return { text: '无限', cls: 'text-emerald-600' }
  if (hours < 0) return { text: '已超额', cls: 'text-rose-600' }
  const cls = hours > 48 ? 'text-emerald-600' : hours > 12 ? 'text-amber-600' : 'text-rose-600'
  if (hours >= 24 * 30) return { text: '>30天', cls }
  if (hours >= 24) return { text: `${Math.floor(hours / 24)}天${Math.floor(hours % 24)}小时`, cls }
  return { text: `${hours.toFixed(1)}小时`, cls }
}

function ProgressBar({ pct }: { pct: number }) {
  const color = pct > 20 ? 'bg-emerald-500' : pct > 5 ? 'bg-amber-500' : 'bg-rose-500'
  return (
    <div className="flex items-center gap-2">
      <span className="w-12 text-right tabular-nums">{pct.toFixed(1)}%</span>
      <div className="w-20 h-1.5 bg-gray-200 rounded-full overflow-hidden">
        <div className={`h-full ${color} rounded-full`} style={{ width: `${Math.min(100, Math.max(0, pct))}%` }} />
      </div>
    </div>
  )
}

function BatchCreatePanel({ onCreated }: { onCreated: () => void }) {
  const [studio, setStudio] = useState('')
  const [studioMode, setStudioMode] = useState<'pick' | 'new'>('pick')
  const [suffix, setSuffix] = useState('')
  const [costInput, setCostInput] = useState('')          // per-key 上游单价 (CNY)
  const [priorityInput, setPriorityInput] = useState('')  // channels.priority
  const [input, setInput] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [result, setResult] = useState<string | null>(null)
  const [studios, setStudios] = useState<string[]>([])

  useEffect(() => {
    void (async () => {
      try {
        const res = await api.listStudios()
        setStudios(res.studios)
        // Pre-select the first existing studio, or pipi if it exists, so
        // the operator doesn't have to click before the form is usable.
        setStudio(prev => {
          if (prev) return prev
          if (res.studios.includes('pipi')) return 'pipi'
          return res.studios[0] ?? ''
        })
      } catch { /* leave dropdown empty on error */ }
    })()
  }, [])

  const handleSubmit = async () => {
    setResult(null)
    if (!studio.trim()) {
      setResult('请填写工作室名')
      return
    }
    if (!suffix.trim()) {
      setResult('请填写后缀')
      return
    }
    const channels: { key: string; quota_usd: number }[] = []
    input.split('\n').forEach(line => {
      const t = line.trim()
      if (!t || t.startsWith('#')) return
      const parts = t.split(/[\s,]+/)
      if (parts.length < 2) return
      const q = parseFloat(parts[1])
      if (!parts[0] || isNaN(q) || q <= 0) return
      channels.push({ key: parts[0], quota_usd: q })
    })
    if (channels.length === 0) {
      setResult('未解析到有效行')
      return
    }
    const defaults: { priority?: number; unit_price_cny?: number } = {}
    if (costInput.trim()) {
      const c = parseFloat(costInput.trim())
      if (!isNaN(c) && c > 0) defaults.unit_price_cny = c
    }
    if (priorityInput.trim()) {
      const p = parseInt(priorityInput.trim(), 10)
      if (!isNaN(p) && p > 0) defaults.priority = p
    }
    setSubmitting(true)
    try {
      const res = await api.batchCreateChannels(studio.trim(), suffix.trim(), channels, defaults)
      setResult(`成功创建 ${res.count} 个渠道`)
      setInput('')
      onCreated()
    } catch (e: any) {
      setResult(`失败: ${e.message || e}`)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="bg-white border border-gray-200 rounded-xl p-4">
      <h2 className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-3">批量创建渠道</h2>
      <div className="grid grid-cols-2 gap-2 mb-2">
        <div>
          <label className="block text-[11px] text-gray-500 mb-1">工作室</label>
          {studioMode === 'pick' ? (
            <select
              value={studios.includes(studio) ? studio : ''}
              onChange={e => {
                if (e.target.value === '__NEW__') {
                  setStudioMode('new')
                  setStudio('')
                } else {
                  setStudio(e.target.value)
                }
              }}
              className="w-full border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
            >
              {studios.length === 0 && <option value="">（无现有工作室）</option>}
              {studios.map(s => <option key={s} value={s}>{s}</option>)}
              <option value="__NEW__">+ 新建工作室…</option>
            </select>
          ) : (
            <div className="flex gap-1">
              <input
                value={studio}
                onChange={e => setStudio(e.target.value)}
                placeholder="新工作室名称"
                autoFocus
                className="flex-1 border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
              />
              <button
                type="button"
                onClick={() => { setStudioMode('pick'); setStudio(studios[0] ?? '') }}
                className="border border-gray-200 rounded-md px-2 text-xs text-gray-500 hover:bg-gray-50"
                title="返回选择"
              >×</button>
            </div>
          )}
        </div>
        <div>
          <label className="block text-[11px] text-gray-500 mb-1">后缀</label>
          <input
            value={suffix}
            onChange={e => setSuffix(e.target.value)}
            placeholder="例如 a / 5"
            className="w-full border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
          />
        </div>
      </div>
      <div className="grid grid-cols-2 gap-2 mb-2">
        <div>
          <label className="block text-[11px] text-gray-500 mb-1">默认成本 (CNY/USD 上游单价)</label>
          <input
            type="number"
            step="0.01"
            min="0"
            value={costInput}
            onChange={e => setCostInput(e.target.value)}
            placeholder="例如 4.3，空=不写"
            className="w-full border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
          />
        </div>
        <div>
          <label className="block text-[11px] text-gray-500 mb-1">默认优先级</label>
          <input
            type="number"
            step="1"
            min="1"
            value={priorityInput}
            onChange={e => setPriorityInput(e.target.value)}
            placeholder="例如 2，空=默认 1001"
            className="w-full border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
          />
        </div>
      </div>
      <textarea
        value={input}
        onChange={e => setInput(e.target.value)}
        rows={8}
        placeholder={'每行: key 额度（USD）\n\nsk-ant-api03-xxxx 220\nsk-ant-api03-yyyy 500'}
        className="w-full border border-gray-200 rounded-md p-2.5 text-xs font-mono resize-y bg-gray-50 focus:outline-none focus:border-gray-900"
      />
      <p className="text-[10px] text-gray-400 mt-2 leading-relaxed">
        命名 MMDD-工作室-后缀-容量；上方"默认成本/优先级"会写到所有新建渠道；channels.tag 用作 user 角色可见范围
        {studios.length > 0 && <>。已有：{studios.join('、')}</>}
      </p>
      <button
        onClick={handleSubmit}
        disabled={submitting}
        className="mt-3 w-full bg-emerald-600 text-white rounded-md py-1.5 text-sm font-medium hover:opacity-85 disabled:opacity-50"
      >
        {submitting ? '创建中...' : '批量创建'}
      </button>
      {result && (
        <p className={`text-[11px] mt-2 ${result.startsWith('成功') ? 'text-emerald-600' : 'text-rose-600'}`}>{result}</p>
      )}
    </div>
  )
}

export default function KeyCapacity() {
  const [channels, setChannels] = useState<ChannelRow[]>([])
  const [totalLastHour, setTotalLastHour] = useState(0)
  const [quotaInput, setQuotaInput] = useState('')
  const [refreshedAt, setRefreshedAt] = useState('')
  const [saving, setSaving] = useState(false)

  // 批量改优先级状态：勾选的 channel.id 集合 + 目标优先级值
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [bulkPriority, setBulkPriority] = useState('')
  const [bulkBusy, setBulkBusy] = useState(false)
  const [bulkMsg, setBulkMsg] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const res = await api.getKeysData()
      setChannels(res.channels)
      setTotalLastHour(res.total_last_hour)
      setRefreshedAt(new Date().toLocaleTimeString('zh-CN'))
    } catch (err) { console.error(err) }
  }, [])

  useEffect(() => {
    load()
    const t = setInterval(load, 60000)
    return () => clearInterval(t)
  }, [load])

  const toggleRow = (id: number) => {
    setSelected(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const toggleAll = (checked: boolean) => {
    setSelected(checked ? new Set(channels.map(c => c.id)) : new Set())
  }

  const handleBulkPriority = async () => {
    setBulkMsg(null)
    const p = parseInt(bulkPriority.trim(), 10)
    if (isNaN(p) || p <= 0) { setBulkMsg('优先级必须是正整数'); return }
    if (selected.size === 0) { setBulkMsg('请勾选至少一条渠道'); return }
    setBulkBusy(true)
    try {
      const ids = Array.from(selected)
      const res = await api.batchUpdateChannelPriority(ids, p)
      setBulkMsg(`已更新 ${res.updated} 条渠道优先级为 ${res.priority}`)
      setSelected(new Set())
      setBulkPriority('')
      await load()
    } catch (e: any) {
      setBulkMsg('失败: ' + (e?.message || e))
    } finally {
      setBulkBusy(false)
    }
  }

  const handleApply = async () => {
    const map: Record<string, number> = {}
    quotaInput.split('\n').forEach(line => {
      line = line.trim()
      if (!line || line.startsWith('#')) return
      const parts = line.split(/[\s,]+/)
      if (parts.length >= 2) {
        const q = parseFloat(parts[1])
        if (!isNaN(q)) map[parts[0]] = q
      }
    })
    const payload = Object.entries(map).map(([key, quota_usd]) => ({ key, quota_usd }))
    if (!payload.length) return
    setSaving(true)
    try {
      await api.saveQuotas(payload)
      await load()
    } finally { setSaving(false) }
  }

  let totalUsed = 0, totalQuota = 0, totalRemaining = 0
  channels.forEach(ch => {
    totalUsed += ch.used_usd
    if (ch.quota_usd != null) { totalQuota += ch.quota_usd; totalRemaining += Math.max(0, ch.quota_usd - ch.used_usd) }
  })
  const totalETA = totalLastHour > 0 && totalQuota > 0 ? totalRemaining / totalLastHour : totalRemaining > 0 ? Infinity : null
  const etaFmt = fmtETA(totalETA)

  const actions = (
    <button onClick={load} className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85">
      刷新数据
    </button>
  )

  return (
    <Layout
      title="Key Capacity"
      subtitle={`每个 Key 的用量与剩余寿命估算${refreshedAt ? ` · 最后更新：${refreshedAt}` : ''}`}
      actions={actions}
    >
      <SummaryCards cards={[
        { label: '启用 Key 数', value: String(channels.length), color: 'text-blue-600' },
        { label: '总额度', value: totalQuota ? '$' + totalQuota.toFixed(2) : '未配置' },
        { label: '总已用', value: '$' + totalUsed.toFixed(2), color: 'text-rose-600' },
        { label: '总剩余', value: totalQuota ? '$' + totalRemaining.toFixed(2) : '—', color: totalRemaining < totalQuota * 0.2 ? 'text-amber-600' : 'text-emerald-600' },
        { label: '最近1小时消耗', value: totalLastHour > 0 ? '$' + totalLastHour.toFixed(4) : '$0', color: 'text-gray-500' },
        { label: '预计剩余时长', value: etaFmt.text, color: etaFmt.cls },
      ]} />

      <div className="grid grid-cols-1 lg:grid-cols-[300px_1fr] gap-6 items-start">
        <div className="space-y-4">
          <div className="bg-white border border-gray-200 rounded-xl p-4">
            <h2 className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-3">Key 额度配置</h2>
            <textarea
              value={quotaInput}
              onChange={e => setQuotaInput(e.target.value)}
              rows={10}
              placeholder={'每行一个 Key 及其额度（USD）：\n\nsk-ant-api03-xxxx    150\nsk-ant-api03-yyyy    200\n\n# 井号开头为注释'}
              className="w-full border border-gray-200 rounded-md p-2.5 text-xs font-mono resize-y bg-gray-50 focus:outline-none focus:border-gray-900"
            />
            <p className="text-[10px] text-gray-400 mt-2 leading-relaxed">空格/Tab/逗号分隔，# 行为注释</p>
            <button onClick={handleApply} disabled={saving}
              className="mt-3 w-full bg-gray-900 text-white rounded-md py-1.5 text-sm font-medium hover:opacity-85 disabled:opacity-50">
              {saving ? '保存中...' : '应用并保存'}
            </button>
          </div>

          <BatchCreatePanel onCreated={load} />
        </div>

        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          {/* 批量改优先级工具栏 */}
          <div className="flex items-center justify-between px-4 py-2.5 border-b border-gray-100 gap-3 flex-wrap">
            <div className="flex items-center gap-2 text-xs text-gray-500">
              <span>已选 <span className="tabular-nums font-medium text-gray-900">{selected.size}</span> / {channels.length}</span>
              {selected.size > 0 && (
                <button onClick={() => setSelected(new Set())} className="text-gray-400 hover:text-gray-700">清空</button>
              )}
            </div>
            <div className="flex items-center gap-2">
              <input
                type="number"
                step="1"
                min="1"
                value={bulkPriority}
                onChange={e => setBulkPriority(e.target.value)}
                placeholder="优先级 (例如 2)"
                className="w-32 border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-white focus:outline-none focus:border-gray-900"
              />
              <button
                onClick={handleBulkPriority}
                disabled={bulkBusy || selected.size === 0}
                className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-40 disabled:cursor-not-allowed"
              >
                {bulkBusy ? '应用中…' : '设置选中优先级'}
              </button>
            </div>
          </div>
          {bulkMsg && (
            <div className={`px-4 py-1.5 text-[11px] border-b border-gray-100 ${bulkMsg.startsWith('已更新') ? 'text-emerald-600 bg-emerald-50/40' : 'text-rose-600 bg-rose-50/40'}`}>{bulkMsg}</div>
          )}

          <div className="overflow-x-auto max-h-[70vh] overflow-y-auto">
            <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
              <thead>
                <tr>
                  <th className="sticky top-0 bg-gray-50 px-3 py-2 text-left border-b border-gray-200 w-8">
                    <input
                      type="checkbox"
                      checked={channels.length > 0 && selected.size === channels.length}
                      ref={el => { if (el) el.indeterminate = selected.size > 0 && selected.size < channels.length }}
                      onChange={e => toggleAll(e.target.checked)}
                      className="rounded border-gray-300"
                    />
                  </th>
                  {['ID','名称','Key 末尾','优先级','已用 ($)','额度 ($)','剩余 ($)','剩余%','最近1小时消耗 ($)','预计剩余时长'].map(h => (
                    <th key={h} className="sticky top-0 bg-gray-50 px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {channels.map(ch => {
                  const quota = ch.quota_usd
                  const remaining = quota != null ? quota - ch.used_usd : null
                  const pct = quota && quota > 0 ? (remaining! / quota) * 100 : null
                  const eta = remaining != null && ch.last_hour_usd > 0 ? remaining / ch.last_hour_usd : remaining != null && remaining > 0 ? Infinity : null
                  const etaF = fmtETA(eta)
                  const isSelected = selected.has(ch.id)
                  return (
                    <tr key={ch.id} className={isSelected ? 'bg-blue-50/40 hover:bg-blue-50' : 'hover:bg-gray-50'}>
                      <td className="px-3 py-1.5 border-b border-gray-50 w-8">
                        <input
                          type="checkbox"
                          checked={isSelected}
                          onChange={() => toggleRow(ch.id)}
                          className="rounded border-gray-300"
                        />
                      </td>
                      <td className="px-3 py-1.5 border-b border-gray-50">{ch.id}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50">{ch.name}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50 font-mono text-gray-400">{ch.key}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{ch.priority || <span className="text-gray-300">—</span>}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">${ch.used_usd.toFixed(4)}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{quota != null ? '$' + quota.toFixed(2) : <span className="text-gray-300">未设置</span>}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{remaining != null ? '$' + remaining.toFixed(4) : '—'}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50">{pct != null ? <ProgressBar pct={pct} /> : <span className="text-gray-300">—</span>}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{ch.last_hour_usd > 0 ? '$' + ch.last_hour_usd.toFixed(4) : <span className="text-gray-300">0</span>}</td>
                      <td className={`px-3 py-1.5 border-b border-gray-50 font-medium ${etaF.cls}`}>{etaF.text}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </Layout>
  )
}
