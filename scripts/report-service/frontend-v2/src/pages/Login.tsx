import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { api } from '../api';
import { useAuth } from '../auth';
import { useI18n } from '../i18n';

export default function Login() {
  const [username, setU] = useState('');
  const [password, setP] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const nav = useNavigate();
  const { refresh } = useAuth();
  const { t, lang, setLang } = useI18n();

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
    <div className="min-h-screen flex items-center justify-center relative bg-slate-50">
      <button
        className="absolute top-4 right-4 text-xs px-2 py-1 rounded border border-slate-300 text-slate-600 hover:bg-white"
        onClick={() => setLang(lang === 'zh' ? 'en' : 'zh')}
      >
        {lang === 'zh' ? 'EN' : '中文'}
      </button>
      <form onSubmit={submit} className="card w-80 space-y-4">
        <div className="text-center">
          <div className="text-2xl text-slate-900 font-semibold tracking-wide">{t('app.title')}</div>
        </div>
        <div>
          <label className="text-xs text-slate-500 block mb-1">{t('login.username')}</label>
          <input className="input" value={username} onChange={(e) => setU(e.target.value)} autoFocus />
        </div>
        <div>
          <label className="text-xs text-slate-500 block mb-1">{t('login.password')}</label>
          <input className="input" type="password" value={password} onChange={(e) => setP(e.target.value)} />
        </div>
        {err && <div className="text-xs text-red-600">{err}</div>}
        <button className="btn btn-primary w-full justify-center" type="submit" disabled={busy}>
          {busy ? t('login.submitting') : t('login.submit')}
        </button>
      </form>
    </div>
  );
}
