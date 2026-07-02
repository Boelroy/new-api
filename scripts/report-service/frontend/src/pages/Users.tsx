import { useCallback, useEffect, useState } from 'react'
import Layout from '../components/Layout'
import { api, ROLE_ADMIN, ROLE_STUDIO_OPERATOR, ROLE_SUPER_ADMIN, ROLE_TESTER, ROLE_USER, type AuthMe, type AuthUser } from '../api'

const ROLE_OPTIONS: { value: number; label: string }[] = [
  { value: ROLE_USER, label: 'User (All Keys only)' },
  { value: ROLE_STUDIO_OPERATOR, label: 'Studio Operator (batch-create, locked to bound studio)' },
  { value: ROLE_TESTER, label: 'Tester (Key Tester + Provider Testing only)' },
  { value: ROLE_ADMIN, label: 'Admin (no Profit / Provider Testing)' },
  { value: ROLE_SUPER_ADMIN, label: 'Super Admin (all features)' },
]

function roleLabel(role: number): string {
  return ROLE_OPTIONS.find(o => o.value === role)?.label ?? `Role ${role}`
}

function formatTime(epochSec: number): string {
  if (!epochSec) return '-'
  return new Date(epochSec * 1000).toLocaleString()
}

// Sentinel selected value that triggers the "create new studio" prompt.
// Not a valid studio name (bracketed sentinel that can never match a tag).
const NEW_STUDIO_SENTINEL = '__new_studio__'

// Prompt for a fresh studio name. Returns the trimmed name, or null when
// the user cancelled / entered something invalid.
function askForNewStudio(existing: string[]): string | null {
  const raw = window.prompt('New studio name (letters / digits / . _ -):')
  if (raw === null) return null
  const name = raw.trim()
  if (!name) return null
  if (!/^[A-Za-z0-9._-]+$/.test(name)) {
    alert('Studio name may only contain letters, digits, dot, underscore, or dash.')
    return null
  }
  if (existing.includes(name)) return name // idempotent — already in list
  return name
}

