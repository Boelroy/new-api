import { useCallback, useEffect, useState } from 'react'
import { Copy, ExternalLink } from 'lucide-react'
import Layout from '../components/Layout'
import { Badge, Button, Card, Input, Select, Textarea } from '../components/ui'
import { toast } from '../components/feedback'
import {
  api,
  ROLE_ADMIN,
  type CCMaxAccount,
  type CCMaxOAuthStart,
  type CCMaxProxyInput,
  type CCMaxSettings,
  type CCMaxSubmitForm,
  type SupplierMetric,
} from '../api'
import { getCachedRole, loadRole } from '../auth'

// CC Max 上号 — supplier-driven Claude (Max subscription) OAuth login, the web
// version of claude_oauth.py. The supplier fills in their own socks5; the
// server checks its exit and hands back an authorize URL. After the supplier
// pastes the code and the portal's required fields, the server exchanges tokens
// through that same socks5 and uploads the account to the 账号资源录入 portal's
// claude lane. List + usage below come from the portal.

const EMPTY_PROXY: CCMaxProxyInput = { url: '', host: '', port: '', username: '', password: '' }
const EMPTY_FORM: CCMaxSubmitForm = {
  plan: '',
  account: '',
  password: '',
  register_location: '',
  register_time: '',
  plan_start_time: '',
  plan_end_time: '',
}

// Portal account status → label (account statuses + the claude/CPA lifecycle).
const STATUS_LABELS: Record<string, string> = {
  online: '线上',
  store: '储备',
  offline: '已下线',
  check_fail: '验证失败',
  wait_check: '待验证',
  arrearage: '已欠费',
  fault: '故障',
  suspended: '挂起',
  init: '初始化',
  bound: '绑定完成',
  cooking: '养号中',
  cooking_failed: '养号失败',
  ready: '待登录',
  using: '使用中',
  login_failed: '登录失败',
  re_login: '待重新登录',
  re_login_failed: '重登多次失败',
  pending_activation: '待重新激活',
  bound_failed: '绑定失败',
  ban: '已封禁',
  sync_failed: '同步失败',
}

