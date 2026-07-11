import { NavLink, Outlet } from 'react-router-dom';
import { useAuth } from './auth';
import { useI18n } from './i18n';

interface NavItem {
  to: string;
  labelKey: string;
  perm: string | null; // null = always visible
  scope?: string;
}

// Nav items are filtered by permission — items the user can't touch are
// dropped entirely so the sidebar reflects the caller's reach.
const NAV: NavItem[] = [
  { to: '/keys/upload', labelKey: 'nav.keys.upload', perm: 'keys.pool.upload', scope: 'own_studio' },
  { to: '/keys/pool', labelKey: 'nav.keys.pool', perm: 'keys.pool.view', scope: 'own_studio' },
  { to: '/keys/active', labelKey: 'nav.keys.active', perm: 'keys.newapi.view', scope: 'own_studio' },
  { to: '/usage/my', labelKey: 'nav.usage.my', perm: 'usage.view', scope: 'self' },
  { to: '/usage/studio', labelKey: 'nav.usage.studio', perm: 'usage.view', scope: 'own_studio' },
  { to: '/usage/all', labelKey: 'nav.usage.all', perm: 'usage.view', scope: 'any_studio' },
  { to: '/roles', labelKey: 'nav.roles', perm: 'roles.view' },
  { to: '/users', labelKey: 'nav.users', perm: 'users.view' },
  { to: '/profiles', labelKey: 'nav.profiles', perm: 'remote_newapi.profile.manage' },
  { to: '/settings', labelKey: 'nav.settings', perm: 'system.config' },
];

export default function Layout() {
  const { me, hasPerm, logout } = useAuth();
  const { t, lang, setLang } = useI18n();
  return (
    <div className="min-h-screen flex">
      <aside className="w-56 shrink-0 border-r border-slate-700 bg-slate-900/50 flex flex-col">
        <div className="px-4 py-4 border-b border-slate-800 flex items-start justify-between gap-2">
          <div className="min-w-0 flex-1">
            <div className="text-slate-100 font-semibold">{t('app.title')}</div>
            <div className="text-xs text-slate-400 mt-1 truncate">{me?.username}</div>
            {me?.studio ? (
              <div className="text-xs text-slate-500 truncate">
                {t('nav.studioLabel', { name: me.studio })}
              </div>
            ) : null}
          </div>
          <button
            className="text-xs px-1.5 py-0.5 rounded border border-slate-600 text-slate-300 hover:bg-slate-800"
            onClick={() => setLang(lang === 'zh' ? 'en' : 'zh')}
            title="Switch language"
          >
            {lang === 'zh' ? 'EN' : '中'}
          </button>
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
              {t(n.labelKey)}
            </NavLink>
          ))}
        </nav>
        <button onClick={logout} className="m-3 btn btn-danger">
          {t('common.logout')}
        </button>
      </aside>
      <main className="flex-1 overflow-x-hidden">
        <Outlet />
      </main>
    </div>
  );
}
