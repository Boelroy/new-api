import { useCallback, useEffect, useState } from 'react'
import Layout from '../components/Layout'
import SummaryCards from '../components/SummaryCards'
import { LivePollChip } from '../components/ui'
import {
  api,
  LocalHealthConfig,
  LocalHealthEvent,
  LocalHealthItem,
  LocalHealthPreview,
  LocalHealthRule,
  LocalHealthStatus,
} from '../api'

const REFRESH_MS = 30_000

const STATE_STYLE: Record<string, { label: string; cls: string }> = {
  up: { label: '可用', cls: 'bg-[#E6F4EE] text-success' },
  down: { label: '不可用', cls: 'bg-rose-100 text-rose-700' },
  unknown: { label: '未知', cls: 'bg-gray-100 text-gray-500' },
  unsupported: { label: '不可测', cls: 'bg-[#FBF0DC] text-warning' },
}

const CHANNEL_STATUS: Record<number, string> = {
  1: '启用',
  2: '手动禁用',
  3: '自动禁用',
}

const EVENT_LABEL: Record<string, string> = {
  model_down: '模型转不可用',
  model_up: '模型恢复',
  model_removed: '摘除/写回模型',
  model_added: '追加模型',
  channel_disabled: '渠道关闭',
  channel_enabled: '渠道开启',
  floor_blocked: '保底拦截',
  breaker_tripped: '熔断',
  action_failed: '操作失败',
  unsupported: '渠道不可测',
}

