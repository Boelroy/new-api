import { useEffect, useState } from 'react'
import Layout from '../components/Layout'
import { Button, Card, MonoLabel } from '../components/ui'
import { toast } from '../components/feedback'
import { baselineItemsFor, invalidateSidebarBoot, type SidebarFlags } from '../components/Sidebar'
import { TZ_OPTIONS, useReportTz } from '../lib/reportTz'
import { withBase } from '../basePath'
import { api, type MainServiceTokenStatus, type ProxyReportSettings, ROLE_ADMIN, ROLE_PROJECT_ADMIN, ROLE_REMOTE_STUDIO_OPERATOR, ROLE_STUDIO_OPERATOR, ROLE_SUPER_ADMIN, ROLE_SUPPLIER_01, ROLE_TESTER, ROLE_USER } from '../api'

type Tab = 'general' | 'sidebar' | 'aws'

// Roles that have a configurable sidebar, most-privileged first.
const ROLES: { value: number; label: string }[] = [
  { value: ROLE_SUPER_ADMIN, label: 'Super Admin' },
  { value: ROLE_ADMIN, label: 'Admin' },
  { value: ROLE_PROJECT_ADMIN, label: 'Project Admin' },
  { value: ROLE_TESTER, label: 'Tester' },
  { value: ROLE_STUDIO_OPERATOR, label: 'Studio Operator' },
  { value: ROLE_REMOTE_STUDIO_OPERATOR, label: 'Remote Studio Operator' },
  { value: ROLE_SUPPLIER_01, label: 'Supplier' },
  { value: ROLE_USER, label: 'User' },
]

// ── General settings tab ────────────────────────────────────────────────
function GeneralSettings() {
  const { tz, setTz, loaded } = useReportTz()
  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <Card className="p-5">
        <MonoLabel>Statistics</MonoLabel>
        <div className="text-sm font-semibold text-ink mt-1 mb-1">统计时区</div>
        <p className="text-xs text-secondary mb-3">
          全站统一的统计时区，Usage Report 与 Billing 的按天口径都以此为准。
        </p>
        <select
          value={tz}
          disabled={!loaded}
          onChange={e => setTz(e.target.value)}
          className="border border-border rounded-lg px-3 py-2 text-sm bg-canvas focus:outline-none focus:border-brand focus:ring-2 focus:ring-outline/40 disabled:opacity-60"
        >
          {TZ_OPTIONS.map(z => <option key={z} value={z}>{z}</option>)}
        </select>
      </Card>
      <MainServiceTokenCard />
      <ProxyReportCard />
    </div>
  )
}

// Local new-api admin token used by the schedulers (model cooldown, model
// health, model cleanup). Saved here it overrides MAIN_SERVICE_TOKEN from the
// environment; the server checks it against new-api before storing.
function MainServiceTokenCard() {
  const [st, setSt] = useState<MainServiceTokenStatus | null>(null)
  const [token, setToken] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api.mainServiceToken().then(setSt).catch(e => toast.error(e))
  }, [])

  const submit = async (value: string) => {
    setSaving(true)
    try {
      setSt(await api.setMainServiceToken(value))
      setToken('')
      toast.success(value ? '已验证并保存' : '已清除，回退到环境变量')
    } catch (e) {
      toast.error(e)
    } finally {
      setSaving(false)
    }
  }

  const sourceText = st?.source === 'settings' ? '后台配置' : st?.source === 'env' ? '环境变量 MAIN_SERVICE_TOKEN' : '未配置'
  return (
    <Card className="p-5">
      <MonoLabel>Local new-api</MonoLabel>
      <div className="text-sm font-semibold text-ink mt-1 mb-1">管理令牌</div>
      <p className="text-xs text-secondary mb-3">
        模型限流冷却（本地）、模型健康、模型清理通过它调用本地 new-api 的渠道接口。填 new-api 上用户 {st?.user_id ?? '1'} 的访问令牌（需要渠道读写权限），保存前会先验证。
      </p>
      {st && (
        <div className="text-xs mb-3 space-y-0.5">
          <div>状态：<span className={st.configured ? 'text-emerald-600 font-medium' : 'text-rose-600 font-medium'}>{st.configured ? '已配置' : '未配置'}</span>
            {st.masked && <span className="ml-2 font-mono text-secondary">{st.masked}</span>}
          </div>
          <div className="text-secondary">来源：{sourceText}{!st.url && ' · MAIN_SERVICE_URL 未设置'}</div>
        </div>
      )}
      <div className="flex gap-2">
        <input
          type="password"
          autoComplete="off"
          value={token}
          onChange={e => setToken(e.target.value)}
          placeholder="粘贴新的访问令牌"
          className="flex-1 border border-border rounded-lg px-3 py-2 text-sm bg-canvas focus:outline-none focus:border-brand font-mono"
        />
        <Button size="sm" onClick={() => void submit(token.trim())} disabled={saving || !token.trim()}>{saving ? '验证中…' : '保存'}</Button>
        {st?.source === 'settings' && (
          <Button variant="outline" size="sm" onClick={() => void submit('')} disabled={saving}>清除</Button>
        )}
      </div>
    </Card>
  )
}

