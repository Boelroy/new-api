import { useEffect, useState } from 'react'
import { NavLink } from 'react-router-dom'
import { api, ROLE_ADMIN, ROLE_PROJECT_ADMIN, ROLE_REMOTE_STUDIO_OPERATOR, ROLE_STUDIO_OPERATOR, ROLE_SUPER_ADMIN, ROLE_SUPPLIER_01, ROLE_TESTER } from '../api'
import { withBase } from '../basePath'

// Sidebar groups. Items carry a group; admin+ sees grouped section headers,
// lower-privilege roles get a flat list (no headers) since they see few items.
export type SidebarGroup = 'analytics' | 'key' | 'admin' | 'other'
const GROUP_ORDER: SidebarGroup[] = ['analytics', 'key', 'admin', 'other']
const GROUP_LABELS: Record<SidebarGroup, string> = {
  analytics: 'Analytics',
  key: 'Key',
  admin: 'Admin',
  other: 'Other',
}
// Canonical within-group ordering by route key.
const NAV_ORDER = [
  '/', '/profit', '/billing', '/cache', '/errors', '/model-health',
  '/keys', '/allkeys', '/tester', '/remote-channels', '/pool-upload',
  '/local-sync', '/users', '/settings', '/testing',
  '/supplier-accounts',
]
function navIndex(to: string): number {
  const i = NAV_ORDER.indexOf(to)
  return i === -1 ? NAV_ORDER.length : i
}

// Per-item accent colour for the nav icon. Keeps the sidebar lively without
// touching label colours (which still follow the active/hover state). Values
// are picked to stay distinct within each group.
const NAV_COLORS: Record<string, string> = {
  '/': '#2864FF',            // Usage Report — brand blue
  '/profit': '#16A34A',      // Profit — green
  '/billing': '#7C3AED',     // Billing — violet
  '/cache': '#0EA5E9',       // Cache — sky
  '/errors': '#EF4444',      // Error Center — red
  '/model-health': '#EC4899',// Model Health — pink
  '/keys': '#F59E0B',        // Key Capacity — amber
  '/allkeys': '#0D9488',     // All Keys — teal
  '/tester': '#22C55E',      // Key Tester — green
  '/remote-channels': '#6366F1', // Remote NewAPI — indigo
  '/pool-upload': '#06B6D4', // Pool Upload — cyan
  '/local-sync': '#3B82F6',  // Local Sync — blue
  '/users': '#8B5CF6',       // User — violet
  '/settings': '#64748B',    // Settings — slate
  '/testing': '#14B8A6',     // Provider Testing — teal
  '/supplier-accounts': '#F97316', // Third-party Systems — orange
}
function navColor(to: string): string {
  return NAV_COLORS[to] ?? '#687083'
}

export type Item = {
  to: string
  label: string
  group: SidebarGroup
  icon: JSX.Element
  end?: boolean
}

// Route key of the super-admin Settings page. Appended to the nav for super
// admins and intentionally NOT part of the toggleable catalog, so a super
// admin can never hide the page that unhides things.
export const SETTINGS_KEY = '/settings'

const USAGE_REPORT_ITEM: Item = {
  to: '/', end: true, label: 'Usage Report', group: 'analytics',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 3v18h18" />
      <path d="M7 14l4-4 4 4 6-6" />
    </svg>
  ),
}

const PROFIT_ITEM: Item = {
  to: '/profit', label: 'Profit Report', group: 'analytics',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <line x1="12" y1="2" x2="12" y2="22" />
      <path d="M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6" />
    </svg>
  ),
}

const BILLING_ITEM: Item = {
  to: '/billing', label: 'Billing', group: 'analytics',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M4 2v20l2-1 2 1 2-1 2 1 2-1 2 1 2-1 2 1V2l-2 1-2-1-2 1-2-1-2 1-2-1-2 1Z" />
      <path d="M8 7h8" />
      <path d="M8 11h8" />
      <path d="M8 15h5" />
    </svg>
  ),
}

const CACHE_REPORT_ITEM: Item = {
  to: '/cache', label: 'Cache Report', group: 'analytics',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <ellipse cx="12" cy="5" rx="9" ry="3" />
      <path d="M3 5v14a9 3 0 0 0 18 0V5" />
      <path d="M3 12a9 3 0 0 0 18 0" />
    </svg>
  ),
}

