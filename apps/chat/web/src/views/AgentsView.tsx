import { useCallback, useEffect, useState } from 'react'
import { Bot, Loader2, RefreshCw, RotateCcw, Shrink } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { api } from '@/lib/api'

interface Props {
  onNotify: (message: string, kind?: 'success' | 'error' | 'info') => void
  onError: (err: unknown) => void
}

interface AgentInfo {
  name: string
  label: string
  desc: string
  thread_id: string
  ready: boolean
  turns?: number
}

interface AgentsResponse {
  enabled: boolean
  message?: string
  agents: AgentInfo[]
}

export function AgentsView({ onNotify, onError }: Props) {
  const [agents, setAgents] = useState<AgentInfo[]>([])
  const [enabled, setEnabled] = useState(true)
  const [message, setMessage] = useState<string | undefined>()
  const [busy, setBusy] = useState('')

  const load = useCallback(async () => {
    try {
      const res = await api<AgentsResponse>('/admin/api/agents')
      setAgents(res.agents || [])
      setEnabled(res.enabled)
      setMessage(res.message)
    } catch (err) {
      onError(err)
    }
  }, [onError])

  useEffect(() => {
    void load()
  }, [load])

  const act = useCallback(
    async (name: string, action: 'compact' | 'reset') => {
      setBusy(`${name}:${action}`)
      try {
        const res = await api<{ message: string }>('/admin/api/agents/action', {
          method: 'POST',
          body: JSON.stringify({ name, action }),
        })
        onNotify(res.message, 'success')
        await load()
      } catch (err) {
        onError(err)
      } finally {
        setBusy('')
      }
    },
    [load, onError, onNotify],
  )

  return (
    <div className="flex h-full min-h-0 flex-col gap-3 overflow-y-auto">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-ui-base font-medium">
          <Bot className="size-4" /> 智能体会话
          <span className="text-ui-xs font-normal text-muted-foreground">
            三条 codex thread，各自一份上下文；不会自动压缩，什么时候压你说了算
          </span>
        </div>
        <Button size="sm" variant="ghost" onClick={() => void load()}>
          <RefreshCw className="size-3.5" /> 刷新
        </Button>
      </div>

      {!enabled && (
        <Card>
          <CardContent className="py-6 text-ui-sm text-muted-foreground">
            {message || '当前不是 codex 后端，没有可管理的会话'}
          </CardContent>
        </Card>
      )}

      {enabled &&
        agents.map((a) => (
          <Card key={a.name}>
            <CardHeader className="pb-2">
              <CardTitle className="flex items-center justify-between text-ui-base">
                <span className="flex items-center gap-2">
                  {a.label}
                  {typeof a.turns === 'number' && a.turns >= 0 && (
                    <span className="text-ui-xs font-normal text-muted-foreground">
                      {a.turns} 轮上下文
                    </span>
                  )}
                  {a.ready ? (
                    <Badge variant="secondary" className="text-ui-xs">
                      已建立
                    </Badge>
                  ) : (
                    <Badge variant="outline" className="text-ui-xs text-muted-foreground">
                      未建立
                    </Badge>
                  )}
                </span>
                <span className="flex gap-1.5">
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={!a.ready || busy !== ''}
                    onClick={() => void act(a.name, 'compact')}
                  >
                    {busy === `${a.name}:compact` ? (
                      <Loader2 className="size-3.5 animate-spin" />
                    ) : (
                      <Shrink className="size-3.5" />
                    )}
                    压缩上下文
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy !== ''}
                    onClick={() => void act(a.name, 'reset')}
                  >
                    {busy === `${a.name}:reset` ? (
                      <Loader2 className="size-3.5 animate-spin" />
                    ) : (
                      <RotateCcw className="size-3.5" />
                    )}
                    新开会话
                  </Button>
                </span>
              </CardTitle>
            </CardHeader>
            <CardContent className="space-y-1.5">
              <div className="text-ui-sm text-muted-foreground">{a.desc}</div>
              <div className="font-mono text-ui-xs text-muted-foreground">
                thread: {a.thread_id || '（还没建立）'}
              </div>
            </CardContent>
          </Card>
        ))}
    </div>
  )
}
