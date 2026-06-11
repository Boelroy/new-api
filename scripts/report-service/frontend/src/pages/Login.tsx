import { useState, useEffect, FormEvent } from 'react'

export default function Login() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [ssoUrl, setSsoUrl] = useState<string | null>(null)
  const [configLoaded, setConfigLoaded] = useState(false)

  const params = new URLSearchParams(window.location.search)
  const next = params.get('next') || '/'

  useEffect(() => {
    fetch('/api/auth/config')
      .then(r => r.json())
      .then(d => { setSsoUrl(d.sso_url || null) })
      .catch(() => {})
      .finally(() => setConfigLoaded(true))
  }, [])

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setLoading(true)
    setError('')
    try {
      const res = await fetch('/api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      })
      if (res.ok) {
        window.location.href = next
      } else {
        setError('用户名或密码错误')
      }
    } catch {
      setError('网络错误')
    } finally {
      setLoading(false)
    }
  }

  if (!configLoaded) return null

  return (
    <div className="min-h-screen bg-gray-100 flex items-center justify-center">
      <div className="bg-white border border-gray-200 rounded-xl p-9 w-80 shadow-sm">
        <h1 className="text-lg font-semibold text-center mb-6 tracking-tight">Report Service</h1>

        {ssoUrl && (
          <>
            <a
              href={ssoUrl + `?redirect=${encodeURIComponent(window.location.origin + next)}`}
              className="flex items-center justify-center w-full bg-gray-900 text-white rounded-md py-2 text-sm font-medium hover:opacity-85 mb-4"
            >
              使用主服务账号登录
            </a>
            <div className="flex items-center gap-2 mb-4">
              <div className="flex-1 h-px bg-gray-200" />
              <span className="text-xs text-gray-400">或使用管理员账号</span>
              <div className="flex-1 h-px bg-gray-200" />
            </div>
          </>
        )}

        {error && (
          <div className="bg-red-50 text-red-700 text-sm rounded-lg px-3 py-2 mb-4 text-center">{error}</div>
        )}
        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <label className="block text-xs font-medium text-gray-500 mb-1">用户名</label>
            <input
              type="text"
              value={username}
              onChange={e => setUsername(e.target.value)}
              required
              autoComplete="username"
              className="w-full border border-gray-200 rounded-md px-3 py-2 text-sm bg-gray-50 focus:outline-none focus:border-gray-900"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-500 mb-1">密码</label>
            <input
              type="password"
              value={password}
              onChange={e => setPassword(e.target.value)}
              required
              autoComplete="current-password"
              className="w-full border border-gray-200 rounded-md px-3 py-2 text-sm bg-gray-50 focus:outline-none focus:border-gray-900"
            />
          </div>
          <button
            type="submit"
            disabled={loading}
            className="w-full bg-gray-900 text-white rounded-md py-2 text-sm font-medium hover:opacity-85 disabled:opacity-50"
          >
            {loading ? '登录中...' : '登录'}
          </button>
        </form>
      </div>
    </div>
  )
}
