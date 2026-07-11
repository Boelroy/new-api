import { useEffect, useMemo, useState } from 'react';
import { api, Permission, Role } from '../api';
import { useAuth } from '../auth';
import { useI18n } from '../i18n';

interface Catalog {
  actions: { group: string; action: string; label: string }[];
  scopes: { scope: string; label: string }[];
}

export default function Roles() {
  const { me, hasPerm } = useAuth();
  const { t } = useI18n();
  const canManage = hasPerm('roles.manage');
  const [roles, setRoles] = useState<Role[]>([]);
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [err, setErr] = useState('');
  const [editing, setEditing] = useState<Role | null>(null);
  const [creating, setCreating] = useState(false);

  const reload = async () => {
    try {
      const r = await api.listRoles();
      setRoles(r.roles);
      const c = await api.permissionsCatalog();
      setCatalog(c);
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    }
  };
  useEffect(() => {
    reload();
  }, []);

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl text-slate-100 font-semibold">{t('roles.title')}</h1>
        {canManage && (
          <button className="btn btn-primary" onClick={() => setCreating(true)}>
            {t('roles.new')}
          </button>
        )}
      </div>
      {err && <div className="text-red-400 text-sm">{err}</div>}
      <div className="card overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr>
              <th className="th">{t('roles.col.name')}</th>
              <th className="th">{t('roles.col.display')}</th>
              <th className="th">{t('roles.col.level')}</th>
              <th className="th">{t('roles.col.builtin')}</th>
              <th className="th">{t('roles.col.permissions')}</th>
              <th className="th">{t('roles.col.users')}</th>
              <th className="th"></th>
            </tr>
          </thead>
          <tbody>
            {roles.map((r) => (
              <tr key={r.id}>
                <td className="td font-mono text-xs">{r.name}</td>
                <td className="td">{r.display_name}</td>
                <td className="td">{r.level}</td>
                <td className="td">{r.is_builtin ? t('common.yes') : ''}</td>
                <td className="td">{r.permissions?.length ?? 0}</td>
                <td className="td">{r.user_count ?? 0}</td>
                <td className="td text-right space-x-2">
                  {canManage && (r.is_builtin ? me?.is_super : true) && (
                    <button className="btn" onClick={() => setEditing(r)}>
                      {t('common.edit')}
                    </button>
                  )}
                  {canManage && !r.is_builtin && (r.user_count ?? 0) === 0 && (
                    <button
                      className="btn btn-danger"
                      onClick={async () => {
                        if (!confirm(t('roles.deleteConfirm', { name: r.name }))) return;
                        await api.deleteRole(r.id);
                        reload();
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
      {(editing || creating) && catalog && (
        <RoleEditor
          role={editing}
          catalog={catalog}
          onClose={() => {
            setEditing(null);
            setCreating(false);
          }}
          onSaved={() => {
            setEditing(null);
            setCreating(false);
            reload();
          }}
        />
      )}
    </div>
  );
}

function RoleEditor({
  role,
  catalog,
  onClose,
  onSaved,
}: {
  role: Role | null;
  catalog: Catalog;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { me } = useAuth();
  const { t } = useI18n();
  const [name, setName] = useState(role?.name ?? '');
  const [display, setDisplay] = useState(role?.display_name ?? '');
  const [level, setLevel] = useState(role?.level ?? 10);
  const initialPerms = useMemo(() => new Set((role?.permissions ?? []).map((p) => `${p.action}@${p.scope}`)), [role]);
  const [checked, setChecked] = useState<Set<string>>(initialPerms);
  const [err, setErr] = useState('');
  const [saving, setSaving] = useState(false);

  const myPerms = new Set(me?.permissions ?? []);
  const canGrant = (action: string, scope: string) => {
    if (me?.is_super) return true;
    // Backend does subsumption check; for UI we accept if we hold the pair
    // OR a broader scope of the same action.
    const chain = { global: ['global'], any_studio: ['any_studio', 'global'], own_studio: ['own_studio', 'any_studio', 'global'], self: ['self', 'own_studio', 'any_studio', 'global'] } as Record<string, string[]>;
    return (chain[scope] ?? [scope]).some((s) => myPerms.has(`${action}@${s}`));
  };

  const toggle = (key: string) => {
    const next = new Set(checked);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    setChecked(next);
  };

  const save = async () => {
    setSaving(true);
    setErr('');
    try {
      const perms: Permission[] = Array.from(checked).map((k) => {
        const at = k.lastIndexOf('@');
        return { action: k.slice(0, at), scope: k.slice(at + 1) };
      });
      if (role) {
        await api.updateRole(role.id, { display_name: display, level, permissions: perms });
      } else {
        await api.createRole({ name, display_name: display, level, permissions: perms });
      }
      onSaved();
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    } finally {
      setSaving(false);
    }
  };

  const grouped = useMemo(() => {
    const m: Record<string, typeof catalog.actions> = {};
    for (const a of catalog.actions) {
      (m[a.group] ??= []).push(a);
    }
    return m;
  }, [catalog]);

  return (
    <div className="fixed inset-0 bg-black/60 flex justify-end" onClick={onClose}>
      <div className="w-[720px] bg-slate-900 border-l border-slate-700 h-full overflow-y-auto p-6" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg text-slate-100 font-semibold">
            {role ? t('roles.editor.editTitle', { name: role.name }) : t('roles.editor.newTitle')}
          </h2>
          <button className="btn" onClick={onClose}>{t('common.close')}</button>
        </div>
        <div className="space-y-3 mb-6">
          {!role && (
            <div>
              <label className="text-xs text-slate-400 block mb-1">{t('roles.editor.name')}</label>
              <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
            </div>
          )}
          <div>
            <label className="text-xs text-slate-400 block mb-1">{t('roles.editor.display')}</label>
            <input className="input" value={display} onChange={(e) => setDisplay(e.target.value)} />
          </div>
          <div>
            <label className="text-xs text-slate-400 block mb-1">{t('roles.editor.level')}</label>
            <input className="input" type="number" value={level} onChange={(e) => setLevel(parseInt(e.target.value, 10) || 0)} />
          </div>
        </div>
        <h3 className="text-sm text-slate-300 mb-2">{t('roles.editor.permissions')}</h3>
        {Object.entries(grouped).map(([group, actions]) => (
          <div key={group} className="mb-3 border border-slate-700 rounded p-3">
            <div className="text-xs text-slate-400 uppercase tracking-wider mb-2">{group}</div>
            {actions.map((a) => (
              <div key={a.action} className="mb-2">
                <div className="text-sm text-slate-100 mb-1">{a.label}</div>
                <div className="flex gap-3 flex-wrap">
                  {catalog.scopes.map((s) => {
                    const key = `${a.action}@${s.scope}`;
                    const allowed = canGrant(a.action, s.scope);
                    return (
                      <label key={s.scope} className={`text-xs flex items-center gap-1 ${allowed ? '' : 'opacity-40'}`}>
                        <input
                          type="checkbox"
                          checked={checked.has(key)}
                          disabled={!allowed}
                          onChange={() => toggle(key)}
                        />
                        <span className="font-mono">{s.scope}</span>
                      </label>
                    );
                  })}
                </div>
              </div>
            ))}
          </div>
        ))}
        {err && <div className="text-red-400 text-sm mb-2">{err}</div>}
        <div className="flex justify-end gap-2">
          <button className="btn" onClick={onClose}>{t('common.cancel')}</button>
          <button className="btn btn-primary" onClick={save} disabled={saving}>
            {saving ? t('common.saving') : t('common.save')}
          </button>
        </div>
      </div>
    </div>
  );
}
