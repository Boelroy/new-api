import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { api } from '../api';
import { useAuth } from '../auth';

export default function Login() {
  const [username, setU] = useState('');
  const [password, setP] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const nav = useNavigate();
  const { refresh } = useAuth();

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr('');
    try {
      await api.login(username, password);
      await refresh();
      nav('/');
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="min-h-screen flex items-center justify-center">
      <form onSubmit={submit} className="card w-80 space-y-3">
        <h1 className="text-lg text-slate-100 font-semibold">Report V2 · Sign in</h1>
        <div>
          <label className="text-xs text-slate-400 block mb-1">Username</label>
          <input className="input" value={username} onChange={(e) => setU(e.target.value)} autoFocus />
        </div>
        <div>
          <label className="text-xs text-slate-400 block mb-1">Password</label>
          <input className="input" type="password" value={password} onChange={(e) => setP(e.target.value)} />
        </div>
        {err && <div className="text-xs text-red-400">{err}</div>}
        <button className="btn btn-primary w-full" type="submit" disabled={busy}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </div>
  );
}
