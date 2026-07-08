import { useCallback, useEffect, useState } from 'react'
import { api, type LocalPendingKey, type LocalPoolConfig } from '../api'

// Local pool panel — the "Pool 上 Key" tab on KeyCapacity. Same drip
// mechanism as the Remote Channels pool (interval + batch + auto RPM +
// priority accumulation), except uploads land in the local channels
// table via the existing handleBatchCreateChannels insert path.
//
// Intentionally NOT reusing BatchCreatePanel — that component uploads
// synchronously (POST /api/channels/batch-create) and this one stages
// keys into local_pending_key for the scheduler to drip. Same input
// shape (studio + suffix + key list), different backend contract.

function fmtTime(epoch: number) {
  if (!epoch) return '—'
  return new Date(epoch * 1000).toLocaleString()
}

const STATUS_CLS: Record<LocalPendingKey['status'], string> = {
  pending: 'bg-blue-100 text-blue-700',
  active:  'bg-emerald-100 text-emerald-800',
  used:    'bg-gray-100 text-gray-500',
  failed:  'bg-red-100 text-red-700',
}

type Props = {
  // Studio operator sees a locked studio (JWT-bound). Super admin picks
  // from a dropdown / creates new one — matches BatchCreatePanel UX.
  lockedStudio?: string
}

