import { useCallback, useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import {
  api,
  type RemoteChannel,
  type RemoteChannelCreateResult,
  type RemoteProfile,
} from '../api'

const STATUS_LABEL: Record<number, string> = {
  1: '启用',
  2: '手动禁用',
  3: '自动禁用',
}
const STATUS_CLS: Record<number, string> = {
  1: 'bg-emerald-100 text-emerald-800',
  2: 'bg-red-100 text-red-700',
  3: 'bg-amber-100 text-amber-700',
}

const DEFAULT_ANTHROPIC_MODELS = [
  'claude-opus-4-7',
  'claude-sonnet-4-6',
  'claude-opus-4-6',
  'claude-haiku-4-5-20251001',
  'claude-sonnet-4-5-20250929',
  'claude-opus-4-5-20251101',
  'claude-opus-4-8',
  'claude-fable-5',
].join(',')

const DEFAULT_TEST_MODEL = 'claude-haiku-4-5-20251001'

// FragmentRow is just <>{children}</> — used so `{channels.map(...)}` can
// emit two adjacent <tr> elements (main row + expandable sparkline) and
// still key on the channel id at the outermost node.
function FragmentRow({ children }: { children: React.ReactNode }) {
  return <>{children}</>
}

// Sparkline: minimal SVG line chart for cumulative used_quota. We normalise
// the domain to [min, max] of the visible window so idle channels still
// show a flat readable line instead of collapsing to a single pixel.
function Sparkline({ points }: { points: { t: number; q: number }[] }) {
  if (points.length < 2) return null
  const w = 640, h = 60, padX = 4, padY = 6
  const tMin = points[0].t
  const tMax = points[points.length - 1].t
  const tRange = Math.max(1, tMax - tMin)
  const qMin = Math.min(...points.map(p => p.q))
  const qMax = Math.max(...points.map(p => p.q))
  const qRange = Math.max(1, qMax - qMin)
  const path = points.map((p, i) => {
    const x = padX + ((p.t - tMin) / tRange) * (w - padX * 2)
    const y = h - padY - ((p.q - qMin) / qRange) * (h - padY * 2)
    return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
  }).join(' ')
  const first = points[0]
  const last = points[points.length - 1]
  const totalDeltaUSD = usdFromQuota(last.q - first.q)
  return (
    <div className="flex items-center gap-4">
      <svg width={w} height={h} className="bg-white border border-gray-200 rounded">
        <path d={path} stroke="#10b981" strokeWidth="1.5" fill="none" />
      </svg>
      <div className="text-[11px] text-gray-600 space-y-0.5 tabular-nums">
        <div>点数：{points.length}</div>
        <div>窗口：{fmtTime(first.t)} → {fmtTime(last.t)}</div>
        <div className={totalDeltaUSD > 0 ? 'text-rose-600 font-medium' : 'text-gray-500'}>
          该窗口用量 Δ = ${totalDeltaUSD.toFixed(4)}
        </div>
      </div>
    </div>
  )
}

function fmtTime(epoch: number) {
  if (!epoch) return '—'
  return new Date(epoch * 1000).toLocaleString()
}

function usdFromQuota(q: number) {
  return q / 500000
}

export default function RemoteChannels() {
  const [profiles, setProfiles] = useState<RemoteProfile[]>([])
  const [selectedID, setSelectedID] = useState<number | null>(null)
  const [loadingProfiles, setLoadingProfiles] = useState(true)

  const [channels, setChannels] = useState<RemoteChannel[]>([])
  const [meta, setMeta] = useState<{ total: number; truncated: boolean; host: string } | null>(null)
  const [fetching, setFetching] = useState(false)
  const [fetchErr, setFetchErr] = useState<string | null>(null)
  const [refreshedAt, setRefreshedAt] = useState('')

  // Last-hour cost per channel (channel_id -> USD). Loaded on demand.
  const [lastHour, setLastHour] = useState<Record<number, number>>({})
  const [lastHourLoading, setLastHourLoading] = useState(false)

  // Baseline used_quota per channel from the previous background snapshot.
  // The Δ column subtracts this from live used_quota to show recent burn.
  // Empty until fetchChannels or a manual reload populates it.
  const [snapshotBaseline, setSnapshotBaseline] = useState<Record<number, { captured_at: number; used_quota: number }>>({})

  // Sparkline state: which channel row is expanded, and cached per-channel
  // 24h time series so re-expanding is instant.
  const [expandedRow, setExpandedRow] = useState<number | null>(null)
  const [seriesCache, setSeriesCache] = useState<Record<number, { t: number; q: number }[]>>({})
  const [seriesLoading, setSeriesLoading] = useState<number | null>(null)

  // Date filter: client-side by channel.created_time. Empty = no bound on
  // that side. Applied to the table + summary + CSV export.
  const [filterStart, setFilterStart] = useState('')
  const [filterEnd, setFilterEnd] = useState('')

  // Create / edit form. `editingID = 0` means we're creating a new profile.
  const [formOpen, setFormOpen] = useState(false)
  const [editingID, setEditingID] = useState<number | null>(null)
  const [formName, setFormName] = useState('')
  const [formHost, setFormHost] = useState('')
  const [formUserID, setFormUserID] = useState('')
  const [formToken, setFormToken] = useState('')
  const [formBusy, setFormBusy] = useState(false)
  const [formErr, setFormErr] = useState<string | null>(null)

  // Batch upload keys modal.
  const [batchOpen, setBatchOpen] = useState(false)
  const [batchPrefix, setBatchPrefix] = useState('')
  const [batchGroup, setBatchGroup] = useState('default')
  const [batchTag, setBatchTag] = useState('')
  const [batchPriority, setBatchPriority] = useState('')
  // Sequential-priority mode: same UX as BatchCreatePanel.
  //   same → all keys share `batchPriority`
  //   desc → key[i] = batchPriority − i (higher priority up front)
  //   asc  → key[i] = batchPriority + i
  const [batchPrioMode, setBatchPrioMode] = useState<'same' | 'desc' | 'asc'>('same')
  const [batchModels, setBatchModels] = useState(DEFAULT_ANTHROPIC_MODELS)
  const [batchInput, setBatchInput] = useState('')
  const [batchBusy, setBatchBusy] = useState(false)
  const [batchErr, setBatchErr] = useState<string | null>(null)
  const [batchResults, setBatchResults] = useState<RemoteChannelCreateResult[] | null>(null)

  // Row edit modal.
  const [rowOpen, setRowOpen] = useState(false)
  const [rowChannel, setRowChannel] = useState<RemoteChannel | null>(null)
  const [rowName, setRowName] = useState('')
  const [rowTag, setRowTag] = useState('')
  const [rowGroup, setRowGroup] = useState('')
  const [rowStatus, setRowStatus] = useState(1)
  const [rowPriority, setRowPriority] = useState('')
  const [rowQuotaUSD, setRowQuotaUSD] = useState('')
  const [rowNote, setRowNote] = useState('')
  const [rowBusy, setRowBusy] = useState(false)
  const [rowErr, setRowErr] = useState<string | null>(null)

  // Per-row test result (channel_id -> pretty message).
  const [testMsg, setTestMsg] = useState<Record<number, string>>({})
  const [testingID, setTestingID] = useState<number | null>(null)

  const reloadProfiles = useCallback(async () => {
    setLoadingProfiles(true)
    try {
      const res = await api.remoteProfiles()
      setProfiles(res.profiles)
      // Auto-select the first profile if none picked yet.
      setSelectedID(prev => prev ?? (res.profiles[0]?.id ?? null))
    } catch (e) {
      console.error(e)
    } finally {
      setLoadingProfiles(false)
    }
  }, [])

  useEffect(() => { void reloadProfiles() }, [reloadProfiles])

  // Load cached channel list from local mirror as soon as a profile is
  // selected (including on page reload). Purely local — no remote hit.
  // The user can still click "Fetch channels" to force a live pull.
  useEffect(() => {
    if (!selectedID) {
      setChannels([])
      setMeta(null)
      setRefreshedAt('')
      return
    }
    void (async () => {
      try {
        const res = await api.remoteCachedChannels(selectedID)
        setChannels(res.channels)
        setMeta({ total: res.total, truncated: false, host: '' })
        if (res.cached_at > 0) {
          setRefreshedAt(new Date(res.cached_at * 1000).toLocaleTimeString('zh-CN') + ' · cached')
        } else {
          setRefreshedAt('')
        }
        // Sparkline / test / last-hour data belongs to a specific live
        // fetch — reset when we're just showing the cached mirror.
        setLastHour({})
        setTestMsg({})
        setExpandedRow(null)
        // Pull the previous-snapshot baseline so the Δ column renders.
        void loadSnapshotBaseline(selectedID)
      } catch (e) {
        console.warn('cached load failed', e)
      }
    })()
  }, [selectedID])

  const openCreate = () => {
    setEditingID(0)
    setFormName('')
    setFormHost('')
    setFormUserID('')
    setFormToken('')
    setFormErr(null)
    setFormOpen(true)
  }

  const openEdit = (p: RemoteProfile) => {
    setEditingID(p.id)
    setFormName(p.name)
    setFormHost(p.host)
    setFormUserID(String(p.user_id))
    setFormToken('')
    setFormErr(null)
    setFormOpen(true)
  }

  const submitForm = async () => {
    setFormErr(null)
    const uid = parseInt(formUserID, 10)
    if (!formName.trim()) return setFormErr('name is required')
    if (!formHost.trim()) return setFormErr('host is required')
    if (isNaN(uid) || uid <= 0) return setFormErr('user_id must be positive integer')
    if (editingID === 0 && !formToken.trim()) return setFormErr('access_token is required for new profile')
    setFormBusy(true)
    try {
      if (editingID === 0) {
        const created = await api.remoteProfileCreate({
          name: formName.trim(),
          host: formHost.trim(),
          user_id: uid,
          access_token: formToken.trim(),
        })
        await reloadProfiles()
        setSelectedID(created.id)
      } else if (editingID) {
        const patch: Parameters<typeof api.remoteProfileUpdate>[1] = {
          name: formName.trim(),
          host: formHost.trim(),
          user_id: uid,
        }
        if (formToken.trim()) patch.access_token = formToken.trim()
        await api.remoteProfileUpdate(editingID, patch)
        await reloadProfiles()
      }
      setFormOpen(false)
    } catch (e: any) {
      setFormErr(e?.message || String(e))
    } finally {
      setFormBusy(false)
    }
  }

  const deleteProfile = async (p: RemoteProfile) => {
    if (!window.confirm(`Delete profile "${p.name}"? Cannot be undone.`)) return
    try {
      await api.remoteProfileDelete(p.id)
      if (selectedID === p.id) setSelectedID(null)
      await reloadProfiles()
    } catch (e: any) {
      alert('delete failed: ' + (e?.message || e))
    }
  }

  const fetchChannels = async () => {
    if (!selectedID) return
    setFetching(true)
    setFetchErr(null)
    try {
      const res = await api.remoteFetchChannels({ profile_id: selectedID })
      setChannels(res.channels)
      setMeta({ total: res.total, truncated: res.truncated, host: res.host })
      setRefreshedAt(new Date().toLocaleTimeString('zh-CN'))
      // Any prior last-hour / test state is stale against the refreshed list.
      setLastHour({})
      setTestMsg({})
      // Cached sparkline data is per-channel time series; keep it, since
      // adding new points doesn't invalidate older ones. Just close any
      // currently-open sparkline so the layout resets cleanly.
      setExpandedRow(null)
      // Pull the latest background-captured snapshot so the Δ column can
      // compare live used_quota against the previous known value. Fire and
      // forget — a slow query shouldn't block the table from rendering.
      void loadSnapshotBaseline(selectedID)
    } catch (e: any) {
      setFetchErr(e?.message || String(e))
      setChannels([])
      setMeta(null)
    } finally {
      setFetching(false)
    }
  }

  const loadSnapshotBaseline = async (profileID: number) => {
    try {
      // 24h window is enough for the "recent burn" delta while keeping the
      // response small (~5000 rows worst case).
      const since = Math.floor(Date.now() / 1000) - 24 * 3600
      const res = await api.remoteSnapshotLatest(profileID, since)
      const next: Record<number, { captured_at: number; used_quota: number }> = {}
      for (const [k, v] of Object.entries(res.latest)) {
        next[parseInt(k, 10)] = v
      }
      setSnapshotBaseline(next)
    } catch (e) {
      // Non-fatal — the Δ column just shows "—" if we couldn't load.
      console.warn('snapshot baseline load failed', e)
    }
  }

  const toggleSparkline = async (channelID: number) => {
    if (expandedRow === channelID) {
      setExpandedRow(null)
      return
    }
    setExpandedRow(channelID)
    if (seriesCache[channelID] || !selectedID) return
    setSeriesLoading(channelID)
    try {
      const since = Math.floor(Date.now() / 1000) - 24 * 3600
      const res = await api.remoteSnapshotSeries(selectedID, channelID, since)
      const points = res.points.map(p => ({ t: p.captured_at, q: p.used_quota }))
      setSeriesCache(prev => ({ ...prev, [channelID]: points }))
    } catch (e) {
      console.warn('sparkline load failed', e)
    } finally {
      setSeriesLoading(null)
    }
  }

  const loadLastHour = async () => {
    if (!selectedID || channels.length === 0) return
    setLastHourLoading(true)
    try {
      const res = await api.remoteChannelLastHour(selectedID, channels.map(c => c.id))
      const next: Record<number, number> = {}
      for (const [k, v] of Object.entries(res.data)) {
        next[parseInt(k, 10)] = usdFromQuota(v as number)
      }
      setLastHour(next)
    } catch (e: any) {
      alert('load last-hour failed: ' + (e?.message || e))
    } finally {
      setLastHourLoading(false)
    }
  }

  const openBatch = () => {
    setBatchPrefix('')
    setBatchGroup('default')
    setBatchTag('')
    setBatchPriority('')
    setBatchModels(DEFAULT_ANTHROPIC_MODELS)
    setBatchInput('')
    setBatchErr(null)
    setBatchResults(null)
    setBatchOpen(true)
  }

  const submitBatch = async () => {
    if (!selectedID) return
    setBatchErr(null)
    if (!batchPrefix.trim()) return setBatchErr('name_prefix is required')
    if (!batchModels.trim()) return setBatchErr('models is required')
    const items: { key: string; quota_usd?: number; note?: string; priority?: number }[] = []
    for (const raw of batchInput.split('\n')) {
      const t = raw.trim()
      if (!t || t.startsWith('#')) continue
      const parts = t.split(/[\s,]+/)
      const key = parts[0]
      if (!key) continue
      const item: { key: string; quota_usd?: number; note?: string; priority?: number } = { key }
      if (parts[1]) {
        const q = parseFloat(parts[1])
        if (!isNaN(q) && q > 0) item.quota_usd = q
      }
      if (parts.length > 2) {
        item.note = parts.slice(2).join(' ')
      }
      items.push(item)
    }
    if (items.length === 0) return setBatchErr('未解析到有效行')

    const basePriority = batchPriority.trim() ? parseInt(batchPriority.trim(), 10) : NaN
    // In 'same' mode we pass the priority at batch level and every item
    // inherits it. In sequential modes we compute per-item priority so the
    // backend applies each independently.
    let batchLevelPriority: number | undefined
    if (!isNaN(basePriority) && basePriority > 0) {
      if (batchPrioMode === 'same') {
        batchLevelPriority = basePriority
      } else {
        const step = batchPrioMode === 'desc' ? -1 : 1
        items.forEach((it, i) => { it.priority = Math.max(1, basePriority + i * step) })
      }
    }
    setBatchBusy(true)
    try {
      const res = await api.remoteChannelCreate({
        profile_id: selectedID,
        name_prefix: batchPrefix.trim(),
        group: batchGroup.trim() || 'default',
        tag: batchTag.trim() || undefined,
        priority: batchLevelPriority,
        models: batchModels.trim(),
        items,
      })
      setBatchResults(res.results)
      // Refresh the list so newly created rows show up.
      void fetchChannels()
    } catch (e: any) {
      setBatchErr(e?.message || String(e))
    } finally {
      setBatchBusy(false)
    }
  }

  const openRowEdit = (ch: RemoteChannel) => {
    setRowChannel(ch)
    setRowName(ch.name)
    setRowTag(ch.tag)
    setRowGroup(ch.group)
    setRowStatus(ch.status)
    setRowPriority(String(ch.priority ?? ''))
    setRowQuotaUSD(ch.quota_usd != null ? String(ch.quota_usd) : '')
    setRowNote(ch.note ?? '')
    setRowErr(null)
    setRowOpen(true)
  }

  const submitRowEdit = async () => {
    if (!selectedID || !rowChannel) return
    setRowErr(null)
    const patch: Parameters<typeof api.remoteChannelUpdate>[0] = {
      profile_id: selectedID,
      channel_id: rowChannel.id,
    }
    if (rowName !== rowChannel.name) patch.name = rowName
    if (rowTag !== rowChannel.tag) patch.tag = rowTag
    if (rowGroup !== rowChannel.group) patch.group = rowGroup
    if (rowStatus !== rowChannel.status) patch.status = rowStatus
    const priorityNum = rowPriority.trim() ? parseInt(rowPriority.trim(), 10) : undefined
    if (priorityNum != null && !isNaN(priorityNum) && priorityNum !== rowChannel.priority) {
      patch.priority = priorityNum
    }
    const quotaNum = rowQuotaUSD.trim() ? parseFloat(rowQuotaUSD.trim()) : null
    const prevQuota = rowChannel.quota_usd ?? null
    if (quotaNum !== prevQuota) {
      patch.quota_usd = quotaNum // may be null to clear
    }
    if ((rowNote ?? '') !== (rowChannel.note ?? '')) {
      patch.note = rowNote
    }
    setRowBusy(true)
    try {
      await api.remoteChannelUpdate(patch)
      setRowOpen(false)
      // Refresh just this row.
      try {
        const r = await api.remoteChannelGet(selectedID, rowChannel.id)
        setChannels(prev => prev.map(c => (c.id === rowChannel.id ? r.channel : c)))
      } catch { /* fallthrough */ }
    } catch (e: any) {
      setRowErr(e?.message || String(e))
    } finally {
      setRowBusy(false)
    }
  }

  const deleteRow = async (ch: RemoteChannel) => {
    if (!selectedID) return
    if (!window.confirm(`确认删除 "${ch.name}"？此操作会同时删除远端渠道，不可恢复。`)) return
    try {
      await api.remoteChannelDelete(selectedID, ch.id)
      setChannels(prev => prev.filter(c => c.id !== ch.id))
    } catch (e: any) {
      alert('delete failed: ' + (e?.message || e))
    }
  }

  const testRow = async (ch: RemoteChannel) => {
    // We don't have the raw key on the frontend — the operator must paste it.
    const key = window.prompt(`粘贴 ${ch.name} 的原始 key（不会被存储，仅用于本次连通性测试）:`)
    if (!key) return
    setTestingID(ch.id)
    try {
      const res = await api.remoteTestKey(key.trim(), DEFAULT_TEST_MODEL)
      const msg = res.ok
        ? `✓ ${res.latency_ms}ms`
        : `✗ ${res.status || ''} ${res.error || res.message || '失败'}`
      setTestMsg(prev => ({ ...prev, [ch.id]: msg }))
    } catch (e: any) {
      setTestMsg(prev => ({ ...prev, [ch.id]: '✗ ' + (e?.message || e) }))
    } finally {
      setTestingID(null)
    }
  }

  // Client-side date filter on channel.created_time. Dates are interpreted
  // in local time, upper bound exclusive so [today, today] = "just today".
  const filteredChannels = useMemo(() => {
    const startTS = filterStart ? Math.floor(new Date(filterStart + 'T00:00:00').getTime() / 1000) : 0
    const endTS = filterEnd ? Math.floor(new Date(filterEnd + 'T00:00:00').getTime() / 1000) + 86400 : 0
    if (!startTS && !endTS) return channels
    return channels.filter(c => {
      const t = c.created_time || 0
      if (startTS && t < startTS) return false
      if (endTS && t >= endTS) return false
      return true
    })
  }, [channels, filterStart, filterEnd])

  const summary = useMemo(() => {
    const totalUsedUSD = filteredChannels.reduce((s, c) => s + usdFromQuota(c.used_quota), 0)
    const enabled = filteredChannels.filter(c => c.status === 1).length
    const disabled = filteredChannels.length - enabled
    return { count: filteredChannels.length, totalUsedUSD, enabled, disabled }
  }, [filteredChannels])

  const exportCSV = () => {
    if (filteredChannels.length === 0) return
    const header = ['ID', 'Name', 'Type', 'Group', 'Tag', 'Priority', 'Used USD', 'Δ USD (since baseline)', '额度 USD', 'Last 1h USD', 'Status', 'Created', 'Note']
    const escape = (v: unknown) => {
      const s = v == null ? '' : String(v)
      return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s
    }
    const rows = filteredChannels.map(c => {
      const usedUSD = usdFromQuota(c.used_quota)
      const baseline = snapshotBaseline[c.id]
      const deltaUSD = baseline ? usdFromQuota(c.used_quota - baseline.used_quota) : null
      const lh = lastHour[c.id]
      return [
        c.id, c.name, c.type, c.group, c.tag, c.priority,
        usedUSD.toFixed(4),
        deltaUSD != null ? deltaUSD.toFixed(4) : '',
        c.quota_usd != null ? c.quota_usd.toFixed(2) : '',
        lh != null ? lh.toFixed(4) : '',
        STATUS_LABEL[c.status] ?? c.status,
        c.created_time ? new Date(c.created_time * 1000).toISOString() : '',
        c.note || '',
      ].map(escape).join(',')
    })
    const csv = [header.join(','), ...rows].join('\n')
    // BOM so Excel opens it as UTF-8 without garbling.
    const blob = new Blob(['﻿' + csv], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    const suffix = (filterStart || filterEnd) ? `_${filterStart || 'any'}_${filterEnd || 'any'}` : ''
    const profileName = profiles.find(p => p.id === selectedID)?.name || 'remote'
    a.href = url
    a.download = `remote-channels_${profileName}${suffix}.csv`
    a.click()
    URL.revokeObjectURL(url)
  }

  const actions = (
    <div className="flex items-center gap-2 flex-wrap">
      <div className="flex items-center gap-1">
        <input
          type="date"
          value={filterStart}
          onChange={e => setFilterStart(e.target.value)}
          className="border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-white"
          title="创建时间 ≥"
        />
        <span className="text-gray-300 text-xs">→</span>
        <input
          type="date"
          value={filterEnd}
          onChange={e => setFilterEnd(e.target.value)}
          className="border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-white"
          title="创建时间 ≤"
        />
        {(filterStart || filterEnd) && (
          <button
            onClick={() => { setFilterStart(''); setFilterEnd('') }}
            className="text-[10px] text-gray-400 hover:text-gray-700 px-1"
            title="清除日期筛选"
          >×</button>
        )}
      </div>
      <button
        onClick={exportCSV}
        disabled={filteredChannels.length === 0}
        className="border border-gray-300 text-gray-700 rounded-md px-3 py-1.5 text-xs hover:bg-gray-50 disabled:opacity-40"
      >
        导出 CSV
      </button>
      <button
        onClick={openCreate}
        className="border border-gray-300 text-gray-700 rounded-md px-3 py-1.5 text-xs hover:bg-gray-50"
      >
        + New profile
      </button>
      <button
        onClick={openBatch}
        disabled={!selectedID}
        className="border border-emerald-600 text-emerald-700 rounded-md px-3 py-1.5 text-xs hover:bg-emerald-50 disabled:opacity-40"
      >
        + 批量上 key
      </button>
      <button
        onClick={loadLastHour}
        disabled={!selectedID || channels.length === 0 || lastHourLoading}
        className="border border-gray-300 text-gray-700 rounded-md px-3 py-1.5 text-xs hover:bg-gray-50 disabled:opacity-40"
      >
        {lastHourLoading ? '加载中…' : '加载 last-hour'}
      </button>
      <button
        onClick={fetchChannels}
        disabled={!selectedID || fetching}
        className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-50"
      >
        {fetching ? 'Fetching…' : 'Fetch channels'}
      </button>
    </div>
  )

  return (
    <Layout
      title="Remote Channels"
      subtitle={`拉取外部 new-api 部署的所有渠道与累计用量${refreshedAt ? ` · 更新于 ${refreshedAt}` : ''}`}
      actions={actions}
    >
      <div className="space-y-4">
        {/* Profile selector */}
        <section className="bg-white border border-gray-200 rounded-xl p-4">
          <div className="flex items-center justify-between mb-3">
            <h2 className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">Profile</h2>
            {loadingProfiles && <span className="text-[11px] text-gray-400">loading…</span>}
          </div>
          {profiles.length === 0 && !loadingProfiles && (
            <p className="text-xs text-gray-500">
              还没有 profile，点右上角 <span className="font-medium">"+ New profile"</span> 添加。
            </p>
          )}
          {profiles.length > 0 && (
            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-2">
              {profiles.map(p => {
                const active = selectedID === p.id
                return (
                  <div
                    key={p.id}
                    onClick={() => setSelectedID(p.id)}
                    className={`border rounded-md p-3 cursor-pointer transition-colors ${
                      active ? 'border-gray-900 bg-gray-50' : 'border-gray-200 hover:border-gray-400'
                    }`}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <div className="text-sm font-medium text-gray-900 truncate">{p.name}</div>
                        <div className="text-[11px] text-gray-500 truncate">{p.host}</div>
                        <div className="text-[10px] text-gray-400 mt-1">
                          user_id={p.user_id} · token {p.has_token ? '已保存' : '未设'}
                        </div>
                      </div>
                      <div className="flex flex-col gap-1 shrink-0">
                        <button
                          onClick={e => { e.stopPropagation(); openEdit(p) }}
                          className="text-[10px] text-gray-500 hover:text-gray-900"
                        >编辑</button>
                        <button
                          onClick={e => { e.stopPropagation(); void deleteProfile(p) }}
                          className="text-[10px] text-rose-500 hover:text-rose-700"
                        >删除</button>
                      </div>
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </section>

        {/* Fetch result */}
        {fetchErr && (
          <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
            {fetchErr}
          </div>
        )}
        {channels.length > 0 && meta && (
          <>
            <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
              <MetricCard label="渠道总数" value={String(summary.count)} />
              <MetricCard label="启用" value={String(summary.enabled)} color="text-emerald-600" />
              <MetricCard label="禁用" value={String(summary.disabled)} color="text-rose-600" />
              <MetricCard label="累计已用" value={'$' + summary.totalUsedUSD.toFixed(2)} color="text-blue-600" />
            </div>
            {meta.truncated && (
              <div className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800">
                结果被截断 —— 远端 total={meta.total}, 只拉到 {channels.length}。远端超过 5000 个渠道时启用。
              </div>
            )}
            <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
              <div className="overflow-x-auto">
                <table className="w-full text-xs">
                  <thead className="bg-gray-50 border-b border-gray-200 text-gray-500">
                    <tr>
                      <th className="px-3 py-2 text-left font-medium" title="点击 📈 展开 24h 曲线"></th>
                      <th className="px-3 py-2 text-left font-medium">ID</th>
                      <th className="px-3 py-2 text-left font-medium">名称</th>
                      <th className="px-3 py-2 text-left font-medium">Type</th>
                      <th className="px-3 py-2 text-left font-medium">Group</th>
                      <th className="px-3 py-2 text-left font-medium">Tag</th>
                      <th className="px-3 py-2 text-right font-medium">Priority</th>
                      <th className="px-3 py-2 text-right font-medium">已用 (USD)</th>
                      <th className="px-3 py-2 text-right font-medium" title="距上一次后台快照的用量增量">Δ</th>
                      <th className="px-3 py-2 text-right font-medium">额度 (USD)</th>
                      <th className="px-3 py-2 text-right font-medium">Last 1h</th>
                      <th className="px-3 py-2 text-left font-medium">状态</th>
                      <th className="px-3 py-2 text-left font-medium">Note</th>
                      <th className="px-3 py-2 text-left font-medium">操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {filteredChannels.map(c => {
                      const usedUSD = usdFromQuota(c.used_quota)
                      const pct = c.quota_usd && c.quota_usd > 0 ? Math.min(100, (usedUSD / c.quota_usd) * 100) : null
                      // Δ vs last background snapshot. If we have no baseline
                      // yet (channel first seen this session), leave it blank
                      // rather than showing a misleading zero.
                      const baseline = snapshotBaseline[c.id]
                      const deltaUSD = baseline ? usdFromQuota(c.used_quota - baseline.used_quota) : null
                      const isOpen = expandedRow === c.id
                      const series = seriesCache[c.id]
                      return (
                        <FragmentRow key={c.id}>
                          <tr className="border-b border-gray-100 hover:bg-gray-50">
                            <td className="px-2 py-2 text-center">
                              <button
                                onClick={() => void toggleSparkline(c.id)}
                                className={`text-[10px] ${isOpen ? 'text-emerald-600' : 'text-gray-400 hover:text-gray-700'}`}
                                title={isOpen ? '收起' : '查看 24h 曲线'}
                              >
                                {isOpen ? '▾' : '▸'}
                              </button>
                            </td>
                            <td className="px-3 py-2 tabular-nums">{c.id}</td>
                            <td className="px-3 py-2 font-mono text-[11px] max-w-[280px] truncate" title={c.name}>{c.name}</td>
                            <td className="px-3 py-2 tabular-nums">{c.type}</td>
                            <td className="px-3 py-2">{c.group || '—'}</td>
                            <td className="px-3 py-2 text-gray-500">{c.tag || '—'}</td>
                            <td className="px-3 py-2 tabular-nums text-right">{c.priority}</td>
                            <td className="px-3 py-2 tabular-nums text-right font-medium">${usedUSD.toFixed(2)}</td>
                            <td className="px-3 py-2 tabular-nums text-right">
                              {deltaUSD != null ? (
                                <span
                                  className={deltaUSD > 0 ? 'text-rose-600' : 'text-gray-400'}
                                  title={baseline ? `since ${new Date(baseline.captured_at * 1000).toLocaleTimeString('zh-CN')}` : ''}
                                >
                                  {deltaUSD > 0 ? '+' : ''}${deltaUSD.toFixed(4)}
                                </span>
                              ) : (
                                <span className="text-gray-300">—</span>
                              )}
                            </td>
                            <td className="px-3 py-2 tabular-nums text-right">
                              {c.quota_usd != null ? (
                                <div className="flex flex-col items-end gap-0.5">
                                  <span>${c.quota_usd.toFixed(2)}</span>
                                  {pct != null && (
                                    <div className="w-16 h-1 bg-gray-100 rounded overflow-hidden">
                                      <div
                                        className={`h-full ${pct >= 100 ? 'bg-rose-500' : pct >= 80 ? 'bg-amber-500' : 'bg-emerald-500'}`}
                                        style={{ width: pct + '%' }}
                                      />
                                    </div>
                                  )}
                                </div>
                              ) : (
                                <span className="text-gray-300">—</span>
                              )}
                            </td>
                            <td className="px-3 py-2 tabular-nums text-right">
                              {lastHour[c.id] != null ? '$' + lastHour[c.id].toFixed(4) : <span className="text-gray-300">—</span>}
                            </td>
                            <td className="px-3 py-2">
                              <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] ${STATUS_CLS[c.status] ?? 'bg-gray-100 text-gray-600'}`}>
                                {STATUS_LABEL[c.status] ?? c.status}
                              </span>
                            </td>
                            <td className="px-3 py-2 text-gray-500 max-w-[180px] truncate" title={c.note || ''}>
                              {c.note || '—'}
                            </td>
                            <td className="px-3 py-2">
                              <div className="flex items-center gap-1.5 flex-wrap">
                                <button
                                  onClick={() => openRowEdit(c)}
                                  className="text-[10px] text-gray-600 hover:text-gray-900"
                                >编辑</button>
                                <button
                                  onClick={() => void testRow(c)}
                                  disabled={testingID === c.id}
                                  className="text-[10px] text-blue-600 hover:text-blue-800 disabled:opacity-40"
                                >{testingID === c.id ? '测试中…' : '测试'}</button>
                                <button
                                  onClick={() => void deleteRow(c)}
                                  className="text-[10px] text-rose-500 hover:text-rose-700"
                                >删除</button>
                                {testMsg[c.id] && (
                                  <span
                                    className={`text-[10px] ${testMsg[c.id].startsWith('✓') ? 'text-emerald-600' : 'text-rose-600'}`}
                                    title={testMsg[c.id]}
                                  >
                                    {testMsg[c.id].length > 24 ? testMsg[c.id].slice(0, 24) + '…' : testMsg[c.id]}
                                  </span>
                                )}
                              </div>
                            </td>
                          </tr>
                          {isOpen && (
                            <tr className="bg-gray-50/60 border-b border-gray-100">
                              <td colSpan={14} className="px-4 py-3">
                                {seriesLoading === c.id ? (
                                  <div className="text-[11px] text-gray-400">加载 24h 数据…</div>
                                ) : series && series.length >= 2 ? (
                                  <Sparkline points={series} />
                                ) : (
                                  <div className="text-[11px] text-gray-400">
                                    暂无历史点（后台每 15 min 采一次；等下一轮就有数据了）
                                  </div>
                                )}
                              </td>
                            </tr>
                          )}
                        </FragmentRow>
                      )
                    })}
                  </tbody>
                </table>
              </div>
              <div className="px-3 py-2 text-[10px] text-gray-400 border-t border-gray-100">
                创建时间列已从表格移除以节省空间；如需查看，将鼠标悬停到名称。
              </div>
            </div>
          </>
        )}
      </div>

      {/* Modal: batch upload keys */}
      {batchOpen && (
        <div
          className="fixed inset-0 z-50 bg-black/40 flex items-center justify-center p-4"
          onClick={() => !batchBusy && setBatchOpen(false)}
        >
          <div
            className="bg-white rounded-lg shadow-xl w-full max-w-2xl p-5 max-h-[90vh] overflow-y-auto"
            onClick={e => e.stopPropagation()}
          >
            <h3 className="text-sm font-semibold text-gray-900 mb-3">批量上 key 到远端 new-api</h3>
            <div className="grid grid-cols-2 gap-3 mb-3">
              <Field label="Name prefix (最终名 = prefix-sha8key)">
                <input
                  value={batchPrefix}
                  onChange={e => setBatchPrefix(e.target.value)}
                  placeholder="例如 0706-pipi-a"
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                />
              </Field>
              <Field label="Group">
                <input
                  value={batchGroup}
                  onChange={e => setBatchGroup(e.target.value)}
                  placeholder="default"
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                />
              </Field>
              <Field label="Tag（可选）">
                <input
                  value={batchTag}
                  onChange={e => setBatchTag(e.target.value)}
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                />
              </Field>
              <Field label={`Priority${batchPrioMode === 'desc' ? '（base − i）' : batchPrioMode === 'asc' ? '（base + i）' : '（可选）'}`}>
                <div className="flex gap-1">
                  <input
                    type="number"
                    min="0"
                    value={batchPriority}
                    onChange={e => setBatchPriority(e.target.value)}
                    placeholder={batchPrioMode === 'same' ? '例如 1001' : '起始 base'}
                    className="flex-1 border border-gray-300 rounded-md px-2 py-1.5 text-sm tabular-nums focus:outline-none focus:border-gray-900"
                  />
                  <select
                    value={batchPrioMode}
                    onChange={e => setBatchPrioMode(e.target.value as 'same' | 'desc' | 'asc')}
                    className="border border-gray-300 rounded-md px-2 py-1.5 text-sm bg-white focus:outline-none focus:border-gray-900"
                    title="统一 = 所有 key 用同一 priority；顺序 = 每个 key 依次递减/递增"
                  >
                    <option value="same">统一</option>
                    <option value="desc">顺序 ↓</option>
                    <option value="asc">顺序 ↑</option>
                  </select>
                </div>
              </Field>
            </div>
            <Field label="Models（逗号分隔）">
              <textarea
                value={batchModels}
                onChange={e => setBatchModels(e.target.value)}
                rows={2}
                className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-[11px] font-mono focus:outline-none focus:border-gray-900"
              />
            </Field>
            <div className="mt-3">
              <label className="block text-[11px] text-gray-500 mb-1">
                Keys —— 每行 <code className="text-gray-700 bg-gray-100 px-1">key [额度USD] [备注...]</code>
              </label>
              <textarea
                value={batchInput}
                onChange={e => setBatchInput(e.target.value)}
                rows={8}
                placeholder={'sk-ant-api03-xxxx 220\nsk-ant-api03-yyyy 500 备注文字\n# 井号开头的行会被忽略'}
                className="w-full border border-gray-300 rounded-md p-2 text-[11px] font-mono resize-y focus:outline-none focus:border-gray-900"
              />
              <p className="text-[10px] text-gray-400 mt-1">
                额度和备注可省。额度写在本地 remote_channel_meta；key 明文只走一次 POST，不落本地。
              </p>
            </div>
            {batchErr && <p className="text-xs text-rose-600 mt-2">{batchErr}</p>}
            {batchResults && (
              <div className="mt-3 border border-gray-200 rounded-md max-h-56 overflow-y-auto">
                <table className="w-full text-[11px]">
                  <thead className="bg-gray-50 text-gray-500 sticky top-0">
                    <tr>
                      <th className="px-2 py-1 text-left">Key</th>
                      <th className="px-2 py-1 text-left">结果</th>
                      <th className="px-2 py-1 text-left">Channel</th>
                    </tr>
                  </thead>
                  <tbody>
                    {batchResults.map((r, i) => (
                      <tr key={i} className="border-t border-gray-100">
                        <td className="px-2 py-1 font-mono">{r.key}</td>
                        <td className="px-2 py-1">
                          {r.ok ? <span className="text-emerald-600">✓ 成功</span>
                                : <span className="text-rose-600" title={r.error}>✗ {(r.error ?? '失败').slice(0, 40)}</span>}
                        </td>
                        <td className="px-2 py-1 text-gray-600">
                          {r.channel_id ? `#${r.channel_id} ${r.name ?? ''}` : '—'}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <div className="mt-5 flex justify-end gap-2">
              <button
                onClick={() => setBatchOpen(false)}
                disabled={batchBusy}
                className="border border-gray-300 rounded-md px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50"
              >
                关闭
              </button>
              <button
                onClick={submitBatch}
                disabled={batchBusy}
                className="bg-emerald-600 text-white rounded-md px-3 py-1.5 text-sm hover:opacity-85 disabled:opacity-50"
              >
                {batchBusy ? '上传中…' : '上传'}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Modal: row edit */}
      {rowOpen && rowChannel && (
        <div
          className="fixed inset-0 z-50 bg-black/40 flex items-center justify-center p-4"
          onClick={() => !rowBusy && setRowOpen(false)}
        >
          <div
            className="bg-white rounded-lg shadow-xl w-full max-w-md p-5"
            onClick={e => e.stopPropagation()}
          >
            <h3 className="text-sm font-semibold text-gray-900 mb-3">
              编辑渠道 #{rowChannel.id}
            </h3>
            <div className="space-y-3">
              <Field label="Name">
                <input
                  value={rowName}
                  onChange={e => setRowName(e.target.value)}
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm font-mono focus:outline-none focus:border-gray-900"
                />
              </Field>
              <div className="grid grid-cols-2 gap-2">
                <Field label="Group">
                  <input
                    value={rowGroup}
                    onChange={e => setRowGroup(e.target.value)}
                    className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                  />
                </Field>
                <Field label="Tag">
                  <input
                    value={rowTag}
                    onChange={e => setRowTag(e.target.value)}
                    className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                  />
                </Field>
              </div>
              <div className="grid grid-cols-2 gap-2">
                <Field label="Status">
                  <select
                    value={rowStatus}
                    onChange={e => setRowStatus(parseInt(e.target.value, 10))}
                    className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                  >
                    <option value={1}>1 · 启用</option>
                    <option value={2}>2 · 手动禁用</option>
                    <option value={3}>3 · 自动禁用</option>
                  </select>
                </Field>
                <Field label="Priority">
                  <input
                    type="number"
                    value={rowPriority}
                    onChange={e => setRowPriority(e.target.value)}
                    className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm tabular-nums focus:outline-none focus:border-gray-900"
                  />
                </Field>
              </div>
              <Field label="额度上限 (USD) · 本地存储">
                <input
                  type="number"
                  step="0.01"
                  value={rowQuotaUSD}
                  onChange={e => setRowQuotaUSD(e.target.value)}
                  placeholder="留空清除"
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm tabular-nums focus:outline-none focus:border-gray-900"
                />
              </Field>
              <Field label="Note · 本地存储">
                <textarea
                  value={rowNote}
                  onChange={e => setRowNote(e.target.value)}
                  rows={2}
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                />
              </Field>
              {rowErr && <p className="text-xs text-rose-600">{rowErr}</p>}
            </div>
            <div className="mt-5 flex justify-end gap-2">
              <button
                onClick={() => setRowOpen(false)}
                disabled={rowBusy}
                className="border border-gray-300 rounded-md px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50"
              >
                取消
              </button>
              <button
                onClick={submitRowEdit}
                disabled={rowBusy}
                className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-sm hover:opacity-85 disabled:opacity-50"
              >
                {rowBusy ? '保存中…' : '保存'}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Modal: create / edit */}
      {formOpen && (
        <div
          className="fixed inset-0 z-50 bg-black/40 flex items-center justify-center p-4"
          onClick={() => !formBusy && setFormOpen(false)}
        >
          <div
            className="bg-white rounded-lg shadow-xl w-full max-w-md p-5"
            onClick={e => e.stopPropagation()}
          >
            <h3 className="text-sm font-semibold text-gray-900 mb-3">
              {editingID === 0 ? 'New remote profile' : 'Edit profile'}
            </h3>
            <div className="space-y-3">
              <Field label="Name">
                <input
                  value={formName}
                  onChange={e => setFormName(e.target.value)}
                  placeholder="例如 anispark-prod"
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                />
              </Field>
              <Field label="Host">
                <input
                  value={formHost}
                  onChange={e => setFormHost(e.target.value)}
                  placeholder="https://ai-router-hk.anispark.ai"
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm font-mono focus:outline-none focus:border-gray-900"
                />
              </Field>
              <Field label="User ID (New-Api-User header)">
                <input
                  type="number"
                  min="1"
                  value={formUserID}
                  onChange={e => setFormUserID(e.target.value)}
                  placeholder="例如 1"
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm tabular-nums focus:outline-none focus:border-gray-900"
                />
              </Field>
              <Field label={editingID === 0 ? 'Access token' : 'Access token (留空保留原值)'}>
                <input
                  type="password"
                  value={formToken}
                  onChange={e => setFormToken(e.target.value)}
                  placeholder={editingID === 0 ? 'new-api access_token' : '••••••••'}
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm font-mono focus:outline-none focus:border-gray-900"
                />
              </Field>
              {formErr && <p className="text-xs text-rose-600">{formErr}</p>}
            </div>
            <div className="mt-5 flex justify-end gap-2">
              <button
                onClick={() => setFormOpen(false)}
                disabled={formBusy}
                className="border border-gray-300 rounded-md px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50"
              >
                取消
              </button>
              <button
                onClick={submitForm}
                disabled={formBusy}
                className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-sm hover:opacity-85 disabled:opacity-50"
              >
                {formBusy ? '保存中…' : '保存'}
              </button>
            </div>
          </div>
        </div>
      )}
    </Layout>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="block text-[11px] text-gray-500 mb-1">{label}</label>
      {children}
    </div>
  )
}

function MetricCard({ label, value, color }: { label: string; value: string; color?: string }) {
  return (
    <div className="bg-white border border-gray-200 rounded-xl p-3">
      <div className="text-[10px] text-gray-400 uppercase tracking-wider">{label}</div>
      <div className={`mt-1 text-lg font-semibold tabular-nums ${color ?? 'text-gray-900'}`}>{value}</div>
    </div>
  )
}
