import { useEffect, useState, type ReactNode } from 'react'
import { NavLink } from 'react-router-dom'
import { Radio, type LucideIcon } from 'lucide-react'
import { ROLE_ADMIN, ROLE_REMOTE_STUDIO_OPERATOR } from '../api'
import { getCachedRole, loadRole } from '../auth'
import { cx } from './ui'

type NavItem = {
  to: string
  label: string
  icon: LucideIcon
  end?: boolean
  allow: (role: number) => boolean
}
type NavGroup = { title: string; items: NavItem[] }

// First migration wave: only the Remote NewAPI page lives in v3. New groups /
// items slot in here as pages are ported over from the v1 frontend.
const GROUPS: NavGroup[] = [
  {
    title: '渠道',
    items: [
      {
        to: '/remote-channels',
        label: 'NewAPI',
        icon: Radio,
        allow: (r) => r >= ROLE_ADMIN || r === ROLE_REMOTE_STUDIO_OPERATOR,
      },
    ],
  },
]

type Props = {
  collapsed: boolean
  mobileOpen: boolean
  onClose: () => void
}

export default function Sidebar(props: Props) {
  const [role, setRole] = useState<number | null>(getCachedRole())
  useEffect(() => {
    if (role !== null) return
    void loadRole().then(setRole)
  }, [role])

  const visibleGroups: NavGroup[] =
    role === null
      ? []
      : GROUPS.map((g) => ({ ...g, items: g.items.filter((it) => it.allow(role)) })).filter((g) => g.items.length > 0)

  // Desktop: floating card that collapses to an icon rail. Mobile: always the
  // full expanded nav inside an off-canvas drawer.
  return (
    <>
      <aside className={cx('hidden shrink-0 p-2 lg:block', props.collapsed ? 'w-[4.25rem]' : 'w-56')}>
        <SidebarSurface>
          <Nav groups={visibleGroups} collapsed={props.collapsed} onNavigate={() => {}} />
        </SidebarSurface>
      </aside>

      {props.mobileOpen && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="absolute inset-0 bg-black/50" onClick={props.onClose} />
          <div className="absolute left-0 top-0 h-full w-64 p-2">
            <SidebarSurface>
              <Nav groups={visibleGroups} collapsed={false} onNavigate={props.onClose} />
            </SidebarSurface>
          </div>
        </div>
      )}
    </>
  )
}

function SidebarSurface({ children }: { children: ReactNode }) {
  return (
    <div className="h-full rounded-lg bg-sidebar text-sidebar-foreground shadow-sm ring-1 ring-sidebar-border">
      {children}
    </div>
  )
}

function Nav({ groups, collapsed, onNavigate }: { groups: NavGroup[]; collapsed: boolean; onNavigate: () => void }) {
  return (
    <nav className="flex h-full flex-col gap-4 overflow-y-auto p-2">
      {groups.map((g) => (
        <div key={g.title} className="px-0.5">
          {!collapsed && (
            <div className="px-2 pb-1 text-[11px] font-medium uppercase tracking-wider text-muted-foreground/70">
              {g.title}
            </div>
          )}
          <ul className="flex flex-col gap-0.5">
            {g.items.map((it) => (
              <li key={it.to}>
                <NavLink
                  to={it.to}
                  end={it.end}
                  onClick={onNavigate}
                  title={collapsed ? it.label : undefined}
                  className={({ isActive }) =>
                    cx(
                      'flex w-full items-center gap-2 rounded-md p-2 text-sm outline-none transition-colors',
                      collapsed && 'justify-center',
                      isActive
                        ? 'bg-sidebar-accent font-medium text-sidebar-accent-foreground'
                        : 'text-sidebar-foreground hover:bg-muted hover:text-foreground',
                    )
                  }
                >
                  <it.icon className="size-4 shrink-0" />
                  {!collapsed && <span className="min-w-0 flex-1 truncate">{it.label}</span>}
                </NavLink>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </nav>
  )
}
