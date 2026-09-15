import { useState, useEffect, type FormEvent } from 'react'
import { withBase } from '../basePath'
import { Button, Input } from '../components/ui'

export default function Login() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [ssoUrl, setSsoUrl] = useState<string | null>(null)
  const [configLoaded, setConfigLoaded] = useState(false)

  const params = new URLSearchParams(window.location.search)
  const next = params.get('next') || '/v3/'

  useEffect(() => {
    fetch(withBase('/api/auth/config'))
      .then((r) => r.json())
      .then((d) => {
        setSsoUrl(d.sso_url || null)
      })
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
    <div className="flex min-h-svh items-center justify-center bg-background px-4 text-foreground">
      <div className="card w-full max-w-[360px] p-8">
        <div className="mb-7 flex flex-col items-center">
          <div className="mb-3 flex size-10 items-center justify-center rounded-lg bg-primary text-base font-bold text-primary-foreground">
            N
          </div>
          <h1 className="display text-xl">New API</h1>
          <span className="mono-label mt-2 block">Admin Console</span>
        </div>

        {ssoUrl && (
          <>
            <a
              href={ssoUrl + `?redirect=${encodeURIComponent(window.location.origin + withBase('/api/auth/callback'))}`}
              className="mb-4 flex w-full items-center justify-center rounded-lg bg-primary py-2.5 text-sm font-medium text-primary-foreground hover:bg-primary/90"
            >
              使用主服务账号登录
            </a>
            <div className="mb-4 flex items-center gap-2">
              <div className="h-px flex-1 bg-border" />
              <span className="text-xs text-muted-foreground">或使用管理员账号</span>
              <div className="h-px flex-1 bg-border" />
            </div>
          </>
        )}

        {error && (
          <div className="mb-4 rounded-lg bg-destructive/10 px-3 py-2 text-center text-sm text-destructive">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <label className="mono-label mb-1.5 block">用户名</label>
            <Input type="text" value={username} onChange={(e) => setUsername(e.target.value)} required autoComplete="username" />
          </div>
          <div>
            <label className="mono-label mb-1.5 block">密码</label>
            <Input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              autoComplete="current-password"
            />
          </div>
          <Button type="submit" disabled={loading} className="w-full">
            {loading ? '登录中...' : '登录'}
          </Button>
        </form>
      </div>
    </div>
  )
}
