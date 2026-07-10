import { useEffect, useState } from 'react';
import { api, ProfileFull } from '../api';

export default function Profiles() {
  const [rows, setRows] = useState<ProfileFull[]>([]);
  const [err, setErr] = useState('');
  const [editing, setEditing] = useState<ProfileFull | null>(null);
  const [creating, setCreating] = useState(false);

  const reload = async () => {
    try {
      const p = await api.listProfilesFull();
      setRows(p.profiles);
    } catch (e: any) { setErr(e?.message ?? String(e)); }
  };
  useEffect(() => { reload(); }, []);

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl text-slate-100 font-semibold">Remote Profiles</h1>
        <button className="btn btn-primary" onClick={() => setCreating(true)}>+ New profile</button>
      </div>
      {err && <div className="text-red-400 text-sm">{err}</div>}
      <div className="card overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr>
              <th className="th">ID</th>
              <th className="th">Name</th>
              <th className="th">Host</th>
              <th className="th">Token</th>
              <th className="th">Pool</th>
              <th className="th"></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((p) => (
              <tr key={p.id}>
                <td className="td">{p.id}</td>
                <td className="td">{p.name}</td>
                <td className="td font-mono text-xs">{p.host}</td>
                <td className="td">
                  {p.has_access_token ? <span className="text-green-400">set</span> : <span className="text-red-400">missing</span>}
                </td>
                <td className="td text-xs text-slate-400">
                  {p.auto_mode ? `auto (base ${p.rpm_base}, min ${p.rpm_min})` : `${p.pool_interval_sec}s / ${p.pool_batch_size}`}
                </td>
                <td className="td text-right space-x-2">
                  <button className="btn" onClick={() => setEditing(p)}>Edit</button>
                  <button
                    className="btn btn-danger"
                    onClick={async () => {
                      if (!confirm(`Delete profile ${p.name}?`)) return;
                      try { await api.deleteProfile(p.id); reload(); } catch (e: any) { alert(e?.message); }
                    }}
                  >Delete</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {(creating || editing) && (
        <ProfileEditor profile={editing} onClose={() => { setEditing(null); setCreating(false); }} onSaved={() => { setEditing(null); setCreating(false); reload(); }} />
      )}
    </div>
  );
}

function ProfileEditor({ profile, onClose, onSaved }: { profile: ProfileFull | null; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(profile?.name ?? '');
  const [host, setHost] = useState(profile?.host ?? '');
  const [userID, setUserID] = useState(profile?.user_id ?? 1);
  const [token, setToken] = useState('');
  const [defModels, setDefModels] = useState(profile?.default_models ?? '');
  const [defGroup, setDefGroup] = useState(profile?.default_group ?? 'default');
  const [interval, setIv] = useState(profile?.pool_interval_sec ?? 60);
  const [batch, setBatch] = useState(profile?.pool_batch_size ?? 2);
  const [auto, setAuto] = useState(profile?.auto_mode ?? false);
  const [rbase, setRB] = useState(profile?.rpm_base ?? 150);
  const [rmin, setRM] = useState(profile?.rpm_min ?? 50);
  const [err, setErr] = useState('');
  const [saving, setSav] = useState(false);

  const submit = async () => {
    setSav(true); setErr('');
    try {
      if (profile) {
        const body: any = {
          name, host, user_id: userID, default_models: defModels, default_group: defGroup,
          pool_interval_sec: interval, pool_batch_size: batch, auto_mode: auto, rpm_base: rbase, rpm_min: rmin,
        };
        if (token) body.access_token = token;
        await api.updateProfile(profile.id, body);
      } else {
        await api.createProfile({
          name, host, user_id: userID, access_token: token, default_models: defModels, default_group: defGroup,
          pool_interval_sec: interval, pool_batch_size: batch, auto_mode: auto, rpm_base: rbase, rpm_min: rmin,
        });
      }
      onSaved();
    } catch (e: any) { setErr(e?.message ?? String(e)); } finally { setSav(false); }
  };

  return (
    <div className="fixed inset-0 bg-black/60 flex justify-end" onClick={onClose}>
      <div className="w-[560px] bg-slate-900 border-l border-slate-700 h-full overflow-y-auto p-6" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg text-slate-100 font-semibold">{profile ? `Edit ${profile.name}` : 'New profile'}</h2>
          <button className="btn" onClick={onClose}>×</button>
        </div>
        <div className="space-y-3">
          <div>
            <label className="text-xs text-slate-400 block mb-1">Name</label>
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div>
            <label className="text-xs text-slate-400 block mb-1">Host (base URL)</label>
            <input className="input font-mono" value={host} onChange={(e) => setHost(e.target.value)} placeholder="https://example.com" />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="text-xs text-slate-400 block mb-1">User ID (New-Api-User)</label>
              <input className="input" type="number" value={userID} onChange={(e) => setUserID(parseInt(e.target.value, 10) || 1)} />
            </div>
            <div>
              <label className="text-xs text-slate-400 block mb-1">
                Access token {profile ? '(leave blank to keep)' : ''}
              </label>
              <input className="input" type="password" value={token} onChange={(e) => setToken(e.target.value)} placeholder={profile?.has_access_token ? '••••••••' : ''} />
            </div>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="text-xs text-slate-400 block mb-1">Default models</label>
              <input className="input" value={defModels} onChange={(e) => setDefModels(e.target.value)} />
            </div>
            <div>
              <label className="text-xs text-slate-400 block mb-1">Default group</label>
              <input className="input" value={defGroup} onChange={(e) => setDefGroup(e.target.value)} />
            </div>
          </div>
          <div className="grid grid-cols-4 gap-3">
            <div>
              <label className="text-xs text-slate-400 block mb-1">Interval (s)</label>
              <input className="input" type="number" value={interval} onChange={(e) => setIv(parseInt(e.target.value, 10) || 60)} />
            </div>
            <div>
              <label className="text-xs text-slate-400 block mb-1">Batch</label>
              <input className="input" type="number" value={batch} onChange={(e) => setBatch(parseInt(e.target.value, 10) || 2)} />
            </div>
            <div>
              <label className="text-xs text-slate-400 block mb-1">RPM base</label>
              <input className="input" type="number" value={rbase} onChange={(e) => setRB(parseInt(e.target.value, 10) || 150)} />
            </div>
            <div>
              <label className="text-xs text-slate-400 block mb-1">RPM min</label>
              <input className="input" type="number" value={rmin} onChange={(e) => setRM(parseInt(e.target.value, 10) || 50)} />
            </div>
          </div>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
            Auto mode (size batch by live RPM)
          </label>
          {err && <div className="text-red-400 text-sm">{err}</div>}
          <div className="flex justify-end gap-2">
            <button className="btn" onClick={onClose}>Cancel</button>
            <button className="btn btn-primary" onClick={submit} disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
          </div>
        </div>
      </div>
    </div>
  );
}
