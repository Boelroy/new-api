import { useEffect, useMemo, useState } from 'react';
import { api, UsageRow } from '../api';
import { StatusBadge } from './KeysPool';

// Single component for /usage/my, /usage/studio, /usage/all. Backend
// enforces scope from the caller's permission set — this component just
// adjusts filters for UX.
export default function Usage({ kind }: { kind: 'my' | 'studio' | 'all' }) {
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

  const title = kind === 'my' ? 'My Usage' : kind === 'studio' ? 'Studio Usage' : 'All Usage';

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl text-slate-100 font-semibold">{title}</h1>
        <div className="flex gap-2 text-sm">
          <select className="input" value={keyType} onChange={(e) => setKeyType(e.target.value)}>
            <option value="">all types</option>
            <option value="regular">regular</option>
            <option value="trial_5usd">trial_5usd</option>
          </select>
          <select className="input" value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="pending,active,used,failed">non-awaiting</option>
            <option value="active">active only</option>
            <option value="used">used only</option>
          </select>
        </div>
      </div>
      {err && <div className="text-red-400 text-sm">{err}</div>}
      <div className="grid grid-cols-3 gap-3">
        <Stat label="Keys" value={totals.count.toString()} />
        <Stat label="Used (USD)" value={`$${totals.usedUSD.toFixed(2)}`} />
        <Stat label="Quota (USD)" value={`$${totals.quotaUSD.toFixed(2)}`} />
      </div>
      <div className="card overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr>
              <th className="th">ID</th>
              <th className="th">Studio</th>
              <th className="th">Type</th>
              <th className="th">Key</th>
              <th className="th">Status</th>
              <th className="th text-right">Used</th>
              <th className="th text-right">Quota</th>
              <th className="th text-right">Used / Quota</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id}>
                <td className="td">{r.id}</td>
                <td className="td">{r.studio}</td>
                <td className="td font-mono text-xs">{r.key_type}</td>
                <td className="td font-mono text-xs">
                  {r.key ? <span className="text-yellow-300">{r.key}</span> : r.key_masked}
                  {r.is_dead && <span className="ml-2 text-xs text-red-400">dead</span>}
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
      <div className="text-xl text-slate-100 font-semibold">{value}</div>
    </div>
  );
}
