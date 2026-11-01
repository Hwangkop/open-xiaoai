import { useCallback, useRef, useState } from 'react'
import { Loader2, Send, Volume2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'

interface Props {
  onNotify: (message: string, kind?: 'success' | 'error' | 'info') => void
  onError: (err: unknown) => void
}

interface SentItem {
  id: number
  text: string
  at: string
  ok: boolean
  mode: Mode
}

type Mode = 'tts' | 'chat'


let seq = 0

export function TtsView({ onNotify, onError }: Props) {
  const [text, setText] = useState('')
  const [mode, setMode] = useState<Mode>('tts')
  const [sending, setSending] = useState(false)
  const [history, setHistory] = useState<SentItem[]>([])
  const inputRef = useRef<HTMLTextAreaElement | null>(null)

  const send = useCallback(async () => {
    const value = text.trim()
    if (!value) {
      onNotify('请输入要播放的文本', 'info')
      return
    }
    setSending(true)
    const at = new Date().toLocaleTimeString('zh-CN', { hour12: false })
    try {
      await api(mode === 'tts' ? '/admin/api/tts' : '/admin/api/chat', {
        method: 'POST',
        body: JSON.stringify({ text: value }),
      })
      setHistory((prev) => [{ id: ++seq, text: value, at, ok: true, mode }, ...prev].slice(0, 20))
      onNotify(mode === 'tts' ? '已发送到音箱播放' : '已交给 AI，稍等听音箱', 'success')
      setText('')
      inputRef.current?.focus()
    } catch (err) {
      setHistory((prev) => [{ id: ++seq, text: value, at, ok: false, mode }, ...prev].slice(0, 20))
      onError(err)
    } finally {
      setSending(false)
    }
  }, [mode, onError, onNotify, text])

  return (
    <div className="grid h-full min-h-0 gap-4 lg:grid-cols-[minmax(0,420px)_minmax(0,1fr)]">
      <Card size="sm" className="h-fit self-start">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-ui-sm">
            <Volume2 className="size-3.5 text-muted-foreground" />
            发一段话到音箱
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex items-center gap-1 rounded-md bg-muted p-0.5 text-ui-xs">
            {([
              ['tts', '语音合成'],
              ['chat', 'AI 对话'],
            ] as [Mode, string][]).map(([value, label]) => (
              <button
                key={value}
                type="button"
                onClick={() => setMode(value)}
                className={cn(
                  'flex-1 rounded px-2 py-1 transition-colors',
                  mode === value
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                {label}
              </button>
            ))}
          </div>
          <textarea
            ref={inputRef}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
                e.preventDefault()
                void send()
              }
            }}
            placeholder={
              mode === 'tts'
                ? '输入要朗读的文本，按 Ctrl/⌘ + Enter 发送'
                : '输入要对 AI 说的话（会走唤醒后的完整对话链路）'
            }
            className={cn(
              'min-h-[120px] w-full resize-y rounded-md border border-border bg-background px-3 py-2',
              'placeholder:text-muted-foreground',
              'text-ui-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/40',
            )}
          />
          <div className="flex items-center justify-between">
            <span className="text-ui-xs text-muted-foreground">
              {mode === 'tts'
                ? '只朗读文本：豆包音色或音箱自带音色'
                : '跳过唤醒词，直接问 AI（可触发放歌等工具）'}
            </span>
            <Button type="button" onClick={() => void send()} disabled={sending}>
              {sending ? <Loader2 className="animate-spin" /> : <Send />}
              {mode === 'tts' ? '发送到音箱' : '发送并对话'}
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card size="sm" className="flex min-h-0 flex-col overflow-hidden">
        <CardHeader className="shrink-0 border-b border-border/70 pb-2.5">
          <CardTitle className="flex items-center justify-between text-ui-sm">
            最近发送
            <span className="text-ui-xs font-normal text-muted-foreground">
              {history.length} 条
            </span>
          </CardTitle>
        </CardHeader>
        <CardContent className="min-h-0 flex-1 overflow-auto p-0">
          {history.length === 0 ? (
            <div className="flex h-full min-h-[120px] items-center justify-center text-ui-sm text-muted-foreground">
              还没有发送记录
            </div>
          ) : (
            history.map((item) => (
              <div
                key={item.id}
                className="flex items-start gap-3 border-b border-border/60 px-4 py-2.5"
              >
                <span
                  className={cn(
                    'mt-1 size-1.5 shrink-0 rounded-full',
                    item.ok ? 'bg-success' : 'bg-destructive',
                  )}
                />
                <div className="min-w-0 flex-1">
                  <div className="whitespace-pre-wrap break-words text-ui-sm">{item.text}</div>
                  <div className="mt-0.5 text-ui-xs text-muted-foreground">
                    {item.at} · {item.mode === 'tts' ? '语音合成' : 'AI 对话'} ·{' '}
                    {item.ok ? '已发送' : '失败'}
                  </div>
                </div>
              </div>
            ))
          )}
        </CardContent>
      </Card>
    </div>
  )
}
