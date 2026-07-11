import { useEffect, useState } from 'react';
import { api, KeyPoolRow, ProfileSlim } from '../api';
import { useAuth } from '../auth';
import { useI18n } from '../i18n';
import { StatusBadge } from './KeysPool';

export default function KeysActive() {
  const { hasPerm } = useAuth();
  const { t } = useI18n();
  const canRebind = hasPerm('keys.newapi.rebind');
  const canDisable = hasPerm('keys.newapi.disable');
  const [rows, setRows] = useState<KeyPoolRow[]>([]);
  const [profiles, setProfiles] = useState<ProfileSlim[]>([]);
  const [err, setErr] = useState('');

  const reload = async () => {
    try {
      const r = await api.listActive();
      setRows(r.keys);
      const p = await api.listProfilesSlim();
      setProfiles(p.profiles);
    } catch (e: any) { setErr(e?.message ?? String(e)); }
  };
  useEffect(() => { reload(); }, []);

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl text-slate-900 font-semibold">{t('keys.active.title')}</h1>
        <a className="btn" href="/api/v2/keys/export.csv?status=active,used" target="_blank" rel="noreferrer">{t('common.exportCsv')}</a>
      </div>
      {err && <div className="text-red-600 text-sm">{err}</div>}

      <div className="card overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr>
              <th className="th">{t('users.col.id')}</th>
              <th className="th">{t('keys.pool.col.studio')}</th>
              <th className="th">{t('keys.pool.col.type')}</th>
              <th className="th">{t('keys.pool.col.key')}</th>
              <th className="th">{t('keys.pool.col.status')}</th>
              <th className="th">{t('keys.pool.col.profile')}</th>
              <th className="th">{t('keys.active.col.channel')}</th>
              <th className="th"></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id}>
                <td className="td">{r.id}</td>
                <td className="td">{r.studio}</td>
                <td className="td font-mono text-xs">{r.key_type}</td>
                <td className="td font-mono text-xs">
                  {r.key ? <span className="text-amber-700">{r.key}</span> : r.key_masked}
                  {r.is_dead && <span className="ml-2 text-xs text-red-600">{t('keys.pool.dead')}</span>}
                </td>
                <td className="td"><StatusBadge s={r.status} /></td>
                <td className="td">{r.assigned_profile_id || '—'}</td>
                <td className="td">{r.remote_channel_id || '—'}</td>
                <td className="td text-right space-x-2">
                  {canRebind && r.status === 'active' && (
                    <button
                      className="btn"
                      onClick={async () => {
                        const to = prompt(t('keys.active.rebindPrompt', { opts: profiles.map((p) => `${p.id}=${p.name}`).join(', ') }));
                        if (!to) return;
                        try { await api.rebindKey(r.id, parseInt(to, 10)); reload(); } catch (e: any) { alert(e?.message); }
                      }}
                    >{t('keys.active.rebind')}</button>
                  )}
                  {canDisable && r.status !== 'used' && (
                    <button
                      className="btn btn-danger"
                      onClick={async () => {
                        if (!confirm(t('keys.active.disableConfirm', { id: r.id }))) return;
                        try { await api.disableKey(r.id); reload(); } catch (e: any) { alert(e?.message); }
                      }}
                    >{t('users.action.disable')}</button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