// newapi-proxy 用量播报: today's usage of the enabled proxy channels by
// supplier, pushed to Lark by the proxy at the set UTC+8 hours or on demand.
function ProxyReportCard() {
  const [st, setSt] = useState<ProxyReportSettings | null>(null)
  const [webhook, setWebhook] = useState('')
  const [hours, setHours] = useState('')
  const [saving, setSaving] = useState(false)
  const [pushing, setPushing] = useState(false)

  const apply = (next: ProxyReportSettings) => {
    setSt(next)
    setWebhook(next.webhook)
    setHours(next.hours)
  }

  useEffect(() => {
    api.proxyReportSettings().then(apply).catch(e => toast.error(e))
  }, [])

  const save = async () => {
    setSaving(true)
    try {
      apply(await api.setProxyReportSettings({ webhook: webhook.trim(), hours: hours.trim() }))
      toast.success('已保存')
    } catch (e) {
      toast.error(e)
    } finally {
      setSaving(false)
    }
  }

  // The proxy picks the request up within ~15s; poll until its outcome is newer.
  const push = async () => {
    setPushing(true)
    try {
      const { push_at } = await api.pushProxyReport()
      for (let i = 0; i < 40; i++) {
        await new Promise(r => setTimeout(r, 3000))
        const next = await api.proxyReportSettings()
        if (next.last && next.last.at >= push_at) {
          setSt(next)
          if (next.last.ok) toast.success('已推送')
          else toast.error(next.last.error || '推送失败')
          return
        }
      }
      toast.error('newapi-proxy 2 分钟内没有响应，请确认它在运行')
    } catch (e) {
      toast.error(e)
    } finally {
      setPushing(false)
    }
  }

  const dirty = st != null && (webhook.trim() !== st.webhook || hours.trim() !== st.hours)
  const inputCls = 'w-full border border-border rounded-lg px-3 py-2 text-sm bg-canvas focus:outline-none focus:border-brand'
  return (
    <Card className="p-5 lg:col-span-2">
      <MonoLabel>newapi-proxy</MonoLabel>
      <div className="text-sm font-semibold text-ink mt-1 mb-1">渠道用量播报</div>
      <p className="text-xs text-secondary mb-3">
        把通过 newapi-proxy 创建、当前启用的渠道今日（UTC+8 00:00 起）的花费、请求数、RPM/TPM 按供应商汇总推送到 Lark。由 newapi-proxy 负责发送。
      </p>
      <div className="grid grid-cols-1 md:grid-cols-[1fr_12rem] gap-3 mb-3">
        <label className="space-y-1">
          <span className="text-xs text-secondary">Lark Webhook（留空 = 关闭）</span>
          <input value={webhook} onChange={e => setWebhook(e.target.value)} autoComplete="off" placeholder="https://open.larksuite.com/open-apis/bot/v2/hook/..." className={`${inputCls} font-mono`} />
        </label>
        <label className="space-y-1">
          <span className="text-xs text-secondary">定时推送（UTC+8 整点，逗号分隔）</span>
          <input value={hours} onChange={e => setHours(e.target.value)} placeholder="例如 10,22；留空只手动" className={inputCls} />
        </label>
      </div>
      <div className="flex gap-2 mb-3">
        <Button size="sm" onClick={() => void save()} disabled={saving || !dirty}>{saving ? '保存中…' : '保存'}</Button>
        <Button variant="outline" size="sm" onClick={() => void push()} disabled={pushing || dirty || !st?.webhook}>
          {pushing ? '推送中…' : '立即推送'}
        </Button>
        {dirty && <span className="text-xs text-secondary self-center">先保存再推送</span>}
      </div>
      {st?.last && (
        <div className="text-xs space-y-1">
          <div>
            上次推送：{new Date(st.last.at * 1000).toLocaleString()}（{st.last.trigger === 'manual' ? '手动' : '定时'}）
            <span className={st.last.ok ? 'ml-2 text-emerald-600 font-medium' : 'ml-2 text-rose-600 font-medium'}>{st.last.ok ? '成功' : `失败：${st.last.error ?? ''}`}</span>
          </div>
          {st.last.text && <pre className="whitespace-pre-wrap break-words rounded-lg bg-canvas border border-border p-3 max-h-80 overflow-auto">{st.last.text}</pre>}
        </div>
      )}
    </Card>
  )
}

