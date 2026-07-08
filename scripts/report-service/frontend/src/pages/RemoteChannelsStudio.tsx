import { useCallback, useEffect, useState } from 'react'
import Layout from '../components/Layout'
import { api, type PendingKey, type RemoteProfile } from '../api'

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

export default function RemoteChannelsStudio() {
  const [profiles, setProfiles] = useState<RemoteProfile[]>([])
  const [selectedID, setSelectedID] = useState<number | null>(null)
  const [loadingProfiles, setLoadingProfiles] = useState(true)
  const [pending, setPending] = useState<PendingKey[]>([])
  // Studio bound to this JWT — used as the default "middle segment" of
  // new channel names. Fetched once on mount; empty string until it
  // arrives (openBatch guards against opening the modal before that).
  const [userStudio, setUserStudio] = useState('')

  const [batchOpen, setBatchOpen] = useState(false)
  const [batchPrefix, setBatchPrefix] = useState('')
  const [batchGroup, setBatchGroup] = useState('default')
  const [batchModels, setBatchModels] = useState(DEFAULT_ANTHROPIC_MODELS)
  const [batchInput, setBatchInput] = useState('')
  const [batchBusy, setBatchBusy] = useState(false)
  const [batchErr, setBatchErr] = useState<string | null>(null)

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

  useEffect(() => {
    void reloadPending()
    // Auto-refresh the queue so pending → active → used transitions land
    // without the operator hunting for a refresh button.
    const t = setInterval(() => { void reloadPending() }, 30000)
    return () => clearInterval(t)
  }, [selectedID, reloadPending])

  const openBatch = () => {
    const p = profiles.find(x => x.id === selectedID)
    // Middle segment defaults to the operator's bound studio — that's
    // the identifier they use to distinguish batches downstream. They
    // can still edit it (e.g. append -alpha / -beta) but the studio
    // stays visible.
    setBatchPrefix(userStudio)
    setBatchGroup((p?.default_group || '').trim() || 'default')
    setBatchModels((p?.default_models || '').trim() || DEFAULT_ANTHROPIC_MODELS)
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
      title="Remote Channels"
      subtitle="批量上传 Key 到远端 New-Api"
      actions={
        <button
          onClick={openBatch}
          disabled={!selectedID}
          className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-sm hover:opacity-85 disabled:opacity-50"
        >
          批量上 Key
        </button>
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
                  <th className="text-left px-4 py-2 font-medium">尝试</th>
                  <th className="text-left px-4 py-2 font-medium">创建时间</th>
                  <th className="text-left px-4 py-2 font-medium">失败原因</th>
                  <th className="text-right px-4 py-2 font-medium">操作</th>
                </tr>
              </thead>
              <tbody>
                {pending.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="px-4 py-6 text-center text-xs text-gray-400">
                      队列为空
                    </td>
                  </tr>
                ) : (
                  pending.map(row => (
                    <tr key={row.id} className="border-t border-gray-100">
                      <td className="px-4 py-2 font-mono text-[11px]">{row.key_masked}</td>
                      <td className="px-4 py-2">
                        <span className={`inline-block px-1.5 py-0.5 rounded text-[10px] ${STATUS_CLS[row.status]}`}>
                          {STATUS_LABEL[row.status]}
                        </span>
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
                  ))
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
    </Layout>
  )
}
