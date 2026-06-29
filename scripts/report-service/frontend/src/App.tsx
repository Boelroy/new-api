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
import { api, ROLE_ADMIN, ROLE_SUPER_ADMIN } from './api'

// RoleGate guards a page against unauthorized roles. While the role is being
// fetched it renders null so we don't flash protected content; on denial it
// redirects to a sensible landing page based on the caller's tier.
function RoleGate({ min, children }: { min: number; children: ReactElement }) {
  const [role, setRole] = useState<number | null>(null)
  useEffect(() => {
    void (async () => {
      try {
        const me = await api.getAuthMe()
        setRole(me.role)
      } catch {
        setRole(0)
      }
    })()
  }, [])
  if (role === null) return null
  if (role < min) {
    return <Navigate to={role >= ROLE_ADMIN ? '/' : '/allkeys'} replace />
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
        <Route path="/testing" element={<RoleGate min={ROLE_SUPER_ADMIN}><ProviderTesting /></RoleGate>} />
        <Route path="/testing/:projectId" element={<RoleGate min={ROLE_SUPER_ADMIN}><ProviderTesting /></RoleGate>} />
        <Route path="/users" element={<RoleGate min={ROLE_SUPER_ADMIN}><Users /></RoleGate>} />
        <Route path="/detect" element={<Navigate to="/testing" replace />} />
        <Route path="/eval" element={<Navigate to="/testing" replace />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  )
}
