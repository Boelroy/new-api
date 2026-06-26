import { useEffect, useRef, useState } from 'react'
import { api, DetectResult, TestRun, TestRunDetail, TestRunLiveStatus } from '../api'

const POLL_INTERVAL_MS = 2000
const URL_REFRESH_INTERVAL_MS = 4 * 60 * 1000 // refresh signed URLs every 4 min (TTL = 5 min)

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

function confidenceColor(conf: string) {
  switch (conf) {
    case 'high': return 'bg-emerald-100 text-emerald-800'
    case 'medium': return 'bg-amber-100 text-amber-800'
    case 'low': return 'bg-orange-100 text-orange-700'
    default: return 'bg-gray-100 text-gray-500'
  }
}

function tierColor(tier: number) {
  return tier === 1 ? 'bg-rose-100 text-rose-700' : 'bg-blue-100 text-blue-700'
}

function fmtBytes(n: number) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`
  return `${(n / 1024 / 1024).toFixed(2)} MiB`
}

function fmtElapsed(ms: number | undefined) {
  if (!ms) return '—'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const m = Math.floor(ms / 60_000)
  const s = ((ms % 60_000) / 1000).toFixed(0)
  return `${m}m${s}s`
}

type Props = {
  run: TestRun
  onClose?: () => void
  onDeleted?: () => void
}

export default function RunDetailPanels({ run, onClose, onDeleted }: Props) {
  const [detail, setDetail] = useState<TestRunDetail | null>(null)
  const [live, setLive] = useState<TestRunLiveStatus | null>(null)
  const [traceText, setTraceText] = useState<string | null>(null)
  const [reportText, setReportText] = useState<string | null>(null)
  const [stderrText, setStderrText] = useState<string | null>(null)
  const [result, setResult] = useState<DetectResult | null>(null)
  const [loadingErr, setLoadingErr] = useState<string | null>(null)
  const [showTraceFull, setShowTraceFull] = useState(false)
  const logRef = useRef<HTMLPreElement>(null)
  const followRef = useRef(true)

  const isTerminal = run.status === 'ok' || run.status === 'error' || run.status === 'cancelled'

  // Initial detail fetch + URL refresher.
  useEffect(() => {
    let stopped = false
    let timer: ReturnType<typeof setTimeout> | null = null
    const tick = async () => {
      try {
        const d = await api.testingGetRun(run.id)
        if (stopped) return
        setDetail(d)
      } catch (e: any) {
        if (!stopped) setLoadingErr(e?.message || String(e))
      }
      if (!stopped) timer = setTimeout(tick, URL_REFRESH_INTERVAL_MS)
    }
    tick()
    return () => {
      stopped = true
      if (timer) clearTimeout(timer)
    }
  }, [run.id])

  // Live polling while not terminal.
  useEffect(() => {
    if (isTerminal) return
    let stopped = false
    const poll = async () => {
      try {
        const s = await api.testingRunStatus(run.id)
        if (stopped) return
        setLive(s)
        if (s.status === 'running' || s.status === 'grading') {
          setTimeout(poll, POLL_INTERVAL_MS)
        } else {
          // Run became terminal → re-fetch detail to pick up signed URLs.
          api.testingGetRun(run.id).then(d => { if (!stopped) setDetail(d) }).catch(() => {})
        }
      } catch {
        /* keep polling */
        if (!stopped) setTimeout(poll, POLL_INTERVAL_MS * 2)
      }
    }
    poll()
    return () => { stopped = true }
  }, [run.id, isTerminal])

  // Auto-scroll live log.
  useEffect(() => {
    if (!logRef.current || !followRef.current) return
    logRef.current.scrollTop = logRef.current.scrollHeight
  }, [live?.stderr])

  // Fetch the three text artifacts when signed URLs become available.
  useEffect(() => {
    if (!detail) return
    const ac = new AbortController()
    if (detail.report_url && reportText == null) {
      fetch(detail.report_url, { signal: ac.signal })
        .then(r => r.text())
        .then(t => setReportText(t))
        .catch(() => {})
    }
    if (detail.trace_url && traceText == null && (run.trace_bytes < 200_000 || showTraceFull)) {
      fetch(detail.trace_url, { signal: ac.signal })
        .then(r => r.text())
        .then(t => setTraceText(t))
        .catch(() => {})
    }
    if (detail.stderr_url && stderrText == null && isTerminal) {
      fetch(detail.stderr_url, { signal: ac.signal })
        .then(r => r.text())
        .then(t => setStderrText(t))
        .catch(() => {})
    }
    if (detail.result_url && result == null && run.kind === 'detect') {
      fetch(detail.result_url, { signal: ac.signal })
        .then(r => r.json())
        .then(j => setResult(j as DetectResult))
        .catch(() => {})
    }
    return () => ac.abort()
  }, [detail, showTraceFull, isTerminal, run.kind, run.trace_bytes])

  const onLogScroll = () => {
    const el = logRef.current
    if (!el) return
    followRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  }

  const downloadFromURL = (url: string | undefined, name: string) => {
    if (!url) return
    const a = document.createElement('a')
    a.href = url
    a.download = name
    a.target = '_blank'
    a.rel = 'noopener'
    a.click()
  }

  const handleCancel = async () => {
    try { await api.testingCancelRun(run.id) } catch { /* ignore */ }
  }

  const handleDelete = async () => {
    if (!confirm('删除这次测试记录？包含 R2 上的 trace / report。')) return
    try {
      await api.testingDeleteRun(run.id)
      onDeleted?.()
    } catch (e: any) {
      alert('删除失败：' + (e?.message || String(e)))
    }
  }

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="bg-white border border-gray-200 rounded-xl p-4 flex items-center justify-between flex-wrap gap-2">
        <div className="flex items-center gap-2 flex-wrap">
          <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold ${statusColor(run.status)}`}>
            {run.status}
          </span>
          <span className="text-xs text-gray-500 font-mono">{run.id}</span>
          <span className="text-[11px] text-gray-400">·</span>
          <span className="text-xs text-gray-700">{run.kind}</span>
          <span className="text-[11px] text-gray-400">·</span>
          <span className="text-xs text-gray-700 font-mono">{run.model}</span>
          {run.kind === 'eval' && run.pass_at > 1 && (
            <span className="text-[11px] text-gray-400">pass@{run.pass_at}</span>
          )}
          <span className="text-[11px] text-gray-400">·</span>
          <span className="text-[11px] text-gray-500 tabular-nums">
            {fmtElapsed(run.elapsed_ms ?? (live?.elapsed_ms))}
          </span>
        </div>
        <div className="flex items-center gap-2">
          {!isTerminal && (
            <button
              onClick={handleCancel}
              className="bg-rose-600 text-white rounded-md px-3 py-1 text-[11px] hover:opacity-85"
            >
              停止
            </button>
          )}
          {isTerminal && (
            <button
              onClick={handleDelete}
              className="border border-rose-200 text-rose-700 rounded-md px-3 py-1 text-[11px] hover:bg-rose-50"
            >
              删除
            </button>
          )}
          {onClose && (
            <button
              onClick={onClose}
              className="border border-gray-200 text-gray-500 rounded-md px-3 py-1 text-[11px] hover:bg-gray-50"
            >
              关闭
            </button>
          )}
        </div>
      </div>

      {(run.error_msg || live?.error_msg) && (
        <div className="bg-rose-50 border border-rose-100 text-rose-700 text-xs rounded-md px-3 py-2">
          {run.error_msg || live?.error_msg}
        </div>
      )}
      {loadingErr && (
        <div className="bg-amber-50 border border-amber-100 text-amber-800 text-xs rounded-md px-3 py-2">
          加载失败：{loadingErr}
        </div>
      )}

      {/* Live log (only while not terminal) */}
      {!isTerminal && (
        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="flex items-center justify-between px-4 py-2 border-b border-gray-100">
            <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">Live log (stderr)</div>
            {live?.stderr_trimmed && <span className="text-[10px] text-amber-700">头部已截断</span>}
          </div>
          <pre
            ref={logRef}
            onScroll={onLogScroll}
            className="text-[11px] font-mono bg-gray-900 text-gray-100 p-3 max-h-72 overflow-auto whitespace-pre-wrap break-all"
          >
            {live?.stderr || (run.status === 'running' ? 'starting probe ...' : '<no output>')}
          </pre>
        </div>
      )}

      {/* Eval grader report */}
      {reportText && (
        <div className="bg-white border border-emerald-200 rounded-xl overflow-hidden">
          <div className="flex items-center justify-between px-4 py-3 border-b border-emerald-100 bg-emerald-50">
            <div className="flex items-center gap-2">
              <span className="inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold bg-emerald-100 text-emerald-800">Claude</span>
              <span className="text-sm font-semibold text-emerald-900">{run.kind === 'detect' ? 'Detection Report' : 'Eval Report'}</span>
            </div>
            <div className="flex items-center gap-2">
              <span className="text-[10px] text-emerald-700 tabular-nums">
                {run.grader_ms ? `${(run.grader_ms / 1000).toFixed(1)}s` : ''}
              </span>
              <button
                onClick={() => downloadFromURL(detail?.report_url, `${run.kind}-${run.model}-${run.id}-report.md`)}
                className="border border-emerald-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-emerald-50 text-emerald-700"
              >
                下载 .md
              </button>
            </div>
          </div>
          <pre className="text-[12px] font-mono leading-relaxed bg-white p-4 max-h-[60vh] overflow-auto whitespace-pre-wrap break-words">{reportText}</pre>
        </div>
      )}
      {run.llm_error && (
        <div className="bg-amber-50 border border-amber-100 rounded-xl px-4 py-3 text-xs text-amber-800">
          Claude grader 失败：{run.llm_error}
        </div>
      )}
      {run.status === 'grading' && !reportText && (
        <div className="bg-purple-50 border border-purple-100 rounded-xl px-4 py-3 text-xs text-purple-800 flex items-center gap-2">
          <span className="w-1.5 h-1.5 rounded-full bg-purple-500 animate-pulse" />
          正在调用 Claude grader ...
        </div>
      )}

      {/* Detect structured classification */}
      {run.kind === 'detect' && result && (
        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="px-4 py-3 border-b border-gray-100 flex items-center justify-between">
            <div className="text-sm font-semibold text-gray-900">分类</div>
          </div>
          <div className="p-4 space-y-2">
            <div className="flex items-center gap-2 text-sm">
              <span className="text-gray-500 w-16 text-xs">Router</span>
              <span className="font-mono text-gray-900">{result.classification.router_label || '—'}</span>
              <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold ${confidenceColor(result.classification.router_confidence)}`}>
                {result.classification.router_confidence}
              </span>
            </div>
            <div className="flex items-center gap-2 text-sm">
              <span className="text-gray-500 w-16 text-xs">Backend</span>
              <span className="font-mono text-gray-900">{result.classification.backend_label || '—'}</span>
              <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold ${confidenceColor(result.classification.backend_confidence)}`}>
                {result.classification.backend_confidence}
              </span>
            </div>
            {result.classification.signals.length > 0 && (
              <div className="pt-2 border-t border-gray-100 space-y-1.5">
                <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1">命中信号 ({result.classification.signals.length})</div>
                {result.classification.signals.map((s, i) => (
                  <div key={i} className="flex items-start gap-2 text-xs">
                    <span className={`inline-block px-1.5 py-0.5 rounded text-[10px] font-mono font-semibold ${tierColor(s.tier)}`}>
                      {s.code} T{s.tier}
                    </span>
                    <span className="text-gray-700">{s.label}</span>
                    <span className="text-gray-500">— {s.detail}</span>
                  </div>
                ))}
              </div>
            )}
            {result.classification.notes && result.classification.notes.length > 0 && (
              <div className="pt-2 border-t border-gray-100">
                <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1">备注</div>
                {result.classification.notes.map((n, i) => (
                  <div key={i} className="text-xs text-gray-600">· {n}</div>
                ))}
              </div>
            )}
          </div>
        </div>
      )}

      {/* Trace */}
      {isTerminal && run.trace_bytes > 0 && (
        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="flex items-center justify-between px-4 py-2 border-b border-gray-100">
            <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">
              Trace ({fmtBytes(run.trace_bytes)})
            </div>
            <div className="flex items-center gap-2">
              {run.trace_bytes >= 200_000 && !showTraceFull && (
                <button
                  onClick={() => setShowTraceFull(true)}
                  className="border border-gray-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-gray-50 text-gray-700"
                >
                  加载完整 trace
                </button>
              )}
              <button
                onClick={() => downloadFromURL(detail?.trace_url, `${run.kind}-${run.model}-${run.id}-trace.md`)}
                className="border border-gray-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-gray-50"
              >
                下载 .md
              </button>
            </div>
          </div>
          {traceText ? (
            <pre className="text-[11px] font-mono bg-gray-50 p-3 max-h-[60vh] overflow-auto whitespace-pre-wrap break-all">
              {traceText}
            </pre>
          ) : (
            <div className="text-xs text-gray-400 p-4">{run.trace_bytes >= 200_000 && !showTraceFull ? '大文件，点击「加载完整 trace」加载' : '加载中 ...'}</div>
          )}
        </div>
      )}

      {/* stderr snapshot (terminal runs) */}
      {isTerminal && run.stderr_bytes > 0 && (
        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="flex items-center justify-between px-4 py-2 border-b border-gray-100">
            <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">stderr ({fmtBytes(run.stderr_bytes)})</div>
            <button
              onClick={() => downloadFromURL(detail?.stderr_url, `${run.kind}-${run.model}-${run.id}-stderr.log`)}
              className="border border-gray-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-gray-50"
            >
              下载 .log
            </button>
          </div>
          <pre className="text-[11px] font-mono bg-gray-900 text-gray-100 p-3 max-h-72 overflow-auto whitespace-pre-wrap break-all">
            {stderrText ?? '加载中 ...'}
          </pre>
        </div>
      )}
    </div>
  )
}
