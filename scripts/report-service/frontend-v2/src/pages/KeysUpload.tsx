import { useEffect, useMemo, useState } from 'react';
import { api, ProfileSlim } from '../api';
import { useAuth } from '../auth';
import { useI18n } from '../i18n';

// Default model list studio operators upload against when going to the
// pool. Direct-to-newapi uploads override these with the profile's own
// `default_models` and can't be edited (see below).
const DEFAULT_MODELS = [
  'claude-sonnet-5',
  'claude-opus-4-8',
  'claude-opus-4-7',
  'claude-sonnet-4-6',
  'claude-opus-4-6',
  'claude-haiku-4-5-20251001',
  'claude-sonnet-4-5-20250929',
  'claude-opus-4-5-20251101',
  'claude-fable-5',
  'claude-sonnet-5',
].join(',');

const DEFAULT_GROUP = 'default';

// Parsed CSV row from the keys textarea.
interface ParsedRow {
  key: string;
  quota?: number;       // undefined = no cost specified
  invalidCost?: boolean;
  duplicate?: boolean;
}

function parseKeysInput(raw: string, keyType: 'regular' | 'trial_5usd'): ParsedRow[] {
  const lines = raw.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  const seen = new Map<string, number>();
  return lines.map((line) => {
    const parts = line.split(',').map((p) => p.trim());
    const key = parts[0] ?? '';
    const row: ParsedRow = { key };
    if (keyType === 'regular' && parts.length >= 2 && parts[1] !== '') {
      const c = parseFloat(parts[1]);
      if (isFinite(c) && c > 0) row.quota = c;
      else row.invalidCost = true;
    }
    // trial_5usd ignores per-row cost — backend applies $5.
    const prior = seen.get(key);
    if (prior !== undefined) row.duplicate = true;
    else seen.set(key, 1);
    return row;
  });
}

