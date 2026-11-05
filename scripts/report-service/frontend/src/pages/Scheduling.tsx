import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import Layout from '../components/Layout'
import { Card } from '../components/ui'
import { toast, confirmDialog } from '../components/feedback'
import { api, type StreamGuardConfig, type StreamGuardEvent } from '../api'
import { ModelCooldownPanel } from './RemoteModelCooldown'
import { ChannelPriorityPanel, NumField } from './ChannelPriority'

// 调度: the three automatic traffic schedulers as tabs of one page — 429 model
// cooldown, 429 channel-priority tuning, and the stream-failure guard.

function fmtTime(unix: number): string {
  if (!unix) return '—'
  const d = new Date(unix * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

const ACTION_LABEL: Record<string, string> = {
  'strip_model+demote': '下模型 + 降优先级',
  strip_model: '下模型',
  demote: '降优先级',
}

const CHANNEL_STATUS: Record<number, { text: string; cls: string }> = {
  1: { text: '启用', cls: 'text-emerald-600' },
  2: { text: '手动禁用', cls: 'text-gray-500' },
  3: { text: '自动禁用', cls: 'text-rose-600' },
}

function StreamGuardPanel() {
  const [events, setEvents] = useState<StreamGuardEvent[]>([])
  const [cfg, setCfg] = useState<StreamGuardConfig | null>(null)
  const [saving, setSaving] = useState(false)
  // Bumped on every save so the inputs remount with the stored (clamped) value.
  const [rev, setRev] = useState(0)
  const [busy, setBusy] = useState<number | null>(null)

  const load = useCallback(async () => {
    try {
      const res = await api.streamGuardEvents()
      setEvents(res.events)
    } catch (e) {
      toast.error(e)
    }
  }, [])

  useEffect(() => {
    api.streamGuardConfig().then(setCfg).catch(e => toast.error(e))
  }, [])

  const save = async (patch: Partial<StreamGuardConfig>) => {
    setSaving(true)
    try {
      setCfg(await api.saveStreamGuardConfig(patch))
      setRev(r => r + 1)
      toast.success('已保存，下一轮检查生效')
    } catch (e) {
      toast.error(e)
    } finally {
      setSaving(false)
    }
  }

  useEffect(() => {
    void load()
    const t = setInterval(load, 30000)
    return () => clearInterval(t)
  }, [load])

  const restore = async (ev: StreamGuardEvent) => {
    const what = ev.action.includes('strip_model') ? `把 ${ev.model} 加回渠道 ${ev.channel_id}，` : `渠道 ${ev.channel_id} `
    if (!(await confirmDialog({ message: `${what}优先级恢复到 ${ev.old_priority}？`, confirmText: '恢复' }))) return
    setBusy(ev.id)
    try {
      const res = await api.streamGuardRestore(ev.id)
      toast.success(`已恢复，当前优先级 ${res.priority}`)
      await load()
    } catch (e) {
      toast.error(e)
    } finally {
      setBusy(null)
    }
  }

  const pending = events.filter(e => !e.restored_at).length

  return (
    <>
      <p className="text-xs text-gray-500 mb-4">
        Key 快没额度时 OpenAI 会先在流里返回 response.failed（下游看到的就是断流），之后才回 429。命中规则的 (渠道, 模型) 会被下掉该模型并降低渠道优先级，把流量提前切到健康的 Key。
      </p>
      <div className="space-y-4">
        <Card className="p-5">
          <div className="flex items-center justify-between mb-3">
            <div className="text-sm font-semibold">规则</div>
            {cfg && (
              <label className="flex items-center gap-2 text-xs">
                <input type="checkbox" checked={cfg.enabled} disabled={saving} onChange={e => void save({ enabled: e.target.checked })} />
                <span className={cfg.enabled ? 'text-green-700 font-medium' : 'text-gray-500'}>{cfg.enabled ? '已开启' : '已关闭'}</span>
              </label>
            )}
          </div>
          {cfg ? (
            <>
              <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-4">
                <NumField key={`t${rev}`} label="检查间隔 (秒)" value={cfg.tick_sec} min={5} max={3600}
                  onSave={n => void save({ tick_sec: n })} />
                <NumField key={`w${rev}`} label="统计窗口 (秒)" value={cfg.window_sec} min={30} max={21600}
                  hint="看最近这么久的流式请求" onSave={n => void save({ window_sec: n })} />
                <NumField key={`s${rev}`} label="最少流数" value={cfg.min_streams} min={1} max={100000}
                  hint="窗口内该渠道该模型至少这么多条流" onSave={n => void save({ min_streams: n })} />
                <NumField key={`f${rev}`} label="最少失败数" value={cfg.min_failed} min={1} max={100000}
                  hint="response.failed 至少这么多条" onSave={n => void save({ min_failed: n })} />
                <NumField key={`r${rev}`} label="失败率阈值 (%)" value={cfg.min_fail_rate_pct} min={1} max={100}
                  onSave={n => void save({ min_fail_rate_pct: n })} />
                <NumField key={`ws${rev}`} label="整体故障占比 (%)" value={cfg.wide_share_pct} min={1} max={100}
                  hint="同一模型这么多比例的渠道同时失败 → 不处理" onSave={n => void save({ wide_share_pct: n })} />
                <NumField key={`wc${rev}`} label="整体故障最少渠道数" value={cfg.wide_min_chans} min={1} max={1000}
                  hint="且至少这么多个渠道" onSave={n => void save({ wide_min_chans: n })} />
                <NumField key={`p${rev}`} label="降到的优先级" value={cfg.demote_priority} min={-100000} max={100000}
                  hint="newapi 优先级越高越先用" onSave={n => void save({ demote_priority: n })} />
              </div>
              <div className="flex items-center gap-6 mt-4 text-xs">
                <label className="flex items-center gap-2">
                  <input type="checkbox" checked={cfg.strip_model} disabled={saving} onChange={e => void save({ strip_model: e.target.checked })} />
                  <span>命中后下掉该模型（渠道只剩这一个模型时不下）</span>
                </label>
                <label className="flex items-center gap-2">
                  <input type="checkbox" checked={cfg.demote} disabled={saving} onChange={e => void save({ demote: e.target.checked })} />
                  <span>命中后降低渠道优先级</span>
                </label>
              </div>
              <p className="text-[11px] text-gray-400 mt-3">只在 leader 节点执行；改动约 60 秒内同步到 newapi。不会自动恢复，误判时在下方点「恢复」。</p>
            </>
          ) : <div className="text-xs text-gray-400">加载中…</div>}
        </Card>

        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="px-4 py-2 border-b border-gray-100 text-[11px] font-medium text-gray-700 flex items-center justify-between">
            <span>处理记录（{events.length}，未恢复 {pending}）</span>
            <button className="text-[11px] text-gray-400 hover:text-gray-700" onClick={() => void load()}>刷新</button>
          </div>
          <div className="overflow-x-auto max-h-[70vh] overflow-y-auto">
            <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
              <thead>
                <tr>
                  {['时间', '渠道', '模型', '动作', '失败 / 流', '优先级', '渠道当前', ''].map(h => (
                    <th key={h} className="sticky top-0 z-10 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {events.length === 0 && (
                  <tr><td colSpan={8} className="px-3 py-6 text-center text-gray-400">暂无处理记录</td></tr>
                )}
                {events.map(ev => {
                  const st = CHANNEL_STATUS[ev.channel_status] ?? { text: `状态 ${ev.channel_status}`, cls: 'text-gray-400' }
                  return (
                    <tr key={ev.id} className={ev.restored_at ? 'text-gray-400' : 'hover:bg-gray-50'}>
                      <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums">{fmtTime(ev.created_at)}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50">
                        <span className="tabular-nums">{ev.channel_id}</span>
                        <span className="text-gray-500 ml-1.5">{ev.channel_name}</span>
                      </td>
                      <td className="px-3 py-1.5 border-b border-gray-50 font-mono">{ev.model}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50">{ACTION_LABEL[ev.action] ?? ev.action}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums">
                        <span className={ev.restored_at ? '' : 'text-rose-600 font-medium'}>{ev.failed}</span> / {ev.streams}
                        <span className="text-gray-400 ml-1">({Math.round((ev.failed / Math.max(1, ev.streams)) * 100)}%)</span>
                      </td>
                      <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums">{ev.old_priority} → {ev.new_priority}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50">
                        <span className={st.cls}>{st.text}</span>
                        <span className="text-gray-400 ml-1.5 tabular-nums">优先级 {ev.channel_priority}</span>
                        {ev.action.includes('strip_model') && (
                          <span className="ml-1.5 text-gray-400">{ev.model_present ? '· 模型在' : '· 模型已下'}</span>
                        )}
                      </td>
                      <td className="px-3 py-1.5 border-b border-gray-50 text-right">
                        {ev.restored_at ? (
                          <span className="text-[11px]">已恢复 {fmtTime(ev.restored_at)}</span>
                        ) : (
                          <button
                            onClick={() => void restore(ev)}
                            disabled={busy === ev.id}
                            className="rounded-md border border-gray-300 bg-white px-2 py-0.5 text-[11px] text-gray-700 hover:bg-gray-50 disabled:opacity-50"
                          >
                            {busy === ev.id ? '恢复中…' : '恢复'}
                          </button>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </>
  )
}

function TierGuardPanel() {
  const [cfg, setCfg] = useState<{ enabled: boolean; max_tier: number } | null>(null)
  const [items, setItems] = useState<Awaited<ReturnType<typeof api.keyTierGuardDisabled>>['items']>([])

  const load = useCallback(async () => {
    try {
      setItems((await api.keyTierGuardDisabled()).items)
    } catch (e) {
      toast.error(e)
    }
  }, [])

  useEffect(() => {
    api.keyTierGuardConfig().then(setCfg).catch(e => toast.error(e))
    void load()
    const t = setInterval(load, 30000)
    return () => clearInterval(t)
  }, [load])

  const save = async (patch: { enabled?: boolean; max_tier?: number }) => {
    try {
      setCfg(await api.saveKeyTierGuardConfig(patch))
      toast.success('已保存，下一轮巡检生效')
    } catch (e) {
      toast.error(e)
    }
  }

  return (
    <>
      <p className="text-xs text-gray-500 mb-4">
        后台每 5 分钟探测 OpenAI Key 的 Tier（见 Key Capacity 的 Tier 列），Tier 不高于设定值的 Key 直接禁用（手动禁用状态，newapi 不会自动启用回来）。
      </p>
      <div className="space-y-4">
        <Card className="p-5">
          <div className="flex items-center justify-between mb-3">
            <div className="text-sm font-semibold">规则</div>
            {cfg && (
              <label className="flex items-center gap-2 text-xs">
                <input type="checkbox" checked={cfg.enabled} onChange={e => void save({ enabled: e.target.checked })} />
                <span className={cfg.enabled ? 'text-green-700 font-medium' : 'text-gray-500'}>{cfg.enabled ? '已开启' : '已关闭'}</span>
              </label>
            )}
          </div>
          {cfg ? (
            <label className="flex items-center gap-2 text-xs">
              <span>禁用 Tier ≤</span>
              <select
                value={cfg.max_tier}
                onChange={e => void save({ max_tier: Number(e.target.value) })}
                className="border border-gray-200 rounded-md px-2 py-1 bg-gray-50"
              >
                {[0, 1, 2, 3, 4].map(t => <option key={t} value={t}>{t === 0 ? 'Free' : `T${t}`}</option>)}
              </select>
              <span className="text-gray-400">的 OpenAI Key</span>
            </label>
          ) : <div className="text-xs text-gray-400">加载中…</div>}
        </Card>

        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="px-4 py-2 border-b border-gray-100 text-[11px] font-medium text-gray-700 flex items-center justify-between">
            <span>已禁用（{items.length}）</span>
            <button className="text-[11px] text-gray-400 hover:text-gray-700" onClick={() => void load()}>刷新</button>
          </div>
          <div className="overflow-x-auto max-h-[70vh] overflow-y-auto">
            <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
              <thead>
                <tr>
                  {['禁用时间', '渠道', 'Tier', 'gpt-4o-mini 限额', '渠道当前'].map(h => (
                    <th key={h} className="sticky top-0 z-10 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {items.length === 0 && <tr><td colSpan={5} className="px-3 py-6 text-center text-gray-400">暂无</td></tr>}
                {items.map(it => {
                  const st = CHANNEL_STATUS[it.channel_status] ?? { text: `状态 ${it.channel_status}`, cls: 'text-gray-400' }
                  return (
                    <tr key={it.channel_id} className="hover:bg-gray-50">
                      <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums">{fmtTime(it.disabled_at)}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50">
                        <span className="tabular-nums">{it.channel_id}</span>
                        <span className="text-gray-500 ml-1.5">{it.channel_name}</span>
                      </td>
                      <td className="px-3 py-1.5 border-b border-gray-50 font-semibold">{it.tier === 0 ? 'Free' : `T${it.tier}`}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums">{it.limit_rpm.toLocaleString()} RPM / {it.limit_tpm.toLocaleString()} TPM</td>
                      <td className={`px-3 py-1.5 border-b border-gray-50 ${st.cls}`}>{st.text}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </>
  )
}

type Tab = 'cooldown' | 'priority' | 'stream' | 'tier'

const TABS: { id: Tab; label: string }[] = [
  { id: 'cooldown', label: '模型限流冷却' },
  { id: 'priority', label: '渠道优先级自调' },
  { id: 'stream', label: '断流处理' },
  { id: 'tier', label: 'Tier 巡检' },
]

export default function Scheduling() {
  const [params, setParams] = useSearchParams()
  const tab = (TABS.some(t => t.id === params.get('tab')) ? params.get('tab') : 'cooldown') as Tab

  return (
    <Layout title="调度" subtitle="限流冷却、优先级自调、断流处理、Tier 巡检 —— 自动把流量从有问题的 Key 上挪走">
      <div className="flex items-center gap-1 border-b border-gray-200 mb-4">
        {TABS.map(t => (
          <button
            key={t.id}
            onClick={() => setParams({ tab: t.id }, { replace: true })}
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
      {tab === 'cooldown' && <ModelCooldownPanel />}
      {tab === 'priority' && <ChannelPriorityPanel />}
      {tab === 'stream' && <StreamGuardPanel />}
      {tab === 'tier' && <TierGuardPanel />}
    </Layout>
  )
}