export default function LocalPoolPanel({ lockedStudio }: Props) {
  const [cfg, setCfg] = useState<LocalPoolConfig | null>(null)
  const [cfgDirty, setCfgDirty] = useState(false)
  const [cfgSaving, setCfgSaving] = useState(false)
  const [cfgMsg, setCfgMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const [rpmNow, setRpmNow] = useState<number | null>(null)

  const [studio, setStudio] = useState(lockedStudio ?? '')
  const [studioMode, setStudioMode] = useState<'pick' | 'new'>('pick')
  const [studios, setStudios] = useState<string[]>([])
  const [suffix, setSuffix] = useState('')
  const [unitPrice, setUnitPrice] = useState('')
  const [input, setInput] = useState('')
  const [enqueueBusy, setEnqueueBusy] = useState(false)
  const [enqueueMsg, setEnqueueMsg] = useState<{ ok: boolean; text: string } | null>(null)

  const [pending, setPending] = useState<LocalPendingKey[]>([])
  const studioLocked = !!lockedStudio

  // Load studios list (super admin) + initial config + queue.
  useEffect(() => {
    if (studioLocked) return
    void (async () => {
      try {
        const res = await api.listStudios()
        setStudios(res.studios)
        setStudio(prev => {
          if (prev) return prev
          if (res.studios.includes('pipi')) return 'pipi'
          return res.studios[0] ?? ''
        })
      } catch (e) {
        console.warn('studios list failed', e)
      }
    })()
  }, [studioLocked])

  const loadCfg = useCallback(async () => {
    try {
      const res = await api.localPoolGetConfig()
      setCfg(res)
    } catch (e) {
      console.warn('local pool config failed', e)
    }
  }, [])

  const loadRPM = useCallback(async () => {
    try {
      const res = await api.localPoolGetRPM()
      setRpmNow(res.rpm)
    } catch (e) {
      console.warn('local pool rpm failed', e)
    }
  }, [])

  const loadPending = useCallback(async () => {
    try {
      const res = await api.localPoolList(studioLocked ? undefined : (studio || undefined))
      setPending(res.items)
    } catch (e) {
      console.warn('local pool list failed', e)
    }
  }, [studio, studioLocked])

  useEffect(() => { void loadCfg() }, [loadCfg])
  useEffect(() => { void loadRPM() }, [loadRPM])
  useEffect(() => { void loadPending() }, [loadPending])
  useEffect(() => {
    const t = setInterval(() => {
      void loadPending()
      void loadRPM()
    }, 30000)
    return () => clearInterval(t)
  }, [loadPending, loadRPM])

  const saveCfg = async () => {
    if (!cfg) return
    setCfgSaving(true)
    setCfgMsg(null)
    try {
      const res = await api.localPoolSetConfig(cfg)
      setCfg(res)
      setCfgDirty(false)
      setCfgMsg({ ok: true, text: '已保存' })
    } catch (e: any) {
      setCfgMsg({ ok: false, text: e?.message || String(e) })
    } finally {
      setCfgSaving(false)
    }
  }

  const submitEnqueue = async () => {
    setEnqueueMsg(null)
    if (!studioLocked && !studio.trim()) {
      setEnqueueMsg({ ok: false, text: '请选择或新建 studio' })
      return
    }
    if (!suffix.trim()) {
      setEnqueueMsg({ ok: false, text: 'suffix 不能为空' })
      return
    }
    // Parse "key quota_usd" lines. Same shape as BatchCreatePanel so
    // operator muscle memory carries over.
    const channels: { key: string; quota_usd: number; unit_price_cny?: number }[] = []
    const globalUnit = unitPrice.trim() ? parseFloat(unitPrice.trim()) : NaN
    for (const raw of input.split('\n')) {
      const line = raw.trim()
      if (!line || line.startsWith('#')) continue
      const parts = line.split(/[\s,]+/)
      const key = parts[0]
      if (!key) continue
      const q = parts[1] ? parseFloat(parts[1]) : NaN
      if (isNaN(q) || q <= 0) continue
      const item: { key: string; quota_usd: number; unit_price_cny?: number } = { key, quota_usd: q }
      if (parts[2]) {
        const u = parseFloat(parts[2])
        if (!isNaN(u) && u > 0) item.unit_price_cny = u
      }
      channels.push(item)
    }
    if (channels.length === 0) {
      setEnqueueMsg({ ok: false, text: '未解析到有效行' })
      return
    }
    setEnqueueBusy(true)
    try {
      const res = await api.localPoolEnqueue({
        studio: studioLocked ? '' : studio.trim(),  // server overrides when operator
        suffix: suffix.trim(),
        unit_price_cny: !isNaN(globalUnit) && globalUnit > 0 ? globalUnit : undefined,
        channels,
      })
      setEnqueueMsg({
        ok: true,
        text: `已入队 ${res.inserted} 条${res.skipped ? `（${res.skipped} 条跳过 / 已存在）` : ''}`,
      })
      setInput('')
      void loadPending()
    } catch (e: any) {
      setEnqueueMsg({ ok: false, text: e?.message || String(e) })
    } finally {
      setEnqueueBusy(false)
    }
  }

  const cancelPending = async (row: LocalPendingKey) => {
    if (row.status !== 'pending' && row.status !== 'failed') return
    if (!window.confirm(`删除队列条目 (${row.key_masked})？只能删 pending/failed 的。`)) return
    try {
      await api.localPoolDelete(row.id)
      await loadPending()
    } catch (e: any) {
      alert('删除失败: ' + (e?.message || e))
    }
  }

  // Effective batch size hint (when cfg + rpmNow available).
  const effectiveN = (() => {
    if (!cfg) return null
    if (!cfg.auto_mode) return cfg.pool_batch_size
    if (rpmNow == null) return null
    if (rpmNow < cfg.rpm_min) return 0
    if (cfg.rpm_base <= 0) return cfg.pool_batch_size
    return Math.min(cfg.pool_batch_size, Math.ceil(rpmNow / cfg.rpm_base))
  })()

  return (
    <div className="space-y-4">
      {/* Pool 节流 config bar */}
      <div className="bg-white border border-gray-200 rounded-xl">
        <div className="flex flex-wrap items-center gap-3 px-4 py-2.5">
          <div className="text-[10px] uppercase tracking-wider text-gray-500 font-medium">
            Pool 节流（全局）
          </div>
          <label className="flex items-center gap-1.5 text-xs text-gray-700">
            检查间隔
            <input
              value={cfg?.pool_interval_sec ?? ''}
              onChange={e => { setCfg(c => c && { ...c, pool_interval_sec: parseInt(e.target.value, 10) || 0 }); setCfgDirty(true) }}
              inputMode="numeric"
              className="w-16 border border-gray-300 rounded px-1.5 py-0.5 text-xs tabular-nums text-right focus:outline-none focus:border-gray-900"
            />
            <span className="text-[10px] text-gray-400">秒</span>
          </label>
          <label className="flex items-center gap-1.5 text-xs text-gray-700">
            {cfg?.auto_mode ? '上限' : '每次上'}
            <input
              value={cfg?.pool_batch_size ?? ''}
              onChange={e => { setCfg(c => c && { ...c, pool_batch_size: parseInt(e.target.value, 10) || 0 }); setCfgDirty(true) }}
              inputMode="numeric"
              className="w-14 border border-gray-300 rounded px-1.5 py-0.5 text-xs tabular-nums text-right focus:outline-none focus:border-gray-900"
            />
            <span className="text-[10px] text-gray-400">个 key</span>
          </label>
          <label className="flex items-center gap-1.5 text-xs text-gray-700 border-l border-gray-200 pl-3 ml-1">
            <input
              type="checkbox"
              checked={cfg?.auto_mode ?? false}
              onChange={e => { setCfg(c => c && { ...c, auto_mode: e.target.checked }); setCfgDirty(true) }}
            />
            自动模式
          </label>
          {cfg?.auto_mode && (
            <>
              <label className="flex items-center gap-1.5 text-xs text-gray-700">
                RPM/key
                <input
                  value={cfg.rpm_base}
                  onChange={e => { setCfg(c => c && { ...c, rpm_base: parseInt(e.target.value, 10) || 0 }); setCfgDirty(true) }}
                  inputMode="numeric"
                  className="w-16 border border-gray-300 rounded px-1.5 py-0.5 text-xs tabular-nums text-right focus:outline-none focus:border-gray-900"
                />
              </label>
              <label className="flex items-center gap-1.5 text-xs text-gray-700">
                低于
                <input
                  value={cfg.rpm_min}
                  onChange={e => { setCfg(c => c && { ...c, rpm_min: parseInt(e.target.value, 10) || 0 }); setCfgDirty(true) }}
                  inputMode="numeric"
                  className="w-14 border border-gray-300 rounded px-1.5 py-0.5 text-xs tabular-nums text-right focus:outline-none focus:border-gray-900"
                />
                <span className="text-[10px] text-gray-400">RPM 停</span>
              </label>
            </>
          )}
          <button
            onClick={saveCfg}
            disabled={!cfgDirty || cfgSaving}
            className="bg-gray-900 text-white rounded px-2 py-0.5 text-xs hover:opacity-85 disabled:opacity-40"
          >
            {cfgSaving ? '保存中…' : '保存'}
          </button>
          {cfgMsg && (
            <span className={`text-[11px] ${cfgMsg.ok ? 'text-emerald-600' : 'text-rose-600'}`}>
              {cfgMsg.text}
            </span>
          )}
        </div>
        <div className="px-4 pb-2 text-[10px] text-gray-400 flex flex-wrap gap-4">
          <span>当前 RPM: <span className="tabular-nums text-gray-600">{rpmNow ?? '—'}</span></span>
          <span>下一 tick 上 key 数: <span className="tabular-nums text-gray-600">{effectiveN ?? '—'}</span></span>
          <span>Priority = 存活最高 + 1，逐条累加</span>
        </div>
      </div>

      {/* Enqueue form */}
      <div className="bg-white border border-gray-200 rounded-xl p-4 space-y-3">
        <h3 className="text-sm font-semibold text-gray-900">批量入队上 Key（Pool·本地）</h3>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
          <div>
            <label className="block text-[11px] text-gray-500 mb-1">Studio</label>
            {studioLocked ? (
              <div className="border border-gray-200 rounded-md px-2 py-1.5 text-sm bg-gray-50 font-mono text-gray-700">
                {lockedStudio}
              </div>
            ) : studioMode === 'pick' ? (
              <select
                value={studios.includes(studio) ? studio : ''}
                onChange={e => {
                  if (e.target.value === '__new__') {
                    setStudioMode('new')
                    setStudio('')
                  } else {
                    setStudio(e.target.value)
                  }
                }}
                className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
              >
                {studios.map(s => <option key={s} value={s}>{s}</option>)}
                <option value="__new__">＋ 新建 studio…</option>
              </select>
            ) : (
              <div className="flex gap-1">
                <input
                  value={studio}
                  onChange={e => setStudio(e.target.value)}
                  placeholder="新 studio 名"
                  className="flex-1 border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
                />
                <button
                  onClick={() => { setStudioMode('pick'); setStudio(studios[0] ?? '') }}
                  className="text-[11px] text-gray-500 hover:text-gray-800 px-1"
                >
                  取消
                </button>
              </div>
            )}
          </div>
          <div>
            <label className="block text-[11px] text-gray-500 mb-1">Suffix</label>
            <input
              value={suffix}
              onChange={e => setSuffix(e.target.value)}
              placeholder="例如 alpha"
              className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
            />
          </div>
        </div>
        <div>
          <label className="block text-[11px] text-gray-500 mb-1">
            单价 CNY / USD 额度（可选，默认每行的第 3 列）
          </label>
          <input
            value={unitPrice}
            onChange={e => setUnitPrice(e.target.value)}
            placeholder="例如 3.5"
            className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm focus:outline-none focus:border-gray-900"
          />
        </div>
        <div>
          <label className="block text-[11px] text-gray-500 mb-1">
            Keys（每行：<code>key quota_usd [unit_price_cny]</code>）
          </label>
          <textarea
            value={input}
            onChange={e => setInput(e.target.value)}
            rows={8}
            placeholder={'sk-... 10\nsk-... 20 3.5'}
            className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-[11px] font-mono focus:outline-none focus:border-gray-900"
          />
        </div>
        <div className="flex items-center gap-3">
          <button
            onClick={submitEnqueue}
            disabled={enqueueBusy}
            className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-sm hover:opacity-85 disabled:opacity-40"
          >
            {enqueueBusy ? '入队中…' : '入队到 Pool'}
          </button>
          {enqueueMsg && (
            <span className={`text-[11px] ${enqueueMsg.ok ? 'text-emerald-600' : 'text-rose-600'}`}>
              {enqueueMsg.text}
            </span>
          )}
        </div>
      </div>

      {/* Queue */}
      <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
        <div className="flex items-center justify-between px-4 py-2.5 border-b border-gray-100">
          <div>
            <div className="text-sm font-semibold text-gray-900">Pool 队列</div>
            <div className="text-[11px] text-gray-400 mt-0.5">
              {pending.filter(p => p.status === 'pending').length} pending ·{' '}
              {pending.filter(p => p.status === 'active').length} active ·{' '}
              {pending.filter(p => p.status === 'used').length} used ·{' '}
              <span className={pending.filter(p => p.status === 'failed').length > 0 ? 'text-rose-600' : ''}>
                {pending.filter(p => p.status === 'failed').length} failed
              </span>
              {' · '}每 30s 自动刷新
            </div>
          </div>
          <button
            onClick={() => void loadPending()}
            className="text-xs text-gray-600 border border-gray-300 rounded-md px-2 py-1 hover:bg-gray-50"
          >
            刷新
          </button>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead className="bg-gray-50 border-b border-gray-100 text-gray-500">
              <tr>
                <th className="px-3 py-2 text-left font-medium">ID</th>
                <th className="px-3 py-2 text-left font-medium">Studio</th>
                <th className="px-3 py-2 text-left font-medium">Key</th>
                <th className="px-3 py-2 text-left font-medium">Status</th>
                <th className="px-3 py-2 text-right font-medium">Priority</th>
                <th className="px-3 py-2 text-right font-medium">Quota</th>
                <th className="px-3 py-2 text-right font-medium">Channel</th>
                <th className="px-3 py-2 text-right font-medium">Try</th>
                <th className="px-3 py-2 text-left font-medium">Error / 更新</th>
                <th className="px-3 py-2 text-right font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {pending.length === 0 ? (
                <tr>
                  <td colSpan={10} className="px-4 py-6 text-center text-xs text-gray-400">
                    队列为空
                  </td>
                </tr>
              ) : (
                pending.map(row => (
                  <tr key={row.id} className="border-b border-gray-100 hover:bg-gray-50">
                    <td className="px-3 py-2 tabular-nums">{row.id}</td>
                    <td className="px-3 py-2 font-mono text-[11px]">{row.studio}</td>
                    <td className="px-3 py-2 font-mono text-[11px]">{row.key_masked}</td>
                    <td className="px-3 py-2">
                      <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] ${STATUS_CLS[row.status]}`}>
                        {row.status}
                      </span>
                    </td>
                    <td className="px-3 py-2 tabular-nums text-right">{row.priority || '—'}</td>
                    <td className="px-3 py-2 tabular-nums text-right">
                      {row.quota_usd > 0 ? '$' + row.quota_usd.toFixed(2) : '—'}
                    </td>
                    <td className="px-3 py-2 tabular-nums text-right">
                      {row.channel_id > 0 ? row.channel_id : '—'}
                    </td>
                    <td className="px-3 py-2 tabular-nums text-right">{row.attempts}</td>
                    <td className="px-3 py-2 text-[10px] text-gray-500 max-w-[240px] truncate" title={row.failed_reason || fmtTime(row.updated_at)}>
                      {row.failed_reason
                        ? <span className="text-rose-600">{row.failed_reason}</span>
                        : fmtTime(row.updated_at)}
                    </td>
                    <td className="px-3 py-2 text-right">
                      {(row.status === 'pending' || row.status === 'failed') ? (
                        <button
                          onClick={() => void cancelPending(row)}
                          className="text-[11px] text-rose-500 hover:text-rose-700"
                        >
                          删除
                        </button>
                      ) : (
                        <span className="text-[10px] text-gray-300">—</span>
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
  )
}
