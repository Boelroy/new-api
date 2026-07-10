import { createContext, useContext, useEffect, useState, ReactNode } from 'react';
import { api, MeResponse } from './api';

interface AuthCtx {
  me: MeResponse | null;
  loading: boolean;
  hasPerm: (action: string, scope?: string) => boolean;
  refresh: () => Promise<void>;
  logout: () => Promise<void>;
}

const Ctx = createContext<AuthCtx | null>(null);

// scopeSubsumers mirrors the backend rule: a broader scope subsumes a
// narrower one. Keep the arrays in sync with v2_rbac.go scopeSubsumers.
const scopeChain: Record<string, string[]> = {
  global: ['global'],
  any_studio: ['any_studio', 'global'],
  own_studio: ['own_studio', 'any_studio', 'global'],
  self: ['self', 'own_studio', 'any_studio', 'global'],
};

export function AuthProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<MeResponse | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = async () => {
    try {
      const m = await api.me();
      setMe(m);
    } catch {
      setMe(null);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    refresh();
  }, []);

  const hasPerm = (action: string, scope: string = 'global') => {
    if (!me) return false;
    if (me.is_super) return true;
    const chain = scopeChain[scope] ?? [scope];
    return chain.some((s) => me.permissions.includes(`${action}@${s}`));
  };

  const logout = async () => {
    try { await api.logout(); } catch {}
    setMe(null);
    location.href = '/v2/login';
  };

  return <Ctx.Provider value={{ me, loading, hasPerm, refresh, logout }}>{children}</Ctx.Provider>;
}

export function useAuth() {
  const c = useContext(Ctx);
  if (!c) throw new Error('useAuth outside AuthProvider');
  return c;
}