export default function Users() {
  const [me, setMe] = useState<AuthMe | null>(null)
  const [users, setUsers] = useState<AuthUser[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busyId, setBusyId] = useState<number | null>(null)

  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [newRole, setNewRole] = useState<number>(ROLE_USER)
  const [newStudio, setNewStudio] = useState('')
  const [creating, setCreating] = useState(false)

  const [studios, setStudios] = useState<string[]>([])

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [usersRes, studiosRes] = await Promise.all([
        api.listUsers(),
        api.listStudios().catch(() => ({ studios: [] as string[] })),
      ])
      setUsers(usersRes.users)
      setStudios(studiosRes.studios)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'failed to load users')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void (async () => {
      try {
        setMe(await api.getAuthMe())
      } catch {
        setMe(null)
      }
    })()
    void reload()
  }, [reload])

  const handleCreate = async () => {
    setError(null)
    if (!newUsername.trim() || !newPassword) {
      setError('username and password are required')
      return
    }
    setCreating(true)
    try {
      await api.createUser({
        username: newUsername.trim(),
        password: newPassword,
        role: newRole,
        studio: newStudio.trim(),
      })
      setNewUsername('')
      setNewPassword('')
      setNewRole(ROLE_USER)
      setNewStudio('')
      await reload()
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'create failed')
    } finally {
      setCreating(false)
    }
  }

  const handleChangeRole = async (u: AuthUser, role: number) => {
    if (role === u.role) return
    setBusyId(u.id)
    setError(null)
    try {
      await api.updateUser(u.id, { role })
      await reload()
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'update failed')
    } finally {
      setBusyId(null)
    }
  }

  const handleChangeStudio = async (u: AuthUser, studio: string) => {
    if (studio === NEW_STUDIO_SENTINEL) {
      const name = askForNewStudio(studios)
      if (!name) return
      // Optimistically add to the local list so subsequent renders show
      // the new studio even before reload() finishes. The backend already
      // accepts arbitrary studio strings; listStudios() returns the union
      // of channel tags + rs_auth_user.studio values so once we save, it
      // will persist across reloads.
      setStudios(prev => Array.from(new Set([...prev, name])).sort())
      setBusyId(u.id)
      setError(null)
      try {
        await api.updateUser(u.id, { studio: name })
        await reload()
      } catch (e: unknown) {
        setError(e instanceof Error ? e.message : 'update failed')
      } finally {
        setBusyId(null)
      }
      return
    }
    if (studio === u.studio) return
    setBusyId(u.id)
    setError(null)
    try {
      await api.updateUser(u.id, { studio })
      await reload()
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'update failed')
    } finally {
      setBusyId(null)
    }
  }

  const handleSelectNewStudio = (v: string) => {
    if (v === NEW_STUDIO_SENTINEL) {
      const name = askForNewStudio(studios)
      if (name) {
        setStudios(prev => Array.from(new Set([...prev, name])).sort())
        setNewStudio(name)
      }
      return
    }
    setNewStudio(v)
  }

  const handleResetPassword = async (u: AuthUser) => {
    const next = window.prompt(`Enter new password for ${u.username}:`)
    if (!next) return
    setBusyId(u.id)
    setError(null)
    try {
      await api.updateUser(u.id, { password: next })
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'reset failed')
    } finally {
      setBusyId(null)
    }
  }

  const handleDelete = async (u: AuthUser) => {
    if (!window.confirm(`Delete user "${u.username}"? This cannot be undone.`)) return
    setBusyId(u.id)
    setError(null)
    try {
      await api.deleteUser(u.id)
      await reload()
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'delete failed')
    } finally {
      setBusyId(null)
    }
  }

  return (
    <Layout
      title="Users"
      subtitle="Manage report-service login accounts. user → All Keys only; admin → no Profit/Testing; super admin → full access."
    >
      <div className="space-y-6">
        {error && (
          <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
            {error}
          </div>
        )}

        <section className="rounded-lg border border-gray-200 bg-white p-4">
          <h2 className="text-sm font-medium text-gray-900 mb-3">Create user</h2>
          <div className="grid grid-cols-1 md:grid-cols-5 gap-3">
            <input
              className="rounded-md border border-gray-300 px-3 py-2 text-sm"
              placeholder="Username"
              value={newUsername}
              onChange={e => setNewUsername(e.target.value)}
              autoComplete="off"
            />
            <input
              className="rounded-md border border-gray-300 px-3 py-2 text-sm"
              placeholder="Password"
              type="password"
              value={newPassword}
              onChange={e => setNewPassword(e.target.value)}
              autoComplete="new-password"
            />
            <select
              className="rounded-md border border-gray-300 px-3 py-2 text-sm bg-white"
              value={newRole}
              onChange={e => setNewRole(Number(e.target.value))}
            >
              {ROLE_OPTIONS.map(o => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
            <select
              className="rounded-md border border-gray-300 bg-white px-3 py-2 text-sm"
              value={newStudio}
              onChange={e => handleSelectNewStudio(e.target.value)}
            >
              <option value="">Studio: (no access for user-tier)</option>
              {studios.map(s => <option key={s} value={s}>{s}</option>)}
              <option value={NEW_STUDIO_SENTINEL}>+ Create new studio…</option>
            </select>
            <button
              type="button"
              onClick={handleCreate}
              disabled={creating}
              className="rounded-md bg-gray-900 text-white px-3 py-2 text-sm font-medium hover:bg-gray-800 disabled:opacity-50"
            >
              {creating ? 'Creating…' : 'Create'}
            </button>
          </div>
          <p className="text-xs text-gray-400 mt-2">
            Studio binds a User-role or Studio Operator account to channels
            whose tag matches it. Without a studio binding, a User sees no
            channels, and a Studio Operator can't batch-create (returns 400).
            Admin / super admin ignore studio. Add new studios by creating
            channels with that tag in Key Capacity → Batch create.
          </p>
        </section>

        <section className="rounded-lg border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-4 py-3 flex items-center justify-between">
            <h2 className="text-sm font-medium text-gray-900">All users</h2>
            <button
              type="button"
              onClick={reload}
              className="text-xs text-gray-500 hover:text-gray-900"
            >
              Refresh
            </button>
          </div>
          <div className="overflow-x-auto">
            <table className="min-w-full text-sm">
              <thead className="bg-gray-50 text-gray-600">
                <tr>
                  <th className="px-4 py-2 text-left font-medium">ID</th>
                  <th className="px-4 py-2 text-left font-medium">Username</th>
                  <th className="px-4 py-2 text-left font-medium">Role</th>
                  <th className="px-4 py-2 text-left font-medium">Studio</th>
                  <th className="px-4 py-2 text-left font-medium">Created</th>
                  <th className="px-4 py-2 text-left font-medium">Updated</th>
                  <th className="px-4 py-2 text-right font-medium">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {loading ? (
                  <tr><td colSpan={7} className="px-4 py-6 text-center text-gray-400">Loading…</td></tr>
                ) : users.length === 0 ? (
                  <tr><td colSpan={7} className="px-4 py-6 text-center text-gray-400">No users yet.</td></tr>
                ) : users.map(u => {
                  const isSelf = me?.user_id === u.id
                  const disabled = busyId === u.id
                  return (
                    <tr key={u.id} className={disabled ? 'opacity-50' : ''}>
                      <td className="px-4 py-2 text-gray-500">{u.id}</td>
                      <td className="px-4 py-2 font-medium text-gray-900">
                        {u.username}{isSelf && <span className="ml-2 text-xs text-gray-400">(you)</span>}
                      </td>
                      <td className="px-4 py-2">
                        <select
                          className="rounded-md border border-gray-300 bg-white px-2 py-1 text-xs"
                          value={u.role}
                          disabled={disabled}
                          onChange={e => void handleChangeRole(u, Number(e.target.value))}
                          title={roleLabel(u.role)}
                        >
                          {ROLE_OPTIONS.map(o => (
                            <option key={o.value} value={o.value}>{o.label}</option>
                          ))}
                        </select>
                      </td>
                      <td className="px-4 py-2">
                        <select
                          className="rounded-md border border-gray-300 bg-white px-2 py-1 text-xs w-full min-w-[8rem]"
                          value={u.studio}
                          disabled={disabled}
                          onChange={e => void handleChangeStudio(u, e.target.value)}
                        >
                          <option value="">(no access)</option>
                          {/* Preserve current value as an option even when not in
                              the channel-tag list, so a freshly-bound studio
                              that no channel uses yet still displays here. */}
                          {u.studio && !studios.includes(u.studio) && (
                            <option value={u.studio}>{u.studio}</option>
                          )}
                          {studios.map(s => <option key={s} value={s}>{s}</option>)}
                          <option value={NEW_STUDIO_SENTINEL}>+ Create new studio…</option>
                        </select>
                      </td>
                      <td className="px-4 py-2 text-gray-500">{formatTime(u.created_at)}</td>
                      <td className="px-4 py-2 text-gray-500">{formatTime(u.updated_at)}</td>
                      <td className="px-4 py-2 text-right whitespace-nowrap">
                        <button
                          type="button"
                          onClick={() => void handleResetPassword(u)}
                          disabled={disabled}
                          className="text-xs text-gray-600 hover:text-gray-900 px-2"
                        >
                          Reset password
                        </button>
                        <button
                          type="button"
                          onClick={() => void handleDelete(u)}
                          disabled={disabled || isSelf}
                          className="text-xs text-red-600 hover:text-red-700 disabled:text-gray-300 px-2"
                          title={isSelf ? 'Cannot delete yourself' : undefined}
                        >
                          Delete
                        </button>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </Layout>
  )
}
