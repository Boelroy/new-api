import { useCallback, useEffect, useState } from 'react'
import Layout from '../components/Layout'
import { Button, Card } from '../components/ui'
import { toast } from '../components/feedback'
import { api, type OpsNotifyConfig, type OpsNotifyMessage } from '../api'
import { NumField } from './ChannelPriority'

// 运维通知: every window the server compresses who uploaded keys (with OpenAI
// tier) and who changed priorities into one digest, and alerts when a model's
// stream-failure or 429 rate rises against its baseline. Messages are listed
// here and pushed to the Lark webhook when one is set.

const CATEGORIES: { value: string; label: string; cls: string }[] = [
  { value: '', label: '全部', cls: '' },
  { value: 'digest', label: '上号 / 优先级', cls: 'bg-blue-50 text-blue-700' },
  { value: 'stream_rise', label: '断流上升', cls: 'bg-rose-50 text-rose-700' },
  { value: 'rate_limit_rise', label: '429 上升', cls: 'bg-amber-50 text-amber-700' },
]

function fmtTime(unix: number): string {
  const d = new Date(unix * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function ConfigCard() {
  const [cfg, setCfg] = useState<OpsNotifyConfig | null>(null)
  const [webhook, setWebhook] = useState('')
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  // Bumped on every save so the inputs remount with the stored (clamped) value.
  const [rev, setRev] = useState(0)

  const apply = (next: OpsNotifyConfig) => {
    setCfg(next)
    setWebhook(next.webhook)
    setRev(r => r + 1)
  }

  useEffect(() => {
    api.opsNotifyConfig().then(apply).catch(e => toast.error(e))
  }, [])

  const save = async (patch: Partial<OpsNotifyConfig>) => {
    setSaving(true)
    try {
      apply(await api.saveOpsNotifyConfig(patch))
      toast.success('已保存')
    } catch (e) {
      toast.error(e)
    } finally {
      setSaving(false)
    }
  }

  const test = async () => {
    setTesting(true)
    try {
      await api.testOpsNotify()
      toast.success('测试消息已发送')
    } catch (e) {
      toast.error(e)
    } finally {
      setTesting(false)
    }
  }

  if (!cfg) return <Card className="p-5"><div className="text-xs text-gray-400">加载中…</div></Card>

  const toggles: { key: 'uploads' | 'priority' | 'stream_rise' | 'rate_limit_rise'; label: string }[] = [
    { key: 'uploads', label: '上号（谁上了几个号、OpenAI Tier）' },
    { key: 'priority', label: '优先级调整（手动 + 429 调优 + 断流守卫）' },
    { key: 'stream_rise', label: '断流上升' },
    { key: 'rate_limit_rise', label: '429 上升' },
  ]
  const webhookDirty = webhook.trim() !== cfg.webhook
  return (
    <Card className="p-5">
      <div className="flex items-center justify-between mb-3">
        <div className="text-sm font-semibold">设置</div>
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={cfg.enabled} disabled={saving} onChange={e => void save({ enabled: e.target.checked })} />
          <span className={cfg.enabled ? 'text-green-700 font-medium' : 'text-gray-500'}>{cfg.enabled ? '已开启' : '已关闭'}</span>
        </label>
      </div>
      <label className="block mb-4">
        <span className="mono-label block mb-1">Lark Webhook（留空 = 只在本页显示）</span>
        <div className="flex gap-2">
          <input
            value={webhook} onChange={e => setWebhook(e.target.value)} autoComplete="off"
            placeholder="https://open.larksuite.com/open-apis/bot/v2/hook/..."
            className="flex-1 border border-border rounded-lg px-3 py-2 text-sm bg-canvas focus:outline-none focus:border-brand font-mono"
          />
          <Button size="sm" onClick={() => void save({ webhook: webhook.trim() })} disabled={saving || !webhookDirty}>保存</Button>
          <Button variant="outline" size="sm" onClick={() => void test()} disabled={testing || webhookDirty || !cfg.webhook}>
            {testing ? '发送中…' : '测试'}
          </Button>
        </div>
      </label>
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2 mb-4 text-xs">
        {toggles.map(t => (
          <label key={t.key} className="flex items-center gap-2">
            <input type="checkbox" checked={cfg[t.key]} disabled={saving} onChange={e => void save({ [t.key]: e.target.checked })} />
            <span>{t.label}</span>
          </label>
        ))}
      </div>
      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-4">
        <NumField key={`w${rev}`} label="汇总窗口 (分钟)" value={cfg.window_min} min={1} max={60}
          hint="每隔这么久合并推送一次" onSave={n => void save({ window_min: n })} />
        <NumField key={`b${rev}`} label="基线 (分钟)" value={cfg.baseline_min} min={1} max={60}
          hint="和窗口之前这么久比较；窗口 + 基线 = 回看总时长" onSave={n => void save({ baseline_min: n })} />
        <NumField key={`r${rev}`} label="上升倍数 (%)" value={cfg.rise_pct} min={101} max={10000}
          hint="200 = 比率达到基线 2 倍" onSave={n => void save({ rise_pct: n })} />
        <NumField key={`d${rev}`} label="同模型去重 (分钟)" value={cfg.dedupe_min} min={1} max={10080}
          hint="报过后这么久内不再报" onSave={n => void save({ dedupe_min: n })} />
        <div />
        <NumField key={`ss${rev}`} label="断流：最少流数" value={cfg.stream_min_streams} min={1} max={1000000}
          onSave={n => void save({ stream_min_streams: n })} />
        <NumField key={`sf${rev}`} label="断流：最少失败数" value={cfg.stream_min_failed} min={1} max={1000000}
          onSave={n => void save({ stream_min_failed: n })} />
        <NumField key={`sr${rev}`} label="断流：最低失败率 (%)" value={cfg.stream_min_rate_pct} min={0} max={100}
          onSave={n => void save({ stream_min_rate_pct: n })} />
        <NumField key={`rc${rev}`} label="429：最少次数" value={cfg.rl_min_count} min={1} max={1000000}
          onSave={n => void save({ rl_min_count: n })} />
        <NumField key={`rr${rev}`} label="429：最低占比 (%)" value={cfg.rl_min_rate_pct} min={0} max={100}
          hint="429 / (成功 + 429)" onSave={n => void save({ rl_min_rate_pct: n })} />
      </div>
      <p className="text-[11px] text-gray-400 mt-3">只在 leader 节点执行。没有新动态、没有上升时不发消息。</p>
    </Card>
  )
}

export default function Notifications() {
  const [category, setCategory] = useState('')
  const [items, setItems] = useState<OpsNotifyMessage[]>([])
  const [running, setRunning] = useState(false)

  const load = useCallback(async () => {
    try {
      setItems((await api.opsNotifyMessages(category)).items)
    } catch (e) {
      toast.error(e)
    }
  }, [category])

  useEffect(() => {
    void load()
    const t = setInterval(load, 60000)
    return () => clearInterval(t)
  }, [load])

  const runNow = async () => {
    setRunning(true)
    try {
      const { sent } = await api.runOpsNotify()
      toast.success(sent > 0 ? `生成 ${sent} 条通知` : '没有新动态')
      await load()
    } catch (e) {
      toast.error(e)
    } finally {
      setRunning(false)
    }
  }

  return (
    <Layout
      title="Notifications"
      subtitle="上号、优先级调整按窗口汇总；断流率 / 429 占比相比基线上升时告警"
      actions={<Button size="sm" variant="outline" onClick={() => void runNow()} disabled={running}>{running ? '检查中…' : '立即汇总'}</Button>}
    >
      <div className="space-y-4">
        <ConfigCard />
        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="px-4 py-2 border-b border-gray-100 flex items-center gap-2">
            {CATEGORIES.map(c => (
              <button
                key={c.value}
                onClick={() => setCategory(c.value)}
                className={`rounded-md px-2 py-0.5 text-[11px] ${category === c.value ? 'bg-gray-900 text-white' : 'text-gray-600 hover:bg-gray-100'}`}
              >
                {c.label}
              </button>
            ))}
            <button className="ml-auto text-[11px] text-gray-400 hover:text-gray-700" onClick={() => void load()}>刷新</button>
          </div>
          <div className="divide-y divide-gray-100 max-h-[75vh] overflow-y-auto">
            {items.length === 0 && <div className="px-4 py-6 text-center text-xs text-gray-400">暂无通知</div>}
            {items.map(m => {
              const cat = CATEGORIES.find(c => c.value === m.category)
              return (
                <div key={m.id} className="px-4 py-3">
                  <div className="flex items-center gap-2 text-xs mb-1">
                    <span className="tabular-nums text-gray-500">{fmtTime(m.created_at)}</span>
                    {cat && <span className={`rounded px-1.5 py-0.5 text-[10px] ${cat.cls}`}>{cat.label}</span>}
                    <span className="font-medium">{m.title}</span>
                    <span className="ml-auto text-[10px]">
                      {m.pushed
                        ? <span className="text-emerald-600">已推送</span>
                        : m.push_error
                          ? <span className="text-rose-600" title={m.push_error}>推送失败</span>
                          : <span className="text-gray-400">未推送</span>}
                    </span>
                  </div>
                  <pre className="whitespace-pre-wrap break-words text-xs text-gray-700 font-sans">{m.body}</pre>
                </div>
              )
            })}
          </div>
        </div>
      </div>
    </Layout>
  )
}
