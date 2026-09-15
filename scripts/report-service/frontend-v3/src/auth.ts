import { api } from './api'

// Role cache shared by App's RoleGate and the Sidebar so navigating between
// routes (which remount Layout → Sidebar) doesn't refetch /api/auth/me each
// time. Ported from the v1 frontend's App.tsx.
let cachedRole: number | null = null
let inflightRole: Promise<number> | null = null

export async function loadRole(): Promise<number> {
  if (cachedRole !== null) return cachedRole
  if (inflightRole) return inflightRole
  inflightRole = (async () => {
    try {
      const me = await api.getAuthMe()
      cachedRole = typeof me?.role === 'number' ? me.role : 0
    } catch {
      cachedRole = 0
    }
    return cachedRole
  })().finally(() => {
    inflightRole = null
  })
  return inflightRole
}

export function getCachedRole(): number | null {
  return cachedRole
}
