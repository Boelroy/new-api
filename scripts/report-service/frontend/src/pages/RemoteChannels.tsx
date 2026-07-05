import { useCallback, useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import { api, type RemoteChannel, type RemoteProfile } from '../api'

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

  // Create / edit form. `editingID = 0` means we're creating a new profile.
  const [formOpen, setFormOpen] = useState(false)
  const [editingID, setEditingID] = useState<number | null>(null)
  const [formName, setFormName] = useState('')
  const [formHost, setFormHost] = useState('')
  const [formUserID, setFormUserID] = useState('')
  const [formToken, setFormToken] = useState('')
  const [formBusy, setFormBusy] = useState(false)
  const [formErr, setFormErr] = useState<string | null>(null)

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
    } catch (e: any) {
      setFetchErr(e?.message || String(e))
      setChannels([])
      setMeta(null)
    } finally {
      setFetching(false)
    }
  }

  const summary = useMemo(() => {
    const totalUsedUSD = channels.reduce((s, c) => s + usdFromQuota(c.used_quota), 0)
    const enabled = channels.filter(c => c.status === 1).length
    const disabled = channels.length - enabled
    return { count: channels.length, totalUsedUSD, enabled, disabled }
  }, [channels])

  const actions = (
    <div className="flex items-center gap-2">
      <button
        onClick={openCreate}
        className="border border-gray-300 text-gray-700 rounded-md px-3 py-1.5 text-xs hover:bg-gray-50"
      >
        + New profile
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
                      <th className="px-3 py-2 text-left font-medium">ID</th>
                      <th className="px-3 py-2 text-left font-medium">名称</th>
                      <th className="px-3 py-2 text-left font-medium">Type</th>
                      <th className="px-3 py-2 text-left font-medium">Group</th>
                      <th className="px-3 py-2 text-left font-medium">Tag</th>
                      <th className="px-3 py-2 text-right font-medium">Priority</th>
                      <th className="px-3 py-2 text-right font-medium">已用 (USD)</th>
                      <th className="px-3 py-2 text-left font-medium">状态</th>
                      <th className="px-3 py-2 text-left font-medium">创建时间</th>
                    </tr>
                  </thead>
                  <tbody>
                    {channels.map(c => (
                      <tr key={c.id} className="border-b border-gray-100 hover:bg-gray-50">
                        <td className="px-3 py-2 tabular-nums">{c.id}</td>
                        <td className="px-3 py-2 font-mono text-[11px] max-w-[280px] truncate" title={c.name}>{c.name}</td>
                        <td className="px-3 py-2 tabular-nums">{c.type}</td>
                        <td className="px-3 py-2">{c.group || '—'}</td>
                        <td className="px-3 py-2 text-gray-500">{c.tag || '—'}</td>
                        <td className="px-3 py-2 tabular-nums text-right">{c.priority}</td>
                        <td className="px-3 py-2 tabular-nums text-right font-medium">
                          ${usdFromQuota(c.used_quota).toFixed(2)}
                        </td>
                        <td className="px-3 py-2">
                          <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] ${STATUS_CLS[c.status] ?? 'bg-gray-100 text-gray-600'}`}>
                            {STATUS_LABEL[c.status] ?? c.status}
                          </span>
                        </td>
                        <td className="px-3 py-2 text-gray-500 text-[11px]">{fmtTime(c.created_time)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          </>
        )}
      </div>

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
