import { useCallback, useEffect, useState } from 'react'
import { Loader2, Music2, Pause, Play, RefreshCw, RotateCcw, SkipBack, SkipForward, Square } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { api } from '@/lib/api'

interface Song { id: number; name: string; artists: string; duration_ms: number }
interface MusicState {
	 source?: 'netease' | 'bilibili'
  current: Song | null
  queue: Song[] | null
  queue_name: string
  index: number
  one_shot: boolean
  error?: string
  playback: {
    status: string; tracking: boolean; position_ms: number; duration_ms: number
    position_estimated: boolean; observed_at: number; error?: string
  }
}
interface Props {
  onNotify: (message: string, kind?: 'success' | 'error' | 'info') => void
  onError: (error: unknown) => void
}
const STATUS: Record<string, string> = {
  stopped: '已停止', loading: '正在起播', playing: '播放中', paused: '已暂停',
  interrupted: '对话中', recovering: '正在恢复', stalled: '播放中断', ended: '队列已播完', unknown: '设备状态待确认', error: '播放失败',
}
function time(ms: number) {
  const seconds = Math.max(0, Math.floor(ms / 1000))
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
}

export function MusicView({ onNotify, onError }: Props) {
  const [state, setState] = useState<MusicState | null>(null)
  const [query, setQuery] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const [loadError, setLoadError] = useState('')
  const load = useCallback(async () => {
    try {
      setState(await api<MusicState>('/admin/api/music'))
      setLoadError('')
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : '读取播放状态失败')
    }
  }, [])
  useEffect(() => {
    let stopped = false
    let timer: ReturnType<typeof setTimeout>
    const poll = async () => {
      await load()
      if (!stopped) timer = setTimeout(poll, 2000)
    }
    void poll()
    return () => { stopped = true; clearTimeout(timer) }
  }, [load])

  const control = async (action: string, index?: number) => {
    setBusy(action)
    try {
      const result = await api<{ message: string }>('/admin/api/music', {
        method: 'POST', body: JSON.stringify({ action, query: query.trim(), index }),
      })
      onNotify(result.message, 'success')
      await load()
    } catch (err) { onError(err) } finally { setBusy(null) }
  }
  const p = state?.playback
	const itemLabel = state?.source === 'bilibili' ? '项' : '首'
  const queue = state?.queue ?? []
  const disabled = busy !== null || !!loadError || !state
  const progress = p?.duration_ms ? Math.min(100, p.position_ms / p.duration_ms * 100) : 0
  const active = !!p && !['stopped', 'ended', 'error'].includes(p.status)
  const canReturn = !!state && state.index + 1 < queue.length
  const canReplay = !!state?.current && (state.one_shot || queue.length > 0)

  return (
    <div className="flex h-full min-h-0 flex-col gap-4 overflow-y-auto pr-1">
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-ui-lg font-semibold">音乐管理</div>
          <div className="text-ui-sm text-muted-foreground">{state?.source === 'bilibili' ? 'B 站视频 / 分 P 队列' : '网易云歌曲队列'} · 每 2 秒更新</div>
        </div>
        <Button variant="outline" size="sm" onClick={() => void load()}><RefreshCw className="size-3.5" />刷新</Button>
      </div>
      {loadError && <div role="alert" className="text-ui-sm text-destructive">{loadError}。显示的是上次读取的状态。</div>}
      <Card size="sm">
        <CardHeader><CardTitle className="flex items-center gap-2"><Music2 className="size-4" />{loadError ? '状态未更新' : STATUS[p?.status ?? ''] ?? '正在读取'}</CardTitle></CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div>
            <div className="text-ui-lg font-semibold">{state?.current?.name ?? '还没有选择歌曲'}</div>
            <div className="text-ui-sm text-muted-foreground">{state?.current?.artists || '搜索歌曲、歌手，或播放每日推荐'}</div>
            {state?.one_shot && <div className="mt-2 text-ui-sm text-muted-foreground">
              单次点播 · {canReturn ? `播完继续「${state.queue_name}」第 ${state.index + 2} 首` : '播完停止，可选择推荐或歌单持续播放'}
            </div>}
          </div>
          {!!p && !!state?.current && <div className="flex flex-col gap-1.5">
            <div className="h-1.5 overflow-hidden rounded-full bg-muted"><div className="h-full bg-primary transition-all" style={{ width: `${progress}%` }} /></div>
            <div className="flex justify-between text-ui-xs text-muted-foreground">
              <span>{p.position_estimated ? '约 ' : ''}{time(p.position_ms)} / {p.duration_ms ? time(p.duration_ms) : '时长未知'}</span>
              <span>{p.observed_at ? `音箱状态更新于 ${new Date(p.observed_at * 1000).toLocaleTimeString()}` : '等待音箱播放状态'}</span>
            </div>
          </div>}
          {(p?.error || state?.error) && <div role="alert" className="text-ui-sm text-destructive">{p?.error || state?.error}</div>}
          {p?.status === 'stalled' && <div className="text-ui-sm text-muted-foreground">音箱已空闲，尚未确认歌曲播完。可继续播放，或重新播放当前歌曲。</div>}
          {p?.status === 'interrupted' && <div className="text-ui-sm text-muted-foreground">正在处理语音对话，结束后继续音乐；期间保留队列。</div>}
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" disabled={disabled || state?.one_shot || !queue.length || state!.index <= 0} onClick={() => void control('previous')}><SkipBack className="size-3.5" />上一{itemLabel}</Button>
            {p?.status === 'playing'
              ? <Button size="sm" disabled={disabled} onClick={() => void control('pause')}><Pause className="size-3.5" />暂停</Button>
              : <Button size="sm" disabled={disabled || !canReplay || p?.status === 'interrupted' || p?.status === 'recovering'} onClick={() => void control('resume')}><Play className="size-3.5" />继续播放</Button>}
            <Button size="sm" variant="outline" disabled={disabled || !canReturn} onClick={() => void control('next')}><SkipForward className="size-3.5" />{state?.one_shot ? '回到歌单' : `下一${itemLabel}`}</Button>
            <Button size="sm" variant="outline" disabled={disabled || !active} onClick={() => void control('stop')}><Square className="size-3.5" />停止</Button>
            <Button size="sm" variant="outline" disabled={disabled || !canReplay} onClick={() => void control('replay')}><RotateCcw className="size-3.5" />重播当前</Button>
            {busy && <Loader2 className="size-4 self-center animate-spin text-muted-foreground" />}
          </div>
        </CardContent>
      </Card>
      <form className="flex flex-wrap gap-2" onSubmit={(e) => { e.preventDefault(); if (query.trim() && !disabled) void control('search') }}>
        <Input className="min-w-40 flex-1" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="输入歌曲或歌手" aria-label="歌曲或歌手" />
        <Button type="submit" disabled={disabled || !query.trim()}>单次点播</Button>
        <Button type="button" variant="outline" disabled={disabled} onClick={() => void control('daily')}>每日推荐</Button>
      </form>
      <Card size="sm">
        <CardHeader><CardTitle className="text-ui-sm">{state?.queue_name || '播放队列'} · {queue.length} {itemLabel}</CardTitle></CardHeader>
        <CardContent className="flex flex-col gap-1">
          {!queue.length && <div className="py-6 text-center text-ui-sm text-muted-foreground">尚未选择持续歌单，单次点播不会填充队列</div>}
          {state?.one_shot && canReturn && <div className="px-3 py-2 text-ui-xs text-muted-foreground">已保留原歌单，点播结束后从第 {state.index + 2} 首继续</div>}
          {queue.map((song, index) => <button key={`${song.id}-${index}`} type="button" disabled={disabled} onClick={() => void control('select', index)} aria-label={`播放第 ${index + 1} 首 ${song.name}`} aria-current={!state?.one_shot && index === state?.index ? 'true' : undefined}
            className={`flex items-center gap-3 rounded-md px-3 py-2 text-left text-ui-sm hover:bg-muted disabled:opacity-50 ${!state?.one_shot && index === state?.index ? 'bg-muted' : ''}`}>
            <span className="w-7 shrink-0 text-muted-foreground">{!state?.one_shot && index === state?.index ? <Music2 className="size-3.5" /> : index + 1}</span>
            <span className="min-w-0 flex-1"><span className="block truncate">{song.name}</span><span className="block truncate text-ui-xs text-muted-foreground">{song.artists}</span></span>
            <span className="text-ui-xs text-muted-foreground">{song.duration_ms ? time(song.duration_ms) : ''}</span>
          </button>)}
        </CardContent>
      </Card>
    </div>
  )
}
