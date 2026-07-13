import { useCallback, useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import {
  api,
  ROLE_STUDIO_OPERATOR,
  type PendingKey,
  type RemoteChannel,
  type RemoteChannelCreateResult,
  type RemoteProfile,
  type StudioPolicy,
} from '../api'
import { getCachedRole, loadRole } from '../App'
import RemoteChannelsStudio from './RemoteChannelsStudio'

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
  'claude-sonnet-5',
  'claude-opus-4-8',
  'claude-opus-4-7',
  'claude-sonnet-4-6',
  'claude-opus-4-6',
  'claude-haiku-4-5-20251001',
  'claude-sonnet-4-5-20250929',
  'claude-opus-4-5-20251101',
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

// todayUTC returns today's date as YYYY-MM-DD in UTC. Used as the default
// bound for the date filter — the operator sees "just today" regardless of
// browser timezone, matching how the backend stores created_time.
const todayUTC = (() => {
  const n = new Date()
  const y = n.getUTCFullYear()
  const m = String(n.getUTCMonth() + 1).padStart(2, '0')
  const d = String(n.getUTCDate()).padStart(2, '0')
  return `${y}-${m}-${d}`
})()

function fmtTime(epoch: number) {
  if (!epoch) return '—'
  return new Date(epoch * 1000).toLocaleString()
}

function usdFromQuota(q: number) {
  return q / 500000
}

export default function RemoteChannels() {
  // Studio-operator gets the slim page — profile picker, batch-upload
  // modal, own-studio pending queue. Everything else on the admin view
  // (channel table, profile CRUD, priority editor, snapshot chart) is
  // gated to super_admin server-side and doesn't render for them.
  const [role, setRole] = useState<number | null>(getCachedRole())
  useEffect(() => {
    if (role !== null) return
    void loadRole().then(setRole)
  }, [role])
  if (role === null) return null
  if (role === ROLE_STUDIO_OPERATOR) return <RemoteChannelsStudio />
  return <RemoteChannelsAdmin />
}

function RemoteChannelsAdmin() {
  const [profiles, setProfiles] = useState<RemoteProfile[]>([])
  const [selectedID, setSelectedID] = useState<number | null>(null)
  const [loadingProfiles, setLoadingProfiles] = useState(true)

  const [channels, setChannels] = useState<RemoteChannel[]>([])
  const [meta, setMeta] = useState<{ total: number; truncated: boolean; host: string } | null>(null)
  const [fetching, setFetching] = useState(false)
  const [fetchErr, setFetchErr] = useState<string | null>(null)
  const [refreshedAt, setRefreshedAt] = useState('')

  // Last-hour cost per channel (channel_id -> USD). Loaded on demand.
  // Last-hour column + per-channel /api/log/stat fan-out were removed —
  // realtime numbers live in the profile-wide summary card only.

  // Row selection for the bulk-cost editor. Cleared whenever the visible
  // channel list changes so a stale ID from a previous profile can't leak
  // into an update batch.
  const [selectedIDs, setSelectedIDs] = useState<Set<number>>(new Set())
  const [bulkCostOpen, setBulkCostOpen] = useState(false)
  // Only upstream unit_price_cny is bulk-editable here now; downstream
  // pricing moved to the per-profile per-day discount editor on Profit.
  const [bulkCostValue, setBulkCostValue] = useState('')

  // Bulk priority editor. `same` = one value across everyone; `desc`/`asc`
  // walks per-channel by ±1 from the base, mirroring the batch-upload
  // priority modes. Order = channel_id ascending so the result is stable.
  const [bulkPrioOpen, setBulkPrioOpen] = useState(false)
  const [bulkPrioValue, setBulkPrioValue] = useState('')
  const [bulkPrioMode, setBulkPrioMode] = useState<'same' | 'desc' | 'asc'>('same')
  const [bulkPrioBusy, setBulkPrioBusy] = useState(false)
  const [bulkPrioErr, setBulkPrioErr] = useState<string | null>(null)
  const [bulkPrioProgress, setBulkPrioProgress] = useState<{ done: number; total: number } | null>(null)
  const [bulkCostBusy, setBulkCostBusy] = useState(false)
  const [bulkCostErr, setBulkCostErr] = useState<string | null>(null)

  // Profile-wide realtime stat (rpm / tpm / last-hour quota). One remote
  // call per refresh regardless of channel count, so this stays cheap
  // even for large deployments. Polled every 30s while the page is open.
  const [statSummary, setStatSummary] = useState<{ rpm: number; tpm: number; quota_last_hour: number } | null>(null)

  // Baseline used_quota per channel from the previous background snapshot.
  // The Δ column subtracts this from live used_quota to show recent burn.
  // Empty until fetchChannels or a manual reload populates it.
  const [snapshotBaseline, setSnapshotBaseline] = useState<Record<number, { captured_at: number; used_quota: number }>>({})

  // Sparkline state: which channel row is expanded, and cached per-channel
  // 24h time series so re-expanding is instant.
  const [expandedRow, setExpandedRow] = useState<number | null>(null)
  const [seriesCache, setSeriesCache] = useState<Record<number, { t: number; q: number }[]>>({})
  const [seriesLoading, setSeriesLoading] = useState<number | null>(null)

  // Error-rate stats. Opt-in via toolbar button since it costs 2 remote
  // API calls per channel; backend caches for 5 minutes so subsequent
  // clicks reuse. rpm/errRpm are both 60s-window request counts (see
  // /api/log/stat on newapi). Rate = errRpm / (rpm + errRpm) when either
  // is nonzero; blank when both are 0 (no traffic to measure against).
  // Error-rate uses precise counts from the remote paginated log endpoint
  // over `errWindowSec`, not the hardcoded-60s RPM. Preset window keeps
  // the UI simple; adding a full date-range picker is trivial later.
  const [errStats, setErrStats] = useState<Record<number, { success: number; errors: number }>>({})
  const [errRateLoading, setErrRateLoading] = useState(false)
  const [errWindowSec, setErrWindowSec] = useState(3600) // 1h default
  // Modal state for the categorised error breakdown popup. null = closed.
  const [breakdownFor, setBreakdownFor] = useState<{ id: number; name: string } | null>(null)

  // Date filter: client-side by channel.created_time, dates interpreted in
  // UTC so [today, today] means "since UTC 00:00 today" and doesn't shift
  // with the operator's browser timezone. Default = today (UTC) on both
  // ends so the page opens to just-today's channels.
  const [filterStart, setFilterStart] = useState(todayUTC)
  const [filterEnd, setFilterEnd] = useState(todayUTC)

  // Create / edit form. `editingID = 0` means we're creating a new profile.
  const [formOpen, setFormOpen] = useState(false)
  const [editingID, setEditingID] = useState<number | null>(null)
  const [formName, setFormName] = useState('')
  const [formHost, setFormHost] = useState('')
  const [formUserID, setFormUserID] = useState('')
  const [formToken, setFormToken] = useState('')
  // Batch-upload defaults preloaded into the create modal from the
  // selected profile. Editable per-batch but sticky at the profile
  // level so operators don't retype 8 model names every day.
  const [formDefaultModels, setFormDefaultModels] = useState('')
  const [formDefaultGroup, setFormDefaultGroup] = useState('')
  // Pool throttle lives on the profile but is edited from the upload
  // queue panel (right where the operator watches keys stream through).
  // `pool_dirty` guards against clobbering an unsaved edit if the
  // profile list refetches mid-typing.
  const [poolIntervalSec, setPoolIntervalSec] = useState('60')
  const [poolBatchSize, setPoolBatchSize] = useState('2')
  // Auto mode: when on, the scheduler sizes each tick's batch against
  // live remote RPM. pool_batch_size becomes the ceiling; a fresh RPM
  // read below rpm_min pauses uploads entirely.
  const [poolAutoMode, setPoolAutoMode] = useState(false)
  const [poolRPMBase, setPoolRPMBase] = useState('150')
  const [poolRPMMin, setPoolRPMMin] = useState('50')
  const [poolDirty, setPoolDirty] = useState(false)
  const [poolSaving, setPoolSaving] = useState(false)
  const [poolMsg, setPoolMsg] = useState<{ ok: boolean; text: string } | null>(null)
  // Per-(profile, studio) accept/reject policy. Loaded when the queue
  // panel opens and after every enqueue so a new studio shows up.
  const [studioPolicies, setStudioPolicies] = useState<StudioPolicy[]>([])
  const [policyBusy, setPolicyBusy] = useState<string | null>(null)
  const [policyErr, setPolicyErr] = useState<string | null>(null)
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

  // Queue mode: when true, keys go into remote_pending_key instead of
  // being uploaded synchronously. pool_size>0 turns on drip: at most N
  // active at once, next one promotes when an active row's remote
  // channel gets disabled (quota exhausted).
  const [batchQueue, setBatchQueue] = useState(false)
  const [batchPoolSize, setBatchPoolSize] = useState('0')

  // Upload queue: rows from remote_pending_key for the selected profile.
  // Auto-refreshed after enqueue and every 30s so status transitions
  // (pending → active → used / failed) show up without a manual refresh.
  const [pending, setPending] = useState<PendingKey[]>([])
  const [pendingOpen, setPendingOpen] = useState(false)
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
        // Sparkline / test state belongs to a specific live fetch —
        // reset when we're just showing the cached mirror.
        setTestMsg({})
        setSelectedIDs(new Set())
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
    setFormDefaultModels(DEFAULT_ANTHROPIC_MODELS)
    setFormDefaultGroup('default')
    setFormErr(null)
    setFormOpen(true)
  }

  const openEdit = (p: RemoteProfile) => {
    setEditingID(p.id)
    setFormName(p.name)
    // Blank host on edit — same pattern as access_token. The current
    // value isn't shown; leave blank to keep, or type to replace. That
    // way the upstream URL isn't visible to anyone glancing at the
    // modal, and screenshots of the edit form don't leak it either.
    setFormHost('')
    setFormUserID(p.user_id != null ? String(p.user_id) : '')
    setFormToken('')
    setFormDefaultModels(p.default_models || '')
    setFormDefaultGroup(p.default_group || '')
    setFormErr(null)
    setFormOpen(true)
  }

  const submitForm = async () => {
    setFormErr(null)
    const uid = parseInt(formUserID, 10)
    if (!formName.trim()) return setFormErr('name is required')
    if (editingID === 0 && !formHost.trim()) return setFormErr('host is required')
    if (isNaN(uid) || uid <= 0) return setFormErr('user_id must be positive integer')
    if (editingID === 0 && !formToken.trim()) return setFormErr('access_token is required for new profile')
    setFormBusy(true)
    try {
      if (editingID === 0) {
        // New profile — pool tuning takes the schema defaults (60s / 2)
        // and is edited later from the upload queue panel.
        const created = await api.remoteProfileCreate({
          name: formName.trim(),
          host: formHost.trim(),
          user_id: uid,
          access_token: formToken.trim(),
          default_models: formDefaultModels.trim(),
          default_group: formDefaultGroup.trim(),
        })
        await reloadProfiles()
        setSelectedID(created.id)
      } else if (editingID) {
        const patch: Parameters<typeof api.remoteProfileUpdate>[1] = {
          name: formName.trim(),
          user_id: uid,
          default_models: formDefaultModels.trim(),
          default_group: formDefaultGroup.trim(),
        }
        if (formHost.trim()) patch.host = formHost.trim()
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
      // Any prior test state is stale against the refreshed list.
      setTestMsg({})
      setSelectedIDs(new Set())
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

  // Fetch success + error counts for every channel over errWindowSec.
  // Backend caches 5 min per (profile, channel, window) so re-clicks are
  // instant. Populates the "错误率" column; the per-channel breakdown
  // popover fetches its own type-bucketed data lazily on click.
  const loadErrorRates = async () => {
    if (!selectedID || channels.length === 0) return
    setErrRateLoading(true)
    try {
      const ids = channels.map(c => c.id)
      const res = await api.remoteChannelCounts(selectedID, ids, errWindowSec)
      const next: Record<number, { success: number; errors: number }> = {}
      for (const c of channels) {
        const key = String(c.id)
        const cnt = res.data[key]
        next[c.id] = { success: cnt?.success ?? 0, errors: cnt?.errors ?? 0 }
      }
      setErrStats(next)
    } catch (e: any) {
      console.warn('error rate load failed', e)
      alert('加载错误率失败：' + (e?.message || e))
    } finally {
      setErrRateLoading(false)
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

  // Profile-wide realtime stat for the summary cards. One remote call
  // per tick regardless of channel count, so this stays cheap. Kept
  // silent — a network blip just leaves the last value on screen.
  const loadStatSummary = useCallback(async (pid: number) => {
    try {
      const res = await api.remoteStatSummary(pid)
      setStatSummary({ rpm: res.rpm, tpm: res.tpm, quota_last_hour: res.quota_last_hour })
    } catch (e) {
      console.warn('stat summary failed', e)
    }
  }, [])

  useEffect(() => {
    if (!selectedID) {
      setStatSummary(null)
      return
    }
    void loadStatSummary(selectedID)
    const t = setInterval(() => { void loadStatSummary(selectedID) }, 30000)
    return () => clearInterval(t)
  }, [selectedID, loadStatSummary])

  const toggleRowSelected = (id: number, checked: boolean) => {
    setSelectedIDs(prev => {
      const next = new Set(prev)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  const toggleAllSelected = (visible: RemoteChannel[], checked: boolean) => {
    setSelectedIDs(prev => {
      const next = new Set(prev)
      for (const c of visible) {
        if (checked) next.add(c.id)
        else next.delete(c.id)
      }
      return next
    })
  }

  const openBulkCost = () => {
    setBulkCostValue('')
    setBulkCostErr(null)
    setBulkCostOpen(true)
  }

  const openBulkPrio = () => {
    setBulkPrioValue('')
    setBulkPrioMode('same')
    setBulkPrioErr(null)
    setBulkPrioProgress(null)
    setBulkPrioOpen(true)
  }

  const submitBulkPrio = async () => {
    if (!selectedID) return
    setBulkPrioErr(null)
    if (selectedIDs.size === 0) {
      setBulkPrioErr('先勾选至少一行')
      return
    }
    const base = parseInt(bulkPrioValue.trim(), 10)
    if (isNaN(base) || base < 1) {
      setBulkPrioErr('优先级必须是 ≥1 的整数')
      return
    }
    // Stable ordering: iterate by channel_id ascending so 'desc' assigns
    // the highest priority to the smallest ID (which is usually the
    // oldest / most-trusted channel in new-api).
    const ordered = channels
      .filter(c => selectedIDs.has(c.id))
      .map(c => c.id)
      .sort((a, b) => a - b)
    const step = bulkPrioMode === 'desc' ? -1 : bulkPrioMode === 'asc' ? 1 : 0
    const priorities = new Map<number, number>()
    for (let i = 0; i < ordered.length; i++) {
      const p = bulkPrioMode === 'same' ? base : Math.max(1, base + i * step)
      priorities.set(ordered[i], p)
    }

    setBulkPrioBusy(true)
    setBulkPrioProgress({ done: 0, total: ordered.length })
    let ok = 0
    const failed: number[] = []
    // Sequential to keep the remote's rate limit happy; ~50ms/channel is
    // fine for a couple hundred rows.
    for (let i = 0; i < ordered.length; i++) {
      const chID = ordered[i]
      const prio = priorities.get(chID)!
      try {
        await api.remoteChannelUpdate({
          profile_id: selectedID,
          channel_id: chID,
          priority: prio,
        })
        ok++
      } catch (e) {
        console.warn('bulk priority', chID, e)
        failed.push(chID)
      }
      setBulkPrioProgress({ done: i + 1, total: ordered.length })
    }
    // Optimistic patch so the "Priority" column reflects the new value
    // without waiting for a Fetch.
    setChannels(prev => prev.map(c => {
      const p = priorities.get(c.id)
      return p != null ? { ...c, priority: p } : c
    }))
    setBulkPrioBusy(false)
    alert(`已更新 ${ok} 条${failed.length ? `，${failed.length} 条失败 (id: ${failed.slice(0, 8).join(', ')}${failed.length > 8 ? '…' : ''})` : ''}`)
    setBulkPrioOpen(false)
    setSelectedIDs(new Set())
  }

  const submitBulkCost = async () => {
    if (!selectedID) return
    setBulkCostErr(null)
    if (selectedIDs.size === 0) {
      setBulkCostErr('先勾选至少一行')
      return
    }
    const v = parseFloat(bulkCostValue.trim())
    if (isNaN(v) || v < 0) {
      setBulkCostErr('单价必须是非负数字（CNY）')
      return
    }
    setBulkCostBusy(true)
    try {
      const res = await api.remoteChannelMetaBulk({
        profile_id: selectedID,
        channel_ids: Array.from(selectedIDs),
        unit_price_cny: v,
      })
      setChannels(prev => prev.map(c => selectedIDs.has(c.id) ? { ...c, unit_price_cny: v } : c))
      alert(`已更新 ${res.updated} 条${res.failed.length ? `，${res.failed.length} 失败` : ''}`)
      setBulkCostOpen(false)
      setSelectedIDs(new Set())
    } catch (e: any) {
      setBulkCostErr(e?.message || String(e))
    } finally {
      setBulkCostBusy(false)
    }
  }

  const reloadPending = useCallback(async () => {
    if (!selectedID) {
      setPending([])
      return
    }
    try {
      const res = await api.remotePendingList(selectedID)
      setPending(res.items)
    } catch (e) {
      console.warn('pending list failed', e)
    }
  }, [selectedID])

  // Auto-poll the queue every 30s while the panel is open — status
  // transitions inside the scheduler tick (60s + retries) become visible
  // without a manual refresh.
  useEffect(() => {
    if (!selectedID) return
    void reloadPending()
    if (!pendingOpen) return
    const t = setInterval(() => { void reloadPending() }, 30000)
    return () => clearInterval(t)
  }, [selectedID, pendingOpen, reloadPending])

  // Sync pool-throttle inputs from the selected profile whenever the
  // pick changes (or profiles reload). Skip if the operator has an
  // in-flight edit — poolDirty is our "don't clobber" flag, cleared on
  // save or profile switch.
  useEffect(() => {
    const p = profiles.find(x => x.id === selectedID)
    if (!p) return
    if (poolDirty) return
    setPoolIntervalSec(String(p.pool_interval_sec ?? 60))
    setPoolBatchSize(String(p.pool_batch_size ?? 2))
    setPoolAutoMode(!!p.auto_mode)
    setPoolRPMBase(String(p.rpm_base ?? 150))
    setPoolRPMMin(String(p.rpm_min ?? 50))
    setPoolMsg(null)
  }, [selectedID, profiles, poolDirty])

  // Reset dirty flag when switching profiles — otherwise a stale edit
  // from profile A would prevent profile B's values from loading.
  useEffect(() => {
    setPoolDirty(false)
    setPoolMsg(null)
  }, [selectedID])

  const savePoolTuning = async () => {
    if (!selectedID) return
    const parsePool = (raw: string): number | undefined => {
      const t = raw.trim()
      if (t === '') return undefined
      const n = parseInt(t, 10)
      return isNaN(n) ? undefined : n
    }
    const interval = parsePool(poolIntervalSec)
    const batch = parsePool(poolBatchSize)
    const rpmBase = parsePool(poolRPMBase)
    const rpmMin = parsePool(poolRPMMin)
    setPoolSaving(true)
    setPoolMsg(null)
    try {
      const patch: Parameters<typeof api.remoteProfileUpdate>[1] = {
        auto_mode: poolAutoMode,
      }
      if (interval != null) patch.pool_interval_sec = interval
      if (batch != null) patch.pool_batch_size = batch
      if (rpmBase != null) patch.rpm_base = rpmBase
      if (rpmMin != null) patch.rpm_min = rpmMin
      await api.remoteProfileUpdate(selectedID, patch)
      setPoolDirty(false)
      await reloadProfiles()
      setPoolMsg({ ok: true, text: '已保存' })
    } catch (e: any) {
      setPoolMsg({ ok: false, text: e?.message || String(e) })
    } finally {
      setPoolSaving(false)
    }
  }

  // Studio policy: load + toggle. Loaded whenever a profile is picked
  // and after each enqueue so a newly-seen studio appears without a
  // manual refresh.
  const reloadStudioPolicies = useCallback(async () => {
    if (!selectedID) {
      setStudioPolicies([])
      return
    }
    try {
      const res = await api.remoteStudioPolicyList(selectedID)
      setStudioPolicies(res.items)
      setPolicyErr(null)
    } catch (e: any) {
      setPolicyErr(e?.message || String(e))
    }
  }, [selectedID])

  useEffect(() => { void reloadStudioPolicies() }, [reloadStudioPolicies, pending])

  const toggleStudioPolicy = async (studio: string, next: boolean) => {
    if (!selectedID) return
    setPolicyBusy(studio)
    setPolicyErr(null)
    try {
      await api.remoteStudioPolicyUpsert({
        profile_id: selectedID,
        studio,
        accepting_keys: next,
      })
      await reloadStudioPolicies()
    } catch (e: any) {
      setPolicyErr(e?.message || String(e))
    } finally {
      setPolicyBusy(null)
    }
  }

  const cancelPending = async (row: PendingKey) => {
    if (row.status !== 'pending' && row.status !== 'failed') return
    if (!window.confirm(`删除队列条目 (${row.key_masked})？只能删 pending/failed 的。`)) return
    try {
      await api.remotePendingDelete(row.id)
      await reloadPending()
    } catch (e: any) {
      alert('delete failed: ' + (e?.message || e))
    }
  }

  const openBatch = () => {
    // Preload from the selected profile's saved defaults. The user just
    // types the "middle" segment of the name — the final channel name
    // becomes  YYYYMMDD-<middle>-<key-tail>-<hash>.
    const p = profiles.find(x => x.id === selectedID)
    setBatchPrefix('')  // "middle" segment only; date auto-prepended before submit
    setBatchGroup((p?.default_group || '').trim() || 'default')
    setBatchTag('')
    setBatchPriority('')
    setBatchModels((p?.default_models || '').trim() || DEFAULT_ANTHROPIC_MODELS)
    setBatchInput('')
    setBatchErr(null)
    setBatchResults(null)
    setBatchOpen(true)
  }

  // todayYYYYMMDD returns the local-time date as a compact string, used
  // as the auto-prepended prefix of new channel names.
  const todayYYYYMMDD = () => {
    const d = new Date()
    const y = d.getFullYear()
    const m = String(d.getMonth() + 1).padStart(2, '0')
    const dd = String(d.getDate()).padStart(2, '0')
    return `${y}${m}${dd}`
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
    // Final name_prefix = <YYYYMMDD>-<user middle>. Backend then appends
    // -<key末8>-<sha8> per key, so the full channel name is
    // 20260707-mid-abcd1234-3f5c9e2a.
    const fullNamePrefix = todayYYYYMMDD() + '-' + batchPrefix.trim()

    setBatchBusy(true)
    try {
      if (batchQueue) {
        // Queue path: stage the batch into remote_pending_key. The scheduler
        // goroutine picks it up within 60s (or immediately via nudge) and
        // uploads either all at once (pool_size=0) or drip-style (pool_size>0).
        const poolSize = parseInt(batchPoolSize, 10)
        if (isNaN(poolSize) || poolSize < 0) {
          setBatchErr('pool size must be a non-negative integer')
          return
        }
        const res = await api.remotePendingEnqueue({
          profile_id: selectedID,
          name_prefix: fullNamePrefix,
          group: batchGroup.trim() || 'default',
          tag: batchTag.trim() || undefined,
          priority: batchLevelPriority,
          models: batchModels.trim(),
          pool_size: poolSize,
          items,
        })
        setBatchResults([])
        setBatchErr(null)
        alert(`已入队 ${res.inserted} 条${res.skipped ? `（${res.skipped} 条跳过 / 已存在）` : ''}${poolSize > 0 ? `，池大小 ${poolSize}` : '，立即上传'}`)
        void reloadPending()
        setBatchOpen(false)
        return
      }
      const res = await api.remoteChannelCreate({
        profile_id: selectedID,
        name_prefix: fullNamePrefix,
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
  // in UTC (Z suffix) so [today, today] = "since UTC 00:00 today, up to but
  // not including UTC 00:00 tomorrow", independent of browser timezone.
  const filteredChannels = useMemo(() => {
    const startTS = filterStart ? Math.floor(new Date(filterStart + 'T00:00:00Z').getTime() / 1000) : 0
    const endTS = filterEnd ? Math.floor(new Date(filterEnd + 'T00:00:00Z').getTime() / 1000) + 86400 : 0
    if (!startTS && !endTS) return channels
    return channels.filter(c => {
      const t = c.created_time || 0
      if (startTS && t < startTS) return false
      if (endTS && t >= endTS) return false
      return true
    })
  }, [channels, filterStart, filterEnd])

  // Derived selection state — computed after filteredChannels so the
  // header checkbox reflects the currently visible slice.
  const someVisibleSelected = filteredChannels.some(c => selectedIDs.has(c.id))
  const allVisibleSelected = filteredChannels.length > 0 && filteredChannels.every(c => selectedIDs.has(c.id))

  const summary = useMemo(() => {
    const totalUsedUSD = filteredChannels.reduce((s, c) => s + usdFromQuota(c.used_quota), 0)
    const enabled = filteredChannels.filter(c => c.status === 1).length
    const disabled = filteredChannels.length - enabled
    // Realtime RPM / TPM come from the profile-wide /stat/summary endpoint
    // (one remote call regardless of channel count), NOT from a per-row
    // fan-out. Falls back to 0 when the summary hasn't loaded yet.
    return {
      count: filteredChannels.length,
      totalUsedUSD,
      enabled,
      disabled,
      totalRpm: statSummary?.rpm ?? 0,
      totalTpm: statSummary?.tpm ?? 0,
    }
  }, [filteredChannels, statSummary])

  const exportCSV = () => {
    if (filteredChannels.length === 0) return
    const header = ['ID', 'Name', 'Type', 'Group', 'Tag', 'Priority', 'Used USD', 'Δ USD (since baseline)', '额度 USD', '单价 CNY', 'Status', 'Created', 'Note']
    const escape = (v: unknown) => {
      const s = v == null ? '' : String(v)
      return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s
    }
    const rows = filteredChannels.map(c => {
      const usedUSD = usdFromQuota(c.used_quota)
      const baseline = snapshotBaseline[c.id]
      const deltaUSD = baseline ? usdFromQuota(c.used_quota - baseline.used_quota) : null
      return [
        c.id, c.name, c.type, c.group, c.tag, c.priority,
        usedUSD.toFixed(4),
        deltaUSD != null ? deltaUSD.toFixed(4) : '',
        c.quota_usd != null ? c.quota_usd.toFixed(2) : '',
        c.unit_price_cny != null ? c.unit_price_cny.toFixed(4) : '',
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

  // Top-of-page actions: profile-level ops only. Everything else moved
  // into the channels-table toolbar so it lives next to the data it
  // affects.
  const actions = (
    <div className="flex items-center gap-2 flex-wrap">
      <button
        onClick={openCreate}
        className="border border-gray-300 text-gray-700 rounded-md px-3 py-1.5 text-xs hover:bg-gray-50"
      >
        + New profile
      </button>
    </div>
  )

  // Channel-table toolbar. Rendered above the table, right-aligned so
  // the numbers stay dominant. Filters + row-selection actions + fetch.
  const tableToolbar = (
    <div className="flex items-center gap-2 flex-wrap justify-end">
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
        onClick={openBulkCost}
        disabled={selectedIDs.size === 0}
        className="border border-amber-500 text-amber-700 rounded-md px-3 py-1.5 text-xs hover:bg-amber-50 disabled:opacity-40"
        title="将勾选行的单价 (CNY) 批量写到本地"
      >
        批量设成本
        {selectedIDs.size > 0 && <span className="ml-1 text-amber-600 font-medium">({selectedIDs.size})</span>}
      </button>
      <button
        onClick={openBulkPrio}
        disabled={selectedIDs.size === 0}
        className="border border-indigo-500 text-indigo-700 rounded-md px-3 py-1.5 text-xs hover:bg-indigo-50 disabled:opacity-40"
        title="将勾选行的优先级批量改到远端"
      >
        批量改优先级
        {selectedIDs.size > 0 && <span className="ml-1 text-indigo-600 font-medium">({selectedIDs.size})</span>}
      </button>
      <button
        onClick={openBatch}
        disabled={!selectedID}
        className="border border-emerald-600 text-emerald-700 rounded-md px-3 py-1.5 text-xs hover:bg-emerald-50 disabled:opacity-40"
      >
        + 批量上 key
      </button>
      <div className="inline-flex items-center gap-1 border border-rose-300 rounded-md">
        <select
          value={errWindowSec}
          onChange={e => setErrWindowSec(parseInt(e.target.value, 10))}
          className="text-xs px-2 py-1.5 bg-white text-rose-700 border-r border-rose-200 focus:outline-none rounded-l-md"
          title="错误率统计的时间窗口"
        >
          <option value={5 * 60}>过去 5 分钟</option>
          <option value={15 * 60}>过去 15 分钟</option>
          <option value={60 * 60}>过去 1 小时</option>
          <option value={6 * 60 * 60}>过去 6 小时</option>
          <option value={24 * 60 * 60}>过去 24 小时</option>
        </select>
        <button
          onClick={loadErrorRates}
          disabled={!selectedID || errRateLoading || channels.length === 0}
          className="text-rose-600 px-2.5 py-1.5 text-xs hover:bg-rose-50 disabled:opacity-40 rounded-r-md"
          title="用选中的时间窗口拉每个渠道的成功/错误数，计算错误率。5 分钟缓存。"
        >
          {errRateLoading ? '加载中…' : '加载错误率'}
        </button>
      </div>
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

        {/* Upload queue (drip pool). Collapsed by default; expands into a
            table when the operator wants to see what's staged. Pool
            throttle knobs live in the header so they're right next to
            the queue they control. */}
        {selectedID && (
          <section className="bg-white border border-gray-200 rounded-xl overflow-hidden">
            <button
              type="button"
              onClick={() => setPendingOpen(v => !v)}
              className="w-full flex items-center justify-between px-4 py-2.5 border-b border-gray-100 hover:bg-gray-50"
            >
              <div className="text-sm font-semibold text-gray-900 flex items-center gap-2">
                上 Key 队列
                <span className="text-[10px] text-gray-400 uppercase tracking-wider">
                  {pending.filter(p => p.status === 'pending').length} pending ·{' '}
                  {pending.filter(p => p.status === 'active').length} active ·{' '}
                  {pending.filter(p => p.status === 'used').length} used ·{' '}
                  <span className={pending.filter(p => p.status === 'failed').length > 0 ? 'text-rose-600' : ''}>
                    {pending.filter(p => p.status === 'failed').length} failed
                  </span>
                </span>
              </div>
              <span className="text-gray-400">{pendingOpen ? '▾' : '▸'}</span>
            </button>
            {/* Pool 节流 — 前一批 key 死光后，下一 tick 从 pending 里
                按 FIFO 取 N 个上传，priority 自动累加。仅对 pool 模式
                (pool_size > 0) 的行生效；pool_size=0 的立即上传不受影响。
                自动模式打开时"每次上"变成上限，实际值 =
                min(cap, ceil(rpm / rpm_base))，rpm < rpm_min 时暂停上传。 */}
            <div
              className="flex flex-wrap items-center gap-3 px-4 py-2.5 border-b border-gray-100 bg-gray-50/50"
              onClick={e => e.stopPropagation()}
            >
              <div className="text-[10px] uppercase tracking-wider text-gray-500 font-medium">
                Pool 节流
              </div>
              <label className="flex items-center gap-1.5 text-xs text-gray-700">
                检查间隔
                <input
                  value={poolIntervalSec}
                  onChange={e => { setPoolIntervalSec(e.target.value); setPoolDirty(true) }}
                  inputMode="numeric"
                  className="w-16 border border-gray-300 rounded px-1.5 py-0.5 text-xs tabular-nums text-right focus:outline-none focus:border-gray-900"
                />
                <span className="text-[10px] text-gray-400">秒</span>
              </label>
              <label className="flex items-center gap-1.5 text-xs text-gray-700">
                {poolAutoMode ? '上限' : '每次上'}
                <input
                  value={poolBatchSize}
                  onChange={e => { setPoolBatchSize(e.target.value); setPoolDirty(true) }}
                  inputMode="numeric"
                  className="w-14 border border-gray-300 rounded px-1.5 py-0.5 text-xs tabular-nums text-right focus:outline-none focus:border-gray-900"
                />
                <span className="text-[10px] text-gray-400">个 key</span>
              </label>
              <label className="flex items-center gap-1.5 text-xs text-gray-700 border-l border-gray-200 pl-3 ml-1">
                <input
                  type="checkbox"
                  checked={poolAutoMode}
                  onChange={e => { setPoolAutoMode(e.target.checked); setPoolDirty(true) }}
                />
                自动模式
              </label>
              {poolAutoMode && (
                <>
                  <label className="flex items-center gap-1.5 text-xs text-gray-700">
                    RPM/key
                    <input
                      value={poolRPMBase}
                      onChange={e => { setPoolRPMBase(e.target.value); setPoolDirty(true) }}
                      inputMode="numeric"
                      className="w-16 border border-gray-300 rounded px-1.5 py-0.5 text-xs tabular-nums text-right focus:outline-none focus:border-gray-900"
                    />
                  </label>
                  <label className="flex items-center gap-1.5 text-xs text-gray-700">
                    低于
                    <input
                      value={poolRPMMin}
                      onChange={e => { setPoolRPMMin(e.target.value); setPoolDirty(true) }}
                      inputMode="numeric"
                      className="w-14 border border-gray-300 rounded px-1.5 py-0.5 text-xs tabular-nums text-right focus:outline-none focus:border-gray-900"
                    />
                    <span className="text-[10px] text-gray-400">RPM 停</span>
                  </label>
                </>
              )}
              <button
                type="button"
                onClick={savePoolTuning}
                disabled={poolSaving || !poolDirty}
                className="bg-gray-900 text-white rounded px-2 py-0.5 text-xs hover:opacity-85 disabled:opacity-40"
              >
                {poolSaving ? '保存中…' : '保存'}
              </button>
              {poolMsg && (
                <span className={`text-[11px] ${poolMsg.ok ? 'text-emerald-600' : 'text-rose-600'}`}>
                  {poolMsg.text}
                </span>
              )}
              <span className="text-[10px] text-gray-400 ml-auto">
                Priority = 存活最高 + 1，逐条累加
              </span>
            </div>
            {pendingOpen && pending.length === 0 && (
              <div className="px-4 py-4 text-xs text-gray-400">队列为空</div>
            )}
            {pendingOpen && pending.length > 0 && (
              <div className="overflow-x-auto">
                <table className="w-full text-xs">
                  <thead className="bg-gray-50 border-b border-gray-100 text-gray-500">
                    <tr>
                      <th className="px-3 py-2 text-left font-medium">ID</th>
                      <th className="px-3 py-2 text-left font-medium">Key</th>
                      <th className="px-3 py-2 text-left font-medium">Status</th>
                      <th className="px-3 py-2 text-right font-medium">Pool</th>
                      <th className="px-3 py-2 text-right font-medium">Quota</th>
                      <th className="px-3 py-2 text-left font-medium">Prefix</th>
                      <th className="px-3 py-2 text-right font-medium">Channel</th>
                      <th className="px-3 py-2 text-right font-medium">Try</th>
                      <th className="px-3 py-2 text-left font-medium">Error / 更新</th>
                      <th className="px-3 py-2 text-left font-medium">操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {pending.map(row => (
                      <tr key={row.id} className="border-b border-gray-100 hover:bg-gray-50">
                        <td className="px-3 py-2 tabular-nums">{row.id}</td>
                        <td className="px-3 py-2 font-mono text-[11px]">{row.key_masked}</td>
                        <td className="px-3 py-2">
                          <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] ${
                            row.status === 'active' ? 'bg-emerald-100 text-emerald-800'
                              : row.status === 'used' ? 'bg-gray-100 text-gray-500'
                              : row.status === 'failed' ? 'bg-red-100 text-red-700'
                              : 'bg-blue-100 text-blue-700'
                          }`}>{row.status}</span>
                        </td>
                        <td className="px-3 py-2 tabular-nums text-right">
                          {row.pool_size === 0 ? <span className="text-gray-400">立即</span> : row.pool_size}
                        </td>
                        <td className="px-3 py-2 tabular-nums text-right">
                          {row.quota_usd > 0 ? '$' + row.quota_usd.toFixed(2) : '—'}
                        </td>
                        <td className="px-3 py-2 text-gray-500">{row.name_prefix || '—'}</td>
                        <td className="px-3 py-2 tabular-nums text-right">
                          {row.remote_channel_id > 0 ? row.remote_channel_id : '—'}
                        </td>
                        <td className="px-3 py-2 tabular-nums text-right">{row.attempts}</td>
                        <td className="px-3 py-2 text-[10px] text-gray-500 max-w-[240px] truncate" title={row.failed_reason || fmtTime(row.updated_at)}>
                          {row.failed_reason
                            ? <span className="text-rose-600">{row.failed_reason}</span>
                            : fmtTime(row.updated_at)}
                        </td>
                        <td className="px-3 py-2">
                          {(row.status === 'pending' || row.status === 'failed') && (
                            <button
                              onClick={() => void cancelPending(row)}
                              className="text-[10px] text-rose-500 hover:text-rose-700"
                            >
                              删除
                            </button>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        )}

        {/* Studio policy — flip whether each studio may enqueue new keys
            on this profile. Rows come from any explicit policy row + any
            studio that has ever appeared as remote_pending_key.tag. */}
        {selectedID && studioPolicies.length > 0 && (
          <section className="bg-white border border-gray-200 rounded-xl overflow-hidden">
            <div className="px-4 py-2.5 border-b border-gray-100 flex items-center justify-between">
              <div>
                <div className="text-sm font-semibold text-gray-900">工作室上 Key 策略</div>
                <div className="text-[11px] text-gray-400 mt-0.5">
                  关掉后，对应工作室提交批量 Key 时会被拒绝。默认接收。
                </div>
              </div>
              {policyErr && <span className="text-[11px] text-rose-600">{policyErr}</span>}
            </div>
            <div className="overflow-x-auto">
              <table className="w-full text-xs">
                <thead className="bg-gray-50 border-b border-gray-100 text-gray-500">
                  <tr>
                    <th className="px-3 py-2 text-left font-medium">Studio</th>
                    <th className="px-3 py-2 text-left font-medium">状态</th>
                    <th className="px-3 py-2 text-left font-medium">最后调整</th>
                    <th className="px-3 py-2 text-right font-medium">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {studioPolicies.map(p => (
                    <tr key={p.studio} className="border-b border-gray-100 hover:bg-gray-50">
                      <td className="px-3 py-2 font-mono">{p.studio}</td>
                      <td className="px-3 py-2">
                        {p.accepting_keys ? (
                          <span className="inline-block px-2 py-0.5 rounded-full text-[10px] bg-emerald-100 text-emerald-800">
                            接收{p.has_row ? '' : '（默认）'}
                          </span>
                        ) : (
                          <span className="inline-block px-2 py-0.5 rounded-full text-[10px] bg-rose-100 text-rose-700">
                            拒绝
                          </span>
                        )}
                      </td>
                      <td className="px-3 py-2 text-[10px] text-gray-500">
                        {p.has_row ? fmtTime(p.updated_at) : '—'}
                      </td>
                      <td className="px-3 py-2 text-right">
                        <button
                          type="button"
                          onClick={() => void toggleStudioPolicy(p.studio, !p.accepting_keys)}
                          disabled={policyBusy === p.studio}
                          className={`text-[11px] px-2 py-0.5 rounded border disabled:opacity-40 ${
                            p.accepting_keys
                              ? 'border-rose-300 text-rose-600 hover:bg-rose-50'
                              : 'border-emerald-300 text-emerald-700 hover:bg-emerald-50'
                          }`}
                        >
                          {policyBusy === p.studio ? '…' : p.accepting_keys ? '关闭上 Key' : '开放上 Key'}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        )}
        {selectedID != null && tableToolbar}
        {channels.length > 0 && meta && (
          <>
            <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
              <MetricCard label="渠道总数" value={String(summary.count)} />
              <MetricCard label="启用" value={String(summary.enabled)} color="text-emerald-600" />
              <MetricCard label="禁用" value={String(summary.disabled)} color="text-rose-600" />
              <MetricCard label="累计已用" value={'$' + summary.totalUsedUSD.toFixed(2)} color="text-blue-600" />
              <MetricCard
                label="实时 RPM"
                value={summary.totalRpm.toLocaleString()}
                color={summary.totalRpm > 0 ? 'text-emerald-600' : 'text-gray-400'}
              />
              <MetricCard
                label="实时 TPM"
                value={summary.totalTpm.toLocaleString()}
                color={summary.totalTpm > 0 ? 'text-emerald-600' : 'text-gray-400'}
              />
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
                      <th className="px-2 py-2 text-center font-medium">
                        <input
                          type="checkbox"
                          checked={allVisibleSelected}
                          ref={el => { if (el) el.indeterminate = someVisibleSelected && !allVisibleSelected }}
                          onChange={e => toggleAllSelected(filteredChannels, e.target.checked)}
                          title="全选当前视图"
                        />
                      </th>
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
                      <th className="px-3 py-2 text-right font-medium" title="本地维护的上游成本, CNY / USD 额度">单价 CNY</th>
                      <th className="px-3 py-2 text-left font-medium">状态</th>
                      <th className="px-3 py-2 text-right font-medium" title="过去 60 秒的错误率 = err_rpm / (rpm + err_rpm)。点击单元格查看错误类型分桶。">错误率</th>
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
                          <tr className={`border-b border-gray-100 hover:bg-gray-50 ${selectedIDs.has(c.id) ? 'bg-blue-50/40' : ''}`}>
                            <td className="px-2 py-2 text-center">
                              <input
                                type="checkbox"
                                checked={selectedIDs.has(c.id)}
                                onChange={e => toggleRowSelected(c.id, e.target.checked)}
                              />
                            </td>
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
                              {c.unit_price_cny != null ? (
                                <span className="text-gray-700">¥{c.unit_price_cny.toFixed(4)}</span>
                              ) : <span className="text-gray-300">—</span>}
                            </td>
                            <td className="px-3 py-2">
                              <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] ${STATUS_CLS[c.status] ?? 'bg-gray-100 text-gray-600'}`}>
                                {STATUS_LABEL[c.status] ?? c.status}
                              </span>
                            </td>
                            <td className="px-3 py-2 tabular-nums text-right">
                              <ErrorRateCell
                                stat={errStats[c.id]}
                                onOpen={() => setBreakdownFor({ id: c.id, name: c.name })}
                              />
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
                              <td colSpan={16} className="px-4 py-3">
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

      {/* Modal: bulk set unit_price_cny across the selected rows.
          Purely local — never touches the remote. */}
      {bulkCostOpen && (
        <div
          className="fixed inset-0 z-50 bg-black/40 flex items-center justify-center p-4"
          onClick={() => !bulkCostBusy && setBulkCostOpen(false)}
        >
          <div
            className="bg-white rounded-lg shadow-xl w-full max-w-md p-5"
            onClick={e => e.stopPropagation()}
          >
            <h3 className="text-sm font-semibold text-gray-900 mb-1">批量设成本</h3>
            <p className="text-xs text-gray-500 mb-4">
              将 <span className="font-medium text-gray-900">{selectedIDs.size}</span> 个选中渠道的单价改为下面填写的值 (CNY / 每 USD 上游额度)。仅本地存储，不写远端。
              <br />
              <span className="text-gray-400">下游折扣按 profile 每日单独在 Profit 页面配置。</span>
            </p>
            <Field label="单价 (CNY)">
              <input
                type="number"
                step="0.001"
                min="0"
                value={bulkCostValue}
                onChange={e => setBulkCostValue(e.target.value)}
                placeholder="例如 4.3"
                autoFocus
                className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm tabular-nums focus:outline-none focus:border-gray-900"
              />
            </Field>
            {bulkCostErr && <p className="mt-2 text-xs text-rose-600">{bulkCostErr}</p>}
            <div className="mt-5 flex justify-end gap-2">
              <button
                onClick={() => setBulkCostOpen(false)}
                disabled={bulkCostBusy}
                className="border border-gray-300 rounded-md px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50"
              >
                取消
              </button>
              <button
                onClick={submitBulkCost}
                disabled={bulkCostBusy}
                className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-sm hover:opacity-85 disabled:opacity-50"
              >
                {bulkCostBusy ? '保存中…' : `保存 (${selectedIDs.size})`}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Modal: bulk update priority. Same three modes as the
          batch-upload priority selector. Sequential (desc/asc) walks
          selected channels by ±1 from the base, ordered by channel_id
          ascending so the result is stable across sessions. */}
      {bulkPrioOpen && (
        <div
          className="fixed inset-0 z-50 bg-black/40 flex items-center justify-center p-4"
          onClick={() => !bulkPrioBusy && setBulkPrioOpen(false)}
        >
          <div
            className="bg-white rounded-lg shadow-xl w-full max-w-md p-5"
            onClick={e => e.stopPropagation()}
          >
            <h3 className="text-sm font-semibold text-gray-900 mb-1">批量改优先级</h3>
            <p className="text-xs text-gray-500 mb-4">
              选中 <span className="font-medium text-gray-900">{selectedIDs.size}</span> 个渠道，改到远端。
              <br />
              <span className="text-gray-400">顺序模式下按 channel_id 升序依次分配，priority 最小 1（负值会被夹到 1）。</span>
            </p>
            <div className="space-y-3">
              <Field label={`起始优先级${bulkPrioMode === 'desc' ? '（base − i）' : bulkPrioMode === 'asc' ? '（base + i）' : ''}`}>
                <div className="flex gap-1">
                  <input
                    type="number"
                    step="1"
                    min="1"
                    value={bulkPrioValue}
                    onChange={e => setBulkPrioValue(e.target.value)}
                    placeholder={bulkPrioMode === 'same' ? '例如 1001' : '起始 base'}
                    autoFocus
                    className="flex-1 border border-gray-300 rounded-md px-2 py-1.5 text-sm tabular-nums focus:outline-none focus:border-gray-900"
                  />
                  <select
                    value={bulkPrioMode}
                    onChange={e => setBulkPrioMode(e.target.value as 'same' | 'desc' | 'asc')}
                    className="border border-gray-300 rounded-md px-2 py-1.5 text-sm bg-white focus:outline-none focus:border-gray-900"
                    title="统一 = 所有 key 同一值；顺序 = 每个 key 依次递减/递增"
                  >
                    <option value="same">统一</option>
                    <option value="desc">顺序 ↓</option>
                    <option value="asc">顺序 ↑</option>
                  </select>
                </div>
              </Field>
              {bulkPrioProgress && (
                <div className="text-xs text-gray-500">
                  进度: {bulkPrioProgress.done} / {bulkPrioProgress.total}
                  <div className="mt-1 h-1 bg-gray-100 rounded overflow-hidden">
                    <div
                      className="h-full bg-indigo-500"
                      style={{ width: `${(bulkPrioProgress.done / Math.max(1, bulkPrioProgress.total)) * 100}%` }}
                    />
                  </div>
                </div>
              )}
              {bulkPrioErr && <p className="text-xs text-rose-600">{bulkPrioErr}</p>}
            </div>
            <div className="mt-5 flex justify-end gap-2">
              <button
                onClick={() => setBulkPrioOpen(false)}
                disabled={bulkPrioBusy}
                className="border border-gray-300 rounded-md px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50"
              >
                取消
              </button>
              <button
                onClick={submitBulkPrio}
                disabled={bulkPrioBusy}
                className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-sm hover:opacity-85 disabled:opacity-50"
              >
                {bulkPrioBusy ? `保存中… ${bulkPrioProgress?.done ?? 0}/${bulkPrioProgress?.total ?? 0}` : `保存 (${selectedIDs.size})`}
              </button>
            </div>
          </div>
        </div>
      )}

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
              <Field label={`名字中间段（最终 = ${todayYYYYMMDD()}-<你填>-<key末8>-<hash8>）`}>
                <div className="flex items-center gap-1.5">
                  <span className="text-[11px] text-gray-400 font-mono whitespace-nowrap">{todayYYYYMMDD()}-</span>
                  <input
                    value={batchPrefix}
                    onChange={e => setBatchPrefix(e.target.value)}
                    placeholder="例如 pipi-a"
                    className="flex-1 border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                  />
                </div>
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

            {/* Queue mode toggle. When on, keys stage into
                remote_pending_key and the scheduler goroutine uploads
                them. Immediate mode (default) is the original
                synchronous path. */}
            <div className="mb-3 rounded-md border border-gray-200 bg-gray-50 p-3 space-y-2">
              <label className="flex items-center gap-2 text-xs text-gray-700 cursor-pointer">
                <input
                  type="checkbox"
                  checked={batchQueue}
                  onChange={e => setBatchQueue(e.target.checked)}
                />
                使用队列（定时上传 / drip 池）
              </label>
              {batchQueue && (
                <div className="pl-6 space-y-1.5">
                  <label className="block text-[11px] text-gray-500">
                    Pool size（<span className="text-gray-400">0 = 全部立即上；N = 一批 N 个，全部用完了再上下一批 N 个</span>）
                  </label>
                  <input
                    type="number"
                    min="0"
                    max="100"
                    value={batchPoolSize}
                    onChange={e => setBatchPoolSize(e.target.value)}
                    className="w-24 border border-gray-300 rounded-md px-2 py-1.5 text-sm tabular-nums focus:outline-none focus:border-gray-900"
                  />
                  <p className="text-[10px] text-gray-400">
                    队列由后台 goroutine 每 20s 扫描一次；只有当整批 key 都被 remote 自动禁用（status ≠ 1），才会一起上下一批。上传失败会重试 3 次。
                  </p>
                </div>
              )}
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
                  placeholder="例如 newapi-remote"
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                />
              </Field>
              <Field label="Host">
                <input
                  value={formHost}
                  onChange={e => setFormHost(e.target.value)}
                  placeholder={editingID === 0 ? 'http://example.com' : '留空 = 保持原 host 不变'}
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

              {/* Defaults preloaded into the batch-upload modal so the
                  operator only has to type the "middle" segment of the
                  channel name and pick keys. */}
              <div className="pt-2 border-t border-gray-100">
                <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-2">
                  批量上传默认值
                </div>
                <Field label="默认 Group">
                  <input
                    value={formDefaultGroup}
                    onChange={e => setFormDefaultGroup(e.target.value)}
                    placeholder="例如 default"
                    className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                  />
                </Field>
                <Field label="默认 Models (逗号分隔)">
                  <textarea
                    value={formDefaultModels}
                    onChange={e => setFormDefaultModels(e.target.value)}
                    rows={3}
                    placeholder="claude-opus-4-7,claude-sonnet-4-6,..."
                    className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-[11px] font-mono focus:outline-none focus:border-gray-900"
                  />
                </Field>
              </div>
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
      {breakdownFor && selectedID && (
        <BreakdownModal
          profileID={selectedID}
          channel={breakdownFor}
          windowSec={errWindowSec}
          onClose={() => setBreakdownFor(null)}
        />
      )}
    </Layout>
  )
}

// ErrorRateCell renders "err / (err+ok)" as a percentage in a coloured
// pill. Clicking opens the breakdown modal for the same channel.
// stat=undefined ⇒ user hasn't clicked "加载错误率" yet.
function ErrorRateCell({
  stat,
  onOpen,
}: {
  stat: { success: number; errors: number } | undefined
  onOpen: () => void
}) {
  if (!stat) return <span className="text-gray-300">—</span>
  const total = stat.success + stat.errors
  if (total === 0) return <span className="text-gray-300" title="窗口内无请求">0</span>
  const rate = stat.errors / total
  const pct = rate * 100
  const cls =
    pct >= 20 ? 'bg-rose-100 text-rose-700 border-rose-200'
    : pct >= 5 ? 'bg-amber-100 text-amber-700 border-amber-200'
    : pct > 0  ? 'bg-emerald-50 text-emerald-700 border-emerald-200'
    : 'bg-gray-50 text-gray-500 border-gray-200'
  return (
    <button
      onClick={onOpen}
      className={`inline-flex items-center px-2 py-0.5 rounded-md border text-[11px] tabular-nums hover:opacity-80 ${cls}`}
      title={`${stat.errors} 错误 / ${total} 总请求 · 点击查看类型分桶`}
    >
      {pct.toFixed(pct < 1 ? 2 : 1)}%
    </button>
  )
}

// BreakdownModal fetches (error_type, status_code) buckets for one
// channel over the same window as the summary cell. Backend already
// caches for 5min so this is cheap even on repeated opens.
function BreakdownModal({
  profileID,
  channel,
  windowSec,
  onClose,
}: {
  profileID: number
  channel: { id: number; name: string }
  windowSec: number
  onClose: () => void
}) {
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState<string | null>(null)
  const [data, setData] = useState<{
    total: number
    buckets: Array<{ error_type: string; status_code: number; count: number }>
    sample_size?: number
  } | null>(null)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      setLoading(true)
      setErr(null)
      try {
        const res = await api.remoteChannelErrors(profileID, channel.id, windowSec)
        if (cancelled) return
        setData(res)
      } catch (e: any) {
        if (cancelled) return
        setErr(e?.message || String(e))
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => { cancelled = true }
  }, [profileID, channel.id, windowSec])

  const humanWindow = windowSec < 3600
    ? `${Math.round(windowSec / 60)} 分钟`
    : `${(windowSec / 3600).toFixed(windowSec % 3600 === 0 ? 0 : 1)} 小时`

  return (
    <div className="fixed inset-0 z-50 bg-black/40 flex items-center justify-center p-4" onClick={onClose}>
      <div className="bg-white rounded-xl shadow-xl w-full max-w-lg" onClick={e => e.stopPropagation()}>
        <div className="px-4 py-3 border-b border-gray-100 flex items-start justify-between">
          <div>
            <div className="text-sm font-medium text-gray-900">
              渠道 <span className="font-mono text-xs">{channel.name}</span> · 错误类型分桶
            </div>
            <div className="text-[11px] text-gray-500 mt-0.5">过去 {humanWindow}</div>
          </div>
          <button onClick={onClose} className="text-gray-400 hover:text-gray-700 text-lg leading-none">×</button>
        </div>
        <div className="p-4 max-h-[60vh] overflow-y-auto">
          {loading && <div className="text-sm text-gray-500">加载中…</div>}
          {err && <div className="text-sm text-rose-600">{err}</div>}
          {data && (
            <>
              <div className="mb-3 text-sm text-gray-700">
                共 <span className="font-semibold text-rose-700">{data.total}</span> 条错误日志
                {data.sample_size !== undefined && data.sample_size < data.total && (
                  <span className="text-[11px] text-gray-400 ml-2">
                    （分桶基于最新 {data.sample_size} 条采样）
                  </span>
                )}
              </div>
              {data.buckets.length === 0 ? (
                <div className="text-sm text-gray-500">窗口内没有错误</div>
              ) : (
                <table className="w-full text-xs">
                  <thead className="bg-gray-50 text-gray-500">
                    <tr>
                      <th className="text-left px-3 py-2 font-medium">错误类型</th>
                      <th className="text-left px-3 py-2 font-medium">状态码</th>
                      <th className="text-right px-3 py-2 font-medium">数量</th>
                      <th className="text-right px-3 py-2 font-medium">占比</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.buckets.map((b, i) => {
                      const share = data.sample_size ? (b.count / data.sample_size) * 100 : 0
                      return (
                        <tr key={i} className="border-t border-gray-100">
                          <td className="px-3 py-2 font-mono text-[11px]">{b.error_type || '—'}</td>
                          <td className="px-3 py-2 tabular-nums">{b.status_code || '—'}</td>
                          <td className="px-3 py-2 tabular-nums text-right">{b.count}</td>
                          <td className="px-3 py-2 tabular-nums text-right text-gray-500">
                            {share > 0 ? share.toFixed(1) + '%' : '—'}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              )}
            </>
          )}
        </div>
      </div>
    </div>
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
