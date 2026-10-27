import { useCallback, useEffect, useMemo, useState } from 'react'
import { CircleAlert, Copy, ExternalLink, KeyRound, Pencil } from 'lucide-react'
import Layout from '../components/Layout'
import { Badge, Button, Card, Input, Select, Textarea } from '../components/ui'
import { Modal, promptDialog, toast } from '../components/feedback'
import {
  api,
  ROLE_ADMIN,
  ROLE_CCMAX_MANAGER,
  type CCMaxAccount,
  type CCMaxManagers,
  type CCMaxReportSettings,
  type CCMaxOAuthStart,
  type CCMaxProxyInput,
  type CCMaxSettings,
  type CCMaxSubmitForm,
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
// Statuses that carry a portal-recorded reason (sync / check failure, or a
// Claude-side state like 封号 / token 撤销) worth a 查看原因 button.
const REASON_STATUSES = new Set([
  'check_fail',
  'sync_failed',
  'bound_failed',
  'cooking_failed',
  'login_failed',
  're_login',
  're_login_failed',
  'offline',
  'ban',
  'fault',
  'suspended',
])

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

type StatusTone = 'neutral' | 'success' | 'warning' | 'danger'

// Status → badge colour: green = serving, amber = in progress / needs a
// retry, red = failed or dead, grey (default) = parked.
const STATUS_TONES: Record<string, StatusTone> = {
  online: 'success',
  using: 'success',
  bound: 'success',
  ready: 'warning',
  init: 'warning',
  cooking: 'warning',
  wait_check: 'warning',
  re_login: 'warning',
  pending_activation: 'warning',
  suspended: 'warning',
  check_fail: 'danger',
  arrearage: 'danger',
  fault: 'danger',
  cooking_failed: 'danger',
  login_failed: 'danger',
  re_login_failed: 'danger',
  bound_failed: 'danger',
  ban: 'danger',
  sync_failed: 'danger',
}

// Filter key for "every red status" — not a portal status itself.
const FILTER_FAILED = '__failed__'

// fmtUntil renders the time left until a window resets: "1d 0h" / "1h 59m".
function fmtUntil(resetAt: number, nowSec: number): string {
  const left = resetAt - nowSec
  if (!resetAt || left <= 0) return '—'
  const d = Math.floor(left / 86400)
  const h = Math.floor((left % 86400) / 3600)
  const m = Math.floor((left % 3600) / 60)
  return d > 0 ? `${d}d ${h}h` : `${h}h ${m}m`
}

// UsageWindow is one rate-limit window row: label chip, fill bar, percent used
// and time until reset. used is a fraction (0.06 = 6%).
function UsageWindow(props: { label: string; chip: string; used: number; resetAt: number; nowSec: number }) {
  const pct = Math.max(0, Math.min(100, props.used * 100))
  const bar = pct >= 90 ? 'bg-destructive' : pct >= 70 ? 'bg-warning' : 'bg-success'
  return (
    <div className="flex items-center gap-2">
      <span className={`w-8 rounded px-1 text-center text-[11px] font-medium ${props.chip}`}>{props.label}</span>
      <span className="h-1.5 w-14 overflow-hidden rounded-full bg-muted">
        <span className={`block h-full rounded-full ${bar}`} style={{ width: `${pct}%` }} />
      </span>
      <span className="w-9 text-right">{Math.round(pct)}%</span>
      <span className="text-muted-foreground" title={props.resetAt ? new Date(props.resetAt * 1000).toLocaleString() : ''}>
        {fmtUntil(props.resetAt, props.nowSec)}
      </span>
    </div>
  )
}

// OAuthFlow is the socks5 → authorize link → paste code → form flow. Inline it
// uploads a new account; with reauth (rendered in a modal) it replaces that
// account's credential and proxy, prefilled from the portal, password optional.
function OAuthFlow(props: {
  plans: string[]
  locations: { value: string; label: string }[]
  reauth?: CCMaxAccount
  onDone: () => void
}) {
  const { plans, locations, reauth } = props
  const [proxy, setProxy] = useState<CCMaxProxyInput>(EMPTY_PROXY)
  const [session, setSession] = useState<CCMaxOAuthStart | null>(null)
  const [code, setCode] = useState('')
  const [form, setForm] = useState<CCMaxSubmitForm>(() =>
    reauth
      ? {
          plan: reauth.plan,
          account: reauth.account,
          password: '',
          register_location: reauth.register_location,
          register_time: reauth.register_time,
          plan_start_time: reauth.plan_start_time,
          plan_end_time: reauth.plan_end_time,
        }
      : EMPTY_FORM,
  )
  const [starting, setStarting] = useState(false)
  const [submitting, setSubmitting] = useState(false)

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
    // Re-authorizing may leave the password blank (unchanged).
    if (Object.entries(form).some(([k, v]) => !v.trim() && !(reauth && k === 'password'))) {
      toast.error('请填写全部必填项')
      return
    }
    setSubmitting(true)
    try {
      const res = await api.ccmaxSubmit({
        ...form,
        state: session.state,
        code: code.trim(),
        ...(reauth ? { account_id: reauth.id } : {}),
      })
      toast.success(`${res.msg}：${res.email}`)
      setSession(null)
      setCode('')
      setForm(EMPTY_FORM)
      props.onDone()
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
    <>
      {reauth && (
        <p className="mb-3 text-xs text-muted-foreground">
          用新的（或原来的）socks5 重新完成授权，提交后会替换该账号在号池中的凭据和代理。
        </p>
      )}
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
              <Input autoComplete="off" placeholder="请输入账号" readOnly={!!reauth} {...field('account')} />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">密码{reauth ? '（留空则不修改）' : ''}</span>
              <Input
                type="password"
                autoComplete="new-password"
                placeholder={reauth ? '留空则不修改' : '请输入密码'}
                {...field('password')}
              />
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
            {submitting ? '提交中…' : reauth ? '提交重新授权' : '提交上号'}
          </Button>
        </Card>
      )}
    </>
  )
}

