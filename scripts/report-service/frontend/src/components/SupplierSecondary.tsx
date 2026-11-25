import { useEffect, useState } from 'react'
import {
  api,
  type AuthUser,
  type Supplier2Account,
  type Supplier2Metrics,
  type SupplierReportSettings,
  type Supplier2Settings,
} from '../api'

// 账号 2 — a second login on the same 号池 portal, read-only: its settings
// (stored in report_config, applied without a restart), its live portal
// roster with each account's owner, its usage over a time range and the Lark
// usage report. Nothing here touches the 账号上号 (account 1) list, sync or
// quota alerts. The usage section and account table are shared with the
// owners' read-only 「我的账号」 page (pages/SupplierMine.tsx).

// Times are portal time (UTC+8) whatever the browser's zone, matching what the
// backend parses.
function fmtPortal(d: Date): string {
  const t = new Date(d.getTime() + 8 * 3600_000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${t.getUTCFullYear()}-${p(t.getUTCMonth() + 1)}-${p(t.getUTCDate())} ${p(t.getUTCHours())}:${p(t.getUTCMinutes())}:${p(t.getUTCSeconds())}`
}

// USD first, then the portal's RMB.
function fmtCost(usd: number | null | undefined, rmb: number | null | undefined): string {
  if (rmb == null) return '—'
  return `${usd != null ? `$${usd.toFixed(2)}` : '—'} · ¥${rmb.toFixed(2)}`
}

function fmtNum(n: number | null | undefined): string {
  return n != null ? n.toLocaleString() : '—'
}

function fmtRate(rate: number | null | undefined): string {
  return rate != null ? `${rate.toFixed(2)}%` : '—'
}

const inputCls = 'w-full border border-gray-300 rounded-md px-2.5 py-1.5 text-sm'
const errorBox = 'mb-3 bg-rose-50 border border-rose-200 text-rose-700 text-sm rounded-md px-3 py-2'

// Supplier2Usage is the time-range usage section: totals plus one row per
// account. showCost hides every cost figure (the owners' view never gets them
// from the server either).
export function Supplier2Usage({
  accounts,
  load,
  showCost,
  disabled,
}: {
  accounts: Supplier2Account[]
  load: (range: { begin_time: string; end_time: string }) => Promise<Supplier2Metrics>
  showCost: boolean
  disabled?: boolean
}) {
  const [beginTime, setBeginTime] = useState(() => fmtPortal(new Date(Date.now() - 7 * 86400_000)))
  const [endTime, setEndTime] = useState(() => fmtPortal(new Date()))
  const [metrics, setMetrics] = useState<Supplier2Metrics | null>(null)
  const [metricsErr, setMetricsErr] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  async function query() {
    setMetricsErr(null)
    setLoading(true)
    try {
      const res = await load({ begin_time: beginTime.trim(), end_time: endTime.trim() })
      if (!res.configured) {
        setMetrics(null)
        setMetricsErr(res.error || '账号 2 未配置')
        return
      }
      setMetrics(res)
      if (res.end_time) setEndTime(res.end_time)
    } catch (e: any) {
      setMetricsErr(e?.message || String(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (!disabled) void query()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [disabled])

  const rows = metrics?.accounts || []
  const byId = new Map(accounts.map(a => [a.id, a]))
  const total = metrics?.summary?.total
  const tiles: [string, string][] = total
    ? [
        ['账号', fmtNum(total.accounts)],
        ['请求数', fmtNum(total.requests)],
        ['Prompt', fmtNum(total.prompt_tokens)],
        ['Completion', fmtNum(total.completion_tokens)],
        ...(showCost ? [['成本', fmtCost(metrics?.total_cost_usd, total.cost)] as [string, string]] : []),
      ]
    : []

  return (
    <section className="mb-5 bg-white border border-gray-200 rounded-lg p-4 sm:p-5">
      <div className="flex flex-wrap items-end gap-2 mb-3">
        <div>
          <label className="block text-xs font-medium text-gray-600 mb-1">开始（UTC+8）</label>
          <input value={beginTime} onChange={e => setBeginTime(e.target.value)} className={`${inputCls} font-mono w-48`} />
        </div>
        <div>
          <label className="block text-xs font-medium text-gray-600 mb-1">结束（UTC+8）</label>
          <input value={endTime} onChange={e => setEndTime(e.target.value)} className={`${inputCls} font-mono w-48`} />
        </div>
        <button
          onClick={() => void query()}
          disabled={loading || disabled}
          className="px-3 py-1.5 text-sm rounded-md bg-gray-900 text-white disabled:opacity-50"
        >
          {loading ? '查询中…' : '查询用量'}
        </button>
        <span className="text-[11px] text-gray-500">超过 7 天会分段查询；结束时间不晚于当前。</span>
      </div>
      {metricsErr && <div className={errorBox}>{metricsErr}</div>}
      {total && (
        <div className={`mb-3 grid grid-cols-2 ${showCost ? 'sm:grid-cols-5' : 'sm:grid-cols-4'} gap-2 text-sm`}>
          {tiles.map(([k, v]) => (
            <div key={k} className="border border-gray-200 rounded-md px-3 py-2">
              <div className="text-[11px] text-gray-500">{k}</div>
              <div className="font-medium tabular-nums">{v}</div>
            </div>
          ))}
        </div>
      )}
      <div className="overflow-x-auto">
        <table className="w-full text-xs border-collapse">
          <thead>
            <tr className="text-left border-b border-gray-200">
              <th className="py-1.5 pr-3 mono-label">账号</th>
              <th className="py-1.5 pr-3 mono-label">厂商</th>
              <th className="py-1.5 pr-3 mono-label text-right">请求数</th>
              <th className="py-1.5 pr-3 mono-label text-right">Prompt</th>
              <th className="py-1.5 pr-3 mono-label text-right">Completion</th>
              {showCost && <th className="py-1.5 pr-3 mono-label text-right">成本($ · ¥)</th>}
              <th className="py-1.5 pr-3 mono-label text-right">成功率</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr>
                <td colSpan={showCost ? 7 : 6} className="py-3 text-center text-gray-400">
                  {metrics ? '该时间范围内暂无用量' : '尚未查询'}
                </td>
              </tr>
            ) : (
              rows.map(m => (
                <tr key={m.aid} className="border-b border-gray-100">
                  <td className="py-1.5 pr-3">{byId.get(m.aid)?.account_alias || m.account_alias || `#${m.aid}`}</td>
                  <td className="py-1.5 pr-3">{byId.get(m.aid)?.provider || '—'}</td>
                  <td className="py-1.5 pr-3 text-right tabular-nums">{fmtNum(m.requests)}</td>
                  <td className="py-1.5 pr-3 text-right tabular-nums">{fmtNum(m.prompt_tokens)}</td>
                  <td className="py-1.5 pr-3 text-right tabular-nums">{fmtNum(m.completion_tokens)}</td>
                  {showCost && <td className="py-1.5 pr-3 text-right tabular-nums">{fmtCost(m.cost_usd, m.cost)}</td>}
                  <td className="py-1.5 pr-3 text-right tabular-nums">{fmtRate(m.success_rate)}</td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
      {showCost && metrics?.fx_rate != null && (
        <p className="mt-2 text-[11px] text-gray-500">成本来自号池账单（约 2 小时延迟，按天）；USD 按汇率 {metrics.fx_rate} 折算。</p>
      )}
    </section>
  )
}

