import { useEffect, useMemo, useState } from 'react';
import { api, ProfileSlim } from '../api';
import { useAuth } from '../auth';

// Dual-mode: pool_only (default) OR direct_newapi. Studio is locked to the
// caller's JWT-bound studio for own_studio-scoped roles; admin with
// any_studio can set it explicitly via ?studio=X (not exposed in UI here —
// admin uploads typically happen via V1).
export default function KeysUpload() {
  const { me } = useAuth();
  const [keyType, setKeyType] = useState<'regular' | 'trial_5usd'>('regular');
  const [mode, setMode] = useState<'pool_only' | 'direct_newapi'>('pool_only');
  const [profileID, setProfileID] = useState<number | null>(null);
  const [profiles, setProfiles] = useState<ProfileSlim[]>([]);
  const [models, setModels] = useState('');
  const [group, setGroup] = useState('');
  const [prefix, setPrefix] = useState('');
  const [raw, setRaw] = useState('');
  const [results, setResults] = useState<{ row: number; status: string; error?: string }[]>([]);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        const p = await api.listProfilesSlim();
        setProfiles(p.profiles);
      } catch { /* ignore — direct_newapi will just show empty */ }
    })();
  }, []);

  useEffect(() => {
    if (mode === 'direct_newapi' && profileID) {
      const p = profiles.find((x) => x.id === profileID);
      if (p) {
        if (!models) setModels(p.default_models);
        if (!group) setGroup(p.default_group);
      }
    }
  }, [mode, profileID, profiles]);

  const keys = useMemo(
    () => raw.split(/\r?\n/).map((s) => s.trim()).filter(Boolean),
    [raw]
  );

  const submit = async () => {
    setBusy(true);
    try {
      const body = {
        key_type: keyType,
        target_mode: mode,
        target_profile_id: mode === 'direct_newapi' ? profileID : undefined,
        models,
        group,
        name_prefix: prefix,
        keys: keys.map((k) => ({ key: k })),
      };
      const r = await api.uploadKeys(body);
      setResults(r.results);
    } catch (e: any) {
      setResults([{ row: -1, status: 'error', error: e?.message ?? String(e) }]);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="p-6 space-y-4">
      <h1 className="text-xl text-slate-100 font-semibold">Upload Keys</h1>
      {me?.studio ? (
        <p className="text-sm text-slate-400">Studio locked to <span className="font-mono">{me.studio}</span></p>
      ) : (
        <p className="text-sm text-yellow-400">No studio bound to your account — an admin must set one before uploading.</p>
      )}

      <div className="card space-y-4">
        <div>
          <div className="text-xs text-slate-400 mb-1">Key type</div>
          <div className="flex gap-4 text-sm">
            <label><input type="radio" name="kt" checked={keyType === 'regular'} onChange={() => setKeyType('regular')} /> regular</label>
            <label><input type="radio" name="kt" checked={keyType === 'trial_5usd'} onChange={() => setKeyType('trial_5usd')} /> trial_5usd (5 USD)</label>
          </div>
        </div>
        <div>
          <div className="text-xs text-slate-400 mb-1">Target</div>
          <div className="flex gap-4 text-sm">
            <label><input type="radio" name="tm" checked={mode === 'pool_only'} onChange={() => setMode('pool_only')} /> pool only (admin will assign)</label>
            <label><input type="radio" name="tm" checked={mode === 'direct_newapi'} onChange={() => setMode('direct_newapi')} /> direct to remote newapi</label>
          </div>
        </div>
        {mode === 'direct_newapi' && (
          <div>
            <div className="text-xs text-slate-400 mb-1">Remote newapi profile</div>
            <select className="input" value={profileID ?? ''} onChange={(e) => setProfileID(parseInt(e.target.value, 10) || null)}>
              <option value="">— pick a profile —</option>
              {profiles.map((p) => (
                <option key={p.id} value={p.id} disabled={p.accepts_studio === false}>
                  {p.name}{p.accepts_studio === false ? ' (not accepting your studio)' : ''}
                </option>
              ))}
            </select>
          </div>
        )}
        <div className="grid grid-cols-3 gap-3">
          <div>
            <div className="text-xs text-slate-400 mb-1">Models</div>
            <input className="input" value={models} onChange={(e) => setModels(e.target.value)} placeholder="claude-sonnet-4-6,claude-opus-4-7" />
          </div>
          <div>
            <div className="text-xs text-slate-400 mb-1">Group</div>
            <input className="input" value={group} onChange={(e) => setGroup(e.target.value)} placeholder="default" />
          </div>
          <div>
            <div className="text-xs text-slate-400 mb-1">Name prefix</div>
            <input className="input" value={prefix} onChange={(e) => setPrefix(e.target.value)} placeholder="alpha" />
          </div>
        </div>
        <div>
          <div className="text-xs text-slate-400 mb-1">Keys (one per line, {keys.length} loaded)</div>
          <textarea
            className="input h-48 font-mono text-xs"
            value={raw}
            onChange={(e) => setRaw(e.target.value)}
            placeholder="sk-..."
          />
        </div>
        <div className="flex justify-end">
          <button className="btn btn-primary" onClick={submit} disabled={busy || keys.length === 0 || (mode === 'direct_newapi' && !profileID)}>
            {busy ? 'Uploading…' : `Upload ${keys.length} key${keys.length === 1 ? '' : 's'}`}
          </button>
        </div>
      </div>

      {results.length > 0 && (
        <div className="card">
          <h2 className="text-slate-100 font-semibold mb-2">Results</h2>
          <table className="w-full text-sm">
            <thead>
              <tr><th className="th">Row</th><th className="th">Status</th><th className="th">Error</th></tr>
            </thead>
            <tbody>
              {results.map((r, i) => (
                <tr key={i}>
                  <td className="td">{r.row}</td>
                  <td className="td">
                    <span className={r.status === 'error' || r.status === 'duplicate' ? 'text-red-400' : 'text-green-400'}>{r.status}</span>
                  </td>
                  <td className="td text-xs text-slate-400">{r.error ?? ''}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
