import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { Loader2, LogIn, RefreshCw } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { api, type DoubaoHealth, type QrPoll, type QrStart } from '@/lib/api'
import { cn } from '@/lib/utils'

interface DoubaoLoginState {
  health: DoubaoHealth | null
  healthLoading: boolean
  checkedAt: string
  loginStatus: string
  loginBusy: boolean
  reload: () => Promise<void>
  startLogin: () => Promise<void>
}

const DoubaoLoginContext = createContext<DoubaoLoginState | null>(null)

export function useDoubaoLogin(): DoubaoLoginState {
  const value = useContext(DoubaoLoginContext)
  if (!value) throw new Error('useDoubaoLogin must be used inside DoubaoLoginProvider')
  return value
}

interface ProviderProps {
  children: React.ReactNode
  onError: (err: unknown) => void
}

/** 管理豆包登录状态：顶栏状态条与会话页共享同一份数据与二维码弹窗。 */
export function DoubaoLoginProvider({ children, onError }: ProviderProps) {
  const [health, setHealth] = useState<DoubaoHealth | null>(null)
  const [healthLoading, setHealthLoading] = useState(true)
  const [checkedAt, setCheckedAt] = useState('')

  const [loginStatus, setLoginStatus] = useState('')
  const [loginBusy, setLoginBusy] = useState(false)
  const [qrOpen, setQrOpen] = useState(false)
  const [qrImage, setQrImage] = useState<string | null>(null)

  const pollTimer = useRef<number | null>(null)

  const stopPolling = useCallback(() => {
    if (pollTimer.current != null) {
      window.clearInterval(pollTimer.current)
      pollTimer.current = null
    }
  }, [])

  const reload = useCallback(async () => {
    setHealthLoading(true)
    try {
      setHealth(await api<DoubaoHealth>('/admin/api/doubao/health'))
      setCheckedAt(
        new Date().toLocaleTimeString('zh-CN', {
          hour12: false,
          hour: '2-digit',
          minute: '2-digit',
        }),
      )
    } catch (err) {
      onError(err)
      setHealth(null)
    } finally {
      setHealthLoading(false)
    }
  }, [onError])

  useEffect(() => {
    void reload()
    return stopPolling
  }, [reload, stopPolling])

  const startLogin = useCallback(async () => {
    setLoginBusy(true)
    setLoginStatus('正在生成二维码...')
    setQrImage(null)
    setQrOpen(true)
    stopPolling()
    try {
      const data = await api<QrStart>('/admin/api/doubao/login', { method: 'POST' })
      if (!data.qr_image_base64) {
        setLoginStatus(data.error || '生成二维码失败')
        setLoginBusy(false)
        return
      }
      setQrImage('data:image/png;base64,' + data.qr_image_base64)
      setLoginStatus('请用豆包 App 扫码')
      setLoginBusy(false)

      pollTimer.current = window.setInterval(async () => {
        try {
          const poll = await api<QrPoll>('/admin/api/doubao/login')
          if (poll.status === 'success') {
            stopPolling()
            const extra = poll.cookies_count ? `（${poll.cookies_count} 个 cookie）` : ''
            setLoginStatus('登录成功，session 已更新' + extra)
            setQrImage(null)
            await reload()
            window.setTimeout(() => setQrOpen(false), 1200)
          } else if (poll.status === 'failed') {
            stopPolling()
            setLoginStatus('登录失败：' + (poll.error || '未知错误'))
          }
        } catch (err) {
          stopPolling()
          setLoginStatus(err instanceof Error ? err.message : String(err))
        }
      }, 2000)
    } catch (err) {
      setLoginBusy(false)
      setLoginStatus(err instanceof Error ? err.message : String(err))
    }
  }, [reload, stopPolling])

  const status = describeStatus(health, healthLoading)

  return (
    <DoubaoLoginContext.Provider
      value={{ health, healthLoading, checkedAt, loginStatus, loginBusy, reload, startLogin }}
    >
      {children}

      <Dialog
        open={qrOpen}
        onOpenChange={(open) => {
          setQrOpen(open)
          if (!open) stopPolling()
        }}
      >
        <DialogContent className="max-w-[380px]" showCloseButton={false}>
          <DialogHeader>
            <DialogTitle>豆包扫码登录</DialogTitle>
            <DialogDescription>
              打开豆包 App → 右上角扫一扫，确认后这里会自动完成。
            </DialogDescription>
          </DialogHeader>

          <div className="flex flex-col items-center gap-3 py-1">
            {qrImage ? (
              <img
                src={qrImage}
                alt="豆包登录二维码"
                className="size-[240px] rounded-lg border border-border bg-white p-2"
              />
            ) : (
              <div className="flex size-[240px] items-center justify-center rounded-lg border border-dashed border-border">
                {loginBusy ? (
                  <Loader2 className="size-6 animate-spin text-muted-foreground" />
                ) : (
                  <span className="px-6 text-center text-ui-sm text-muted-foreground">
                    二维码已失效
                  </span>
                )}
              </div>
            )}
            {loginStatus && (
              <p
                className={cn(
                  'm-0 text-center text-ui-sm',
                  loginStatus.includes('成功')
                    ? 'text-success'
                    : loginStatus.includes('失败') || loginStatus.includes('无法')
                      ? 'text-destructive'
                      : 'text-muted-foreground',
                )}
              >
                {loginStatus}
              </p>
            )}
          </div>

          <DialogFooter>
            <Button variant="outline" size="lg" type="button" onClick={() => setQrOpen(false)}>
              关闭
            </Button>
            <Button size="lg" type="button" onClick={startLogin} disabled={loginBusy}>
              {loginBusy ? <Loader2 className="animate-spin" /> : <RefreshCw />}
              刷新二维码
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

    </DoubaoLoginContext.Provider>
  )
}

/** 放在页面顶栏里的紧凑登录状态条。 */
export function DoubaoLoginBar() {
  const { health, healthLoading, checkedAt, loginBusy, reload, startLogin } = useDoubaoLogin()
  const status = describeStatus(health, healthLoading)
  const title = [
    health?.base_url ? `doubao2api：${health.base_url}` : health?.message || health?.error || '',
    checkedAt ? `上次检查 ${checkedAt}` : '',
  ]
    .filter(Boolean)
    .join('\n')

  return (
    <div className="flex items-center gap-1.5" title={title || undefined}>
      <Badge variant={status.variant}>{status.label}</Badge>
      <Button size="sm" type="button" onClick={() => void startLogin()} disabled={loginBusy}>
        {loginBusy ? <Loader2 className="animate-spin" /> : <LogIn />}
        扫码登录
      </Button>
      <Button variant="ghost" size="icon-sm" type="button" onClick={() => void reload()} title="刷新登录状态">
        <RefreshCw />
      </Button>
    </div>
  )
}

export function describeStatus(
  health: DoubaoHealth | null,
  loading: boolean,
): { label: string; variant: 'default' | 'secondary' | 'destructive' | 'outline' } {
  if (loading) return { label: '检查中', variant: 'secondary' }
  if (!health) return { label: '无法获取状态', variant: 'destructive' }
  if (health.enabled === false) return { label: '未启用', variant: 'outline' }
  if (health.reachable === false) return { label: 'doubao2api 未连接', variant: 'destructive' }
  if (health.logged_in) return { label: '已登录', variant: 'default' }
  return { label: '未登录 / 已过期', variant: 'destructive' }
}
