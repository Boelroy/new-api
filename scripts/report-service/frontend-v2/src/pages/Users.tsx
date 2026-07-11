import { useEffect, useState } from 'react';
import { api, Role, UserRow } from '../api';
import { useAuth } from '../auth';
import { useI18n } from '../i18n';

export default function Users() {
  const { me, hasPerm } = useAuth();
  const { t } = useI18n();
  const canCreate = hasPerm('users.create');
  const canDelete = hasPerm('users.delete');
  const canDisable = hasPerm('users.disable');
  const canReset = hasPerm('users.reset_password');
  const canAssign = hasPerm('users.assign_role');

  const [users, setUsers] = useState<UserRow[]>([]);
  const [roles, setRoles] = useState<Role[]>([]);
  const [err, setErr] = useState('');
  const [creating, setCreating] = useState(false);
  const [assign, setAssign] = useState<UserRow | null>(null);

  const reload = async () => {
    try {
      const u = await api.listUsers();
      setUsers(u.users);
      const r = await api.listRoles();
      setRoles(r.roles);
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    }
  };
  useEffect(() => {
    reload();
  }, []);

  const canTouch = (row: UserRow) => me?.is_super || (me?.max_role_level ?? 0) > row.max_level;

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl text-slate-900 font-semibold">{t('users.title')}</h1>
        {canCreate && <button className="btn btn-primary" onClick={() => setCreating(true)}>{t('users.new')}</button>}
      </div>
      {err && <div className="text-red-600 text-sm">{err}</div>}
      <div className="card overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr>
              <th className="th">{t('users.col.id')}</th>
              <th className="th">{t('users.col.username')}</th>
              <th className="th">{t('users.col.studio')}</th>
              <th className="th">{t('users.col.roles')}</th>
              <th className="th">{t('users.col.level')}</th>
              <th className="th">{t('users.col.status')}</th>
              <th className="th"></th>
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id}>
                <td className="td">{u.id}</td>
                <td className="td">{u.username}</td>
                <td className="td">{u.studio || <span className="text-slate-400">—</span>}</td>
                <td className="td">
                  <div className="flex flex-wrap gap-1">
                    {u.role_names.map((n) => (
                      <span key={n} className="px-1.5 py-0.5 text-xs rounded bg-slate-200">{n}</span>
                    ))}
                  </div>
                </td>
                <td className="td">{u.max_level}</td>
                <td className="td">{u.status === 1 ? <span className="text-green-700">{t('users.status.enabled')}</span> : <span className="text-red-600">{t('users.status.disabled')}</span>}</td>
                <td className="td text-right space-x-2">
                  {canTouch(u) && canAssign && (
                    <button className="btn" onClick={() => setAssign(u)}>{t('users.action.roles')}</button>
                  )}
                  {canTouch(u) && canReset && (
                    <button
                      className="btn"
                      onClick={async () => {
                        const p = prompt(t('users.pwPrompt', { name: u.username }));
                        if (!p) return;
                        try { await api.resetPassword(u.id, p); alert(t('users.pwReset')); } catch (e: any) { alert(e?.message); }
                      }}
                    >
                      {t('users.action.resetPw')}
                    </button>
                  )}
                  {canTouch(u) && canDisable && (u.status === 1 ? (
                    <button className="btn" onClick={async () => { await api.disableUser(u.id); reload(); }}>{t('users.action.disable')}</button>
                  ) : (
                    <button className="btn" onClick={async () => { await api.enableUser(u.id); reload(); }}>{t('users.action.enable')}</button>
                  ))}
                  {canTouch(u) && canDelete && u.id !== me?.user_id && (
                    <button
                      className="btn btn-danger"
                      onClick={async () => {
                        if (!confirm(t('users.deleteConfirm', { name: u.username }))) return;
                        try { await api.deleteUser(u.id); reload(); } catch (e: any) { alert(e?.message); }
                      }}
                    >
                      {t('common.delete')}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {creating && (
        <CreateUserDrawer
          roles={roles}
          onClose={() => setCreating(false)}
          onSaved={() => { setCreating(false); reload(); }}
        />
      )}
      {assign && (
        <AssignRolesDrawer
          user={assign}
          roles={roles}
          onClose={() => setAssign(null)}
          onSaved={() => { setAssign(null); reload(); }}
        />
      )}
    </div>
  );
}

