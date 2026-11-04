import { useCallback, useEffect, useState } from 'react'
import Layout from '../components/Layout'
import { Card } from '../components/ui'
import { toast, confirmDialog } from '../components/feedback'
import { api, type StreamGuardEvent, type StreamGuardRules } from '../api'
import { ModelCooldownPanel } from './RemoteModelCooldown'
import { ChannelPriorityPanel } from './ChannelPriority'

// 调度: the three automatic traffic schedulers stacked on one page — 429 model
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
  const [rules, setRules] = useState<StreamGuardRules | null>(null)
  const [busy, setBusy] = useState<number | null>(null)

  const load = useCallback(async () => {
    try {
      const res = await api.streamGuardEvents()
      setEvents(res.events)
      setRules(res.rules)
    } catch (e) {
      toast.error(e)
    }
  }, [])

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
          <div className="text-sm font-semibold mb-3">规则</div>
          {rules ? (
            <ul className="text-xs text-gray-600 space-y-1.5 list-disc pl-4">
              <li>每 <b>{rules.tick_sec / 60}</b> 分钟检查一次所有启用渠道最近 <b>{rules.window_sec / 60}</b> 分钟的流式请求（只在 leader 节点执行）。</li>
              <li>同一渠道同一模型：流 ≥ <b>{rules.min_streams}</b> 条、失败 ≥ <b>{rules.min_failed}</b> 条、失败率 ≥ <b>{Math.round(rules.min_fail_rate * 100)}%</b> 即命中。</li>
              <li>命中后从该渠道下掉这个模型（渠道只剩这一个模型时不下，只降优先级），渠道优先级降到 <b>{rules.demote_priority}</b>。newapi 约 60 秒内生效。</li>
              <li>同一模型在 ≥ <b>{Math.round(rules.wide_share * 100)}%</b> 的渠道（且 ≥ {rules.wide_min_chans} 个）同时失败，视为上游整体故障，不处理。</li>
              <li>不会自动恢复；误判时在下方点「恢复」，模型加回、优先级回到处理前的值。</li>
            </ul>
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

const SECTIONS = [
  { id: 'cooldown', title: '模型限流冷却', body: <ModelCooldownPanel /> },
  { id: 'priority', title: '渠道优先级自调', body: <ChannelPriorityPanel /> },
  { id: 'stream', title: '断流处理', body: <StreamGuardPanel /> },
]

export default function Scheduling() {
  // Old /channel-priority and /remote-model-cooldown routes redirect here
  // with a #section; the SPA render does not scroll to it on its own.
  useEffect(() => {
    const id = window.location.hash.slice(1)
    if (id) document.getElementById(id)?.scrollIntoView()
  }, [])

  return (
    <Layout title="调度" subtitle="限流冷却、优先级自调、断流处理 —— 自动把流量从有问题的 Key 上挪走">
      <nav className="flex items-center gap-2 mb-6 text-xs">
        {SECTIONS.map(s => (
          <a key={s.id} href={`#${s.id}`} className="rounded-md border border-gray-200 bg-white px-2.5 py-1 text-gray-600 hover:text-gray-900 hover:border-gray-400">
            {s.title}
          </a>
        ))}
      </nav>
      <div className="space-y-10">
        {SECTIONS.map(s => (
          <section key={s.id} id={s.id} className="scroll-mt-4">
            <h2 className="text-base font-semibold text-gray-900 mb-2">{s.title}</h2>
            {s.body}
          </section>
        ))}
      </div>
    </Layout>
  )
}