// Dual-mode: pool_only (default) OR direct_newapi. Studio is locked to the
// caller's JWT-bound studio for own_studio-scoped roles; admin with
// any_studio can set it explicitly via ?studio=X (not exposed in UI here —
// admin uploads typically happen via V1).
export default function KeysUpload() {
  const { me } = useAuth();
  const { t } = useI18n();
  const [keyType, setKeyType] = useState<'regular' | 'trial_5usd'>('regular');
  const [mode, setMode] = useState<'pool_only' | 'direct_newapi'>('pool_only');
  const [profileID, setProfileID] = useState<number | null>(null);
  const [profiles, setProfiles] = useState<ProfileSlim[]>([]);
  const [models, setModels] = useState(DEFAULT_MODELS);
  const [group, setGroup] = useState(DEFAULT_GROUP);
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

  const selectedProfile = mode === 'direct_newapi' && profileID
    ? profiles.find((p) => p.id === profileID) ?? null
    : null;

  // In direct_newapi mode the profile owns models + group. Fall back to
  // the pool defaults only when the profile hasn't configured them.
  const effectiveModels = selectedProfile
    ? (selectedProfile.default_models || DEFAULT_MODELS)
    : models;
  const effectiveGroup = selectedProfile
    ? (selectedProfile.default_group || DEFAULT_GROUP)
    : group;

  const parsed = useMemo(() => parseKeysInput(raw, keyType), [raw, keyType]);
  const validKeys = parsed.filter((r) => r.key && !r.invalidCost && !r.duplicate);
  const invalidCostCount = parsed.filter((r) => r.invalidCost).length;
  const totalCost = useMemo(() => {
    if (keyType === 'trial_5usd') return parsed.length * 5;
    return parsed.reduce((s, r) => s + (r.quota ?? 0), 0);
  }, [parsed, keyType]);

  const submit = async () => {
    setBusy(true);
    try {
      const body = {
        key_type: keyType,
        target_mode: mode,
        target_profile_id: mode === 'direct_newapi' ? profileID : undefined,
        models: effectiveModels,
        group: effectiveGroup,
        name_prefix: prefix,
        keys: validKeys.map((r) => ({ key: r.key, quota_usd: r.quota })),
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
      <h1 className="text-xl text-slate-900 font-semibold">{t('keys.upload.title')}</h1>
      {me?.studio ? (
        <p className="text-sm text-slate-500">{t('keys.upload.studioLocked', { studio: me.studio })}</p>
      ) : (
        <p className="text-sm text-amber-600">{t('keys.upload.noStudio')}</p>
      )}

      <div className="card space-y-4">
        <div>
          <div className="text-xs text-slate-500 mb-1">{t('keys.upload.keyType')}</div>
          <div className="flex gap-4 text-sm">
            <label><input type="radio" name="kt" checked={keyType === 'regular'} onChange={() => setKeyType('regular')} /> regular</label>
            <label><input type="radio" name="kt" checked={keyType === 'trial_5usd'} onChange={() => setKeyType('trial_5usd')} /> trial_5usd (5 USD)</label>
          </div>
        </div>
        <div>
          <div className="text-xs text-slate-500 mb-1">{t('keys.upload.target')}</div>
          <div className="flex gap-4 text-sm">
            <label><input type="radio" name="tm" checked={mode === 'pool_only'} onChange={() => setMode('pool_only')} /> {t('keys.upload.target.pool')}</label>
            <label><input type="radio" name="tm" checked={mode === 'direct_newapi'} onChange={() => setMode('direct_newapi')} /> {t('keys.upload.target.direct')}</label>
          </div>
        </div>
        {mode === 'direct_newapi' && (
          <div>
            <div className="text-xs text-slate-500 mb-1">{t('keys.upload.profile')}</div>
            <select className="input" value={profileID ?? ''} onChange={(e) => setProfileID(parseInt(e.target.value, 10) || null)}>
              <option value="">{t('keys.upload.pickProfile')}</option>
              {profiles.map((p) => (
                <option key={p.id} value={p.id} disabled={p.accepts_studio === false}>
                  {p.name}{p.accepts_studio === false ? ' ' + t('keys.upload.notAccepting') : ''}
                </option>
              ))}
            </select>
          </div>
        )}
        <div className="grid grid-cols-3 gap-3">
          <div>
            <div className="text-xs text-slate-500 mb-1 flex items-center gap-2">
              {t('keys.upload.models')}
              {selectedProfile && (
                <span className="text-[10px] text-slate-400 uppercase tracking-wider">
                  {t('keys.upload.lockedFromProfile')}
                </span>
              )}
            </div>
            <input
              className="input disabled:bg-slate-100 disabled:text-slate-500"
              value={effectiveModels}
              onChange={(e) => setModels(e.target.value)}
              disabled={!!selectedProfile}
            />
          </div>
          <div>
            <div className="text-xs text-slate-500 mb-1 flex items-center gap-2">
              {t('keys.upload.group')}
              {selectedProfile && (
                <span className="text-[10px] text-slate-400 uppercase tracking-wider">
                  {t('keys.upload.lockedFromProfile')}
                </span>
              )}
            </div>
            <input
              className="input disabled:bg-slate-100 disabled:text-slate-500"
              value={effectiveGroup}
              onChange={(e) => setGroup(e.target.value)}
              disabled={!!selectedProfile}
            />
          </div>
          <div>
            <div className="text-xs text-slate-500 mb-1">{t('keys.upload.namePrefix')}</div>
            <input className="input" value={prefix} onChange={(e) => setPrefix(e.target.value)} placeholder="alpha" />
          </div>
        </div>
        <div>
          <div className="text-xs text-slate-500 mb-1">
            {t('keys.upload.keys', { count: parsed.length })}
          </div>
          <textarea
            className="input h-40 font-mono text-xs"
            value={raw}
            onChange={(e) => setRaw(e.target.value)}
            placeholder={keyType === 'trial_5usd' ? 'sk-xxx' : 'sk-xxx,5.00'}
          />
          <div className="text-[11px] text-slate-400 mt-1">
            {keyType === 'trial_5usd' ? t('keys.upload.keysFormatHintTrial') : t('keys.upload.keysFormatHint')}
          </div>
        </div>

        {parsed.length > 0 && (
          <div>
            <div className="flex items-center justify-between mb-2">
              <div className="text-sm text-slate-700 font-medium">{t('keys.upload.preview')}</div>
              <div className="text-sm text-slate-500">
                {t('keys.upload.preview.totalCost', { amount: totalCost.toFixed(2) })}
              </div>
            </div>
            {invalidCostCount > 0 && (
              <div className="text-xs text-red-600 mb-2">
                {t('keys.upload.preview.rowsBadCost', { n: invalidCostCount })}
              </div>
            )}
            <div className="border border-slate-200 rounded overflow-x-auto max-h-72 overflow-y-auto">
              <table className="w-full text-xs">
                <thead className="sticky top-0">
                  <tr>
                    <th className="th">{t('keys.upload.preview.col.row')}</th>
                    <th className="th">{t('keys.upload.preview.col.key')}</th>
                    <th className="th text-right">{t('keys.upload.preview.col.cost')}</th>
                    <th className="th">{t('keys.upload.preview.col.note')}</th>
                  </tr>
                </thead>
                <tbody>
                  {parsed.map((r, i) => {
                    const cost = keyType === 'trial_5usd' ? 5 : r.quota;
                    return (
                      <tr key={i}>
                        <td className="td">{i + 1}</td>
                        <td className="td font-mono">{r.key.length > 8 ? '…' + r.key.slice(-8) : r.key}</td>
                        <td className="td text-right font-mono">
                          {cost !== undefined ? cost.toFixed(2) : <span className="text-slate-400">—</span>}
                        </td>
                        <td className="td text-slate-500">
                          {r.invalidCost && <span className="text-red-600">{t('keys.upload.preview.invalidCost')}</span>}
                          {r.duplicate && <span className="text-amber-600">{t('keys.upload.preview.duplicate')}</span>}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        )}

        <div className="flex justify-end">
          <button
            className="btn btn-primary"
            onClick={submit}
            disabled={
              busy ||
              validKeys.length === 0 ||
              invalidCostCount > 0 ||
              (mode === 'direct_newapi' && !profileID)
            }
          >
            {busy
              ? t('keys.upload.submitting')
              : t('keys.upload.submit', { count: validKeys.length, plural: validKeys.length === 1 ? '' : 's' })}
          </button>
        </div>
      </div>

      {results.length > 0 && (
        <div className="card">
          <h2 className="text-slate-900 font-semibold mb-2">{t('keys.upload.results')}</h2>
          <table className="w-full text-sm">
            <thead>
              <tr><th className="th">{t('keys.upload.col.row')}</th><th className="th">{t('keys.upload.col.status')}</th><th className="th">{t('keys.upload.col.error')}</th></tr>
            </thead>
            <tbody>
              {results.map((r, i) => (
                <tr key={i}>
                  <td className="td">{r.row}</td>
                  <td className="td">
                    <span className={r.status === 'error' || r.status === 'duplicate' ? 'text-red-600' : 'text-green-700'}>{r.status}</span>
                  </td>
                  <td className="td text-xs text-slate-500">{r.error ?? ''}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
