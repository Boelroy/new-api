import { useEffect, useState } from 'react'
import { api, type Supplier2Account, type Supplier2Metrics, type Supplier2Settings } from '../api'

// 账号 2 — a second login on the same 号池 portal, admin-only and read-only:
// its settings (stored in report_config, applied without a restart), its live
// portal roster, and its usage over a time range. Nothing here touches the
// 账号上号 (account 1) list, sync or quota alerts.

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

  const [beginTime, setBeginTime] = useState(() => fmtPortal(new Date(Date.now() - 7 * 86400_000)))
  const [endTime, setEndTime] = useState(() => fmtPortal(new Date()))
  const [metrics, setMetrics] = useState<Supplier2Metrics | null>(null)
  const [metricsErr, setMetricsErr] = useState<string | null>(null)
  const [loadingMetrics, setLoadingMetrics] = useState(false)

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

  async function loadMetrics() {
    setMetricsErr(null)
    setLoadingMetrics(true)
    try {
      const res = await api.supplier2Metrics({ begin_time: beginTime.trim(), end_time: endTime.trim() })
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
      setLoadingMetrics(false)
    }
  }

  async function refreshAll(s: Supplier2Settings | null) {
    if (!s?.web_configured) return
    await loadAccounts()
    if (s.openapi_configured) await loadMetrics()
  }

  useEffect(() => {
    void (async () => {
      const s = await loadSettings()
      if (!s?.web_configured) setShowSettings(true)
      await refreshAll(s)
    })()
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
      await refreshAll(s)
    } catch (e: any) {
      setSettingsErr(e?.message || String(e))
    } finally {
      setSaving(false)
    }
  }

  const configured = settings?.web_configured === true
  const metricRows = metrics?.accounts || []
  const aliasById = new Map(accounts.map(a => [a.id, a]))
  const total = metrics?.summary?.total

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
        <>
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
                onClick={() => void loadMetrics()}
                disabled={loadingMetrics || !settings?.openapi_configured}
                className="px-3 py-1.5 text-sm rounded-md bg-gray-900 text-white disabled:opacity-50"
              >
                {loadingMetrics ? '查询中…' : '查询用量'}
              </button>
              <span className="text-[11px] text-gray-500">超过 7 天会分段查询；结束时间不晚于当前。</span>
            </div>
            {metricsErr && (
              <div className="mb-3 bg-rose-50 border border-rose-200 text-rose-700 text-sm rounded-md px-3 py-2">{metricsErr}</div>
            )}
            {total && (
              <div className="mb-3 grid grid-cols-2 sm:grid-cols-5 gap-2 text-sm">
                {[
                  ['账号', fmtNum(total.accounts)],
                  ['请求数', fmtNum(total.requests)],
                  ['Prompt', fmtNum(total.prompt_tokens)],
                  ['Completion', fmtNum(total.completion_tokens)],
                  ['成本', fmtCost(metrics?.total_cost_usd, total.cost)],
                ].map(([k, v]) => (
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
                    <th className="py-1.5 pr-3 mono-label text-right">成本($ · ¥)</th>
                    <th className="py-1.5 pr-3 mono-label text-right">成功率</th>
                  </tr>
                </thead>
                <tbody>
                  {metricRows.length === 0 ? (
                    <tr>
                      <td colSpan={7} className="py-3 text-center text-gray-400">
                        {metrics ? '该时间范围内暂无用量' : '尚未查询'}
                      </td>
                    </tr>
                  ) : (
                    metricRows.map(m => (
                      <tr key={m.aid} className="border-b border-gray-100">
                        <td className="py-1.5 pr-3">{aliasById.get(m.aid)?.account_alias || m.account_alias || `#${m.aid}`}</td>
                        <td className="py-1.5 pr-3">{aliasById.get(m.aid)?.provider || '—'}</td>
                        <td className="py-1.5 pr-3 text-right tabular-nums">{fmtNum(m.requests)}</td>
                        <td className="py-1.5 pr-3 text-right tabular-nums">{fmtNum(m.prompt_tokens)}</td>
                        <td className="py-1.5 pr-3 text-right tabular-nums">{fmtNum(m.completion_tokens)}</td>
                        <td className="py-1.5 pr-3 text-right tabular-nums">{fmtCost(m.cost_usd, m.cost)}</td>
                        <td className="py-1.5 pr-3 text-right tabular-nums">{fmtRate(m.success_rate)}</td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
            {metrics?.fx_rate != null && (
              <p className="mt-2 text-[11px] text-gray-500">成本来自号池账单（约 2 小时延迟，按天）；USD 按汇率 {metrics.fx_rate} 折算。</p>
            )}
          </section>

          <section className="mb-5 bg-white border border-gray-200 rounded-lg p-4 sm:p-5">
            <div className="flex items-center justify-between mb-3">
              <h3 className="text-sm font-semibold">账号列表（{accounts.length}）</h3>
              <button
                onClick={() => void loadAccounts()}
                disabled={loadingAccounts}
                className="px-3 py-1.5 text-xs rounded-md border border-gray-300 disabled:opacity-50"
              >
                {loadingAccounts ? '加载中…' : '刷新'}
              </button>
            </div>
            {accountsErr && (
              <div className="mb-3 bg-rose-50 border border-rose-200 text-rose-700 text-sm rounded-md px-3 py-2">{accountsErr}</div>
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
                    <th className="py-1.5 pr-3 mono-label">创建时间</th>
                  </tr>
                </thead>
                <tbody>
                  {accounts.length === 0 ? (
                    <tr>
                      <td colSpan={9} className="py-3 text-center text-gray-400">{loadingAccounts ? '加载中…' : '暂无账号'}</td>
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
                        <td className="py-1.5 pr-3 whitespace-nowrap">{a.created_at || '—'}</td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          </section>
        </>
      )}
    </div>
  )
}