const ERROR_CENTER_ITEM: Item = {
  to: '/errors', label: 'Error Center', group: 'analytics',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z" />
      <line x1="12" y1="9" x2="12" y2="13" />
      <line x1="12" y1="17" x2="12.01" y2="17" />
    </svg>
  ),
}

const MODEL_HEALTH_ITEM: Item = {
  to: '/model-health', label: 'Model Health', group: 'analytics',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 12h4l2 6 4-14 2 8h6" />
    </svg>
  ),
}

const KEY_CAPACITY_ITEM: Item = {
  to: '/keys', label: 'Key Capacity', group: 'key',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="9" cy="12" r="3" />
      <path d="M12 12h10" />
      <path d="M18 12v3" />
      <path d="M22 12v3" />
    </svg>
  ),
}

const ALL_KEYS_ITEM: Item = {
  to: '/allkeys', label: 'All Keys', group: 'key',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect x="3" y="4" width="18" height="4" rx="1" />
      <rect x="3" y="10" width="18" height="4" rx="1" />
      <rect x="3" y="16" width="18" height="4" rx="1" />
    </svg>
  ),
}

const KEY_TESTER_ITEM: Item = {
  to: '/tester', label: 'Key Tester', group: 'key',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M9 12l2 2 4-4" />
      <circle cx="12" cy="12" r="9" />
    </svg>
  ),
}

// Remote New-API inspector.
const REMOTE_CHANNELS_ITEM: Item = {
  to: '/remote-channels', label: 'Remote NewAPI', group: 'key',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M4 6h16" />
      <path d="M4 12h16" />
      <path d="M4 18h16" />
      <circle cx="20" cy="6" r="2" fill="currentColor" />
      <circle cx="4" cy="12" r="2" fill="currentColor" />
      <circle cx="20" cy="18" r="2" fill="currentColor" />
    </svg>
  ),
}

// Studio operator's dedicated slim page for uploading keys into the local pool.
const POOL_UPLOAD_ITEM: Item = {
  to: '/pool-upload', label: 'Pool Upload', group: 'key',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v10" />
      <path d="M9.5 10a2.5 2.5 0 0 1 5 0c0 1-1 1.5-2.5 2s-2.5 1-2.5 2a2.5 2.5 0 0 0 5 0" />
    </svg>
  ),
}

const LOCAL_SYNC_ITEM: Item = {
  to: '/local-sync', label: 'Local Sync', group: 'admin',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M21 2v6h-6" />
      <path d="M3 12a9 9 0 0 1 15-6.7L21 8" />
      <path d="M3 22v-6h6" />
      <path d="M21 12a9 9 0 0 1-15 6.7L3 16" />
    </svg>
  ),
}

const USERS_ITEM: Item = {
  to: '/users', label: 'User', group: 'admin',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
      <circle cx="9" cy="7" r="4" />
      <path d="M22 21v-2a4 4 0 0 0-3-3.87" />
      <path d="M16 3.13a4 4 0 0 1 0 7.75" />
    </svg>
  ),
}

const TESTING_ITEM: Item = {
  to: '/testing', label: 'Provider Testing', group: 'admin',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="11" cy="11" r="7" />
      <line x1="21" y1="21" x2="16.65" y2="16.65" />
      <path d="M8 11l2 2 4-4" />
    </svg>
  ),
}

// Third-party systems portal (账号上号): admin+ (see all) and supplier_01
// (own only). Shown only when supplier_account_enabled=true.
const SUPPLIER_ACCOUNTS_ITEM: Item = {
  to: '/supplier-accounts', label: 'Third-party Systems', group: 'other',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect x="3" y="5" width="18" height="14" rx="2" />
      <path d="M3 10h18" />
      <path d="M7 15h4" />
    </svg>
  ),
}

// Super-admin Settings hub (sidebar visibility + site-wide settings).
// Appended in the render path, never in baselineItemsFor.
const SETTINGS_ITEM: Item = {
  to: SETTINGS_KEY, label: 'Settings', group: 'admin',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z" />
    </svg>
  ),
}

// SidebarFlags gates items that only exist when this deployment wired up the
// corresponding backend feature.
export type SidebarFlags = { showProfit: boolean; showTesting: boolean; showSupplier: boolean }

