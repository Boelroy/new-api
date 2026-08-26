import { useEffect, useRef, useState } from 'react'
import Layout from '../components/Layout'
import { Button, Card } from '../components/ui'
import { toast } from '../components/feedback'
import { api, type ModelCleanupConfig, type ModelCleanupEvent, type ModelCleanupStat, type ModelCleanupSummary } from '../api'

// Model Cleanup: configure the log-driven auto-removal of dead models from
// channels. A model is stripped from a channel when, in the last window, that
// channel's traffic for it was all failure (0 success + >=1 error containing
// the configured substring, default "Operation not allowed"). Channel stays
// enabled; only the dead model is removed.

const inputCls = 'border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900'

// The backend may serialize an unset list as JSON null; coerce so the UI never
// calls .length/.includes/.filter on null.
function normalizeCfg(c: ModelCleanupConfig): ModelCleanupConfig {
  return { ...c, models: c.models ?? [], groups: c.groups ?? [], error_substrs: c.error_substrs ?? [] }
}

function fmtTime(unix: number): string {
  if (!unix) return '—'
  const d = new Date(unix * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

const KIND_LABEL: Record<string, string> = {
  removed: '已下线',
  would_remove: '将下线(dry-run)',
  skip_last_model: '跳过(唯一模型)',
  error: '失败',
}

function ChipList({ items, onRemove, tone }: { items: string[]; onRemove: (v: string) => void; tone: 'brand' | 'slate' }) {
  const cls = tone === 'brand'
    ? 'bg-brand-50 text-brand'
    : 'bg-gray-100 text-gray-700'
  return (
    <div className="flex flex-wrap gap-1.5 mb-2 min-h-[1.5rem]">
      {items.length === 0 && <span className="text-[11px] text-gray-400">（空）</span>}
      {items.map(v => (
        <span key={v} className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-mono ${cls}`}>
          {v}
          <button type="button" onClick={() => onRemove(v)} title="移除" className="opacity-60 hover:opacity-100">✕</button>
        </span>
      ))}
    </div>
  )
}

export default function ModelCleanup() {
  const [cfg, setCfg] = useState<ModelCleanupConfig | null>(null)
  const [saving, setSaving] = useState(false)
  const [running, setRunning] = useState(false)
  const [events, setEvents] = useState<ModelCleanupEvent[]>([])
  const [stats, setStats] = useState<ModelCleanupStat[]>([])
  const [modelInput, setModelInput] = useState('')
  const [groupInput, setGroupInput] = useState('')
  const [kwInput, setKwInput] = useState('')
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const loadEvents = async () => {
    try {
      const res = await api.modelCleanupEvents({ limit: 200 })
      setEvents(res.events)
    } catch (e) {
      // silent — the config load surfaces the real errors
    }
  }

  const loadStats = async () => {
    try {
      const res = await api.modelCleanupStats()
      setStats(res.stats ?? [])
    } catch (e) {
      // silent
    }
  }

  const refresh = () => { void loadEvents(); void loadStats() }

  useEffect(() => {
    void (async () => {
      try {
        setCfg(normalizeCfg(await api.modelCleanupGetConfig()))
      } catch (e) {
        toast.error(e instanceof Error ? e.message : String(e))
      }
    })()
    refresh()
    pollRef.current = setInterval(refresh, 15000)
    return () => { if (pollRef.current) clearInterval(pollRef.current) }
  }, [])

  // Persist a partial config change and adopt the server's clamped result.
  const save = async (patch: Partial<ModelCleanupConfig>) => {
    setSaving(true)
    try {
      const next = normalizeCfg(await api.modelCleanupSetConfig(patch))
      setCfg(next)
      return next
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
      // Re-pull to discard the optimistic edit.
      try { setCfg(normalizeCfg(await api.modelCleanupGetConfig())) } catch { /* ignore */ }
      return null
    } finally {
      setSaving(false)
    }
  }

  const runNow = async () => {
    setRunning(true)
    try {
      const res = await api.modelCleanupRunNow()
      const s: ModelCleanupSummary = res.summary
      toast.success(
        `${res.dry_run ? '演练' : '执行'}完成：命中 ${s.scanned}，${res.dry_run ? `将下线 ${s.would_remove}` : `已下线 ${s.removed}`}，跳过 ${s.skipped}，失败 ${s.errors}`,
      )
      refresh()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setRunning(false)
    }
  }

  // Highlight the models the loop is configured to police.
  const watched = new Set(cfg?.models ?? [])

  const addModel = () => {
    const v = modelInput.trim()
    if (v && cfg && !cfg.models.includes(v)) void save({ models: [...cfg.models, v] })
    setModelInput('')
  }
  const addGroup = () => {
    const v = groupInput.trim()
    if (v && cfg && !cfg.groups.includes(v)) void save({ groups: [...cfg.groups, v] })
    setGroupInput('')
  }
  const addKeyword = () => {
    const v = kwInput.trim()
    if (v && cfg && !cfg.error_substrs.includes(v)) void save({ error_substrs: [...cfg.error_substrs, v] })
    setKwInput('')
  }

  if (!cfg) {
    return (
      <Layout title="模型自动下线" subtitle="日志驱动 · 剔除持续报错的坏模型">
        <div className="text-sm text-secondary">加载中…</div>
      </Layout>
    )
  }

  return (
    <Layout
      title="模型自动下线"
      subtitle="按真实日志:某模型在渠道上近窗口内全报错(0 成功)则从该渠道剔除,渠道保持启用"
      actions={
        <>
          <Button variant="outline" size="sm" onClick={runNow} disabled={running || !cfg.token_configured}>
            {running ? '执行中…' : '立即执行一次'}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {!cfg.token_configured && (
          <div className="rounded-lg border border-amber-300 bg-amber-50 px-3 py-2 text-xs text-amber-800">
            未配置 MAIN_SERVICE_URL / MAIN_SERVICE_TOKEN,调度器无法改动渠道。请在部署环境设置后重启。
          </div>
        )}

        <Card className="p-5">
          <div className="flex items-center justify-between mb-3">
            <div className="text-sm font-semibold">调度</div>
            <label className="flex items-center gap-2 text-xs">
              <input
                type="checkbox"
                checked={cfg.enabled}
                disabled={saving || (!cfg.enabled && !cfg.token_configured)}
                onChange={e => void save({ enabled: e.target.checked })}
              />
              <span className={cfg.enabled ? 'text-green-700 font-medium' : 'text-gray-500'}>
                {cfg.enabled ? '已开启' : '已关闭'}
              </span>
            </label>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
            <label className="block">
              <span className="mono-label block mb-1">窗口 (秒)</span>
              <input
                type="number" className={inputCls + ' w-full'} defaultValue={cfg.window_sec} min={30} max={3600}
                onBlur={e => { const n = Number(e.target.value); if (n !== cfg.window_sec) void save({ window_sec: n }) }}
              />
              <span className="text-[10px] text-gray-400">该窗口内 0 成功 + 报错才算坏</span>
            </label>
            <label className="block">
              <span className="mono-label block mb-1">检查间隔 (秒)</span>
              <input
                type="number" className={inputCls + ' w-full'} defaultValue={cfg.tick_sec} min={15} max={600}
                onBlur={e => { const n = Number(e.target.value); if (n !== cfg.tick_sec) void save({ tick_sec: n }) }}
              />
            </label>
            <label className="block">
              <span className="mono-label block mb-1">每轮最多下线数</span>
              <input
                type="number" className={inputCls + ' w-full'} defaultValue={cfg.max_actions} min={1} max={500}
                onBlur={e => { const n = Number(e.target.value); if (n !== cfg.max_actions) void save({ max_actions: n }) }}
              />
            </label>
          </div>

          <div className="mt-4 grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div className="block">
              <span className="mono-label block mb-1">错误关键字(命中任一即算)</span>
              <ChipList items={cfg.error_substrs} tone="slate" onRemove={v => void save({ error_substrs: cfg.error_substrs.filter(x => x !== v) })} />
              <div className="flex gap-1">
                <input
                  value={kwInput} onChange={e => setKwInput(e.target.value)}
                  onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); addKeyword() } }}
                  placeholder="回车添加,例 Operation not allowed"
                  className={inputCls + ' flex-1 font-mono'}
                />
                <Button variant="outline" size="sm" onClick={addKeyword}>添加</Button>
              </div>
              <span className="text-[10px] text-gray-400 mt-1 block">报错内容包含其中任意一个关键字即视为该模型不可用(大小写不敏感)</span>
            </div>
            <label className="flex items-center gap-2 mt-6 text-xs">
              <input type="checkbox" checked={cfg.dry_run} disabled={saving} onChange={e => void save({ dry_run: e.target.checked })} />
              <span className={cfg.dry_run ? 'text-amber-700 font-medium' : 'text-gray-500'}>
                演练模式 (dry-run):只记录不真删
              </span>
            </label>
          </div>
        </Card>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <Card className="p-5">
            <div className="text-sm font-semibold mb-2">检查的模型</div>
            <ChipList items={cfg.models} tone="brand" onRemove={v => void save({ models: cfg.models.filter(x => x !== v) })} />
            <div className="flex gap-1">
              <input
                value={modelInput} onChange={e => setModelInput(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); addModel() } }}
                placeholder="回车添加,例 claude-opus-5"
                className={inputCls + ' flex-1 font-mono'}
              />
              <Button variant="outline" size="sm" onClick={addModel}>添加</Button>
            </div>
          </Card>

          <Card className="p-5">
            <div className="text-sm font-semibold mb-2">检查的分组</div>
            <ChipList items={cfg.groups} tone="slate" onRemove={v => void save({ groups: cfg.groups.filter(x => x !== v) })} />
            <div className="flex gap-1">
              <input
                value={groupInput} onChange={e => setGroupInput(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); addGroup() } }}
                placeholder="回车添加;留空=全部分组"
                className={inputCls + ' flex-1 font-mono'}
              />
              <Button variant="outline" size="sm" onClick={addGroup}>添加</Button>
            </div>
            <span className="text-[10px] text-gray-400 mt-1 block">空 = 不限分组,检查所有渠道</span>
          </Card>
        </div>

        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="px-4 py-2 border-b border-gray-100 text-[11px] font-medium text-gray-700 flex items-center justify-between">
            <span>各模型当前活跃(启用)渠道数</span>
            <button className="text-[11px] text-gray-400 hover:text-gray-700" onClick={() => void loadStats()}>刷新</button>
          </div>
          <div className="p-3">
            {stats.length === 0
              ? <div className="text-xs text-gray-400 px-1 py-2">暂无数据</div>
              : (
                <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-2">
                  {stats.map(s => (
                    <div
                      key={s.model}
                      className={`flex items-center justify-between rounded-lg border px-3 py-2 ${
                        watched.has(s.model) ? 'border-brand-200 bg-brand-50' : 'border-gray-200 bg-gray-50'
                      }`}
                      title={watched.has(s.model) ? '已纳入自动清理' : ''}
                    >
                      <span className="font-mono text-[11px] text-gray-700 truncate mr-2">{s.model}</span>
                      <span className="tabular-nums text-sm font-semibold text-ink">{s.enabled_channels}</span>
                    </div>
                  ))}
                </div>
              )}
          </div>
        </div>

        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="px-4 py-2 border-b border-gray-100 text-[11px] font-medium text-gray-700 flex items-center justify-between">
            <span>最近动作</span>
            <button className="text-[11px] text-gray-400 hover:text-gray-700" onClick={() => void loadEvents()}>刷新</button>
          </div>
          <div className="overflow-x-auto max-h-[46vh] overflow-y-auto">
            <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
              <thead>
                <tr>
                  {['时间', '动作', '渠道', '模型', '分组', '错误数', '详情'].map(h => (
                    <th key={h} className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {events.length === 0 && (
                  <tr><td colSpan={7} className="px-3 py-6 text-center text-gray-400">暂无记录</td></tr>
                )}
                {events.map(ev => (
                  <tr key={ev.id} className="hover:bg-gray-50">
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 tabular-nums">{fmtTime(ev.created_at)}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50">
                      <span className={
                        ev.kind === 'removed' ? 'text-green-700'
                          : ev.kind === 'would_remove' ? 'text-amber-700'
                          : ev.kind === 'error' ? 'text-red-600'
                          : 'text-gray-500'
                      }>{KIND_LABEL[ev.kind] ?? ev.kind}</span>
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums">
                      <span className="text-gray-400">{ev.channel_id}</span> {ev.channel_name}
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50 font-mono">{ev.model}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{ev.group || '—'}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums text-gray-600">{ev.err_count}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 max-w-[380px] truncate" title={ev.detail}>{ev.detail}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </Layout>
  )
}
