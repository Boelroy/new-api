import { useEffect, useState } from 'react'
import Layout from '../components/Layout'
import { Button, Card, MonoLabel } from '../components/ui'
import { toast } from '../components/feedback'
import {
  baselineItemsFor,
  invalidateSidebarBoot,
  type SidebarFlags,
} from '../components/Sidebar'
import { withBase } from '../basePath'
import { api, ROLE_ADMIN, ROLE_PROJECT_ADMIN, ROLE_REMOTE_STUDIO_OPERATOR, ROLE_STUDIO_OPERATOR, ROLE_SUPER_ADMIN, ROLE_SUPPLIER_01, ROLE_TESTER, ROLE_USER } from '../api'

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

export default function SidebarSettings() {
  const [flags, setFlags] = useState<SidebarFlags>({ showProfit: true, showTesting: true, showSupplier: true })
  // hidden[roleValue] = Set of hidden item keys.
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

  const showAll = (role: number) => {
    setHidden(prev => ({ ...prev, [String(role)]: new Set() }))
  }

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
      // Hard reload so the already-mounted sidebar picks up the new config.
      setTimeout(() => window.location.reload(), 600)
    } catch (err) {
      toast.error(err)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Layout
      title="侧边栏设置"
      subtitle="按角色控制本站点侧边栏显示哪些入口。仅影响当前部署。"
      actions={
        <Button onClick={save} disabled={!loaded || saving} size="sm">
          {saving ? '保存中…' : '保存'}
        </Button>
      }
    >
      {!loaded ? (
        <div className="text-sm text-secondary">加载中…</div>
      ) : (
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
                  <button
                    onClick={() => showAll(value)}
                    className="text-[11px] text-secondary hover:text-brand"
                  >
                    全部显示
                  </button>
                </div>
                <div className="space-y-1">
                  {items.map(it => {
                    const on = isVisible(value, it.to)
                    return (
                      <label
                        key={it.to}
                        className="flex items-center gap-3 rounded-lg px-2 py-1.5 hover:bg-canvas cursor-pointer"
                      >
                        <span className={on ? 'text-ink' : 'text-gray-300'}>{it.icon}</span>
                        <span className={`flex-1 text-sm ${on ? 'text-ink' : 'text-gray-400 line-through'}`}>
                          {it.label}
                        </span>
                        <span className="mono-label">{it.to}</span>
                        <input
                          type="checkbox"
                          checked={on}
                          onChange={() => toggle(value, it.to)}
                          className="h-4 w-4 accent-brand"
                        />
                      </label>
                    )
                  })}
                  {items.length === 0 && (
                    <div className="text-xs text-secondary py-2">该角色无可配置入口。</div>
                  )}
                </div>
              </Card>
            )
          })}
        </div>
      )}
    </Layout>
  )
}