// baselineItemsFor returns the items a role sees BEFORE per-role visibility
// overrides are applied. Shared by the live Sidebar and the Settings matrix.
// Excludes SETTINGS_ITEM. Order within the returned list is irrelevant — the
// Sidebar regroups/sorts by group + NAV_ORDER.
export function baselineItemsFor(role: number, flags: SidebarFlags): Item[] {
  const { showProfit, showTesting, showSupplier } = flags
  if (role >= ROLE_ADMIN) {
    const items: Item[] = [
      USAGE_REPORT_ITEM, BILLING_ITEM, CACHE_REPORT_ITEM, ERROR_CENTER_ITEM, MODEL_HEALTH_ITEM,
      KEY_CAPACITY_ITEM, ALL_KEYS_ITEM, KEY_TESTER_ITEM, REMOTE_CHANNELS_ITEM,
      LOCAL_SYNC_ITEM, USERS_ITEM,
    ]
    if (showSupplier) items.push(SUPPLIER_ACCOUNTS_ITEM)
    if (role >= ROLE_SUPER_ADMIN) {
      if (showProfit) items.push(PROFIT_ITEM)
      if (showTesting) items.push(TESTING_ITEM)
    }
    return items
  }
  if (role === ROLE_SUPPLIER_01) {
    return [SUPPLIER_ACCOUNTS_ITEM]
  }
  if (role === ROLE_TESTER) {
    return showTesting ? [KEY_TESTER_ITEM, TESTING_ITEM] : [KEY_TESTER_ITEM]
  }
  if (role === ROLE_PROJECT_ADMIN) {
    return [KEY_CAPACITY_ITEM, KEY_TESTER_ITEM]
  }
  if (role === ROLE_STUDIO_OPERATOR) {
    return [ALL_KEYS_ITEM, POOL_UPLOAD_ITEM, KEY_TESTER_ITEM]
  }
  if (role === ROLE_REMOTE_STUDIO_OPERATOR) {
    return [ALL_KEYS_ITEM, REMOTE_CHANNELS_ITEM, KEY_TESTER_ITEM]
  }
  return [ALL_KEYS_ITEM]
}

type Props = {
  open: boolean
  onClose: () => void
}

// Module-level cache so route changes (which remount Layout → Sidebar) reuse
// the already-fetched values instead of flashing an empty nav.
// overrides: role value (string) -> hidden nav item keys for THIS deployment.
type SidebarOverrides = Record<string, string[]>
type SidebarBoot = { role: number; showProfit: boolean; showTesting: boolean; showSupplier: boolean; overrides: SidebarOverrides }
let cachedBoot: SidebarBoot | null = null
let inflightBoot: Promise<SidebarBoot> | null = null

// Drop the boot cache so the next Sidebar mount refetches /api/auth/config.
export function invalidateSidebarBoot() {
  cachedBoot = null
}

async function loadSidebarBoot(): Promise<SidebarBoot> {
  if (cachedBoot) return cachedBoot
  if (inflightBoot) return inflightBoot
  inflightBoot = (async () => {
    const [cfgRes, meRes] = await Promise.allSettled([
      fetch(withBase('/api/auth/config')).then(r => r.json()),
      api.getAuthMe(),
    ])
    const cfg = cfgRes.status === 'fulfilled' ? cfgRes.value : {}
    const me = meRes.status === 'fulfilled' ? meRes.value : { role: 0 }
    const boot: SidebarBoot = {
      role: typeof me?.role === 'number' ? me.role : 0,
      showProfit: cfg?.profit_enabled === true,
      showTesting: cfg?.r2_configured === true,
      showSupplier: cfg?.supplier_account_enabled === true,
      overrides: (cfg?.sidebar_overrides && typeof cfg.sidebar_overrides === 'object') ? cfg.sidebar_overrides as SidebarOverrides : {},
    }
    cachedBoot = boot
    return boot
  })().finally(() => {
    inflightBoot = null
  })
  return inflightBoot
}

