import { useEffect, useState } from 'react'
import Layout from '../components/Layout'
import { Button, Card, MonoLabel } from '../components/ui'
import { toast } from '../components/feedback'
import { baselineItemsFor, invalidateSidebarBoot, type SidebarFlags } from '../components/Sidebar'
import { TZ_OPTIONS, useReportTz } from '../lib/reportTz'
import { withBase } from '../basePath'
import { api, ROLE_ADMIN, ROLE_PROJECT_ADMIN, ROLE_REMOTE_STUDIO_OPERATOR, ROLE_STUDIO_OPERATOR, ROLE_SUPER_ADMIN, ROLE_SUPPLIER_01, ROLE_TESTER, ROLE_USER } from '../api'

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
      <Card className="p-5">
        <MonoLabel>More</MonoLabel>
        <div className="text-sm font-semibold text-ink mt-1 mb-1">其他全站设置</div>
        <p className="text-xs text-secondary">更多站点级配置将陆续加入这里。</p>
      </Card>
    </div>
  )
}

// ── AWS Bedrock tab (default regions + region→prefix map) ───────────────
function AwsSettings() {
  const [regions, setRegions] = useState<string[]>([])
  const [regionInput, setRegionInput] = useState('')
  // Prefix map kept as an editable row list so keys can be typed freely.
  const [rows, setRows] = useState<{ region: string; prefix: string }[]>([])
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
      await api.setAwsConfig({ default_regions: regions.map(r => r.trim()).filter(Boolean), region_prefix_map })
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