// OwnerCell edits one account's owner: type to search the user list (a shared
// datalist), Enter or leaving the box saves, an empty box clears.
function OwnerCell({
  account,
  users,
  onSaved,
}: {
  account: Supplier2Account
  users: AuthUser[]
  onSaved: (id: number, userId: number | null, username: string | null) => void
}) {
  const current = account.owner_username || ''
  const [value, setValue] = useState(current)
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => setValue(current), [current])

  async function save(next: string) {
    const name = next.trim()
    if (name === current) return
    setErr(null)
    const user = name ? users.find(u => u.username === name) : null
    if (name && !user) {
      setErr('没有这个用户')
      return
    }
    setSaving(true)
    try {
      const res = await api.setSupplier2Owner(account.id, user ? user.id : null)
      onSaved(account.id, res.owner_user_id, res.owner_username)
    } catch (e: any) {
      setErr(e?.message || String(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="min-w-[150px]">
      <div className="flex items-center gap-1">
        <input
          list="supplier2-owner-users"
          value={value}
          onChange={e => setValue(e.target.value)}
          onBlur={() => void save(value)}
          onKeyDown={e => {
            if (e.key === 'Enter') void save(value)
            if (e.key === 'Escape') setValue(current)
          }}
          disabled={saving}
          placeholder="未分配"
          className="w-full border border-gray-200 rounded px-1.5 py-0.5 text-xs disabled:opacity-50"
        />
        {current && (
          <button
            onClick={() => {
              setValue('')
              void save('')
            }}
            disabled={saving}
            title="清除归属"
            className="text-gray-400 hover:text-rose-600 px-1 disabled:opacity-50"
          >
            ×
          </button>
        )}
      </div>
      {err && <div className="text-[11px] text-rose-600 mt-0.5">{err}</div>}
    </div>
  )
}

// Supplier2AccountTable lists roster rows. With users it adds the admin's
// editable 归属 column.
export function Supplier2AccountTable({
  accounts,
  loading,
  error,
  onRefresh,
  users,
  onOwnerSaved,
}: {
  accounts: Supplier2Account[]
  loading: boolean
  error: string | null
  onRefresh: () => void
  users?: AuthUser[]
  onOwnerSaved?: (id: number, userId: number | null, username: string | null) => void
}) {
  const editable = users != null && onOwnerSaved != null
  const cols = editable ? 10 : 9
  return (
    <section className="mb-5 bg-white border border-gray-200 rounded-lg p-4 sm:p-5">
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-sm font-semibold">账号列表（{accounts.length}）</h3>
        <button
          onClick={onRefresh}
          disabled={loading}
          className="px-3 py-1.5 text-xs rounded-md border border-gray-300 disabled:opacity-50"
        >
          {loading ? '加载中…' : '刷新'}
        </button>
      </div>
      {error && <div className={errorBox}>{error}</div>}
      {editable && (
        <datalist id="supplier2-owner-users">
          {users.map(u => (
            <option key={u.id} value={u.username}>
              {u.studio ? `${u.studio} · #${u.id}` : `#${u.id}`}
            </option>
          ))}
        </datalist>
      )}
      <div className="overflow-x-auto">
        <table className="w-full text-xs border-collapse">
          <thead>
            <tr className="text-left border-b border-gray-200">
              <th className="py-1.5 pr-3 mono-label">ID</th>
              <th className="py-1.5 pr-3 mono-label">别名</th>
              <th className="py-1.5 pr-3 mono-label">厂商</th>
              <th className="py-1.5 pr-3 mono-label">模型</th>
              <th className="py-1.5 pr-3 mono-label">状态</th>
              <th className="py-1.5 pr-3 mono-label">类型</th>
              <th className="py-1.5 pr-3 mono-label">Key</th>
              <th className="py-1.5 pr-3 mono-label">备注</th>
              {editable && <th className="py-1.5 pr-3 mono-label">归属</th>}
              <th className="py-1.5 pr-3 mono-label">创建时间</th>
            </tr>
          </thead>
          <tbody>
            {accounts.length === 0 ? (
              <tr>
                <td colSpan={cols} className="py-3 text-center text-gray-400">{loading ? '加载中…' : '暂无账号'}</td>
              </tr>
            ) : (
              accounts.map(a => (
                <tr key={a.id} className="border-b border-gray-100 align-top">
                  <td className="py-1.5 pr-3 tabular-nums">{a.id}</td>
                  <td className="py-1.5 pr-3">{a.account_alias || '—'}</td>
                  <td className="py-1.5 pr-3">{a.provider || '—'}</td>
                  <td className="py-1.5 pr-3 max-w-[240px] break-words">{a.model || '—'}</td>
                  <td className="py-1.5 pr-3">{a.status || '—'}</td>
                  <td className="py-1.5 pr-3">{a.account_type === 1 ? '速刷号' : a.account_type === 0 ? '普通号' : '—'}</td>
                  <td className="py-1.5 pr-3 font-mono">{a.key_last8 ? `…${a.key_last8}` : '—'}</td>
                  <td className="py-1.5 pr-3">{a.remark || '—'}</td>
                  {editable && (
                    <td className="py-1.5 pr-3">
                      <OwnerCell account={a} users={users} onSaved={onOwnerSaved} />
                    </td>
                  )}
                  <td className="py-1.5 pr-3 whitespace-nowrap">{a.created_at || '—'}</td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </section>
  )
}

// SupplierReportCard is the admin's Lark usage report settings of one login
// (账号上号 or 账号 2): webhook (masked; blank keeps it), UTC+8 push hours,
// 立即推送 and the last outcome.
export function SupplierReportCard({
  confirmLabel,
  load,
  save: saveSettings,
  push: pushNow,
}: {
  confirmLabel: string
  load: () => Promise<SupplierReportSettings>
  save: (payload: { webhook?: string; clear_webhook?: boolean; hours?: string; min_usd?: number }) => Promise<SupplierReportSettings>
  push: () => Promise<unknown>
}) {
  const [report, setReport] = useState<SupplierReportSettings | null>(null)
  const [open, setOpen] = useState(false)
  const [webhook, setWebhook] = useState('')
  const [hours, setHours] = useState('')
  const [minUsd, setMinUsd] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [msg, setMsg] = useState<string | null>(null)

  function apply(r: SupplierReportSettings) {
    setReport(r)
    setHours(r.hours)
    setMinUsd(String(r.min_usd ?? ''))
  }

  useEffect(() => {
    load().then(apply).catch((e: any) => setErr(e?.message || String(e)))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function run(action: () => Promise<void>) {
    setErr(null)
    setMsg(null)
    setBusy(true)
    try {
      await action()
    } catch (e: any) {
      setErr(e?.message || String(e))
    } finally {
      setBusy(false)
    }
  }

  const save = (clearWebhook = false) =>
    run(async () => {
      const min = Number(minUsd.trim())
      if (!clearWebhook && (minUsd.trim() === '' || !Number.isFinite(min) || min < 0)) {
        throw new Error('最小播报金额须为非负数（0 = 不合并）')
      }
      apply(
        await saveSettings(
          clearWebhook ? { clear_webhook: true } : { webhook: webhook.trim() || undefined, hours: hours.trim(), min_usd: min },
        ),
      )
      setWebhook('')
      setMsg(clearWebhook ? '已关闭推送' : '已保存')
    })

  const push = () =>
    run(async () => {
      try {
        await pushNow()
        setMsg('已推送')
      } finally {
        // The outcome (success or failure) is recorded as the last result.
        apply(await load())
      }
    })

  const last = report?.last
  return (
    <section className="mb-5 bg-white border border-gray-200 rounded-lg p-4 sm:p-5">
      <button className="text-sm font-semibold" onClick={() => setOpen(v => !v)}>
        {open ? '▾' : '▸'} Lark 用量播报
        {report && (
          <span className="ml-2 text-xs font-normal text-gray-500">
            {report.webhook_set ? `已配置 · 推送时间 ${report.hours || '仅手动'}` : '未配置'}
            {last && ` · 最近一次${last.ok ? '成功' : '失败'}`}
          </span>
        )}
      </button>
      {open && (
        <div className="mt-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <div>
              <label className="block text-xs font-medium text-gray-600 mb-1">Lark Webhook</label>
              <input
                type="password"
                value={webhook}
                onChange={e => setWebhook(e.target.value)}
                autoComplete="new-password"
                placeholder={
                  report?.webhook_set ? `已设置（…${report.webhook_last4 || '****'}），留空不修改` : 'https://open.larksuite.com/open-apis/bot/v2/hook/…'
                }
                className={inputCls}
              />
            </div>
            <div>
              <label className="block text-xs font-medium text-gray-600 mb-1">推送时间（UTC+8 整点，逗号分隔）</label>
              <input value={hours} onChange={e => setHours(e.target.value)} placeholder="如 10,22；留空只手动推送" className={inputCls} />
            </div>
            <div>
              <label className="block text-xs font-medium text-gray-600 mb-1">最小播报金额（$）</label>
              <input
                value={minUsd}
                onChange={e => setMinUsd(e.target.value)}
                inputMode="decimal"
                placeholder="10"
                className={inputCls}
              />
              <p className="mt-1 text-[11px] text-gray-500">按今日费用：低于此金额的分组合并成末尾一行，组内低于此金额的账号合并成一行；0 = 不合并。</p>
            </div>
          </div>
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <button
              onClick={() => void save()}
              disabled={busy}
              className="px-3 py-1.5 text-sm rounded-md bg-gray-900 text-white disabled:opacity-50"
            >
              保存
            </button>
            <button
              onClick={() => void push()}
              disabled={busy || !report?.webhook_set}
              className="px-3 py-1.5 text-sm rounded-md border border-gray-300 disabled:opacity-50"
            >
              {busy ? '处理中…' : '立即推送'}
            </button>
            {report?.webhook_set && (
              <button
                onClick={() => {
                  if (window.confirm(`关闭${confirmLabel}的 Lark 播报（清除 Webhook）？`)) void save(true)
                }}
                disabled={busy}
                className="px-3 py-1.5 text-sm rounded-md border border-gray-300 disabled:opacity-50"
              >
                关闭推送
              </button>
            )}
            {msg && <span className="text-xs text-emerald-700">{msg}</span>}
          </div>
          {err && <p className="mt-2 text-xs text-rose-600">{err}</p>}
          <div className="mt-4">
            <div className="text-xs font-medium text-gray-600 mb-1">最近一次结果</div>
            {last ? (
              <div className="text-xs">
                <div className={last.ok ? 'text-emerald-700' : 'text-rose-600'}>
                  {new Date(last.at * 1000).toLocaleString()} · {last.trigger === 'manual' ? '手动' : '定时'} · {last.ok ? '成功' : '失败'}
                  {last.error && `：${last.error}`}
                </div>
                {last.text && (
                  <pre className="mt-1 max-h-72 overflow-auto whitespace-pre-wrap bg-gray-50 border border-gray-200 rounded-md p-2 text-[11px]">
                    {last.text}
                  </pre>
                )}
              </div>
            ) : (
              <div className="text-xs text-gray-400">尚未推送</div>
            )}
          </div>
        </div>
      )}
    </section>
  )
}

export default function SupplierSecondary({ onLabel }: { onLabel?: (label: string) => void }) {
  const [settings, setSettings] = useState<Supplier2Settings | null>(null)
  const [settingsErr, setSettingsErr] = useState<string | null>(null)
  const [showSettings, setShowSettings] = useState(false)
  const [label, setLabel] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [token, setToken] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveMsg, setSaveMsg] = useState<string | null>(null)

  const [accounts, setAccounts] = useState<Supplier2Account[]>([])
  const [accountsErr, setAccountsErr] = useState<string | null>(null)
  const [loadingAccounts, setLoadingAccounts] = useState(false)
  const [users, setUsers] = useState<AuthUser[]>([])
  // Bumped after a settings save so the usage section re-queries.
  const [usageKey, setUsageKey] = useState(0)

  async function loadSettings(): Promise<Supplier2Settings | null> {
    setSettingsErr(null)
    try {
      const s = await api.supplier2Settings()
      setSettings(s)
      setLabel(s.label)
      setUsername(s.username)
      onLabel?.(s.label)
      return s
    } catch (e: any) {
      setSettingsErr(e?.message || String(e))
      return null
    }
  }

  async function loadAccounts() {
    setAccountsErr(null)
    setLoadingAccounts(true)
    try {
      const res = await api.supplier2Accounts()
      if (!res.configured) {
        setAccounts([])
        setAccountsErr(res.error || '账号 2 未配置')
        return
      }
      setAccounts(res.accounts || [])
    } catch (e: any) {
      setAccountsErr(e?.message || String(e))
    } finally {
      setLoadingAccounts(false)
    }
  }

  useEffect(() => {
    void (async () => {
      const s = await loadSettings()
      if (!s?.web_configured) setShowSettings(true)
      if (s?.web_configured) await loadAccounts()
    })()
    api
      .listUsers()
      .then(r => setUsers([...r.users].sort((a, b) => a.username.localeCompare(b.username))))
      .catch(() => {})
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function handleSave(clear = false) {
    setSaveMsg(null)
    setSettingsErr(null)
    setSaving(true)
    try {
      await api.setSupplier2Settings(
        clear
          ? { clear: true }
          : { label: label.trim(), username: username.trim(), password: password || undefined, openapi_token: token.trim() || undefined },
      )
      setPassword('')
      setToken('')
      setSaveMsg(clear ? '已清除登录信息' : '已保存')
      const s = await loadSettings()
      if (s?.web_configured) await loadAccounts()
      setUsageKey(k => k + 1)
    } catch (e: any) {
      setSettingsErr(e?.message || String(e))
    } finally {
      setSaving(false)
    }
  }

  const configured = settings?.web_configured === true

  return (
    <div>
      {settings && !settings.portal_configured && (
        <div className="mb-4 bg-amber-50 border border-amber-200 text-amber-800 text-sm rounded-md px-3 py-2">
          号池地址未配置（<code>SUPPLIER_ACCOUNT_BASE_URL</code>），账号 2 与账号 1 共用该地址。
        </div>
      )}
      {settings && !configured && (
        <div className="mb-4 bg-amber-50 border border-amber-200 text-amber-800 text-sm rounded-md px-3 py-2">
          {settings.label} 未配置：请在下方填写号池登录用户名和密码；查看用量还需要 OpenAPI token。
        </div>
      )}
      {configured && settings && !settings.openapi_configured && (
        <div className="mb-4 bg-amber-50 border border-amber-200 text-amber-800 text-sm rounded-md px-3 py-2">
          未配置 OpenAPI token：可查看账号列表，用量暂不可用。
        </div>
      )}

      <section className="mb-5 bg-white border border-gray-200 rounded-lg p-4 sm:p-5">
        <button className="text-sm font-semibold" onClick={() => setShowSettings(v => !v)}>
          {showSettings ? '▾' : '▸'} 登录设置
          {settings && (
            <span className="ml-2 text-xs font-normal text-gray-500">
              {configured ? `已配置：${settings.username}` : '未配置'}
              {settings.openapi_token_set ? ` · token …${settings.openapi_token_last4 || '已设置'}` : ' · token 未设置'}
            </span>
          )}
        </button>
        {showSettings && (
          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            <div>
              <label className="block text-xs font-medium text-gray-600 mb-1">页签名称</label>
              <input value={label} onChange={e => setLabel(e.target.value)} placeholder="账号 2" className={inputCls} />
            </div>
            <div>
              <label className="block text-xs font-medium text-gray-600 mb-1">号池用户名</label>
              <input value={username} onChange={e => setUsername(e.target.value)} autoComplete="off" className={inputCls} />
            </div>
            <div>
              <label className="block text-xs font-medium text-gray-600 mb-1">号池密码</label>
              <input
                type="password"
                value={password}
                onChange={e => setPassword(e.target.value)}
                autoComplete="new-password"
                placeholder={settings?.password_set ? '已设置，留空不修改' : '未设置'}
                className={inputCls}
              />
            </div>
            <div>
              <label className="block text-xs font-medium text-gray-600 mb-1">OpenAPI token</label>
              <input
                type="password"
                value={token}
                onChange={e => setToken(e.target.value)}
                autoComplete="new-password"
                placeholder={settings?.openapi_token_set ? `已设置（…${settings.openapi_token_last4}），留空不修改` : '未设置'}
                className={inputCls}
              />
            </div>
            <div className="sm:col-span-2 flex items-center gap-2">
              <button
                onClick={() => void handleSave()}
                disabled={saving}
                className="px-3 py-1.5 text-sm rounded-md bg-gray-900 text-white disabled:opacity-50"
              >
                {saving ? '保存中…' : '保存'}
              </button>
              {configured && (
                <button
                  onClick={() => {
                    if (window.confirm('清除账号 2 的用户名、密码和 token？')) void handleSave(true)
                  }}
                  disabled={saving}
                  className="px-3 py-1.5 text-sm rounded-md border border-gray-300 disabled:opacity-50"
                >
                  清除登录信息
                </button>
              )}
              {saveMsg && <span className="text-xs text-emerald-700">{saveMsg}</span>}
            </div>
          </div>
        )}
        {settingsErr && <p className="mt-2 text-xs text-rose-600">{settingsErr}</p>}
      </section>

      {configured && (
        <SupplierReportCard
          confirmLabel={settings?.label || '账号 2'}
          load={api.supplier2Report}
          save={api.setSupplier2Report}
          push={api.pushSupplier2Report}
        />
      )}

      {configured && (
        <>
          <Supplier2Usage
            key={usageKey}
            accounts={accounts}
            load={api.supplier2Metrics}
            showCost
            disabled={!settings?.openapi_configured}
          />
          <Supplier2AccountTable
            accounts={accounts}
            loading={loadingAccounts}
            error={accountsErr}
            onRefresh={() => void loadAccounts()}
            users={users}
            onOwnerSaved={(id, userId, name) =>
              setAccounts(prev => prev.map(a => (a.id === id ? { ...a, owner_user_id: userId, owner_username: name } : a)))
            }
          />
        </>
      )}
    </div>
  )
}
