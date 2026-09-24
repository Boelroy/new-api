import { useCallback, useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import { Button, Card, Input, Select } from '../components/ui'
import { toast } from '../components/feedback'
import { api, ROLE_ADMIN, type KeyhubCategory } from '../api'
import { getCachedRole, loadRole } from '../auth'

// Key 列表 — the keys a studio has imported into KHub (pd-maas). The backend
// scopes this per supplier (group = sup_<user_id>), so a supplier_02 only sees
// their own rows; admins see the whole shared pool. Mirrors the upstream
// /api/admin/key-management/keys row shape (see keyhub.go handleKeyhubKeysList).

interface KeyRow {
  id?: string
  import_batch_id?: string
  category_code?: string
  key_hint?: string
  models?: unknown
  tag?: string
  group_name?: string
  note?: string
  status?: number
  active_targets?: number
  created_at?: string
  updated_at?: string
  [k: string]: unknown
}

function extractRows(data: unknown): KeyRow[] {
  if (Array.isArray(data)) return data as KeyRow[]
  if (data && typeof data === 'object') {
    const items = (data as { items?: unknown }).items
    if (Array.isArray(items)) return items as KeyRow[]
  }
  return []
}
function extractTotal(data: unknown): number | undefined {
  if (data && typeof data === 'object') {
    const t = (data as { total?: unknown }).total
    if (typeof t === 'number') return t
  }
  return undefined
}

function modelsText(v: unknown): string {
  if (Array.isArray(v)) return v.join(', ')
  if (v == null) return ''
  return String(v)
}

const PAGE_SIZE = 20

export default function KeyhubKeys() {
  const [categories, setCategories] = useState<KeyhubCategory[]>([])
  const [categoryCode, setCategoryCode] = useState('')
  const [keyword, setKeyword] = useState('')
  const [page, setPage] = useState(1)
  const [rows, setRows] = useState<KeyRow[]>([])
  const [total, setTotal] = useState<number | undefined>(undefined)
  const [loading, setLoading] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [role, setRole] = useState<number | null>(getCachedRole())

  useEffect(() => {
    if (role !== null) return
    void loadRole().then(setRole)
  }, [role])
  // Admins see the shared pool, so the group column is meaningful for them; for
  // a supplier every row is their own sup_<id>, so it's just noise.
  const isAdmin = role != null && role >= ROLE_ADMIN

  useEffect(() => {
    api
      .keyhubCategories()
      .then((res) => setCategories(Array.isArray(res?.data) ? res.data : []))
      .catch(() => {
        /* category filter is optional; ignore load failure */
      })
  }, [])

  const load = useCallback(
    async (forPage: number) => {
      setLoading(true)
      try {
        const res = await api.keyhubKeys({
          page: forPage,
          page_size: PAGE_SIZE,
          category_code: categoryCode || undefined,
          keyword: keyword.trim() || undefined,
        })
        if (res?.code === 'ok') {
          setRows(extractRows(res.data))
          setTotal(extractTotal(res.data))
          setPage(forPage)
          setLoaded(true)
        } else {
          toast.error(res?.message || '加载失败')
        }
      } catch (e) {
        toast.error(e)
      } finally {
        setLoading(false)
      }
    },
    [categoryCode, keyword],
  )

  // Load the first page on mount (and whenever the category filter changes) so
  // studios land on their list without having to click 查询 first.
  useEffect(() => {
    void load(1)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [categoryCode])

  const totalPages = total != null ? Math.max(1, Math.ceil(total / PAGE_SIZE)) : undefined

  const columns = useMemo(
    () =>
      [
        { key: 'category_code', label: '类别' },
        { key: 'key_hint', label: 'Key' },
        { key: 'models', label: '模型' },
        { key: 'tag', label: '标签' },
        ...(isAdmin ? [{ key: 'group_name', label: '分组' }] : []),
        { key: 'note', label: '备注' },
        { key: 'status', label: '状态' },
        { key: 'active_targets', label: '已上线' },
        { key: 'created_at', label: '导入时间' },
      ] as { key: keyof KeyRow; label: string }[],
    [isAdmin],
  )

  return (
    <Layout title="Key 列表" subtitle="已导入 KHub（pd-maas）的 Key">
      <Card className="space-y-4 p-4">
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">类别 (category)</span>
            <Select className="w-full" value={categoryCode} onChange={(e) => setCategoryCode(e.target.value)}>
              <option value="">全部</option>
              {categories.map((c) => (
                <option key={c.code} value={c.code}>
                  {c.label || c.code} ({c.code})
                </option>
              ))}
            </Select>
          </label>
          <label className="space-y-1.5 lg:col-span-2">
            <span className="text-xs font-medium text-muted-foreground">关键字 (keyword，可选)</span>
            <Input
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') load(1)
              }}
              placeholder="按 Key 提示 / 标签 / 备注搜索"
            />
          </label>
          <div className="flex items-end">
            <Button className="w-full" onClick={() => load(1)} disabled={loading}>
              {loading ? '查询中…' : '查询'}
            </Button>
          </div>
        </div>
      </Card>

      <Card className="mt-4 overflow-hidden p-0">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <div className="text-sm font-semibold">Key</div>
          <div className="flex items-center gap-2 text-[11px] text-muted-foreground tnum">
            {total != null && <span>共 {total} 条</span>}
            {totalPages != null && (
              <>
                <Button size="sm" variant="outline" disabled={loading || page <= 1} onClick={() => load(page - 1)}>
                  上一页
                </Button>
                <span>
                  {page} / {totalPages}
                </span>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={loading || page >= totalPages}
                  onClick={() => load(page + 1)}
                >
                  下一页
                </Button>
              </>
            )}
          </div>
        </div>
        {rows.length === 0 ? (
          <p className="px-4 py-8 text-center text-xs text-muted-foreground">
            {loaded ? '暂无 Key。上传后会显示在这里。' : '加载中…'}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="bg-muted/50 text-muted-foreground">
                <tr>
                  {columns.map((c) => (
                    <th key={String(c.key)} className="whitespace-nowrap px-3 py-2 font-medium">
                      {c.label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((r, ri) => (
                  <tr key={r.id ?? ri} className="border-t border-border">
                    {columns.map((c) => {
                      const raw = c.key === 'models' ? modelsText(r.models) : r[c.key]
                      const text = raw == null ? '' : String(raw)
                      return (
                        <td
                          key={String(c.key)}
                          className={
                            'max-w-[260px] truncate px-3 py-1.5 ' +
                            (c.key === 'key_hint' ? 'font-mono' : 'tnum')
                          }
                          title={text}
                        >
                          {text}
                        </td>
                      )
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </Layout>
  )
}
