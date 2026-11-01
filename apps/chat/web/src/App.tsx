import { useCallback, useEffect, useState } from 'react'
import {
  CalendarClock,
  MessagesSquare,
  Mic,
  Boxes,
  Bot,
  Moon,
  SlidersHorizontal,
  Sun,
  Volume2,
  Wifi,
  WifiOff,
  Settings2,
  CheckCircle2,
  XCircle,
  Info,
} from 'lucide-react'

import { Button } from '@/components/ui/button'
import { ConnectionDialog } from '@/components/ConnectionDialog'
import { DoubaoLoginProvider } from '@/lib/doubaoLogin'
import { DoubaoView } from '@/views/DoubaoView'
import { ScheduleView } from '@/views/ScheduleView'
import { ConfigView } from '@/views/ConfigView'
import { TtsView } from '@/views/TtsView'
import { WakeView } from '@/views/WakeView'
import { AppManageView } from '@/views/AppManageView'
import { AgentsView } from '@/views/AgentsView'
import { api, getBase } from '@/lib/api'
import { useToasts, type ToastItem } from '@/lib/useToasts'
import { cn } from '@/lib/utils'

type ViewId = 'doubao' | 'apps' | 'schedule' | 'wake' | 'config' | 'tts' | 'agents'

const NAV: { id: ViewId; label: string; icon: typeof MessagesSquare; hint: string }[] = [
  { id: 'apps', label: '应用管理', icon: Boxes, hint: '豆包 / 网易云 账号登录' },
  { id: 'schedule', label: '定时任务', icon: CalendarClock, hint: '轮询豆包任务并播报到音箱' },
  { id: 'wake', label: '唤醒与识别', icon: Mic, hint: '唤醒词、识别引擎与在线测试' },
  { id: 'agents', label: '智能体', icon: Bot, hint: '三条会话的上下文管理' },
  { id: 'config', label: '配置', icon: SlidersHorizontal, hint: '在线编辑 chat 配置' },
  { id: 'tts', label: '语音测试', icon: Volume2, hint: '把文字发到音箱试听' },
]

const VIEW_IDS = NAV.map((n) => n.id)

