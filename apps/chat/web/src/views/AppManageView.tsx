import { useCallback, useEffect, useRef, useState } from 'react'
import { CheckCircle2, Loader2, LogIn, Music2, RefreshCw, Sparkles } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import {
  api,
  type DoubaoHealth,
  type QrStart,
  type QrPoll,
  type NeteaseStatus,
  type NeteaseQrStart,
  type NeteaseQrPoll,
} from '@/lib/api'

interface Props {
  onNotify: (message: string, kind?: 'success' | 'error' | 'info') => void
  onError: (err: unknown) => void
}

type PollResult = 'waiting' | 'scanned' | 'done' | 'expired' | 'failed'

// QRLoginCard 是一个通用的"扫码登录"卡片：状态 + 刷新 + 扫码弹窗 + 轮询。
function QRLoginCard({
  title,
  icon,
  hint,
  loggedIn,
  detail,
  onRefresh,
  startQr,
  pollQr,
  onDone,
  onNotify,
  onError,
}: {
  title: string
  icon: React.ReactNode
  hint: string
  loggedIn: boolean
  detail?: string
  onRefresh: () => Promise<void>
  startQr: () => Promise<{ key?: string; image: string }>
  pollQr: (key?: string) => Promise<PollResult>
  onDone: () => void
  onNotify: Props['onNotify']
  onError: Props['onError']
}) {
  const [open, setOpen] = useState(false)
  const [image, setImage] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [polling, setPolling] = useState(false)
  const timer = useRef<number | null>(null)

  const stop = useCallback(() => {
    if (timer.current) {
      window.clearInterval(timer.current)
      timer.current = null
    }
    setPolling(false)
  }, [])
  useEffect(() => stop, [stop])

  const start = useCallback(async () => {
    setBusy(true)
    setImage(null)
    setOpen(true)
    stop()
    try {
      const { key, image: img } = await startQr()
      setImage(img)
      setPolling(true)
      timer.current = window.setInterval(async () => {
        try {
          const r = await pollQr(key)
          if (r === 'done') {
            stop()
            setOpen(false)
            onNotify(`${title} 登录成功`, 'success')
            await onRefresh()
          } else if (r === 'expired') {
            stop()
            onNotify('二维码已过期，请重新获取', 'error')
          } else if (r === 'failed') {
            stop()
            onNotify('登录失败', 'error')
          }
        } catch (err) {
          stop()
          onError(err)
        }
      }, 1500)
    } catch (err) {
      onError(err)
      setOpen(false)
    } finally {
      setBusy(false)
    }
  }, [onError, onNotify, onRefresh, pollQr, startQr, stop, title])

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="flex items-center gap-2 text-sm">
          {icon} {title}
          {loggedIn && <CheckCircle2 className="size-4 text-green-500" />}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="text-sm">
          {loggedIn ? (
            <span className="text-green-500">已登录</span>
          ) : (
            <span className="text-muted-foreground">未登录</span>
          )}
          {detail && <span className="ml-2 text-xs text-muted-foreground">{detail}</span>}
        </div>
        <div className="flex gap-2">
          <Button size="sm" onClick={() => void start()} disabled={busy}>
            {busy ? <Loader2 className="size-4 animate-spin" /> : <LogIn className="size-4" />}
            扫码登录
          </Button>
          <Button size="sm" variant="outline" onClick={() => void onRefresh()}>
            <RefreshCw className="size-4" /> 刷新
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">{hint}</p>
      </CardContent>

      <Dialog
        open={open}
        onOpenChange={(v) => {
          setOpen(v)
          if (!v) stop()
        }}
      >
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>扫码登录 {title}</DialogTitle>
          </DialogHeader>
          <div className="flex flex-col items-center gap-3 py-2">
            {image ? (
              <img src={image} alt="登录二维码" className="size-64 rounded-md bg-white p-2" />
            ) : (
              <div className="flex size-64 items-center justify-center">
                <Loader2 className="size-6 animate-spin text-muted-foreground" />
              </div>
            )}
            <div className="text-sm text-muted-foreground">{polling ? '等待扫码…' : '已停止'}</div>
          </div>
        </DialogContent>
      </Dialog>
    </Card>
  )
}

export function AppManageView({ onNotify, onError }: Props) {
  const [doubao, setDoubao] = useState<DoubaoHealth | null>(null)
  const [netease, setNetease] = useState<NeteaseStatus | null>(null)

  const loadDoubao = useCallback(async () => {
    try {
      setDoubao(await api<DoubaoHealth>('/admin/api/doubao/health'))
    } catch (err) {
      onError(err)
    }
  }, [onError])

  const loadNetease = useCallback(async () => {
    try {
      setNetease(await api<NeteaseStatus>('/admin/api/netease/status'))
    } catch (err) {
      onError(err)
    }
  }, [onError])

  useEffect(() => {
    void loadDoubao()
    void loadNetease()
  }, [loadDoubao, loadNetease])

  return (
    <div className="grid gap-3 lg:grid-cols-2">
      <QRLoginCard
        title="豆包"
        icon={<Sparkles className="size-4" />}
        hint="登录后可以读取会话记录、定时任务；对话本身走网页版直连。"
        loggedIn={!!doubao?.logged_in}
        detail={doubao?.saved_at ? `保存于 ${doubao.saved_at}` : undefined}
        onRefresh={loadDoubao}
        startQr={async () => {
          const r = await api<QrStart>('/admin/api/doubao/login', { method: 'POST' })
          if (!r.qr_image_base64) throw new Error(r.error || '生成二维码失败')
          return { image: 'data:image/png;base64,' + r.qr_image_base64 }
        }}
        pollQr={async () => {
          const r = await api<QrPoll>('/admin/api/doubao/login')
          if (r.status === 'success') return 'done'
          if (r.status === 'failed') return 'failed'
          return 'waiting'
        }}
        onDone={loadDoubao}
        onNotify={onNotify}
        onError={onError}
      />

      <QRLoginCard
        title="网易云音乐"
        icon={<Music2 className="size-4" />}
        hint="登录后可播放 VIP 歌曲；未登录只能播放免费曲目。"
        loggedIn={!!netease?.logged_in}
        detail={netease?.saved_at ? `保存于 ${netease.saved_at}` : undefined}
        onRefresh={loadNetease}
        startQr={async () => {
          const r = await api<NeteaseQrStart>('/admin/api/netease/qr', { method: 'POST' })
          return { key: r.key, image: 'data:image/png;base64,' + r.image }
        }}
        pollQr={async (key) => {
          const r = await api<NeteaseQrPoll>(
            `/admin/api/netease/qr?key=${encodeURIComponent(key || '')}`,
          )
          if (r.status === 'confirmed') return 'done'
          if (r.status === 'expired') return 'expired'
          if (r.status === 'scanned') return 'scanned'
          return 'waiting'
        }}
        onDone={loadNetease}
        onNotify={onNotify}
        onError={onError}
      />

    </div>
  )
}
