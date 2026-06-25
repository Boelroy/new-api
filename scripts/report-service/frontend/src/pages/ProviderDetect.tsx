import { useMemo, useState } from 'react'
import Layout from '../components/Layout'
import { api, DetectResult, DetectSignal } from '../api'

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

function prettyJSON(s: string) {
  if (!s) return s
  const t = s.trim()
  if (!t.startsWith('{') && !t.startsWith('[')) return s
  try {
    return JSON.stringify(JSON.parse(t), null, 2)
  } catch {
    return s
  }
}

function confidenceColor(conf: string) {
  switch (conf) {
    case 'high':
      return 'bg-emerald-100 text-emerald-800'
    case 'medium':
      return 'bg-amber-100 text-amber-800'
    case 'low':
      return 'bg-orange-100 text-orange-700'
    default:
      return 'bg-gray-100 text-gray-500'
  }
}

function tierColor(tier: number) {
  return tier === 1 ? 'bg-rose-100 text-rose-700' : 'bg-blue-100 text-blue-700'
}

function statusColor(status: number) {
  if (status >= 200 && status < 300) return 'bg-emerald-100 text-emerald-800'
  if (status >= 400 && status < 500) return 'bg-amber-100 text-amber-800'
  if (status >= 500) return 'bg-rose-100 text-rose-700'
  return 'bg-gray-100 text-gray-500'
}

type ModelOption = { id: string; ownedBy?: string }

function parseModelsBody(body: string): ModelOption[] {
  try {
    const obj = JSON.parse(body)
    const arr = Array.isArray(obj?.data) ? obj.data : Array.isArray(obj) ? obj : []
    return arr
      .map((m: any) => ({ id: String(m?.id ?? ''), ownedBy: m?.owned_by }))
      .filter((m: ModelOption) => m.id)
  } catch {
    return []
  }
}

function ProbeCard({ probe }: { probe: DetectResult['probes'][number] }) {
  const [open, setOpen] = useState(true)
  const isStream = probe.stream_event_count !== undefined
  const retries = probe.retries ?? 0
  return (
    <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
      <button
        onClick={() => setOpen(v => !v)}
        className="w-full flex items-center justify-between px-4 py-3 border-b border-gray-100 hover:bg-gray-50 text-left"
      >
        <div className="min-w-0 flex-1">
          <div className="text-sm font-semibold text-gray-900 truncate">{probe.label}</div>
          <div className="text-[10px] text-gray-400 mt-1 line-clamp-2">{probe.intent}</div>
        </div>
        <div className="flex items-center gap-2 ml-3 shrink-0">
          <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold ${statusColor(probe.status)}`}>
            HTTP {probe.status || '—'}
          </span>
          {retries > 0 && (
            <span
              className="inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold bg-amber-100 text-amber-800"
              title={`Retried ${retries}× — earlier statuses: ${(probe.retry_history ?? []).join(', ')}`}
            >
              ↻ {retries}
            </span>
          )}
          <span className="text-[10px] text-gray-400 tabular-nums">{probe.elapsed_ms} ms</span>
          {isStream && (
            <span className="text-[10px] text-blue-600 tabular-nums">
              events {probe.stream_event_count} · max gap {probe.stream_max_gap_ms} ms
            </span>
          )}
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className={`text-gray-400 transition-transform ${open ? 'rotate-180' : ''}`}>
            <polyline points="6 9 12 15 18 9" />
          </svg>
        </div>
      </button>
      {open && (
        <div className="p-4 space-y-3">
          <div>
            <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1">Headers</div>
            <pre className="text-[11px] font-mono bg-gray-50 border border-gray-200 rounded-md p-2.5 overflow-x-auto max-h-48 overflow-y-auto whitespace-pre-wrap break-all">
              {Object.keys(probe.headers || {}).length
                ? Object.entries(probe.headers).map(([k, v]) => `${k}: ${v}`).join('\n')
                : '<none>'}
            </pre>
          </div>
          <div>
            <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1">Body</div>
            <pre className="text-[11px] font-mono bg-gray-50 border border-gray-200 rounded-md p-2.5 overflow-x-auto max-h-96 overflow-y-auto whitespace-pre-wrap break-all">
              {prettyJSON(probe.body) || '<empty>'}
            </pre>
          </div>
        </div>
      )}
    </div>
  )
}

function SignalRow({ s }: { s: DetectSignal }) {
  return (
    <div className="flex items-start gap-3 py-2 border-b border-gray-50 last:border-0">
      <span className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold shrink-0 ${tierColor(s.tier)}`}>
        T{s.tier} · {s.code}
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-xs text-gray-700">{s.detail}</div>
        <div className="text-[10px] text-gray-400 mt-0.5">
          {s.layer} → <span className="font-medium text-gray-600">{s.implies}</span>
        </div>
      </div>
    </div>
  )
}

