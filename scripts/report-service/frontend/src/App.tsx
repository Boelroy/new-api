import { useEffect, useState, type ReactElement } from 'react'
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { BASE_PATH } from './basePath'
import Login from './pages/Login'
import Report from './pages/Report'
import Billing from './pages/Billing'
import KeyCapacity from './pages/KeyCapacity'
import AllKeys from './pages/AllKeys'
import KeyTester from './pages/KeyTester'
import ProviderTesting from './pages/ProviderTesting'
import Profit from './pages/Profit'
import Users from './pages/Users'
import CacheReport from './pages/CacheReport'
import ErrorCenter from './pages/ErrorCenter'
import ModelHealth from './pages/ModelHealth'
import RemoteChannels from './pages/RemoteChannels'
import LocalChannelSync from './pages/LocalChannelSync'
import PoolUploadStudio from './pages/PoolUploadStudio'
import SupplierAccounts from './pages/SupplierAccounts'
import Settings from './pages/Settings'
import { api, ROLE_ADMIN, ROLE_PROJECT_ADMIN, ROLE_REMOTE_STUDIO_OPERATOR, ROLE_STUDIO_OPERATOR, ROLE_SUPER_ADMIN, ROLE_SUPPLIER_01, ROLE_TESTER } from './api'
import { Toaster, ConfirmHost } from './components/feedback'

// RoleGate guards a page against unauthorized roles. While the role is being
// fetched it renders null so we don't flash protected content; on denial it
// redirects to a sensible landing page based on the caller's tier.
// Cached role/promise so switching between /keys, /allkeys, /tester… doesn't
// re-fetch auth on every navigation (the Sidebar's cache lives alongside).
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

function landingFor(role: number): string {
  if (role >= ROLE_ADMIN) return '/'
  // Supplier lands on their only surface — the account portal.
  if (role === ROLE_SUPPLIER_01) return '/supplier-accounts'
  // Project admin lands on Key Capacity — it's their primary surface.
  if (role === ROLE_PROJECT_ADMIN) return '/keys'
  // Tester lands on Key Tester because it's always available (Provider
  // Testing only shows when R2 is wired up).
  if (role === ROLE_TESTER) return '/tester'
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
    <BrowserRouter basename={BASE_PATH || undefined}>
      <Toaster />
      <ConfirmHost />
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/" element={<RoleGate min={ROLE_ADMIN}><Report /></RoleGate>} />
        <Route path="/billing" element={<RoleGate min={ROLE_ADMIN}><Billing /></RoleGate>} />
        <Route path="/profit" element={<RoleGate min={ROLE_SUPER_ADMIN}><Profit /></RoleGate>} />
        <Route path="/keys" element={<RoleGate allow={r => r >= ROLE_ADMIN || r === ROLE_PROJECT_ADMIN}><KeyCapacity /></RoleGate>} />
        <Route path="/allkeys" element={<AllKeys />} />
        <Route path="/tester" element={<RoleGate allow={r => r >= ROLE_ADMIN || r === ROLE_TESTER || r === ROLE_STUDIO_OPERATOR || r === ROLE_REMOTE_STUDIO_OPERATOR || r === ROLE_PROJECT_ADMIN}><KeyTester /></RoleGate>} />
        <Route path="/cache" element={<RoleGate min={ROLE_ADMIN}><CacheReport /></RoleGate>} />
        <Route path="/errors" element={<RoleGate min={ROLE_ADMIN}><ErrorCenter /></RoleGate>} />
        <Route path="/model-health" element={<RoleGate min={ROLE_ADMIN}><ModelHealth /></RoleGate>} />
        <Route path="/testing" element={<RoleGate allow={r => r >= ROLE_SUPER_ADMIN || r === ROLE_TESTER}><ProviderTesting /></RoleGate>} />
        <Route path="/testing/:projectId" element={<RoleGate allow={r => r >= ROLE_SUPER_ADMIN || r === ROLE_TESTER}><ProviderTesting /></RoleGate>} />
        <Route path="/users" element={<RoleGate min={ROLE_ADMIN}><Users /></RoleGate>} />
        <Route path="/remote-channels" element={<RoleGate allow={r => r >= ROLE_ADMIN || r === ROLE_REMOTE_STUDIO_OPERATOR}><RemoteChannels /></RoleGate>} />
        <Route path="/local-sync" element={<RoleGate min={ROLE_ADMIN}><LocalChannelSync /></RoleGate>} />
        <Route path="/pool-upload" element={<RoleGate allow={r => r === ROLE_STUDIO_OPERATOR}><PoolUploadStudio /></RoleGate>} />
        <Route path="/supplier-accounts" element={<RoleGate allow={r => r >= ROLE_ADMIN || r === ROLE_SUPPLIER_01}><SupplierAccounts /></RoleGate>} />
        <Route path="/settings" element={<RoleGate min={ROLE_SUPER_ADMIN}><Settings /></RoleGate>} />
        <Route path="/sidebar-settings" element={<Navigate to="/settings" replace />} />
        <Route path="/detect" element={<Navigate to="/testing" replace />} />
        <Route path="/eval" element={<Navigate to="/testing" replace />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  )
}