// ── AWS Bedrock tab (default regions + region→prefix map) ───────────────
function AwsSettings() {
  const [regions, setRegions] = useState<string[]>([])
  const [regionInput, setRegionInput] = useState('')
  // Prefix map kept as an editable row list so keys can be typed freely.
  const [rows, setRows] = useState<{ region: string; prefix: string }[]>([])
  const [defaultGroup, setDefaultGroup] = useState('')
  const [defaultModels, setDefaultModels] = useState('')
  const [loaded, setLoaded] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    void (async () => {
      try {
        const cfg = await fetch(withBase('/api/auth/config')).then(r => r.json()).catch(() => ({}))
        const def: string[] = Array.isArray(cfg?.aws_default_regions) ? cfg.aws_default_regions : []
        const map: Record<string, string> = (cfg?.aws_region_prefix_map && typeof cfg.aws_region_prefix_map === 'object') ? cfg.aws_region_prefix_map : {}
        setRegions(def)
        setRows(Object.entries(map).map(([region, prefix]) => ({ region, prefix: String(prefix) })))
        setDefaultGroup(typeof cfg?.aws_default_group === 'string' ? cfg.aws_default_group : '')
        setDefaultModels(typeof cfg?.aws_default_models === 'string' ? cfg.aws_default_models : '')
      } finally {
        setLoaded(true)
      }
    })()
  }, [])

  const addRegion = () => {
    const v = regionInput.trim()
    if (v && !regions.includes(v)) setRegions(prev => [...prev, v])
    setRegionInput('')
  }

  const save = async () => {
    setSaving(true)
    try {
      const region_prefix_map: Record<string, string> = {}
      for (const r of rows) {
        const region = r.region.trim()
        const prefix = r.prefix.trim()
        if (region && prefix) region_prefix_map[region] = prefix
      }
      await api.setAwsConfig({
        default_regions: regions.map(r => r.trim()).filter(Boolean),
        region_prefix_map,
        default_group: defaultGroup.trim(),
        default_models: defaultModels.trim(),
      })
      toast.success('已保存')
    } catch (err) {
      toast.error(err)
    } finally {
      setSaving(false)
    }
  }

  if (!loaded) return <div className="text-sm text-secondary">加载中…</div>

  return (
    <>
      <div className="flex items-center justify-between mb-3">
        <p className="text-xs text-secondary">配置 AWS Bedrock 批量创建的默认区域，以及区域→模型映射前缀。仅影响当前部署。</p>
        <Button onClick={save} disabled={saving} size="sm">{saving ? '保存中…' : '保存'}</Button>
      </div>
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <Card className="p-5">
          <MonoLabel>Default Regions</MonoLabel>
          <div className="text-sm font-semibold text-ink mt-1 mb-1">默认区域</div>
          <p className="text-xs text-secondary mb-3">批量创建 AWS 渠道时预选的区域列表。一个 key 会在每个区域各建一个渠道。</p>
          <div className="flex flex-wrap gap-1.5 mb-2 min-h-[1.5rem]">
            {regions.length === 0 && <span className="text-[11px] text-secondary">未设置</span>}
            {regions.map(r => (
              <span key={r} className="inline-flex items-center gap-1 rounded-full bg-brand-50 text-brand px-2 py-0.5 text-[11px] font-mono">
                {r}
                <button type="button" onClick={() => setRegions(prev => prev.filter(x => x !== r))} className="hover:text-brand-700" title="移除">
                  <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round"><line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" /></svg>
                </button>
              </span>
            ))}
          </div>
          <div className="flex gap-1">
            <input
              value={regionInput}
              onChange={e => setRegionInput(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); addRegion() } }}
              placeholder="回车添加，例 us-east-1"
              className="flex-1 border border-border rounded-lg px-3 py-2 text-sm font-mono bg-canvas focus:outline-none focus:border-brand"
            />
            <Button variant="outline" size="sm" onClick={addRegion}>添加</Button>
          </div>
        </Card>

        <Card className="p-5">
          <MonoLabel>Region → Prefix</MonoLabel>
          <div className="text-sm font-semibold text-ink mt-1 mb-1">区域 → 模型映射前缀</div>
          <p className="text-xs text-secondary mb-3">
            指定某区域使用的跨区域推理前缀（如 <code className="font-mono">us-west-1 → us</code>、<code className="font-mono">ap-west-1 → global</code>）。未配置的区域按前缀自动推导（us/eu/apac）。
          </p>
          <div className="space-y-1.5">
            {rows.map((r, i) => (
              <div key={i} className="flex items-center gap-2">
                <input
                  value={r.region}
                  onChange={e => setRows(prev => prev.map((x, j) => j === i ? { ...x, region: e.target.value } : x))}
                  placeholder="us-west-1"
                  className="flex-1 border border-border rounded-lg px-2.5 py-1.5 text-sm font-mono bg-canvas focus:outline-none focus:border-brand"
                />
                <span className="text-secondary">→</span>
                <input
                  value={r.prefix}
                  onChange={e => setRows(prev => prev.map((x, j) => j === i ? { ...x, prefix: e.target.value } : x))}
                  placeholder="us"
                  className="w-24 border border-border rounded-lg px-2.5 py-1.5 text-sm font-mono bg-canvas focus:outline-none focus:border-brand"
                />
                <button type="button" onClick={() => setRows(prev => prev.filter((_, j) => j !== i))} className="text-secondary hover:text-red-600" title="删除">
                  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" /></svg>
                </button>
              </div>
            ))}
          </div>
          <button
            type="button"
            onClick={() => setRows(prev => [...prev, { region: '', prefix: '' }])}
            className="mt-2 inline-flex items-center gap-1 text-[12px] text-brand hover:text-brand-700"
          >
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round"><line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" /></svg>
            添加映射
          </button>
        </Card>

        <Card className="p-5">
          <MonoLabel>Default Group</MonoLabel>
          <div className="text-sm font-semibold text-ink mt-1 mb-1">默认分组</div>
          <p className="text-xs text-secondary mb-3">批量创建 AWS 渠道时写入 channels."group" 的默认分组（本地与远端上 key 都用它作缺省）。缺省 <code className="font-mono">claude-aws</code>。</p>
          <input
            value={defaultGroup}
            onChange={e => setDefaultGroup(e.target.value)}
            placeholder="claude-aws"
            className="w-full border border-border rounded-lg px-3 py-2 text-sm font-mono bg-canvas focus:outline-none focus:border-brand"
          />
        </Card>

        <Card className="p-5">
          <MonoLabel>Default Models</MonoLabel>
          <div className="text-sm font-semibold text-ink mt-1 mb-1">默认模型（逗号分隔）</div>
          <p className="text-xs text-secondary mb-3">批量创建 AWS 渠道预填的模型列表（与批量创建面板的「AWS 默认模型列表」同一份配置）。</p>
          <textarea
            value={defaultModels}
            onChange={e => setDefaultModels(e.target.value)}
            rows={4}
            placeholder="claude-opus-4-6,claude-sonnet-4-6,claude-sonnet-5,..."
            className="w-full border border-border rounded-lg px-3 py-2 text-[12px] font-mono resize-y bg-canvas focus:outline-none focus:border-brand"
          />
        </Card>
      </div>
    </>
  )
}

