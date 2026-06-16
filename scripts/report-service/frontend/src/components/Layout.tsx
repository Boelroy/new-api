import { ReactNode } from 'react'
import Sidebar from './Sidebar'

type Props = {
  title: string
  subtitle?: string
  actions?: ReactNode
  children: ReactNode
}

export default function Layout({ title, subtitle, actions, children }: Props) {
  return (
    <div className="min-h-screen bg-gray-50 text-gray-900">
      <Sidebar />
      <main className="pl-56">
        <div className="max-w-[1400px] mx-auto px-8 py-7">
          <header className="flex items-start justify-between gap-4 mb-6">
            <div>
              <h1 className="text-[22px] font-semibold tracking-tight leading-tight">{title}</h1>
              {subtitle && <p className="text-xs text-gray-400 mt-1">{subtitle}</p>}
            </div>
            {actions && <div className="flex items-center gap-2">{actions}</div>}
          </header>
          {children}
        </div>
      </main>
    </div>
  )
}
