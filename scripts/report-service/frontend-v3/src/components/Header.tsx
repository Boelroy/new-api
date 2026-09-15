import { Menu, Moon, Sun, LogOut } from 'lucide-react'
import { api } from '../api'
import { useTheme } from '../context/theme-provider'

type Props = {
  onMenu: () => void
}

// Full-width 3rem top bar, matching the new-api console shell: sidebar toggle,
// brand pill on the left; theme toggle + logout on the right.
export default function Header({ onMenu }: Props) {
  const { theme, toggle } = useTheme()

  const handleLogout = async () => {
    try {
      await api.logout()
    } finally {
      window.location.href = '/v3/login'
    }
  }

  return (
    <header className="sticky top-0 z-30 h-12 w-full shrink-0 border-b border-border bg-background">
      <div className="flex h-full items-center gap-2 px-2 sm:px-3">
        <button
          onClick={onMenu}
          className="inline-flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground lg:hidden"
          aria-label="打开菜单"
        >
          <Menu className="size-4" />
        </button>

        <a
          href="/v3/"
          className="inline-flex h-7 min-w-0 items-center gap-1.5 rounded-md px-1.5 text-sm font-medium text-foreground hover:bg-accent"
        >
          <span className="flex size-5 items-center justify-center rounded-md bg-primary text-[11px] font-bold text-primary-foreground">
            N
          </span>
          <span className="max-w-[12rem] truncate">New API</span>
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
          <button
            onClick={handleLogout}
            className="inline-flex h-8 items-center gap-1.5 rounded-md px-2.5 text-sm text-muted-foreground hover:bg-muted hover:text-foreground"
            aria-label="退出登录"
          >
            <LogOut className="size-4" />
            <span className="hidden sm:inline">退出</span>
          </button>
        </div>
      </div>
    </header>
  )
}
