import { useState, useEffect, useCallback, useMemo, Fragment } from 'react'
import Layout from '../components/Layout'
import SummaryCards from '../components/SummaryCards'
import BatchCreatePanel from '../components/BatchCreatePanel'
import LocalPoolPanel from '../components/LocalPoolPanel'
import BalanceAlertPanel from '../components/BalanceAlertPanel'
import { api, ChannelRow, ROLE_PROJECT_ADMIN, ROLE_SUPER_ADMIN } from '../api'
import { toast, Modal } from '../components/feedback'
import { ProviderMark } from '../components/ProviderMark'
import { getCachedRole, loadRole } from '../App'

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

export default function KeyCapacity() {
  const [channels, setChannels] = useState<ChannelRow[]>([])
  const [totalLastHour, setTotalLastHour] = useState(0)
  const [refreshedAt, setRefreshedAt] = useState('')
  // Tab switcher between the classic capacity/batch-create view, the
  // "Pool 上 Key" panel and the per-group Lark alert config. Storing in
  // state (not URL) is fine — all views are cheap to unmount / remount.
  const [tab, setTab] = useState<'capacity' | 'pool' | 'alerts'>('capacity')
  // Project admin (role=7) can only see the classic capacity view — the
  // Pool 上 Key panel calls super-admin-scoped local-pool endpoints and
  // the alert panel calls admin-scoped /api/notify/*, both 403 for them.
  const [role, setRole] = useState<number | null>(getCachedRole())
  useEffect(() => {
    if (getCachedRole() !== null) return
    void loadRole().then(setRole)
  }, [])
  const isProjectAdmin = role === ROLE_PROJECT_ADMIN

  // 批量改优先级状态：勾选的 channel.id 集合 + 目标优先级值
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [bulkPriority, setBulkPriority] = useState('')
  const [bulkBusy, setBulkBusy] = useState(false)
  const [bulkMsg, setBulkMsg] = useState<string | null>(null)

  // 批量创建渠道弹窗开关。面板自带卡片外壳，弹窗只提供遮罩 + 关闭。
  const [createOpen, setCreateOpen] = useState(false)
  // 已折叠的分组名集合（默认全部展开）。
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())

  const load = useCallback(async () => {
    try {
      const res = await api.getKeysData()
      setChannels(res.channels)
      setTotalLastHour(res.total_last_hour)
      setRefreshedAt(new Date().toLocaleTimeString('zh-CN'))
    } catch (err) { console.error(err); toast.error(err) }
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

  // 按 channels."group" 分组展示。同一渠道可能服务多个逗号分隔的 group，
  // 这里以整串原值作为分组键（与后台一致），不再二次拆分。未设置 group 的
  // 归入占位组。分组按名称排序，组内保留原有顺序。
  const groups = useMemo(() => {
    const m = new Map<string, ChannelRow[]>()
    for (const ch of channels) {
      const g = ch.group?.trim() || '（未分组）'
      const arr = m.get(g)
      if (arr) arr.push(ch)
      else m.set(g, [ch])
    }
    return Array.from(m.entries()).sort((a, b) => a[0].localeCompare(b[0], 'zh-CN'))
  }, [channels])

  const toggleGroupCollapse = (name: string) => {
    setCollapsed(prev => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  const toggleGroupSelect = (rows: ChannelRow[], checked: boolean) => {
    setSelected(prev => {
      const next = new Set(prev)
      for (const ch of rows) {
        if (checked) next.add(ch.id)
        else next.delete(ch.id)
      }
      return next
    })
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

  let totalUsed = 0, totalQuota = 0, totalRemaining = 0
  channels.forEach(ch => {
    totalUsed += ch.used_usd
    if (ch.quota_usd != null) { totalQuota += ch.quota_usd; totalRemaining += Math.max(0, ch.quota_usd - ch.used_usd) }
  })
  const totalETA = totalLastHour > 0 && totalQuota > 0 ? totalRemaining / totalLastHour : totalRemaining > 0 ? Infinity : null
  const etaFmt = fmtETA(totalETA)

  const actions = (
    <div className="flex items-center gap-2">
      <button
        onClick={() => setCreateOpen(true)}
        className="inline-flex items-center gap-1.5 rounded-md bg-emerald-600 text-white px-3 py-1.5 text-xs font-medium hover:opacity-85"
      >
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round">
          <line x1="12" y1="5" x2="12" y2="19" />
          <line x1="5" y1="12" x2="19" y2="12" />
        </svg>
        批量创建渠道
      </button>
      <button onClick={load} className="bg-brand text-white rounded-md px-3 py-1.5 text-xs hover:bg-brand-700">
        刷新数据
      </button>
    </div>
  )

  const tabDefs = isProjectAdmin
    ? [{ id: 'capacity' as const, label: '额度与批量创建' }]
    : [
        { id: 'capacity' as const, label: '额度与批量创建' },
        { id: 'pool' as const, label: 'Pool 上 Key（本地）' },
        { id: 'alerts' as const, label: '余额报警（分组）' },
      ]

  const tabBar = (
    <div className="flex items-center gap-1 border-b border-gray-200 mb-4">
      {tabDefs.map(t => (
        <button
          key={t.id}
          onClick={() => setTab(t.id)}
          className={`px-3 py-2 text-sm border-b-2 -mb-px transition-colors ${
            tab === t.id
              ? 'border-gray-900 text-gray-900 font-medium'
              : 'border-transparent text-gray-500 hover:text-gray-800'
          }`}
        >
          {t.label}
        </button>
      ))}
    </div>
  )

  return (
    <Layout
      title="Key Capacity"
      subtitle={`每个 Key 的用量与剩余寿命估算${refreshedAt ? ` · 最后更新：${refreshedAt}` : ''}`}
      actions={tab === 'capacity' ? actions : undefined}
    >
      {tabBar}
      {tab === 'pool' && <LocalPoolPanel configEditable={role !== null && role >= ROLE_SUPER_ADMIN} />}
      {tab === 'alerts' && <BalanceAlertPanel />}
      {tab === 'capacity' && (<>
      <SummaryCards cards={[
        { label: '启用 Key 数', value: String(channels.length), color: 'text-blue-600' },
        { label: '总额度', value: totalQuota ? '$' + totalQuota.toFixed(2) : '未配置' },
        { label: '总已用', value: '$' + totalUsed.toFixed(2), color: 'text-rose-600' },
        { label: '总剩余', value: totalQuota ? '$' + totalRemaining.toFixed(2) : '—', color: totalRemaining < totalQuota * 0.2 ? 'text-amber-600' : 'text-emerald-600' },
        { label: '最近1小时消耗', value: totalLastHour > 0 ? '$' + totalLastHour.toFixed(4) : '$0', color: 'text-gray-500' },
        { label: '预计剩余时长', value: etaFmt.text, color: etaFmt.cls },
      ]} />

      <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
        {/* 批量改优先级工具栏 */}
        <div className="flex items-center justify-between px-4 py-2.5 border-b border-gray-100 gap-3 flex-wrap">
          <div className="flex items-center gap-2 text-xs text-gray-500">
            <span>已选 <span className="tabular-nums font-medium text-gray-900">{selected.size}</span> / {channels.length}</span>
            {selected.size > 0 && (
              <button onClick={() => setSelected(new Set())} className="text-gray-400 hover:text-gray-700">清空</button>
            )}
            <span className="text-gray-300">·</span>
            <span>{groups.length} 个分组</span>
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
              className="bg-brand text-white rounded-md px-3 py-1.5 text-xs hover:bg-brand-700 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {bulkBusy ? '应用中…' : '设置选中优先级'}
            </button>
          </div>
        </div>
        {bulkMsg && (
          <div className={`px-4 py-1.5 text-[11px] border-b border-gray-100 ${bulkMsg.startsWith('已更新') ? 'text-emerald-600 bg-emerald-50/40' : 'text-rose-600 bg-rose-50/40'}`}>{bulkMsg}</div>
        )}

        <div className="overflow-x-auto max-h-[74vh] overflow-y-auto">
          <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
            <thead>
              <tr>
                <th className="sticky top-0 z-10 bg-gray-50 px-3 py-2 text-left border-b border-gray-200 w-8">
                  <input
                    type="checkbox"
                    checked={channels.length > 0 && selected.size === channels.length}
                    ref={el => { if (el) el.indeterminate = selected.size > 0 && selected.size < channels.length }}
                    onChange={e => toggleAll(e.target.checked)}
                    className="rounded border-gray-300"
                  />
                </th>
                {['ID','名称','Key 末尾','优先级','已用 ($)','额度 ($)','剩余 ($)','剩余%','最近1小时消耗 ($)','预计剩余时长'].map(h => (
                  <th key={h} className="sticky top-0 z-10 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {groups.map(([groupName, rows]) => {
                const isCollapsed = collapsed.has(groupName)
                const selCount = rows.reduce((n, ch) => n + (selected.has(ch.id) ? 1 : 0), 0)
                const allSel = selCount === rows.length
                const someSel = selCount > 0 && !allSel
                let gUsed = 0, gQuota = 0, gRemaining = 0, gLastHour = 0, gQuotaSet = false
                rows.forEach(ch => {
                  gUsed += ch.used_usd
                  gLastHour += ch.last_hour_usd
                  if (ch.quota_usd != null) { gQuotaSet = true; gQuota += ch.quota_usd; gRemaining += Math.max(0, ch.quota_usd - ch.used_usd) }
                })
                return (
                  <Fragment key={groupName}>
                    {/* 分组标题行：勾选整组 / 折叠 / 组内小计 */}
                    <tr className="bg-gray-100/80">
                      <td className="px-3 py-1.5 border-b border-gray-200 w-8">
                        <input
                          type="checkbox"
                          checked={allSel}
                          ref={el => { if (el) el.indeterminate = someSel }}
                          onChange={e => toggleGroupSelect(rows, e.target.checked)}
                          className="rounded border-gray-300"
                        />
                      </td>
                      <td colSpan={10} className="px-3 py-1.5 border-b border-gray-200">
                        <div className="flex items-center gap-2 flex-wrap">
                          <button
                            onClick={() => toggleGroupCollapse(groupName)}
                            className="inline-flex items-center gap-1.5 text-gray-800 font-semibold hover:text-gray-900"
                          >
                            <span className="text-gray-400 text-[10px] w-3 inline-block">{isCollapsed ? '▶' : '▼'}</span>
                            <span className="font-mono">{groupName}</span>
                            <span className="text-gray-400 font-normal">· {rows.length} keys</span>
                          </button>
                          <span className="text-[11px] text-gray-500 tabular-nums">
                            已用 <span className="text-rose-600">${gUsed.toFixed(2)}</span>
                            {gQuotaSet && <> / 额度 ${gQuota.toFixed(2)} · 剩 <span className="text-emerald-600">${gRemaining.toFixed(2)}</span></>}
                            {gLastHour > 0 && <> · 近1h ${gLastHour.toFixed(4)}</>}
                          </span>
                        </div>
                      </td>
                    </tr>
                    {!isCollapsed && rows.map(ch => {
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
                          <td className="px-3 py-1.5 border-b border-gray-50">
                            <span className="inline-flex items-center gap-2">
                              <ProviderMark type={ch.type} size={18} />
                              <span>{ch.name}</span>
                            </span>
                          </td>
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
                  </Fragment>
                )
              })}
            </tbody>
          </table>
        </div>
      </div>
      </>)}

      <Modal open={createOpen} onClose={() => setCreateOpen(false)}>
        <BatchCreatePanel onCreated={load} />
      </Modal>
    </Layout>
  )
}
