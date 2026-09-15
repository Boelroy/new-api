import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'

// Minimal light/dark provider replacing next-themes. Persists the choice in
// localStorage and toggles the `.dark` class on <html>, which is the exact
// mechanism new-api's theme.css keys off (`@custom-variant dark`).

type Theme = 'light' | 'dark'
const STORAGE_KEY = 'v3-theme'

type ThemeCtx = { theme: Theme; toggle: () => void; setTheme: (t: Theme) => void }
const Ctx = createContext<ThemeCtx>({ theme: 'light', toggle: () => {}, setTheme: () => {} })

function readInitial(): Theme {
  if (typeof window === 'undefined') return 'light'
  const saved = localStorage.getItem(STORAGE_KEY)
  if (saved === 'light' || saved === 'dark') return saved
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(readInitial)

  useEffect(() => {
    const root = document.documentElement
    root.classList.toggle('dark', theme === 'dark')
    localStorage.setItem(STORAGE_KEY, theme)
  }, [theme])

  const setTheme = useCallback((t: Theme) => setThemeState(t), [])
  const toggle = useCallback(() => setThemeState((t) => (t === 'dark' ? 'light' : 'dark')), [])

  return <Ctx.Provider value={{ theme, toggle, setTheme }}>{children}</Ctx.Provider>
}

export function useTheme(): ThemeCtx {
  return useContext(Ctx)
}
