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

export default function BatchCreatePanel({ onCreated, lockedStudio, canConfigureModels = true }: Props) {
  const [studio, setStudio] = useState(lockedStudio ?? '')
  const [studioMode, setStudioMode] = useState<'pick' | 'new'>('pick')
  const [suffix, setSuffix] = useState('')
  const [costInput, setCostInput] = useState('')          // per-key 上游单价 (CNY)
  const [priorityInput, setPriorityInput] = useState('')  // channels.priority
  const [input, setInput] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [result, setResult] = useState<string | null>(null)
  const [studios, setStudios] = useState<string[]>([])

  // 可配置的默认 model 列表（存在 report_config 里）
  const [modelsCfg, setModelsCfg] = useState('')
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
    void (async () => {
      try {
        const res = await api.getBatchCreateModels()
        setModelsCfg(res.models)
      } catch { /* fall back to placeholder */ }
    })()
  }, [studioLocked])

  const handleSaveModels = async () => {
    setModelsMsg(null)
    setModelsSaving(true)
    try {
      const res = await api.saveBatchCreateModels(modelsCfg)
      setModelsCfg(res.models)
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
    const channels: { key: string; quota_usd: number }[] = []
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
    const defaults: { priority?: number; unit_price_cny?: number } = {}
    if (costInput.trim()) {
      const c = parseFloat(costInput.trim())
      if (!isNaN(c) && c > 0) defaults.unit_price_cny = c
    }
    if (priorityInput.trim()) {
      const p = parseInt(priorityInput.trim(), 10)
      if (!isNaN(p) && p > 0) defaults.priority = p
    }
    setSubmitting(true)
    try {
      const res = await api.batchCreateChannels(studio.trim(), suffix.trim(), channels, defaults)
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
          <label className="block text-[11px] text-gray-500 mb-1">默认优先级</label>
          <input
            type="number"
            step="1"
            min="1"
            value={priorityInput}
            onChange={e => setPriorityInput(e.target.value)}
            placeholder="例如 2，空=默认 1001"
            className="w-full border border-gray-200 rounded-md px-2 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
          />
        </div>
      </div>
      {/* 可折叠：默认模型列表配置。改了会作用到之后所有批量创建。
          Studio Operator (canConfigureModels=false) 只显示当前列表、不允许改。 */}
      <div className="border border-gray-200 rounded-md mb-2 bg-gray-50/50">
        <button
          type="button"
          onClick={() => setModelsCfgOpen(v => !v)}
          className="w-full flex items-center justify-between px-2.5 py-1.5 text-[11px] text-gray-600 hover:bg-gray-100"
        >
          <span>默认模型列表 <span className="text-gray-400">({modelsCfg.split(',').filter(Boolean).length} 个)</span></span>
          <span className="text-gray-400">{modelsCfgOpen ? '▲' : '▼'}</span>
        </button>
        {modelsCfgOpen && (
          <div className="p-2.5 pt-1 space-y-2">
            <textarea
              value={modelsCfg}
              onChange={e => canConfigureModels && setModelsCfg(e.target.value)}
              readOnly={!canConfigureModels}
              rows={4}
              placeholder={'一行或逗号分隔一个模型名，例如\nclaude-opus-4-7,claude-sonnet-4-6,claude-opus-4-6'}
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
      <textarea
        value={input}
        onChange={e => setInput(e.target.value)}
        rows={8}
        placeholder={'每行: key 额度（USD）\n\nsk-ant-api03-xxxx 220\nsk-ant-api03-yyyy 500'}
        className="w-full border border-gray-200 rounded-md p-2.5 text-xs font-mono resize-y bg-gray-50 focus:outline-none focus:border-gray-900"
      />
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
