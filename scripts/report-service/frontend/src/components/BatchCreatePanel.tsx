import { useState, useEffect } from 'react'
import { api } from '../api'

type Props = {
  onCreated: () => void
  // Studio Operator has a fixed studio bound to their account. When set,
  // the studio field is rendered as read-only text and the studios dropdown
  // is skipped entirely (they don't have permission to list studios anyway).
  lockedStudio?: string
  // Admins can configure the default model list; the operator role cannot.
  // Hiding the entire config UI keeps the panel focused for them.
  canConfigureModels?: boolean
}

// Provider presets for the local batch-create panel. Mirrors the shape used
// by RemoteChannelsStudio.tsx — same integers, same groups, same fallback
// model lists — but the two components stay standalone on purpose (remote
// upload vs local channel insert diverge downstream).
type PresetID = 'anthropic' | 'openai' | 'azure' | 'gemini' | 'vertex'
type PresetSpec = {
  id: PresetID
  label: string
  kind: 'text' | 'vertex'
  type: number
  fallbackGroup: string
  fallbackModels: string
}
const DEFAULT_ANTHROPIC_MODELS = [
  'claude-sonnet-5',
  'claude-opus-4-8',
  'claude-opus-4-7',
  'claude-sonnet-4-6',
  'claude-opus-4-6',
  'claude-haiku-4-5-20251001',
  'claude-sonnet-4-5-20250929',
  'claude-opus-4-5-20251101',
].join(',')
const DEFAULT_OPENAI_MODELS = [
  'gpt-5',
  'gpt-5-mini',
  'gpt-5-nano',
  'gpt-4.1',
  'gpt-4o',
  'gpt-4o-mini',
  'o4-mini',
  'o3',
].join(',')
const DEFAULT_GEMINI_MODELS = [
  'gemini-2.5-flash',
  'gemini-2.5-pro',
  'gemini-3-flash-preview',
  'gemini-3-pro-image',
  'gemini-3.1-flash-lite',
  'gemini-3.5-flash',
].join(',')
// Vertex ships Google's Gemini family (Claude-on-Vertex uses a different
// upload flow), so the default deployment list mirrors DEFAULT_GEMINI_MODELS.
const DEFAULT_VERTEX_MODELS = DEFAULT_GEMINI_MODELS

const PRESETS: PresetSpec[] = [
  { id: 'anthropic', label: 'Anthropic', kind: 'text',   type: 14, fallbackGroup: 'default', fallbackModels: DEFAULT_ANTHROPIC_MODELS },
  { id: 'openai',    label: 'OpenAI',    kind: 'text',   type: 1,  fallbackGroup: 'openai',  fallbackModels: DEFAULT_OPENAI_MODELS },
  { id: 'azure',     label: 'Azure',     kind: 'text',   type: 3,  fallbackGroup: 'openai',  fallbackModels: DEFAULT_OPENAI_MODELS },
  { id: 'gemini',    label: 'Gemini',    kind: 'text',   type: 24, fallbackGroup: 'gemini',  fallbackModels: DEFAULT_GEMINI_MODELS },
  { id: 'vertex',    label: 'Vertex AI', kind: 'vertex', type: 41, fallbackGroup: 'gemini',  fallbackModels: DEFAULT_VERTEX_MODELS },
]

// Azure only: default API version. Mirrors AZURE_DEFAULT_API_VERSION on the
// backend so batch-created channels don't drift from admin-UI ones.
const AZURE_DEFAULT_API_VERSION = '2025-04-01-preview'

// One parsed Service Account JSON in the Vertex upload UI. Files are
// parsed on selection so JSON validation errors surface before submit.
type VertexFile = { name: string; json: unknown; quotaUSD: number }

