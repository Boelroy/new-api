// Thin fetch wrapper for /api/v2/*.
// - Cookies are same-origin, sent automatically.
// - 401 triggers a redirect to /v2/login (client-side routing).

export interface MeResponse {
  user_id: number;
  username: string;
  studio: string;
  max_role_level: number;
  is_super: boolean;
  permissions: string[];
}

export interface Permission {
  action: string;
  scope: string;
}

export interface Role {
  id: number;
  name: string;
  display_name: string;
  level: number;
  is_builtin: boolean;
  permissions?: Permission[];
  user_count?: number;
  created_at: number;
  updated_at: number;
}

export interface UserRow {
  id: number;
  username: string;
  role: number;
  studio: string;
  status: number;
  disabled_at: number;
  created_at: number;
  updated_at: number;
  roles: number[];
  role_names: string[];
  max_level: number;
}

export interface KeyPoolRow {
  id: number;
  studio: string;
  uploaded_by: number;
  key_type: string;
  key_masked: string;
  key?: string;
  quota_usd: number | null;
  models: string;
  name_prefix: string;
  group: string;
  status: string;
  assigned_profile_id: number;
  remote_channel_id: number;
  remote_status: number;
  is_dead: boolean;
  failed_reason: string;
  created_at: number;
  updated_at: number;
}

export interface UsageRow extends KeyPoolRow {
  used_quota_raw: number;
  used_usd: number;
}

export interface ProfileSlim {
  id: number;
  name: string;
  default_models: string;
  default_group: string;
  has_access_token: boolean;
  accepts_studio?: boolean;
}

export interface ProfileFull extends ProfileSlim {
  host: string;
  user_id: number;
  pool_interval_sec: number;
  pool_batch_size: number;
  auto_mode: boolean;
  rpm_base: number;
  rpm_min: number;
  created_at: number;
  updated_at: number;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) {
    if (!location.pathname.endsWith('/login')) {
      location.href = '/v2/login';
    }
    throw new Error('unauthorized');
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const msg = (data && (data.error || data.message)) || `HTTP ${res.status}`;
    throw new Error(msg);
  }
  return data as T;
}

export const api = {
  login: (username: string, password: string) =>
    request<{ ok: boolean; role: number; username: string; studio: string }>('POST', '/api/login', { username, password }),
  logout: () => request<{ ok: boolean }>('POST', '/api/logout'),
  me: () => request<MeResponse>('GET', '/api/v2/me'),
  permissionsCatalog: () =>
    request<{ actions: { group: string; action: string; label: string }[]; scopes: { scope: string; label: string }[] }>(
      'GET',
      '/api/v2/permissions'
    ),
  studios: () => request<{ studios: string[] }>('GET', '/api/v2/studios'),

  listRoles: () => request<{ roles: Role[] }>('GET', '/api/v2/roles'),
  createRole: (r: { name: string; display_name: string; level: number; permissions: Permission[] }) =>
    request<Role>('POST', '/api/v2/roles', r),
  updateRole: (id: number, patch: { display_name?: string; level?: number; permissions?: Permission[] }) =>
    request<Role>('PATCH', `/api/v2/roles/${id}`, patch),
  deleteRole: (id: number) => request<{ ok: boolean }>('DELETE', `/api/v2/roles/${id}`),

  listUsers: () => request<{ users: UserRow[] }>('GET', '/api/v2/users'),
  createUser: (b: { username: string; password: string; studio: string; roles: number[] }) =>
    request<UserRow>('POST', '/api/v2/users', b),
  updateUser: (id: number, patch: { password?: string; studio?: string }) =>
    request<UserRow>('PATCH', `/api/v2/users/${id}`, patch),
  resetPassword: (id: number, password: string) =>
    request<{ ok: boolean }>('POST', `/api/v2/users/${id}/reset-password`, { password }),
  disableUser: (id: number) => request<{ ok: boolean }>('POST', `/api/v2/users/${id}/disable`),
  enableUser: (id: number) => request<{ ok: boolean }>('POST', `/api/v2/users/${id}/enable`),
  deleteUser: (id: number) => request<{ ok: boolean }>('DELETE', `/api/v2/users/${id}`),
  assignRoles: (id: number, roles: number[]) =>
    request<UserRow>('POST', `/api/v2/users/${id}/roles`, { roles }),

  uploadKeys: (b: unknown) => request<{ results: { row: number; status: string; error?: string }[] }>('POST', '/api/v2/keys/pool', b),
  listPool: (status = 'awaiting_assignment,failed') =>
    request<{ keys: KeyPoolRow[] }>('GET', `/api/v2/keys/pool?status=${encodeURIComponent(status)}`),
  listActive: () => request<{ keys: KeyPoolRow[] }>('GET', '/api/v2/keys/active'),
  assignPool: (key_ids: number[], profile_id: number) =>
    request<{ results: { pool_id: number; status: string; error?: string }[] }>('POST', '/api/v2/keys/pool/assign', { key_ids, profile_id }),
  rebindKey: (pool_id: number, new_profile_id: number) =>
    request<{ ok: boolean }>('POST', '/api/v2/keys/rebind', { pool_id, new_profile_id }),
  disableKey: (pool_id: number) =>
    request<{ ok: boolean }>('POST', '/api/v2/keys/disable', { pool_id }),
  deletePool: (id: number) => request<{ ok: boolean }>('DELETE', `/api/v2/keys/pool/${id}`),

  usage: (params: URLSearchParams) => request<{ rows: UsageRow[] }>('GET', `/api/v2/usage?${params.toString()}`),

  listProfilesSlim: () => request<{ profiles: ProfileSlim[] }>('GET', '/api/v2/profiles'),
  listProfilesFull: () => request<{ profiles: ProfileFull[] }>('GET', '/api/v2/profiles'),
  createProfile: (b: unknown) => request<{ id: number }>('POST', '/api/v2/profiles', b),
  updateProfile: (id: number, b: unknown) => request<{ ok: boolean }>('PATCH', `/api/v2/profiles/${id}`, b),
  deleteProfile: (id: number) => request<{ ok: boolean }>('DELETE', `/api/v2/profiles/${id}`),
};
