import { useEffect, useState } from 'react'
import { NavLink } from 'react-router-dom'
import { api, ROLE_ADMIN, ROLE_SUPER_ADMIN } from '../api'

type Item = {
  to: string
  label: string
  icon: JSX.Element
  end?: boolean
}

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

// Visible to all admins (role >= 10). Super-admin-only items are appended
// below in the render path when the user's role and config flags allow.
const ADMIN_NAV_ITEMS: Item[] = [
  USAGE_REPORT_ITEM,
  KEY_CAPACITY_ITEM,
  ALL_KEYS_ITEM,
  KEY_TESTER_ITEM,
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

type Props = {
  open: boolean
  onClose: () => void
}

export default function Sidebar({ open, onClose }: Props) {
  const [showProfit, setShowProfit] = useState(false)
  const [showTesting, setShowTesting] = useState(false)
  const [role, setRole] = useState<number | null>(null)

  useEffect(() => {
    void (async () => {
      try {
        const cfg = await fetch('/api/auth/config').then(r => r.json())
        if (cfg.profit_enabled === true) setShowProfit(true)
        if (cfg.r2_configured === true) setShowTesting(true)
      } catch { /* keep hidden on error */ }
    })()
    void (async () => {
      try {
        const me = await api.getAuthMe()
        setRole(me.role)
      } catch { setRole(0) }
    })()
  }, [])

  // Render nothing until we know the role, to avoid flashing admin items
  // to a regular user.
  let items: Item[]
  if (role === null) {
    items = []
  } else if (role >= ROLE_ADMIN) {
    items = ADMIN_NAV_ITEMS.slice()
    if (role >= ROLE_SUPER_ADMIN) {
      if (showProfit) items = [items[0], PROFIT_ITEM, ...items.slice(1)]
      if (showTesting) items = [...items, TESTING_ITEM]
      items = [...items, USERS_ITEM]
    }
  } else {
    // Regular users only see All Keys.
    items = [ALL_KEYS_ITEM]
  }

  const handleLogout = async () => {
    await api.logout()
    window.location.href = '/login'
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
        className={`fixed inset-y-0 left-0 w-60 bg-white border-r border-gray-200 z-40 flex flex-col transition-transform lg:translate-x-0 lg:w-56 ${
          open ? 'translate-x-0' : '-translate-x-full'
        }`}
      >
        <div className="px-5 pt-6 pb-5 flex items-center justify-between">
          <div className="flex items-center gap-2">
            <div className="w-8 h-8 rounded-lg bg-gray-900 text-white flex items-center justify-center text-sm font-semibold tracking-tight">R</div>
            <div>
              <div className="text-sm font-semibold tracking-tight text-gray-900 leading-none">Report Service</div>
              <div className="text-[10px] text-gray-400 mt-1 uppercase tracking-wider">Admin</div>
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
          <div className="px-2 pt-2 pb-1 text-[10px] uppercase tracking-wider text-gray-400 font-medium">Overview</div>
          {items.map(item => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.end}
              onClick={onClose}
              className={({ isActive }) =>
                `flex items-center gap-2.5 px-2.5 py-2 rounded-md text-sm transition-colors ${
                  isActive
                    ? 'bg-gray-900 text-white shadow-sm'
                    : 'text-gray-600 hover:bg-gray-100 hover:text-gray-900'
                }`
              }
            >
              <span className="opacity-90">{item.icon}</span>
              <span className="leading-none">{item.label}</span>
            </NavLink>
          ))}
        </nav>

        <div className="px-3 pb-5 border-t border-gray-100 pt-4">
          <button
            onClick={handleLogout}
            className="w-full flex items-center gap-2.5 px-2.5 py-2 rounded-md text-sm text-gray-500 hover:bg-gray-100 hover:text-gray-900 transition-colors"
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
