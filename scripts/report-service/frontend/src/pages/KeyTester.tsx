import { useMemo, useRef, useState } from 'react'
import Layout from '../components/Layout'
import SummaryCards from '../components/SummaryCards'
import { api, KeyTestResult } from '../api'

const DEFAULT_MODEL = 'claude-sonnet-4-6'

const MODELS = [
  'claude-sonnet-4-6',
  'claude-opus-4-7',
  'claude-opus-4-6',
  'claude-haiku-4-5-20251001',
  'claude-sonnet-4-5-20250929',
  'claude-opus-4-5-20251101',
  'claude-opus-4-8',
  'claude-fable-5',
]

function maskKey(k: string) {
  if (k.length <= 12) return k
  return k.slice(0, 10) + '…' + k.slice(-6)
}

export default function KeyTester() {
  const [model, setModel] = useState(DEFAULT_MODEL)
  const [input, setInput] = useState('')
  const [running, setRunning] = useState(false)
  const [results, setResults] = useState<KeyTestResult[]>([])
  const [progress, setProgress] = useState<{ done: number; total: number }>({ done: 0, total: 0 })
  const [error, setError] = useState<string | null>(null)
  const cancelRef = useRef(false)

  const parsedKeys = useMemo(() => {
    const set = new Set<string>()
    input.split('\n').forEach(line => {
      const t = line.trim()
      if (!t || t.startsWith('#')) return
      const first = t.split(/[\s,]+/)[0]
      if (first) set.add(first)
    })
    return Array.from(set)
  }, [input])

  const handleRun = async () => {
    setError(null)
    if (parsedKeys.length === 0) {
      setError('请填写至少一个 Key')
      return
    }
    cancelRef.current = false
    setRunning(true)
    setResults([])
    setProgress({ done: 0, total: parsedKeys.length })
    try {
      for (let i = 0; i < parsedKeys.length; i++) {
        if (cancelRef.current) break
        const k = parsedKeys[i]
        try {
          const res = await api.testKeys([k], model)
          const r = res.results[0]
          if (r) setResults(prev => [...prev, r])
        } catch (e: any) {
          setResults(prev => [
            ...prev,
            { key: k, ok: false, status: 0, latency_ms: 0, error: e.message || String(e) },
          ])
        } finally {
          setProgress(p => ({ done: i + 1, total: p.total }))
        }
      }
    } finally {
      setRunning(false)
    }
  }

  const handleCancel = () => {
    cancelRef.current = true
  }

  const handleClear = () => {
    setInput('')
    setResults([])
    setProgress({ done: 0, total: 0 })
    setError(null)
  }

  const stats = useMemo(() => {
    const total = results.length
    const ok = results.filter(r => r.ok).length
    const fail = total - ok
    const avgLatency = total
      ? Math.round(results.reduce((s, r) => s + r.latency_ms, 0) / total)
      : 0
    return { total, ok, fail, avgLatency }
  }, [results])

  const actions = (
    <>
      <button
        onClick={handleClear}
        disabled={running}
        className="border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50 disabled:opacity-50"
      >
        清空
      </button>
      {running ? (
        <button
          onClick={handleCancel}
          className="bg-rose-600 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85"
        >
          停止 ({progress.done}/{progress.total})
        </button>
      ) : (
        <button
          onClick={handleRun}
          disabled={parsedKeys.length === 0}
          className="bg-gray-900 text-white rounded-md px-3 py-1.5 text-xs hover:opacity-85 disabled:opacity-50"
        >
          开始测试 ({parsedKeys.length})
        </button>
      )}
    </>
  )

  return (
    <Layout
      title="Key Tester"
      subtitle="批量检测 Claude API Key 可用性"
      actions={actions}
    >
      <SummaryCards cards={[
        { label: '总计', value: String(stats.total), color: 'text-gray-900' },
        { label: '可用', value: String(stats.ok), color: 'text-emerald-600' },
        { label: '不可用', value: String(stats.fail), color: 'text-rose-600' },
        { label: '平均延迟', value: stats.avgLatency ? `${stats.avgLatency} ms` : '—', color: 'text-blue-600' },
      ]} />

      <div className="grid grid-cols-[340px_1fr] gap-6 items-start">
        <div className="bg-white border border-gray-200 rounded-xl p-4 space-y-3">
          <div>
            <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">模型</label>
            <select
              value={model}
              onChange={e => setModel(e.target.value)}
              className="w-full border border-gray-200 rounded-md px-2.5 py-2 text-xs bg-gray-50 focus:outline-none focus:border-gray-900"
            >
              {MODELS.map(m => (
                <option key={m} value={m}>{m}</option>
              ))}
            </select>
          </div>

          <div>
            <label className="block text-[10px] uppercase tracking-wider text-gray-400 font-medium mb-1.5">
              Keys <span className="text-gray-300 normal-case">（每行一个）</span>
            </label>
            <textarea
              value={input}
              onChange={e => setInput(e.target.value)}
              rows={16}
              placeholder={'sk-ant-api03-xxxxx\nsk-ant-api03-yyyyy\nsk-ant-api03-zzzzz\n\n# 井号开头为注释'}
              className="w-full border border-gray-200 rounded-md p-2.5 text-xs font-mono resize-y bg-gray-50 focus:outline-none focus:border-gray-900"
            />
            <p className="text-[10px] text-gray-400 mt-2 leading-relaxed">
              将向所选模型发送一次最小请求（1 个 token）检测可用性
            </p>
          </div>

          {error && (
            <div className="bg-rose-50 border border-rose-100 text-rose-700 text-xs rounded-md px-3 py-2">{error}</div>
          )}
        </div>

        <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
          {running && (
            <div className="px-4 py-2 bg-blue-50 border-b border-blue-100 text-[11px] text-blue-700 flex items-center gap-2">
              <span className="w-1.5 h-1.5 rounded-full bg-blue-500 animate-pulse" />
              顺序测试中 ({progress.done} / {progress.total})
            </div>
          )}
          {results.length === 0 && !running ? (
            <div className="text-center text-gray-400 text-xs py-16">
              填入 Key 并点击「开始测试」
            </div>
          ) : (
            <div className="overflow-x-auto max-h-[72vh] overflow-y-auto">
              <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
                <thead>
                  <tr>
                    {['#', '状态', 'Key', 'HTTP', '延迟', '说明'].map(h => (
                      <th
                        key={h}
                        className="sticky top-0 bg-gray-50 px-3 py-2 text-left text-[10px] font-medium uppercase tracking-wider text-gray-400 border-b border-gray-200"
                      >
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {results.map((r, i) => (
                    <tr key={i} className="hover:bg-gray-50">
                      <td className="px-3 py-1.5 border-b border-gray-50 text-gray-400 tabular-nums">{i + 1}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50">
                        <span
                          className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-semibold ${
                            r.ok ? 'bg-emerald-100 text-emerald-800' : 'bg-rose-100 text-rose-700'
                          }`}
                        >
                          <span
                            className={`w-1.5 h-1.5 rounded-full ${r.ok ? 'bg-emerald-500' : 'bg-rose-500'}`}
                          />
                          {r.ok ? '可用' : '不可用'}
                        </span>
                      </td>
                      <td className="px-3 py-1.5 border-b border-gray-50 font-mono text-gray-500">{maskKey(r.key)}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums">{r.status || '—'}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50 text-right tabular-nums text-gray-500">{r.latency_ms ? `${r.latency_ms} ms` : '—'}</td>
                      <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 max-w-[420px] truncate" title={r.error || r.message || ''}>
                        {r.error || r.message || (r.ok ? 'OK' : '')}
                      </td>
                    </tr>
                  ))}
                  {running && (
                    <tr>
                      <td colSpan={6} className="px-3 py-2 text-xs text-gray-400 italic">
                        正在测试 #{progress.done + 1} / {progress.total} …
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>
    </Layout>
  )
}
