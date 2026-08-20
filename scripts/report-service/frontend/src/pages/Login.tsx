import { useState, useEffect, FormEvent } from 'react'
import { withBase } from '../basePath'
import { Button } from '../components/ui'

export default function Login() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [ssoUrl, setSsoUrl] = useState<string | null>(null)
  const [configLoaded, setConfigLoaded] = useState(false)

  const params = new URLSearchParams(window.location.search)
  const next = params.get('next') || withBase('/')

  useEffect(() => {
    fetch(withBase('/api/auth/config'))
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
      const res = await fetch(withBase('/api/login'), {
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
    <div className="min-h-screen bg-canvas flex items-center justify-center px-4">
      <div className="card corner-marks relative overflow-hidden p-9 w-full max-w-[340px] shadow-card">
        <div className="absolute inset-0 bg-grid bg-grid opacity-[0.35] pointer-events-none" />
        <div className="relative">
          <div className="flex flex-col items-center mb-7">
            <div className="w-10 h-10 rounded-md bg-brand text-white flex items-center justify-center text-base font-bold tracking-tight mb-3">R</div>
            <h1 className="display text-xl">Report Service</h1>
            <span className="mono-label mt-2 block">Admin Console</span>
          </div>

          {ssoUrl && (
            <>
              <a
                href={ssoUrl + `?redirect=${encodeURIComponent(window.location.origin + withBase('/api/auth/callback'))}`}
                className="flex items-center justify-center w-full bg-brand text-white rounded-lg py-2.5 text-sm font-medium tracking-display shadow-[0_1px_2px_rgba(11,16,32,0.12)] hover:bg-brand-700 mb-4"
              >
                使用主服务账号登录
              </a>
              <div className="flex items-center gap-2 mb-4">
                <div className="flex-1 h-px bg-border" />
                <span className="text-xs text-secondary">或使用管理员账号</span>
                <div className="flex-1 h-px bg-border" />
              </div>
            </>
          )}

          {error && (
            <div className="bg-red-50 text-red-700 text-sm rounded-lg px-3 py-2 mb-4 text-center">{error}</div>
          )}
          <form onSubmit={handleSubmit} className="space-y-3">
            <div>
              <label className="mono-label mb-1.5 block">用户名</label>
              <input
                type="text"
                value={username}
                onChange={e => setUsername(e.target.value)}
                required
                autoComplete="username"
                className="w-full border border-border rounded-lg px-3 py-2 text-sm bg-canvas focus:outline-none focus:border-brand focus:ring-2 focus:ring-outline/40"
              />
            </div>
            <div>
              <label className="mono-label mb-1.5 block">密码</label>
              <input
                type="password"
                value={password}
                onChange={e => setPassword(e.target.value)}
                required
                autoComplete="current-password"
                className="w-full border border-border rounded-lg px-3 py-2 text-sm bg-canvas focus:outline-none focus:border-brand focus:ring-2 focus:ring-outline/40"
              />
            </div>
            <Button type="submit" disabled={loading} className="w-full">
              {loading ? '登录中...' : '登录'}
            </Button>
          </form>
        </div>
      </div>
    </div>
  )
}