function fmtTime(ts: number) {
  if (!ts) return '—'
  const d = new Date(ts * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getMonth() + 1}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function fmtAgo(ts: number) {
  if (!ts) return '—'
  const sec = Math.max(0, Math.floor(Date.now() / 1000) - ts)
  if (sec < 60) return `${sec}秒前`
  if (sec < 3600) return `${Math.floor(sec / 60)}分钟前`
  if (sec < 86400) return `${Math.floor(sec / 3600)}小时前`
  return `${Math.floor(sec / 86400)}天前`
}

const inputCls =
  'border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900'
const btnCls = 'border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50 disabled:opacity-40'

const BLANK_RULE: Partial<LocalHealthRule> = {
  name: '',
  match_tag: '',
  match_type: -1,
  match_group: '',
  match_channel_ids: '',
  candidate_models: '',
  enabled: false,
  enforce: false,
  probe_interval_sec: 3600,
  down_window_sec: 1800,
  down_fail_min: 3,
  recover_ok_min: 2,
}

export default function ModelHealth() {
  const [status, setStatus] = useState<LocalHealthStatus | null>(null)
  const [rules, setRules] = useState<LocalHealthRule[]>([])
  const [events, setEvents] = useState<LocalHealthEvent[]>([])
  const [error, setError] = useState<string | null>(null)
  const [stateFilter, setStateFilter] = useState('')
  const [tagFilter, setTagFilter] = useState('')
  const [editing, setEditing] = useState<Partial<LocalHealthRule> | null>(null)
  const [refreshedAt, setRefreshedAt] = useState<number | null>(null)

  const reload = useCallback(async () => {
    try {
      const [s, r, e] = await Promise.all([
        api.localHealthStatus({ state: stateFilter || undefined, tag: tagFilter || undefined, limit: 1000 }),
        api.localHealthListRules(),
        api.localHealthEvents({ limit: 100 }),
      ])
      setStatus(s)
      setRules(r.rules)
      setEvents(e.events)
      setError(null)
      setRefreshedAt(Date.now())
    } catch (err) {
      setError((err as Error).message || String(err))
    }
  }, [stateFilter, tagFilter])

  useEffect(() => {
    void reload()
    const t = setInterval(() => void reload(), REFRESH_MS)
    return () => clearInterval(t)
  }, [reload])

  const cfg = status?.config
  const counts = status?.state_counts ?? {}
  const tick = status?.last_tick

  const saveConfig = async (patch: Partial<LocalHealthConfig>) => {
    try {
      const next = await api.localHealthSetConfig(patch)
      setStatus(s => (s ? { ...s, config: next } : s))
      setError(null)
    } catch (err) {
      setError((err as Error).message || String(err))
    }
  }

  return (
    <Layout
      title="Model Health"
      subtitle="按规则探测本地渠道的模型可用性，自动收敛 models 并开关渠道"
      actions={
        <>
          <LivePollChip at={refreshedAt} className="mr-1" />
          <button className={btnCls} onClick={() => void reload()}>
            刷新
          </button>
          <button className={btnCls} onClick={() => setEditing({ ...BLANK_RULE })}>
            + 新建规则
          </button>
        </>
      }
    >
      {cfg && !cfg.token_configured && (
        <div className="bg-amber-50 border border-amber-200 text-amber-800 text-xs rounded-md px-3 py-2 mb-4">
          未配置 <code className="font-mono">MAIN_SERVICE_TOKEN</code>（需要本地 new-api 上具备 ChannelRead / ChannelWrite /
          ChannelOperate 权限的管理员 token），调度器无法探测或修改渠道，将保持关闭。
        </div>
      )}

      {error && (
        <div className="bg-rose-50 border border-rose-100 text-rose-700 text-xs rounded-md px-3 py-2 mb-4">{error}</div>
      )}

      <SummaryCards
        cards={[
          { label: '可用', value: String(counts.up ?? 0), color: 'text-emerald-600' },
          { label: '不可用', value: String(counts.down ?? 0), color: 'text-rose-600' },
          { label: '未知', value: String(counts.unknown ?? 0) },
          { label: '不可测', value: String(counts.unsupported ?? 0), color: 'text-amber-600' },
          {
            label: '队列积压',
            value: status ? `${status.queue_lag_sec}s` : '—',
            color: (status?.queue_lag_sec ?? 0) > 600 ? 'text-rose-600' : 'text-gray-900',
          },
          { label: '被本功能关闭的渠道', value: String(status?.channels_disabled ?? 0), color: 'text-blue-600' },
        ]}
      />

      {/* Global switches. Probes are real billed calls, so the batch/interval
          knobs live next to the master switch rather than buried in a rule. */}
      {cfg && (
        <div className="bg-white border border-gray-200 rounded-xl p-3 mb-4">
          <div className="flex flex-wrap items-center gap-3 text-xs">
            <label className="flex items-center gap-1.5">
              <input
                type="checkbox"
                checked={cfg.enabled}
                disabled={!cfg.token_configured}
                onChange={e => void saveConfig({ enabled: e.target.checked })}
              />
              <span className="font-medium">启用调度器</span>
            </label>
            <NumberField
              label="调度间隔(s)"
              value={cfg.tick_sec}
              onCommit={v => void saveConfig({ tick_sec: v })}
            />
            <NumberField
              label="每轮探测数"
              value={cfg.probe_batch}
              onCommit={v => void saveConfig({ probe_batch: v })}
            />
            <NumberField
              label="首轮探测数"
              value={cfg.bootstrap_batch}
              onCommit={v => void saveConfig({ bootstrap_batch: v })}
            />
            <NumberField
              label="并发"
              value={cfg.concurrency}
              onCommit={v => void saveConfig({ concurrency: v })}
            />
            <NumberField
              label="探测超时(s)"
              value={cfg.probe_timeout_sec}
              onCommit={v => void saveConfig({ probe_timeout_sec: v })}
            />
            <NumberField
              label="每轮改动上限"
              value={cfg.max_actions_per_tick}
              onCommit={v => void saveConfig({ max_actions_per_tick: v })}
            />
          </div>
          {tick && tick.at > 0 && (
            <div className="text-[11px] text-gray-400 mt-2">
              上轮 {fmtAgo(tick.at)}：探测 {tick.probed}（成功 {tick.ok} / 模型故障 {tick.model_down} / 渠道故障{' '}
              {tick.channel_down} / 限流 {tick.throttled} / 未达 {tick.neutral} / 不可测 {tick.unsupported}），
              改动 {tick.actions} 次，耗时 {tick.duration_ms}ms
              {tick.breaker_open && <span className="text-rose-600 font-medium"> · 熔断（跳过了改动）</span>}
            </div>
          )}
        </div>
      )}

      <RulesTable
        rules={rules}
        onEdit={setEditing}
        onChanged={() => void reload()}
        onError={setError}
      />

      <div className="bg-white border border-gray-200 rounded-xl overflow-hidden mb-4">
        <div className="px-4 py-2 border-b border-gray-100 flex items-center gap-2 text-[11px] text-gray-500">
          <span className="font-medium text-gray-700">健康矩阵</span>
          <select value={stateFilter} onChange={e => setStateFilter(e.target.value)} className={inputCls}>
            <option value="">全部状态</option>
            <option value="up">可用</option>
            <option value="down">不可用</option>
            <option value="unknown">未知</option>
            <option value="unsupported">不可测</option>
          </select>
          <input
            value={tagFilter}
            onChange={e => setTagFilter(e.target.value)}
            placeholder="studio (tag)"
            className={inputCls}
          />
          <span className="ml-auto">共 {status?.items.length ?? 0} 行</span>
        </div>
        <HealthTable items={status?.items ?? []} onProbed={() => void reload()} onError={setError} />
      </div>

      <EventsTable events={events} />

      {editing && (
        <RuleEditor
          rule={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void reload()
          }}
        />
      )}
    </Layout>
  )
}

