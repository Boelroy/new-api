import { useCallback, useEffect, useState, type ReactNode } from 'react'
import Header from './Header'
import Sidebar from './Sidebar'

type Props = {
  title: string
  subtitle?: string
  actions?: ReactNode
  children: ReactNode
}

const COLLAPSE_KEY = 'v3-sidebar-collapsed'

// App shell mirroring new-api's default console: full-width transparent header
// on top, then a row of a floating (collapsible-to-icon) sidebar + flush
// content on a bg-background canvas. Keeps the v1 (title, subtitle, actions,
// children) contract so ported pages need no changes.
export default function Layout({ title, subtitle, actions, children }: Props) {
  const [collapsed, setCollapsed] = useState<boolean>(() => localStorage.getItem(COLLAPSE_KEY) === '1')
  const [mobileOpen, setMobileOpen] = useState(false)

  useEffect(() => {
    localStorage.setItem(COLLAPSE_KEY, collapsed ? '1' : '0')
  }, [collapsed])

  // One toggle button in the header: on desktop it collapses the rail, on
  // mobile it opens the off-canvas drawer.
  const toggle = useCallback(() => {
    if (window.matchMedia('(min-width: 1024px)').matches) {
      setCollapsed((c) => !c)
    } else {
      setMobileOpen((o) => !o)
    }
  }, [])

  // ⌘/Ctrl+B toggles the sidebar, matching new-api's shortcut.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === 'b' || e.key === 'B')) {
        e.preventDefault()
        setCollapsed((c) => !c)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div className="flex h-svh flex-col bg-background text-foreground">
      <Header onToggle={toggle} />

      <div className="flex min-h-0 w-full flex-1">
        <Sidebar collapsed={collapsed} mobileOpen={mobileOpen} onClose={() => setMobileOpen(false)} />

        <main className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto max-w-[1400px] px-4 py-5 sm:px-6 sm:py-6 lg:px-8">
            <div className="mb-5 flex flex-col gap-2 sm:flex-row sm:items-center sm:gap-4">
              <div className="min-w-0">
                <h1 className="display text-lg leading-tight sm:text-[22px]">{title}</h1>
                {subtitle && <p className="mt-1 text-xs text-muted-foreground sm:text-[13px]">{subtitle}</p>}
              </div>
              {actions && <div className="flex flex-wrap items-center gap-2 sm:ml-auto">{actions}</div>}
            </div>
            {children}
          </div>
        </main>
      </div>
    </div>
  )
}