export default function BatchCreatePanel({ onCreated, lockedStudio, canConfigureModels = true }: Props) {
  const [studio, setStudio] = useState(lockedStudio ?? '')
  const [studioMode, setStudioMode] = useState<'pick' | 'new'>('pick')
  const [suffix, setSuffix] = useState('')
  const [costInput, setCostInput] = useState('')          // per-key 上游单价 (CNY)
  const [priorityInput, setPriorityInput] = useState('')  // channels.priority
  // 'same' = every key uses `priorityInput` as-is.
  // 'desc' = key[i] gets priorityInput - i (higher priority on the earlier keys).
  // 'asc'  = key[i] gets priorityInput + i.
  const [prioMode, setPrioMode] = useState<'same' | 'desc' | 'asc'>('same')
  const [input, setInput] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [result, setResult] = useState<string | null>(null)
  const [studios, setStudios] = useState<string[]>([])

  // Provider preset. Empty = 'anthropic' (backward-compatible default;
  // pre-rc.150 the batch-create endpoint only produced Anthropic channels).
  // Changing this rewrites `models` and `group` unless the user has hand-
  // edited them (tracked by `modelsDirty` / `groupDirty`).
  const [presetID, setPresetID] = useState<PresetID>('anthropic')
  const preset = PRESETS.find(p => p.id === presetID) ?? PRESETS[0]
  const [models, setModels] = useState(DEFAULT_ANTHROPIC_MODELS)
  const [group, setGroup] = useState('default')
  const [modelsDirty, setModelsDirty] = useState(false)
  const [groupDirty, setGroupDirty] = useState(false)
  // Vertex-only state. Region is the newapi channel.other value — either a
  // bare region string or a JSON model→region map. Defaults to "global" so
  // multi-region Gemini deployments work without extra config. Files are
  // pre-parsed on selection to reject invalid JSON before submit and to
  // ship each SA JSON straight to the backend without a re-read.
  const [region, setRegion] = useState('global')
  const [vertexFiles, setVertexFiles] = useState<VertexFile[]>([])
  // vertexKeyMode selects the Vertex auth flavor written to
  // channels.settings.vertex_key_type — 'json' (default) uploads Service
  // Account JSON files, 'api_key' uploads plain Vertex Express API keys
  // parsed from `vertexKeysText`.
  const [vertexKeyMode, setVertexKeyMode] = useState<'json' | 'api_key'>('json')
  const [vertexKeysText, setVertexKeysText] = useState('')
  // Azure-only state. base_url is the resource endpoint (channels.base_url)
  // and apiVersion is channels.other. The whole batch shares one resource.
  const [azureBaseUrl, setAzureBaseUrl] = useState('')
  const [azureApiVersion, setAzureApiVersion] = useState(AZURE_DEFAULT_API_VERSION)

  // 可配置的默认 model 列表 —— 按预设分开存（rc.154+）。key 是 preset.id，
  // value 是服务端保存的 models 字符串；'' 或缺失表示尚未保存过（前端会
  // 回退到 preset.fallbackModels）。切换预设时按需拉取，避免打开面板就
  // 打四次接口。
  const [modelsByPreset, setModelsByPreset] = useState<Partial<Record<PresetID, string>>>({})
  const modelsCfg = modelsByPreset[presetID] ?? ''
  const [modelsCfgOpen, setModelsCfgOpen] = useState(false)
  const [modelsSaving, setModelsSaving] = useState(false)
  const [modelsMsg, setModelsMsg] = useState<string | null>(null)

  const studioLocked = !!lockedStudio

  useEffect(() => {
    if (!studioLocked) {
      void (async () => {
        try {
          const res = await api.listStudios()
          setStudios(res.studios)
          // Pre-select the first existing studio, or pipi if it exists, so
          // the operator doesn't have to click before the form is usable.
          setStudio(prev => {
            if (prev) return prev
            if (res.studios.includes('pipi')) return 'pipi'
            return res.studios[0] ?? ''
          })
        } catch { /* leave dropdown empty on error */ }
      })()
    }
  }, [studioLocked])

  // Fetch the saved default-models list for the currently-selected preset
  // if we haven't cached it yet. Lazy per-preset so opening the panel only
  // hits /config/batch-models once per type used. Server empty string is
  // memoised so we don't refetch the "never saved" case.
  useEffect(() => {
    if (modelsByPreset[presetID] !== undefined) return
    void (async () => {
      try {
        const res = await api.getBatchCreateModels(preset.type)
        setModelsByPreset(prev => ({ ...prev, [presetID]: res.models ?? '' }))
      } catch {
        setModelsByPreset(prev => ({ ...prev, [presetID]: '' }))
      }
    })()
  }, [presetID, preset.type, modelsByPreset])

  // Re-seed models + group whenever the preset changes, unless the operator
  // has hand-edited them (dirty flag). Prefers the server-saved list for
  // the current preset; falls back to the baked list if none saved.
  useEffect(() => {
    if (!modelsDirty) {
      setModels(modelsCfg && modelsCfg.trim() ? modelsCfg : preset.fallbackModels)
    }
    if (!groupDirty) {
      setGroup(preset.fallbackGroup)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [presetID, modelsCfg])

  // File picker → parsed Vertex JSON list. Skip invalid files with a
  // console warning rather than throwing — matches the remote Vertex
  // upload's partial-success semantics.
  const onPickVertexFiles = async (list: FileList | null) => {
    if (!list || list.length === 0) return
    const parsed: VertexFile[] = []
    const errors: string[] = []
    for (const f of Array.from(list)) {
      try {
        const txt = await f.text()
        const json = JSON.parse(txt)
        parsed.push({ name: f.name, json, quotaUSD: 5 })
      } catch (e: any) {
        errors.push(`${f.name}: ${e?.message || 'JSON 解析失败'}`)
      }
    }
    setVertexFiles(prev => [...prev, ...parsed])
    if (errors.length) setResult('部分文件解析失败: ' + errors.join('; '))
  }

  const handleSaveModels = async () => {
    setModelsMsg(null)
    setModelsSaving(true)
    try {
      // Persist the currently-displayed list for the active preset. Passing
      // preset.type keeps each preset's saved list in its own report_config
      // row so switching Anthropic → OpenAI doesn't clobber the other's
      // saved defaults.
      const res = await api.saveBatchCreateModels(modelsCfg, preset.type)
      setModelsByPreset(prev => ({ ...prev, [presetID]: res.models }))
      setModelsMsg('已保存')
    } catch (e: any) {
      setModelsMsg('失败: ' + (e?.message || e))
    } finally {
      setModelsSaving(false)
    }
  }

  const handleSubmit = async () => {
    setResult(null)
    // When studio is locked, we trust the JWT-side enforcement and just
    // pass whatever we have; the server will substitute the user's bound
    // studio anyway. When editable, keep the client-side guard.
    if (!studioLocked && !studio.trim()) {
      setResult('请填写工作室名')
      return
    }
    if (!suffix.trim()) {
      setResult('请填写后缀')
      return
    }

    // Common defaults shared by text presets and Vertex.
    const baseDefaults: {
      priority?: number
      unit_price_cny?: number
      type?: number
      group?: string
      models?: string
      other?: string
      settings?: string
      base_url?: string
    } = {
      type: preset.type,
      group: group.trim() || preset.fallbackGroup,
      models: models.trim() || preset.fallbackModels,
    }
    if (costInput.trim()) {
      const c = parseFloat(costInput.trim())
      if (!isNaN(c) && c > 0) baseDefaults.unit_price_cny = c
    }
    if (preset.id === 'azure') {
      const url = azureBaseUrl.trim()
      if (!url) return setResult('Azure 需要 base_url (例: https://<resource>.openai.azure.com)')
      baseDefaults.base_url = url
      baseDefaults.other = azureApiVersion.trim() || AZURE_DEFAULT_API_VERSION
    }

    // Vertex takes JSON files or plain API keys + a shared region, so it
    // branches to a different input parser here but shares the same
    // batchCreateChannels wire call. In JSON mode `key` is the raw SA
    // JSON string; in API-key mode it's the plain Vertex Express API key.
    // The backend stores it verbatim in channels.key (a TEXT column) and
    // stamps channels.settings.vertex_key_type per the mode.
    if (preset.kind === 'vertex') {
      baseDefaults.other = region.trim() || 'global'
      let channels: { key: string; quota_usd: number }[]
      if (vertexKeyMode === 'api_key') {
        baseDefaults.settings = '{"vertex_key_type":"api_key"}'
        channels = []
        for (const raw of vertexKeysText.split('\n')) {
          const t = raw.trim()
          if (!t || t.startsWith('#')) continue
          const parts = t.split(/[\s,]+/)
          if (parts.length < 2) continue
          const q = parseFloat(parts[1])
          if (!parts[0] || isNaN(q) || q <= 0) continue
          channels.push({ key: parts[0], quota_usd: q })
        }
        if (channels.length === 0) return setResult('未解析到有效行')
      } else {
        baseDefaults.settings = '{"vertex_key_type":"json"}'
        if (vertexFiles.length === 0) return setResult('请至少选择一个 Service Account JSON 文件')
        channels = vertexFiles.map(f => ({
          key: JSON.stringify(f.json),
          quota_usd: f.quotaUSD > 0 ? f.quotaUSD : 5,
        }))
      }
      const basePriority = priorityInput.trim() ? parseInt(priorityInput.trim(), 10) : NaN
      if (!isNaN(basePriority) && basePriority > 0) baseDefaults.priority = basePriority
      setSubmitting(true)
      try {
        const res = await api.batchCreateChannels(studio.trim(), suffix.trim(), channels, baseDefaults)
        setResult(`成功创建 ${res.count} 个 Vertex 渠道`)
        if (vertexKeyMode === 'api_key') {
          setVertexKeysText('')
        } else {
          setVertexFiles([])
        }
        onCreated()
      } catch (e: any) {
        setResult(`失败: ${e.message || e}`)
      } finally {
        setSubmitting(false)
      }
      return
    }

    const channels: { key: string; quota_usd: number; priority?: number }[] = []
    input.split('\n').forEach(line => {
      const t = line.trim()
      if (!t || t.startsWith('#')) return
      const parts = t.split(/[\s,]+/)
      if (parts.length < 2) return
      const q = parseFloat(parts[1])
      if (!parts[0] || isNaN(q) || q <= 0) return
      channels.push({ key: parts[0], quota_usd: q })
    })
    if (channels.length === 0) {
      setResult('未解析到有效行')
      return
    }

    // Sequential-priority mode assigns per-channel priorities BEFORE the
    // request goes out. In 'same' mode we leave channels[i].priority unset
    // and rely on the batch-level `defaults.priority` (existing behaviour).
    const basePriority = priorityInput.trim() ? parseInt(priorityInput.trim(), 10) : NaN
    if (!isNaN(basePriority) && basePriority > 0) {
      if (prioMode === 'same') {
        baseDefaults.priority = basePriority
      } else {
        const step = prioMode === 'desc' ? -1 : 1
        for (let i = 0; i < channels.length; i++) {
          // Never emit a non-positive priority — new-api treats those as
          // "unset" and falls back to legacy default. Clamp at 1 instead.
          const p = Math.max(1, basePriority + i * step)
          channels[i].priority = p
        }
      }
    }
    setSubmitting(true)
    try {
      const res = await api.batchCreateChannels(studio.trim(), suffix.trim(), channels, baseDefaults)
      setResult(`成功创建 ${res.count} 个渠道`)
      setInput('')
      onCreated()
    } catch (e: any) {
      setResult(`失败: ${e.message || e}`)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="bg-white border border-gray-200 rounded-xl p-4">
      <h2 className="text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-3">批量创建渠道</h2>
      <div className="mb-3">
        <label className="block text-[11px] text-gray-500 mb-1">渠道类型</label>
        <div className="inline-flex rounded-md border border-gray-300 overflow-hidden">
          {PRESETS.map(p => {
            const active = presetID === p.id
            return (
              <button
                key={p.id}
                type="button"
                onClick={() => {
                  setPresetID(p.id)
                  // Reset dirty flags on switch so the new preset's
                  // fallback models/group re-seed; the user can still
                  // edit after the seed and it'll stick until they
                  // switch presets again.
                  setModelsDirty(false)
                  setGroupDirty(false)
                }}
                className={`px-3 py-1 text-xs border-r border-gray-200 last:border-r-0 transition-colors ${
                  active ? 'bg-gray-900 text-white' : 'bg-white text-gray-700 hover:bg-gray-50'
                }`}
              >
                {p.label}
              </button>
            )
          })}
        </div>
      </div>
      <div className="grid grid-cols-2 gap-2 mb-2">
        <div>
          <label className="block text-[11px] text-gray-500 mb-1">工作室</label>
          {studioLocked ? (
            <div
              className="w-full border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-100 text-gray-700 cursor-not-allowed"
              title="此账号已绑定工作室，不可切换"
            >
              {lockedStudio}
            </div>
          ) : studioMode === 'pick' ? (
            <select
              value={studios.includes(studio) ? studio : ''}
              onChange={e => {
                if (e.target.value === '__NEW__') {
                  setStudioMode('new')
                  setStudio('')
                } else {
                  setStudio(e.target.value)
                }
              }}
              className="w-full border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
            >
              {studios.length === 0 && <option value="">（无现有工作室）</option>}
              {studios.map(s => <option key={s} value={s}>{s}</option>)}
              <option value="__NEW__">+ 新建工作室…</option>
            </select>
          ) : (
            <div className="flex gap-1">
              <input
                value={studio}
                onChange={e => setStudio(e.target.value)}
                placeholder="新工作室名称"
                autoFocus
                className="flex-1 border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
              />
              <button
                type="button"
                onClick={() => { setStudioMode('pick'); setStudio(studios[0] ?? '') }}
                className="border border-gray-200 rounded-md px-2 text-xs text-gray-500 hover:bg-gray-50"
                title="返回选择"
              >×</button>
            </div>
          )}
        </div>
        <div>
          <label className="block text-[11px] text-gray-500 mb-1">后缀</label>
          <input
            value={suffix}
            onChange={e => setSuffix(e.target.value)}
            placeholder="例如 a / 5"
            className="w-full border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
          />
        </div>
      </div>
      <div className="grid grid-cols-2 gap-2 mb-2">
        <div>
          <label className="block text-[11px] text-gray-500 mb-1">默认成本 (CNY/USD 上游单价)</label>
          <input
            type="number"
            step="0.01"
            min="0"
            value={costInput}
            onChange={e => setCostInput(e.target.value)}
            placeholder="例如 4.3，空=不写"
            className="w-full border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
          />
        </div>
        <div>
          <label className="block text-[11px] text-gray-500 mb-1">
            默认优先级
            <span className="text-gray-400 font-normal">
              {prioMode === 'desc' && '（起始值 base，key[i] = base − i）'}
              {prioMode === 'asc' && '（起始值 base，key[i] = base + i）'}
            </span>
          </label>
          <div className="flex gap-1">
            <input
              type="number"
              step="1"
              min="1"
              value={priorityInput}
              onChange={e => setPriorityInput(e.target.value)}
              placeholder={prioMode === 'same' ? '例如 2，空=默认 1001' : '起始 base'}
              className="flex-1 border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
            />
            <select
              value={prioMode}
              onChange={e => setPrioMode(e.target.value as 'same' | 'desc' | 'asc')}
              className="border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-white focus:outline-none focus:border-gray-900"
              title="统一 = 所有 key 用同一 priority；顺序 = 每个 key 依次递减/递增"
            >
              <option value="same">统一</option>
              <option value="desc">顺序 ↓</option>
              <option value="asc">顺序 ↑</option>
            </select>
          </div>
        </div>
      </div>
      {/* 可折叠：默认模型列表配置。改了只作用到当前预设 —— 每个 preset
          (Anthropic / OpenAI / Gemini / Vertex) 各自有一条 report_config 记录。
          Studio Operator (canConfigureModels=false) 只显示当前列表、不允许改。 */}
      <div className="border border-gray-200 rounded-md mb-2 bg-gray-50/50">
        <button
          type="button"
          onClick={() => setModelsCfgOpen(v => !v)}
          className="w-full flex items-center justify-between px-2.5 py-1.5 text-[11px] text-gray-600 hover:bg-gray-100"
        >
          <span>
            {preset.label} 默认模型列表{' '}
            <span className="text-gray-400">({modelsCfg.split(',').filter(Boolean).length} 个)</span>
          </span>
          <span className="text-gray-400">{modelsCfgOpen ? '▲' : '▼'}</span>
        </button>
        {modelsCfgOpen && (
          <div className="p-2.5 pt-1 space-y-2">
            <textarea
              value={modelsCfg}
              onChange={e => {
                if (!canConfigureModels) return
                const v = e.target.value
                setModelsByPreset(prev => ({ ...prev, [presetID]: v }))
              }}
              readOnly={!canConfigureModels}
              rows={4}
              placeholder={preset.fallbackModels}
              className={`w-full border border-gray-200 rounded-md p-2 text-[11px] font-mono resize-y focus:outline-none focus:border-gray-900 ${
                canConfigureModels ? 'bg-white' : 'bg-gray-100 cursor-not-allowed text-gray-600'
              }`}
            />
            {canConfigureModels && (
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={handleSaveModels}
                  disabled={modelsSaving}
                  className="bg-gray-900 text-white rounded-md px-3 py-1 text-[11px] hover:opacity-85 disabled:opacity-40"
                >
                  {modelsSaving ? '保存中…' : '保存默认模型'}
                </button>
                {modelsMsg && (
                  <span className={`text-[11px] ${modelsMsg === '已保存' ? 'text-emerald-600' : 'text-rose-600'}`}>{modelsMsg}</span>
                )}
              </div>
            )}
            {!canConfigureModels && (
              <p className="text-[10px] text-gray-400">此列表由管理员维护，本账号只读。</p>
            )}
          </div>
        )}
      </div>
      {/* Group + Models — per-preset overridable. Anthropic reads the
          server-side configurable list (via the collapsible section
          above); other presets seed from the baked fallback. Both
          fields flip a dirty flag so a manual edit sticks across
          re-renders — until the operator switches presets, at which
          point the seed re-runs. */}
      <div className="grid grid-cols-2 gap-2 mb-2">
        <div>
          <label className="block text-[11px] text-gray-500 mb-1">Group（写入 channels."group"）</label>
          <input
            value={group}
            onChange={e => { setGroup(e.target.value); setGroupDirty(true) }}
            placeholder={preset.fallbackGroup}
            className="w-full border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
          />
        </div>
        <div>
          <label className="block text-[11px] text-gray-500 mb-1">Models（逗号分隔）</label>
          <textarea
            value={models}
            onChange={e => { setModels(e.target.value); setModelsDirty(true) }}
            rows={1}
            placeholder={preset.fallbackModels.slice(0, 60) + '…'}
            className="w-full border border-gray-200 rounded-md px-2 py-1 text-[11px] font-mono focus:outline-none focus:border-gray-900 bg-gray-50"
          />
        </div>
      </div>

      {preset.kind === 'vertex' ? (
        <div className="space-y-2 border border-dashed border-gray-300 rounded-md p-3 bg-gray-50/50">
          <div>
            <label className="block text-[11px] text-gray-500 mb-1">Auth Mode</label>
            <div className="inline-flex rounded-md border border-gray-300 overflow-hidden">
              {(
                [
                  { id: 'json',    label: 'Service Account JSON' },
                  { id: 'api_key', label: 'API Key' },
                ] as { id: 'json' | 'api_key'; label: string }[]
              ).map(m => {
                const active = vertexKeyMode === m.id
                return (
                  <button
                    key={m.id}
                    type="button"
                    onClick={() => setVertexKeyMode(m.id)}
                    className={`px-3 py-1 text-[11px] border-r border-gray-200 last:border-r-0 transition-colors ${
                      active ? 'bg-gray-900 text-white' : 'bg-white text-gray-700 hover:bg-gray-50'
                    }`}
                  >
                    {m.label}
                  </button>
                )
              })}
            </div>
            <p className="text-[10px] text-gray-400 mt-1">
              JSON 走 Bearer Token 鉴权；API Key 走 <code className="font-mono">?key=</code> URL 鉴权。写进 channels.settings 的 <code className="font-mono">vertex_key_type</code>。
            </p>
          </div>
          <div>
            <label className="block text-[11px] text-gray-500 mb-1">
              Deployment Region <span className="text-rose-500">*</span>
            </label>
            <input
              value={region}
              onChange={e => setRegion(e.target.value)}
              placeholder="global"
              className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-sm font-mono focus:outline-none focus:border-gray-900"
            />
            <p className="text-[10px] text-gray-400 mt-1">
              输入部署区域或 JSON 映射：<code className="font-mono">{'{"default": "us-central1", "claude-3-5-sonnet-20240620": "europe-west1"}'}</code>。默认 <code className="font-mono">global</code>。写进 channels.other，本批次共用。
            </p>
          </div>
          {vertexKeyMode === 'json' ? (
            <div>
              <label className="block text-[11px] text-gray-500 mb-1">Service Account JSON 文件（可多选）</label>
              <input
                type="file"
                accept=".json,application/json"
                multiple
                onChange={e => { onPickVertexFiles(e.target.files); e.target.value = '' }}
                className="block w-full text-[11px] text-gray-700 file:mr-3 file:py-1 file:px-2 file:rounded file:border file:border-gray-300 file:text-[11px] file:bg-gray-50 file:hover:bg-gray-100"
              />
              {vertexFiles.length > 0 && (
                <ul className="mt-2 divide-y divide-gray-100 border border-gray-200 rounded-md bg-white">
                  {vertexFiles.map((f, i) => (
                    <li key={i} className="px-3 py-2 flex items-center gap-2 text-[11px]">
                      <span className="flex-1 truncate font-mono text-gray-700" title={f.name}>{f.name}</span>
                      <label className="text-[10px] text-gray-500">quota</label>
                      <input
                        type="number"
                        step="0.01"
                        min="0"
                        value={f.quotaUSD}
                        onChange={e => {
                          const v = parseFloat(e.target.value)
                          setVertexFiles(prev => prev.map((x, j) => j === i ? { ...x, quotaUSD: isNaN(v) ? 0 : v } : x))
                        }}
                        className="w-20 border border-gray-300 rounded px-1.5 py-0.5 text-[11px] tabular-nums focus:outline-none focus:border-gray-900"
                      />
                      <button
                        type="button"
                        onClick={() => setVertexFiles(prev => prev.filter((_, j) => j !== i))}
                        className="text-rose-600 hover:underline"
                      >删除</button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          ) : (
            <div>
              <label className="block text-[11px] text-gray-500 mb-1">
                Vertex API Keys —— 每行 <code className="text-gray-700 bg-gray-100 px-1">key 额度USD</code>
              </label>
              <textarea
                value={vertexKeysText}
                onChange={e => setVertexKeysText(e.target.value)}
                rows={6}
                placeholder={'AIzaSy... 220\nAIzaSy... 500'}
                className="w-full border border-gray-300 rounded-md p-2 text-[11px] font-mono resize-y bg-white focus:outline-none focus:border-gray-900"
              />
              <p className="text-[10px] text-gray-400 mt-1">
                额度必填，用作命名与初始额度。key 明文只走一次 POST，不落本地。
              </p>
            </div>
          )}
        </div>
      ) : (
        <>
          {preset.id === 'azure' && (
            <div className="mb-2 grid grid-cols-2 gap-2">
              <div>
                <label className="block text-[11px] text-gray-500 mb-1">
                  Resource Endpoint <span className="text-rose-500">*</span>
                </label>
                <input
                  value={azureBaseUrl}
                  onChange={e => setAzureBaseUrl(e.target.value)}
                  placeholder="https://<resource>.openai.azure.com"
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-xs font-mono focus:outline-none focus:border-gray-900"
                />
                <p className="text-[10px] text-gray-400 mt-1">写进 channels.base_url，本批次共用。</p>
              </div>
              <div>
                <label className="block text-[11px] text-gray-500 mb-1">API Version</label>
                <input
                  value={azureApiVersion}
                  onChange={e => setAzureApiVersion(e.target.value)}
                  placeholder={AZURE_DEFAULT_API_VERSION}
                  className="w-full border border-gray-300 rounded-md px-2 py-1.5 text-xs font-mono focus:outline-none focus:border-gray-900"
                />
                <p className="text-[10px] text-gray-400 mt-1">写进 channels.other，缺省 {AZURE_DEFAULT_API_VERSION}。</p>
              </div>
            </div>
          )}
          <textarea
            value={input}
            onChange={e => setInput(e.target.value)}
            rows={8}
            placeholder={'每行: key 额度（USD）\n\nsk-... 220\nsk-... 500'}
            className="w-full border border-gray-200 rounded-md p-2.5 text-xs font-mono resize-y bg-gray-50 focus:outline-none focus:border-gray-900"
          />
        </>
      )}
      <p className="text-[10px] text-gray-400 mt-2 leading-relaxed">
        命名 MMDD-工作室-后缀-容量；上方"默认成本/优先级"会写到所有新建渠道；channels.tag 用作 user 角色可见范围
        {!studioLocked && studios.length > 0 && <>。已有：{studios.join('、')}</>}
      </p>
      <button
        onClick={handleSubmit}
        disabled={submitting}
        className="mt-3 w-full bg-emerald-600 text-white rounded-md py-1.5 text-sm font-medium hover:opacity-85 disabled:opacity-50"
      >
        {submitting ? '创建中...' : '批量创建'}
      </button>
      {result && (
        <p className={`text-[11px] mt-2 ${result.startsWith('成功') ? 'text-emerald-600' : 'text-rose-600'}`}>{result}</p>
      )}
    </div>
  )
}
