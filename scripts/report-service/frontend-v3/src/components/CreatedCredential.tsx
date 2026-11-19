import { Copy } from 'lucide-react'
import { Button } from './ui'
import { toast } from './feedback'

// CreatedCredential shows a server-generated password once, right after the
// account is created; it is not kept anywhere and cannot be shown again.
export default function CreatedCredential(props: {
  username: string
  password: string
  onDismiss: () => void
  kind?: string
}) {
  function copy() {
    navigator.clipboard.writeText(`用户名：${props.username}\n密码：${props.password}`).then(
      () => toast.success('已复制'),
      () => toast.error('复制失败'),
    )
  }
  return (
    <div className="space-y-2 rounded-lg border border-border bg-muted p-3 text-xs">
      <div>
        已创建{props.kind ?? '号商'} <span className="font-mono font-semibold">{props.username}</span>
        ，密码只显示这一次，请立即保存：
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <code className="rounded bg-background px-2 py-1 font-mono text-sm">{props.password}</code>
        <Button size="sm" variant="outline" onClick={copy}>
          <Copy className="size-3.5" /> 复制账号密码
        </Button>
        <Button size="sm" variant="ghost" onClick={props.onDismiss}>
          我已保存
        </Button>
      </div>
    </div>
  )
}
