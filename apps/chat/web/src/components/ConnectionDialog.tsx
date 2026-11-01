import { useEffect, useState } from 'react'
import { Loader2, PlugZap } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { api, DEFAULT_BASE, getBase, getCredentials, saveSettings } from '@/lib/api'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: () => Promise<void> | void
}

export function ConnectionDialog({ open, onOpenChange, onSaved }: Props) {
  const [base, setBase] = useState(getBase())
  const [user, setUser] = useState('')
  const [pass, setPass] = useState('')
  const [testing, setTesting] = useState(false)
  const [message, setMessage] = useState<{ text: string; ok: boolean } | null>(null)

  useEffect(() => {
    if (!open) return
    const creds = getCredentials()
    setBase(getBase())
    setUser(creds.user)
    setPass(creds.pass)
    setMessage(null)
  }, [open])

  async function handleSave() {
    saveSettings(base || DEFAULT_BASE, user, pass)
    setTesting(true)
    setMessage(null)
    try {
      await api('/admin/api/config')
      setMessage({ text: '连接成功', ok: true })
      await onSaved()
      window.setTimeout(() => onOpenChange(false), 500)
    } catch (err) {
      setMessage({ text: err instanceof Error ? err.message : String(err), ok: false })
    } finally {
      setTesting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[440px]">
        <DialogHeader>
          <DialogTitle>连接设置</DialogTitle>
          <DialogDescription>
            填写 chat 后端的地址。若后端配置了 <code>auth.users</code>，同时填入账号密码。
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3">
          <label className="flex flex-col gap-1.5">
            <span className="text-ui-sm font-medium">后端 API 地址</span>
            <Input
              value={base}
              onChange={(e) => setBase(e.target.value)}
              placeholder={DEFAULT_BASE}
              spellCheck={false}
            />
          </label>
          <div className="grid grid-cols-2 gap-3">
            <label className="flex flex-col gap-1.5">
              <span className="text-ui-sm font-medium">用户名</span>
              <Input
                value={user}
                onChange={(e) => setUser(e.target.value)}
                autoComplete="username"
                spellCheck={false}
              />
            </label>
            <label className="flex flex-col gap-1.5">
              <span className="text-ui-sm font-medium">密码</span>
              <Input
                type="password"
                value={pass}
                onChange={(e) => setPass(e.target.value)}
                autoComplete="current-password"
              />
            </label>
          </div>
          {message && (
            <div className={'text-ui-sm ' + (message.ok ? 'text-success' : 'text-destructive')}>
              {message.text}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" size="lg" type="button" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button size="lg" type="button" onClick={handleSave} disabled={testing}>
            {testing ? <Loader2 className="animate-spin" /> : <PlugZap />}
            保存并测试
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
