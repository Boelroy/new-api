import { NavLink } from 'react-router-dom'
import { api } from '../api'

type Item = {
  to: string
  label: string
  icon: JSX.Element
  end?: boolean
}

const NAV_ITEMS: Item[] = [
  {
    to: '/',
    end: true,
    label: 'Usage Report',
    icon: (
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M3 3v18h18" />
        <path d="M7 14l4-4 4 4 6-6" />
      </svg>
    ),
  },
  {
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
  },
  {
    to: '/allkeys',
    label: 'All Keys',
    icon: (
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <rect x="3" y="4" width="18" height="4" rx="1" />
        <rect x="3" y="10" width="18" height="4" rx="1" />
        <rect x="3" y="16" width="18" height="4" rx="1" />
      </svg>
    ),
  },
  {
    to: '/tester',
    label: 'Key Tester',
    icon: (
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M9 12l2 2 4-4" />
        <circle cx="12" cy="12" r="9" />
      </svg>
    ),
  },
]

export default function Sidebar() {
  const handleLogout = async () => {
    await api.logout()
    window.location.href = '/login'
  }

  return (
    <aside className="fixed inset-y-0 left-0 w-56 bg-white border-r border-gray-200 flex flex-col">
      <div className="px-5 pt-6 pb-5">
        <div className="flex items-center gap-2">
          <div className="w-8 h-8 rounded-lg bg-gray-900 text-white flex items-center justify-center text-sm font-semibold tracking-tight">R</div>
          <div>
            <div className="text-sm font-semibold tracking-tight text-gray-900 leading-none">Report Service</div>
            <div className="text-[10px] text-gray-400 mt-1 uppercase tracking-wider">Admin</div>
          </div>
        </div>
      </div>

      <nav className="flex-1 px-3 space-y-0.5">
        <div className="px-2 pt-2 pb-1 text-[10px] uppercase tracking-wider text-gray-400 font-medium">Overview</div>
        {NAV_ITEMS.map(item => (
          <NavLink
            key={item.to}
            to={item.to}
            end={item.end}
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
  )
}
