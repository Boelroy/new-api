import { useState, type ReactNode } from 'react'
import Header from './Header'
import Sidebar from './Sidebar'

type Props = {
  title: string
  subtitle?: string
  actions?: ReactNode
  children: ReactNode
}

// App shell: full-width header on top, then a row of floating sidebar + main
// content. Keeps the same (title, subtitle, actions, children) contract as the
// v1 Layout so ported pages need no changes. The page title lives inside the
// content region (new-api style), not in the global top bar.
export default function Layout({ title, subtitle, actions, children }: Props) {
  const [sidebarOpen, setSidebarOpen] = useState(false)

  return (
    <div className="flex h-svh flex-col bg-background text-foreground">
      <Header onMenu={() => setSidebarOpen(true)} />

      <div className="flex min-h-0 w-full flex-1">
        <Sidebar open={sidebarOpen} onClose={() => setSidebarOpen(false)} />

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