function CreateUserDrawer({ roles, onClose, onSaved }: { roles: Role[]; onClose: () => void; onSaved: () => void }) {
  const { me } = useAuth();
  const { t } = useI18n();
  const [username, setU] = useState('');
  const [password, setP] = useState('');
  const [studio, setS] = useState('');
  const [selected, setSel] = useState<Set<number>>(new Set());
  const [err, setErr] = useState('');
  const [saving, setSav] = useState(false);
  const canGrantRole = (r: Role) => me?.is_super || r.level < (me?.max_role_level ?? 0);
  const submit = async () => {
    setSav(true); setErr('');
    try {
      await api.createUser({ username, password, studio, roles: Array.from(selected) });
      onSaved();
    } catch (e: any) { setErr(e?.message ?? String(e)); } finally { setSav(false); }
  };
  return (
    <Drawer title={t('users.create.title')} onClose={onClose}>
      <div className="space-y-3">
        <Field label={t('users.create.username')}><input className="input" value={username} onChange={(e)=>setU(e.target.value)} /></Field>
        <Field label={t('users.create.password')}><input className="input" type="password" value={password} onChange={(e)=>setP(e.target.value)} /></Field>
        <Field label={t('users.create.studio')}><input className="input" value={studio} onChange={(e)=>setS(e.target.value)} placeholder={t('common.optional')} /></Field>
        <Field label={t('users.create.rolesLabel')}>
          <div className="space-y-1">
            {roles.map((r) => (
              <label key={r.id} className={`flex items-center gap-2 text-sm ${canGrantRole(r) ? '' : 'opacity-40'}`}>
                <input type="checkbox" disabled={!canGrantRole(r)} checked={selected.has(r.id)} onChange={() => {
                  const n = new Set(selected); n.has(r.id) ? n.delete(r.id) : n.add(r.id); setSel(n);
                }} />
                <span>{r.name}</span>
                <span className="text-xs text-slate-400">(level {r.level})</span>
              </label>
            ))}
          </div>
        </Field>
        {err && <div className="text-red-600 text-sm">{err}</div>}
        <div className="flex justify-end gap-2">
          <button className="btn" onClick={onClose}>{t('common.cancel')}</button>
          <button className="btn btn-primary" onClick={submit} disabled={saving}>{saving ? t('common.saving') : t('common.new')}</button>
        </div>
      </div>
    </Drawer>
  );
}

function AssignRolesDrawer({ user, roles, onClose, onSaved }: { user: UserRow; roles: Role[]; onClose: () => void; onSaved: () => void }) {
  const { me } = useAuth();
  const { t } = useI18n();
  const [selected, setSel] = useState<Set<number>>(new Set(user.roles));
  const [err, setErr] = useState('');
  const canGrantRole = (r: Role) => me?.is_super || r.level < (me?.max_role_level ?? 0);
  const submit = async () => {
    setErr('');
    try {
      await api.assignRoles(user.id, Array.from(selected));
      onSaved();
    } catch (e: any) { setErr(e?.message ?? String(e)); }
  };
  return (
    <Drawer title={t('users.rolesDrawer.title', { name: user.username })} onClose={onClose}>
      <div className="space-y-3">
        {roles.map((r) => (
          <label key={r.id} className={`flex items-center gap-2 text-sm ${canGrantRole(r) ? '' : 'opacity-40'}`}>
            <input type="checkbox" disabled={!canGrantRole(r)} checked={selected.has(r.id)} onChange={() => {
              const n = new Set(selected); n.has(r.id) ? n.delete(r.id) : n.add(r.id); setSel(n);
            }} />
            <span>{r.name}</span>
            <span className="text-xs text-slate-400">(level {r.level})</span>
          </label>
        ))}
        {err && <div className="text-red-600 text-sm">{err}</div>}
        <div className="flex justify-end gap-2">
          <button className="btn" onClick={onClose}>{t('common.cancel')}</button>
          <button className="btn btn-primary" onClick={submit}>{t('common.save')}</button>
        </div>
      </div>
    </Drawer>
  );
}

function Drawer({ title, children, onClose }: { title: string; children: React.ReactNode; onClose: () => void }) {
  return (
    <div className="fixed inset-0 bg-slate-900/30 flex justify-end" onClick={onClose}>
      <div className="w-[520px] bg-white border-l border-slate-200 h-full overflow-y-auto p-6" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg text-slate-900 font-semibold">{title}</h2>
          <button className="btn" onClick={onClose}>×</button>
        </div>
        {children}
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="text-xs text-slate-400 block mb-1">{label}</label>
      {children}
    </div>
  );
}