// ── Sidebar visibility tab (role × item matrix) ─────────────────────────
function SidebarSettings() {
  const [flags, setFlags] = useState<SidebarFlags>({ showProfit: true, showTesting: true, showSupplier: true })
  const [hidden, setHidden] = useState<Record<string, Set<string>>>({})
  const [loaded, setLoaded] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    void (async () => {
      try {
        const [cfgRaw, sb] = await Promise.all([
          fetch(withBase('/api/auth/config')).then(r => r.json()).catch(() => ({})),
          api.getSidebarConfig(),
        ])
        setFlags({
          showProfit: cfgRaw?.profit_enabled === true,
          showTesting: cfgRaw?.r2_configured === true,
          showSupplier: cfgRaw?.supplier_account_enabled === true,
        })
        const next: Record<string, Set<string>> = {}
        for (const [role, keys] of Object.entries(sb.overrides ?? {})) {
          next[role] = new Set(keys as string[])
        }
        setHidden(next)
      } catch (err) {
        toast.error(err)
      } finally {
        setLoaded(true)
      }
    })()
  }, [])

  const isVisible = (role: number, key: string) => !hidden[String(role)]?.has(key)
  const toggle = (role: number, key: string) => {
    setHidden(prev => {
      const rk = String(role)
      const set = new Set(prev[rk] ?? [])
      if (set.has(key)) set.delete(key)
      else set.add(key)
      return { ...prev, [rk]: set }
    })
  }
  const showAll = (role: number) => setHidden(prev => ({ ...prev, [String(role)]: new Set() }))

  const save = async () => {
    setSaving(true)
    try {
      const overrides: Record<string, string[]> = {}
      for (const { value } of ROLES) {
        const arr = Array.from(hidden[String(value)] ?? [])
        if (arr.length) overrides[String(value)] = arr
      }
      await api.setSidebarConfig(overrides)
      invalidateSidebarBoot()
      toast.success('已保存，正在刷新侧边栏…')
      setTimeout(() => window.location.reload(), 600)
    } catch (err) {
      toast.error(err)
    } finally {
      setSaving(false)
    }
  }

  if (!loaded) return <div className="text-sm text-secondary">加载中…</div>

  return (
    <>
      <div className="flex items-center justify-between mb-3">
        <p className="text-xs text-secondary">按角色控制本站点侧边栏显示哪些入口。仅影响当前部署。</p>
        <Button onClick={save} disabled={saving} size="sm">{saving ? '保存中…' : '保存'}</Button>
      </div>
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        {ROLES.map(({ value, label }) => {
          const items = baselineItemsFor(value, flags)
          return (
            <Card key={value} className="p-5">
              <div className="flex items-center justify-between mb-3">
                <div>
                  <MonoLabel>{`Role ${value}`}</MonoLabel>
                  <div className="text-sm font-semibold text-ink mt-1">{label}</div>
                </div>
                <button onClick={() => showAll(value)} className="text-[11px] text-secondary hover:text-brand">全部显示</button>
              </div>
              <div className="space-y-1">
                {items.map(it => {
                  const on = isVisible(value, it.to)
                  return (
                    <label key={it.to} className="flex items-center gap-3 rounded-lg px-2 py-1.5 hover:bg-canvas cursor-pointer">
                      <span className={on ? 'text-ink' : 'text-gray-300'}>{it.icon}</span>
                      <span className={`flex-1 text-sm ${on ? 'text-ink' : 'text-gray-400 line-through'}`}>{it.label}</span>
                      <span className="mono-label">{it.to}</span>
                      <input type="checkbox" checked={on} onChange={() => toggle(value, it.to)} className="h-4 w-4 accent-brand" />
                    </label>
                  )
                })}
                {items.length === 0 && <div className="text-xs text-secondary py-2">该角色无可配置入口。</div>}
              </div>
            </Card>
          )
        })}
      </div>
    </>
  )
}

export default function Settings() {
  const [tab, setTab] = useState<Tab>('general')
  return (
    <Layout title="Settings" subtitle="站点级设置（仅超级管理员）">
      <div className="flex gap-0 border-b border-border mb-5">
        {(['general', 'aws', 'sidebar'] as Tab[]).map(t => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={`px-4 py-2.5 text-sm border-b-2 -mb-px transition-all ${tab === t ? 'border-brand text-brand font-semibold' : 'border-transparent text-secondary hover:text-ink'}`}
          >
            {t === 'general' ? 'General' : t === 'aws' ? 'AWS' : 'Sidebar'}
          </button>
        ))}
      </div>
      {tab === 'general' ? <GeneralSettings /> : tab === 'aws' ? <AwsSettings /> : <SidebarSettings />}
    </Layout>
  )
}