export default function Sidebar({ open, onClose }: Props) {
  const [showProfit, setShowProfit] = useState(cachedBoot?.showProfit ?? false)
  const [showTesting, setShowTesting] = useState(cachedBoot?.showTesting ?? false)
  const [showSupplier, setShowSupplier] = useState(cachedBoot?.showSupplier ?? false)
  const [overrides, setOverrides] = useState<SidebarOverrides>(cachedBoot?.overrides ?? {})
  const [role, setRole] = useState<number | null>(cachedBoot ? cachedBoot.role : null)

  useEffect(() => {
    if (cachedBoot) return
    void (async () => {
      const boot = await loadSidebarBoot()
      setRole(boot.role)
      setShowProfit(boot.showProfit)
      setShowTesting(boot.showTesting)
      setShowSupplier(boot.showSupplier)
      setOverrides(boot.overrides)
    })()
  }, [])

  // Compute the role's items: baseline minus per-role hidden keys, plus the
  // (never-hideable) Settings entry for super admins.
  let items: Item[] = []
  if (role !== null) {
    const hidden = new Set(overrides[String(role)] ?? [])
    items = baselineItemsFor(role, { showProfit, showTesting, showSupplier }).filter(it => !hidden.has(it.to))
    if (role >= ROLE_SUPER_ADMIN) items = [...items, SETTINGS_ITEM]
  }
  const grouped = role !== null && role >= ROLE_ADMIN

  const handleLogout = async () => {
    await api.logout()
    window.location.href = withBase('/login')
  }

  const link = (item: Item) => {
    const color = navColor(item.to)
    return (
      <NavLink
        key={item.to}
        to={item.to}
        end={item.end}
        onClick={onClose}
        className={({ isActive }) =>
          `group flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-sm transition-colors ${
            isActive ? 'font-medium text-ink' : 'text-secondary hover:bg-canvas hover:text-ink'
          }`
        }
        style={({ isActive }) => (isActive ? { background: color + '1A' } : undefined)}
      >
        {/* Icon always carries its accent colour so the nav reads as colourful
            regardless of active/hover state. */}
        <span className="shrink-0" style={{ color }}>{item.icon}</span>
        <span className="leading-none">{item.label}</span>
      </NavLink>
    )
  }

  const sections = GROUP_ORDER.map(g => ({
    group: g,
    label: GROUP_LABELS[g],
    items: items.filter(i => i.group === g).sort((a, b) => navIndex(a.to) - navIndex(b.to)),
  })).filter(s => s.items.length > 0)

  return (
    <>
      {/* Mobile backdrop */}
      <div
        onClick={onClose}
        className={`fixed inset-0 bg-black/30 z-30 transition-opacity lg:hidden ${
          open ? 'opacity-100' : 'opacity-0 pointer-events-none'
        }`}
      />

      <aside
        className={`fixed inset-y-0 left-0 w-60 bg-white border-r border-border z-40 flex flex-col transition-transform lg:translate-x-0 lg:w-56 ${
          open ? 'translate-x-0' : '-translate-x-full'
        }`}
      >
        <div className="px-5 pt-6 pb-5 flex items-center justify-between">
          <div className="flex items-center gap-2.5">
            <div className="w-8 h-8 rounded-md bg-brand text-white flex items-center justify-center text-sm font-bold tracking-tight">R</div>
            <div>
              <div className="text-sm font-bold tracking-tight text-ink leading-none">Report Service</div>
              <div className="mono-label mt-1.5 block">Admin</div>
            </div>
          </div>
          <button
            onClick={onClose}
            className="lg:hidden text-gray-400 hover:text-gray-700"
            aria-label="关闭菜单"
          >
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <line x1="18" y1="6" x2="6" y2="18" />
              <line x1="6" y1="6" x2="18" y2="18" />
            </svg>
          </button>
        </div>

        <nav className="flex-1 px-3 pb-3 space-y-0.5 overflow-y-auto">
          {grouped
            ? sections.map(section => (
                <div key={section.group} className="mb-1">
                  <div className="mono-label px-2 pt-3 pb-2 block">{section.label}</div>
                  {section.items.map(link)}
                </div>
              ))
            : items.map(link)}
        </nav>

        <div className="px-3 pb-5 border-t border-border pt-4">
          <button
            onClick={handleLogout}
            className="w-full flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-sm text-secondary hover:bg-canvas hover:text-ink transition-colors"
          >
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
              <polyline points="16 17 21 12 16 7" />
              <line x1="21" y1="12" x2="9" y2="12" />
            </svg>
            退出登录
          </button>
        </div>
      </aside>
    </>
  )
}
