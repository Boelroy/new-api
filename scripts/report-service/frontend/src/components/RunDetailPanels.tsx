import { useEffect, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { api, DetectResult, TestRun, TestRunDetail, TestRunLiveStatus } from '../api'

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

const MD_CLASSES = 'prose prose-sm max-w-none ' +
  'prose-headings:font-semibold prose-headings:text-gray-900 ' +
  'prose-h1:text-lg prose-h1:mt-1 prose-h1:mb-2 ' +
  'prose-h2:text-base prose-h2:mt-4 prose-h2:mb-1.5 ' +
  'prose-h3:text-sm prose-h3:mt-3 prose-h3:mb-1 ' +
  'prose-p:text-sm prose-p:leading-relaxed prose-p:my-1.5 prose-p:text-gray-800 ' +
  'prose-li:text-sm prose-li:my-0 prose-ul:my-1.5 prose-ol:my-1.5 ' +
  'prose-code:text-[12px] prose-code:bg-gray-100 prose-code:px-1 prose-code:py-0.5 prose-code:rounded prose-code:font-mono prose-code:before:content-none prose-code:after:content-none ' +
  'prose-pre:text-[11px] prose-pre:bg-gray-50 prose-pre:border prose-pre:border-gray-200 prose-pre:text-gray-800 ' +
  'prose-table:text-xs prose-table:my-2 ' +
  'prose-th:border prose-th:border-gray-200 prose-th:bg-gray-50 prose-th:px-2 prose-th:py-1 prose-th:text-left prose-th:font-semibold ' +
  'prose-td:border prose-td:border-gray-200 prose-td:px-2 prose-td:py-1 ' +
  'prose-strong:text-gray-900 prose-strong:font-semibold ' +
  'prose-blockquote:text-gray-600 prose-blockquote:border-l-2 prose-blockquote:border-gray-300 prose-blockquote:px-3 prose-blockquote:py-0 prose-blockquote:not-italic'

function MarkdownBlock({ text }: { text: string }) {
  return (
    <div className={MD_CLASSES}>
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
    </div>
  )
}

type Props = {
  run: TestRun
  onClose?: () => void
  onDeleted?: () => void
}

export default function RunDetailPanels({ run, onClose, onDeleted }: Props) {
  const [detail, setDetail] = useState<TestRunDetail | null>(null)
  const [live, setLive] = useState<TestRunLiveStatus | null>(null)
  const [detectTrace, setDetectTrace] = useState<string | null>(null)
  const [detectReport, setDetectReport] = useState<string | null>(null)
  const [detectResult, setDetectResult] = useState<DetectResult | null>(null)
  const [evalTrace, setEvalTrace] = useState<string | null>(null)
  const [evalReport, setEvalReport] = useState<string | null>(null)
  const [stderrText, setStderrText] = useState<string | null>(null)
  const [loadingErr, setLoadingErr] = useState<string | null>(null)
  const [showDetectTraceFull, setShowDetectTraceFull] = useState(false)
  const [showEvalTraceFull, setShowEvalTraceFull] = useState(false)
  const logRef = useRef<HTMLPreElement>(null)
  const followRef = useRef(true)

  const isTerminal = run.status === 'ok' || run.status === 'error' || run.status === 'cancelled'

  // Initial + occasional refresh of detail (file URLs are server-proxy paths,
  // they don't expire — but bytes change as the run progresses, so re-fetch).
  useEffect(() => {
    let stopped = false
    const tick = async () => {
      try {
        const d = await api.testingGetRun(run.id)
        if (!stopped) setDetail(d)
      } catch (e: any) {
        if (!stopped) setLoadingErr(e?.message || String(e))
      }
    }
    tick()
    const id = setInterval(tick, isTerminal ? 60_000 : 5_000)
    return () => { stopped = true; clearInterval(id) }
  }, [run.id, isTerminal])

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
        }
      } catch {
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

  // Lazy-fetch artifacts as they appear.
  useEffect(() => {
    if (!detail) return
    const ac = new AbortController()
    const fetchText = (url: string | undefined, set: (s: string) => void) => {
      if (!url) return
      fetch(url, { signal: ac.signal, credentials: 'same-origin' })
        .then(r => r.text())
        .then(set)
        .catch(() => {})
    }
    const fetchJSON = (url: string | undefined, set: (j: any) => void) => {
      if (!url) return
      fetch(url, { signal: ac.signal, credentials: 'same-origin' })
        .then(r => r.json())
        .then(set)
        .catch(() => {})
    }
    if (detectReport == null) fetchText(detail.files['detect-report'], setDetectReport)
    if (evalReport == null) fetchText(detail.files['eval-report'], setEvalReport)
    if (detectResult == null && run.detect_result_bytes > 0) fetchJSON(detail.files['detect-result'], setDetectResult)
    if (detectTrace == null && (run.detect_trace_bytes < 200_000 || showDetectTraceFull)) {
      fetchText(detail.files['detect-trace'], setDetectTrace)
    }
    if (evalTrace == null && (run.eval_trace_bytes < 200_000 || showEvalTraceFull)) {
      fetchText(detail.files['eval-trace'], setEvalTrace)
    }
    if (stderrText == null && isTerminal) fetchText(detail.files['stderr'], setStderrText)
    return () => ac.abort()
  }, [detail, showDetectTraceFull, showEvalTraceFull, isTerminal, run])

  const onLogScroll = () => {
    const el = logRef.current
    if (!el) return
    followRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  }

  const handleCancel = async () => {
    try { await api.testingCancelRun(run.id) } catch { /* ignore */ }
  }

  const handleRegrade = async (phase: 'detect' | 'eval') => {
    try {
      await api.testingRegrade(run.id, phase)
      // Optimistic: pull fresh detail so the UI flips into the grading state.
      const d = await api.testingGetRun(run.id)
      setDetail(d)
      if (phase === 'detect') setDetectReport(null)
      else setEvalReport(null)
    } catch (e: any) {
      alert(`重试失败：${e?.message || String(e)}`)
    }
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
          <span className="text-xs text-gray-700 font-mono">{run.model}</span>
          {run.pass_at > 1 && (
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

      {/* Live log */}
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

      {/* DETECT section */}
      <div className="space-y-3">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <span className="inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold bg-amber-100 text-amber-800">Detect</span>
            <span className="text-xs text-gray-500">6 probes · 路由层 / 后端供应商判别</span>
          </div>
          {isTerminal && run.run_grader && run.detect_trace_bytes > 0 && run.detect_report_bytes === 0 && (
            <button
              onClick={() => handleRegrade('detect')}
              className="border border-amber-200 text-amber-800 bg-amber-50 hover:bg-amber-100 rounded-md px-3 py-1 text-[11px]"
            >
              重试 Grader
            </button>
          )}
        </div>

        {detectResult && (
          <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
            <div className="px-4 py-3 border-b border-gray-100 text-sm font-semibold text-gray-900">分类</div>
            <div className="p-4 space-y-2">
              <div className="flex items-center gap-2 text-sm">
                <span className="text-gray-500 w-16 text-xs">Router</span>
                <span className="font-mono text-gray-900">{detectResult.classification.router_label || '—'}</span>
                <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold ${confidenceColor(detectResult.classification.router_confidence)}`}>
                  {detectResult.classification.router_confidence}
                </span>
              </div>
              <div className="flex items-center gap-2 text-sm">
                <span className="text-gray-500 w-16 text-xs">Backend</span>
                <span className="font-mono text-gray-900">{detectResult.classification.backend_label || '—'}</span>
                <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold ${confidenceColor(detectResult.classification.backend_confidence)}`}>
                  {detectResult.classification.backend_confidence}
                </span>
              </div>
              {detectResult.classification.signals.length > 0 && (
                <div className="pt-2 border-t border-gray-100 space-y-1.5">
                  <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1">命中信号 ({detectResult.classification.signals.length})</div>
                  {detectResult.classification.signals.map((s, i) => (
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
              {detectResult.classification.notes && detectResult.classification.notes.length > 0 && (
                <div className="pt-2 border-t border-gray-100">
                  <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1">备注</div>
                  {detectResult.classification.notes.map((n, i) => (
                    <div key={i} className="text-xs text-gray-600">· {n}</div>
                  ))}
                </div>
              )}
            </div>
          </div>
        )}

        {detectReport && (
          <div className="bg-white border border-emerald-200 rounded-xl overflow-hidden">
            <div className="flex items-center justify-between px-4 py-3 border-b border-emerald-100 bg-emerald-50">
              <div className="flex items-center gap-2">
                <span className="inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold bg-emerald-100 text-emerald-800">Claude</span>
                <span className="text-sm font-semibold text-emerald-900">Detection Report</span>
              </div>
              <a
                href={detail?.files['detect-report']}
                download={`detect-${run.model}-${run.id}-report.md`}
                className="border border-emerald-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-emerald-50 text-emerald-700"
              >
                下载 .md
              </a>
            </div>
            <div className="p-4 max-h-[60vh] overflow-auto bg-white">
              <MarkdownBlock text={detectReport} />
            </div>
          </div>
        )}

        {isTerminal && run.detect_trace_bytes > 0 && (
          <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
            <div className="flex items-center justify-between px-4 py-2 border-b border-gray-100">
              <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">Detect trace ({fmtBytes(run.detect_trace_bytes)})</div>
              <div className="flex items-center gap-2">
                {run.detect_trace_bytes >= 200_000 && !showDetectTraceFull && (
                  <button onClick={() => setShowDetectTraceFull(true)} className="border border-gray-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-gray-50 text-gray-700">加载完整</button>
                )}
                <a
                  href={detail?.files['detect-trace']}
                  download={`detect-${run.model}-${run.id}-trace.md`}
                  className="border border-gray-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-gray-50"
                >
                  下载 .md
                </a>
              </div>
            </div>
            {detectTrace ? (
              <pre className="text-[11px] font-mono bg-gray-50 p-3 max-h-[40vh] overflow-auto whitespace-pre-wrap break-all">{detectTrace}</pre>
            ) : (
              <div className="text-xs text-gray-400 p-4">{run.detect_trace_bytes >= 200_000 && !showDetectTraceFull ? '大文件，点击「加载完整」查看' : '加载中 ...'}</div>
            )}
          </div>
        )}
      </div>

      {/* EVAL section */}
      <div className="space-y-3 pt-3 border-t border-dashed border-gray-200">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <span className="inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold bg-blue-100 text-blue-800">Eval</span>
            <span className="text-xs text-gray-500">25 步 / ~47 probe · 性能 / 智商 / 功能</span>
          </div>
          {isTerminal && run.run_grader && run.eval_trace_bytes > 0 && run.eval_report_bytes === 0 && (
            <button
              onClick={() => handleRegrade('eval')}
              className="border border-blue-200 text-blue-800 bg-blue-50 hover:bg-blue-100 rounded-md px-3 py-1 text-[11px]"
            >
              重试 Grader
            </button>
          )}
        </div>

        {evalReport && (
          <div className="bg-white border border-emerald-200 rounded-xl overflow-hidden">
            <div className="flex items-center justify-between px-4 py-3 border-b border-emerald-100 bg-emerald-50">
              <div className="flex items-center gap-2">
                <span className="inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold bg-emerald-100 text-emerald-800">Claude</span>
                <span className="text-sm font-semibold text-emerald-900">Eval Report</span>
              </div>
              <a
                href={detail?.files['eval-report']}
                download={`eval-${run.model}-${run.id}-report.md`}
                className="border border-emerald-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-emerald-50 text-emerald-700"
              >
                下载 .md
              </a>
            </div>
            <div className="p-4 max-h-[60vh] overflow-auto bg-white">
              <MarkdownBlock text={evalReport} />
            </div>
          </div>
        )}

        {isTerminal && run.eval_trace_bytes > 0 && (
          <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
            <div className="flex items-center justify-between px-4 py-2 border-b border-gray-100">
              <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">Eval trace ({fmtBytes(run.eval_trace_bytes)})</div>
              <div className="flex items-center gap-2">
                {run.eval_trace_bytes >= 200_000 && !showEvalTraceFull && (
                  <button onClick={() => setShowEvalTraceFull(true)} className="border border-gray-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-gray-50 text-gray-700">加载完整</button>
                )}
                <a
                  href={detail?.files['eval-trace']}
                  download={`eval-${run.model}-${run.id}-trace.md`}
                  className="border border-gray-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-gray-50"
                >
                  下载 .md
                </a>
              </div>
            </div>
            {evalTrace ? (
              <pre className="text-[11px] font-mono bg-gray-50 p-3 max-h-[40vh] overflow-auto whitespace-pre-wrap break-all">{evalTrace}</pre>
            ) : (
              <div className="text-xs text-gray-400 p-4">{run.eval_trace_bytes >= 200_000 && !showEvalTraceFull ? '大文件，点击「加载完整」查看' : '加载中 ...'}</div>
            )}
          </div>
        )}
      </div>

      {run.llm_error && (
        <div className="bg-amber-50 border border-amber-100 rounded-xl px-4 py-3 text-xs text-amber-800">
          Grader 信息：{run.llm_error}
        </div>
      )}
      {run.status === 'grading' && !detectReport && !evalReport && (
        <div className="bg-purple-50 border border-purple-100 rounded-xl px-4 py-3 text-xs text-purple-800 flex items-center gap-2">
          <span className="w-1.5 h-1.5 rounded-full bg-purple-500 animate-pulse" />
          正在调用 Claude grader ...
        </div>
      )}

      {/* stderr snapshot */}
      {isTerminal && run.stderr_bytes > 0 && (
        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          <div className="flex items-center justify-between px-4 py-2 border-b border-gray-100">
            <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">stderr ({fmtBytes(run.stderr_bytes)})</div>
            <a
              href={detail?.files['stderr']}
              download={`${run.model}-${run.id}-stderr.log`}
              className="border border-gray-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-gray-50"
            >
              下载 .log
            </a>
          </div>
          <pre className="text-[11px] font-mono bg-gray-900 text-gray-100 p-3 max-h-72 overflow-auto whitespace-pre-wrap break-all">
            {stderrText ?? '加载中 ...'}
          </pre>
        </div>
      )}
    </div>
  )
}
