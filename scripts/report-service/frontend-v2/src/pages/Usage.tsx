import { useEffect, useMemo, useState } from 'react';
import { api, UsageRow } from '../api';
import { useAuth } from '../auth';
import { useI18n } from '../i18n';
import { StatusBadge } from './KeysPool';

// Single page, three filter scopes gated by permission.
// - "mine": rs_key_pool.uploaded_by = self  (needs any usage.view)
// - "studio": rs_key_pool.studio = ctx.studio (needs usage.view@own_studio+)
// - "all": no studio filter (needs usage.view@any_studio+)
//
// Default picks the most useful scope for the caller: uploaders see "mine",
// pure viewers see their studio, admins see everything.
type Scope = 'mine' | 'studio' | 'all';

export default function Usage() {
  const { hasPerm } = useAuth();
  const { t } = useI18n();

  // Which scopes is the caller allowed to view?
  const canMine = hasPerm('usage.view', 'self');
  const canStudio = hasPerm('usage.view', 'own_studio');
  const canAll = hasPerm('usage.view', 'any_studio');

  // uploader-capable users default to "mine"; else studio; else all.
  const canUpload = hasPerm('keys.pool.upload', 'own_studio');
  const defaultScope: Scope = canUpload && canMine ? 'mine' : canStudio ? 'studio' : 'all';
  const [scope, setScope] = useState<Scope>(defaultScope);
  const [rows, setRows] = useState<UsageRow[]>([]);
  const [err, setErr] = useState('');
  const [keyType, setKeyType] = useState('');
  const [status, setStatus] = useState('pending,active,used,failed');

  const reload = async () => {
    try {
      const params = new URLSearchParams();
      if (scope === 'mine') params.set('mine', 'true');
      // scope=studio: backend already scopes by caller studio unless they
      //   have any_studio. Which is exactly what we want.
      // scope=all: only allowed if caller has any_studio, and backend
      //   returns unfiltered rows.
      if (keyType) params.set('key_type', keyType);
      if (status) params.set('status', status);
      const r = await api.usage(params);
      // If scope=all and caller only has own_studio, backend still filters
      // by studio — this is safe (defense in depth).
      setRows(r.rows);
    } catch (e: any) { setErr(e?.message ?? String(e)); }
  };
  useEffect(() => { reload(); }, [scope, keyType, status]);

  const totals = useMemo(() => {
    let usedUSD = 0;
    let quotaUSD = 0;
    for (const r of rows) {
      usedUSD += r.used_usd;
      if (r.quota_usd != null) quotaUSD += r.quota_usd;
    }
    return { usedUSD, quotaUSD, count: rows.length };
  }, [rows]);

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl text-slate-900 font-semibold">{t('usage.title')}</h1>
        <div className="flex gap-2 text-sm">
          <select className="input" value={keyType} onChange={(e) => setKeyType(e.target.value)}>
            <option value="">{t('usage.filter.allTypes')}</option>
            <option value="regular">regular</option>
            <option value="trial_5usd">trial_5usd</option>
          </select>
          <select className="input" value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="pending,active,used,failed">{t('usage.filter.nonAwaiting')}</option>
            <option value="active">{t('usage.filter.activeOnly')}</option>
            <option value="used">{t('usage.filter.usedOnly')}</option>
          </select>
        </div>
      </div>

      <div className="flex items-center gap-3">
        <div className="text-xs text-slate-500 uppercase tracking-wider">{t('usage.scope.label')}</div>
        <div className="inline-flex rounded-md border border-slate-200 overflow-hidden">
          <ScopeButton active={scope === 'mine'} disabled={!canMine} onClick={() => setScope('mine')}>
            {t('usage.scope.mine')}
          </ScopeButton>
          <ScopeButton active={scope === 'studio'} disabled={!canStudio} onClick={() => setScope('studio')}>
            {t('usage.scope.studio')}
          </ScopeButton>
          <ScopeButton active={scope === 'all'} disabled={!canAll} onClick={() => setScope('all')}>
            {t('usage.scope.all')}
          </ScopeButton>
        </div>
      </div>

      {err && <div className="text-red-600 text-sm">{err}</div>}
      <div className="grid grid-cols-3 gap-3">
        <Stat label={t('usage.stat.keys')} value={totals.count.toString()} />
        <Stat label={t('usage.stat.usedUsd')} value={`$${totals.usedUSD.toFixed(2)}`} />
        <Stat label={t('usage.stat.quotaUsd')} value={`$${totals.quotaUSD.toFixed(2)}`} />
      </div>
      <div className="card overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr>
              <th className="th">{t('users.col.id')}</th>
              <th className="th">{t('keys.pool.col.studio')}</th>
              <th className="th">{t('keys.pool.col.type')}</th>
              <th className="th">{t('keys.pool.col.key')}</th>
              <th className="th">{t('keys.pool.col.status')}</th>
              <th className="th text-right">{t('usage.col.used')}</th>
              <th className="th text-right">{t('usage.col.quota')}</th>
              <th className="th text-right">{t('usage.col.usedOverQuota')}</th>
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
                <td className="td text-right">${r.used_usd.toFixed(4)}</td>
                <td className="td text-right">{r.quota_usd != null ? `$${r.quota_usd.toFixed(2)}` : '—'}</td>
                <td className="td text-right">
                  {r.quota_usd != null && r.quota_usd > 0
                    ? `${((r.used_usd / r.quota_usd) * 100).toFixed(1)}%`
                    : '—'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function ScopeButton({
  active, disabled, onClick, children,
}: { active: boolean; disabled: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={`px-3 py-1.5 text-sm border-r border-slate-200 last:border-r-0 transition-colors ${
        active
          ? 'bg-blue-600 text-white'
          : disabled
            ? 'bg-slate-50 text-slate-400 cursor-not-allowed'
            : 'bg-white text-slate-700 hover:bg-slate-50'
      }`}
    >
      {children}
    </button>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="card">
      <div className="text-xs text-slate-500">{label}</div>
      <div className="text-xl text-slate-900 font-semibold">{value}</div>
    </div>
  );
}
