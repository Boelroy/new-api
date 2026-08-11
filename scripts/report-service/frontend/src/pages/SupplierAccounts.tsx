import { useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import { withBase } from '../basePath'
import {
  api,
  ROLE_ADMIN,
  type SupplierAccount,
  type SupplierMetric,
  type SupplierModel,
  type SupplierProvider,
  type SupplierProviderDefault,
  type SupplierSettings,
} from '../api'

// Supplier Account portal page.
//
//   - supplier_01 (role=4) uploads keys to the third-party account portal and
//     sees ONLY their own accounts + realtime usage (incl. cost).
//   - admin / super_admin see every studio's accounts (+ owner identity)
//     and can upload too.
//
// The supplier/company name embedded in the portal alias is masked server-side
// for everyone; the raw name never reaches this page.
//
// Server enforces the scoping; the `isAdmin` flag here only drives which
// columns are rendered. Only "keyonly"-shape providers are supported for
// upload (the portal currently accepts API-key-only vendors).

const ACCOUNT_TYPE_LABELS: Record<number, string> = { 0: '普通', 1: '速刷号' }

const STATUS_LABELS: Record<string, string> = {
  online: '在线',
  offline: '离线',
  wait_check: '待验证',
  check_fail: '验证失败',
  store: '入库',
  arrearage: '欠费',
}

// Format a Date as the portal's expected "2006-01-02 15:04:05" local string.
function fmtLocal(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function todayStart(): string {
  const d = new Date()
  d.setHours(0, 0, 0, 0)
  return fmtLocal(d)
}

function statusBadge(status: string) {
  const label = STATUS_LABELS[status] || status || '—'
  const tone =
    status === 'online'
      ? 'bg-emerald-50 text-emerald-700 border-emerald-200'
      : status === 'check_fail' || status === 'arrearage'
        ? 'bg-rose-50 text-rose-700 border-rose-200'
        : status === 'wait_check'
          ? 'bg-amber-50 text-amber-700 border-amber-200'
          : 'bg-gray-50 text-gray-600 border-gray-200'
  return <span className={`inline-block px-2 py-0.5 rounded text-[11px] border ${tone}`}>{label}</span>
}

export default function SupplierAccounts() {
  const [isAdmin, setIsAdmin] = useState(false)
  const [openapiReady, setOpenapiReady] = useState(true)
  const [providers, setProviders] = useState<SupplierProvider[]>([])
  const [models, setModels] = useState<SupplierModel[]>([])
  const [bootErr, setBootErr] = useState<string | null>(null)

  // Submit form state.
  const [provider, setProvider] = useState('')
  const [selectedModels, setSelectedModels] = useState<Set<string>>(new Set())
  const [modelSearch, setModelSearch] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [accountType, setAccountType] = useState(0)
  const [remark, setRemark] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [submitMsg, setSubmitMsg] = useState<string | null>(null)
  const [submitErr, setSubmitErr] = useState<string | null>(null)

  // History + metrics.
  const [accounts, setAccounts] = useState<SupplierAccount[]>([])
  const [metrics, setMetrics] = useState<Record<number, SupplierMetric>>({})
  const [beginTime, setBeginTime] = useState(todayStart())
  const [endTime, setEndTime] = useState(fmtLocal(new Date()))
  const [loadingMetrics, setLoadingMetrics] = useState(false)
  const [metricsErr, setMetricsErr] = useState<string | null>(null)

  // Admin settings: OpenAPI token + provider visibility.
  const [settings, setSettings] = useState<SupplierSettings | null>(null)
  const [tokenInput, setTokenInput] = useState('')
  const [visibleSet, setVisibleSet] = useState<Set<string>>(new Set())
  const [savingToken, setSavingToken] = useState(false)
  const [savingVis, setSavingVis] = useState(false)
  const [settingsMsg, setSettingsMsg] = useState<string | null>(null)
  const [settingsErr, setSettingsErr] = useState<string | null>(null)

  // Per-provider defaults: formDefaults prefills the upload form (all users);
  // defaultsDraft is the admin editor's working copy.
  const [formDefaults, setFormDefaults] = useState<Record<string, SupplierProviderDefault>>({})
  const [defaultsDraft, setDefaultsDraft] = useState<Record<string, SupplierProviderDefault>>({})
  const [defProvider, setDefProvider] = useState('')
  const [savingDefaults, setSavingDefaults] = useState(false)

  // Only keyonly providers can be uploaded through this portal.
  const keyonlyProviders = useMemo(
    () => providers.filter(p => p.shape === 'keyonly'),
    [providers],
  )

  const providerModels = useMemo(() => {
    const list = models.filter(m => m.provider === provider)
    const q = modelSearch.trim().toLowerCase()
    if (!q) return list
    return list.filter(m => (m.model_name || m.value || '').toLowerCase().includes(q))
  }, [models, provider, modelSearch])

  const selectedProvider = keyonlyProviders.find(p => p.name === provider)

  async function loadAccounts() {
    try {
      const res = await api.supplierAccounts()
      setAccounts(res.accounts || [])
    } catch (e: any) {
      setMetricsErr(e?.message || String(e))
    }
  }

  useEffect(() => {
    void (async () => {
      let admin = false
      try {
        const me = await api.getAuthMe()
        admin = (me?.role ?? 0) >= ROLE_ADMIN
        setIsAdmin(admin)
      } catch {
        /* role defaults to non-admin */
      }
      try {
        const cfg = await fetch(withBase('/api/auth/config')).then(r => r.json())
        setOpenapiReady(cfg?.supplier_account_openapi_ready === true)
      } catch {
        /* leave optimistic; upload/metrics will surface a 503 if not ready */
      }
      try {
        const d = await api.getSupplierProviderDefaults()
        setFormDefaults(d.defaults || {})
      } catch {
        /* form just won't prefill */
      }
      if (admin) {
        try {
          const s = await api.getSupplierSettings()
          setSettings(s)
          setVisibleSet(new Set(s.visible_providers || []))
          setDefaultsDraft(s.provider_defaults || {})
        } catch {
          /* settings panel just won't prefill */
        }
      }
      try {
        const [prov, mod] = await Promise.all([api.supplierProviders(), api.supplierModels()])
        setProviders(prov.list || [])
        setModels(mod.list || [])
      } catch (e: any) {
        setBootErr(e?.message || String(e))
      }
      await loadAccounts()
      // Populate the realtime metric columns on open instead of forcing the
      // user to hit "刷新实时数据" first.
      await handleRefreshMetrics()
    })()
  }, [])

  // Prefill the model picker + account type from the admin-configured
  // defaults whenever the provider changes (or the defaults finish loading).
  useEffect(() => {
    const d = formDefaults[provider]
    setSelectedModels(new Set(d?.models ?? []))
    setAccountType(d?.account_type ?? 0)
    setModelSearch('')
  }, [provider, formDefaults])

  function toggleModel(value: string) {
    setSelectedModels(prev => {
      const next = new Set(prev)
      if (next.has(value)) next.delete(value)
      else next.add(value)
      return next
    })
  }

  async function handleSubmit() {
    setSubmitMsg(null)
    setSubmitErr(null)
    const modelList = Array.from(selectedModels)
    if (!provider) return setSubmitErr('请选择厂商')
    if (modelList.length === 0) return setSubmitErr('请至少选择一个模型')
    const key = apiKey.trim()
    if (!key) return setSubmitErr('请填写 API Key')
    if (selectedProvider && key.length < selectedProvider.key_min_len) {
      return setSubmitErr(`API Key 长度至少 ${selectedProvider.key_min_len} 位`)
    }
    setSubmitting(true)
    try {
      const res = await api.supplierUploadAccount({
        provider,
        model: modelList.join(','),
        api_key: key,
        account_type: accountType,
        remark: remark.trim() || undefined,
      })
      setSubmitMsg(`${res.msg}（别名：${res.alias}）`)
      setApiKey('')
      setRemark('')
      setSelectedModels(new Set())
      await loadAccounts()
    } catch (e: any) {
      setSubmitErr(e?.message || String(e))
    } finally {
      setSubmitting(false)
    }
  }

  async function handleSaveToken() {
    setSettingsMsg(null)
    setSettingsErr(null)
    setSavingToken(true)
    try {
      const s = await api.setSupplierSettings({ openapi_token: tokenInput.trim() })
      setSettings(s)
      setTokenInput('')
      setOpenapiReady(s.openapi_token_set)
      setSettingsMsg(s.openapi_token_set ? `OpenAPI token 已保存（…${s.openapi_token_last4}）` : 'OpenAPI token 已清空')
    } catch (e: any) {
      setSettingsErr(e?.message || String(e))
    } finally {
      setSavingToken(false)
    }
  }

  function toggleVisible(name: string) {
    setVisibleSet(prev => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  async function handleSaveVisibility() {
    setSettingsMsg(null)
    setSettingsErr(null)
    setSavingVis(true)
    try {
      const s = await api.setSupplierSettings({ visible_providers: Array.from(visibleSet) })
      setSettings(s)
      setVisibleSet(new Set(s.visible_providers || []))
      setSettingsMsg(s.visible_providers.length === 0 ? '已保存：全部厂商对工作室可见' : `已保存：${s.visible_providers.length} 个厂商对工作室可见`)
    } catch (e: any) {
      setSettingsErr(e?.message || String(e))
    } finally {
      setSavingVis(false)
    }
  }

  function toggleDefaultModel(prov: string, model: string) {
    setDefaultsDraft(prev => {
      const cur = prev[prov] ?? { models: [], account_type: 0 }
      const has = cur.models.includes(model)
      const models = has ? cur.models.filter(m => m !== model) : [...cur.models, model]
      return { ...prev, [prov]: { ...cur, models } }
    })
  }

  function setDefaultAccountType(prov: string, at: number) {
    setDefaultsDraft(prev => {
      const cur = prev[prov] ?? { models: [], account_type: 0 }
      return { ...prev, [prov]: { ...cur, account_type: at } }
    })
  }

  async function handleSaveDefaults() {
    setSettingsMsg(null)
    setSettingsErr(null)
    setSavingDefaults(true)
    try {
      const s = await api.setSupplierSettings({ provider_defaults: defaultsDraft })
      setSettings(s)
      setDefaultsDraft(s.provider_defaults || {})
      setFormDefaults(s.provider_defaults || {})
      setSettingsMsg('厂商默认配置已保存')
    } catch (e: any) {
      setSettingsErr(e?.message || String(e))
    } finally {
      setSavingDefaults(false)
    }
  }

  async function handleRefreshMetrics() {
    setMetricsErr(null)
    setLoadingMetrics(true)
    try {
      const res = await api.supplierMetrics({ begin_time: beginTime, end_time: endTime })
      const map: Record<number, SupplierMetric> = {}
      for (const m of res.accounts || []) map[m.aid] = m
      setMetrics(map)
    } catch (e: any) {
      setMetricsErr(e?.message || String(e))
    } finally {
      setLoadingMetrics(false)
    }
  }

  return (
    <Layout
      title="账号上号 / 账号资源录入"
      subtitle={isAdmin ? '管理员视角：可见所有工作室的账号与用量' : '仅展示你上传的账号与实时用量'}
    >
      {bootErr && (
        <div className="mb-4 bg-rose-50 border border-rose-200 text-rose-700 text-sm rounded-md px-3 py-2">
          加载厂商/模型失败：{bootErr}
        </div>
      )}

      {!openapiReady && (
        <div className="mb-4 bg-amber-50 border border-amber-200 text-amber-800 text-sm rounded-md px-3 py-2">
          OpenAPI token 尚未配置（<code>SUPPLIER_ACCOUNT_TOKEN</code>）——厂商/模型可正常浏览，但<b>上号与实时用量暂不可用</b>，配置后即可启用。
        </div>
      )}

      {isAdmin && (
        <section className="mb-5 bg-white border border-gray-200 rounded-lg p-4 sm:p-5">
          <h2 className="text-base font-semibold mb-4">管理员设置</h2>
          {settingsErr && (
            <div className="mb-3 bg-rose-50 border border-rose-200 text-rose-700 text-xs rounded-md px-3 py-2">{settingsErr}</div>
          )}
          {settingsMsg && (
            <div className="mb-3 bg-emerald-50 border border-emerald-200 text-emerald-700 text-xs rounded-md px-3 py-2">{settingsMsg}</div>
          )}

          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            {/* OpenAPI token */}
            <div>
              <div className="flex items-center justify-between mb-1">
                <label className="text-xs font-medium text-gray-600">OpenAPI Token</label>
                <span className="text-[11px] text-gray-400">
                  {settings?.openapi_token_set ? `已配置 …${settings.openapi_token_last4}` : '未配置'}
                </span>
              </div>
              <div className="flex gap-2">
                <input
                  type="password"
                  value={tokenInput}
                  onChange={e => setTokenInput(e.target.value)}
                  placeholder={settings?.openapi_token_set ? '输入新 token 覆盖，留空并保存则清除' : '粘贴管理员签发的 OpenAPI token'}
                  className="flex-1 border border-gray-300 rounded-md px-2.5 py-2 text-sm font-mono"
                />
                <button
                  onClick={handleSaveToken}
                  disabled={savingToken}
                  className="bg-gray-900 hover:bg-gray-800 disabled:opacity-60 text-white text-xs rounded-md px-3 py-2 whitespace-nowrap"
                >
                  {savingToken ? '保存中…' : '保存'}
                </button>
              </div>
              <p className="text-[11px] text-gray-400 mt-1">用于上号与实时用量（/openapi/*）。保存后立即生效,无需重启。</p>
            </div>

            {/* Provider visibility */}
            <div>
              <div className="flex items-center justify-between mb-1">
                <label className="text-xs font-medium text-gray-600">工作室可见厂商（不勾 = 全部可见）</label>
                <button
                  onClick={handleSaveVisibility}
                  disabled={savingVis}
                  className="bg-gray-900 hover:bg-gray-800 disabled:opacity-60 text-white text-xs rounded-md px-3 py-1.5"
                >
                  {savingVis ? '保存中…' : '保存可见性'}
                </button>
              </div>
              <div className="border border-gray-300 rounded-md max-h-40 overflow-y-auto p-1">
                {keyonlyProviders.length === 0 ? (
                  <div className="text-xs text-gray-400 px-2 py-2">厂商加载中…</div>
                ) : (
                  keyonlyProviders.map(p => (
                    <label key={p.name} className="flex items-center gap-2 px-2 py-1.5 rounded hover:bg-gray-50 cursor-pointer text-sm">
                      <input type="checkbox" checked={visibleSet.has(p.name)} onChange={() => toggleVisible(p.name)} />
                      <span>{p.name}</span>
                    </label>
                  ))
                )}
              </div>
            </div>
          </div>

          {/* Per-provider defaults: default models + default account type. */}
          <div className="mt-6 border-t border-gray-100 pt-4">
            <div className="flex items-center justify-between mb-2">
              <label className="text-xs font-medium text-gray-600">厂商默认（默认模型 + 默认账号类型,选厂商后上号表单自动预填）</label>
              <button
                onClick={handleSaveDefaults}
                disabled={savingDefaults}
                className="bg-gray-900 hover:bg-gray-800 disabled:opacity-60 text-white text-xs rounded-md px-3 py-1.5"
              >
                {savingDefaults ? '保存中…' : '保存默认配置'}
              </button>
            </div>
            <select
              value={defProvider}
              onChange={e => setDefProvider(e.target.value)}
              className="w-full sm:w-72 border border-gray-300 rounded-md px-2.5 py-2 text-sm mb-3 bg-white"
            >
              <option value="">选择厂商配置默认…</option>
              {keyonlyProviders.map(p => {
                const n = defaultsDraft[p.name]?.models.length ?? 0
                return <option key={p.name} value={p.name}>{p.name}{n ? ` · 默认 ${n} 模型` : ''}</option>
              })}
            </select>
            {defProvider && (
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div>
                  <div className="text-[11px] text-gray-400 mb-1">默认模型（可多选）</div>
                  <div className="border border-gray-300 rounded-md max-h-44 overflow-y-auto p-1">
                    {models.filter(m => m.provider === defProvider).length === 0 ? (
                      <div className="text-xs text-gray-400 px-2 py-2">该厂商暂无模型</div>
                    ) : (
                      models
                        .filter(m => m.provider === defProvider)
                        .map(m => {
                          const val = m.model_name || m.value
                          const checked = (defaultsDraft[defProvider]?.models ?? []).includes(val)
                          return (
                            <label key={val} className="flex items-center gap-2 px-2 py-1.5 rounded hover:bg-gray-50 cursor-pointer text-sm">
                              <input type="checkbox" checked={checked} onChange={() => toggleDefaultModel(defProvider, val)} />
                              <span>{val}</span>
                            </label>
                          )
                        })
                    )}
                  </div>
                </div>
                <div>
                  <div className="text-[11px] text-gray-400 mb-1">默认账号类型</div>
                  <select
                    value={defaultsDraft[defProvider]?.account_type ?? 0}
                    onChange={e => setDefaultAccountType(defProvider, Number(e.target.value))}
                    className="w-full border border-gray-300 rounded-md px-2.5 py-2 text-sm bg-white"
                  >
                    <option value={0}>普通</option>
                    <option value={1}>速刷号</option>
                  </select>
                  <p className="text-[11px] text-gray-400 mt-1">工作室选择该厂商时,模型与账号类型会按此预填(仍可手动改)。</p>
                </div>
              </div>
            )}
          </div>
        </section>
      )}

      <div className="grid grid-cols-1 lg:grid-cols-[minmax(320px,380px)_1fr] gap-5">
        {/* ---- Submit form ---- */}
        <section className="bg-white border border-gray-200 rounded-lg p-4 sm:p-5 h-fit">
          <h2 className="text-base font-semibold mb-1">提交 API 账号</h2>
          <p className="text-[11px] text-gray-400 mb-4">目前仅支持「仅 API Key」类型厂商；代理统一按代理池处理。</p>

          <label className="block text-xs font-medium text-gray-600 mb-1">厂商 *</label>
          <select
            value={provider}
            onChange={e => setProvider(e.target.value)}
            className="w-full border border-gray-300 rounded-md px-2.5 py-2 text-sm mb-3 bg-white"
          >
            <option value="">请选择厂商</option>
            {keyonlyProviders.map(p => (
              <option key={p.name} value={p.name}>{p.name}</option>
            ))}
          </select>

          <label className="block text-xs font-medium text-gray-600 mb-1">
            模型选择 *（可多选，已选 {selectedModels.size}）
          </label>
          {provider ? (
            <div className="border border-gray-300 rounded-md mb-3">
              <input
                value={modelSearch}
                onChange={e => setModelSearch(e.target.value)}
                placeholder="输入关键字搜索模型"
                className="w-full border-b border-gray-200 px-2.5 py-1.5 text-sm outline-none rounded-t-md"
              />
              <div className="max-h-52 overflow-y-auto p-1">
                {providerModels.length === 0 ? (
                  <div className="text-xs text-gray-400 px-2 py-3">没有匹配的模型</div>
                ) : (
                  providerModels.map(m => (
                    <label key={m.value} className="flex items-center gap-2 px-2 py-1.5 rounded hover:bg-gray-50 cursor-pointer text-sm">
                      <input
                        type="checkbox"
                        checked={selectedModels.has(m.model_name || m.value)}
                        onChange={() => toggleModel(m.model_name || m.value)}
                      />
                      <span>{m.model_name || m.value}</span>
                    </label>
                  ))
                )}
              </div>
            </div>
          ) : (
            <div className="text-xs text-gray-400 border border-dashed border-gray-200 rounded-md px-2.5 py-3 mb-3">
              请先选择厂商
            </div>
          )}

          <label className="block text-xs font-medium text-gray-600 mb-1">API Key *</label>
          <textarea
            value={apiKey}
            onChange={e => setApiKey(e.target.value)}
            placeholder="sk-xxxxxxxxxxxxxxxx"
            rows={2}
            className="w-full border border-gray-300 rounded-md px-2.5 py-2 text-sm mb-3 font-mono"
          />

          <label className="block text-xs font-medium text-gray-600 mb-1">账号类型</label>
          <select
            value={accountType}
            onChange={e => setAccountType(Number(e.target.value))}
            className="w-full border border-gray-300 rounded-md px-2.5 py-2 text-sm mb-3 bg-white"
          >
            <option value={0}>普通</option>
            <option value={1}>速刷号</option>
          </select>

          <label className="block text-xs font-medium text-gray-600 mb-1">备注</label>
          <input
            value={remark}
            onChange={e => setRemark(e.target.value)}
            placeholder="其他补充说明"
            className="w-full border border-gray-300 rounded-md px-2.5 py-2 text-sm mb-4"
          />

          {submitErr && (
            <div className="mb-3 bg-rose-50 border border-rose-200 text-rose-700 text-xs rounded-md px-3 py-2">{submitErr}</div>
          )}
          {submitMsg && (
            <div className="mb-3 bg-emerald-50 border border-emerald-200 text-emerald-700 text-xs rounded-md px-3 py-2">{submitMsg}</div>
          )}

          <button
            onClick={handleSubmit}
            disabled={submitting}
            className="w-full bg-emerald-600 hover:bg-emerald-700 disabled:opacity-60 text-white text-sm font-medium rounded-md px-3 py-2.5 transition-colors"
          >
            {submitting ? '提交中…' : '提交账号'}
          </button>
        </section>

        {/* ---- History + metrics ---- */}
        <section className="bg-white border border-gray-200 rounded-lg p-4 sm:p-5">
          <div className="flex flex-col sm:flex-row sm:items-end sm:justify-between gap-3 mb-4">
            <div>
              <h2 className="text-base font-semibold">历史提交记录 · 实时数据</h2>
              <p className="text-[11px] text-gray-400 mt-0.5">实时数据延迟约 2 分钟，时间跨度最长 7 天。</p>
            </div>
            <div className="flex flex-wrap items-end gap-2">
              <div>
                <label className="block text-[10px] text-gray-400 mb-0.5">开始</label>
                <input value={beginTime} onChange={e => setBeginTime(e.target.value)} className="border border-gray-300 rounded-md px-2 py-1.5 text-xs w-40" />
              </div>
              <div>
                <label className="block text-[10px] text-gray-400 mb-0.5">结束</label>
                <input value={endTime} onChange={e => setEndTime(e.target.value)} className="border border-gray-300 rounded-md px-2 py-1.5 text-xs w-40" />
              </div>
              <button
                onClick={handleRefreshMetrics}
                disabled={loadingMetrics}
                className="bg-gray-900 hover:bg-gray-800 disabled:opacity-60 text-white text-xs rounded-md px-3 py-2 transition-colors"
              >
                {loadingMetrics ? '刷新中…' : '刷新实时数据'}
              </button>
            </div>
          </div>

          {metricsErr && (
            <div className="mb-3 bg-rose-50 border border-rose-200 text-rose-700 text-xs rounded-md px-3 py-2">{metricsErr}</div>
          )}

          {accounts.length === 0 ? (
            <div className="text-sm text-gray-400 border border-dashed border-gray-200 rounded-md px-3 py-10 text-center">
              还没有提交记录，从左侧填写并提交第一条账号吧
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm border-collapse">
                <thead>
                  <tr className="text-left text-[11px] text-gray-400 border-b border-gray-200">
                    <th className="py-2 pr-3 font-medium">别名</th>
                    <th className="py-2 pr-3 font-medium">厂商 / 模型</th>
                    {isAdmin && <th className="py-2 pr-3 font-medium">工作室 / 上传人</th>}
                    <th className="py-2 pr-3 font-medium">类型</th>
                    <th className="py-2 pr-3 font-medium">Key</th>
                    <th className="py-2 pr-3 font-medium">状态</th>
                    <th className="py-2 pr-3 font-medium text-right">请求数</th>
                    <th className="py-2 pr-3 font-medium text-right">Tokens (入/出)</th>
                    <th className="py-2 pr-3 font-medium text-right">成功率</th>
                    <th className="py-2 pr-3 font-medium text-right">成本(¥)</th>
                  </tr>
                </thead>
                <tbody>
                  {accounts.map(a => {
                    const m = metrics[a.remote_account_id]
                    return (
                      <tr key={a.id} className="border-b border-gray-100 hover:bg-gray-50">
                        <td className="py-2 pr-3">
                          <div className="font-medium">{a.alias || `#${a.remote_account_id}`}</div>
                          {a.remark && <div className="text-[10px] text-gray-400">{a.remark}</div>}
                        </td>
                        <td className="py-2 pr-3">
                          <div>{a.provider}</div>
                          <div className="text-[10px] text-gray-400 max-w-[220px] truncate" title={a.models}>{a.models}</div>
                        </td>
                        {isAdmin && (
                          <td className="py-2 pr-3">
                            <div>{a.studio || '—'}</div>
                            <div className="text-[10px] text-gray-400">{a.username || `uid ${a.uploaded_by}`}</div>
                          </td>
                        )}
                        <td className="py-2 pr-3">{ACCOUNT_TYPE_LABELS[a.account_type] ?? a.account_type}</td>
                        <td className="py-2 pr-3 font-mono text-xs text-gray-500">…{a.key_last8}</td>
                        <td className="py-2 pr-3">{m ? statusBadge(m.status) : <span className="text-gray-300">—</span>}</td>
                        <td className="py-2 pr-3 text-right tabular-nums">{m?.requests ?? '—'}</td>
                        <td className="py-2 pr-3 text-right tabular-nums text-xs">
                          {m ? `${m.prompt_tokens ?? 0} / ${m.completion_tokens ?? 0}` : '—'}
                        </td>
                        <td className="py-2 pr-3 text-right tabular-nums">
                          {m?.success_rate != null ? `${m.success_rate}%` : '—'}
                        </td>
                        <td className="py-2 pr-3 text-right tabular-nums">
                          {m?.cost != null ? m.cost.toFixed(2) : '—'}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </section>
      </div>
    </Layout>
  )
}
