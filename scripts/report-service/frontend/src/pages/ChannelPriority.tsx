import { useEffect, useRef, useState } from 'react'
import Layout from '../components/Layout'
import { Button, Card } from '../components/ui'
import { toast } from '../components/feedback'
import { api, type ChanPrioConfig, type ChanPrioDemoted, type ChanPrioEvent } from '../api'

// Channel Priority: auto-tune channel priority from 429 throttling. A channel
// throttled (all-429, no success) in the down-window is stepped down (out of
// new-api's top priority tier, so live traffic shifts to healthy peers); a
// channel with no 429 for the up-window is stepped back up toward the baseline
// it had when first demoted.

const inputCls = 'border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900'

function fmtTime(unix: number): string {
  if (!unix) return '—'
  const d = new Date(unix * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

const ACTION_LABEL: Record<string, string> = {
  demote: '下调',
  promote: '上调',
  restore: '恢复基线',
  error: '失败',
}

function normalize(c: ChanPrioConfig): ChanPrioConfig {
  return { ...c, groups: c.groups ?? [] }
}

function ChipList({ items, onRemove }: { items: string[]; onRemove: (v: string) => void }) {
  return (
    <div className="flex flex-wrap gap-1.5 mb-2 min-h-[1.5rem]">
      {items.length === 0 && <span className="text-[11px] text-gray-400">（全部分组）</span>}
      {items.map(v => (
        <span key={v} className="inline-flex items-center gap-1 rounded-full bg-gray-100 text-gray-700 px-2 py-0.5 text-[11px] font-mono">
          {v}
          <button type="button" onClick={() => onRemove(v)} title="移除" className="opacity-60 hover:opacity-100">✕</button>
        </span>
      ))}
    </div>
  )
}

function NumField({ label, value, min, max, hint, onSave }: {
  label: string; value: number; min: number; max: number; hint?: string; onSave: (n: number) => void
}) {
  return (
    <label className="block">
      <span className="mono-label block mb-1">{label}</span>
      <input
        type="number" className={inputCls + ' w-full'} defaultValue={value} min={min} max={max}
        onBlur={e => { const n = Number(e.target.value); if (n !== value) onSave(n) }}
      />
      {hint && <span className="text-[10px] text-gray-400">{hint}</span>}
    </label>
  )
}

export default function ChannelPriority() {
  const [cfg, setCfg] = useState<ChanPrioConfig | null>(null)
  const [saving, setSaving] = useState(false)
  const [running, setRunning] = useState(false)
  const [events, setEvents] = useState<ChanPrioEvent[]>([])
  const [demoted, setDemoted] = useState<ChanPrioDemoted[]>([])
  const [demotedCount, setDemotedCount] = useState(0)
  const [groupInput, setGroupInput] = useState('')
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const loadEvents = async () => {
    try { setEvents((await api.chanPrioEvents({ limit: 200 })).events) } catch { /* silent */ }
  }
  const loadStatus = async () => {
    try {
      const s = await api.chanPrioStatus()
      setDemoted(s.demoted ?? [])
      setDemotedCount(s.demoted_count ?? 0)
    } catch { /* silent */ }
  }
  const refresh = () => { void loadEvents(); void loadStatus() }

  useEffect(() => {
    void (async () => {
      try { setCfg(normalize(await api.chanPrioGetConfig())) }
      catch (e) { toast.error(e instanceof Error ? e.message : String(e)) }
    })()
    refresh()
    pollRef.current = setInterval(refresh, 10000)
    return () => { if (pollRef.current) clearInterval(pollRef.current) }
  }, [])

  const save = async (patch: Partial<ChanPrioConfig>) => {
    setSaving(true)
    try {
      const next = normalize(await api.chanPrioSetConfig(patch))
      setCfg(next)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
      try { setCfg(normalize(await api.chanPrioGetConfig())) } catch { /* ignore */ }
    } finally { setSaving(false) }
  }

  const runNow = async () => {
    setRunning(true)
    try {
      const res = await api.chanPrioRunNow()
      const s = res.summary
      toast.success(`${res.dry_run ? '演练' : '执行'}完成：下调 ${s.demoted}，上调 ${s.promoted}，恢复 ${s.restored}，失败 ${s.errors}`)
      refresh()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally { setRunning(false) }
  }

  const addGroup = () => {
    const v = groupInput.trim()
    if (v && cfg && !cfg.groups.includes(v)) void save({ groups: [...cfg.groups, v] })
    setGroupInput('')
  }

  if (!cfg) {
    return (
      <Layout title="渠道优先级自调" subtitle="429 限流自动降权 / 恢复自动升权">
        <div className="text-sm text-secondary">加载中…</div>
      </Layout>
    )
  }

  return (
    <Layout
      title="渠道优先级自调"
      subtitle="被 429 限流的渠道自动降低优先级(移出主轮换),恢复后自动升回基线"
      actions={<Button variant="outline" size="sm" onClick={runNow} disabled={running}>{running ? '执行中…' : '立即执行一次'}</Button>}
    >
      <div className="space-y-4">
        <Card className="p-5">
          <div className="flex items-center justify-between mb-3">
            <div className="text-sm font-semibold">调度</div>
            <div className="flex items-center gap-4">
              <label className="flex items-center gap-2 text-xs">
                <input type="checkbox" checked={cfg.dry_run} disabled={saving} onChange={e => void save({ dry_run: e.target.checked })} />
                <span className={cfg.dry_run ? 'text-amber-700 font-medium' : 'text-gray-500'}>演练</span>
              </label>
              <label className="flex items-center gap-2 text-xs">
                <input type="checkbox" checked={cfg.enabled} disabled={saving} onChange={e => void save({ enabled: e.target.checked })} />
                <span className={cfg.enabled ? 'text-green-700 font-medium' : 'text-gray-500'}>{cfg.enabled ? '已开启' : '已关闭'}</span>
              </label>
            </div>
          </div>

          <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-4">
            <NumField label="降权窗口 (秒)" value={cfg.down_window_sec} min={10} max={600}
              hint="该窗口内全 429、0 成功 → 降权" onSave={n => void save({ down_window_sec: n })} />
            <NumField label="升权窗口 (秒)" value={cfg.up_window_sec} min={10} max={3600}
              hint="该窗口内无 429 → 升权" onSave={n => void save({ up_window_sec: n })} />
            <NumField label="检查间隔 (秒)" value={cfg.tick_sec} min={5} max={300}
              onSave={n => void save({ tick_sec: n })} />
            <NumField label="每次步长" value={cfg.step} min={1} max={1000000}
              hint="每次升/降的优先级数值" onSave={n => void save({ step: n })} />
            <NumField label="最大降幅" value={cfg.max_drop} min={1} max={100000000}
              hint="相对基线最多降多少" onSave={n => void save({ max_drop: n })} />
            <NumField label="最小 429 数" value={cfg.min_throttle} min={1} max={10000}
              hint="窗口内至少这么多 429 才降权" onSave={n => void save({ min_throttle: n })} />
            <NumField label="每轮最多动作" value={cfg.max_actions} min={1} max={2000}
              onSave={n => void save({ max_actions: n })} />
            <label className="block">
              <span className="mono-label block mb-1">429 关键字</span>
              <input type="text" className={inputCls + ' w-full font-mono'} defaultValue={cfg.throttle_substr}
                onBlur={e => { const v = e.target.value.trim(); if (v && v !== cfg.throttle_substr) void save({ throttle_substr: v }) }} />
              <span className="text-[10px] text-gray-400">报错内容含此串算限流</span>
            </label>
          </div>

          <div className="mt-4">
            <span className="mono-label block mb-1">检查的分组</span>
            <ChipList items={cfg.groups} onRemove={v => void save({ groups: cfg.groups.filter(x => x !== v) })} />
            <div className="flex gap-1 max-w-md">
              <input value={groupInput} onChange={e => setGroupInput(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); addGroup() } }}
                placeholder="回车添加;留空=全部分组" className={inputCls + ' flex-1 font-mono'} />
              <Button variant="outline" size="sm" onClick={addGroup}>添加</Button>
            </div>
          </div>
        </Card>

        {/* Currently demoted channels */}
        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="px-4 py-2 border-b border-gray-100 text-[11px] font-medium text-gray-700 flex items-center justify-between">
            <span>当前被降权(低于基线)的渠道 · {demotedCount}</span>
            <button className="text-[11px] text-gray-400 hover:text-gray-700" onClick={() => void loadStatus()}>刷新</button>
          </div>
          <div className="overflow-x-auto max-h-[32vh] overflow-y-auto">
            <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
              <thead>
                <tr>
                  {['渠道', '分组', '当前优先级', '基线', '时间'].map(h => (
                    <th key={h} className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {demoted.length === 0 && <tr><td colSpan={5} className="px-3 py-5 text-center text-gray-400">无(全部在基线)</td></tr>}
                {demoted.map(d => (
                  <tr key={d.channel_id} className="hover:bg-gray-50">
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums"><span className="text-gray-400">{d.channel_id}</span> {d.channel_name}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{d.group || '—'}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums text-amber-700 font-semibold">{d.priority}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums text-gray-500">{d.base_priority}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 tabular-nums">{fmtTime(d.updated_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>

        {/* Recent actions */}
        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="px-4 py-2 border-b border-gray-100 text-[11px] font-medium text-gray-700 flex items-center justify-between">
            <span>最近动作</span>
            <button className="text-[11px] text-gray-400 hover:text-gray-700" onClick={() => void loadEvents()}>刷新</button>
          </div>
          <div className="overflow-x-auto max-h-[40vh] overflow-y-auto">
            <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
              <thead>
                <tr>
                  {['时间', '动作', '渠道', '分组', '优先级', '429', '详情'].map(h => (
                    <th key={h} className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {events.length === 0 && <tr><td colSpan={7} className="px-3 py-6 text-center text-gray-400">暂无记录</td></tr>}
                {events.map(ev => (
                  <tr key={ev.id} className="hover:bg-gray-50">
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 tabular-nums">{fmtTime(ev.created_at)}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50">
                      <span className={
                        ev.action === 'demote' ? 'text-amber-700'
                          : ev.action === 'restore' ? 'text-green-700'
                          : ev.action === 'promote' ? 'text-blue-600'
                          : ev.action === 'error' ? 'text-red-600'
                          : 'text-gray-500'
                      }>{ACTION_LABEL[ev.action] ?? ev.action}{ev.dry_run ? '(演练)' : ''}</span>
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums"><span className="text-gray-400">{ev.channel_id}</span> {ev.channel_name}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{ev.group || '—'}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums text-gray-600">{ev.from_priority} → {ev.to_priority}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 tabular-nums text-gray-600">{ev.throttle_count || '—'}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 max-w-[360px] truncate" title={ev.detail}>{ev.detail}</td>
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
