import { useEffect, useRef, useState } from 'react'
import Layout from '../components/Layout'
import { api, EvalStatus } from '../api'

const MODEL_DEFAULTS = [
  'claude-sonnet-4-6',
  'claude-opus-4-7',
  'claude-opus-4-6',
  'claude-haiku-4-5-20251001',
  'claude-sonnet-4-5-20250929',
  'claude-opus-4-5-20251101',
  'claude-3-7-sonnet-20250219',
  'claude-3-5-sonnet-20241022',
]

const POLL_INTERVAL_MS = 2000

function statusColor(s: string) {
  switch (s) {
    case 'ok': return 'bg-emerald-100 text-emerald-800'
    case 'error': return 'bg-rose-100 text-rose-700'
    case 'cancelled': return 'bg-gray-100 text-gray-600'
    case 'running': return 'bg-blue-100 text-blue-700'
    case 'grading': return 'bg-purple-100 text-purple-700'
    default: return 'bg-gray-100 text-gray-500'
  }
}

function downloadText(name: string, text: string) {
  const blob = new Blob([text], { type: 'text/markdown' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 60_000)
}

export default function ProviderEval() {
  const [url, setUrl] = useState('')
  const [key, setKey] = useState('')
  const [model, setModel] = useState(MODEL_DEFAULTS[0])
  const [modelMode, setModelMode] = useState<'preset' | 'custom'>('preset')
  const [customModel, setCustomModel] = useState('')
  const [repeat, setRepeat] = useState(1)
  const [graderAvailable, setGraderAvailable] = useState(false)
  const [runGrader, setRunGrader] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)

  useEffect(() => {
    void (async () => {
      try {
        const cfg = await fetch('/api/auth/config').then(r => r.json())
        const ok = cfg.grader_configured === true
        setGraderAvailable(ok)
        if (ok) setRunGrader(true)
      } catch { /* keep disabled on error */ }
    })()
  }, [])
  const [status, setStatus] = useState<EvalStatus | null>(null)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const logRef = useRef<HTMLPreElement>(null)
  const followRef = useRef(true)

  // Poll the running job until it ends.
  useEffect(() => {
    if (!jobId) return
    let cancelled = false
    const tick = async () => {
      try {
        const s = await api.evalStatus(jobId)
        if (cancelled) return
        setStatus(s)
        if (s.status === 'running' || s.status === 'grading') {
          setTimeout(tick, POLL_INTERVAL_MS)
        }
      } catch (e: any) {
        if (cancelled) return
        setSubmitError('poll failed: ' + (e?.message || String(e)))
      }
    }
    tick()
    return () => { cancelled = true }
  }, [jobId])

  // Auto-scroll the log unless the user manually scrolled up.
  useEffect(() => {
    if (!logRef.current || !followRef.current) return
    logRef.current.scrollTop = logRef.current.scrollHeight
  }, [status?.stderr])

  const onLogScroll = () => {
    const el = logRef.current
    if (!el) return
    followRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  }

  const finalModel = modelMode === 'custom' ? customModel.trim() : model
  const running = status?.status === 'running' || status?.status === 'grading'
  const canRun = url.trim() && key.trim() && finalModel.length > 0 && !running

  const handleStart = async () => {
    setSubmitError(null)
    if (!canRun) return
    try {
      followRef.current = true
      setStatus(null)
      const r = await api.evalStart({
        url: url.trim(),
        key: key.trim(),
        model: finalModel,
        repeat,
        run_grader: graderAvailable && runGrader,
      })
      setJobId(r.job_id)
    } catch (e: any) {
      setSubmitError('启动失败：' + (e?.message || String(e)))
    }
  }

  const handleCancel = async () => {
    if (!jobId) return
    try { await api.evalCancel(jobId) } catch { /* ignore */ }
  }

  const actions = (
    <>
      {running ? (
        <button
          onClick={handleCancel}
          className="bg-rose-600 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85"
        >
          停止评估
        </button>
      ) : (
        <button
          onClick={handleStart}
          disabled={!canRun}
          className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-40 disabled:cursor-not-allowed"
        >
          开始评估 (pass@{repeat})
        </button>
      )}
    </>
  )

  const traceName = status?.trace
    ? `eval-${(new URL(url, location.origin)).host.replace(/[^a-z0-9.-]/gi, '-')}-${finalModel}-${new Date().toISOString().slice(0, 10)}-trace.md`
    : 'eval-trace.md'

  return (
    <Layout
      title="Provider Eval"
      subtitle="对 endpoint 跑 25 步 / ~47 个 probe 评估性能 / 智商 / 功能覆盖度（基于 probe.mjs）"
      actions={actions}
    >
      <div className="grid grid-cols-1 lg:grid-cols-[380px_1fr] gap-6 items-start">
        <div className="bg-white border border-gray-200 rounded-xl p-4 space-y-3">
          <div>
            <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">URL</label>
            <input
              type="text"
              value={url}
              onChange={e => setUrl(e.target.value)}
              placeholder="https://api.example.com"
              className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
            />
          </div>
          <div>
            <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">Key</label>
            <input
              type="password"
              value={key}
              onChange={e => setKey(e.target.value)}
              placeholder="sk-..."
              className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
            />
          </div>
          <div>
            <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">Model</label>
            <div className="flex items-center gap-2 mb-2 text-[10px]">
              <label className="flex items-center gap-1 text-gray-500">
                <input type="radio" checked={modelMode === 'preset'} onChange={() => setModelMode('preset')} />
                预设
              </label>
              <label className="flex items-center gap-1 text-gray-500">
                <input type="radio" checked={modelMode === 'custom'} onChange={() => setModelMode('custom')} />
                手填
              </label>
            </div>
            {modelMode === 'preset' ? (
              <select
                value={model}
                onChange={e => setModel(e.target.value)}
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
              >
                {MODEL_DEFAULTS.map(m => <option key={m} value={m}>{m}</option>)}
              </select>
            ) : (
              <input
                type="text"
                value={customModel}
                onChange={e => setCustomModel(e.target.value)}
                placeholder="claude-sonnet-4-6"
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
              />
            )}
          </div>
          <div>
            <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">
              pass@N (重复 IQ probe)
            </label>
            <select
              value={repeat}
              onChange={e => setRepeat(Number(e.target.value))}
              className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
            >
              <option value={1}>pass@1 — ~$2.6 / 3 min</option>
              <option value={3}>pass@3 — ~$5 / 7 min</option>
              <option value={5}>pass@5 — ~$8 / 10 min</option>
            </select>
            <p className="text-[10px] text-gray-400 mt-1">
              非 IQ 步骤（模型目录 / 流式 / 长输出 / 缓存 / 1M 上下文 / 错误恢复）始终单跑，开销不是线性。
            </p>
          </div>

          {graderAvailable && (
            <div className="pt-3 border-t border-gray-100">
              <label className="flex items-center gap-2 text-xs text-gray-700">
                <input
                  type="checkbox"
                  checked={runGrader}
                  onChange={e => setRunGrader(e.target.checked)}
                  className="rounded border-gray-300"
                />
                <span>评估后让 Claude 自动打分</span>
              </label>
              <p className="text-[10px] text-gray-400 mt-1">
                probe.mjs 跑完后调 <code className="bg-gray-100 px-1 rounded">claude -p</code>，按 PIPELINE.md §2-4 输出中文评估报告（~$0.3-0.5）
              </p>
            </div>
          )}

          {submitError && (
            <div className="bg-rose-50 border border-rose-100 text-rose-700 text-xs rounded-md px-3 py-2">{submitError}</div>
          )}

          <div className="text-[10px] text-gray-400 leading-relaxed pt-2 border-t border-gray-100 space-y-1">
            <div>将真实调用上游模型，会产生费用 — 仅对你授权的 endpoint 使用</div>
            <div>采集完成后输出原始 trace.md，{graderAvailable ? '并由 Claude 自动按 PIPELINE.md §2-4 打分' : '可下载后喂给 LLM 按 PIPELINE.md §2-4 打分'}</div>
          </div>
        </div>

        <div className="space-y-4">
          {!status && !jobId && (
            <div className="bg-white border border-gray-200 rounded-xl py-16 text-center text-gray-400 text-xs">
              填写 URL / Key / Model 后点击「开始评估」
            </div>
          )}

          {status && (
            <>
              <div className="bg-white border border-gray-200 rounded-xl p-4">
                <div className="flex items-center justify-between flex-wrap gap-2">
                  <div className="flex items-center gap-2">
                    <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold ${statusColor(status.status)}`}>
                      {status.status}
                    </span>
                    <span className="text-xs text-gray-500">job {status.job_id}</span>
                    <span className="text-xs text-gray-400">pass@{status.repeat}</span>
                  </div>
                  <div className="text-[11px] text-gray-400 tabular-nums">
                    {status.elapsed_ms != null
                      ? `${(status.elapsed_ms / 1000).toFixed(1)}s`
                      : `${((Date.now() / 1000 - status.started_at)).toFixed(0)}s elapsed`}
                  </div>
                </div>
                {status.error && (
                  <div className="mt-3 bg-rose-50 border border-rose-100 text-rose-700 text-xs rounded-md px-3 py-2">
                    {status.error}
                  </div>
                )}
              </div>

              <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
                <div className="flex items-center justify-between px-4 py-2 border-b border-gray-100">
                  <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">Live log (stderr)</div>
                  {status.stderr_trimmed && <span className="text-[10px] text-amber-700">头部已截断</span>}
                </div>
                <pre
                  ref={logRef}
                  onScroll={onLogScroll}
                  className="text-[11px] font-mono bg-gray-900 text-gray-100 p-3 max-h-72 overflow-auto whitespace-pre-wrap break-all"
                >
                  {status.stderr || (status.status === 'running' ? 'starting node probe.mjs ...' : '<no output>')}
                </pre>
              </div>

              {status.llm_report && (
                <div className="bg-white border border-emerald-200 rounded-xl overflow-hidden">
                  <div className="flex items-center justify-between px-4 py-3 border-b border-emerald-100 bg-emerald-50">
                    <div className="flex items-center gap-2">
                      <span className="inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold bg-emerald-100 text-emerald-800">Claude</span>
                      <span className="text-sm font-semibold text-emerald-900">Eval Report</span>
                    </div>
                    <div className="flex items-center gap-2">
                      <span className="text-[10px] text-emerald-700 tabular-nums">
                        {status.grader_ms ? `${(status.grader_ms / 1000).toFixed(1)}s` : ''}
                      </span>
                      <button
                        onClick={() => status.llm_report && downloadText(traceName.replace('-trace.md', '-eval.md'), status.llm_report)}
                        className="border border-emerald-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-emerald-50 text-emerald-700"
                      >
                        下载 .md
                      </button>
                    </div>
                  </div>
                  <pre className="text-[12px] font-mono leading-relaxed bg-white p-4 max-h-[60vh] overflow-auto whitespace-pre-wrap break-words">{status.llm_report}</pre>
                </div>
              )}
              {status.llm_error && (
                <div className="bg-amber-50 border border-amber-100 rounded-xl px-4 py-3 text-xs text-amber-800">
                  Claude grader 失败：{status.llm_error}
                </div>
              )}
              {status.status === 'grading' && !status.llm_report && (
                <div className="bg-purple-50 border border-purple-100 rounded-xl px-4 py-3 text-xs text-purple-800 flex items-center gap-2">
                  <span className="w-1.5 h-1.5 rounded-full bg-purple-500 animate-pulse" />
                  正在调用 Claude grader，预计 30-90s ...
                </div>
              )}

              {status.trace && (
                <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
                  <div className="flex items-center justify-between px-4 py-2 border-b border-gray-100">
                    <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">Trace ({(status.trace.length / 1024).toFixed(1)} KiB)</div>
                    <button
                      onClick={() => status.trace && downloadText(traceName, status.trace)}
                      className="border border-gray-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-gray-50"
                    >
                      下载 .md
                    </button>
                  </div>
                  <pre className="text-[11px] font-mono bg-gray-50 p-3 max-h-[60vh] overflow-auto whitespace-pre-wrap break-all">
                    {status.trace}
                  </pre>
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </Layout>
  )
}
