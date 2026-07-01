import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import Layout from '../components/Layout'
import RunDetailPanels from '../components/RunDetailPanels'
import { api, ClaudeCallResponse, TestProject, TestRun } from '../api'

const MODEL_DEFAULTS = [
  'claude-opus-4-7',
  'claude-sonnet-4-6',
  'claude-opus-4-6',
  'claude-haiku-4-5-20251001',
  'claude-sonnet-4-5-20250929',
  'claude-opus-4-5-20251101',
  'claude-opus-4-8',
  'claude-fable-5',
]

const RUNS_REFRESH_MS = 4000

function statusColor(s: string) {
  switch (s) {
    case 'done': return 'bg-emerald-100 text-emerald-800'
    case 'error': return 'bg-rose-100 text-rose-700'
    case 'cancelled': return 'bg-gray-100 text-gray-600'
    case 'running': return 'bg-blue-100 text-blue-700'
    case 'grading': return 'bg-purple-100 text-purple-700'
    default: return 'bg-gray-100 text-gray-500'
  }
}

function fmtTimeShort(ts: number) {
  const d = new Date(ts * 1000)
  const mm = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  const hh = String(d.getHours()).padStart(2, '0')
  const mi = String(d.getMinutes()).padStart(2, '0')
  return `${mm}-${dd} ${hh}:${mi}`
}

function fmtElapsed(ms: number | undefined) {
  if (!ms) return '—'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const m = Math.floor(ms / 60_000)
  const s = ((ms % 60_000) / 1000).toFixed(0)
  return `${m}m${s}s`
}

