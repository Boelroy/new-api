import { NavLink, Outlet } from 'react-router-dom';
import { useAuth } from './auth';

interface NavItem {
  to: string;
  label: string;
  perm: string | null; // null = always visible
  scope?: string;
}

// The nav is filtered by permission — items the user can't touch are
// dropped entirely so the sidebar reflects the caller's reach.
const NAV: NavItem[] = [
  { to: '/keys/upload', label: 'Upload Keys', perm: 'keys.pool.upload', scope: 'own_studio' },
  { to: '/keys/pool', label: 'Key Pool', perm: 'keys.pool.view', scope: 'own_studio' },
  { to: '/keys/active', label: 'Active Keys', perm: 'keys.newapi.view', scope: 'own_studio' },
  { to: '/usage/my', label: 'My Usage', perm: 'usage.view', scope: 'self' },
  { to: '/usage/studio', label: 'Studio Usage', perm: 'usage.view', scope: 'own_studio' },
  { to: '/usage/all', label: 'All Usage', perm: 'usage.view', scope: 'any_studio' },
  { to: '/roles', label: 'Roles', perm: 'roles.view' },
  { to: '/users', label: 'Users', perm: 'users.view' },
  { to: '/profiles', label: 'Remote Profiles', perm: 'remote_newapi.profile.manage' },
  { to: '/settings', label: 'Settings', perm: 'system.config' },
];

export default function Layout() {
  const { me, hasPerm, logout } = useAuth();
  return (
    <div className="min-h-screen flex">
      <aside className="w-56 shrink-0 border-r border-slate-700 bg-slate-900/50 flex flex-col">
        <div className="px-4 py-4 border-b border-slate-800">
          <div className="text-slate-100 font-semibold">Report V2</div>
          <div className="text-xs text-slate-400 mt-1">{me?.username}</div>
          {me?.studio ? <div className="text-xs text-slate-500">studio: {me.studio}</div> : null}
        </div>
        <nav className="flex-1 overflow-y-auto py-2">
          {NAV.filter((n) => !n.perm || hasPerm(n.perm, n.scope)).map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              className={({ isActive }) =>
                `block px-4 py-2 text-sm ${isActive ? 'bg-slate-800 text-white' : 'text-slate-300 hover:bg-slate-800/60'}`
              }
            >
              {n.label}
            </NavLink>
          ))}
        </nav>
        <button onClick={logout} className="m-3 btn btn-danger">
          Log out
        </button>
      </aside>
      <main className="flex-1 overflow-x-hidden">
        <Outlet />
      </main>
    </div>
  );
}
