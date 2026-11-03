import { useCallback, useEffect, useState } from 'react'
import {
  CalendarClock,
  Loader2,
  PauseCircle,
  PlayCircle,
  Plus,
  Sparkles,
  Trash2,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'

interface LocalTask {
  id: string
  text: string
  schedule: string
  ai: boolean
  enabled: boolean
  next_run: number
  last_run?: number
  last_text?: string
  created_at: number
}

interface Props {
  onNotify: (message: string, kind?: 'success' | 'error' | 'info') => void
  onError: (err: unknown) => void
}

function fmtUnix(sec?: number) {
  if (!sec) return '—'
  return new Date(sec * 1000).toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}

export function ScheduleView({ onNotify, onError }: Props) {
  const [tasks, setTasks] = useState<LocalTask[]>([])
  const [text, setText] = useState('')
  const [schedule, setSchedule] = useState('daily 08:00')
  const [ai, setAi] = useState(false)
  const [creating, setCreating] = useState(false)
  const [busyId, setBusyId] = useState<string | null>(null)

  const loadTasks = useCallback(async () => {
    try {
      const res = await api<{ tasks: LocalTask[] }>('/admin/api/tasks')
      setTasks(res.tasks || [])
    } catch (err) {
      onError(err)
    }
  }, [onError])

  useEffect(() => {
    void loadTasks()
  }, [loadTasks])

  const create = useCallback(async () => {
    const value = text.trim()
    if (!value) {
      onNotify('先写点任务内容', 'info')
      return
    }
    setCreating(true)
    try {
      await api('/admin/api/tasks', {
        method: 'POST',
        body: JSON.stringify({ text: value, schedule: schedule.trim(), ai }),
      })
      onNotify('任务已创建', 'success')
      setText('')
      await loadTasks()
    } catch (err) {
      onError(err)
    } finally {
      setCreating(false)
    }
  }, [ai, loadTasks, onError, onNotify, schedule, text])

  const act = useCallback(
    async (task: LocalTask, action: 'run' | 'enable' | 'disable') => {
      setBusyId(task.id)
      try {
        await api('/admin/api/tasks/action', {
          method: 'POST',
          body: JSON.stringify({ id: task.id, action }),
        })
        if (action === 'run') onNotify('已开始执行，稍后听音箱', 'success')
        await loadTasks()
      } catch (err) {
        onError(err)
      } finally {
        setBusyId(null)
      }
    },
    [loadTasks, onError, onNotify],
  )

  const remove = useCallback(
    async (task: LocalTask) => {
      setBusyId(task.id)
      try {
        await api(`/admin/api/tasks?id=${encodeURIComponent(task.id)}`, { method: 'DELETE' })
        onNotify('已删除', 'success')
        await loadTasks()
      } catch (err) {
        onError(err)
      } finally {
        setBusyId(null)
      }
    },
    [loadTasks, onError, onNotify],
  )

  return (
    <div className="flex h-full min-h-0 flex-col gap-3 overflow-auto">
      {/* 本机任务：时间由我们控制，到点直接在这台音箱上播 */}
      <Card size="sm" className="shrink-0">
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center gap-2 text-ui-sm">
            <CalendarClock className="size-3.5 text-muted-foreground" />
            本机任务
            <span className="text-ui-xs font-normal text-muted-foreground">
              到点由服务端执行并直接播报
            </span>
          </CardTitle>
        </CardHeader>

        <div className="flex flex-col gap-2 px-4 pb-3">
          <Textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="提醒内容，或给 AI 的要求（例如：提醒我该吃药了 / 播报今天的天气）"
            className="min-h-[64px]"
          />
          <div className="flex flex-wrap items-center gap-2">
            <Input
              value={schedule}
              onChange={(e) => setSchedule(e.target.value)}
              className="w-[260px]"
              placeholder="daily 08:00"
            />
            <label className="flex cursor-pointer items-center gap-1.5 text-ui-xs text-muted-foreground">
              <input
                type="checkbox"
                checked={ai}
                onChange={(e) => setAi(e.target.checked)}
                className="size-3.5 accent-foreground"
              />
              <Sparkles className="size-3.5" />
              交给 AI 生成回答
            </label>
            <Button size="sm" type="button" onClick={() => void create()} disabled={creating}>
              {creating ? <Loader2 className="animate-spin" /> : <Plus />}
              创建
            </Button>
          </div>
          <p className="m-0 text-ui-xs text-muted-foreground">
            时间写法：<code>once 2026-11-01 08:00</code> · <code>daily 08:00</code> ·{' '}
            <code>weekly mon 08:00</code> · <code>every 30m</code>
          </p>
        </div>

        <div className="border-t border-border/60">
          {tasks.length === 0 ? (
            <div className="flex items-center justify-center py-6 text-ui-sm text-muted-foreground">
              还没有本机任务
            </div>
          ) : (
            tasks.map((task) => (
              <div key={task.id} className="border-b border-border/50 px-4 py-2.5 last:border-b-0">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="truncate text-ui-sm">{task.text}</span>
                      <Badge variant={task.ai ? 'default' : 'secondary'}>
                        {task.ai ? 'AI' : '提醒'}
                      </Badge>
                      <Badge variant={task.enabled ? 'secondary' : 'outline'}>
                        {task.enabled ? '启用' : '已停用'}
                      </Badge>
                    </div>
                    <div className="mt-1 text-ui-xs text-muted-foreground">
                      {task.schedule} · 下次 {task.enabled ? fmtUnix(task.next_run) : '—'}
                      {task.last_run ? ` · 上次 ${fmtUnix(task.last_run)}` : ''}
                    </div>
                    {task.last_text && (
                      <div className="mt-1 line-clamp-2 text-ui-xs text-muted-foreground/80">
                        {task.last_text}
                      </div>
                    )}
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      type="button"
                      disabled={busyId === task.id}
                      onClick={() => void act(task, 'run')}
                      title="立即执行一次"
                    >
                      {busyId === task.id ? <Loader2 className="animate-spin" /> : <PlayCircle />}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      type="button"
                      disabled={busyId === task.id}
                      onClick={() => void act(task, task.enabled ? 'disable' : 'enable')}
                      title={task.enabled ? '停用' : '启用'}
                    >
                      <PauseCircle />
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      type="button"
                      disabled={busyId === task.id}
                      onClick={() => void remove(task)}
                      title="删除"
                    >
                      <Trash2 />
                    </Button>
                  </div>
                </div>
              </div>
            ))
          )}
        </div>
      </Card>
    </div>
  )
}