export default function ProviderTesting() {
  const params = useParams<{ projectId?: string }>()
  const [search, setSearch] = useSearchParams()
  const navigate = useNavigate()

  const [r2Available, setR2Available] = useState<boolean | null>(null)
  const [graderAvailable, setGraderAvailable] = useState(false)
  const [projects, setProjects] = useState<TestProject[] | null>(null)
  const [selectedProject, setSelectedProject] = useState<TestProject | null>(null)
  const [runs, setRuns] = useState<TestRun[] | null>(null)
  const [pageError, setPageError] = useState<string | null>(null)

  // New-run form state.
  const [modelMode, setModelMode] = useState<'preset' | 'custom'>('preset')
  const [model, setModel] = useState(MODEL_DEFAULTS[0])
  const [customModel, setCustomModel] = useState('')
  const [passAt, setPassAt] = useState(1)
  const [runGrader, setRunGrader] = useState(true)
  const [submitting, setSubmitting] = useState(false)

  // Claude 直接调用 (ad-hoc single-shot) state. Shares no state with the
  // Detect+Eval form above; keeps its own model + message so users can
  // switch back and forth without clobbering the run config.
  const [callModelMode, setCallModelMode] = useState<'preset' | 'custom'>('preset')
  const [callModel, setCallModel] = useState(MODEL_DEFAULTS[0])
  const [callCustomModel, setCallCustomModel] = useState('')
  const [callMessage, setCallMessage] = useState('Say hi in one short sentence.')
  const [callSending, setCallSending] = useState(false)
  const [callResult, setCallResult] = useState<ClaudeCallResponse | null>(null)
  const [callError, setCallError] = useState<string | null>(null)

  // New-project modal.
  const [newOpen, setNewOpen] = useState(false)
  const [newName, setNewName] = useState('')
  const [newUrl, setNewUrl] = useState('')
  const [newKey, setNewKey] = useState('')

  // Edit-project modal.
  const [editOpen, setEditOpen] = useState(false)
  const [editName, setEditName] = useState('')
  const [editUrl, setEditUrl] = useState('')
  const [editKey, setEditKey] = useState('')

  const selectedRunId = search.get('run') || null
  const selectedRun = useMemo(
    () => runs?.find(r => r.id === selectedRunId) ?? null,
    [runs, selectedRunId],
  )

  // Load server capabilities.
  useEffect(() => {
    void (async () => {
      try {
        const cfg = await fetch('/api/auth/config').then(r => r.json())
        setR2Available(cfg.r2_configured === true)
        const grader = cfg.grader_configured === true
        setGraderAvailable(grader)
        setRunGrader(grader)
      } catch {
        setR2Available(false)
      }
    })()
  }, [])

  // Load projects.
  const reloadProjects = async () => {
    try {
      const r = await api.testingListProjects()
      setProjects(r.projects)
    } catch (e: any) {
      setPageError('加载项目失败：' + (e?.message || String(e)))
    }
  }
  useEffect(() => { if (r2Available) reloadProjects() }, [r2Available])

  // Load selected project + run list when projectId changes.
  useEffect(() => {
    // Switching projects invalidates any prior ad-hoc call result.
    setCallResult(null)
    setCallError(null)
    if (!params.projectId) {
      setSelectedProject(null)
      setRuns(null)
      return
    }
    let stopped = false
    const load = async () => {
      try {
        const p = await api.testingGetProject(params.projectId!)
        if (stopped) return
        setSelectedProject(p)
        const rl = await api.testingListRuns(params.projectId!)
        if (stopped) return
        setRuns(rl.runs)
      } catch (e: any) {
        if (!stopped) setPageError('加载项目失败：' + (e?.message || String(e)))
      }
    }
    load()
    return () => { stopped = true }
  }, [params.projectId])

  // Auto-refresh runs while any are not terminal.
  useEffect(() => {
    if (!params.projectId || !runs) return
    const hasActive = runs.some(r => r.status === 'running' || r.status === 'grading')
    if (!hasActive) return
    const t = setTimeout(async () => {
      try {
        const rl = await api.testingListRuns(params.projectId!)
        setRuns(rl.runs)
      } catch { /* ignore */ }
    }, RUNS_REFRESH_MS)
    return () => clearTimeout(t)
  }, [params.projectId, runs])

  const finalModel = modelMode === 'custom' ? customModel.trim() : model
  const canStart = !!selectedProject && finalModel.length > 0 && !submitting

  const handleStart = async () => {
    if (!selectedProject || !canStart) return
    setSubmitting(true)
    setPageError(null)
    try {
      const r = await api.testingStartRun(selectedProject.id, {
        model: finalModel,
        pass_at: passAt,
        run_grader: graderAvailable && runGrader,
      })
      // Reload runs immediately, jump straight into the new run detail.
      const rl = await api.testingListRuns(selectedProject.id)
      setRuns(rl.runs)
      setSearch({ run: r.run_id })
    } catch (e: any) {
      setPageError('启动失败：' + (e?.message || String(e)))
    } finally {
      setSubmitting(false)
    }
  }

  const finalCallModel = callModelMode === 'custom' ? callCustomModel.trim() : callModel
  const canCall = !!selectedProject && finalCallModel.length > 0 && callMessage.trim().length > 0 && !callSending

  const handleClaudeCall = async () => {
    if (!selectedProject || !canCall) return
    setCallSending(true)
    setCallError(null)
    setCallResult(null)
    try {
      const r = await api.testingClaudeCall(selectedProject.id, {
        model: finalCallModel,
        message: callMessage.trim(),
      })
      setCallResult(r)
    } catch (e: any) {
      setCallError(e?.message || String(e))
    } finally {
      setCallSending(false)
    }
  }

  const handleCreateProject = async () => {
    const name = newName.trim()
    const url = newUrl.trim()
    const key = newKey.trim()
    if (!name || !url || !key) return
    try {
      const p = await api.testingCreateProject({ name, url, api_key: key })
      setNewName(''); setNewUrl(''); setNewKey('')
      setNewOpen(false)
      await reloadProjects()
      navigate(`/testing/${p.id}`)
    } catch (e: any) {
      alert('创建失败：' + (e?.message || String(e)))
    }
  }

  const openEdit = () => {
    if (!selectedProject) return
    setEditName(selectedProject.name)
    setEditUrl(selectedProject.url)
    setEditKey('')
    setEditOpen(true)
  }

  const handleSaveEdit = async () => {
    if (!selectedProject) return
    const payload: { name?: string; url?: string; api_key?: string } = {}
    if (editName.trim() && editName.trim() !== selectedProject.name) payload.name = editName.trim()
    if (editUrl.trim() && editUrl.trim() !== selectedProject.url) payload.url = editUrl.trim()
    if (editKey.trim()) payload.api_key = editKey.trim()
    if (!payload.name && !payload.url && !payload.api_key) {
      setEditOpen(false)
      return
    }
    try {
      const updated = await api.testingUpdateProject(selectedProject.id, payload)
      setSelectedProject(updated)
      await reloadProjects()
      setEditOpen(false)
    } catch (e: any) {
      alert('保存失败：' + (e?.message || String(e)))
    }
  }

  const handleDeleteProject = async () => {
    if (!selectedProject) return
    if (!confirm(`删除项目「${selectedProject.name}」？所有测试记录会一并清除。`)) return
    try {
      await api.testingDeleteProject(selectedProject.id)
      await reloadProjects()
      navigate('/testing')
    } catch (e: any) {
      alert('删除失败：' + (e?.message || String(e)))
    }
  }

  if (r2Available === null) {
    return (
      <Layout title="Provider Testing" subtitle="加载中 ...">
        <div className="bg-white border border-gray-200 rounded-xl py-16 text-center text-xs text-gray-400">
          加载中 ...
        </div>
      </Layout>
    )
  }

  if (!r2Available) {
    return (
      <Layout title="Provider Testing" subtitle="存档功能未启用">
        <div className="bg-white border border-amber-200 rounded-xl p-6 text-xs text-amber-800 leading-relaxed">
          R2 凭据未配置。请在服务环境变量里设置 <code className="bg-amber-50 px-1 rounded">R2_ACCOUNT_ID</code> /
          <code className="bg-amber-50 px-1 rounded">R2_ACCESS_KEY_ID</code> /
          <code className="bg-amber-50 px-1 rounded">R2_SECRET_ACCESS_KEY</code> /
          <code className="bg-amber-50 px-1 rounded">R2_BUCKET</code> 后重启 report-service。
        </div>
      </Layout>
    )
  }

  return (
    <Layout
      title="Provider Testing"
      subtitle="按项目跑 Detect + Eval，trace 和评估报告永久存档（R2）"
    >
      <div className="grid grid-cols-1 lg:grid-cols-[240px_1fr] gap-6 items-start">
        {/* Project sidebar */}
        <div className="bg-white border border-gray-200 rounded-xl p-3 lg:sticky lg:top-4">
          <button
            onClick={() => setNewOpen(true)}
            className="w-full bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 mb-3"
          >
            + 新建项目
          </button>
          {projects == null ? (
            <div className="text-xs text-gray-400 py-4 text-center">加载中 ...</div>
          ) : projects.length === 0 ? (
            <div className="text-xs text-gray-400 py-4 text-center">还没有项目</div>
          ) : (
            <div className="space-y-1">
              {projects.map(p => (
                <button
                  key={p.id}
                  onClick={() => navigate(`/testing/${p.id}`)}
                  className={`w-full text-left px-2.5 py-2 rounded-md text-xs transition-colors ${
                    params.projectId === p.id
                      ? 'bg-gray-900 text-white'
                      : 'text-gray-700 hover:bg-gray-100'
                  }`}
                >
                  <div className="font-medium truncate">{p.name}</div>
                  <div className={`text-[10px] mt-0.5 truncate font-mono ${params.projectId === p.id ? 'text-gray-300' : 'text-gray-400'}`}>
                    {p.url}
                  </div>
                  {p.run_count != null && p.run_count > 0 && (
                    <div className={`text-[10px] mt-0.5 ${params.projectId === p.id ? 'text-gray-300' : 'text-gray-400'}`}>
                      {p.run_count} runs
                    </div>
                  )}
                </button>
              ))}
            </div>
          )}
        </div>

        {/* Main content */}
        <div className="space-y-4">
          {pageError && (
            <div className="bg-rose-50 border border-rose-100 text-rose-700 text-xs rounded-md px-3 py-2">{pageError}</div>
          )}

          {!selectedProject && (
            <div className="bg-white border border-gray-200 rounded-xl py-16 text-center text-xs text-gray-400">
              {projects && projects.length === 0
                ? '点击左上「+ 新建项目」开始'
                : '从左边选择一个项目'}
            </div>
          )}

          {selectedProject && !selectedRun && (
            <>
              {/* Project header */}
              <div className="bg-white border border-gray-200 rounded-xl p-4 flex items-center justify-between flex-wrap gap-2">
                <div className="min-w-0">
                  <div className="text-base font-semibold text-gray-900 truncate">{selectedProject.name}</div>
                  <div className="text-[11px] text-gray-500 font-mono truncate mt-0.5">{selectedProject.url}</div>
                  <div className="text-[11px] text-gray-400 font-mono mt-0.5">{selectedProject.api_key}</div>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    onClick={openEdit}
                    className="border border-gray-200 rounded-md px-3 py-1 text-[11px] bg-white hover:bg-gray-50 text-gray-700"
                  >
                    编辑
                  </button>
                  <button
                    onClick={handleDeleteProject}
                    className="border border-rose-200 text-rose-700 rounded-md px-3 py-1 text-[11px] hover:bg-rose-50"
                  >
                    删除
                  </button>
                </div>
              </div>

              {/* New run form */}
              <div className="bg-white border border-gray-200 rounded-xl p-4">
                <div className="flex items-center justify-between mb-3">
                  <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">新建测试</div>
                  <div className="text-[10px] text-gray-400">每次测试自动跑 Detect + Eval，约 3-16 分钟</div>
                </div>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
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
                    <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">Eval pass@N</label>
                    <select
                      value={passAt}
                      onChange={e => setPassAt(Number(e.target.value))}
                      className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
                    >
                      <option value={1}>pass@1 — ~$2.6 / 3 min</option>
                      <option value={3}>pass@3 — ~$5 / 7 min</option>
                      <option value={5}>pass@5 — ~$8 / 10 min</option>
                    </select>
                  </div>
                  {graderAvailable && (
                    <div>
                      <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">自动打分</label>
                      <label className="flex items-center gap-2 text-xs text-gray-700">
                        <input
                          type="checkbox"
                          checked={runGrader}
                          onChange={e => setRunGrader(e.target.checked)}
                          className="rounded border-gray-300"
                        />
                        <span>让 Claude 评估并保存 report.md</span>
                      </label>
                    </div>
                  )}
                </div>
                <div className="mt-4 flex items-center gap-2">
                  <button
                    onClick={handleStart}
                    disabled={!canStart}
                    className="bg-gray-900 text-white rounded-md px-4 py-1.5 text-xs hover:opacity-85 disabled:opacity-40 disabled:cursor-not-allowed"
                  >
                    {submitting ? '提交中 ...' : '开始测试'}
                  </button>
                  <span className="text-[10px] text-gray-400">真实调用上游模型 — 仅对你授权的 endpoint 使用</span>
                </div>
              </div>

              {/* Claude 直接调用 — ad-hoc single-shot against project host+key */}
              <div className="bg-white border border-gray-200 rounded-xl p-4">
                <div className="flex items-center justify-between mb-3">
                  <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">Claude 直接调用</div>
                  <div className="text-[10px] text-gray-400">
                    使用项目的 host + api key 打一发 /v1/messages · 非流式 · max_tokens 1024
                  </div>
                </div>
                <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
                  <div>
                    <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">Model</label>
                    <div className="flex items-center gap-2 mb-2 text-[10px]">
                      <label className="flex items-center gap-1 text-gray-500">
                        <input
                          type="radio"
                          checked={callModelMode === 'preset'}
                          onChange={() => setCallModelMode('preset')}
                        />
                        预设
                      </label>
                      <label className="flex items-center gap-1 text-gray-500">
                        <input
                          type="radio"
                          checked={callModelMode === 'custom'}
                          onChange={() => setCallModelMode('custom')}
                        />
                        手填
                      </label>
                    </div>
                    {callModelMode === 'preset' ? (
                      <select
                        value={callModel}
                        onChange={e => setCallModel(e.target.value)}
                        className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
                      >
                        {MODEL_DEFAULTS.map(m => <option key={m} value={m}>{m}</option>)}
                      </select>
                    ) : (
                      <input
                        type="text"
                        value={callCustomModel}
                        onChange={e => setCallCustomModel(e.target.value)}
                        placeholder="claude-sonnet-4-6"
                        className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
                      />
                    )}
                  </div>
                  <div className="md:col-span-2">
                    <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">User message</label>
                    <textarea
                      value={callMessage}
                      onChange={e => setCallMessage(e.target.value)}
                      rows={3}
                      placeholder="Say hi in one short sentence."
                      className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono resize-y"
                    />
                  </div>
                </div>
                <div className="mt-4 flex items-center gap-2">
                  <button
                    onClick={handleClaudeCall}
                    disabled={!canCall}
                    className="bg-gray-900 text-white rounded-md px-4 py-1.5 text-xs hover:opacity-85 disabled:opacity-40 disabled:cursor-not-allowed"
                  >
                    {callSending ? '调用中 ...' : '发送'}
                  </button>
                  {callResult && (
                    <button
                      onClick={() => { setCallResult(null); setCallError(null) }}
                      className="border border-gray-200 rounded-md px-3 py-1 text-[11px] text-gray-500 hover:bg-gray-50"
                    >
                      清空结果
                    </button>
                  )}
                  <span className="text-[10px] text-gray-400">真实计费 · 请节制</span>
                </div>

                {(callError || callResult) && (
                  <div className="mt-4 border-t border-gray-100 pt-4 space-y-2">
                    {callError && (
                      <div className="bg-rose-50 border border-rose-100 text-rose-700 text-xs rounded-md px-3 py-2 whitespace-pre-wrap break-all">
                        请求失败：{callError}
                      </div>
                    )}
                    {callResult && (
                      <>
                        <div className="flex items-center gap-2 text-[11px] flex-wrap">
                          <span
                            className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold ${
                              callResult.error
                                ? 'bg-rose-100 text-rose-700'
                                : callResult.status >= 200 && callResult.status < 300
                                ? 'bg-emerald-100 text-emerald-800'
                                : 'bg-amber-100 text-amber-800'
                            }`}
                          >
                            {callResult.error ? 'error' : `HTTP ${callResult.status || 0}`}
                          </span>
                          <span className="text-gray-500 tabular-nums">{fmtElapsed(callResult.latency_ms)}</span>
                          {callResult.stop_reason && (
                            <span className="text-gray-400">stop: <span className="font-mono">{callResult.stop_reason}</span></span>
                          )}
                        </div>
                        {callResult.error ? (
                          <pre className="bg-rose-50 border border-rose-100 text-rose-700 text-[11px] rounded-md px-3 py-2 whitespace-pre-wrap break-all font-mono max-h-64 overflow-auto">
                            {callResult.error}
                          </pre>
                        ) : (
                          <div className="bg-gray-50 border border-gray-100 rounded-md px-3 py-2 whitespace-pre-wrap text-xs text-gray-800 max-h-72 overflow-auto">
                            {callResult.text || <span className="text-gray-400">（无文本内容）</span>}
                          </div>
                        )}
                        <div className="grid grid-cols-2 md:grid-cols-4 gap-2 text-[11px] text-gray-500">
                          <div>
                            <div className="text-[10px] uppercase tracking-wider text-gray-400">Input</div>
                            <div className="tabular-nums text-gray-800">{callResult.usage.input_tokens.toLocaleString()}</div>
                          </div>
                          <div>
                            <div className="text-[10px] uppercase tracking-wider text-gray-400">Output</div>
                            <div className="tabular-nums text-gray-800">{callResult.usage.output_tokens.toLocaleString()}</div>
                          </div>
                          <div>
                            <div className="text-[10px] uppercase tracking-wider text-gray-400">Cache read</div>
                            <div className="tabular-nums text-emerald-700">{callResult.usage.cache_read_tokens.toLocaleString()}</div>
                          </div>
                          <div>
                            <div className="text-[10px] uppercase tracking-wider text-gray-400">Cache write</div>
                            <div className="tabular-nums text-rose-600">{callResult.usage.cache_write_tokens.toLocaleString()}</div>
                          </div>
                        </div>
                      </>
                    )}
                  </div>
                )}
              </div>

              {/* Run history */}
              <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
                <div className="px-4 py-3 border-b border-gray-100 flex items-center justify-between">
                  <div className="text-sm font-semibold text-gray-900">测试历史</div>
                  <span className="text-[10px] text-gray-400">{runs?.length ?? 0} 次</span>
                </div>
                {runs == null ? (
                  <div className="text-xs text-gray-400 p-6 text-center">加载中 ...</div>
                ) : runs.length === 0 ? (
                  <div className="text-xs text-gray-400 p-6 text-center">还没有测试记录</div>
                ) : (
                  <div className="divide-y divide-gray-100">
                    {runs.map(r => (
                      <button
                        key={r.id}
                        onClick={() => setSearch({ run: r.id })}
                        className="w-full text-left px-4 py-2.5 hover:bg-gray-50 flex items-center justify-between gap-2"
                      >
                        <div className="flex items-center gap-2 min-w-0">
                          <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold ${statusColor(r.status)}`}>
                            {r.status}
                          </span>
                          <span className="text-[11px] text-gray-500 tabular-nums">{fmtTimeShort(r.started_at)}</span>
                          <span className="text-[11px] text-gray-400">·</span>
                          <span className="text-[11px] text-gray-700 font-mono truncate">{r.model}</span>
                          {r.pass_at > 1 && (
                            <span className="text-[10px] text-gray-400">pass@{r.pass_at}</span>
                          )}
                        </div>
                        <span className="text-[11px] text-gray-400 tabular-nums whitespace-nowrap">
                          {fmtElapsed(r.elapsed_ms)}
                        </span>
                      </button>
                    ))}
                  </div>
                )}
              </div>
            </>
          )}

          {selectedProject && selectedRun && (
            <>
              <button
                onClick={() => setSearch({})}
                className="text-xs text-gray-500 hover:text-gray-900 mb-1"
              >
                ← 返回 {selectedProject.name}
              </button>
              <RunDetailPanels
                run={selectedRun}
                onClose={() => setSearch({})}
                onDeleted={async () => {
                  setSearch({})
                  if (selectedProject) {
                    const rl = await api.testingListRuns(selectedProject.id)
                    setRuns(rl.runs)
                  }
                  reloadProjects()
                }}
              />
            </>
          )}
        </div>
      </div>

      {/* New project modal */}
      {newOpen && (
        <div
          className="fixed inset-0 z-50 bg-black/40 flex items-center justify-center p-4"
          onClick={() => setNewOpen(false)}
        >
          <div
            className="bg-white rounded-xl border border-gray-200 w-full max-w-md p-5 space-y-3"
            onClick={e => e.stopPropagation()}
          >
            <div className="text-base font-semibold text-gray-900">新建项目</div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">Name</label>
              <input
                value={newName}
                onChange={e => setNewName(e.target.value)}
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
                placeholder="例如 nexrouter prod"
              />
            </div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">URL</label>
              <input
                value={newUrl}
                onChange={e => setNewUrl(e.target.value)}
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
                placeholder="https://api.example.com"
              />
            </div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">API Key</label>
              <input
                type="password"
                value={newKey}
                onChange={e => setNewKey(e.target.value)}
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
                placeholder="sk-..."
              />
            </div>
            <div className="flex items-center justify-end gap-2 pt-2">
              <button
                onClick={() => setNewOpen(false)}
                className="border border-gray-200 rounded-md px-3 py-1.5 text-xs text-gray-700 hover:bg-gray-50"
              >
                取消
              </button>
              <button
                onClick={handleCreateProject}
                disabled={!newName.trim() || !newUrl.trim() || !newKey.trim()}
                className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-40"
              >
                创建
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Edit project modal */}
      {editOpen && selectedProject && (
        <div
          className="fixed inset-0 z-50 bg-black/40 flex items-center justify-center p-4"
          onClick={() => setEditOpen(false)}
        >
          <div
            className="bg-white rounded-xl border border-gray-200 w-full max-w-md p-5 space-y-3"
            onClick={e => e.stopPropagation()}
          >
            <div className="text-base font-semibold text-gray-900">编辑项目</div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">Name</label>
              <input
                value={editName}
                onChange={e => setEditName(e.target.value)}
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
              />
            </div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">URL</label>
              <input
                value={editUrl}
                onChange={e => setEditUrl(e.target.value)}
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
              />
            </div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">
                新 API Key（留空则不改）
              </label>
              <input
                type="password"
                value={editKey}
                onChange={e => setEditKey(e.target.value)}
                placeholder="sk-..."
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
              />
            </div>
            <div className="flex items-center justify-end gap-2 pt-2">
              <button
                onClick={() => setEditOpen(false)}
                className="border border-gray-200 rounded-md px-3 py-1.5 text-xs text-gray-700 hover:bg-gray-50"
              >
                取消
              </button>
              <button
                onClick={handleSaveEdit}
                className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85"
              >
                保存
              </button>
            </div>
          </div>
        </div>
      )}
    </Layout>
  )
}
