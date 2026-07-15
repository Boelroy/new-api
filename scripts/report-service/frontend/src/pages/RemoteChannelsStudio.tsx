import { useCallback, useEffect, useState } from 'react'
import Layout from '../components/Layout'
import { api, type PendingKey, type RemoteChannel, type RemoteProfile } from '../api'

// Studio-operator slim view of Remote Channels. Deliberately does NOT
// share code with the super_admin RemoteChannels.tsx — that page has
// 1800+ lines of channel-table + profile CRUD + bulk-price editor
// surface that operators must not see. Isolating the two shapes here
// means:
//   • operator UI can't accidentally render a URL / user_id / priority
//     control if a future refactor forgets a role gate
//   • RemoteChannels.tsx can keep evolving without threading role flags
//     through its state machine
//
// The backend enforces the actual permissions (profile list strips
// host / user_id / has_token, pending list filters by tag = studio,
// enqueue overwrites tag + zeroes priority). This file is the shape
// contract for the operator, not the security boundary.

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

const DEFAULT_GEMINI_MODELS = [
  'gemini-2.5-flash',
  'gemini-2.5-flash-image',
  'gemini-2.5-flash-preview-tts',
  'gemini-2.5-pro',
  'gemini-3-flash-preview',
  'gemini-3-pro-image',
  'gemini-3-pro-image-preview',
  'gemini-3.1-flash-image',
  'gemini-3.1-flash-image-preview',
  'gemini-3.1-flash-lite',
  'gemini-3.1-flash-lite-preview',
  'gemini-3.1-pro-preview',
  'gemini-3.1-pro-preview-customtools',
  'gemini-3.5-flash',
].join(',')

// Channel type integers from newapi's constant/channel.go — 14 = Anthropic,
// 24 = Gemini. Sent through remotePendingEnqueue.type on both the pool
// and immediate lanes. Duplicated (intentionally, see file header) from
// RemoteChannels.tsx to keep this operator surface standalone.
const CHANNEL_TYPE_ANTHROPIC = 14
const CHANNEL_TYPE_GEMINI = 24

type PresetID = 'anthropic' | 'gemini'
type PresetSpec = {
  id: PresetID
  label: string
  type: number
  fallbackModels: string
  fallbackGroup: string
  profileGroupField: 'default_group' | 'default_gemini_group'
  profileModelsField: 'default_models' | 'default_gemini_models'
}
const CHANNEL_TYPE_PRESETS: PresetSpec[] = [
  { id: 'anthropic', label: 'Anthropic (Claude)', type: CHANNEL_TYPE_ANTHROPIC, fallbackModels: DEFAULT_ANTHROPIC_MODELS, fallbackGroup: 'default', profileGroupField: 'default_group',        profileModelsField: 'default_models' },
  { id: 'gemini',    label: 'Gemini',              type: CHANNEL_TYPE_GEMINI,    fallbackModels: DEFAULT_GEMINI_MODELS,    fallbackGroup: 'gemini',  profileGroupField: 'default_gemini_group', profileModelsField: 'default_gemini_models' },
]

function resolvePresetGroup(preset: PresetSpec, profile: RemoteProfile | undefined): string {
  const fromProfile = (profile?.[preset.profileGroupField] || '').trim()
  return fromProfile || preset.fallbackGroup
}
function resolvePresetModels(preset: PresetSpec, profile: RemoteProfile | undefined): string {
  const fromProfile = (profile?.[preset.profileModelsField] || '').trim()
  return fromProfile || preset.fallbackModels
}

function todayYYYYMMDD() {
  const d = new Date()
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  return `${y}${m}${dd}`
}

function fmtTime(epoch: number) {
  if (!epoch) return '—'
  return new Date(epoch * 1000).toLocaleString()
}

