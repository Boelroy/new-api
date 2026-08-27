import { useCallback, useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import { api, type LocalToRemoteSyncable, type LocalToRemoteResult, type RemoteProfile } from '../api'
import { toast } from '../components/feedback'

// Local → Remote sync: push local (studio-uploaded) channels up to a chosen
// remote new-api profile as channels. Mirror of LocalChannelSync (which goes
// remote → local); here the source is the local channels table and the target
// is a remote profile picked at the top.

const btnCls =
  'border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50 disabled:opacity-40 disabled:cursor-not-allowed'
const primaryBtnCls =
  'border border-brand bg-brand text-white rounded-md px-3 py-1.5 text-xs hover:bg-brand-700 disabled:opacity-40 disabled:cursor-not-allowed'
const inputCls = 'border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-white focus:outline-none focus:border-gray-900'

export default function LocalToRemoteSync() {
  const [profiles, setProfiles] = useState<RemoteProfile[]>([])
  const [profileID, setProfileID] = useState<number | null>(null)
  const [studio, setStudio] = useState('')
  const [group, setGroup] = useState('')
  const [items, setItems] = useState<LocalToRemoteSyncable[]>([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState<Set<number>>(new Set())
  const [results, setResults] = useState<Record<number, LocalToRemoteResult>>({})
  const [bulkBusy, setBulkBusy] = useState(false)

  useEffect(() => {
    void (async () => {
      try {
        const res = await api.remoteProfiles()
        setProfiles(res.profiles)
        if (res.profiles.length > 0) setProfileID(res.profiles[0].id)
      } catch (e) {
        setErr(e instanceof Error ? e.message : String(e))
      }
    })()
  }, [])

  const load = useCallback(async () => {
    if (!profileID) {
      setItems([])
      return
    }
    setLoading(true)
    setErr(null)
    try {
      const res = await api.localToRemoteSyncable(profileID, {
        studio: studio.trim() || undefined,
        group: group.trim() || undefined,
      })
      setItems(res.items ?? [])
      setResults({})
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [profileID, studio, group])

  useEffect(() => {
    void load()
  }, [profileID]) // eslint-disable-line react-hooks/exhaustive-deps

  const unsynced = useMemo(() => items.filter(i => !i.already_synced), [items])

  const syncItems = useCallback(
    async (targets: LocalToRemoteSyncable[]) => {
      if (!profileID || targets.length === 0) return
      const ids = targets.map(t => t.local_channel_id)
      setBusy(prev => {
        const next = new Set(prev)
        ids.forEach(id => next.add(id))
        return next
      })
      try {
        const res = await api.localToRemoteSync(
          profileID,
          targets.map(t => ({ local_channel_id: t.local_channel_id })),
        )
        const byID: Record<number, LocalToRemoteResult> = {}
        for (const r of res.results ?? []) byID[r.local_channel_id] = r
        setResults(prev => ({ ...prev, ...byID }))
        await load()
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e)
        setResults(prev => {
          const next = { ...prev }
          for (const t of targets) {
            next[t.local_channel_id] = {
              local_channel_id: t.local_channel_id,
              profile_id: profileID,
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
          ids.forEach(id => next.delete(id))
          return next
        })
      }
    },
    [profileID, load],
  )

  const syncAll = useCallback(async () => {
    if (unsynced.length === 0) return
    setBulkBusy(true)
    try {
      await syncItems(unsynced)
      toast.success(`已同步 ${unsynced.length} 条`)
    } finally {
      setBulkBusy(false)
    }
  }, [unsynced, syncItems])

  const actions = (
    <div className="flex items-center gap-2">
      <button className={btnCls} onClick={() => void load()} disabled={loading || !profileID}>
        刷新
      </button>
      <button className={primaryBtnCls} onClick={() => void syncAll()} disabled={bulkBusy || unsynced.length === 0}>
        {bulkBusy ? '同步中…' : `同步全部未同步 (${unsynced.length})`}
      </button>
    </div>
  )

  return (
    <Layout
      title="本地→远程同步"
      subtitle="把本地(工作室上号)的渠道推送到远程 new-api 实例作为渠道（仅 Admin 以上）"
      actions={actions}
    >
      <div className="mb-3 flex flex-wrap items-end gap-3">
        <label className="block">
          <span className="mono-label block mb-1">目标远程实例</span>
          <select
            className={inputCls + ' min-w-[200px]'}
            value={profileID ?? ''}
            onChange={e => setProfileID(e.target.value ? Number(e.target.value) : null)}
          >
            {profiles.length === 0 && <option value="">（无 profile）</option>}
            {profiles.map(p => (
              <option key={p.id} value={p.id}>
                {p.name} · #{p.id}
              </option>
            ))}
          </select>
        </label>
        <label className="block">
          <span className="mono-label block mb-1">工作室 (tag)</span>
          <input
            className={inputCls}
            value={studio}
            placeholder="留空=全部"
            onChange={e => setStudio(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') void load() }}
          />
        </label>
        <label className="block">
          <span className="mono-label block mb-1">分组 (group)</span>
          <input
            className={inputCls}
            value={group}
            placeholder="留空=全部"
            onChange={e => setGroup(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') void load() }}
          />
        </label>
        <button className={btnCls} onClick={() => void load()} disabled={loading || !profileID}>
          应用筛选
        </button>
      </div>

      {err && (
        <div className="mb-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700">{err}</div>
      )}

      <div className="rounded-lg border border-gray-200 bg-white overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="bg-gray-50 text-gray-500 text-left">
                <th className="px-3 py-2 font-medium">本地渠道</th>
                <th className="px-3 py-2 font-medium">工作室</th>
                <th className="px-3 py-2 font-medium">类型</th>
                <th className="px-3 py-2 font-medium">Key</th>
                <th className="px-3 py-2 font-medium">分组 / 模型</th>
                <th className="px-3 py-2 font-medium text-right">额度 / 本地已用</th>
                <th className="px-3 py-2 font-medium">状态</th>
                <th className="px-3 py-2 font-medium text-right">操作</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {loading && (
                <tr>
                  <td colSpan={8} className="px-3 py-8 text-center text-gray-400">加载中…</td>
                </tr>
              )}
              {!loading && items.length === 0 && (
                <tr>
                  <td colSpan={8} className="px-3 py-8 text-center text-gray-400">没有可同步的本地渠道</td>
                </tr>
              )}
              {!loading &&
                items.map(it => {
                  const rowBusy = busy.has(it.local_channel_id)
                  const result = results[it.local_channel_id]
                  return (
                    <tr key={it.local_channel_id} className="hover:bg-gray-50/60 align-top">
                      <td className="px-3 py-2">
                        <div className="text-gray-900">{it.name || '—'}</div>
                        <div className="text-[10px] text-gray-400">#{it.local_channel_id}</div>
                      </td>
                      <td className="px-3 py-2 text-gray-700">{it.studio || '—'}</td>
                      <td className="px-3 py-2">
                        <span className="inline-block rounded bg-gray-100 px-1.5 py-0.5 text-[10px] text-gray-700">
                          {it.channel_type_name}
                        </span>
                      </td>
                      <td className="px-3 py-2 font-mono text-[10px] text-gray-500">{it.key_masked || '—'}</td>
                      <td className="px-3 py-2 max-w-[300px]">
                        <div className="text-gray-700">{it.group}</div>
                        <div className="text-[10px] text-gray-400 break-words">{it.models || '（无模型）'}</div>
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums text-gray-600">
                        <div>{it.quota_usd != null ? `$${it.quota_usd.toFixed(2)}` : '—'}</div>
                        <div className="text-[10px] text-gray-400">已用 ${it.used_usd.toFixed(2)}</div>
                      </td>
                      <td className="px-3 py-2">
                        {it.already_synced ? (
                          <span className="inline-block rounded bg-emerald-100 px-1.5 py-0.5 text-[10px] text-emerald-700">
                            已同步 → #{it.remote_channel_id}
                          </span>
                        ) : result?.error ? (
                          <span className="text-[10px] text-rose-600" title={result.error}>失败</span>
                        ) : result?.skipped ? (
                          <span className="inline-block rounded bg-amber-100 px-1.5 py-0.5 text-[10px] text-amber-800">
                            已存在 → #{result.remote_channel_id}
                          </span>
                        ) : (
                          <span className="text-[10px] text-gray-400">未同步</span>
                        )}
                      </td>
                      <td className="px-3 py-2 text-right">
                        <button
                          className={btnCls}
                          disabled={rowBusy || it.already_synced || !it.models}
                          onClick={() => void syncItems([it])}
                          title={!it.models ? '该渠道没有模型' : undefined}
                        >
                          {rowBusy ? '同步中…' : it.already_synced ? '已同步' : '推送到远程'}
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
                <li key={r.local_channel_id}>
                  #{r.local_channel_id}: {r.error}
                </li>
              ))}
          </ul>
        </div>
      )}
    </Layout>
  )
}
