import { useEffect, useState } from 'react';
import { api, Role, UserRow } from '../api';
import { useAuth } from '../auth';

export default function Users() {
  const { me, hasPerm } = useAuth();
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
        <h1 className="text-xl text-slate-100 font-semibold">Users</h1>
        {canCreate && <button className="btn btn-primary" onClick={() => setCreating(true)}>+ New user</button>}
      </div>
      {err && <div className="text-red-400 text-sm">{err}</div>}
      <div className="card overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr>
              <th className="th">ID</th>
              <th className="th">Username</th>
              <th className="th">Studio</th>
              <th className="th">Roles</th>
              <th className="th">Level</th>
              <th className="th">Status</th>
              <th className="th"></th>
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id}>
                <td className="td">{u.id}</td>
                <td className="td">{u.username}</td>
                <td className="td">{u.studio || <span className="text-slate-500">—</span>}</td>
                <td className="td">
                  <div className="flex flex-wrap gap-1">
                    {u.role_names.map((n) => (
                      <span key={n} className="px-1.5 py-0.5 text-xs rounded bg-slate-700">{n}</span>
                    ))}
                  </div>
                </td>
                <td className="td">{u.max_level}</td>
                <td className="td">{u.status === 1 ? <span className="text-green-400">enabled</span> : <span className="text-red-400">disabled</span>}</td>
                <td className="td text-right space-x-2">
                  {canTouch(u) && canAssign && (
                    <button className="btn" onClick={() => setAssign(u)}>Roles</button>
                  )}
                  {canTouch(u) && canReset && (
                    <button
                      className="btn"
                      onClick={async () => {
                        const p = prompt(`New password for ${u.username}? (min 6 chars)`);
                        if (!p) return;
                        try { await api.resetPassword(u.id, p); alert('Password reset'); } catch (e: any) { alert(e?.message); }
                      }}
                    >
                      Reset PW
                    </button>
                  )}
                  {canTouch(u) && canDisable && (u.status === 1 ? (
                    <button className="btn" onClick={async () => { await api.disableUser(u.id); reload(); }}>Disable</button>
                  ) : (
                    <button className="btn" onClick={async () => { await api.enableUser(u.id); reload(); }}>Enable</button>
                  ))}
                  {canTouch(u) && canDelete && u.id !== me?.user_id && (
                    <button
                      className="btn btn-danger"
                      onClick={async () => {
                        if (!confirm(`Delete user ${u.username}?`)) return;
                        try { await api.deleteUser(u.id); reload(); } catch (e: any) { alert(e?.message); }
                      }}
                    >
                      Delete
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
    <Drawer title="New user" onClose={onClose}>
      <div className="space-y-3">
        <Field label="Username"><input className="input" value={username} onChange={(e)=>setU(e.target.value)} /></Field>
        <Field label="Password (min 6)"><input className="input" type="password" value={password} onChange={(e)=>setP(e.target.value)} /></Field>
        <Field label="Studio"><input className="input" value={studio} onChange={(e)=>setS(e.target.value)} placeholder="(optional)" /></Field>
        <Field label="Roles">
          <div className="space-y-1">
            {roles.map((r) => (
              <label key={r.id} className={`flex items-center gap-2 text-sm ${canGrantRole(r) ? '' : 'opacity-40'}`}>
                <input type="checkbox" disabled={!canGrantRole(r)} checked={selected.has(r.id)} onChange={() => {
                  const n = new Set(selected); n.has(r.id) ? n.delete(r.id) : n.add(r.id); setSel(n);
                }} />
                <span>{r.name}</span>
                <span className="text-xs text-slate-500">(level {r.level})</span>
              </label>
            ))}
          </div>
        </Field>
        {err && <div className="text-red-400 text-sm">{err}</div>}
        <div className="flex justify-end gap-2">
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" onClick={submit} disabled={saving}>{saving ? 'Saving…' : 'Create'}</button>
        </div>
      </div>
    </Drawer>
  );
}

function AssignRolesDrawer({ user, roles, onClose, onSaved }: { user: UserRow; roles: Role[]; onClose: () => void; onSaved: () => void }) {
  const { me } = useAuth();
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
    <Drawer title={`Roles for ${user.username}`} onClose={onClose}>
      <div className="space-y-3">
        {roles.map((r) => (
          <label key={r.id} className={`flex items-center gap-2 text-sm ${canGrantRole(r) ? '' : 'opacity-40'}`}>
            <input type="checkbox" disabled={!canGrantRole(r)} checked={selected.has(r.id)} onChange={() => {
              const n = new Set(selected); n.has(r.id) ? n.delete(r.id) : n.add(r.id); setSel(n);
            }} />
            <span>{r.name}</span>
            <span className="text-xs text-slate-500">(level {r.level})</span>
          </label>
        ))}
        {err && <div className="text-red-400 text-sm">{err}</div>}
        <div className="flex justify-end gap-2">
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" onClick={submit}>Save</button>
        </div>
      </div>
    </Drawer>
  );
}

function Drawer({ title, children, onClose }: { title: string; children: React.ReactNode; onClose: () => void }) {
  return (
    <div className="fixed inset-0 bg-black/60 flex justify-end" onClick={onClose}>
      <div className="w-[520px] bg-slate-900 border-l border-slate-700 h-full overflow-y-auto p-6" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg text-slate-100 font-semibold">{title}</h2>
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
