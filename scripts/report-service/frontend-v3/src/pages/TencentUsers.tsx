import { useCallback, useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import CreatedCredential from '../components/CreatedCredential'
import { Badge, Button, Card, Input, Select } from '../components/ui'
import { toast } from '../components/feedback'
import { api, ROLE_CCMAX_MANAGER, type TencentPlatform, type TencentUser, type TencentUserRole } from '../api'

// 腾讯一站 · 用户 — one form creates the login of any platform (3P supplier,
// CC Max 号商 / 经理, New API Proxy) with the studio used as the statement's
// 供应商名称; the list shows each user's linked data on the three platforms.

const TENCENT_ROLES: { value: TencentUserRole; label: string }[] = [
  { value: 'supplier_01', label: '3P 供应商（supplier_01 · 账号上号）' },
  { value: 'supplier_02', label: '3P 供应商（supplier_02 · KHub）' },
  { value: 'supplier_ccmax', label: 'CC Max 号商' },
  { value: 'ccmax_manager', label: 'CC Max 经理' },
  { value: 'proxy', label: 'New API Proxy 用户' },
]

const TENCENT_PLATFORM_LABEL: Record<TencentPlatform, string> = {
  third_party: '3P',
  ccmax: 'CC Max',
  proxy: 'Proxy',
}

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
  const [platform, setPlatform] = useState<'' | TencentPlatform>('')
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setUsers((await api.tencentUsers(platform)).list)
    } catch (e) {
      toast.error(e)
    } finally {
      setLoading(false)
    }
  }, [platform])
  useEffect(() => {
    void load()
  }, [load])

  return (
    <Layout title="腾讯一站 · 用户" subtitle="统一新建三平台账号；供应商名称即对账单 A 列">
      <CreateUserCard onCreated={() => void load()} />
      <Card className="overflow-hidden p-0">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-2.5">
          <Select aria-label="平台筛选" value={platform} onChange={(e) => setPlatform(e.target.value as '' | TencentPlatform)}>
            <option value="">全部平台</option>
            <option value="third_party">3P（账号上号 / KHub）</option>
            <option value="ccmax">CC Max</option>
            <option value="proxy">New API Proxy</option>
          </Select>
          <Button size="sm" variant="outline" disabled={loading} onClick={() => void load()}>
            {loading ? '刷新中…' : '刷新'}
          </Button>
        </div>
        {!users || users.length === 0 ? (
          <p className="px-4 py-8 text-center text-xs text-muted-foreground">{users ? '没有符合条件的用户。' : '加载中…'}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="bg-muted/50 text-muted-foreground">
                <tr>
                  {['用户名', '供应商名称', '角色', '平台', '上号账号', 'CC Max 账号', 'Proxy 渠道', '归属经理', '状态', '创建时间'].map((h) => (
                    <th key={h} className="whitespace-nowrap px-3 py-2 font-medium">
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {users.map((u) => (
                  <tr key={u.id} className="border-t border-border">
                    <td className="px-3 py-1.5 font-mono">{u.username}</td>
                    <td className="px-3 py-1.5">{u.studio || '—'}</td>
                    <td className="px-3 py-1.5">{TENCENT_ROLES.find((r) => r.value === u.role_name)?.label ?? `role ${u.role}`}</td>
                    <td className="space-x-1 px-3 py-1.5">
                      {u.platforms.map((p) => (
                        <Badge key={p}>{TENCENT_PLATFORM_LABEL[p]}</Badge>
                      ))}
                    </td>
                    <td className="px-3 py-1.5 tnum">{u.supplier_accounts}</td>
                    <td className="px-3 py-1.5 tnum">{u.ccmax_accounts}</td>
                    <td className="px-3 py-1.5 tnum">{u.proxy_channels}</td>
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

function CreateUserCard(props: { onCreated: () => void }) {
  const [form, setForm] = useState({ username: '', studio: '', role: 'supplier_01' as TencentUserRole, managerId: 0 })
  const [managers, setManagers] = useState<TencentUser[]>([])
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')
  const [created, setCreated] = useState<{ username: string; password: string; shared: string[] } | null>(null)

  useEffect(() => {
    if (form.role !== 'supplier_ccmax') return
    api.tencentUsers('ccmax').then(
      (res) => setManagers(res.list.filter((u) => u.role === ROLE_CCMAX_MANAGER && u.status === 1)),
      (e) => toast.error(e),
    )
  }, [form.role])

  const roleLabel = useMemo(() => TENCENT_ROLES.find((r) => r.value === form.role)?.label ?? '', [form.role])

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
      setCreated({ username: res.username, password: res.password, shared: res.studio_shared_with })
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
          <span className="text-xs font-medium text-muted-foreground">平台角色</span>
          <Select
            aria-label="平台角色"
            className="block"
            value={form.role}
            onChange={(e) => setForm({ ...form, role: e.target.value as TencentUserRole, managerId: 0 })}
          >
            {TENCENT_ROLES.map((r) => (
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
            <span className="text-xs font-medium text-muted-foreground">归属经理（可选）</span>
            <Select
              aria-label="归属经理"
              className="block"
              value={String(form.managerId)}
              onChange={(e) => setForm({ ...form, managerId: Number(e.target.value) })}
            >
              <option value="0">不指定</option>
              {managers.map((m) => (
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
          <CreatedCredential
            kind={roleLabel.replace(/（.*）/, '')}
            username={created.username}
            password={created.password}
            onDismiss={() => setCreated(null)}
          />
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
