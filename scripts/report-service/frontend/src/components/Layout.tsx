import { ReactNode, useState } from 'react'
import Sidebar from './Sidebar'

type Props = {
  title: string
  subtitle?: string
  actions?: ReactNode
  children: ReactNode
}

export default function Layout({ title, subtitle, actions, children }: Props) {
  const [sidebarOpen, setSidebarOpen] = useState(false)

  return (
    <div className="min-h-screen bg-gray-50 text-gray-900">
      <Sidebar open={sidebarOpen} onClose={() => setSidebarOpen(false)} />

      <main className="lg:pl-56">
        <div className="max-w-[1400px] mx-auto px-4 sm:px-6 lg:px-8 py-5 sm:py-7">
          <header className="flex flex-col sm:flex-row sm:items-start sm:justify-between gap-3 sm:gap-4 mb-5 sm:mb-6">
            <div className="flex items-start gap-2">
              <button
                onClick={() => setSidebarOpen(true)}
                className="lg:hidden mt-0.5 -ml-1 p-1.5 rounded-md text-gray-500 hover:bg-gray-100 hover:text-gray-900"
                aria-label="打开菜单"
              >
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <line x1="3" y1="6" x2="21" y2="6" />
                  <line x1="3" y1="12" x2="21" y2="12" />
                  <line x1="3" y1="18" x2="21" y2="18" />
                </svg>
              </button>
              <div>
                <h1 className="text-lg sm:text-[22px] font-semibold tracking-tight leading-tight">{title}</h1>
                {subtitle && <p className="text-[11px] sm:text-xs text-gray-400 mt-1">{subtitle}</p>}
              </div>
            </div>
            {actions && (
              <div className="flex items-center gap-2 flex-wrap">{actions}</div>
            )}
          </header>
          {children}
        </div>
      </main>
    </div>
  )
}
