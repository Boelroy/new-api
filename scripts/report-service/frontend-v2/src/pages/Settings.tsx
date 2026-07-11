import { useI18n } from '../i18n';

// Settings 是 M3 的占位页 —— 实际调节大部分走环境变量或者远程站点页面。
// 这里只做说明索引。
export default function Settings() {
  const { t } = useI18n();
  return (
    <div className="p-6 space-y-4">
      <h1 className="text-xl text-slate-100 font-semibold">{t('settings.title')}</h1>
      <div className="card space-y-3">
        <div>
          <div className="text-slate-400 text-sm">{t('settings.poolThrottle')}</div>
          <div className="text-slate-300 text-sm mt-1">
            {t('settings.poolThrottle.desc')}
          </div>
        </div>
        <div>
          <div className="text-slate-400 text-sm">{t('settings.ttl')}</div>
          <div className="text-slate-300 text-sm mt-1">
            {t('settings.ttl.desc')}
          </div>
        </div>
        <div>
          <div className="text-slate-400 text-sm">{t('settings.notify')}</div>
          <div className="text-slate-300 text-sm mt-1">
            {t('settings.notify.desc')}
          </div>
        </div>
      </div>
    </div>
  );
}
