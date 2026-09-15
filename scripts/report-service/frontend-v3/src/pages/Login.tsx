import { useState, type FormEvent } from 'react'
import { ArrowUpRight } from 'lucide-react'
import { withBase } from '../basePath'

// Editorial "AI Gateway" login, matching the blueprint reference: warm paper
// canvas, Songti serif hero on the left, a login card on the right, dark-green
// accent. Deliberately self-contained (its own palette, light-only) — it does
// NOT use the app's new-api design tokens. Username/password only; the
// main-service SSO button was removed per request.

const SERIF = '"Songti SC", STSong, "Noto Serif SC", Georgia, serif'
const SANS = '"Avenir Next", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif'

export default function Login() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [showPw, setShowPw] = useState(false)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const params = new URLSearchParams(window.location.search)
  const next = params.get('next') || '/v3/'

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
        setError('账号或密码错误')
      }
    } catch {
      setError('网络错误，请重试')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex min-h-svh flex-col bg-[#F2F4EF] text-[#1b1c1e]" style={{ fontFamily: SANS }}>
      {/* Header */}
      <header className="border-b border-[#e1e2dc]">
        <div className="mx-auto flex w-full max-w-[1280px] items-center justify-between px-6 py-4 sm:px-10">
          <div className="flex items-baseline gap-2">
            <span className="text-sm font-bold tracking-[0.18em]">BLUEPRINT</span>
            <span className="text-sm text-[#8a8d84]">/ AI Gateway</span>
          </div>
          <div className="flex items-center gap-2 text-xs text-[#8a8d84]">
            <span>语言</span>
            <span className="rounded-[4px] bg-[#202126] px-3 py-1.5 text-[#e9e7e1]">中文</span>
          </div>
        </div>
      </header>

      {/* Body */}
      <main className="mx-auto grid w-full max-w-[1280px] flex-1 grid-cols-1 items-center gap-14 px-6 py-14 sm:px-10 lg:grid-cols-2 lg:gap-20">
        {/* Left: editorial hero */}
        <section className="order-2 lg:order-1">
          <div className="mb-6 flex items-center gap-2 text-[13px] text-[#284F38]">
            <span className="inline-block size-1.5 rounded-full bg-[#284F38]" />
            <span>AI 网关监控</span>
          </div>
          <h1 className="text-[44px] leading-[1.08] sm:text-[56px] lg:text-[64px]" style={{ fontFamily: SERIF, fontWeight: 500 }}>
            每次调用，
            <br />
            清晰可见。
          </h1>
          <p className="mt-6 max-w-[420px] text-[15px] leading-relaxed text-[#6f726a]">
            查看请求与回复，追踪模型用量和耗时，找到每个结果背后的调用细节。
          </p>

          <div className="mt-10 max-w-[440px] border-t border-[#e1e2dc] pt-5">
            <div className="mb-3 font-mono text-[11px] uppercase tracking-[0.16em] text-[#9a9d93]">Request</div>
            <div className="flex items-center gap-3 font-mono text-[13px]">
              <span className="rounded-[3px] border border-[#cbd5ca] px-1.5 py-0.5 text-[11px] font-semibold text-[#284F38]">LLM</span>
              <span className="text-[#43463f]">chat.completions</span>
              <span className="ml-auto text-[#9a9d93]">200</span>
            </div>
            <div className="mt-3 space-y-1.5">
              <div className="h-1.5 w-[86%] rounded-full bg-[#dfe2d9]" />
              <div className="h-1.5 w-[62%] rounded-full bg-[#e7e9e1]" />
            </div>
            <div className="mt-4 flex items-center justify-between font-mono text-[11px] uppercase tracking-[0.14em] text-[#9a9d93]">
              <span>Input → Output</span>
              <span>Trace / Usage / Latency</span>
            </div>
          </div>
        </section>

        {/* Right: login card */}
        <section className="order-1 lg:order-2">
          <div className="mx-auto w-full max-w-[420px] rounded-[8px] border border-[#e5e6e0] bg-white p-8 shadow-[0_1px_2px_rgba(20,24,26,0.04)] sm:p-9">
            <div className="mb-1.5 font-mono text-[11px] uppercase tracking-[0.16em] text-[#9a9d93]">监控访问</div>
            <h2 className="text-[26px] font-bold tracking-tight">登录网关</h2>
            <p className="mt-1.5 text-[13px] text-[#6f726a]">使用你的网关账号，继续查看调用记录。</p>

            {error && (
              <div className="mt-5 rounded-[5px] border border-[#e3c7c7] bg-[#fbf1f1] px-3 py-2 text-[13px] text-[#a3423a]">
                {error}
              </div>
            )}

            <form onSubmit={handleSubmit} className="mt-6 space-y-4">
              <div>
                <label className="mb-1.5 block text-[13px] font-semibold text-[#33362f]">账号</label>
                <input
                  type="text"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  required
                  autoComplete="username"
                  placeholder="gateway"
                  className="h-11 w-full rounded-[5px] border border-[#cbd5ca] bg-[#fafbf9] px-3 text-[15px] text-[#1b1c1e] placeholder:text-[#a6a9a0] outline-none transition-colors focus:border-[#284F38] focus:ring-2 focus:ring-[#284F38]/15"
                />
              </div>
              <div>
                <label className="mb-1.5 block text-[13px] font-semibold text-[#33362f]">密码</label>
                <div className="relative">
                  <input
                    type={showPw ? 'text' : 'password'}
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    required
                    autoComplete="current-password"
                    className="h-11 w-full rounded-[5px] border border-[#cbd5ca] bg-[#fafbf9] pl-3 pr-16 text-[15px] text-[#1b1c1e] outline-none transition-colors focus:border-[#284F38] focus:ring-2 focus:ring-[#284F38]/15"
                  />
                  <button
                    type="button"
                    onClick={() => setShowPw((v) => !v)}
                    className="absolute right-3 top-1/2 -translate-y-1/2 text-[13px] text-[#6f726a] hover:text-[#284F38]"
                  >
                    {showPw ? '隐藏' : '显示'}
                  </button>
                </div>
              </div>
              <button
                type="submit"
                disabled={loading}
                className="flex h-12 w-full items-center justify-between rounded-[5px] bg-[#284F38] px-4 text-[15px] font-medium text-white transition-colors hover:bg-[#21402e] disabled:opacity-60"
              >
                <span>{loading ? '登录中…' : '登录'}</span>
                <ArrowUpRight className="size-[18px]" />
              </button>
            </form>

            <div className="mt-6 border-t border-[#ececE6] pt-4 text-[12px] text-[#9a9d93]">
              调用记录仅向已授权账号开放。
            </div>
          </div>
        </section>
      </main>

      {/* Footer */}
      <footer className="border-t border-[#e1e2dc]">
        <div className="mx-auto flex w-full max-w-[1280px] items-center justify-between px-6 py-5 font-mono text-[11px] uppercase tracking-[0.14em] text-[#9a9d93] sm:px-10">
          <span>Blueprint / AI Gateway</span>
          <span className="normal-case tracking-normal">请求、回复与运行细节。</span>
        </div>
      </footer>
    </div>
  )
}
