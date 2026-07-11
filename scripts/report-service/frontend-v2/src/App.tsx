import { Routes, Route, Navigate } from 'react-router-dom';
import { AuthProvider, useAuth } from './auth';
import { I18nProvider } from './i18n';
import Layout from './Layout';
import Login from './pages/Login';
import Roles from './pages/Roles';
import Users from './pages/Users';
import KeysUpload from './pages/KeysUpload';
import KeysPool from './pages/KeysPool';
import KeysActive from './pages/KeysActive';
import Usage from './pages/Usage';
import Profiles from './pages/Profiles';
// (three legacy /v2/usage/{my,studio,all} routes were merged into one
// /v2/usage page with an in-page scope switcher.)
import Settings from './pages/Settings';

function Protected({ children }: { children: JSX.Element }) {
  const { me, loading } = useAuth();
  if (loading) return <div className="p-6 text-slate-400">…</div>;
  if (!me) return <Navigate to="/login" replace />;
  return children;
}

export default function App() {
  return (
    <I18nProvider>
    <AuthProvider>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route
          path="/"
          element={
            <Protected>
              <Layout />
            </Protected>
          }
        >
          <Route index element={<Navigate to="/keys/pool" replace />} />
          <Route path="roles" element={<Roles />} />
          <Route path="users" element={<Users />} />
          <Route path="keys/upload" element={<KeysUpload />} />
          <Route path="keys/pool" element={<KeysPool />} />
          <Route path="keys/active" element={<KeysActive />} />
          <Route path="usage" element={<Usage />} />
          <Route path="usage/my" element={<Navigate to="/usage" replace />} />
          <Route path="usage/studio" element={<Navigate to="/usage" replace />} />
          <Route path="usage/all" element={<Navigate to="/usage" replace />} />
          <Route path="profiles" element={<Profiles />} />
          <Route path="settings" element={<Settings />} />
        </Route>
      </Routes>
    </AuthProvider>
    </I18nProvider>
  );
}
