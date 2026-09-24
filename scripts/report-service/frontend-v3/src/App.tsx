import { useEffect, useState, type ReactElement } from 'react'
import { Routes, Route, Navigate } from 'react-router-dom'
import Login from './pages/Login'
import RemoteChannels from './pages/RemoteChannels'
import KeyhubUpload from './pages/KeyhubUpload'
import KeyhubUsageOverview from './pages/KeyhubUsageOverview'
import KeyhubUsageLogs from './pages/KeyhubUsageLogs'
import { ROLE_ADMIN, ROLE_REMOTE_STUDIO_OPERATOR, ROLE_SUPPLIER_02 } from './api'
import { getCachedRole, loadRole } from './auth'
import { APP_BASE } from './basePath'
import { Toaster, ConfirmHost, PromptHost } from './components/feedback'

// RoleGate guards a page. While the role is being fetched it renders null so we
// don't flash protected content; on denial it renders a small no-access screen
// (v3 currently exposes a single page, so there's no better landing target).
function RoleGate({ allow, children }: { allow: (role: number) => boolean; children: ReactElement }) {
  const [role, setRole] = useState<number | null>(getCachedRole())
  useEffect(() => {
    if (role !== null) return
    void loadRole().then(setRole)
  }, [role])
  if (role === null) return null
  if (!allow(role)) return <NoAccess />
  return children
}

// DefaultLanding picks a home route from the caller's role so a supplier_02
// (KHub-only) user isn't bounced to the remote-channels NoAccess screen on
// login. Admin+ and remote operators keep landing on remote-channels.
function DefaultLanding() {
  const [role, setRole] = useState<number | null>(getCachedRole())
  useEffect(() => {
    if (role !== null) return
    void loadRole().then(setRole)
  }, [role])
  if (role === null) return null
  if (role === ROLE_SUPPLIER_02 && role < ROLE_ADMIN) {
    return <Navigate to="/keyhub-upload" replace />
  }
  return <Navigate to="/remote-channels" replace />
}

function NoAccess() {
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-3 bg-background px-4 text-center text-foreground">
      <div className="display text-lg">无访问权限</div>
      <p className="text-sm text-muted-foreground">当前账号没有权限访问该页面。</p>
      <a href={`${APP_BASE}/login`} className="text-sm text-primary hover:underline">
        重新登录
      </a>
    </div>
  )
}

export default function App() {
  return (
    <>
      <Toaster />
      <ConfirmHost />
      <PromptHost />
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route
          path="/remote-channels"
          element={
            <RoleGate allow={(r) => r >= ROLE_ADMIN || r === ROLE_REMOTE_STUDIO_OPERATOR}>
              <RemoteChannels />
            </RoleGate>
          }
        />
        <Route
          path="/keyhub-upload"
          element={
            <RoleGate allow={(r) => r >= ROLE_ADMIN || r === ROLE_SUPPLIER_02}>
              <KeyhubUpload />
            </RoleGate>
          }
        />
        <Route
          path="/keyhub-usage"
          element={
            <RoleGate allow={(r) => r >= ROLE_ADMIN || r === ROLE_SUPPLIER_02}>
              <KeyhubUsageOverview />
            </RoleGate>
          }
        />
        <Route
          path="/keyhub-logs"
          element={
            <RoleGate allow={(r) => r >= ROLE_ADMIN || r === ROLE_SUPPLIER_02}>
              <KeyhubUsageLogs />
            </RoleGate>
          }
        />
        <Route path="/" element={<DefaultLanding />} />
        <Route path="*" element={<DefaultLanding />} />
      </Routes>
    </>
  )
}
