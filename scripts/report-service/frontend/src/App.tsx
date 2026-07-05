import { useEffect, useState, type ReactElement } from 'react'
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import Login from './pages/Login'
import Report from './pages/Report'
import KeyCapacity from './pages/KeyCapacity'
import AllKeys from './pages/AllKeys'
import KeyTester from './pages/KeyTester'
import ProviderTesting from './pages/ProviderTesting'
import Profit from './pages/Profit'
import Users from './pages/Users'
import CacheReport from './pages/CacheReport'
import RemoteChannels from './pages/RemoteChannels'
import { api, ROLE_ADMIN, ROLE_SUPER_ADMIN, ROLE_TESTER } from './api'

// RoleGate guards a page against unauthorized roles. While the role is being
// fetched it renders null so we don't flash protected content; on denial it
// redirects to a sensible landing page based on the caller's tier.
// Cached role/promise so switching between /keys, /allkeys, /tester… doesn't
// re-fetch auth on every navigation (the Sidebar's cache lives alongside).
let cachedRole: number | null = null
let inflightRole: Promise<number> | null = null

async function loadRole(): Promise<number> {
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

function landingFor(role: number): string {
  if (role >= ROLE_ADMIN) return '/'
  if (role === ROLE_TESTER) return '/testing'
  return '/allkeys'
}

// RoleGate accepts either a numeric tier (`min`) or an arbitrary predicate
// (`allow`). Tester is a horizontal role that doesn't fit tier compare, so
// routes that let tester through pass `allow` instead of `min`.
type GateProps = {
  children: ReactElement
  min?: number
  allow?: (role: number) => boolean
}

function RoleGate({ min, allow, children }: GateProps) {
  const [role, setRole] = useState<number | null>(cachedRole)
  useEffect(() => {
    if (cachedRole !== null) return
    void loadRole().then(setRole)
  }, [])
  if (role === null) return null
  const permitted = allow ? allow(role) : (min !== undefined && role >= min)
  if (!permitted) {
    return <Navigate to={landingFor(role)} replace />
  }
  return children
}

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/" element={<RoleGate min={ROLE_ADMIN}><Report /></RoleGate>} />
        <Route path="/profit" element={<RoleGate min={ROLE_SUPER_ADMIN}><Profit /></RoleGate>} />
        <Route path="/keys" element={<RoleGate min={ROLE_ADMIN}><KeyCapacity /></RoleGate>} />
        <Route path="/allkeys" element={<AllKeys />} />
        <Route path="/tester" element={<RoleGate min={ROLE_ADMIN}><KeyTester /></RoleGate>} />
        <Route path="/cache" element={<RoleGate min={ROLE_ADMIN}><CacheReport /></RoleGate>} />
        <Route path="/testing" element={<RoleGate allow={r => r >= ROLE_SUPER_ADMIN || r === ROLE_TESTER}><ProviderTesting /></RoleGate>} />
        <Route path="/testing/:projectId" element={<RoleGate allow={r => r >= ROLE_SUPER_ADMIN || r === ROLE_TESTER}><ProviderTesting /></RoleGate>} />
        <Route path="/users" element={<RoleGate min={ROLE_SUPER_ADMIN}><Users /></RoleGate>} />
        <Route path="/remote-channels" element={<RoleGate min={ROLE_SUPER_ADMIN}><RemoteChannels /></RoleGate>} />
        <Route path="/detect" element={<Navigate to="/testing" replace />} />
        <Route path="/eval" element={<Navigate to="/testing" replace />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  )
}