export default function ProviderDetect() {
  const [url, setUrl] = useState('')
  const [key, setKey] = useState('')
  const [model, setModel] = useState(MODEL_DEFAULTS[0])
  const [modelMode, setModelMode] = useState<'preset' | 'custom'>('preset')
  const [customModel, setCustomModel] = useState('')
  const [models, setModels] = useState<ModelOption[]>([])
  const [modelLookup, setModelLookup] = useState<{ status: number; bodyPreview: string } | null>(null)
  const [listing, setListing] = useState(false)
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<DetectResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [intervalMs, setIntervalMs] = useState(500)
  const [maxRetries, setMaxRetries] = useState(2)

  const finalModel = modelMode === 'custom' ? customModel.trim() : model

  const canRun = url.trim() && key.trim() && finalModel.length > 0 && !running

  const handleListModels = async () => {
    setError(null)
    if (!url.trim() || !key.trim()) {
      setError('请填写 URL 和 Key')
      return
    }
    setListing(true)
    try {
      const r = await api.detectModels(url.trim(), key.trim())
      const parsed = parseModelsBody(r.body)
      setModels(parsed)
      setModelLookup({ status: r.status, bodyPreview: r.body.slice(0, 200) })
      if (parsed.length > 0) {
        setModelMode('preset')
        if (!parsed.some(m => m.id === model)) setModel(parsed[0].id)
      }
    } catch (e: any) {
      setError('列模型失败：' + (e?.message || String(e)))
    } finally {
      setListing(false)
    }
  }

  const handleRun = async () => {
    setError(null)
    if (!canRun) return
    setRunning(true)
    setResult(null)
    try {
      const r = await api.detectRun({
        url: url.trim(),
        key: key.trim(),
        model: finalModel,
        interval_ms: Math.max(0, Math.min(60_000, Math.floor(intervalMs) || 0)),
        max_retries: Math.max(0, Math.min(5, Math.floor(maxRetries) || 0)),
      })
      setResult(r)
    } catch (e: any) {
      setError('探测失败：' + (e?.message || String(e)))
    } finally {
      setRunning(false)
    }
  }

  const modelOptions = useMemo(() => {
    if (models.length > 0) return models.map(m => m.id)
    return MODEL_DEFAULTS
  }, [models])

  const actions = (
    <button
      onClick={handleRun}
      disabled={!canRun}
      className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-40 disabled:cursor-not-allowed"
    >
      {running ? '探测中...' : '开始探测'}
    </button>
  )

  return (
    <Layout
      title="Provider Detect"
      subtitle="对 Claude/Anthropic 兼容 endpoint 发送 6 个 probe，识别 router 与 backend 层供应商"
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
            <p className="text-[10px] text-gray-400 mt-1">不要带 /v1/...，只要 base URL</p>
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
            <div className="flex items-center justify-between mb-1.5">
              <label className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">Model</label>
              <button
                onClick={handleListModels}
                disabled={listing || !url.trim() || !key.trim()}
                className="text-[10px] text-blue-600 hover:underline disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {listing ? '查询中...' : '列模型 (GET /v1/models)'}
              </button>
            </div>
            <div className="flex items-center gap-2 mb-2 text-[10px]">
              <label className="flex items-center gap-1 text-gray-500">
                <input
                  type="radio"
                  checked={modelMode === 'preset'}
                  onChange={() => setModelMode('preset')}
                />
                预设/列出
              </label>
              <label className="flex items-center gap-1 text-gray-500">
                <input
                  type="radio"
                  checked={modelMode === 'custom'}
                  onChange={() => setModelMode('custom')}
                />
                手填
              </label>
            </div>
            {modelMode === 'preset' ? (
              <select
                value={model}
                onChange={e => setModel(e.target.value)}
                className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
              >
                {modelOptions.map(m => (
                  <option key={m} value={m}>{m}</option>
                ))}
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
            {modelLookup && (
              <div className="text-[10px] text-gray-400 mt-1.5">
                {models.length > 0
                  ? `已加载 ${models.length} 个模型`
                  : `GET /v1/models HTTP ${modelLookup.status} — ${modelLookup.bodyPreview.slice(0, 80)}...`}
              </div>
            )}
          </div>

          <div className="pt-3 border-t border-gray-100 grid grid-cols-2 gap-3">
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">
                Probe 间隔 (ms)
              </label>
              <input
                type="number"
                min={0}
                max={60000}
                step={100}
                value={intervalMs}
                onChange={e => setIntervalMs(Number(e.target.value))}
                className="w-full border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 tabular-nums"
              />
            </div>
            <div>
              <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">
                429/5xx 重试次数
              </label>
              <input
                type="number"
                min={0}
                max={5}
                step={1}
                value={maxRetries}
                onChange={e => setMaxRetries(Number(e.target.value))}
                className="w-full border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900 tabular-nums"
              />
            </div>
            <p className="col-span-2 text-[10px] text-gray-400 leading-relaxed">
              重试尊重 <code className="bg-gray-100 px-1 rounded">Retry-After</code>，否则退避 1s / 3s / 5s ...
            </p>
          </div>

          {error && (
            <div className="bg-rose-50 border border-rose-100 text-rose-700 text-xs rounded-md px-3 py-2">{error}</div>
          )}

          <div className="text-[10px] text-gray-400 leading-relaxed pt-2 border-t border-gray-100">
            将发送 6 个 probe：GET /v1/models · plain · tools (force tool_use) · stream · huge max_tokens · invalid role
          </div>
        </div>

        <div className="space-y-4">
          {!result && !running && (
            <div className="bg-white border border-gray-200 rounded-xl py-16 text-center text-gray-400 text-xs">
              填写 URL / Key / Model 后点击「开始探测」
            </div>
          )}

          {running && (
            <div className="bg-white border border-gray-200 rounded-xl py-12 text-center">
              <div className="inline-flex items-center gap-2 text-blue-600 text-xs">
                <span className="w-1.5 h-1.5 rounded-full bg-blue-500 animate-pulse" />
                正在依次发送 6 个 probe（每个最长 30s，预计 10–60s）
              </div>
            </div>
          )}

          {result && (
            <>
              <div className="bg-white border border-gray-200 rounded-xl p-4">
                <div className="text-sm font-semibold text-gray-900 mb-3">判别结论</div>
                <div className="grid grid-cols-2 gap-4">
                  <div>
                    <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">Router 层</div>
                    <div className="mt-1 flex items-center gap-2">
                      <span className="text-sm font-semibold text-gray-900">{result.classification.router_label}</span>
                      <span className={`px-2 py-0.5 rounded-full text-[10px] font-semibold ${confidenceColor(result.classification.router_confidence)}`}>
                        {result.classification.router_confidence}
                      </span>
                    </div>
                  </div>
                  <div>
                    <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium">Backend 层</div>
                    <div className="mt-1 flex items-center gap-2">
                      <span className="text-sm font-semibold text-gray-900">{result.classification.backend_label}</span>
                      <span className={`px-2 py-0.5 rounded-full text-[10px] font-semibold ${confidenceColor(result.classification.backend_confidence)}`}>
                        {result.classification.backend_confidence}
                      </span>
                    </div>
                  </div>
                </div>

                {result.classification.notes && result.classification.notes.length > 0 && (
                  <div className="mt-3 bg-amber-50 border border-amber-100 rounded-md p-2.5 text-[11px] text-amber-800 space-y-1">
                    {result.classification.notes.map((n, i) => <div key={i}>{n}</div>)}
                  </div>
                )}

                <div className="mt-4">
                  <div className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1">命中信号 ({result.classification.signals.length})</div>
                  {result.classification.signals.length === 0 ? (
                    <div className="text-xs text-gray-400 italic py-2">无（Tier 1/2 都没匹配上）</div>
                  ) : (
                    <div>
                      {result.classification.signals.map((s, i) => <SignalRow key={i} s={s} />)}
                    </div>
                  )}
                </div>
              </div>

              <div className="space-y-3">
                {result.probes.map((p, i) => <ProbeCard key={i} probe={p} />)}
              </div>
            </>
          )}
        </div>
      </div>
    </Layout>
  )
}