const STATUS_LABEL: Record<PendingKey['status'], string> = {
  pending: '待上传',
  active:  '已上传',
  used:    '已消耗',
  failed:  '失败',
}
const STATUS_CLS: Record<PendingKey['status'], string> = {
  pending: 'bg-amber-100 text-amber-700',
  active:  'bg-emerald-100 text-emerald-800',
  used:    'bg-gray-100 text-gray-600',
  failed:  'bg-rose-100 text-rose-700',
}

// Remote-channel status codes come from newapi's channel model:
//   1 = enabled, 2 = manually disabled, 3 = auto-disabled (upstream error).
// The studio operator sees only the badge; the underlying number is not
// exposed. Any unknown value falls back to a neutral gray label.
function channelStatusLabel(status: number): string {
  if (status === 1) return '启用'
  if (status === 2) return '手动禁用'
  if (status === 3) return '自动禁用'
  return `状态 ${status}`
}
function channelStatusCls(status: number): string {
  if (status === 1) return 'bg-emerald-100 text-emerald-800'
  if (status === 3) return 'bg-rose-100 text-rose-700'
  return 'bg-gray-100 text-gray-600'
}

function UsagePct({ used, quota }: { used: number; quota: number }) {
  if (!quota || quota <= 0) return <span className="text-gray-300 text-[11px]">—</span>
  const pct = Math.min(100, (used / quota) * 100)
  return (
    <div className="flex items-center gap-2 justify-end">
      <div className="w-16 h-1 bg-gray-100 rounded overflow-hidden">
        <div
          className={`h-full ${pct >= 100 ? 'bg-rose-500' : pct >= 80 ? 'bg-amber-500' : 'bg-emerald-500'}`}
          style={{ width: pct + '%' }}
        />
      </div>
      <span className="text-[10px] tabular-nums text-gray-500 w-8 text-right">{pct.toFixed(0)}%</span>
    </div>
  )
}

