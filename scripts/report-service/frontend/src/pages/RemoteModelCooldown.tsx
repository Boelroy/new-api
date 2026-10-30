import { useEffect, useState } from 'react'
import Layout from '../components/Layout'
import { Button, Card } from '../components/ui'
import { confirmDialog, toast } from '../components/feedback'
import {
  api,
  type RemoteCooldownActive,
  type RemoteCooldownConfig,
  type RemoteCooldownEvent,
  type RemoteProfile,
} from '../api'

// Remote Cooldown: when a remote channel keeps returning upstream
// "Rate limit reached for <model> … (TPM)" 429s, the model is stripped from
// that channel and re-added after a cooldown derived from the reported limit
// (one limit window + overflow drain, doubled on repeat trips, capped).

const inputCls = 'border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900'

// The backend may serialize an unset list as JSON null.
function normalizeCfg(c: RemoteCooldownConfig): RemoteCooldownConfig {
  return { ...c, profile_ids: c.profile_ids ?? [], models: c.models ?? [] }
}

function fmtTime(unix: number): string {
  if (!unix) return '—'
  const d = new Date(unix * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function fmtDuration(sec: number): string {
  if (sec <= 0) return '0s'
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  if (h > 0) return `${h}h${m}m`
  if (m > 0) return `${m}m${s}s`
  return `${s}s`
}

const ACTION_LABEL: Record<string, string> = {
  strip: '下线',
  restore: '恢复',
  manual_restore: '手动恢复',
  skip_floor: '跳过(保底)',
  skip_last_model: '跳过(唯一模型)',
  error: '失败',
}

const ACTION_CLS: Record<string, string> = {
  strip: 'text-amber-700',
  restore: 'text-green-700',
  manual_restore: 'text-green-700',
  error: 'text-red-600',
}

const NUM_FIELDS: { key: keyof RemoteCooldownConfig; label: string; hint: string; min: number; max: number }[] = [
  { key: 'window_sec', label: '统计窗口 (秒)', hint: '在该窗口内累计 429 次数', min: 30, max: 3600 },
  { key: 'min_hits', label: '触发次数', hint: '窗口内限流 429 ≥ 该值才下线', min: 1, max: 1000 },
  { key: 'min_cooldown_sec', label: '最短冷却 (秒)', hint: '冷却不会短于该值', min: 10, max: 86400 },
  { key: 'max_cooldown_sec', label: '最长冷却 (秒)', hint: '连续触发翻倍的上限(按天限额最长 24h)', min: 60, max: 86400 },
  { key: 'escalate_window_sec', label: '升级窗口 (秒)', hint: '恢复后该时间内再次触发则冷却翻倍;0=不翻倍', min: 0, max: 86400 },
  { key: 'min_keep', label: '保底渠道数', hint: '同分组至少保留这么多启用渠道提供该模型', min: 0, max: 100 },
  { key: 'tick_sec', label: '检查间隔 (秒)', hint: '错误日志同步后也会立即检查', min: 10, max: 600 },
  { key: 'max_actions', label: '每轮最多动作', hint: '下线 + 恢复合计', min: 1, max: 500 },
]

export default function RemoteModelCooldown() {
  const [cfg, setCfg] = useState<RemoteCooldownConfig | null>(null)
  const [profiles, setProfiles] = useState<RemoteProfile[]>([])
  const [saving, setSaving] = useState(false)
  const [running, setRunning] = useState(false)
  const [active, setActive] = useState<RemoteCooldownActive[]>([])
  // Server clock minus local clock, so countdowns don't drift with a skewed browser.
  const [clockSkew, setClockSkew] = useState(0)
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000))
  const [events, setEvents] = useState<RemoteCooldownEvent[]>([])
  const [modelInput, setModelInput] = useState('')

  const loadActive = async () => {
    try {
      const res = await api.remoteCooldownActive()
      setActive(res.items ?? [])
      setClockSkew(res.now - Math.floor(Date.now() / 1000))
    } catch {
      // silent — the config load surfaces the real errors
    }
  }

  const loadEvents = async () => {
    try {
      const res = await api.remoteCooldownEvents({ limit: 200 })
      setEvents(res.events ?? [])
    } catch {
      // silent
    }
  }

  const refresh = () => { void loadActive(); void loadEvents() }

  useEffect(() => {
    void (async () => {
      try {
        const [c, p] = await Promise.all([api.remoteCooldownGetConfig(), api.remoteProfiles()])
        setCfg(normalizeCfg(c))
        setProfiles(p.profiles ?? [])
      } catch (e) {
        toast.error(e instanceof Error ? e.message : String(e))
      }
    })()
    refresh()
    const poll = setInterval(refresh, 10000)
    const tick = setInterval(() => setNowSec(Math.floor(Date.now() / 1000)), 1000)
    return () => { clearInterval(poll); clearInterval(tick) }
  }, [])

  // Persist a partial config change and adopt the server's clamped result.
  const save = async (patch: Partial<RemoteCooldownConfig>) => {
    setSaving(true)
    try {
      setCfg(normalizeCfg(await api.remoteCooldownSetConfig(patch)))
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
      try { setCfg(normalizeCfg(await api.remoteCooldownGetConfig())) } catch { /* ignore */ }
    } finally {
      setSaving(false)
    }
  }

  const runNow = async () => {
    setRunning(true)
    try {
      const res = await api.remoteCooldownRunNow()
      const s = res.summary
      toast.success(
        `${res.dry_run ? '演练' : '执行'}完成:命中 ${s.candidates},下线 ${s.stripped},恢复 ${s.restored},跳过 ${s.skipped},失败 ${s.errors}`,
      )
      refresh()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setRunning(false)
    }
  }

  const restoreNow = async (row: RemoteCooldownActive) => {
    if (!(await confirmDialog({ message: `立即把 ${row.model} 加回渠道 ${row.channel_id} (${row.channel_name})?`, confirmText: '恢复' }))) return
    try {
      await api.remoteCooldownRestore({ profile_id: row.profile_id, channel_id: row.channel_id, model: row.model })
      toast.success('已恢复')
      refresh()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    }
  }

  const addModel = () => {
    const v = modelInput.trim()
    if (v && cfg && !cfg.models.includes(v)) void save({ models: [...cfg.models, v] })
    setModelInput('')
  }

  const profileName = (id: number) => profiles.find(p => p.id === id)?.name ?? `#${id}`

  if (!cfg) {
    return (
      <Layout title="远程模型限流冷却" subtitle="429 限流自动下线模型,冷却后加回">
        <div className="text-sm text-secondary">加载中…</div>
      </Layout>
    )
  }

  const serverNow = nowSec + clockSkew

  return (
    <Layout
      title="远程模型限流冷却"
      subtitle="远程渠道出现「Rate limit reached」429 时,把该模型从该渠道下线,按 TPM/RPM 限额冷却后自动加回"
      actions={
        <Button variant="outline" size="sm" onClick={runNow} disabled={running}>
          {running ? '执行中…' : '立即执行一次'}
        </Button>
      }
    >
      <div className="space-y-4">
        <Card className="p-5">
          <div className="flex items-center justify-between mb-3">
            <div className="text-sm font-semibold">调度</div>
            <div className="flex items-center gap-4">
              <label className="flex items-center gap-2 text-xs">
                <input type="checkbox" checked={cfg.dry_run} disabled={saving} onChange={e => void save({ dry_run: e.target.checked })} />
                <span className={cfg.dry_run ? 'text-amber-700 font-medium' : 'text-gray-500'}>演练模式 (只记录不改渠道)</span>
              </label>
              <label className="flex items-center gap-2 text-xs">
                <input type="checkbox" checked={cfg.enabled} disabled={saving} onChange={e => void save({ enabled: e.target.checked })} />
                <span className={cfg.enabled ? 'text-green-700 font-medium' : 'text-gray-500'}>{cfg.enabled ? '已开启' : '已关闭'}</span>
              </label>
            </div>
          </div>

          <div className="mb-4">
            <span className="mono-label block mb-1">生效的远程 Profile</span>
            <div className="flex flex-wrap gap-3">
              {profiles.length === 0 && <span className="text-[11px] text-gray-400">没有远程 Profile</span>}
              {profiles.map(p => (
                <label key={p.id} className="flex items-center gap-1.5 text-xs">
                  <input
                    type="checkbox"
                    checked={cfg.profile_ids.includes(p.id)}
                    disabled={saving}
                    onChange={e => void save({
                      profile_ids: e.target.checked
                        ? [...cfg.profile_ids, p.id]
                        : cfg.profile_ids.filter(id => id !== p.id),
                    })}
                  />
                  <span className="font-mono">{p.name}</span>
                </label>
              ))}
            </div>
          </div>

          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            {NUM_FIELDS.map(f => (
              <label key={f.key} className="block">
                <span className="mono-label block mb-1">{f.label}</span>
                <input
                  type="number" className={inputCls + ' w-full'} defaultValue={cfg[f.key] as number} min={f.min} max={f.max}
                  key={`${f.key}-${cfg[f.key] as number}`}
                  onBlur={e => { const n = Number(e.target.value); if (n !== cfg[f.key]) void save({ [f.key]: n }) }}
                />
                <span className="text-[10px] text-gray-400">{f.hint}</span>
              </label>
            ))}
          </div>

          <div className="mt-4">
            <span className="mono-label block mb-1">只处理这些模型(空 = 全部)</span>
            <div className="flex flex-wrap gap-1.5 mb-2 min-h-[1.5rem]">
              {cfg.models.length === 0 && <span className="text-[11px] text-gray-400">(全部模型)</span>}
              {cfg.models.map(v => (
                <span key={v} className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-mono bg-brand-50 text-brand">
                  {v}
                  <button type="button" onClick={() => void save({ models: cfg.models.filter(x => x !== v) })} title="移除" className="opacity-60 hover:opacity-100">✕</button>
                </span>
              ))}
            </div>
            <div className="flex gap-1 max-w-md">
              <input
                value={modelInput} onChange={e => setModelInput(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); addModel() } }}
                placeholder="回车添加,例 gpt-6-astra"
                className={inputCls + ' flex-1 font-mono'}
              />
              <Button variant="outline" size="sm" onClick={addModel}>添加</Button>
            </div>
          </div>
        </Card>

        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="px-4 py-2 border-b border-gray-100 text-[11px] font-medium text-gray-700 flex items-center justify-between">
            <span>冷却中 ({active.length})</span>
            <button className="text-[11px] text-gray-400 hover:text-gray-700" onClick={() => void loadActive()}>刷新</button>
          </div>
          <div className="overflow-x-auto max-h-[40vh] overflow-y-auto">
            <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
              <thead>
                <tr>
                  {['Profile', '渠道', '模型', '限额', '429 次数', '第几次', '冷却', '剩余', '操作'].map(h => (
                    <th key={h} className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {active.length === 0 && (
                  <tr><td colSpan={9} className="px-3 py-6 text-center text-gray-400">没有冷却中的模型</td></tr>
                )}
                {active.map(row => (
                  <tr key={`${row.profile_id}-${row.channel_id}-${row.model}`} className="hover:bg-gray-50">
                    <td className="px-3 py-1.5 border-b border-gray-50">{row.profile_name || profileName(row.profile_id)}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums">
                      <span className="text-gray-400">{row.channel_id}</span> {row.channel_name}
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50 font-mono">
                      {row.model}
                      {row.dry_run && <span className="ml-1 text-[10px] text-amber-700">演练</span>}
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums text-gray-600">
                      {row.limit_value > 0 ? `${row.limit_unit} ${row.limit_value.toLocaleString()}` : '—'}
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums">{row.hits}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums">{row.trips}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums text-gray-600">{fmtDuration(row.cooldown_sec)}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums font-medium">{fmtDuration(row.restore_at - serverNow)}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50">
                      <button className="text-[11px] text-brand hover:underline" onClick={() => void restoreNow(row)}>立即恢复</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
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
                  {['时间', '动作', 'Profile', '渠道', '模型', '冷却', '详情'].map(h => (
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
                      <span className={ACTION_CLS[ev.action] ?? 'text-gray-500'}>{ACTION_LABEL[ev.action] ?? ev.action}</span>
                      {ev.dry_run && <span className="ml-1 text-[10px] text-amber-700">演练</span>}
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50">{profileName(ev.profile_id)}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums">
                      <span className="text-gray-400">{ev.channel_id}</span> {ev.channel_name}
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50 font-mono">{ev.model}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums text-gray-600">{ev.cooldown_sec > 0 ? fmtDuration(ev.cooldown_sec) : '—'}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 max-w-[420px] truncate" title={ev.detail}>{ev.detail}</td>
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
