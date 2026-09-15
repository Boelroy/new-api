import { useEffect, useRef, useState } from 'react'
import { PanelLeft, Moon, Sun, LogOut, User } from 'lucide-react'
import { api } from '../api'
import { APP_BASE } from '../basePath'
import { useTheme } from '../context/theme-provider'
import { cx } from './ui'

type Props = {
  onToggle: () => void
}

// Full-width transparent top bar, matching the new-api console shell: sidebar
// toggle + brand pill on the left; theme toggle + profile dropdown on the right.
export default function Header(props: Props) {
  const { theme, toggle } = useTheme()

  return (
    <header className="sticky top-0 z-30 flex h-12 w-full shrink-0 items-center gap-2 bg-transparent px-2 sm:px-3">
      <button
        onClick={props.onToggle}
        className="inline-flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
        aria-label="切换侧边栏"
        title="切换侧边栏 (⌘B)"
      >
        <PanelLeft className="size-4" />
      </button>

      <a href={`${APP_BASE}/`} className="inline-flex h-7 min-w-0 items-center rounded-md px-1.5 text-sm font-bold tracking-[0.08em] text-foreground hover:bg-accent">
        <span className="max-w-[14rem] truncate">AI Gateway</span>
      </a>

      <div className="ms-auto flex shrink-0 items-center gap-1 sm:gap-2">
        <button
          onClick={toggle}
          className="inline-flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
          aria-label={theme === 'dark' ? '切换到浅色' : '切换到深色'}
          title={theme === 'dark' ? '浅色模式' : '深色模式'}
        >
          {theme === 'dark' ? <Sun className="size-4" /> : <Moon className="size-4" />}
        </button>
        <ProfileMenu />
      </div>
    </header>
  )
}

function ProfileMenu() {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const handleLogout = async () => {
    try {
      await api.logout()
    } finally {
      window.location.href = `${APP_BASE}/login`
    }
  }

  return (
    <div ref={ref} className="relative">
      <button
        onClick={() => setOpen((o) => !o)}
        className={cx(
          'inline-flex size-8 items-center justify-center rounded-full ring-1 ring-border transition-colors',
          open ? 'bg-muted text-foreground' : 'bg-card text-muted-foreground hover:bg-muted hover:text-foreground',
        )}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="账户菜单"
      >
        <User className="size-4" />
      </button>

      {open && (
        <div
          role="menu"
          className="animate-fadeUp absolute right-0 mt-1.5 w-44 overflow-hidden rounded-lg bg-popover p-1 text-popover-foreground shadow-lg ring-1 ring-foreground/10"
        >
          <button
            role="menuitem"
            onClick={handleLogout}
            className="flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-sm text-foreground hover:bg-muted"
          >
            <LogOut className="size-4 text-muted-foreground" />
            退出登录
          </button>
        </div>
      )}
    </div>
  )
}
