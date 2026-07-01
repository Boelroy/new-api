import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import Layout from '../components/Layout'
import RunDetailPanels from '../components/RunDetailPanels'
import { api, TestProject, TestRun } from '../api'

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

  // New-project modal.
  const [newOpen, setNewOpen] = useState(false)
  const [newName, setNewName] = useState('')
  const [newUrl, setNewUrl] = useState('')
  const [newKey, setNewKey] = useState('')
  const [newGraderUrl, setNewGraderUrl] = useState('')
  const [newGraderKey, setNewGraderKey] = useState('')
  const [newGraderModel, setNewGraderModel] = useState('')

  // Edit-project modal.
  const [editOpen, setEditOpen] = useState(false)
  const [editName, setEditName] = useState('')
  const [editUrl, setEditUrl] = useState('')
  const [editKey, setEditKey] = useState('')
  const [editGraderUrl, setEditGraderUrl] = useState('')
  const [editGraderKey, setEditGraderKey] = useState('')
  const [editGraderModel, setEditGraderModel] = useState('')

  const selectedRunId = search.get('run') || null
  const selectedRun = useMemo(
    () => runs?.find(r => r.id === selectedRunId) ?? null,
    [runs, selectedRunId],
  )

  // Load server capabilities. Grader availability is now per-project so we
  // only need R2 status here.
  useEffect(() => {
    void (async () => {
      try {
        const cfg = await fetch('/api/auth/config').then(r => r.json())
        setR2Available(cfg.r2_configured === true)
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
        // Backend gates on the project's own grader creds; sending true here
        // is a no-op when the project doesn't have them configured.
        run_grader: runGrader,
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

  const handleCreateProject = async () => {
    const name = newName.trim()
    const url = newUrl.trim()
    const key = newKey.trim()
    if (!name || !url || !key) return
    try {
      const p = await api.testingCreateProject({
        name,
        url,
        api_key: key,
        grader_url: newGraderUrl.trim(),
        grader_api_key: newGraderKey.trim(),
        grader_model: newGraderModel.trim(),
      })
      setNewName(''); setNewUrl(''); setNewKey('')
      setNewGraderUrl(''); setNewGraderKey(''); setNewGraderModel('')
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
    setEditGraderUrl(selectedProject.grader_url || '')
    setEditGraderKey('')
    setEditGraderModel(selectedProject.grader_model || '')
    setEditOpen(true)
  }

  const handleSaveEdit = async () => {
    if (!selectedProject) return
    // Pointer-style payload: only include keys the user actually changed.
    // Empty grader_url is a legitimate "clear grader" signal, so we send
    // it whenever the field differs from what the project had.
    const payload: {
      name?: string
      url?: string
      api_key?: string
      grader_url?: string
      grader_api_key?: string
      grader_model?: string
    } = {}
    if (editName.trim() && editName.trim() !== selectedProject.name) payload.name = editName.trim()
    if (editUrl.trim() && editUrl.trim() !== selectedProject.url) payload.url = editUrl.trim()
    if (editKey.trim()) payload.api_key = editKey.trim()
    if (editGraderUrl.trim() !== (selectedProject.grader_url || '').trim()) {
      payload.grader_url = editGraderUrl.trim()
    }
    if (editGraderKey.trim()) payload.grader_api_key = editGraderKey.trim()
    if (editGraderModel.trim() !== (selectedProject.grader_model || '').trim()) {
      payload.grader_model = editGraderModel.trim()
    }
    if (
      payload.name === undefined && payload.url === undefined && payload.api_key === undefined &&
      payload.grader_url === undefined && payload.grader_api_key === undefined && payload.grader_model === undefined
    ) {
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
                  {selectedProject.grader_url && selectedProject.grader_api_key ? (
                    <div>
                      <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">自动打分</label>
                      <label className="flex items-center gap-2 text-xs text-gray-700">
                        <input
                          type="checkbox"
                          checked={runGrader}
                          onChange={e => setRunGrader(e.target.checked)}
                          className="rounded border-gray-300"
                        />
                        <span>调用 Grader endpoint 生成 report.md</span>
                      </label>
                    </div>
                  ) : (
                    <div>
                      <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">自动打分</label>
                      <div className="text-[11px] text-gray-400 leading-relaxed">
                        本项目未配置 Grader URL / API Key，跑测试不会生成 report.md。<br />
                        <button
                          type="button"
                          onClick={openEdit}
                          className="mt-1 text-gray-600 hover:text-gray-900 underline underline-offset-2"
                        >
                          去编辑项目设置 Grader
                        </button>
                      </div>
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

            <div className="pt-3 border-t border-gray-100">
              <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1">Grader（可选）</div>
              <div className="text-[10px] text-gray-400 mb-2 leading-relaxed">
                指定另一个 Anthropic 兼容 endpoint 来跑评分。留空则本项目跑测试时不生成 report.md。
              </div>
            </div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">Grader URL</label>
              <input
                value={newGraderUrl}
                onChange={e => setNewGraderUrl(e.target.value)}
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
                placeholder="https://api.anthropic.com"
              />
            </div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">Grader API Key</label>
              <input
                type="password"
                value={newGraderKey}
                onChange={e => setNewGraderKey(e.target.value)}
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
                placeholder="sk-..."
              />
            </div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">Grader Model（可选，默认 claude-sonnet-4-6）</label>
              <input
                value={newGraderModel}
                onChange={e => setNewGraderModel(e.target.value)}
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
                placeholder="claude-sonnet-4-6"
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

            <div className="pt-3 border-t border-gray-100">
              <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1">Grader（可选）</div>
              <div className="text-[10px] text-gray-400 mb-2 leading-relaxed">
                将 Grader URL 清空以关闭本项目的自动打分。当前存储的 grader key：
                <span className="font-mono text-gray-500 ml-1">{selectedProject.grader_api_key || '（未设置）'}</span>
              </div>
            </div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">Grader URL</label>
              <input
                value={editGraderUrl}
                onChange={e => setEditGraderUrl(e.target.value)}
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
                placeholder="https://api.anthropic.com"
              />
            </div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">
                新 Grader API Key（留空则不改）
              </label>
              <input
                type="password"
                value={editGraderKey}
                onChange={e => setEditGraderKey(e.target.value)}
                placeholder="sk-..."
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
              />
            </div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">Grader Model（可选）</label>
              <input
                value={editGraderModel}
                onChange={e => setEditGraderModel(e.target.value)}
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 font-mono"
                placeholder="claude-sonnet-4-6"
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
