// Settings page is a placeholder in M3 — actual system-config knobs
// (pool interval defaults, notification thresholds, TTL days) surface via
// existing V1 endpoints today. This page shows a summary + points to the
// per-profile pool controls on the Profiles page.
export default function Settings() {
  return (
    <div className="p-6 space-y-4">
      <h1 className="text-xl text-slate-100 font-semibold">Settings</h1>
      <div className="card space-y-3">
        <div>
          <div className="text-slate-400 text-sm">Pool throttle</div>
          <div className="text-slate-300 text-sm mt-1">
            Pool interval, batch size, RPM knobs are configured per remote newapi profile.
            Go to <a className="text-blue-400 underline" href="/v2/profiles">Remote Profiles</a>.
          </div>
        </div>
        <div>
          <div className="text-slate-400 text-sm">Awaiting-assignment TTL</div>
          <div className="text-slate-300 text-sm mt-1">
            Pool rows in <span className="font-mono">awaiting_assignment</span> older than
            <span className="font-mono"> RS_POOL_AWAITING_TTL_DAYS</span> (default 30) are pruned hourly.
            Change via server environment variable.
          </div>
        </div>
        <div>
          <div className="text-slate-400 text-sm">Notification thresholds</div>
          <div className="text-slate-300 text-sm mt-1">
            Managed via <span className="font-mono">NOTIFY_HOURS_THRESHOLD</span> and
            <span className="font-mono"> NOTIFY_USD_THRESHOLD</span> environment variables.
          </div>
        </div>
      </div>
    </div>
  );
}
