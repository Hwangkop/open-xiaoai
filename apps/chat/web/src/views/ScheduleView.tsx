import { useCallback, useEffect, useState } from 'react'
import {
  CalendarClock,
  Loader2,
  PauseCircle,
  PlayCircle,
  Plus,
  Radio,
  RefreshCw,
  Sparkles,
  Trash2,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'

interface JobView {
  id: string
  title: string
  status: number
  last_run_time: string
  last_run_status: number
  conversation_id: string
  last_text?: string
  last_spoken_at?: string
  last_run_time_text?: string
}

interface JobsResponse {
  enabled: boolean
  message?: string
  interval?: string
  last_poll?: string
  last_err?: string
  jobs?: JobView[]
}

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

  const [data, setData] = useState<JobsResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [polling, setPolling] = useState(false)
  const [speakingId, setSpeakingId] = useState<string | null>(null)

  const loadTasks = useCallback(async () => {
    try {
      const res = await api<{ tasks: LocalTask[] }>('/admin/api/tasks')
      setTasks(res.tasks || [])
    } catch (err) {
      onError(err)
    }
  }, [onError])

  const loadJobs = useCallback(async () => {
    setLoading(true)
    try {
      setData(await api<JobsResponse>('/admin/api/doubao/jobs'))
    } catch (err) {
      onError(err)
      setData(null)
    } finally {
      setLoading(false)
    }
  }, [onError])

  useEffect(() => {
    void loadTasks()
    void loadJobs()
  }, [loadJobs, loadTasks])

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

  const pollNow = useCallback(async () => {
    setPolling(true)
    try {
      const res = await api<{ message: string; last_err?: string }>('/admin/api/doubao/jobs/poll', {
        method: 'POST',
      })
      onNotify(res.last_err ? `${res.message}（${res.last_err}）` : res.message, res.last_err ? 'error' : 'success')
      await loadJobs()
    } catch (err) {
      onError(err)
    } finally {
      setPolling(false)
    }
  }, [loadJobs, onError, onNotify])

  const speak = useCallback(
    async (job: JobView) => {
      setSpeakingId(job.id)
      try {
        const res = await api<{ message: string; text: string }>('/admin/api/doubao/jobs/speak', {
          method: 'POST',
          body: JSON.stringify({ job_id: job.id }),
        })
        onNotify(res.message, 'success')
        await loadJobs()
      } catch (err) {
        onError(err)
      } finally {
        setSpeakingId(null)
      }
    },
    [loadJobs, onError, onNotify],
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

      {/* 豆包 job_cron：只读轮询，作为补充 */}
      <Card size="sm" className="shrink-0">
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center gap-2 text-ui-sm">
            <Radio className="size-3.5 text-muted-foreground" />
            豆包定时任务（只读）
            {data?.enabled === false ? (
              <Badge variant="outline">未启用</Badge>
            ) : data?.last_err ? (
              <Badge variant="destructive">异常</Badge>
            ) : (
              <Badge variant="default">正常</Badge>
            )}
          </CardTitle>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-ui-xs text-muted-foreground">
            {data?.enabled === false ? (
              <span>{data.message || '定时任务播报未启用'}</span>
            ) : (
              <>
                <span>间隔 {data?.interval || '-'}</span>
                <span>上次轮询 {data?.last_poll || '尚未'}</span>
                <span>共 {data?.jobs?.length ?? 0} 个任务</span>
                {data?.last_err && <span className="text-destructive">错误：{data.last_err}</span>}
              </>
            )}
          </div>
        </CardHeader>

        <div className="flex flex-wrap gap-2 px-4 pb-2">
          <Button size="sm" variant="outline" type="button" onClick={() => void loadJobs()} disabled={loading}>
            {loading ? <Loader2 className="animate-spin" /> : <RefreshCw />}
            刷新
          </Button>
          <Button
            variant="outline"
            size="sm"
            type="button"
            onClick={() => void pollNow()}
            disabled={polling || data?.enabled === false}
          >
            {polling ? <Loader2 className="animate-spin" /> : <Radio />}
            立即轮询一次
          </Button>
        </div>

        {data?.enabled !== false && (
          <div className="border-t border-border/60">
            {data?.jobs?.length === 0 && (
              <div className="flex items-center justify-center py-6 text-ui-xs text-muted-foreground">
                还没有豆包定时任务（在豆包网页里创建后会自动出现）
              </div>
            )}
            {data?.jobs?.map((job) => (
              <div key={job.id} className="border-b border-border/50 px-4 py-2.5 last:border-b-0">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="truncate text-ui-sm">{job.title || job.id}</span>
                      <Badge variant={job.status === 1 ? 'secondary' : 'outline'}>
                        {job.status === 1 ? '运行中' : '已停用'}
                      </Badge>
                    </div>
                    <div className="mt-1 text-ui-xs text-muted-foreground">
                      上次执行 {job.last_run_time_text || '暂无'}
                      {job.last_spoken_at ? ` · 上次播报 ${job.last_spoken_at}` : ''}
                    </div>
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    type="button"
                    disabled={!job.conversation_id || speakingId === job.id}
                    onClick={() => void speak(job)}
                    title={job.conversation_id ? '立即播报最近一次结果' : '该任务还没有执行记录'}
                  >
                    {speakingId === job.id ? <Loader2 className="animate-spin" /> : <PlayCircle />}
                    播报
                  </Button>
                </div>
                {job.last_text && (
                  <div
                    className={cn(
                      'mt-2 rounded-md bg-muted px-2.5 py-1.5 text-ui-xs text-muted-foreground',
                      'whitespace-pre-wrap break-words',
                    )}
                  >
                    {job.last_text}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </Card>
    </div>
  )
}