// NumberField commits on blur / Enter rather than per keystroke — each commit
// is a config write that the scheduler picks up on its next tick.
function NumberField({ label, value, onCommit }: { label: string; value: number; onCommit: (v: number) => void }) {
  const [draft, setDraft] = useState(String(value))
  useEffect(() => setDraft(String(value)), [value])
  const commit = () => {
    const n = parseInt(draft.trim(), 10)
    if (Number.isFinite(n) && n !== value) onCommit(n)
    else setDraft(String(value))
  }
  return (
    <label className="flex items-center gap-1.5 text-gray-500">
      {label}
      <input
        value={draft}
        onChange={e => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={e => e.key === 'Enter' && commit()}
        className={inputCls + ' w-20 tabular-nums'}
      />
    </label>
  )
}

function RulesTable({
  rules,
  onEdit,
  onChanged,
  onError,
}: {
  rules: LocalHealthRule[]
  onEdit: (r: LocalHealthRule) => void
  onChanged: () => void
  onError: (msg: string) => void
}) {
  const [preview, setPreview] = useState<LocalHealthPreview | null>(null)

  const toggle = async (r: LocalHealthRule, patch: Partial<LocalHealthRule>) => {
    try {
      await api.localHealthUpdateRule(r.id, patch)
      onChanged()
    } catch (err) {
      onError((err as Error).message || String(err))
    }
  }

  return (
    <div className="bg-white border border-gray-200 rounded-xl overflow-hidden mb-4">
      <div className="px-4 py-2 border-b border-gray-100 text-[11px] font-medium text-gray-700">规则</div>
      {rules.length === 0 ? (
        <div className="text-center text-gray-400 text-xs py-10">还没有规则。新建一条并保持“观察模式”先看效果。</div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
            <thead>
              <tr>
                {['名称', 'studio', '类型', '分组', '指定渠道', '候选模型', '探测间隔', '下线窗口', '连败/连胜', '启用', '执行', ''].map(h => (
                  <th
                    key={h}
                    className="bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200"
                  >
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rules.map(r => (
                <tr key={r.id} className="hover:bg-gray-50">
                  <td className="px-3 py-1.5 border-b border-gray-50 text-gray-900">{r.name || `#${r.id}`}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{r.match_tag || '全部'}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">
                    {r.match_type < 0 ? '全部' : r.match_type}
                  </td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{r.match_group || '全部'}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 max-w-[160px] truncate" title={r.match_channel_ids}>
                    {r.match_channel_ids || '—'}
                  </td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 max-w-[240px] truncate" title={r.candidate_models}>
                    {r.candidate_models || '默认列表'}
                  </td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 tabular-nums">{r.probe_interval_sec}s</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 tabular-nums">{r.down_window_sec}s</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 tabular-nums">
                    {r.down_fail_min} / {r.recover_ok_min}
                  </td>
                  <td className="px-3 py-1.5 border-b border-gray-50">
                    <input type="checkbox" checked={r.enabled} onChange={e => void toggle(r, { enabled: e.target.checked })} />
                  </td>
                  <td className="px-3 py-1.5 border-b border-gray-50">
                    <label className="flex items-center gap-1" title="关闭时只记录事件，不修改 new-api">
                      <input type="checkbox" checked={r.enforce} onChange={e => void toggle(r, { enforce: e.target.checked })} />
                      {!r.enforce && <span className="text-[10px] text-amber-600">观察</span>}
                    </label>
                  </td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-right space-x-2">
                    <button
                      className="text-blue-600 hover:underline"
                      onClick={async () => {
                        try {
                          setPreview(await api.localHealthPreviewRule(r.id))
                        } catch (err) {
                          onError((err as Error).message || String(err))
                        }
                      }}
                    >
                      预览
                    </button>
                    <button className="text-gray-600 hover:underline" onClick={() => onEdit(r)}>
                      编辑
                    </button>
                    <button
                      className="text-rose-600 hover:underline"
                      onClick={async () => {
                        if (!confirm(`删除规则「${r.name || r.id}」？`)) return
                        try {
                          await api.localHealthDeleteRule(r.id)
                          onChanged()
                        } catch (err) {
                          onError((err as Error).message || String(err))
                        }
                      }}
                    >
                      删除
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {preview && <PreviewPanel preview={preview} onClose={() => setPreview(null)} />}
    </div>
  )
}

// PreviewPanel answers the question that must be asked before a rule is
// enabled: how many channels does it touch and how many billed probes per day
// does that cost.
function PreviewPanel({ preview, onClose }: { preview: LocalHealthPreview; onClose: () => void }) {
  return (
    <div className="fixed inset-0 bg-gray-900/30 flex justify-end z-40" onClick={onClose}>
      <div
        className="w-[640px] max-w-full bg-white border-l border-gray-200 h-full overflow-y-auto p-5"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-base font-semibold">规则预览 · {preview.rule.name || `#${preview.rule.id}`}</h2>
          <button className={btnCls} onClick={onClose}>
            关闭
          </button>
        </div>
        <div className="grid grid-cols-3 gap-3 mb-4">
          {[
            { label: '命中渠道', value: String(preview.channel_count) },
            { label: '渠道×模型', value: String(preview.pair_count) },
            { label: '预计探测/天', value: String(preview.probes_per_day) },
          ].map(c => (
            <div key={c.label} className="border border-gray-200 rounded-lg px-3 py-2">
              <div className="mono-label">{c.label}</div>
              <div className="text-lg font-semibold tabular-nums mt-0.5">{c.value}</div>
            </div>
          ))}
        </div>
        <p className="text-[11px] text-gray-400 mb-3">
          每次探测都是一次真实的上游计费请求。降低探测频率后，真实流量的错误日志会自动把出问题的渠道×模型提前拉回队列。
        </p>
        <table className="w-full text-xs border-separate border-spacing-0">
          <thead>
            <tr>
              {['渠道', '状态', '分组', '当前 models', '候选模型'].map(h => (
                <th key={h} className="bg-gray-50 px-2 py-1.5 text-left mono-label border-b border-gray-200">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {preview.sample_channels.map(ch => (
              <tr key={ch.channel_id}>
                <td className="px-2 py-1 border-b border-gray-50" title={ch.name}>
                  #{ch.channel_id}
                </td>
                <td className="px-2 py-1 border-b border-gray-50 text-gray-500">
                  {CHANNEL_STATUS[ch.status] ?? ch.status}
                </td>
                <td className="px-2 py-1 border-b border-gray-50 text-gray-500">{ch.group}</td>
                <td className="px-2 py-1 border-b border-gray-50 text-gray-500 max-w-[180px] truncate" title={ch.current_models}>
                  {ch.current_models}
                </td>
                <td className="px-2 py-1 border-b border-gray-50 text-gray-500 max-w-[180px] truncate" title={ch.candidate_models}>
                  {ch.candidate_models}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {preview.channel_count > preview.sample_channels.length && (
          <p className="text-[11px] text-gray-400 mt-2">仅显示前 {preview.sample_channels.length} 个渠道。</p>
        )}
      </div>
    </div>
  )
}

function HealthTable({
  items,
  onProbed,
  onError,
}: {
  items: LocalHealthItem[]
  onProbed: () => void
  onError: (msg: string) => void
}) {
  const [probing, setProbing] = useState('')

  if (items.length === 0) {
    return <div className="text-center text-gray-400 text-xs py-12">还没有覆盖任何渠道×模型。启用一条规则后会自动填充。</div>
  }
  return (
    <div className="overflow-x-auto max-h-[60vh] overflow-y-auto">
      <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
        <thead>
          <tr>
            {['渠道', 'studio', '模型', '状态', '在 models 里', '渠道状态', '连胜/连败', '最近成功', '最近探测', '下次探测', '最近错误', ''].map(h => (
              <th
                key={h}
                className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200"
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {items.map(it => {
            const style = STATE_STYLE[it.state] ?? STATE_STYLE.unknown
            const key = `${it.channel_id}/${it.model}`
            return (
              <tr key={key} className="hover:bg-gray-50">
                <td className="px-3 py-1.5 border-b border-gray-50 text-gray-700" title={it.channel_name}>
                  #{it.channel_id}
                </td>
                <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{it.tag || '—'}</td>
                <td className="px-3 py-1.5 border-b border-gray-50 font-mono text-gray-600">{it.model}</td>
                <td className="px-3 py-1.5 border-b border-gray-50">
                  <span className={`px-2 py-0.5 rounded-full text-[10px] font-semibold ${style.cls}`}>{style.label}</span>
                </td>
                <td className="px-3 py-1.5 border-b border-gray-50">
                  {it.in_models ? <span className="text-emerald-600">是</span> : <span className="text-gray-400">否</span>}
                </td>
                <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">
                  {CHANNEL_STATUS[it.channel_status] ?? it.channel_status}
                  {it.disabled_by_us && <span className="text-[10px] text-blue-600 ml-1">(本功能)</span>}
                </td>
                <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 tabular-nums">
                  {it.consecutive_ok} / {it.consecutive_fail}
                </td>
                <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{fmtAgo(it.last_ok_at)}</td>
                <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{fmtAgo(it.last_checked_at)}</td>
                <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{fmtTime(it.next_check_at)}</td>
                <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 max-w-[320px] truncate" title={it.last_error}>
                  {it.last_class && <span className="font-mono text-[10px] text-gray-400 mr-1">{it.last_class}</span>}
                  {it.last_error || '—'}
                </td>
                <td className="px-3 py-1.5 border-b border-gray-50 text-right">
                  <button
                    className="text-blue-600 hover:underline disabled:opacity-40"
                    disabled={probing === key}
                    onClick={async () => {
                      setProbing(key)
                      try {
                        const res = await api.localHealthProbe({ channel_id: it.channel_id, model: it.model })
                        alert(`${res.class}\n${res.message || '(无错误信息)'}\n${res.seconds.toFixed(2)}s`)
                        onProbed()
                      } catch (err) {
                        onError((err as Error).message || String(err))
                      } finally {
                        setProbing('')
                      }
                    }}
                  >
                    {probing === key ? '探测中…' : '立即探测'}
                  </button>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function EventsTable({ events }: { events: LocalHealthEvent[] }) {
  return (
    <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
      <div className="px-4 py-2 border-b border-gray-100 text-[11px] font-medium text-gray-700">事件</div>
      {events.length === 0 ? (
        <div className="text-center text-gray-400 text-xs py-10">还没有事件</div>
      ) : (
        <div className="overflow-x-auto max-h-[40vh] overflow-y-auto">
          <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
            <thead>
              <tr>
                {['时间', '类型', '渠道', '模型', '详情'].map(h => (
                  <th
                    key={h}
                    className="sticky top-0 bg-gray-50 px-3 py-2 text-left mono-label border-b border-gray-200"
                  >
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {events.map(ev => (
                <tr key={ev.id} className="hover:bg-gray-50">
                  <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 tabular-nums">{fmtTime(ev.created_at)}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50">
                    {EVENT_LABEL[ev.kind] ?? ev.kind}
                    {ev.dry_run && <span className="text-[10px] text-amber-600 ml-1">观察</span>}
                  </td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">
                    {ev.channel_id ? `#${ev.channel_id}` : '—'}
                  </td>
                  <td className="px-3 py-1.5 border-b border-gray-50 font-mono text-gray-600">{ev.model || '—'}</td>
                  <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 max-w-[560px] truncate" title={ev.detail}>
                    {ev.detail}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function RuleEditor({
  rule,
  onClose,
  onSaved,
}: {
  rule: Partial<LocalHealthRule>
  onClose: () => void
  onSaved: () => void
}) {
  const [draft, setDraft] = useState<Partial<LocalHealthRule>>(rule)
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState('')

  const set = (patch: Partial<LocalHealthRule>) => setDraft(d => ({ ...d, ...patch }))

  const submit = async () => {
    setSaving(true)
    setErr('')
    try {
      if (draft.id) await api.localHealthUpdateRule(draft.id, draft)
      else await api.localHealthCreateRule(draft)
      onSaved()
    } catch (e) {
      setErr((e as Error).message || String(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 bg-gray-900/30 flex justify-end z-40" onClick={onClose}>
      <div
        className="w-[560px] max-w-full bg-white border-l border-gray-200 h-full overflow-y-auto p-5"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-base font-semibold">{draft.id ? `编辑规则 #${draft.id}` : '新建规则'}</h2>
          <button className={btnCls} onClick={onClose}>
            ×
          </button>
        </div>

        <div className="space-y-3 text-xs">
          <Field label="名称">
            <input value={draft.name ?? ''} onChange={e => set({ name: e.target.value })} className={inputCls + ' w-full'} />
          </Field>

          <div className="grid grid-cols-2 gap-3">
            <Field label="studio (tag)，留空 = 全部">
              <input value={draft.match_tag ?? ''} onChange={e => set({ match_tag: e.target.value })} className={inputCls + ' w-full'} />
            </Field>
            <Field label="渠道类型，-1 = 全部">
              <input
                value={String(draft.match_type ?? -1)}
                onChange={e => set({ match_type: parseInt(e.target.value, 10) || -1 })}
                className={inputCls + ' w-full tabular-nums'}
              />
            </Field>
            <Field label="分组，留空 = 全部">
              <input value={draft.match_group ?? ''} onChange={e => set({ match_group: e.target.value })} className={inputCls + ' w-full'} />
            </Field>
            <Field label="指定渠道 ID（逗号分隔）">
              <input
                value={draft.match_channel_ids ?? ''}
                onChange={e => set({ match_channel_ids: e.target.value })}
                className={inputCls + ' w-full'}
              />
            </Field>
          </div>

          <Field label="候选模型（逗号分隔）。留空则用该渠道类型的默认建渠道模型列表">
            <textarea
              value={draft.candidate_models ?? ''}
              onChange={e => set({ candidate_models: e.target.value })}
              rows={3}
              className={inputCls + ' w-full font-mono'}
            />
          </Field>

          <div className="grid grid-cols-2 gap-3">
            <Field label="探测间隔(s)">
              <input
                value={String(draft.probe_interval_sec ?? 3600)}
                onChange={e => set({ probe_interval_sec: parseInt(e.target.value, 10) || 3600 })}
                className={inputCls + ' w-full tabular-nums'}
              />
            </Field>
            <Field label="持续不可用窗口(s)">
              <input
                value={String(draft.down_window_sec ?? 1800)}
                onChange={e => set({ down_window_sec: parseInt(e.target.value, 10) || 1800 })}
                className={inputCls + ' w-full tabular-nums'}
              />
            </Field>
            <Field label="判定不可用所需连败次数">
              <input
                value={String(draft.down_fail_min ?? 3)}
                onChange={e => set({ down_fail_min: parseInt(e.target.value, 10) || 3 })}
                className={inputCls + ' w-full tabular-nums'}
              />
            </Field>
            <Field label="判定恢复所需连胜次数">
              <input
                value={String(draft.recover_ok_min ?? 2)}
                onChange={e => set({ recover_ok_min: parseInt(e.target.value, 10) || 2 })}
                className={inputCls + ' w-full tabular-nums'}
              />
            </Field>
          </div>

          <label className="flex items-center gap-1.5">
            <input type="checkbox" checked={!!draft.enabled} onChange={e => set({ enabled: e.target.checked })} />
            启用（开始探测）
          </label>
          <label className="flex items-center gap-1.5">
            <input type="checkbox" checked={!!draft.enforce} onChange={e => set({ enforce: e.target.checked })} />
            执行改动（关闭时只记录事件，不修改 new-api）
          </label>
          <p className="text-[11px] text-gray-400">
            建议先只勾“启用”，观察事件流确认判定符合预期后再勾“执行改动”。
          </p>

          {err && <div className="bg-rose-50 border border-rose-100 text-rose-700 rounded-md px-3 py-2">{err}</div>}

          <div className="flex justify-end gap-2 pt-1">
            <button className={btnCls} onClick={onClose}>
              取消
            </button>
            <button
              className="border border-brand bg-brand text-white rounded-md px-3 py-1.5 text-xs hover:bg-brand-700 disabled:opacity-40"
              onClick={() => void submit()}
              disabled={saving}
            >
              {saving ? '保存中…' : '保存'}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="text-[11px] text-gray-400 block mb-1">{label}</label>
      {children}
    </div>
  )
}
