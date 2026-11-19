import { useCallback, useEffect, useState, type ReactNode } from 'react'
import Layout from '../components/Layout'
import { lastBeijingMonth } from '../lib/month'
import { Badge, Button, Card, Input } from '../components/ui'
import { toast } from '../components/feedback'
import { api, type TencentPushLog, type TencentPushSettings, type TencentPushSettingsUpdate } from '../api'

// 腾讯一站 · 推送 — the statement's own Lark push (schedule + manual) and, in
// one place, the existing push settings of the other pages; they are stored
// where those pages keep them, so editing here or there is the same.

function fmtDate(epochSec: number): string {
  return epochSec ? new Date(epochSec * 1000).toLocaleString() : '—'
}

function Field(props: { label: string; children: ReactNode }) {
  return (
    <label className="space-y-1.5">
      <span className="block text-xs font-medium text-muted-foreground">{props.label}</span>
      {props.children}
    </label>
  )
}

export default function TencentPush() {
  const [settings, setSettings] = useState<TencentPushSettings | null>(null)
  const [logs, setLogs] = useState<TencentPushLog[]>([])

  const load = useCallback(async () => {
    try {
      const [s, l] = await Promise.all([api.tencentPushSettings(), api.tencentPushLogs()])
      setSettings(s)
      setLogs(l.list)
    } catch (e) {
      toast.error(e)
    }
  }, [])
  useEffect(() => {
    void load()
  }, [load])

  async function save(update: TencentPushSettingsUpdate) {
    try {
      setSettings(await api.setTencentPushSettings(update))
      toast.success('已保存')
    } catch (e) {
      toast.error(e)
    }
  }

  return (
    <Layout title="腾讯一站 · 推送" subtitle="对账单 Lark 推送，以及各推送配置集中编辑">
      {settings ? (
        <>
          <StatementPushCard settings={settings} onSave={save} onPushed={() => void load()} />
          <ExistingPushCard settings={settings} onSave={save} />
        </>
      ) : (
        <p className="py-8 text-center text-xs text-muted-foreground">加载中…</p>
      )}
      <Card className="overflow-hidden p-0">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <div className="text-sm font-semibold">推送记录</div>
          <Button size="sm" variant="outline" onClick={() => void load()}>
            刷新
          </Button>
        </div>
        {logs.length === 0 ? (
          <p className="px-4 py-6 text-center text-xs text-muted-foreground">暂无推送记录。</p>
        ) : (
          <table className="w-full text-left text-xs">
            <thead className="bg-muted/50 text-muted-foreground">
              <tr>
                {['时间', '类型', '触发', '月份', '结果', '错误信息'].map((h) => (
                  <th key={h} className="whitespace-nowrap px-3 py-2 font-medium">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {logs.map((l) => (
                <tr key={l.id} className="border-t border-border">
                  <td className="whitespace-nowrap px-3 py-1.5 tnum">{fmtDate(l.created_at)}</td>
                  <td className="px-3 py-1.5">{l.kind === 'statement' ? '对账单' : l.kind}</td>
                  <td className="px-3 py-1.5">{l.trigger === 'schedule' ? '定时' : '手动'}</td>
                  <td className="px-3 py-1.5 tnum">{l.month}</td>
                  <td className="px-3 py-1.5">
                    <Badge tone={l.ok ? 'success' : 'danger'}>{l.ok ? '成功' : '失败'}</Badge>
                  </td>
                  <td className="px-3 py-1.5 text-destructive">{l.error}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </Layout>
  )
}

function StatementPushCard(props: {
  settings: TencentPushSettings
  onSave: (u: TencentPushSettingsUpdate) => Promise<void>
  onPushed: () => void
}) {
  const [form, setForm] = useState(props.settings.statement)
  const [month, setMonth] = useState(lastBeijingMonth())
  const [pushing, setPushing] = useState(false)
  useEffect(() => setForm(props.settings.statement), [props.settings.statement])

  async function push() {
    setPushing(true)
    try {
      const res = await api.pushTencentStatement(month)
      toast.success(`已推送 ${res.month} 对账单（${res.status === 'confirmed' ? '已确认' : '草稿'}）`)
    } catch (e) {
      toast.error(e)
    } finally {
      setPushing(false)
      props.onPushed()
    }
  }

  return (
    <Card className="mb-4 space-y-3 p-4">
      <div className="text-sm font-semibold">对账单推送</div>
      <div className="flex flex-wrap items-end gap-3">
        <Field label="Lark Webhook（独立于其他推送）">
          <Input
            aria-label="对账推送 Webhook"
            className="w-96"
            value={form.webhook}
            placeholder="https://open.larksuite.com/open-apis/bot/v2/hook/…"
            onChange={(e) => setForm({ ...form, webhook: e.target.value })}
          />
        </Field>
        <Field label="站点地址（下载链接）">
          <Input
            aria-label="站点地址"
            className="w-64"
            value={form.base_url}
            placeholder="https://report.example.com"
            onChange={(e) => setForm({ ...form, base_url: e.target.value })}
          />
        </Field>
      </div>
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex h-8 items-center gap-2 text-xs">
          <Input
            type="checkbox"
            aria-label="每月自动推送上月"
            checked={form.enabled}
            onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
          />
          每月自动推送上月
        </label>
        <Field label="每月几号（1–28）">
          <Input
            aria-label="推送日"
            type="number"
            className="w-24"
            value={form.day}
            onChange={(e) => setForm({ ...form, day: Number(e.target.value) })}
          />
        </Field>
        <Field label="几点（北京时间）">
          <Input
            aria-label="推送小时"
            type="number"
            className="w-24"
            value={form.hour}
            onChange={(e) => setForm({ ...form, hour: Number(e.target.value) })}
          />
        </Field>
        <Button onClick={() => void props.onSave({ statement: form })}>保存对账推送</Button>
        {props.settings.statement_last_month && (
          <span className="text-xs text-muted-foreground">定时已推到 {props.settings.statement_last_month}</span>
        )}
      </div>
      <div className="flex flex-wrap items-end gap-3 border-t border-border pt-3">
        <Field label="手动推送月份">
          <Input aria-label="推送月份" type="month" className="w-40" value={month} onChange={(e) => setMonth(e.target.value)} />
        </Field>
        <Button variant="outline" disabled={pushing || !props.settings.statement.webhook} onClick={() => void push()}>
          {pushing ? '推送中…' : '立即推送'}
        </Button>
        <span className="text-xs text-muted-foreground">该月未生成对账单时会先生成草稿。</span>
      </div>
    </Card>
  )
}

function ExistingPushCard(props: { settings: TencentPushSettings; onSave: (u: TencentPushSettingsUpdate) => Promise<void> }) {
  const s = props.settings
  const [quotaWebhook, setQuotaWebhook] = useState('')
  const [ccmax, setCCMax] = useState(s.ccmax_report)
  const [proxy, setProxy] = useState(s.proxy_report)
  const [ops, setOps] = useState(s.ops_daily_spend)
  useEffect(() => {
    setCCMax(s.ccmax_report)
    setProxy(s.proxy_report)
    setOps(s.ops_daily_spend)
  }, [s])

  async function save() {
    await props.onSave({
      ...(quotaWebhook.trim() ? { supplier_quota_webhook: quotaWebhook.trim() } : {}),
      ccmax_report: ccmax,
      proxy_report: proxy,
      ops_daily_spend: ops,
    })
    setQuotaWebhook('')
  }

  return (
    <Card className="mb-4 space-y-4 p-4">
      <div className="text-sm font-semibold">现有推送配置</div>
      <div className="grid gap-4 lg:grid-cols-2">
        <div className="space-y-2">
          <div className="text-xs font-semibold">账号上号 · 额度预警</div>
          <Field
            label={s.supplier_quota.webhook_set ? `Webhook（已设置，…${s.supplier_quota.webhook_last4}）` : 'Webhook（未设置）'}
          >
            <Input
              aria-label="额度预警 Webhook"
              value={quotaWebhook}
              placeholder="留空不修改"
              onChange={(e) => setQuotaWebhook(e.target.value)}
            />
          </Field>
        </div>
        <div className="space-y-2">
          <div className="text-xs font-semibold">CC Max 账号统计</div>
          <Field label="Webhook">
            <Input aria-label="CC Max Webhook" value={ccmax.webhook} onChange={(e) => setCCMax({ ...ccmax, webhook: e.target.value })} />
          </Field>
          <Field label="推送小时（UTC+8，逗号分隔）">
            <Input aria-label="CC Max 推送小时" value={ccmax.hours} onChange={(e) => setCCMax({ ...ccmax, hours: e.target.value })} />
          </Field>
        </div>
        <div className="space-y-2">
          <div className="text-xs font-semibold">New API Proxy 用量</div>
          <Field label="Webhook">
            <Input aria-label="Proxy Webhook" value={proxy.webhook} onChange={(e) => setProxy({ ...proxy, webhook: e.target.value })} />
          </Field>
          <Field label="推送小时（UTC+8，逗号分隔，空 = 仅手动）">
            <Input aria-label="Proxy 推送小时" value={proxy.hours} onChange={(e) => setProxy({ ...proxy, hours: e.target.value })} />
          </Field>
        </div>
        <div className="space-y-2">
          <div className="text-xs font-semibold">运维通知 · 每日消耗</div>
          <div className="flex flex-wrap gap-4 text-xs">
            <label className="flex items-center gap-2">
              <Input
                type="checkbox"
                aria-label="启用运维通知"
                checked={ops.enabled}
                onChange={(e) => setOps({ ...ops, enabled: e.target.checked })}
              />
              启用运维通知
            </label>
            <label className="flex items-center gap-2">
              <Input
                type="checkbox"
                aria-label="推送每日消耗"
                checked={ops.daily_spend}
                onChange={(e) => setOps({ ...ops, daily_spend: e.target.checked })}
              />
              推送每日消耗
            </label>
          </div>
          <Field label="Webhook">
            <Input aria-label="运维 Webhook" value={ops.webhook} onChange={(e) => setOps({ ...ops, webhook: e.target.value })} />
          </Field>
          <Field label="每日消耗推送时间（UTC+8 小时）">
            <Input
              aria-label="每日消耗推送小时"
              type="number"
              className="w-24"
              value={ops.daily_spend_hour}
              onChange={(e) => setOps({ ...ops, daily_spend_hour: Number(e.target.value) })}
            />
          </Field>
        </div>
      </div>
      <Button onClick={() => void save()}>保存现有推送配置</Button>
    </Card>
  )
}
