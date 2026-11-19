import { useCallback, useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import CreatedCredential from '../components/CreatedCredential'
import { Badge, Button, Card, Input, Select } from '../components/ui'
import { toast } from '../components/feedback'
import {
  api,
  ROLE_CCMAX_MANAGER,
  ROLE_SUPPLIER_01,
  ROLE_SUPPLIER_CCMAX,
  type TencentUser,
  type TencentUserRole,
} from '../api'

// 腾讯一站 · 用户 — the statement's suppliers: 老台子 (账号上号, supplier_01)
// and CCMax (号商 supplier_ccmax, Manager ccmax_manager). Other roles are not
// shown here. The studio is the statement's 供应商名称 (column A).

const ROLE_OPTIONS: { value: TencentUserRole; label: string }[] = [
  { value: 'supplier_01', label: '老台子' },
  { value: 'supplier_ccmax', label: 'CCMax 号商' },
  { value: 'ccmax_manager', label: 'CCMax Manager' },
]

// ROLE_TAG is each shown role's tag; the keys are the only roles listed.
const ROLE_TAG: Record<number, string> = {
  [ROLE_SUPPLIER_01]: '老台子',
  [ROLE_SUPPLIER_CCMAX]: 'CCMax',
  [ROLE_CCMAX_MANAGER]: 'CCMax Manager',
}

type Filter = '' | 'old' | 'ccmax'

const USERNAME_RE = /^[A-Za-z0-9_.-]{3,32}$/
const STUDIO_MAX = 64

// tencentUserFormError mirrors the server's checks so mistakes show before
// submitting; the server enforces them regardless.
function tencentUserFormError(form: { username: string; studio: string }): string {
  if (!USERNAME_RE.test(form.username.trim())) return '用户名须为 3-32 位字母、数字、_ - .'
  const studio = form.studio.trim()
  if (!studio) return '请填写供应商名称（studio）'
  if ([...studio].length > STUDIO_MAX) return `供应商名称最多 ${STUDIO_MAX} 个字符`
  return ''
}

function fmtDate(epochSec: number): string {
  return epochSec ? new Date(epochSec * 1000).toLocaleString() : '—'
}

export default function TencentUsers() {
  const [users, setUsers] = useState<TencentUser[] | null>(null)
  const [filter, setFilter] = useState<Filter>('')
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setUsers((await api.tencentUsers()).list.filter((u) => u.role in ROLE_TAG))
    } catch (e) {
      toast.error(e)
    } finally {
      setLoading(false)
    }
  }, [])
  useEffect(() => {
    void load()
  }, [load])

  const rows = useMemo(
    () =>
      (users ?? []).filter(
        (u) => !filter || (filter === 'old' ? u.role === ROLE_SUPPLIER_01 : u.role !== ROLE_SUPPLIER_01),
      ),
    [users, filter],
  )
  const managers = useMemo(() => (users ?? []).filter((u) => u.role === ROLE_CCMAX_MANAGER && u.status === 1), [users])

  return (
    <Layout title="腾讯一站 · 用户" subtitle="老台子与 CCMax 的供应商账号；供应商名称即对账单 A 列">
      <CreateUserCard managers={managers} onCreated={() => void load()} />
      <Card className="overflow-hidden p-0">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-2.5">
          <Select aria-label="筛选" value={filter} onChange={(e) => setFilter(e.target.value as Filter)}>
            <option value="">全部</option>
            <option value="old">老台子</option>
            <option value="ccmax">CCMax</option>
          </Select>
          <Button size="sm" variant="outline" disabled={loading} onClick={() => void load()}>
            {loading ? '刷新中…' : '刷新'}
          </Button>
        </div>
        {rows.length === 0 ? (
          <p className="px-4 py-8 text-center text-xs text-muted-foreground">{users ? '没有符合条件的用户。' : '加载中…'}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="bg-muted/50 text-muted-foreground">
                <tr>
                  {['用户名', '供应商名称', '标记', '上号账号', 'CCMax 账号', '归属 Manager', '状态', '创建时间'].map((h) => (
                    <th key={h} className="whitespace-nowrap px-3 py-2 font-medium">
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((u) => (
                  <tr key={u.id} className="border-t border-border">
                    <td className="px-3 py-1.5 font-mono">{u.username}</td>
                    <td className="px-3 py-1.5">{u.studio || '—'}</td>
                    <td className="px-3 py-1.5">
                      <Badge tone={u.role === ROLE_SUPPLIER_01 ? 'neutral' : 'brand'}>{ROLE_TAG[u.role]}</Badge>
                    </td>
                    <td className="px-3 py-1.5 tnum">{u.role === ROLE_SUPPLIER_01 ? u.supplier_accounts : '—'}</td>
                    <td className="px-3 py-1.5 tnum">{u.role === ROLE_SUPPLIER_01 ? '—' : u.ccmax_accounts}</td>
                    <td className="px-3 py-1.5">
                      {u.role === ROLE_CCMAX_MANAGER ? `管理 ${u.members} 个号商` : u.manager_username || '—'}
                    </td>
                    <td className="px-3 py-1.5">
                      <Badge tone={u.status === 1 ? 'success' : 'danger'}>{u.status === 1 ? '正常' : '已禁用'}</Badge>
                    </td>
                    <td className="whitespace-nowrap px-3 py-1.5 tnum">{fmtDate(u.created_at)}</td>
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

function CreateUserCard(props: { managers: TencentUser[]; onCreated: () => void }) {
  const [form, setForm] = useState({ username: '', studio: '', role: 'supplier_01' as TencentUserRole, managerId: 0 })
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')
  const [created, setCreated] = useState<{ username: string; password: string; kind: string; shared: string[] } | null>(null)

  async function create() {
    const problem = tencentUserFormError(form)
    setError(problem)
    if (problem) return
    setCreating(true)
    try {
      const res = await api.createTencentUser({
        username: form.username.trim(),
        studio: form.studio.trim(),
        role: form.role,
        ...(form.role === 'supplier_ccmax' && form.managerId ? { manager_id: form.managerId } : {}),
      })
      const kind = ROLE_OPTIONS.find((r) => r.value === form.role)?.label ?? ''
      setCreated({ username: res.username, password: res.password, kind, shared: res.studio_shared_with })
      setForm({ ...form, username: '' })
      props.onCreated()
    } catch (e) {
      toast.error(e)
    } finally {
      setCreating(false)
    }
  }

  return (
    <Card className="mb-4 space-y-3 p-4">
      <div className="text-sm font-semibold">新建用户（密码自动生成）</div>
      <div className="flex flex-wrap items-end gap-3">
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">角色</span>
          <Select
            aria-label="角色"
            className="block"
            value={form.role}
            onChange={(e) => setForm({ ...form, role: e.target.value as TencentUserRole, managerId: 0 })}
          >
            {ROLE_OPTIONS.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </Select>
        </label>
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">用户名</span>
          <Input
            aria-label="用户名"
            value={form.username}
            autoComplete="off"
            placeholder="3-32 位字母数字 _ - ."
            onChange={(e) => setForm({ ...form, username: e.target.value })}
          />
        </label>
        <label className="space-y-1.5">
          <span className="text-xs font-medium text-muted-foreground">供应商名称（studio）</span>
          <Input
            aria-label="供应商名称"
            value={form.studio}
            placeholder="对账单 A 列显示的名称"
            onChange={(e) => setForm({ ...form, studio: e.target.value })}
          />
        </label>
        {form.role === 'supplier_ccmax' && (
          <label className="space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">归属 Manager（可选）</span>
            <Select
              aria-label="归属 Manager"
              className="block"
              value={String(form.managerId)}
              onChange={(e) => setForm({ ...form, managerId: Number(e.target.value) })}
            >
              <option value="0">不指定</option>
              {props.managers.map((m) => (
                <option key={m.id} value={String(m.id)}>
                  {m.username}
                </option>
              ))}
            </Select>
          </label>
        )}
        <Button disabled={creating} onClick={() => void create()}>
          {creating ? '创建中…' : '创建'}
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
      {created && (
        <>
          <CreatedCredential kind={created.kind} username={created.username} password={created.password} onDismiss={() => setCreated(null)} />
          {created.shared.length > 0 && (
            <p className="text-xs text-warning">
              同一供应商名称下还有：{created.shared.join('、')}，对账单会合并在同一供应商下。
            </p>
          )}
        </>
      )}
    </Card>
  )
}
