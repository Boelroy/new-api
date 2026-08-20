import { useEffect, useState } from 'react'
import { NavLink } from 'react-router-dom'
import { api, ROLE_ADMIN, ROLE_PROJECT_ADMIN, ROLE_REMOTE_STUDIO_OPERATOR, ROLE_STUDIO_OPERATOR, ROLE_SUPER_ADMIN, ROLE_SUPPLIER_01, ROLE_TESTER } from '../api'
import { withBase } from '../basePath'

export type Item = {
  to: string
  label: string
  icon: JSX.Element
  end?: boolean
}

// Route key of the super-admin sidebar-settings page. It is appended to the
// nav for super admins and is intentionally NOT part of the toggleable
// catalog, so a super admin can never hide the page that unhides things.
export const SIDEBAR_SETTINGS_KEY = '/sidebar-settings'

const USAGE_REPORT_ITEM: Item = {
  to: '/',
  end: true,
  label: 'Usage Report',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 3v18h18" />
      <path d="M7 14l4-4 4 4 6-6" />
    </svg>
  ),
}

const KEY_CAPACITY_ITEM: Item = {
  to: '/keys',
  label: 'Key Capacity',
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
  to: '/allkeys',
  label: 'All Keys',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect x="3" y="4" width="18" height="4" rx="1" />
      <rect x="3" y="10" width="18" height="4" rx="1" />
      <rect x="3" y="16" width="18" height="4" rx="1" />
    </svg>
  ),
}

const KEY_TESTER_ITEM: Item = {
  to: '/tester',
  label: 'Key Tester',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M9 12l2 2 4-4" />
      <circle cx="12" cy="12" r="9" />
    </svg>
  ),
}

const CACHE_REPORT_ITEM: Item = {
  to: '/cache',
  label: 'Cache Report',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <ellipse cx="12" cy="5" rx="9" ry="3" />
      <path d="M3 5v14a9 3 0 0 0 18 0V5" />
      <path d="M3 12a9 3 0 0 0 18 0" />
    </svg>
  ),
}

const ERROR_CENTER_ITEM: Item = {
  to: '/errors',
  label: 'Error Center',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z" />
      <line x1="12" y1="9" x2="12" y2="13" />
      <line x1="12" y1="17" x2="12.01" y2="17" />
    </svg>
  ),
}

const MODEL_HEALTH_ITEM: Item = {
  to: '/model-health',
  label: 'Model Health',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 12h4l2 6 4-14 2 8h6" />
    </svg>
  ),
}

// Visible to all admins (role >= 10). Super-admin-only items are appended
// below in the render path when the user's role and config flags allow.
const ADMIN_NAV_ITEMS: Item[] = [
  USAGE_REPORT_ITEM,
  KEY_CAPACITY_ITEM,
  ALL_KEYS_ITEM,
  KEY_TESTER_ITEM,
  MODEL_HEALTH_ITEM,
  CACHE_REPORT_ITEM,
  ERROR_CENTER_ITEM,
]

const USERS_ITEM: Item = {
  to: '/users',
  label: 'Users',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
      <circle cx="9" cy="7" r="4" />
      <path d="M22 21v-2a4 4 0 0 0-3-3.87" />
      <path d="M16 3.13a4 4 0 0 1 0 7.75" />
    </svg>
  ),
}

// Studio operator's dedicated slim page for uploading keys into the
// local pool. Renders LocalPoolPanel with lockedStudio wired from JWT.
// Kept out of the admin sidebar since admin+/super_admin already have
// the Pool tab inside Key Capacity.
const POOL_UPLOAD_ITEM: Item = {
  to: '/pool-upload',
  label: '上 5刀 Key',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v10" />
      <path d="M9.5 10a2.5 2.5 0 0 1 5 0c0 1-1 1.5-2.5 2s-2.5 1-2.5 2a2.5 2.5 0 0 0 5 0" />
    </svg>
  ),
}

// Supplier Account portal: admin+ (see all) and supplier_01 (own only).
// Shown only when the server reports supplier_account_enabled=true.
const SUPPLIER_ACCOUNTS_ITEM: Item = {
  to: '/supplier-accounts',
  label: '账号上号',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect x="3" y="5" width="18" height="14" rx="2" />
      <path d="M3 10h18" />
      <path d="M7 15h4" />
    </svg>
  ),
}

// Remote New-API inspector: super admin only. Lives right after the
// admin nav items in the super-admin render path below.
const REMOTE_CHANNELS_ITEM: Item = {
  to: '/remote-channels',
  label: 'Remote Channels',
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

// Local Channel Sync: admin+ only. Sits right after Remote Channels in the
// admin render path below.
const LOCAL_SYNC_ITEM: Item = {
  to: '/local-sync',
  label: 'Local Sync',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M21 2v6h-6" />
      <path d="M3 12a9 9 0 0 1 15-6.7L21 8" />
      <path d="M3 22v-6h6" />
      <path d="M21 12a9 9 0 0 1-15 6.7L3 16" />
    </svg>
  ),
}

// Shown only when the server reports r2_configured=true, i.e. R2 is wired up
// so trace + report artifacts can actually persist.
const TESTING_ITEM: Item = {
  to: '/testing',
  label: 'Provider Testing',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="11" cy="11" r="7" />
      <line x1="21" y1="21" x2="16.65" y2="16.65" />
      <path d="M8 11l2 2 4-4" />
    </svg>
  ),
}

// Shown only when the server reports profit_gate_required=false, i.e. the
// /profit page is directly accessible on this deployment.
const PROFIT_ITEM: Item = {
  to: '/profit',
  label: 'Profit Report',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <line x1="12" y1="2" x2="12" y2="22" />
      <path d="M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6" />
    </svg>
  ),
}