export default function CCMaxAccounts() {
  // Account being re-authorized in the modal; null = modal closed.
  const [reauth, setReauth] = useState<CCMaxAccount | null>(null)
  // Ticks every minute so the reset countdowns stay current between reloads.
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000))
  useEffect(() => {
    const t = setInterval(() => setNowSec(Math.floor(Date.now() / 1000)), 60_000)
    return () => clearInterval(t)
  }, [])
  const [plans, setPlans] = useState<string[]>([])
  const [locations, setLocations] = useState<{ value: string; label: string }[]>([])
  const [rows, setRows] = useState<CCMaxAccount[]>([])
  const [fxRate, setFxRate] = useState(7.2)
  const [loaded, setLoaded] = useState(false)
  const [loading, setLoading] = useState(false)
  const [reason, setReason] = useState<{ account: string; text: string } | null>(null)
  const [role, setRole] = useState<number | null>(getCachedRole())
  const [statusFilter, setStatusFilter] = useState('')

  const statusCounts = useMemo(() => {
    const counts = new Map<string, number>()
    for (const r of rows) counts.set(r.status, (counts.get(r.status) ?? 0) + 1)
    return [...counts.entries()].sort((a, b) => b[1] - a[1])
  }, [rows])
  const failedCount = statusCounts.reduce((n, [s, c]) => (STATUS_TONES[s] === 'danger' ? n + c : n), 0)

  const visibleRows = useMemo(() => {
    if (!statusFilter) return rows
    if (statusFilter === FILTER_FAILED) return rows.filter((r) => STATUS_TONES[r.status] === 'danger')
    return rows.filter((r) => r.status === statusFilter)
  }, [rows, statusFilter])

  // Totals follow the active filter; window usage averages only the accounts
  // actually serving (using/online) so idle 0% rows don't drag it down.
  const stats = useMemo(() => {
    let cost = 0
    let input = 0
    let output = 0
    let five = 0
    let week = 0
    let serving = 0
    for (const r of visibleRows) {
      cost += r.cost_rmb
      input += r.input_tokens
      output += r.output_tokens
      if (STATUS_TONES[r.status] === 'success') {
        five += r.five_hour_used_percent
        week += r.week_used_percent
        serving++
      }
    }
    return {
      cost,
      input,
      output,
      serving,
      fiveAvg: serving ? five / serving : 0,
      weekAvg: serving ? week / serving : 0,
    }
  }, [visibleRows])

  useEffect(() => {
    if (role !== null) return
    void loadRole().then(setRole)
  }, [role])
  const isAdmin = role != null && role >= ROLE_ADMIN
  // A ccmax_manager only views their assigned suppliers' accounts: no upload
  // form, but the 号商 column so the rows can be told apart.
  const isManager = role === ROLE_CCMAX_MANAGER
  const showUploader = isAdmin || isManager

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.ccmaxAccounts()
      setRows(res.accounts ?? [])
      if (res.fx_rate > 0) setFxRate(res.fx_rate)
    } catch (e) {
      toast.error(e)
    } finally {
      setLoaded(true)
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    api
      .ccmaxOptions()
      .then((res) => {
        setPlans(res.plans ?? [])
        setLocations(res.register_locations ?? [])
      })
      .catch((e) => toast.error(e))
  }, [load])

  // exportCSV downloads the table as currently filtered (visibleRows).
  function exportCSV() {
    if (visibleRows.length === 0) return
    const resetTime = (at: number) => (at ? new Date(at * 1000).toLocaleString() : '')
    const header = [
      '账号',
      '别名',
      '代理',
      '状态',
      '套餐',
      '注册地',
      '注册时间',
      '套餐开始',
      '套餐结束',
      ...(showUploader ? ['号商'] : []),
      '备注',
      '花费 USD',
      '花费 CNY',
      '5h 用量 %',
      '5h 重置时间',
      '7d 用量 %',
      '7d 重置时间',
      '输入 Tokens',
      '输出 Tokens',
      '上号时间',
    ]
    const escape = (v: unknown) => {
      const s = v == null ? '' : String(v)
      return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s
    }
    const lines = visibleRows.map((r) =>
      [
        r.account,
        r.alias,
        r.proxy_display,
        STATUS_LABELS[r.status] || r.status,
        r.plan,
        r.register_location,
        r.register_time,
        r.plan_start_time,
        r.plan_end_time,
        ...(showUploader ? [r.username ?? ''] : []),
        r.remark,
        (r.cost_rmb / fxRate).toFixed(2),
        r.cost_rmb.toFixed(2),
        (r.five_hour_used_percent * 100).toFixed(1),
        resetTime(r.five_hour_reset_at),
        (r.week_used_percent * 100).toFixed(1),
        resetTime(r.week_reset_at),
        r.input_tokens,
        r.output_tokens,
        r.created_at,
      ]
        .map(escape)
        .join(','),
    )
    const csv = [header.join(','), ...lines].join('\n')
    // BOM so Excel opens it as UTF-8 without garbling.
    const blob = new Blob(['\ufeff' + csv], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `ccmax-accounts_${new Date().toISOString().slice(0, 10)}.csv`
    a.click()
    URL.revokeObjectURL(url)
  }

  async function showReason(row: CCMaxAccount) {
    setReason({ account: row.account, text: '加载中…' })
    try {
      const res = await api.ccmaxAccountReason(row.id)
      setReason({ account: row.account, text: res.found ? res.content : '暂无原因记录' })
    } catch (e) {
      setReason({ account: row.account, text: e instanceof Error ? e.message : String(e) })
    }
  }

  async function editRemark(row: CCMaxAccount) {
    const value = await promptDialog({
      title: '修改备注',
      message: row.account,
      defaultValue: row.remark,
      placeholder: '最多 200 字',
      confirmText: '保存',
    })
    if (value === null) return
    try {
      const res = await api.ccmaxSetRemark(row.id, value)
      setRows((prev) => prev.map((r) => (r.id === row.id ? { ...r, remark: res.remark } : r)))
      toast.success('备注已保存')
    } catch (e) {
      toast.error(e)
    }
  }

  return (
    <Layout
      title="CC Max 上号"
      subtitle={isManager ? '查看分配给你的号商的 CC Max 账号与用量' : '通过你自己的 socks5 代理完成 Claude 账号授权并上号'}
    >
      {isAdmin && <SettingsCard />}
      {isAdmin && <ManagersCard />}
      {isAdmin && <ReportCard />}

      {!isManager && <OAuthFlow plans={plans} locations={locations} onDone={() => void load()} />}

      <Card className="mt-4 overflow-hidden p-0">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <div className="text-sm font-semibold">已上号账号</div>
          <div className="flex items-center gap-2 text-[11px] text-muted-foreground tnum">
            <span>
              {statusFilter ? `${visibleRows.length} / ${rows.length}` : `共 ${rows.length}`} 个
            </span>
            <Button size="sm" variant="outline" disabled={visibleRows.length === 0} onClick={exportCSV}>
              导出 CSV
            </Button>
            <Button size="sm" variant="outline" disabled={loading} onClick={() => void load()}>
              {loading ? '刷新中…' : '刷新'}
            </Button>
          </div>
        </div>
        {rows.length > 0 && (
          <div className="space-y-3 border-b border-border px-4 py-3">
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
              {[
                ['账号数', visibleRows.length.toLocaleString()],
                ['使用中', stats.serving.toLocaleString()],
                ['累计花费', `¥${stats.cost.toFixed(2)} / $${(stats.cost / fxRate).toFixed(2)}`],
                ['输入 / 输出 tokens', `${stats.input.toLocaleString()} / ${stats.output.toLocaleString()}`],
                ['5 小时窗口均值', `${(stats.fiveAvg * 100).toFixed(2)}%`],
                ['7 天窗口均值', `${(stats.weekAvg * 100).toFixed(2)}%`],
              ].map(([label, value]) => (
                <div key={label} className="space-y-0.5">
                  <div className="text-[11px] text-muted-foreground">{label}</div>
                  <div className="text-sm font-semibold tnum">{value}</div>
                </div>
              ))}
            </div>
            <div className="flex flex-wrap items-center gap-1.5">
              {[
                { key: '', label: '全部', count: rows.length, tone: 'neutral' as StatusTone },
                ...(failedCount > 0
                  ? [{ key: FILTER_FAILED, label: '全部失败', count: failedCount, tone: 'danger' as StatusTone }]
                  : []),
                ...statusCounts.map(([s, c]) => ({
                  key: s,
                  label: STATUS_LABELS[s] || s || '—',
                  count: c,
                  tone: STATUS_TONES[s] ?? 'neutral',
                })),
              ].map((f) => (
                <button
                  key={f.key || '__all__'}
                  type="button"
                  onClick={() => setStatusFilter(f.key)}
                  className="rounded-full"
                >
                  <Badge
                    tone={f.tone}
                    className={statusFilter === f.key ? 'border-current' : 'opacity-60 hover:opacity-100'}
                  >
                    {f.label} <span className="tnum">{f.count}</span>
                  </Badge>
                </button>
              ))}
            </div>
          </div>
        )}
        {rows.length === 0 ? (
          <p className="px-4 py-8 text-center text-xs text-muted-foreground">{loaded ? '暂无账号。' : '加载中…'}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="bg-muted/50 text-muted-foreground">
                <tr>
                  {[
                    '账号',
                    '状态',
                    '套餐',
                    '注册地',
                    '套餐结束',
                    ...(showUploader ? ['号商'] : []),
                    '备注',
                    '用量',
                    '上号时间',
                    '',
                  ].map((h) => (
                    <th key={h} className="whitespace-nowrap px-3 py-2 font-medium">
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {visibleRows.map((r) => {
                  return (
                    <tr key={r.id} className="border-t border-border">
                      <td className="px-3 py-1.5">
                        <div className="font-mono">{r.account}</div>
                        <div className="font-mono text-[11px] text-muted-foreground">
                          <span title="别名">{r.alias}</span>
                          {r.proxy_display && <span title="当前代理"> · {r.proxy_display}</span>}
                        </div>
                      </td>
                      <td className="px-3 py-1.5">
                        <Badge tone={STATUS_TONES[r.status] ?? 'neutral'} className="whitespace-nowrap">
                          {STATUS_LABELS[r.status] || r.status || '—'}
                        </Badge>
                      </td>
                      <td className="px-3 py-1.5">{r.plan}</td>
                      <td className="px-3 py-1.5">{r.register_location}</td>
                      <td className="whitespace-nowrap px-3 py-1.5 tnum">{r.plan_end_time}</td>
                      {showUploader && <td className="px-3 py-1.5">{r.username || '—'}</td>}
                      <td className="px-3 py-1.5">
                        <div className="flex max-w-[220px] items-center gap-1">
                          <span className="truncate" title={r.remark}>
                            {r.remark || '—'}
                          </span>
                          {r.remark_editable && (
                            <Button
                              size="sm"
                              variant="ghost"
                              className="size-6 p-0"
                              title="修改备注"
                              aria-label="修改备注"
                              onClick={() => void editRemark(r)}
                            >
                              <Pencil className="size-3" />
                            </Button>
                          )}
                        </div>
                      </td>
                      <td className="whitespace-nowrap px-3 py-1.5 tnum">
                        <div className="space-y-1">
                          <div
                            className={
                              'flex items-baseline gap-1.5 ' +
                              (r.cost_rmb > 0 ? 'text-foreground' : 'text-muted-foreground')
                            }
                            title={`累计花费，汇率 ${fxRate}`}
                          >
                            <span className="text-base font-semibold">${(r.cost_rmb / fxRate).toFixed(2)}</span>
                            <span className="text-[11px] text-muted-foreground">¥{r.cost_rmb.toFixed(2)}</span>
                          </div>
                          <UsageWindow
                            label="5h"
                            chip="bg-primary/10 text-primary"
                            used={r.five_hour_used_percent}
                            resetAt={r.five_hour_reset_at}
                            nowSec={nowSec}
                          />
                          <UsageWindow
                            label="7d"
                            chip="bg-success/10 text-success"
                            used={r.week_used_percent}
                            resetAt={r.week_reset_at}
                            nowSec={nowSec}
                          />
                          <div className="text-[11px] text-muted-foreground">
                            输入/输出 {r.input_tokens.toLocaleString()} / {r.output_tokens.toLocaleString()}
                          </div>
                        </div>
                      </td>
                      <td className="whitespace-nowrap px-3 py-1.5 tnum">{r.created_at}</td>
                      <td className="whitespace-nowrap px-3 py-1.5 text-right">
                        <div className="flex justify-end gap-1">
                          {REASON_STATUSES.has(r.status) && (
                            <Button
                              size="sm"
                              variant="ghost"
                              className="size-7 p-0 text-destructive hover:text-destructive"
                              title="查看原因"
                              aria-label="查看原因"
                              onClick={() => void showReason(r)}
                            >
                              <CircleAlert className="size-4" />
                            </Button>
                          )}
                          {r.remark_editable && (
                            <Button
                              size="sm"
                              variant="ghost"
                              className="size-7 p-0"
                              title="重新授权"
                              aria-label="重新授权"
                              onClick={() => setReauth(r)}
                            >
                              <KeyRound className="size-4" />
                            </Button>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Modal
        open={reauth !== null}
        onClose={() => setReauth(null)}
        title={`重新授权 · ${reauth?.account ?? ''}`}
        className="max-w-3xl"
      >
        {reauth && (
          <OAuthFlow
            key={reauth.id}
            plans={plans}
            locations={locations}
            reauth={reauth}
            onDone={() => {
              setReauth(null)
              void load()
            }}
          />
        )}
      </Modal>

      <Modal open={reason !== null} onClose={() => setReason(null)} title={`失败原因 · ${reason?.account ?? ''}`}>
        <pre className="whitespace-pre-wrap break-words rounded-lg bg-muted p-4 text-sm leading-relaxed">
          {reason?.text}
        </pre>
      </Modal>
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

// ManagersCard — admin-only: which supplier_ccmax users each ccmax_manager
// may see on this page (read-only for the manager).
function ManagersCard() {
  const [data, setData] = useState<CCMaxManagers | null>(null)
  const [savingId, setSavingId] = useState<number | null>(null)

  useEffect(() => {
    api
      .ccmaxManagers()
      .then(setData)
      .catch((e) => toast.error(e))
  }, [])

  async function toggle(managerId: number, memberId: number, checked: boolean) {
    if (!data) return
    const manager = data.managers.find((m) => m.id === managerId)
    if (!manager) return
    const next = checked
      ? [...manager.member_ids, memberId]
      : manager.member_ids.filter((id) => id !== memberId)
    setSavingId(managerId)
    try {
      const res = await api.setCCMaxManagerMembers(managerId, next)
      setData({
        ...data,
        managers: data.managers.map((m) => (m.id === managerId ? { ...m, member_ids: res.member_ids } : m)),
      })
    } catch (e) {
      toast.error(e)
    } finally {
      setSavingId(null)
    }
  }

  if (!data || data.managers.length === 0) return null
  return (
    <Card className="mb-4 space-y-3 p-4">
      <div className="text-sm font-semibold">CC Max 汇总账号（管理员）</div>
      <p className="text-xs text-muted-foreground">勾选每个 CC Max Manager 可以查看哪些号商的账号。</p>
      {data.managers.map((m) => (
        <div key={m.id} className="space-y-1.5">
          <div className="text-xs font-medium">
            {m.username}
            <span className="ml-2 text-muted-foreground tnum">已选 {m.member_ids.length}</span>
          </div>
          <div className="flex flex-wrap gap-x-4 gap-y-1.5 text-xs">
            {data.suppliers.map((s) => (
              <label key={s.id} className="flex cursor-pointer items-center gap-1.5">
                <Input
                  type="checkbox"
                  checked={m.member_ids.includes(s.id)}
                  disabled={savingId === m.id}
                  onChange={(e) => void toggle(m.id, s.id, e.target.checked)}
                  className="rounded border-border"
                />
                <span>{s.username}</span>
              </label>
            ))}
          </div>
        </div>
      ))}
    </Card>
  )
}

// ReportCard — admin-only Lark push of CC Max totals (account count, running
// accounts, average 5h / 7d window, month-to-date cost) at the given hours.
function ReportCard() {
  const [form, setForm] = useState<CCMaxReportSettings>({ webhook: '', hours: '' })
  const [saving, setSaving] = useState(false)
  const [pushing, setPushing] = useState(false)
  const [preview, setPreview] = useState('')

  useEffect(() => {
    api
      .getCCMaxReportSettings()
      .then(setForm)
      .catch((e) => toast.error(e))
  }, [])

  async function save() {
    setSaving(true)
    try {
      setForm(await api.setCCMaxReportSettings(form))
      toast.success('已保存')
    } catch (e) {
      toast.error(e)
    } finally {
      setSaving(false)
    }
  }

  async function pushNow() {
    setPushing(true)
    try {
      const res = await api.pushCCMaxReport()
      setPreview(res.text)
      toast.success('已推送')
    } catch (e) {
      toast.error(e)
    } finally {
      setPushing(false)
    }
  }

  return (
    <Card className="mb-4 space-y-3 p-4">
      <div className="text-sm font-semibold">Lark 推送（管理员）</div>
      <p className="text-xs text-muted-foreground">
        按设定时间（UTC+8）推送账号总数、正在跑的账号数、5h / 7d 平均窗口和本月消耗。Webhook 留空即关闭推送。
      </p>
      <div className="grid gap-4 sm:grid-cols-[1fr_12rem_auto_auto]">
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">Lark Webhook</span>
          <Input
            value={form.webhook}
            autoComplete="off"
            placeholder="https://open.larksuite.com/open-apis/bot/v2/hook/..."
            onChange={(e) => setForm({ ...form, webhook: e.target.value })}
          />
        </label>
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">推送时间（时，逗号分隔）</span>
          <Input value={form.hours} placeholder="10,22" onChange={(e) => setForm({ ...form, hours: e.target.value })} />
        </label>
        <div className="flex items-end">
          <Button onClick={save} disabled={saving}>
            {saving ? '保存中…' : '保存'}
          </Button>
        </div>
        <div className="flex items-end">
          <Button variant="outline" onClick={pushNow} disabled={pushing || !form.webhook}>
            {pushing ? '推送中…' : '立即推送'}
          </Button>
        </div>
      </div>
      {preview && (
        <pre className="whitespace-pre-wrap break-words rounded-lg bg-muted p-3 text-xs leading-relaxed">{preview}</pre>
      )}
    </Card>
  )
}