// 当前页面用 hash 保存（#config / #wake…），刷新和前进后退都能保持
function viewFromHash(): ViewId {
  const raw = window.location.hash.replace(/^#\/?/, '').trim()
  return (VIEW_IDS as string[]).includes(raw) ? (raw as ViewId) : 'apps'
}

type ConnState = 'unknown' | 'ok' | 'error'

export default function App() {
  const [view, setView] = useState<ViewId>(() => viewFromHash())
  const [dark, setDark] = useState(false)
  const [conn, setConn] = useState<ConnState>('unknown')
  const [connMessage, setConnMessage] = useState('尚未连接')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const { toasts, notify, notifyError, dismiss } = useToasts()

  useEffect(() => {
    if (!window.location.hash) {
      window.history.replaceState(null, '', '#apps')
    }
    const onHash = () => setView(viewFromHash())
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])

  useEffect(() => {
    const saved = localStorage.getItem('openxiaoai.theme')
    const preferDark =
      saved === 'dark' ||
      (saved == null && window.matchMedia?.('(prefers-color-scheme: dark)').matches)
    setDark(!!preferDark)
  }, [])

  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark)
    localStorage.setItem('openxiaoai.theme', dark ? 'dark' : 'light')
  }, [dark])

  const checkConnection = useCallback(async () => {
    setConn('unknown')
    setConnMessage('连接中...')
    try {
      await api('/admin/api/config')
      setConn('ok')
      setConnMessage(getBase())
      return true
    } catch (err) {
      setConn('error')
      setConnMessage(err instanceof Error ? err.message : String(err))
      return false
    }
  }, [])

  useEffect(() => {
    void checkConnection()
  }, [checkConnection])

  return (
    <DoubaoLoginProvider onError={notifyError}>
      <div className="flex h-full bg-background text-foreground">
      <aside className="flex w-[228px] shrink-0 flex-col border-r border-border bg-card">
        <div className="flex items-center gap-2.5 px-4 py-5">
          <div className="flex size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
            <MessagesSquare className="size-4" />
          </div>
          <div className="min-w-0">
            <div className="truncate text-ui-base font-semibold">Open XiaoAI</div>
            <div className="truncate text-ui-xs text-muted-foreground">音箱管理台</div>
          </div>
        </div>

        <nav className="flex flex-col gap-0.5 px-2">
          {NAV.map((item) => {
            const Icon = item.icon
            const selected = item.id === view
            return (
              <button
                key={item.id}
                type="button"
                onClick={() => { window.location.hash = item.id }}
                className={cn(
                  'flex h-8 items-center gap-2 rounded-md px-2 text-left text-ui-base transition-colors',
                  selected
                    ? 'bg-primary-soft font-medium text-primary'
                    : 'text-sidebar-foreground hover:bg-muted hover:text-foreground',
                )}
              >
                <Icon className="size-4" />
                {item.label}
              </button>
            )
          })}
        </nav>

        <div className="mt-auto border-t border-border p-2">
          <button
            type="button"
            onClick={() => setSettingsOpen(true)}
            className="mb-1 flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-ui-sm text-sidebar-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            {conn === 'ok' ? (
              <Wifi className="size-3.5 text-success" />
            ) : conn === 'error' ? (
              <WifiOff className="size-3.5 text-destructive" />
            ) : (
              <Wifi className="size-3.5 text-muted-foreground" />
            )}
            <span className="min-w-0 flex-1 truncate">
              {conn === 'ok' ? '已连接' : conn === 'error' ? '未连接' : '检查中'}
            </span>
            <Settings2 className="size-3.5 opacity-60" />
          </button>
          <div className="flex items-center justify-between px-2 pb-1">
            <span className="max-w-[150px] truncate text-ui-xs text-muted-foreground">
              {connMessage}
            </span>
            <Button
              variant="ghost"
              size="icon-sm"
              type="button"
              onClick={() => setDark((v) => !v)}
              title={dark ? '切换到浅色' : '切换到深色'}
            >
              {dark ? <Sun className="size-3.5" /> : <Moon className="size-3.5" />}
            </Button>
          </div>
        </div>
      </aside>

      <main className="flex min-w-0 flex-1 flex-col">

        <div className="min-h-0 flex-1 overflow-hidden">
          <div className="flex h-full min-h-0 w-full flex-col p-3">
            {view === 'doubao' && <DoubaoView onError={notifyError} />}
            {view === 'schedule' && <ScheduleView onNotify={notify} onError={notifyError} />}
            {view === 'config' && <ConfigView onNotify={notify} onError={notifyError} />}
            {view === 'apps' && <AppManageView onNotify={notify} onError={notifyError} />}
            {view === 'wake' && <WakeView onNotify={notify} onError={notifyError} />}
            {view === 'tts' && <TtsView onNotify={notify} onError={notifyError} />}
            {view === 'agents' && <AgentsView onNotify={notify} onError={notifyError} />}
          </div>
        </div>
      </main>

      <ConnectionDialog
        open={settingsOpen}
        onOpenChange={setSettingsOpen}
        onSaved={async () => {
          await checkConnection()
        }}
      />

        <Toaster toasts={toasts} onDismiss={dismiss} />
      </div>
    </DoubaoLoginProvider>
  )
}

function Toaster({ toasts, onDismiss }: { toasts: ToastItem[]; onDismiss: (id: number) => void }) {
  return (
    <div className="pointer-events-none fixed right-4 bottom-4 z-50 flex w-[360px] flex-col gap-2">
      {toasts.map((toast) => {
        const Icon =
          toast.kind === 'success' ? CheckCircle2 : toast.kind === 'error' ? XCircle : Info
        return (
          <div
            key={toast.id}
            onClick={() => onDismiss(toast.id)}
            className={cn(
              'pointer-events-auto flex cursor-pointer items-start gap-2 rounded-lg border px-3 py-2.5 text-ui-sm shadow-lg backdrop-blur',
              toast.kind === 'success' && 'border-success/30 bg-card text-foreground',
              toast.kind === 'error' && 'border-destructive/30 bg-card text-foreground',
              toast.kind === 'info' && 'border-border bg-card text-foreground',
            )}
          >
            <Icon
              className={cn(
                'mt-0.5 size-4 shrink-0',
                toast.kind === 'success' && 'text-success',
                toast.kind === 'error' && 'text-destructive',
                toast.kind === 'info' && 'text-muted-foreground',
              )}
            />
            <span className="min-w-0 flex-1 whitespace-pre-wrap break-words">{toast.message}</span>
          </div>
        )
      })}
    </div>
  )
}
