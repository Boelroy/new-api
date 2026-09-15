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
  // Predicate deciding whether this role sees the item.
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
        label: 'Remote NewAPI',
        icon: Radio,
        allow: (r) => r >= ROLE_ADMIN || r === ROLE_REMOTE_STUDIO_OPERATOR,
      },
    ],
  },
]

type Props = {
  open: boolean
  onClose: () => void
}

export default function Sidebar({ open, onClose }: Props) {
  const [role, setRole] = useState<number | null>(getCachedRole())
  useEffect(() => {
    if (role !== null) return
    void loadRole().then(setRole)
  }, [role])

  const visibleGroups: NavGroup[] =
    role === null
      ? []
      : GROUPS.map((g) => ({ ...g, items: g.items.filter((it) => it.allow(role)) })).filter((g) => g.items.length > 0)

  const nav = (
    <nav className="flex h-full flex-col gap-4 overflow-y-auto p-2">
      {visibleGroups.map((g) => (
        <div key={g.title} className="px-0.5">
          <div className="px-2 pb-1 text-[11px] font-medium uppercase tracking-wider text-muted-foreground/70">
            {g.title}
          </div>
          <ul className="flex flex-col gap-0.5">
            {g.items.map((it) => (
              <li key={it.to}>
                <NavLink
                  to={it.to}
                  end={it.end}
                  onClick={onClose}
                  className={({ isActive }) =>
                    cx(
                      'flex w-full items-center gap-2 rounded-md p-2 text-sm outline-none transition-colors',
                      isActive
                        ? 'bg-sidebar-accent font-medium text-sidebar-accent-foreground'
                        : 'text-sidebar-foreground hover:bg-muted hover:text-foreground',
                    )
                  }
                >
                  <it.icon className="size-4 shrink-0" />
                  <span className="min-w-0 flex-1 truncate">{it.label}</span>
                </NavLink>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </nav>
  )

  return (
    <>
      {/* Desktop: static floating sidebar. */}
      <aside className="hidden w-52 shrink-0 p-2 lg:block">
        <SidebarSurface>{nav}</SidebarSurface>
      </aside>

      {/* Mobile: off-canvas drawer. */}
      {open && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="absolute inset-0 bg-black/50" onClick={onClose} />
          <div className="absolute left-0 top-0 h-full w-64 p-2">
            <SidebarSurface>{nav}</SidebarSurface>
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