// Super-admin-only: configure which sidebar items each role sees on this
// deployment. Appended in the render path, never in baselineItemsFor.
const SIDEBAR_SETTINGS_ITEM: Item = {
  to: SIDEBAR_SETTINGS_KEY,
  label: '侧边栏设置',
  icon: (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <line x1="4" y1="21" x2="4" y2="14" />
      <line x1="4" y1="10" x2="4" y2="3" />
      <line x1="12" y1="21" x2="12" y2="12" />
      <line x1="12" y1="8" x2="12" y2="3" />
      <line x1="20" y1="21" x2="20" y2="16" />
      <line x1="20" y1="12" x2="20" y2="3" />
      <line x1="1" y1="14" x2="7" y2="14" />
      <line x1="9" y1="8" x2="15" y2="8" />
      <line x1="17" y1="16" x2="23" y2="16" />
    </svg>
  ),
}

// SidebarFlags gates items that only exist when this deployment wired up the
// corresponding backend feature.
export type SidebarFlags = { showProfit: boolean; showTesting: boolean; showSupplier: boolean }

// baselineItemsFor returns the items a role sees BEFORE per-role visibility
// overrides are applied. This is the single source of truth shared by the
// live Sidebar and the SidebarSettings matrix. It excludes SIDEBAR_SETTINGS_ITEM.
export function baselineItemsFor(role: number, flags: SidebarFlags): Item[] {
  const { showProfit, showTesting, showSupplier } = flags
  if (role >= ROLE_ADMIN) {
    let items = ADMIN_NAV_ITEMS.slice()
    // Users + Remote Channels are available to admin+ (Remote Channels
    // in a read-only-for-profile mode; profile CRUD is still super
    // admin only). Super admin gets extras: Profit, Provider Testing.
    items = [...items, REMOTE_CHANNELS_ITEM, LOCAL_SYNC_ITEM, USERS_ITEM]
    if (showSupplier) items = [...items, SUPPLIER_ACCOUNTS_ITEM]
    if (role >= ROLE_SUPER_ADMIN) {
      if (showProfit) items = [items[0], PROFIT_ITEM, ...items.slice(1)]
      if (showTesting) items = [...items, TESTING_ITEM]
    }
    return items
  }
  if (role === ROLE_SUPPLIER_01) {
    // Supplier: only the account portal upload/usage page.
    return [SUPPLIER_ACCOUNTS_ITEM]
  }
  if (role === ROLE_TESTER) {
    // Tester is scoped to Key Tester (always) and Provider Testing
    // (only when R2 is wired up, since Provider Testing needs it).
    return showTesting ? [KEY_TESTER_ITEM, TESTING_ITEM] : [KEY_TESTER_ITEM]
  }
  if (role === ROLE_PROJECT_ADMIN) {
    // Project admin sees Key Capacity + Key Tester only.
    return [KEY_CAPACITY_ITEM, KEY_TESTER_ITEM]
  }
  if (role === ROLE_STUDIO_OPERATOR) {
    // Studio operator: All Keys + Key Tester + a dedicated Pool upload page.
    return [ALL_KEYS_ITEM, POOL_UPLOAD_ITEM, KEY_TESTER_ITEM]
  }
  if (role === ROLE_REMOTE_STUDIO_OPERATOR) {
    // Remote studio operator: All Keys + Remote Channels slim page + Key Tester.
    return [ALL_KEYS_ITEM, REMOTE_CHANNELS_ITEM, KEY_TESTER_ITEM]
  }
  // Regular users only see All Keys.
  return [ALL_KEYS_ITEM]
}

type Props = {
  open: boolean
  onClose: () => void
}

// Module-level cache so route changes (which remount Layout → Sidebar) reuse
// the already-fetched values instead of flashing an empty nav for the ~50ms
// each /api/auth/me + /api/auth/config round trip takes.
// overrides: map of role value (string) -> hidden nav item keys for THIS
// deployment, configured by a super admin via /sidebar-settings.
type SidebarOverrides = Record<string, string[]>
type SidebarBoot = { role: number; showProfit: boolean; showTesting: boolean; showSupplier: boolean; overrides: SidebarOverrides }
let cachedBoot: SidebarBoot | null = null
let inflightBoot: Promise<SidebarBoot> | null = null

// Drop the boot cache so the next Sidebar mount refetches /api/auth/config.
// Called by SidebarSettings after saving overrides.
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

  // Render nothing until we know the role, to avoid flashing admin items
  // to a regular user. Otherwise compute the role's baseline items, drop the
  // ones a super admin hid for this role on this deployment, then append the
  // (never-hideable) settings entry for super admins.
  let items: Item[]
  if (role === null) {
    items = []
  } else {
    const hidden = new Set(overrides[String(role)] ?? [])
    items = baselineItemsFor(role, { showProfit, showTesting, showSupplier }).filter(it => !hidden.has(it.to))
    if (role >= ROLE_SUPER_ADMIN) {
      items = [...items, SIDEBAR_SETTINGS_ITEM]
    }
  }

  const handleLogout = async () => {
    await api.logout()
    window.location.href = withBase('/login')
  }

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

        <nav className="flex-1 px-3 space-y-0.5 overflow-y-auto">
          <div className="mono-label px-2 pt-2 pb-2 block">Overview</div>
          {items.map(item => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.end}
              onClick={onClose}
              className={({ isActive }) =>
                `flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-sm transition-colors ${
                  isActive
                    ? 'bg-brand-50 text-brand font-medium'
                    : 'text-secondary hover:bg-canvas hover:text-ink'
                }`
              }
            >
              <span className="opacity-90">{item.icon}</span>
              <span className="leading-none">{item.label}</span>
            </NavLink>
          ))}
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
