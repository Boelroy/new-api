import { useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import SummaryCards from '../components/SummaryCards'
import { api, ErrorFacets, ErrorRow } from '../api'

const WINDOWS: { label: string; sec: number }[] = [
  { label: '最近 15 分钟', sec: 15 * 60 },
  { label: '最近 1 小时', sec: 3600 },
  { label: '最近 6 小时', sec: 6 * 3600 },
  { label: '最近 24 小时', sec: 24 * 3600 },
  { label: '最近 7 天', sec: 7 * 24 * 3600 },
]

const PAGE_SIZE = 50

function fmtTime(ts: number) {
  const d = new Date(ts * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getMonth() + 1}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function statusColor(code: string) {
  if (code.startsWith('4')) return 'bg-amber-100 text-amber-800'
  if (code.startsWith('5')) return 'bg-rose-100 text-rose-700'
  return 'bg-gray-100 text-gray-600'
}

export default function ErrorCenter() {
  const [windowSec, setWindowSec] = useState(3600)
  const [group, setGroup] = useState('')
  const [statusCode, setStatusCode] = useState('')
  const [errorCode, setErrorCode] = useState('')
  const [search, setSearch] = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')

  const [facets, setFacets] = useState<ErrorFacets | null>(null)
  const [rows, setRows] = useState<ErrorRow[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Debounce the free-text search so we don't hit the backend on every keystroke.
  useEffect(() => {
    const t = setTimeout(() => setDebouncedSearch(search.trim()), 350)
    return () => clearTimeout(t)
  }, [search])

  // Structural filters that scope both facets and the list. Changing any of
  // them resets to page 1.
  const structural = useMemo(
    () => ({ window_sec: windowSec, group, model: '', channel_id: undefined as number | undefined }),
    [windowSec, group],
  )

  useEffect(() => {
    setPage(1)
  }, [windowSec, group, statusCode, errorCode, debouncedSearch])

  // Facets follow the window + group (status/error code lists narrow by group).
  useEffect(() => {
    let alive = true
    api
      .errorFacets(structural)
      .then(f => {
        if (alive) setFacets(f)
      })
      .catch(() => {
        if (alive) setFacets(null)
      })
    return () => {
      alive = false
    }
  }, [structural])

  // If the selected code disappears from the new facet scope, clear it.
  useEffect(() => {
    if (!facets) return
    if (statusCode && !facets.status_codes.some(s => s.value === statusCode)) setStatusCode('')
    if (errorCode && !facets.error_codes.some(e => e.value === errorCode)) setErrorCode('')
  }, [facets]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    let alive = true
    setLoading(true)
    setError(null)
    api
      .listErrors({
        window_sec: windowSec,
        group,
        status_code: statusCode,
        error_code: errorCode,
        q: debouncedSearch,
        page,
        page_size: PAGE_SIZE,
      })
      .then(res => {
        if (!alive) return
        setRows(res.rows)
        setTotal(res.total)
      })
      .catch(e => {
        if (!alive) return
        setError(e.message || String(e))
        setRows([])
        setTotal(0)
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [windowSec, group, statusCode, errorCode, debouncedSearch, page])

  const topStatus = facets?.status_codes[0]
  const topError = facets?.error_codes[0]
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const selectCls =
    'border border-gray-200 rounded-md px-2.5 py-1.5 text-xs bg-gray-50 focus:outline-none focus:border-gray-900'

  return (
    <Layout title="Error Center" subtitle="本地错误日志（type=5）按分组 / Code 筛选">
      <SummaryCards
        cards={[
          { label: '窗口内错误总数', value: facets ? String(facets.total) : '—', color: 'text-rose-600' },
          { label: '分组数', value: facets ? String(facets.groups.length) : '—', color: 'text-gray-900' },
          {
            label: '最多状态码',
            value: topStatus ? `${topStatus.value} (${topStatus.count})` : '—',
            color: 'text-amber-600',
          },
          {
            label: '最多错误码',
            value: topError ? topError.value : '—',
            color: 'text-blue-600',
          },
        ]}
      />

      <div className="bg-white border border-gray-200 rounded-xl p-3 mb-4 flex flex-wrap items-center gap-2">
        <select value={windowSec} onChange={e => setWindowSec(Number(e.target.value))} className={selectCls}>
          {WINDOWS.map(w => (
            <option key={w.sec} value={w.sec}>
              {w.label}
            </option>
          ))}
        </select>

        <select value={group} onChange={e => setGroup(e.target.value)} className={selectCls}>
          <option value="">全部分组</option>
          {facets?.groups.map(g => (
            <option key={g.value} value={g.value}>
              {g.value} ({g.count})
            </option>
          ))}
        </select>

        <select value={statusCode} onChange={e => setStatusCode(e.target.value)} className={selectCls}>
          <option value="">全部状态码</option>
          {facets?.status_codes.map(s => (
            <option key={s.value} value={s.value}>
              {s.value} ({s.count})
            </option>
          ))}
        </select>

        <select value={errorCode} onChange={e => setErrorCode(e.target.value)} className={selectCls}>
          <option value="">全部错误码</option>
          {facets?.error_codes.map(e => (
            <option key={e.value} value={e.value}>
              {e.value} ({e.count})
            </option>
          ))}
        </select>

        <input
          value={search}
          onChange={e => setSearch(e.target.value)}
          placeholder="搜索错误内容…"
          className={selectCls + ' flex-1 min-w-[160px]'}
        />

        {(group || statusCode || errorCode || search) && (
          <button
            onClick={() => {
              setGroup('')
              setStatusCode('')
              setErrorCode('')
              setSearch('')
            }}
            className="border border-gray-200 rounded-md px-3 py-1.5 text-xs bg-white hover:bg-gray-50"
          >
            清除筛选
          </button>
        )}
      </div>

      {error && (
        <div className="bg-rose-50 border border-rose-100 text-rose-700 text-xs rounded-md px-3 py-2 mb-4">{error}</div>
      )}

      <div className="bg-white border border-gray-200 rounded-xl overflow-hidden">
        <div className="px-4 py-2 border-b border-gray-100 flex items-center justify-between text-[11px] text-gray-500">
          <span>
            {loading ? '加载中…' : `共 ${total} 条`}
          </span>
          <div className="flex items-center gap-2">
            <button
              onClick={() => setPage(p => Math.max(1, p - 1))}
              disabled={page <= 1 || loading}
              className="border border-gray-200 rounded px-2 py-1 bg-white hover:bg-gray-50 disabled:opacity-40"
            >
              上一页
            </button>
            <span className="tabular-nums">
              {page} / {totalPages}
            </span>
            <button
              onClick={() => setPage(p => Math.min(totalPages, p + 1))}
              disabled={page >= totalPages || loading}
              className="border border-gray-200 rounded px-2 py-1 bg-white hover:bg-gray-50 disabled:opacity-40"
            >
              下一页
            </button>
          </div>
        </div>

        {rows.length === 0 && !loading ? (
          <div className="text-center text-gray-400 text-xs py-16">该窗口 / 筛选条件下没有错误</div>
        ) : (
          <div className="overflow-x-auto max-h-[68vh] overflow-y-auto">
            <table className="w-full text-xs whitespace-nowrap border-separate border-spacing-0">
              <thead>
                <tr>
                  {['时间', '分组', '模型', '渠道', '状态', '错误码', '类型', '路径', '内容'].map(h => (
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
                {rows.map(r => (
                  <tr key={r.id} className="hover:bg-gray-50 align-top">
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500 tabular-nums">{fmtTime(r.created_at)}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-700">{r.group || '—'}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{r.model_name || '—'}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500" title={r.channel_name}>
                      {r.channel_id || '—'}
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50">
                      {r.status_code ? (
                        <span className={`px-2 py-0.5 rounded-full text-[10px] font-semibold ${statusColor(r.status_code)}`}>
                          {r.status_code}
                        </span>
                      ) : (
                        '—'
                      )}
                    </td>
                    <td className="px-3 py-1.5 border-b border-gray-50 font-mono text-gray-600">{r.error_code || '—'}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{r.error_type || '—'}</td>
                    <td className="px-3 py-1.5 border-b border-gray-50 text-gray-500">{r.request_path || '—'}</td>
                    <td
                      className="px-3 py-1.5 border-b border-gray-50 text-gray-500 max-w-[420px] truncate"
                      title={r.content}
                    >
                      {r.content || '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Layout>
  )
}