// The portal metrics endpoint takes "YYYY-MM-DD HH:mm:ss" in local (UTC+8) time
// and caps the span at 7 days.
function fmtLocal(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

const RANGES = [
  { key: 'today', label: '今日', days: 0 },
  { key: '3d', label: '近 3 天', days: 3 },
  { key: '7d', label: '近 7 天', days: 7 },
]

export default function CCMaxAccounts() {
  const [proxy, setProxy] = useState<CCMaxProxyInput>(EMPTY_PROXY)
  const [session, setSession] = useState<CCMaxOAuthStart | null>(null)
  const [code, setCode] = useState('')
  const [form, setForm] = useState<CCMaxSubmitForm>(EMPTY_FORM)
  const [plans, setPlans] = useState<string[]>([])
  const [locations, setLocations] = useState<{ value: string; label: string }[]>([])
  const [starting, setStarting] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [rows, setRows] = useState<CCMaxAccount[]>([])
  const [fxRate, setFxRate] = useState(7.2)
  const [loaded, setLoaded] = useState(false)
  const [range, setRange] = useState('today')
  const [metrics, setMetrics] = useState<Record<number, SupplierMetric>>({})
  const [loadingMetrics, setLoadingMetrics] = useState(false)
  const [role, setRole] = useState<number | null>(getCachedRole())

  useEffect(() => {
    if (role !== null) return
    void loadRole().then(setRole)
  }, [role])
  const isAdmin = role != null && role >= ROLE_ADMIN

  const load = useCallback(async () => {
    try {
      const res = await api.ccmaxAccounts()
      setRows(res.accounts ?? [])
      if (res.fx_rate > 0) setFxRate(res.fx_rate)
    } catch (e) {
      toast.error(e)
    } finally {
      setLoaded(true)
    }
  }, [])

  const loadMetrics = useCallback(async (rangeKey: string) => {
    const days = RANGES.find((r) => r.key === rangeKey)?.days ?? 0
    const now = new Date()
    const begin = new Date(now)
    if (days === 0) begin.setHours(0, 0, 0, 0)
    // Stay a minute inside the portal's 7-day cap.
    else begin.setTime(now.getTime() - days * 86400_000 + 60_000)
    setLoadingMetrics(true)
    try {
      const res = await api.ccmaxMetrics({ begin_time: fmtLocal(begin), end_time: fmtLocal(now) })
      const map: Record<number, SupplierMetric> = {}
      for (const m of res.accounts ?? []) map[m.aid] = m
      setMetrics(map)
    } catch (e) {
      toast.error(e)
    } finally {
      setLoadingMetrics(false)
    }
  }, [])

  useEffect(() => {
    void load()
    void loadMetrics('today')
    api
      .ccmaxOptions()
      .then((res) => {
        setPlans(res.plans ?? [])
        setLocations(res.register_locations ?? [])
      })
      .catch((e) => toast.error(e))
  }, [load, loadMetrics])

  async function start() {
    if (!proxy.url.trim() && (!proxy.host.trim() || !proxy.port.trim())) {
      toast.error('请填写代理 URL，或 socks5 地址和端口')
      return
    }
    setStarting(true)
    try {
      setSession(await api.ccmaxOAuthStart(proxy))
      setCode('')
    } catch (e) {
      toast.error(e)
    } finally {
      setStarting(false)
    }
  }

  async function submit() {
    if (!session || !code.trim()) {
      toast.error('请粘贴授权 code')
      return
    }
    if (Object.values(form).some((v) => !v.trim())) {
      toast.error('请填写全部必填项')
      return
    }
    setSubmitting(true)
    try {
      const res = await api.ccmaxSubmit({ ...form, state: session.state, code: code.trim() })
      toast.success(`${res.msg}：${res.email}`)
      setSession(null)
      setCode('')
      setForm(EMPTY_FORM)
      void load()
    } catch (e) {
      // When the authorization itself failed the session is gone and a new link
      // is needed; a portal-side rejection keeps it so the form can be fixed.
      if (String(e instanceof Error ? e.message : e).includes('session_gone')) setSession(null)
      toast.error(e)
    } finally {
      setSubmitting(false)
    }
  }

  const field = (key: keyof CCMaxSubmitForm) => ({
    value: form[key],
    onChange: (e: { target: { value: string } }) => setForm({ ...form, [key]: e.target.value }),
  })

  return (
    <Layout title="CC Max 上号" subtitle="通过你自己的 socks5 代理完成 Claude 账号授权并上号">
      {isAdmin && <SettingsCard />}

      <Card className="space-y-4 p-4">
        <div className="text-sm font-semibold">1. 填写 socks5 代理</div>
        <label className="block space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">代理 URL（直接粘贴，填了则忽略下方分项）</span>
          <Input
            value={proxy.url}
            autoComplete="off"
            onChange={(e) => setProxy({ ...proxy, url: e.target.value })}
            placeholder="socks5://user:pass@1.2.3.4:1080"
            className="font-mono"
          />
        </label>
        <fieldset
          disabled={!!proxy.url.trim()}
          className="grid gap-4 disabled:opacity-50 sm:grid-cols-2 lg:grid-cols-4"
        >
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">地址 (host)</span>
            <Input value={proxy.host} onChange={(e) => setProxy({ ...proxy, host: e.target.value })} placeholder="1.2.3.4" />
          </label>
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">端口 (port)</span>
            <Input
              value={proxy.port}
              inputMode="numeric"
              onChange={(e) => setProxy({ ...proxy, port: e.target.value })}
              placeholder="1080"
            />
          </label>
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">用户名 (可选)</span>
            <Input
              value={proxy.username}
              autoComplete="off"
              onChange={(e) => setProxy({ ...proxy, username: e.target.value })}
            />
          </label>
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">密码 (可选)</span>
            <Input
              type="password"
              value={proxy.password}
              autoComplete="new-password"
              onChange={(e) => setProxy({ ...proxy, password: e.target.value })}
            />
          </label>
        </fieldset>
        <div className="flex items-center gap-3">
          <Button onClick={start} disabled={starting}>
            {starting ? '检测代理中…' : session ? '重新生成授权链接' : '检测代理并生成授权链接'}
          </Button>
          {session && (
            <span className="text-xs text-muted-foreground tnum">
              出口 {session.exit_ip} · {session.country} · {session.timezone}
            </span>
          )}
        </div>
      </Card>

      {session && (
        <Card className="mt-4 space-y-4 p-4">
          <div className="text-sm font-semibold">2. 在已登录 claude.ai 的浏览器中打开链接并点击 Authorize</div>
          <div className="flex items-start gap-2">
            <code className="min-w-0 flex-1 break-all rounded-md bg-muted px-3 py-2 font-mono text-[11px]">
              {session.auth_url}
            </code>
            <Button
              size="sm"
              variant="outline"
              onClick={() =>
                navigator.clipboard.writeText(session.auth_url).then(
                  () => toast.success('已复制'),
                  () => toast.error('复制失败'),
                )
              }
            >
              <Copy className="size-3.5" /> 复制
            </Button>
            <Button size="sm" variant="outline" onClick={() => window.open(session.auth_url, '_blank', 'noopener')}>
              <ExternalLink className="size-3.5" /> 打开
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            授权后浏览器会跳转到 localhost:54545/callback，页面打不开是正常的。复制地址栏中的 code（或整段地址）粘贴到下方。
            链接 30 分钟内有效。
          </p>

          <div className="text-sm font-semibold">3. 粘贴 code 并填写账号信息</div>
          <label className="block space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">授权 code</span>
            <Textarea
              rows={2}
              value={code}
              onChange={(e) => setCode(e.target.value)}
              placeholder="code#state 或整段回调地址"
              className="font-mono text-xs"
            />
          </label>
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">套餐</span>
              <Select className="w-full" {...field('plan')}>
                <option value="">请选择套餐</option>
                {plans.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </Select>
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">账号（登录 ID / 邮箱，不能包含空格）</span>
              <Input autoComplete="off" placeholder="请输入账号" {...field('account')} />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">密码</span>
              <Input type="password" autoComplete="new-password" placeholder="请输入密码" {...field('password')} />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">注册地</span>
              <Select className="w-full" {...field('register_location')}>
                <option value="">请选择</option>
                {locations.map((l) => (
                  <option key={l.value} value={l.value}>
                    {l.label}
                  </option>
                ))}
              </Select>
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">注册时间</span>
              <Input type="date" {...field('register_time')} />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">套餐开始时间</span>
              <Input type="date" {...field('plan_start_time')} />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">套餐结束时间</span>
              <Input type="date" {...field('plan_end_time')} />
            </label>
          </div>
          <Button onClick={submit} disabled={submitting}>
            {submitting ? '提交中…' : '提交上号'}
          </Button>
        </Card>
      )}

      <Card className="mt-4 overflow-hidden p-0">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <div className="text-sm font-semibold">已上号账号</div>
          <div className="flex items-center gap-2 text-[11px] text-muted-foreground tnum">
            <span>共 {rows.length} 个</span>
            <Select
              value={range}
              onChange={(e) => {
                setRange(e.target.value)
                void loadMetrics(e.target.value)
              }}
            >
              {RANGES.map((r) => (
                <option key={r.key} value={r.key}>
                  用量：{r.label}
                </option>
              ))}
            </Select>
            <Button
              size="sm"
              variant="outline"
              disabled={loadingMetrics}
              onClick={() => {
                void load()
                void loadMetrics(range)
              }}
            >
              {loadingMetrics ? '刷新中…' : '刷新'}
            </Button>
          </div>
        </div>
        {rows.length === 0 ? (
          <p className="px-4 py-8 text-center text-xs text-muted-foreground">{loaded ? '暂无账号。' : '加载中…'}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="bg-muted/50 text-muted-foreground">
                <tr>
                  {[
                    '账号',
                    '别名',
                    '状态',
                    '套餐',
                    '注册地',
                    '套餐结束',
                    ...(isAdmin ? ['号商'] : []),
                    '请求数',
                    '输入 / 输出 tokens',
                    '成功率',
                    '消耗 (USD)',
                    '上号时间',
                  ].map((h) => (
                    <th key={h} className="whitespace-nowrap px-3 py-2 font-medium">
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => {
                  const m = metrics[r.id]
                  return (
                    <tr key={r.id} className="border-t border-border">
                      <td className="px-3 py-1.5 font-mono">{r.account}</td>
                      <td className="px-3 py-1.5 font-mono">{r.alias}</td>
                      <td className="px-3 py-1.5">
                        <Badge>{STATUS_LABELS[r.status] || r.status || '—'}</Badge>
                      </td>
                      <td className="px-3 py-1.5">{r.plan}</td>
                      <td className="px-3 py-1.5">{r.register_location}</td>
                      <td className="whitespace-nowrap px-3 py-1.5 tnum">{r.plan_end_time}</td>
                      {isAdmin && <td className="px-3 py-1.5">{r.username || '—'}</td>}
                      <td className="px-3 py-1.5 tnum">{m?.requests ?? '—'}</td>
                      <td className="whitespace-nowrap px-3 py-1.5 tnum">
                        {m ? `${m.prompt_tokens ?? 0} / ${m.completion_tokens ?? 0}` : '—'}
                      </td>
                      <td className="px-3 py-1.5 tnum">
                        {m?.success_rate != null ? `${m.success_rate}%` : '—'}
                      </td>
                      <td className="px-3 py-1.5 tnum">{m?.cost != null ? `$${(m.cost / fxRate).toFixed(2)}` : '—'}</td>
                      <td className="whitespace-nowrap px-3 py-1.5 tnum">{r.created_at}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </Layout>
  )
}

// SettingsCard — admin-only CC Max portal login + OpenAPI token (a separate
// portal user from 账号上号). Blank inputs leave the stored value unchanged.
function SettingsCard() {
  const [settings, setSettings] = useState<CCMaxSettings | null>(null)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [token, setToken] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api
      .getCCMaxSettings()
      .then((s) => {
        setSettings(s)
        setUsername(s.username)
      })
      .catch((e) => toast.error(e))
  }, [])

  async function save() {
    setSaving(true)
    try {
      const s = await api.setCCMaxSettings({
        username,
        ...(password ? { password } : {}),
        ...(token ? { openapi_token: token } : {}),
      })
      setSettings(s)
      setPassword('')
      setToken('')
      toast.success('已保存')
    } catch (e) {
      toast.error(e)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card className="mb-4 space-y-3 p-4">
      <div className="text-sm font-semibold">CC Max 号池配置（管理员）</div>
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">号池登录账号</span>
          <Input value={username} autoComplete="off" onChange={(e) => setUsername(e.target.value)} />
        </label>
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">
            号池登录密码{settings?.password_set ? '（已设置）' : ''}
          </span>
          <Input
            type="password"
            value={password}
            autoComplete="new-password"
            placeholder={settings?.password_set ? '留空不修改' : ''}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">
            OpenAPI Token{settings?.openapi_token_set ? `（…${settings.openapi_token_last4}）` : ''}
          </span>
          <Input
            type="password"
            value={token}
            autoComplete="off"
            placeholder={settings?.openapi_token_set ? '留空不修改' : ''}
            onChange={(e) => setToken(e.target.value)}
          />
        </label>
        <div className="flex items-end">
          <Button className="w-full" onClick={save} disabled={saving}>
            {saving ? '保存中…' : '保存'}
          </Button>
        </div>
      </div>
    </Card>
  )
}
