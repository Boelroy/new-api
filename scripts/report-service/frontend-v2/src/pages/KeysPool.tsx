import { useEffect, useMemo, useState } from 'react';
import { api, KeyPoolRow, ProfileSlim } from '../api';
import { useAuth } from '../auth';
import { useI18n } from '../i18n';

export default function KeysPool() {
  const { hasPerm } = useAuth();
  const { t } = useI18n();
  const canAssign = hasPerm('keys.pool.assign');
  const canDelete = hasPerm('keys.pool.delete', 'own_studio');
  const [rows, setRows] = useState<KeyPoolRow[]>([]);
  const [profiles, setProfiles] = useState<ProfileSlim[]>([]);
  const [statusFilter, setStatusFilter] = useState('awaiting_assignment,failed');
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [assignProfile, setAssignProfile] = useState<number | null>(null);
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  const reload = async () => {
    try {
      const r = await api.listPool(statusFilter);
      setRows(r.keys);
      const p = await api.listProfilesSlim();
      setProfiles(p.profiles);
    } catch (e: any) { setErr(e?.message ?? String(e)); }
  };
  useEffect(() => { reload(); }, [statusFilter]);

  const toggle = (id: number) => {
    const n = new Set(selected);
    n.has(id) ? n.delete(id) : n.add(id);
    setSelected(n);
  };
  const selectAll = () => setSelected(new Set(rows.filter((r) => r.status === 'awaiting_assignment').map((r) => r.id)));
  const clear = () => setSelected(new Set());

  const doAssign = async () => {
    if (!assignProfile || selected.size === 0) return;
    setBusy(true);
    try {
      await api.assignPool(Array.from(selected), assignProfile);
      setSelected(new Set());
      await reload();
    } catch (e: any) { setErr(e?.message ?? String(e)); } finally { setBusy(false); }
  };

  const assignable = useMemo(() => Array.from(selected).length > 0 && Array.from(selected).every((id) => rows.find((r) => r.id === id)?.status === 'awaiting_assignment'), [selected, rows]);

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl text-slate-100 font-semibold">{t('keys.pool.title')}</h1>
        <div className="flex items-center gap-2">
          <select className="input w-56" value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
            <option value="awaiting_assignment,failed">{t('keys.pool.filter.awaitingFailed')}</option>
            <option value="awaiting_assignment">{t('keys.pool.filter.awaitingOnly')}</option>
            <option value="pending">{t('keys.pool.filter.pending')}</option>
            <option value="failed">{t('keys.pool.filter.failed')}</option>
          </select>
          <a className="btn" href={`/api/v2/keys/export.csv?status=${encodeURIComponent(statusFilter)}`} target="_blank" rel="noreferrer">{t('common.exportCsv')}</a>
        </div>
      </div>
      {err && <div className="text-red-400 text-sm">{err}</div>}

      {canAssign && (
        <div className="card flex items-center gap-3">
          <button className="btn" onClick={selectAll}>{t('keys.pool.selectAllAwaiting')}</button>
          <button className="btn" onClick={clear} disabled={selected.size === 0}>{t('keys.pool.clear')}</button>
          <div className="text-sm text-slate-400">{t('keys.pool.selected', { n: selected.size })}</div>
          <select className="input max-w-xs" value={assignProfile ?? ''} onChange={(e) => setAssignProfile(parseInt(e.target.value, 10) || null)}>
            <option value="">{t('keys.pool.pickTargetProfile')}</option>
            {profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
          <button className="btn btn-primary" disabled={!assignable || !assignProfile || busy} onClick={doAssign}>
            {busy ? t('keys.pool.assigning') : t('keys.pool.assign', { n: selected.size })}
          </button>
        </div>
      )}

      <div className="card overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr>
              <th className="th"></th>
              <th className="th">{t('users.col.id')}</th>
              <th className="th">{t('keys.pool.col.studio')}</th>
              <th className="th">{t('keys.pool.col.type')}</th>
              <th className="th">{t('keys.pool.col.key')}</th>
              <th className="th">{t('keys.pool.col.status')}</th>
              <th className="th">{t('keys.pool.col.profile')}</th>
              <th className="th">{t('keys.pool.col.failedReason')}</th>
              <th className="th"></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id}>
                <td className="td">
                  {r.status === 'awaiting_assignment' && (
                    <input type="checkbox" checked={selected.has(r.id)} onChange={() => toggle(r.id)} />
                  )}
                </td>
                <td className="td">{r.id}</td>
                <td className="td">{r.studio}</td>
                <td className="td font-mono text-xs">{r.key_type}</td>
                <td className="td font-mono text-xs">
                  {r.key ? <span className="text-yellow-300">{r.key}</span> : r.key_masked}
                  {r.is_dead && <span className="ml-2 text-xs text-red-400">{t('keys.pool.dead')}</span>}
                </td>
                <td className="td">
                  <StatusBadge s={r.status} />
                </td>
                <td className="td">{r.assigned_profile_id || <span className="text-slate-500">—</span>}</td>
                <td className="td text-xs text-slate-400 max-w-md truncate" title={r.failed_reason}>{r.failed_reason}</td>
                <td className="td text-right">
                  {canDelete && (r.status === 'awaiting_assignment' || r.status === 'failed') && (
                    <button
                      className="btn btn-danger"
                      onClick={async () => {
                        if (!confirm(t('keys.pool.deleteConfirm', { id: r.id }))) return;
                        try { await api.deletePool(r.id); reload(); } catch (e: any) { alert(e?.message); }
                      }}
                    >{t('common.delete')}</button>
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

export function StatusBadge({ s }: { s: string }) {
  const cls = ({
    awaiting_assignment: 'bg-yellow-800 text-yellow-200',
    pending: 'bg-blue-800 text-blue-200',
    active: 'bg-green-800 text-green-200',
    used: 'bg-slate-700 text-slate-300',
    failed: 'bg-red-900 text-red-200',
  } as Record<string, string>)[s] ?? 'bg-slate-700 text-slate-200';
  return <span className={`px-1.5 py-0.5 rounded text-xs ${cls}`}>{s}</span>;
}
