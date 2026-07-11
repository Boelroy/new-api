import { useEffect, useMemo, useState } from 'react';
import { api, UsageRow } from '../api';
import { useI18n } from '../i18n';
import { StatusBadge } from './KeysPool';

// Single component for /usage/my, /usage/studio, /usage/all. Backend
// enforces scope from the caller's permission set — this component just
// adjusts filters for UX.
export default function Usage({ kind }: { kind: 'my' | 'studio' | 'all' }) {
  const { t } = useI18n();
  const [rows, setRows] = useState<UsageRow[]>([]);
  const [err, setErr] = useState('');
  const [keyType, setKeyType] = useState('');
  const [status, setStatus] = useState('pending,active,used,failed');

  const reload = async () => {
    try {
      const params = new URLSearchParams();
      if (kind === 'my') params.set('mine', 'true');
      if (keyType) params.set('key_type', keyType);
      if (status) params.set('status', status);
      const r = await api.usage(params);
      setRows(r.rows);
    } catch (e: any) { setErr(e?.message ?? String(e)); }
  };
  useEffect(() => { reload(); }, [kind, keyType, status]);

  const totals = useMemo(() => {
    let usedUSD = 0;
    let quotaUSD = 0;
    for (const r of rows) {
      usedUSD += r.used_usd;
      if (r.quota_usd != null) quotaUSD += r.quota_usd;
    }
    return { usedUSD, quotaUSD, count: rows.length };
  }, [rows]);

  const title = kind === 'my' ? t('usage.title.my') : kind === 'studio' ? t('usage.title.studio') : t('usage.title.all');

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl text-slate-900 font-semibold">{title}</h1>
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

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="card">
      <div className="text-xs text-slate-400">{label}</div>
      <div className="text-xl text-slate-900 font-semibold">{value}</div>
    </div>
  );
}
