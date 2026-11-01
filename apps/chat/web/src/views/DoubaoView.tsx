import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Bot, Inbox, Loader2, MessageSquareText, RefreshCw, Search, User } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { api, formatTime, type ChatMessage, type Conversation } from '@/lib/api'
import { cn } from '@/lib/utils'

interface Props {
  onError: (err: unknown) => void
}

export function DoubaoView({ onError }: Props) {
  const [conversations, setConversations] = useState<Conversation[] | null>(null)
  const [convLoading, setConvLoading] = useState(false)
  const [filter, setFilter] = useState('')

  const [selected, setSelected] = useState<Conversation | null>(null)
  const [messages, setMessages] = useState<ChatMessage[] | null>(null)
  const [msgLoading, setMsgLoading] = useState(false)

  const msgListRef = useRef<HTMLDivElement | null>(null)

  const loadConversations = useCallback(async () => {
    setConvLoading(true)
    try {
      setConversations(await api<Conversation[]>('/admin/api/doubao/conversations?limit=30'))
    } catch (err) {
      onError(err)
      setConversations([])
    } finally {
      setConvLoading(false)
    }
  }, [onError])

  useEffect(() => {
    void loadConversations()
  }, [loadConversations])

  const openConversation = useCallback(
    async (conv: Conversation) => {
      setSelected(conv)
      setMessages(null)
      setMsgLoading(true)
      try {
        setMessages(
          await api<ChatMessage[]>(
            `/admin/api/doubao/messages?id=${encodeURIComponent(conv.id)}&limit=50`,
          ),
        )
      } catch (err) {
        onError(err)
        setMessages([])
      } finally {
        setMsgLoading(false)
        window.requestAnimationFrame(() => {
          const el = msgListRef.current
          if (el) el.scrollTop = el.scrollHeight
        })
      }
    },
    [onError],
  )

  const filtered = useMemo(() => {
    if (!conversations) return null
    const q = filter.trim()
    if (!q) return conversations
    return conversations.filter(
      (c) => c.title.includes(q) || c.preview.includes(q) || c.id.includes(q),
    )
  }, [conversations, filter])

  return (
    <Card size="sm" className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <CardHeader className="shrink-0 border-b border-border/70 pb-2.5">
        <CardTitle className="flex items-center gap-2 text-ui-sm">
          <MessageSquareText className="size-3.5 text-muted-foreground" />
          会话记录
          {conversations && (
            <span className="text-ui-xs font-normal text-muted-foreground">
              {conversations.length} 个
            </span>
          )}
          <Button
            variant="ghost"
            size="icon-sm"
            type="button"
            className="ml-auto"
            onClick={() => void loadConversations()}
            title="刷新会话"
            disabled={convLoading}
          >
            {convLoading ? <Loader2 className="animate-spin" /> : <RefreshCw />}
          </Button>
        </CardTitle>
        <div className="relative w-[220px]">
          <Search className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="搜索会话"
            className="h-7 pl-7 text-ui-sm"
          />
        </div>
      </CardHeader>

      <div className="grid min-h-0 flex-1 md:grid-cols-[260px_minmax(0,1fr)]">
        <div className="min-h-0 overflow-auto border-border/70 md:border-r">
          {!conversations && (
            <div className="flex h-full items-center justify-center py-10 text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
            </div>
          )}
          {conversations && filtered?.length === 0 && (
            <div className="flex h-full flex-col items-center justify-center gap-2 px-6 text-center">
              <Inbox className="size-6 text-muted-foreground/40" />
              <p className="m-0 text-ui-sm text-muted-foreground">
                {filter ? '没有匹配的会话' : '没有取到会话，可能需要先登录豆包'}
              </p>
            </div>
          )}
          {filtered?.map((conv) => (
            <button
              key={conv.id}
              type="button"
              onClick={() => void openConversation(conv)}
              className={cn(
                'block w-full border-b border-border/50 px-3 py-2 text-left transition-colors',
                selected?.id === conv.id ? 'bg-primary-soft' : 'hover:bg-muted',
              )}
            >
              <div className="flex items-baseline justify-between gap-2">
                <span className="truncate text-ui-sm font-medium">
                  {conv.title || `会话 ${conv.id.slice(-6)}`}
                </span>
                <span className="shrink-0 text-ui-xs text-muted-foreground">
                  {formatTime(conv.last_time)}
                </span>
              </div>
              <div className="mt-0.5 truncate text-ui-xs text-muted-foreground">
                {conv.preview || '(无文本)'}
              </div>
            </button>
          ))}
        </div>

        <div className="flex min-h-0 flex-col">
          {!selected ? (
            <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
              <MessageSquareText className="size-6 text-muted-foreground/40" />
              <p className="m-0 text-ui-sm text-muted-foreground">选择左侧会话查看消息</p>
            </div>
          ) : (
            <>
              <div className="flex shrink-0 items-center justify-between gap-3 border-b border-border/70 px-4 py-2">
                <span className="truncate text-ui-sm font-medium">
                  {selected.title || `会话 ${selected.id}`}
                </span>
                <span className="shrink-0 font-mono text-ui-xs text-muted-foreground">
                  {selected.id}
                </span>
              </div>
              <div ref={msgListRef} className="min-h-0 flex-1 space-y-3 overflow-auto px-4 py-3">
                {msgLoading && (
                  <div className="flex items-center justify-center py-8 text-muted-foreground">
                    <Loader2 className="size-4 animate-spin" />
                  </div>
                )}
                {messages?.length === 0 && !msgLoading && (
                  <p className="m-0 py-8 text-center text-ui-sm text-muted-foreground">
                    这个会话里没有可显示的文本消息
                  </p>
                )}
                {messages?.map((msg, index) => (
                  <MessageBubble key={index} msg={msg} />
                ))}
              </div>
            </>
          )}
        </div>
      </div>
    </Card>
  )
}

function MessageBubble({ msg }: { msg: ChatMessage }) {
  const isUser = msg.role === 'user'
  return (
    <div className={cn('flex gap-2', isUser ? 'flex-row-reverse' : 'flex-row')}>
      <div
        className={cn(
          'mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full',
          isUser ? 'bg-primary text-primary-foreground' : 'bg-muted text-muted-foreground',
        )}
      >
        {isUser ? <User className="size-3" /> : <Bot className="size-3" />}
      </div>
      <div className={cn('max-w-[78%] rounded-lg px-3 py-2', isUser ? 'bg-primary-soft' : 'bg-muted')}>
        <div className="mb-1 text-ui-xs text-muted-foreground">
          {isUser ? '我' : '豆包'} · {formatTime(msg.time)}
        </div>
        <div className="whitespace-pre-wrap break-words text-ui-sm leading-relaxed">{msg.text}</div>
      </div>
    </div>
  )
}