export default function RemoteChannelsStudio() {
  const [profiles, setProfiles] = useState<RemoteProfile[]>([])
  const [selectedID, setSelectedID] = useState<number | null>(null)
  const [loadingProfiles, setLoadingProfiles] = useState(true)
  const [pending, setPending] = useState<PendingKey[]>([])
  // Live mirror of remote channels the operator uploaded — server-side
  // filters this to (studio, uploaded_by) so we don't have to guard here.
  const [channels, setChannels] = useState<RemoteChannel[]>([])
  // "获取用量" fires a real remote fetch and rewrites the mirror; disable
  // the button while it's running so back-to-back clicks don't stack
  // 429s at the backend guard.
  const [refreshingRemote, setRefreshingRemote] = useState(false)
  // Studio bound to this JWT — used as the default "middle segment" of
  // new channel names. Fetched once on mount; empty string until it
  // arrives (openBatch guards against opening the modal before that).
  const [userStudio, setUserStudio] = useState('')

  const [batchOpen, setBatchOpen] = useState(false)
  const [batchPrefix, setBatchPrefix] = useState('')
  const [batchGroup, setBatchGroup] = useState('default')
  const [batchModels, setBatchModels] = useState(DEFAULT_ANTHROPIC_MODELS)
  const [batchPresetID, setBatchPresetID] = useState<PresetID>('anthropic')
  const [batchInput, setBatchInput] = useState('')
  const [batchBusy, setBatchBusy] = useState(false)
  const [batchErr, setBatchErr] = useState<string | null>(null)

  // "上普通 Key" — separate immediate-upload lane (pool_size=0 on the
  // backend). Same fields as the pool modal, but the intent is
  // different enough that a shared modal-with-toggle would blur the
  // mental model. Keep them side by side and let the operator pick.
  const [immOpen, setImmOpen] = useState(false)
  const [immPrefix, setImmPrefix] = useState('')
  const [immGroup, setImmGroup] = useState('default')
  const [immModels, setImmModels] = useState(DEFAULT_ANTHROPIC_MODELS)
  const [immPresetID, setImmPresetID] = useState<PresetID>('anthropic')
  const [immInput, setImmInput] = useState('')
  const [immBusy, setImmBusy] = useState(false)
  const [immErr, setImmErr] = useState<string | null>(null)

  useEffect(() => {
    void (async () => {
      try {
        const me = await api.getAuthMe()
        setUserStudio((me?.studio || '').trim())
      } catch (e) {
        console.warn('getAuthMe failed', e)
      }
    })()
  }, [])

  const reloadProfiles = useCallback(async () => {
    setLoadingProfiles(true)
    try {
      const res = await api.remoteProfiles()
      setProfiles(res.profiles)
      setSelectedID(prev => prev ?? (res.profiles[0]?.id ?? null))
    } catch (e) {
      console.warn('remoteProfiles failed', e)
    } finally {
      setLoadingProfiles(false)
    }
  }, [])

  useEffect(() => { void reloadProfiles() }, [reloadProfiles])

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

  const reloadChannels = useCallback(async () => {
    if (!selectedID) {
      setChannels([])
      return
    }
    try {
      const res = await api.remoteCachedChannels(selectedID)
      setChannels(res.channels)
    } catch (e) {
      console.warn('cached channels failed', e)
    }
  }, [selectedID])

  const refreshRemoteUsage = useCallback(async () => {
    if (!selectedID || refreshingRemote) return
    setRefreshingRemote(true)
    try {
      const res = await api.remoteChannelsRefresh(selectedID)
      await reloadChannels()
      alert(`已从远端拉取 ${res.fetched} 条渠道`)
    } catch (e: any) {
      alert('获取用量失败: ' + (e?.message || e))
    } finally {
      setRefreshingRemote(false)
    }
  }, [selectedID, refreshingRemote, reloadChannels])

  useEffect(() => {
    void reloadPending()
    void reloadChannels()
    // Auto-refresh both cards on the same tick so used_quota and the
    // queue's active/used transitions stay in sync without doubling the
    // network chatter.
    const t = setInterval(() => {
      void reloadPending()
      void reloadChannels()
    }, 30000)
    return () => clearInterval(t)
  }, [selectedID, reloadPending, reloadChannels])

  const openBatch = () => {
    const p = profiles.find(x => x.id === selectedID)
    // Middle segment defaults to the operator's bound studio — that's
    // the identifier they use to distinguish batches downstream. They
    // can still edit it (e.g. append -alpha / -beta) but the studio
    // stays visible.
    setBatchPrefix(userStudio)
    const initialPreset = CHANNEL_TYPE_PRESETS[0]
    setBatchPresetID(initialPreset.id)
    setBatchGroup(resolvePresetGroup(initialPreset, p))
    setBatchModels(resolvePresetModels(initialPreset, p))
    setBatchInput('')
    setBatchErr(null)
    setBatchOpen(true)
  }

  const submitBatch = async () => {
    if (!selectedID) return
    setBatchErr(null)
    if (!batchPrefix.trim()) return setBatchErr('中间段不能为空')
    if (!batchModels.trim()) return setBatchErr('models 不能为空')
    const items: { key: string; quota_usd?: number; note?: string }[] = []
    for (const raw of batchInput.split('\n')) {
      const t = raw.trim()
      if (!t || t.startsWith('#')) continue
      const parts = t.split(/[\s,]+/)
      const key = parts[0]
      if (!key) continue
      const item: { key: string; quota_usd?: number; note?: string } = { key }
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
    const fullNamePrefix = todayYYYYMMDD() + '-' + batchPrefix.trim()
    setBatchBusy(true)
    try {
      // pool_size=1 = "go into the pool" sentinel. Actual throttle
      // (interval + batch size) is set on the profile by the super
      // admin — operator never sees or picks it. Backend rewrites tag
      // to the caller's studio and zeroes any priority we might send,
      // so we intentionally don't pass tag / priority here.
      const res = await api.remotePendingEnqueue({
        profile_id: selectedID,
        name_prefix: fullNamePrefix,
        type: CHANNEL_TYPE_PRESETS.find(p => p.id === batchPresetID)?.type,
        group: batchGroup.trim() || 'default',
        models: batchModels.trim(),
        pool_size: 1,
        items,
      })
      alert(`已入队 ${res.inserted} 条${res.skipped ? `（${res.skipped} 条跳过 / 已存在）` : ''}`)
      setBatchOpen(false)
      void reloadPending()
    } catch (e: any) {
      setBatchErr(e?.message || String(e))
    } finally {
      setBatchBusy(false)
    }
  }

  const openImmediate = () => {
    const p = profiles.find(x => x.id === selectedID)
    setImmPrefix(userStudio)
    const initialPreset = CHANNEL_TYPE_PRESETS[0]
    setImmPresetID(initialPreset.id)
    setImmGroup(resolvePresetGroup(initialPreset, p))
    setImmModels(resolvePresetModels(initialPreset, p))
    setImmInput('')
    setImmErr(null)
    setImmOpen(true)
  }

  const submitImmediate = async () => {
    if (!selectedID) return
    setImmErr(null)
    if (!immPrefix.trim()) return setImmErr('中间段不能为空')
    if (!immModels.trim()) return setImmErr('models 不能为空')
    const items: { key: string; quota_usd?: number; note?: string }[] = []
    for (const raw of immInput.split('\n')) {
      const t = raw.trim()
      if (!t || t.startsWith('#')) continue
      const parts = t.split(/[\s,]+/)
      const key = parts[0]
      if (!key) continue
      const item: { key: string; quota_usd?: number; note?: string } = { key }
      if (parts[1]) {
        const q = parseFloat(parts[1])
        if (!isNaN(q) && q > 0) item.quota_usd = q
      }
      if (parts.length > 2) item.note = parts.slice(2).join(' ')
      items.push(item)
    }
    if (items.length === 0) return setImmErr('未解析到有效行')
    const fullNamePrefix = todayYYYYMMDD() + '-' + immPrefix.trim()
    setImmBusy(true)
    try {
      // immediate=true → server flips pool_size to 0 so the row goes
      // through the immediate lane on the next scheduler tick (no drip,
      // no wait). priority stays server-forced at 0 for operator.
      const res = await api.remotePendingEnqueue({
        profile_id: selectedID,
        name_prefix: fullNamePrefix,
        type: CHANNEL_TYPE_PRESETS.find(p => p.id === immPresetID)?.type,
        group: immGroup.trim() || 'default',
        models: immModels.trim(),
        pool_size: 0,
        immediate: true,
        items,
      })
      alert(`已入队 ${res.inserted} 条${res.skipped ? `（${res.skipped} 条跳过 / 已存在）` : ''}`)
      setImmOpen(false)
      void reloadPending()
    } catch (e: any) {
      setImmErr(e?.message || String(e))
    } finally {
      setImmBusy(false)
    }
  }

  const cancelPending = async (row: PendingKey) => {
    if (row.status !== 'pending' && row.status !== 'failed') return
    if (!window.confirm(`删除队列条目 (${row.key_masked})？只能删 pending/failed 的。`)) return
    try {
      await api.remotePendingDelete(row.id)
      await reloadPending()
    } catch (e: any) {
      alert('删除失败: ' + (e?.message || e))
    }
  }

  const selectedProfile = profiles.find(p => p.id === selectedID)

  return (
    <Layout
      title="Other Newapi Key"
      subtitle="批量上传 Key 到远端 New-Api"
      actions={
        <div className="flex items-center gap-2">
          <button
            onClick={openImmediate}
            disabled={!selectedID}
            className="border border-gray-300 text-gray-800 rounded-md px-3 py-1.5 text-sm hover:bg-gray-50 disabled:opacity-50"
          >
            上普通 Key
          </button>
          <button
            onClick={openBatch}
            disabled={!selectedID}
            className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-sm hover:opacity-85 disabled:opacity-50"
          >
            批量上 5刀key (Pool)
          </button>
        </div>
      }
    >
      <div className="space-y-4">
        <div className="bg-white border border-gray-200 rounded-xl p-4">
          <label className="block text-[11px] text-gray-500 mb-1">Profile</label>
          {loadingProfiles ? (
            <div className="text-xs text-gray-400">加载中…</div>
          ) : profiles.length === 0 ? (
            <div className="text-xs text-gray-500">还没有配置 Profile，请联系管理员。</div>
          ) : (
            <select
              value={selectedID ?? ''}
              onChange={e => setSelectedID(parseInt(e.target.value, 10) || null)}
              className="border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
            >
              {profiles.map(p => (
                <option key={p.id} value={p.id}>{p.name}</option>
              ))}
            </select>
          )}
          {selectedProfile && (
            <div className="text-[11px] text-gray-400 mt-2">
              默认 Models: <span className="font-mono">{selectedProfile.default_models || '未设置'}</span>
            </div>
          )}
        </div>

        <div className="bg-white border border-gray-200 rounded-xl">
          <div className="flex items-center justify-between px-4 py-3 border-b border-gray-100">
            <div>
              <div className="text-sm font-medium text-gray-900">我的远程渠道</div>
              <div className="text-[11px] text-gray-400 mt-0.5">
                每 30 秒从本地镜像刷新一次；远端用量每 15 分钟同步一次，需要立即拉取请按「获取用量」。
              </div>
            </div>
            <div className="flex items-center gap-2">
              <button
                onClick={() => void refreshRemoteUsage()}
                disabled={refreshingRemote || !selectedID}
                className="text-xs text-white bg-gray-900 rounded-md px-2 py-1 hover:opacity-85 disabled:opacity-50"
                title="向远端 new-api 发起一次拉取，更新用量数据"
              >
                {refreshingRemote ? '拉取中…' : '获取用量'}
              </button>
              <button
                onClick={() => void reloadChannels()}
                className="text-xs text-gray-600 border border-gray-300 rounded-md px-2 py-1 hover:bg-gray-50"
              >
                刷新
              </button>
            </div>
          </div>
          <div className="overflow-x-auto">
            <table className="min-w-full text-sm">
              <thead className="bg-gray-50 text-[11px] uppercase tracking-wider text-gray-500">
                <tr>
                  <th className="text-left px-4 py-2 font-medium">名称</th>
                  <th className="text-left px-4 py-2 font-medium">状态</th>
                  <th className="text-left px-4 py-2 font-medium">Group</th>
                  <th className="text-right px-4 py-2 font-medium" title="从 remote_channel_current 同步的累计用量">已用</th>
                  <th className="text-right px-4 py-2 font-medium" title="上传时填写的额度上限">额度</th>
                  <th className="text-right px-4 py-2 font-medium">剩余</th>
                  <th className="text-left px-4 py-2 font-medium">创建时间</th>
                </tr>
              </thead>
              <tbody>
                {channels.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="px-4 py-6 text-center text-xs text-gray-400">
                      暂无渠道，先在下方队列上传 Key
                    </td>
                  </tr>
                ) : (
                  channels.map(ch => {
                    const usedUSD = ch.used_quota / 500000
                    const quotaUSD = ch.quota_usd ?? 0
                    return (
                      <tr key={ch.id} className="border-t border-gray-100">
                        <td className="px-4 py-2 font-mono text-[11px]">{ch.name}</td>
                        <td className="px-4 py-2">
                          <span className={`inline-block px-1.5 py-0.5 rounded text-[10px] ${channelStatusCls(ch.status)}`}>
                            {channelStatusLabel(ch.status)}
                          </span>
                        </td>
                        <td className="px-4 py-2 text-[11px] text-gray-600">{ch.group || '—'}</td>
                        <td className="px-4 py-2 text-right tabular-nums text-[11px]">${usedUSD.toFixed(4)}</td>
                        <td className="px-4 py-2 text-right tabular-nums text-[11px]">
                          {quotaUSD > 0 ? `$${quotaUSD.toFixed(2)}` : <span className="text-gray-300">—</span>}
                        </td>
                        <td className="px-4 py-2 text-right">
                          <UsagePct used={usedUSD} quota={quotaUSD} />
                        </td>
                        <td className="px-4 py-2 text-[11px] text-gray-500">{fmtTime(ch.created_time)}</td>
                      </tr>
                    )
                  })
                )}
              </tbody>
            </table>
          </div>
        </div>

        <div className="bg-white border border-gray-200 rounded-xl">
          <div className="flex items-center justify-between px-4 py-3 border-b border-gray-100">
            <div>
              <div className="text-sm font-medium text-gray-900">上传队列</div>
              <div className="text-[11px] text-gray-400 mt-0.5">
                pending → active → used。每 30 秒自动刷新一次。
              </div>
            </div>
            <button
              onClick={() => void reloadPending()}
              className="text-xs text-gray-600 border border-gray-300 rounded-md px-2 py-1 hover:bg-gray-50"
            >
              刷新
            </button>
          </div>
          <div className="overflow-x-auto">
            <table className="min-w-full text-sm">
              <thead className="bg-gray-50 text-[11px] uppercase tracking-wider text-gray-500">
                <tr>
                  <th className="text-left px-4 py-2 font-medium">Key</th>
                  <th className="text-left px-4 py-2 font-medium">状态</th>
                  <th className="text-right px-4 py-2 font-medium" title="从 remote_channel_current 同步的累计用量">已用</th>
                  <th className="text-right px-4 py-2 font-medium" title="上传时填写的额度上限">额度</th>
                  <th className="text-left px-4 py-2 font-medium">尝试</th>
                  <th className="text-left px-4 py-2 font-medium">创建时间</th>
                  <th className="text-left px-4 py-2 font-medium">失败原因</th>
                  <th className="text-right px-4 py-2 font-medium">操作</th>
                </tr>
              </thead>
              <tbody>
                {pending.length === 0 ? (
                  <tr>
                    <td colSpan={8} className="px-4 py-6 text-center text-xs text-gray-400">
                      队列为空
                    </td>
                  </tr>
                ) : (
                  pending.map(row => {
                    const pct = row.quota_usd > 0 ? Math.min(100, (row.used_usd / row.quota_usd) * 100) : null
                    return (
                    <tr key={row.id} className="border-t border-gray-100">
                      <td className="px-4 py-2 font-mono text-[11px]">{row.key_masked}</td>
                      <td className="px-4 py-2">
                        <span className={`inline-block px-1.5 py-0.5 rounded text-[10px] ${STATUS_CLS[row.status]}`}>
                          {STATUS_LABEL[row.status]}
                        </span>
                      </td>
                      <td className="px-4 py-2 text-right tabular-nums">
                        {row.used_usd > 0 ? (
                          <div className="flex flex-col items-end gap-0.5">
                            <span className="text-[11px]">${row.used_usd.toFixed(4)}</span>
                            {pct != null && (
                              <div className="w-14 h-1 bg-gray-100 rounded overflow-hidden">
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
                      <td className="px-4 py-2 text-right tabular-nums text-[11px]">
                        {row.quota_usd > 0 ? `$${row.quota_usd.toFixed(2)}` : <span className="text-gray-300">—</span>}
                      </td>
                      <td className="px-4 py-2 text-xs tabular-nums">{row.attempts}</td>
                      <td className="px-4 py-2 text-[11px] text-gray-500">{fmtTime(row.created_at)}</td>
                      <td className="px-4 py-2 text-[11px] text-rose-600 max-w-xs truncate" title={row.failed_reason || ''}>
                        {row.failed_reason || '—'}
                      </td>
                      <td className="px-4 py-2 text-right">
                        {(row.status === 'pending' || row.status === 'failed') ? (
                          <button
                            onClick={() => void cancelPending(row)}
                            className="text-[11px] text-rose-600 hover:underline"
                          >
                            撤销
                          </button>
                        ) : (
                          <span className="text-[11px] text-gray-300">—</span>
                        )}
                      </td>
                    </tr>
                    )
                  })
                )}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      {batchOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
          <div className="bg-white rounded-xl shadow-lg w-full max-w-lg p-5">
            <div className="text-base font-semibold mb-3">批量上 Key</div>
            <div className="space-y-3">
              <div>
                <label className="block text-[11px] text-gray-500 mb-1">
                  名字中间段（最终 = {todayYYYYMMDD()}-&lt;你填&gt;-&lt;key末8&gt;-&lt;hash8&gt;）
                </label>
                <div className="flex items-center gap-1">
                  <span className="text-[11px] text-gray-400 font-mono whitespace-nowrap">{todayYYYYMMDD()}-</span>
                  <input
                    value={batchPrefix}
                    onChange={e => setBatchPrefix(e.target.value)}
                    placeholder="例如 anthropic-A"
                    className="flex-1 border border-gray-300 rounded-md px-2 py-1.5 text-sm font-mono focus:outline-none focus:border-gray-900"
                  />
                </div>
              </div>

              <div>
                <label className="block text-[11px] text-gray-500 mb-1">渠道类型</label>
                <div className="inline-flex rounded-md border border-gray-300 overflow-hidden">
                  {CHANNEL_TYPE_PRESETS.map(p => {
                    const active = batchPresetID === p.id
                    return (
                      <button
                        key={p.id}
                        type="button"
                        onClick={() => {
                          setBatchPresetID(p.id)
                          const prof = profiles.find(x => x.id === selectedID)
                          setBatchGroup(resolvePresetGroup(p, prof))
                          setBatchModels(resolvePresetModels(p, prof))
                        }}
                        className={`px-3 py-1 text-xs border-r border-gray-200 last:border-r-0 transition-colors ${
                          active ? 'bg-gray-900 text-white' : 'bg-white text-gray-700 hover:bg-gray-50'
                        }`}
                      >
                        {p.label}
                      </button>
                    )
                  })}
                </div>
              </div>

              <div>
                <label className="block text-[11px] text-gray-500 mb-1">Group</label>
                <input
                  value={batchGroup}
                  onChange={e => setBatchGroup(e.target.value)}
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                />
              </div>

              <div>
                <label className="block text-[11px] text-gray-500 mb-1">Models（逗号分隔）</label>
                <textarea
                  value={batchModels}
                  onChange={e => setBatchModels(e.target.value)}
                  rows={2}
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-[11px] font-mono focus:outline-none focus:border-gray-900"
                />
              </div>

              <div>
                <label className="block text-[11px] text-gray-500 mb-1">
                  Keys（每行一个，可选 <code>quota_usd</code> / 备注：<code>key 10 备注</code>）
                </label>
                <textarea
                  value={batchInput}
                  onChange={e => setBatchInput(e.target.value)}
                  rows={8}
                  placeholder="sk-... 10&#10;sk-... 20 备注"
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-[11px] font-mono focus:outline-none focus:border-gray-900"
                />
              </div>
              <p className="text-[11px] text-gray-400">
                上 Key 后进入 Pool 队列。管理员配置了每次上几个 + 检查间隔。
                同批 Key 会按 FIFO 依次进池，前一批全部消耗完之前不会开始新一批。
              </p>
              {batchErr && <p className="text-xs text-rose-600">{batchErr}</p>}
            </div>
            <div className="mt-5 flex justify-end gap-2">
              <button
                onClick={() => setBatchOpen(false)}
                disabled={batchBusy}
                className="border border-gray-300 rounded-md px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50"
              >
                取消
              </button>
              <button
                onClick={submitBatch}
                disabled={batchBusy}
                className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-sm hover:opacity-85 disabled:opacity-50"
              >
                {batchBusy ? '入队中…' : '入队上传'}
              </button>
            </div>
          </div>
        </div>
      )}

      {immOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
          <div className="bg-white rounded-xl shadow-lg w-full max-w-lg p-5">
            <div className="text-base font-semibold mb-1">上普通 Key</div>
            <div className="text-[11px] text-gray-500 mb-3">
              立即上传（不进 Pool 队列），默认 priority = 0。
            </div>
            <div className="space-y-3">
              <div>
                <label className="block text-[11px] text-gray-500 mb-1">
                  名字中间段（最终 = {todayYYYYMMDD()}-&lt;你填&gt;-&lt;key末8&gt;-&lt;hash8&gt;）
                </label>
                <div className="flex items-center gap-1">
                  <span className="text-[11px] text-gray-400 font-mono whitespace-nowrap">{todayYYYYMMDD()}-</span>
                  <input
                    value={immPrefix}
                    onChange={e => setImmPrefix(e.target.value)}
                    placeholder="例如 studio-A"
                    className="flex-1 border border-gray-300 rounded-md px-2 py-1.5 text-sm font-mono focus:outline-none focus:border-gray-900"
                  />
                </div>
              </div>
              <div>
                <label className="block text-[11px] text-gray-500 mb-1">渠道类型</label>
                <div className="inline-flex rounded-md border border-gray-300 overflow-hidden">
                  {CHANNEL_TYPE_PRESETS.map(p => {
                    const active = immPresetID === p.id
                    return (
                      <button
                        key={p.id}
                        type="button"
                        onClick={() => {
                          setImmPresetID(p.id)
                          const prof = profiles.find(x => x.id === selectedID)
                          setImmGroup(resolvePresetGroup(p, prof))
                          setImmModels(resolvePresetModels(p, prof))
                        }}
                        className={`px-3 py-1 text-xs border-r border-gray-200 last:border-r-0 transition-colors ${
                          active ? 'bg-gray-900 text-white' : 'bg-white text-gray-700 hover:bg-gray-50'
                        }`}
                      >
                        {p.label}
                      </button>
                    )
                  })}
                </div>
              </div>
              <div>
                <label className="block text-[11px] text-gray-500 mb-1">Group</label>
                <input
                  value={immGroup}
                  onChange={e => setImmGroup(e.target.value)}
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                />
              </div>
              <div>
                <label className="block text-[11px] text-gray-500 mb-1">Models（逗号分隔）</label>
                <textarea
                  value={immModels}
                  onChange={e => setImmModels(e.target.value)}
                  rows={2}
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-[11px] font-mono focus:outline-none focus:border-gray-900"
                />
              </div>
              <div>
                <label className="block text-[11px] text-gray-500 mb-1">
                  Keys（每行一个，可选 <code>quota_usd</code> / 备注：<code>key 10 备注</code>）
                </label>
                <textarea
                  value={immInput}
                  onChange={e => setImmInput(e.target.value)}
                  rows={8}
                  placeholder="sk-... 10&#10;sk-... 20 备注"
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-[11px] font-mono focus:outline-none focus:border-gray-900"
                />
              </div>
              {immErr && <p className="text-xs text-rose-600">{immErr}</p>}
            </div>
            <div className="mt-5 flex justify-end gap-2">
              <button
                onClick={() => setImmOpen(false)}
                disabled={immBusy}
                className="border border-gray-300 rounded-md px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50"
              >
                取消
              </button>
              <button
                onClick={submitImmediate}
                disabled={immBusy}
                className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-sm hover:opacity-85 disabled:opacity-50"
              >
                {immBusy ? '上传中…' : '立即上传'}
              </button>
            </div>
          </div>
        </div>
      )}
    </Layout>
  )
}
