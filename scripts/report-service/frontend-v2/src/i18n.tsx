import { createContext, useContext, useEffect, useState, ReactNode } from 'react';
import en from './locales/en';
import zh from './locales/zh';

// Lightweight i18n: two locale bundles, plain string keys.
// - Falls back to the key itself when a translation is missing so we can
//   ship new UI without blocking on translations landing.
// - Default is Chinese (zh) — the deployment is Chinese-first. Users can
//   flip to en via the header switcher; choice persists in localStorage.

export type Lang = 'zh' | 'en';

const BUNDLES: Record<Lang, Record<string, string>> = { en, zh };
const STORAGE_KEY = 'v2.lang';

interface I18nCtx {
  lang: Lang;
  setLang: (l: Lang) => void;
  t: (key: string, vars?: Record<string, string | number>) => string;
}

const Ctx = createContext<I18nCtx | null>(null);

function initialLang(): Lang {
  if (typeof localStorage !== 'undefined') {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored === 'zh' || stored === 'en') return stored;
  }
  return 'zh';
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(initialLang);

  useEffect(() => {
    document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
  }, [lang]);

  const setLang = (l: Lang) => {
    setLangState(l);
    localStorage.setItem(STORAGE_KEY, l);
  };

  const t = (key: string, vars?: Record<string, string | number>) => {
    const bundle = BUNDLES[lang];
    let s = bundle[key] ?? BUNDLES.en[key] ?? key;
    if (vars) {
      for (const [k, v] of Object.entries(vars)) {
        s = s.replace(new RegExp(`\\{${k}\\}`, 'g'), String(v));
      }
    }
    return s;
  };

  return <Ctx.Provider value={{ lang, setLang, t }}>{children}</Ctx.Provider>;
}

export function useI18n() {
  const c = useContext(Ctx);
  if (!c) throw new Error('useI18n outside I18nProvider');
  return c;
}

// Bare-hook shortcut when a component only needs t(). Callers still pay
// the same context lookup — this is purely readability sugar.
export function useT() {
  return useI18n().t;
}
