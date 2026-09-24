import { useEffect, useMemo, useState } from 'react'
import Layout from '../components/Layout'
import { Button, Card, Input, Select, Textarea } from '../components/ui'
import { toast } from '../components/feedback'
import { api, type KeyhubCategory } from '../api'

// 上传 Key — imports credentials into KHub (pd-maas) via the report-service
// proxy. The category list (and each category's import profile) comes from
// pd-maas; multi-field categories (serialize 'pipe') join their fields with
// '|', one credential per line.

function fieldHint(cat: KeyhubCategory | undefined): string {
  if (!cat) return ''
  const prof = cat.import_profile_json
  if (!prof || !Array.isArray(prof.fields) || prof.fields.length === 0) return ''
  if (prof.serialize === 'pipe') {
    return prof.fields.map((f) => f.name).join(' | ')
  }
  return prof.fields[0]?.name ?? ''
}

export default function KeyhubUpload() {
  const [categories, setCategories] = useState<KeyhubCategory[]>([])
  const [loadingCats, setLoadingCats] = useState(true)
  const [categoryCode, setCategoryCode] = useState('')
  const [rawText, setRawText] = useState('')
  const [models, setModels] = useState('')
  const [groupName, setGroupName] = useState('default')
  const [tag, setTag] = useState('')
  const [note, setNote] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [result, setResult] = useState<unknown>(null)

  useEffect(() => {
    let alive = true
    api
      .keyhubCategories()
      .then((res) => {
        if (!alive) return
        const list = Array.isArray(res?.data) ? res.data : []
        setCategories(list)
        if (list.length > 0) setCategoryCode((prev) => prev || list[0].code)
      })
      .catch((e) => toast.error(e))
      .finally(() => alive && setLoadingCats(false))
    return () => {
      alive = false
    }
  }, [])

  const currentCat = useMemo(
    () => categories.find((c) => c.code === categoryCode),
    [categories, categoryCode],
  )
  const hint = fieldHint(currentCat)

  const lineCount = useMemo(
    () => rawText.split('\n').map((l) => l.trim()).filter(Boolean).length,
    [rawText],
  )

  const submit = async () => {
    const code = categoryCode.trim()
    const text = rawText.trim()
    if (!code) {
      toast.error('请选择类别')
      return
    }
    if (!text) {
      toast.error('请粘贴至少一条 Key')
      return
    }
    setSubmitting(true)
    setResult(null)
    try {
      const res = await api.keyhubImport({
        category_code: code,
        raw_text: text,
        endpoint_url: '',
        models: models.trim() ? models.split(',').map((m) => m.trim()).filter(Boolean) : undefined,
        group_name: groupName.trim() || 'default',
        tag: tag.trim() || undefined,
        note: note.trim() || undefined,
      })
      setResult(res)
      if (res?.code === 'ok') {
        toast.success('导入成功')
        setRawText('')
      } else {
        toast.error(res?.message || '导入失败')
      }
    } catch (e) {
      toast.error(e)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Layout title="上传 Key" subtitle="导入 Key 到 KHub（pd-maas）">
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
        <Card className="space-y-4 p-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">类别 (category)</span>
              <Select
                className="w-full"
                value={categoryCode}
                disabled={loadingCats}
                onChange={(e) => setCategoryCode(e.target.value)}
              >
                {loadingCats && <option>加载中…</option>}
                {!loadingCats && categories.length === 0 && <option value="">无可用类别</option>}
                {categories.map((c) => (
                  <option key={c.code} value={c.code}>
                    {c.label || c.code} ({c.code})
                  </option>
                ))}
              </Select>
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">分组 (group_name)</span>
              <Input value={groupName} onChange={(e) => setGroupName(e.target.value)} placeholder="default" />
            </label>
          </div>

          <label className="block space-y-1.5">
            <div className="flex items-center justify-between">
              <span className="text-xs font-medium text-muted-foreground">Key 内容 (raw_text)</span>
              <span className="text-[11px] text-muted-foreground tnum">{lineCount} 行</span>
            </div>
            {hint && (
              <div className="rounded-md bg-muted px-2.5 py-1.5 text-[11px] text-muted-foreground">
                每行一条{currentCat?.import_profile_json?.serialize === 'pipe' ? '，字段用 | 分隔：' : '：'}
                <span className="font-mono">{hint}</span>
              </div>
            )}
            <Textarea
              className="min-h-52 font-mono text-xs"
              value={rawText}
              onChange={(e) => setRawText(e.target.value)}
              placeholder={
                currentCat?.import_profile_json?.serialize === 'pipe'
                  ? 'AccessKey|SecretKey|Region'
                  : 'sk-...'
              }
              spellCheck={false}
            />
          </label>

          <div className="grid gap-4 sm:grid-cols-2">
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">模型 (models，逗号分隔，可留空)</span>
              <Input value={models} onChange={(e) => setModels(e.target.value)} placeholder="留空使用类别默认" />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-medium text-muted-foreground">标签 (tag，可选)</span>
              <Input value={tag} onChange={(e) => setTag(e.target.value)} />
            </label>
          </div>

          <label className="block space-y-1.5">
            <span className="text-xs font-medium text-muted-foreground">备注 (note，可选)</span>
            <Input value={note} onChange={(e) => setNote(e.target.value)} />
          </label>

          <div className="flex items-center justify-end">
            <Button onClick={submit} disabled={submitting || loadingCats}>
              {submitting ? '导入中…' : '导入 Key'}
            </Button>
          </div>
        </Card>

        <Card className="space-y-3 p-4">
          <div className="text-sm font-semibold">导入结果</div>
          {!result && <p className="text-xs text-muted-foreground">提交后这里显示 KHub 返回的结果。</p>}
          {result != null && (
            <pre className="max-h-[520px] overflow-auto rounded-md bg-muted p-3 text-[11px] leading-relaxed">
              {JSON.stringify(result, null, 2)}
            </pre>
          )}
        </Card>
      </div>
    </Layout>
  )
}
