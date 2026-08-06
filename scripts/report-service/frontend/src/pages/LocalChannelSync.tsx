import { useCallback, useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import { api, SyncableCredential, LocalSyncResult } from '../api'

const btnCls =
  'border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50 disabled:opacity-40 disabled:cursor-not-allowed'
const primaryBtnCls =
  'border border-gray-900 bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:bg-gray-800 disabled:opacity-40 disabled:cursor-not-allowed'

function fmtTime(ts: number) {
  if (!ts) return '—'
  const d = new Date(ts * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

// credKey uniquely identifies a credential row across renders.
function credKey(c: { profile_id: number; remote_channel_id: number }) {
  return `${c.profile_id}:${c.remote_channel_id}`
}

export default function LocalChannelSync() {
  const [items, setItems] = useState<SyncableCredential[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState<string | null>(null)
  // Per-row in-flight state, keyed by credKey.
  const [busy, setBusy] = useState<Set<string>>(new Set())
  // Per-row result message (error or "已同步 → #id"), keyed by credKey.
  const [results, setResults] = useState<Record<string, LocalSyncResult>>({})
  const [bulkBusy, setBulkBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setErr(null)
    try {
      const res = await api.localSyncList()
      setItems(res.items ?? [])
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const unsynced = useMemo(() => items.filter(i => !i.already_synced), [items])

  const syncItems = useCallback(
    async (targets: SyncableCredential[]) => {
      if (targets.length === 0) return
      const keys = targets.map(credKey)
      setBusy(prev => {
        const next = new Set(prev)
        keys.forEach(k => next.add(k))
        return next
      })
      try {
        const res = await api.localSync(
          targets.map(t => ({ profile_id: t.profile_id, remote_channel_id: t.remote_channel_id })),
        )
        const byKey: Record<string, LocalSyncResult> = {}
        for (const r of res.results ?? []) {
          byKey[credKey(r)] = r
        }
        setResults(prev => ({ ...prev, ...byKey }))
        // Reload so already_synced / local_channel_id reflect the new state.
        await load()
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e)
        setResults(prev => {
          const next = { ...prev }
          for (const t of targets) {
            next[credKey(t)] = {
              profile_id: t.profile_id,
              remote_channel_id: t.remote_channel_id,
              ok: false,
              skipped: false,
              error: msg,
            }
          }
          return next
        })
      } finally {
        setBusy(prev => {
          const next = new Set(prev)
          keys.forEach(k => next.delete(k))
          return next
        })
      }
    },
    [load],
  )

  const syncAll = useCallback(async () => {
    if (unsynced.length === 0) return
    setBulkBusy(true)
    try {
      await syncItems(unsynced)
    } finally {
      setBulkBusy(false)
    }
  }, [unsynced, syncItems])

  const actions = (
    <div className="flex items-center gap-2">
      <button className={btnCls} onClick={() => void load()} disabled={loading}>
        刷新
      </button>
      <button className={primaryBtnCls} onClick={() => void syncAll()} disabled={bulkBusy || unsynced.length === 0}>
        {bulkBusy ? '同步中…' : `同步全部未同步 (${unsynced.length})`}
      </button>
    </div>
  )

  return (
    <Layout
      title="本地渠道同步"
      subtitle="把已保存 credential 的远端渠道同步为本地 new-api 渠道（仅 Admin 以上）"
      actions={actions}
    >
      {err && (
        <div className="mb-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700">{err}</div>
      )}

      <div className="rounded-lg border border-gray-200 bg-white overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="bg-gray-50 text-gray-500 text-left">
                <th className="px-3 py-2 font-medium">Profile</th>
                <th className="px-3 py-2 font-medium">远端渠道</th>
                <th className="px-3 py-2 font-medium">类型</th>
                <th className="px-3 py-2 font-medium">Key</th>
                <th className="px-3 py-2 font-medium">Region</th>
                <th className="px-3 py-2 font-medium">目标分组 / 模型</th>
                <th className="px-3 py-2 font-medium">状态</th>
                <th className="px-3 py-2 font-medium text-right">操作</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {loading && (
                <tr>
                  <td colSpan={8} className="px-3 py-8 text-center text-gray-400">
                    加载中…
                  </td>
                </tr>
              )}
              {!loading && items.length === 0 && (
                <tr>
                  <td colSpan={8} className="px-3 py-8 text-center text-gray-400">
                    没有已保存 credential 的渠道
                  </td>
                </tr>
              )}
              {!loading &&
                items.map(it => {
                  const k = credKey(it)
                  const rowBusy = busy.has(k)
                  const result = results[k]
                  return (
                    <tr key={k} className="hover:bg-gray-50/60 align-top">
                      <td className="px-3 py-2">
                        <div className="text-gray-900">{it.profile_name}</div>
                        <div className="text-[10px] text-gray-400">#{it.profile_id}</div>
                      </td>
                      <td className="px-3 py-2 tabular-nums">
                        <div className="text-gray-900">#{it.remote_channel_id}</div>
                        {it.channel_name && <div className="text-[10px] text-gray-400">{it.channel_name}</div>}
                        <div className="text-[10px] text-gray-400">{fmtTime(it.created_at)}</div>
                      </td>
                      <td className="px-3 py-2">
                        <span className="inline-block rounded bg-gray-100 px-1.5 py-0.5 text-[10px] text-gray-700">
                          {it.channel_type_name}
                        </span>
                        {it.key_type && <div className="text-[10px] text-gray-400 mt-0.5">{it.key_type}</div>}
                      </td>
                      <td className="px-3 py-2 font-mono text-[10px] text-gray-500">{it.key_masked || '—'}</td>
                      <td className="px-3 py-2 text-[10px] text-gray-500 max-w-[180px] truncate" title={it.region}>
                        {it.region || '—'}
                      </td>
                      <td className="px-3 py-2 max-w-[280px]">
                        <div className="text-gray-700">{it.resolved_group}</div>
                        <div className="text-[10px] text-gray-400 break-words">{it.resolved_models || '（无默认模型）'}</div>
                      </td>
                      <td className="px-3 py-2">
                        {it.already_synced ? (
                          <span className="inline-block rounded bg-emerald-100 px-1.5 py-0.5 text-[10px] text-emerald-700">
                            已同步 → #{it.local_channel_id}
                          </span>
                        ) : result?.error ? (
                          <span className="text-[10px] text-rose-600" title={result.error}>
                            失败
                          </span>
                        ) : result?.skipped ? (
                          <span className="inline-block rounded bg-amber-100 px-1.5 py-0.5 text-[10px] text-amber-800">
                            已存在 → #{result.channel_id}
                          </span>
                        ) : (
                          <span className="text-[10px] text-gray-400">未同步</span>
                        )}
                      </td>
                      <td className="px-3 py-2 text-right">
                        <button
                          className={btnCls}
                          disabled={rowBusy || it.already_synced || !it.resolved_models}
                          onClick={() => void syncItems([it])}
                          title={!it.resolved_models ? '该 profile 未设置对应类型的默认模型' : undefined}
                        >
                          {rowBusy ? '同步中…' : it.already_synced ? '已同步' : '同步到本地'}
                        </button>
                      </td>
                    </tr>
                  )
                })}
            </tbody>
          </table>
        </div>
      </div>

      {Object.values(results).some(r => r.error) && (
        <div className="mt-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700">
          <div className="font-medium mb-1">同步失败：</div>
          <ul className="space-y-0.5">
            {Object.values(results)
              .filter(r => r.error)
              .map(r => (
                <li key={`${r.profile_id}:${r.remote_channel_id}`}>
                  #{r.profile_id}/#{r.remote_channel_id}: {r.error}
                </li>
              ))}
          </ul>
        </div>
      )}
    </Layout>
  )
}
