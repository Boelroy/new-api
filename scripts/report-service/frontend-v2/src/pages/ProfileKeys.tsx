import { useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { api, KeyPoolRow } from '../api';
import { useI18n } from '../i18n';
import { StatusBadge } from './KeysPool';

// Drilldown for one remote profile — shows every channel on it,
// regardless of whether it was uploaded through V2's pool or predates
// Argus. Backend serves the same KeyPoolDTO shape as /keys/pool /active
// /usage so this table is a stripped-down reuse of KeysActive.
export default function ProfileKeys() {
  const { t } = useI18n();
  const { id } = useParams<{ id: string }>();
  const profileID = parseInt(id ?? '0', 10);
  const [rows, setRows] = useState<KeyPoolRow[]>([]);
  const [profileName, setProfileName] = useState('');
  const [err, setErr] = useState('');

  useEffect(() => {
    if (!profileID) return;
    (async () => {
      try {
        const r = await api.profileChannels(profileID);
        setRows(r.keys);
        setProfileName(r.profile.name);
      } catch (e: any) {
        setErr(e?.message ?? String(e));
      }
    })();
  }, [profileID]);

  const totals = useMemo(() => {
    let usedUSD = 0;
    let quotaUSD = 0;
    for (const r of rows) {
      usedUSD += r.used_usd ?? 0;
      if (r.quota_usd != null) quotaUSD += r.quota_usd;
    }
    return { count: rows.length, usedUSD, quotaUSD };
  }, [rows]);

  return (
    <div className="p-6 space-y-4">
      <div>
        <Link to="/profiles" className="text-sm text-blue-600 hover:underline">
          {t('profileKeys.back')}
        </Link>
      </div>
      <div className="flex items-center justify-between">
        <h1 className="text-xl text-slate-900 font-semibold">
          {t('profileKeys.title', { name: profileName || `#${profileID}` })}
        </h1>
      </div>
      {err && <div className="text-red-600 text-sm">{err}</div>}

      <div className="grid grid-cols-3 gap-3">
        <Stat label={t('profileKeys.stat.total')} value={totals.count.toString()} />
        <Stat label={t('profileKeys.stat.used')} value={`$${totals.usedUSD.toFixed(2)}`} />
        <Stat label={t('profileKeys.stat.quota')} value={`$${totals.quotaUSD.toFixed(2)}`} />
      </div>

      <div className="card overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr>
              <th className="th">{t('profileKeys.col.channelId')}</th>
              <th className="th">{t('profileKeys.col.name')}</th>
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
            {rows.map((r) => {
              const usedUsd = r.used_usd ?? 0;
              return (
                <tr key={`${r.assigned_profile_id}-${r.remote_channel_id}`}>
                  <td className="td">{r.remote_channel_id}</td>
                  <td className="td">
                    {r.channel_name || <span className="text-slate-400">—</span>}
                  </td>
                  <td className="td">{r.studio || <span className="text-slate-400">—</span>}</td>
                  <td className="td font-mono text-xs">
                    {r.id > 0 ? (
                      <>
                        {r.key_type}
                        <span className="ml-2 px-1.5 py-0.5 rounded bg-blue-50 text-blue-700 border border-blue-100 text-[10px] uppercase tracking-wider">
                          {t('profileKeys.badge.v2')}
                        </span>
                      </>
                    ) : (
                      <span className="text-slate-400 text-[10px] uppercase tracking-wider">
                        {t('profileKeys.badge.legacy')}
                      </span>
                    )}
                  </td>
                  <td className="td font-mono text-xs">
                    {r.key ? <span className="text-amber-700">{r.key}</span> : r.key_masked || <span className="text-slate-400">—</span>}
                    {r.is_dead && <span className="ml-2 text-xs text-red-600">{t('keys.pool.dead')}</span>}
                  </td>
                  <td className="td"><StatusBadge s={r.status} /></td>
                  <td className="td text-right">${usedUsd.toFixed(4)}</td>
                  <td className="td text-right">{r.quota_usd != null ? `$${r.quota_usd.toFixed(2)}` : '—'}</td>
                  <td className="td text-right">
                    {r.quota_usd != null && r.quota_usd > 0
                      ? `${((usedUsd / r.quota_usd) * 100).toFixed(1)}%`
                      : '—'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
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
