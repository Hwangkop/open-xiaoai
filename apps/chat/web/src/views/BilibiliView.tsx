import { useEffect, useState, type FormEvent } from 'react'
import { Download, ExternalLink, ListMusic, ListStart, Loader2, Pause, Play, RefreshCw, Search, Shuffle, SkipBack, SkipForward, Square, Tv, X } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, getBase } from '@/lib/api'
import { cn } from '@/lib/utils'

interface Props {
  onNotify: (message: string, kind?: 'success' | 'error' | 'info') => void
  onError: (err: unknown) => void
}

interface BilibiliItem {
  bvid: string
  aid: number
  title: string
  author: string
  duration: string
  duration_seconds: number
  cover: string
  url: string
  score: number
  recommended: boolean
}

interface BilibiliStatus {
  enabled: boolean
  message?: string
  ffmpeg_ready: boolean
  ffmpeg: string
  cached_files: number
}

interface BilibiliActionResult {
  message: string
  title: string
  author: string
  page?: string
  download_url: string
  saved_name?: string
  audio_quality?: string
}

interface CatalogItem { bvid: string; cid: number; title: string; author?: string; duration_seconds: number; page?: number; section?: string }
interface Catalog { video: CatalogItem; selected_cid: number; parts: CatalogItem[]; collection?: { id: number; title: string; total: number; items: CatalogItem[] } }
interface QueueState { source: string; queue_name: string; index: number; queue?: { name: string }[]; current?: { name: string }; error?: string; playback: { status: string } }
const playbackLabels: Record<string, string> = { loading: '正在准备音轨', playing: '播放中', paused: '已暂停', stopped: '已停止', ended: '队列已播完', error: '播放失败', interrupted: '语音对话中', recovering: '正在恢复', unknown: '等待设备状态', stalled: '播放中断' }
function formatDuration(seconds: number) {
  const h = Math.floor(seconds / 3600), m = Math.floor(seconds % 3600 / 60), s = String(seconds % 60).padStart(2, '0')
  return h ? `${h}:${String(m).padStart(2, '0')}:${s}` : `${m}:${s}`
}

export function BilibiliView({ onNotify, onError }: Props) {
  const [status, setStatus] = useState<BilibiliStatus | null>(null)
  const [query, setQuery] = useState('')
  const [items, setItems] = useState<BilibiliItem[]>([])
  const [searching, setSearching] = useState(false)
  const [busy, setBusy] = useState('')
  const [downloads, setDownloads] = useState<Record<string, { url: string; name: string; quality?: string }>>({})
  const [catalog, setCatalog] = useState<Catalog | null>(null)
  const [kind, setKind] = useState<'collection' | 'parts'>('collection')
  const [queue, setQueue] = useState<QueueState | null>(null)

  const loadPlayback = async () => { setQueue(await api<QueueState>('/admin/api/music')) }
  useEffect(() => {
    let alive = true, running = false
    const poll = () => {
      if (running) return
      running = true
      void api<QueueState>('/admin/api/music').then(value => { if (alive) setQueue(value) }).catch(() => {}).finally(() => { running = false })
    }
    poll()
    const timer = window.setInterval(poll, 2000)
    return () => { alive = false; window.clearInterval(timer) }
  }, [])

  const inspect = async (reference: string) => {
    setSearching(true)
    try {
      const value = await api<Catalog>(`/admin/api/bilibili/catalog?reference=${encodeURIComponent(reference)}`)
      setCatalog(value); setKind(value.collection ? 'collection' : 'parts')
    } catch (err) { onError(err) } finally { setSearching(false) }
  }

  const enqueue = async (index = 0, shuffle = false) => {
    if (!catalog) return
    setBusy('queue')
    try {
      const result = await api<{ message: string }>('/admin/api/bilibili/queue', { method: 'POST', body: JSON.stringify({ reference: catalog.video.bvid, kind, index, shuffle }) })
      onNotify(result.message, 'success'); await loadPlayback()
    } catch (err) { onError(err) } finally { setBusy('') }
  }

  const control = async (action: string) => {
    setBusy(action)
    try {
      const result = await api<{ message: string }>('/admin/api/music', { method: 'POST', body: JSON.stringify({ action }) })
      onNotify(result.message, 'success'); await loadPlayback()
    } catch (err) { onError(err) } finally { setBusy('') }
  }

  const playItem = async (item: CatalogItem) => {
    setBusy(`item:${item.bvid}:${item.cid}`)
    try {
      const result = await api<{ message: string }>('/admin/api/bilibili/play', { method: 'POST', body: JSON.stringify({ bvid: item.bvid, cid: item.cid }) })
      onNotify(result.message, 'success'); await loadPlayback()
    } catch (err) { onError(err) } finally { setBusy('') }
  }

  const loadStatus = async () => {
    try {
      setStatus(await api<BilibiliStatus>('/admin/api/bilibili/status'))
    } catch (err) {
      onError(err)
    }
  }

  useEffect(() => {
    void loadStatus()
  }, [])

  const search = async (event: FormEvent) => {
    event.preventDefault()
    const value = query.trim()
    if (!value || searching) return
    if (/^BV[0-9a-z]+$/i.test(value) || /^https?:\/\//i.test(value)) { await inspect(value); return }
    setSearching(true)
    try {
      const result = await api<{ items: BilibiliItem[] }>(
        `/admin/api/bilibili/search?q=${encodeURIComponent(value)}`,
      )
      setItems(result.items || [])
      setCatalog(null)
      if (!result.items?.length) onNotify('B 站没有搜到匹配视频', 'info')
    } catch (err) {
      onError(err)
    } finally {
      setSearching(false)
    }
  }

  const act = async (item: BilibiliItem, action: 'play' | 'download') => {
    if (!status?.ffmpeg_ready) {
      onNotify('ffmpeg 不可用，请先检查 B 站音乐配置', 'error')
      return
    }
    const key = `${item.bvid}:${action}`
    setBusy(key)
    try {
      const result = await api<BilibiliActionResult>(`/admin/api/bilibili/${action}`, {
        method: 'POST',
        body: JSON.stringify({ bvid: item.bvid }),
      })
      if (action === 'play') {
        onNotify(result.message, 'success')
        await loadPlayback()
      } else {
        setDownloads((old) => ({
          ...old,
          [item.bvid]: {
            url: getBase() + result.download_url,
            name: result.saved_name || item.title,
            quality: result.audio_quality,
          },
        }))
        onNotify(result.message, 'success')
      }
      await loadStatus()
    } catch (err) {
      onError(err)
    } finally {
      setBusy('')
    }
  }

  const stop = async () => {
    setBusy('stop')
    try {
      await api('/admin/api/music', {
        method: 'POST',
        body: JSON.stringify({ action: 'stop' }),
      })
      await loadPlayback()
      onNotify('已停止播放', 'success')
    } catch (err) {
      onError(err)
    } finally {
      setBusy('')
    }
  }

  const catalogItems = kind === 'collection' ? catalog?.collection?.items || [] : catalog?.parts || []
  const catalogTitle = kind === 'collection' && catalog?.collection ? catalog.collection.title : catalog?.video.title
  const queueActive = queue?.source === 'bilibili'
  const canPlay = !!status?.ffmpeg_ready

  if (status && !status.enabled) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 text-ui-sm text-muted-foreground">
        <Tv className="size-8 opacity-40" />
        <div>{status.message || 'B 站音乐未启用'}</div>
        <Button size="sm" variant="outline" onClick={() => { window.location.hash = 'config' }}>去配置</Button>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col gap-3 overflow-hidden">
      <div className="flex shrink-0 flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-ui-lg font-semibold">B站音乐</h1>
            {status && (
              <Badge variant={status.ffmpeg_ready ? 'secondary' : 'outline'} className="text-ui-xs">
                {status.ffmpeg_ready ? '可以播放与下载' : 'ffmpeg 不可用'}
              </Badge>
            )}
          </div>
          <div className="text-ui-sm text-muted-foreground">搜索音乐，或粘贴视频链接读取合集与分 P</div>
        </div>
        <div className="flex gap-1.5">
          {queue?.source === 'bilibili' && (
            <Button size="sm" variant="outline" onClick={() => void stop()} disabled={busy !== ''}>
              {busy === 'stop' ? <Loader2 className="size-3.5 animate-spin" /> : <Square className="size-3.5" />}
              停止
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => void loadStatus()}>
            <RefreshCw className="size-3.5" /> 刷新
          </Button>
        </div>
      </div>

      {status && !status.ffmpeg_ready && (
        <div className="shrink-0 border border-destructive/30 bg-destructive-surface px-3 py-2 text-ui-sm text-destructive">
          ffmpeg 不可用，搜索仍可使用，但暂时不能转换音轨。请在配置页检查路径。
        </div>
      )}

      <form onSubmit={(event) => void search(event)} className="flex w-full shrink-0 flex-col gap-2 sm:max-w-[560px] sm:flex-row">
        <Input
          className="min-w-0 flex-1"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="搜索歌名、歌手，或粘贴 B 站视频链接 / BV 号"
          aria-label="B站音乐搜索"
          maxLength={2048}
        />
        <Button type="submit" disabled={searching || !query.trim()}>
          {searching ? <Loader2 className="size-3.5 animate-spin" /> : <Search className="size-3.5" />}
          搜索 / 读取目录
        </Button>
      </form>

      <div
        className={cn(
          'min-h-0 flex-1',
          catalog
            ? 'grid gap-3 overflow-y-auto xl:grid-cols-[minmax(0,1fr)_380px] xl:overflow-hidden'
            : 'overflow-y-auto pr-1',
        )}
      >
        <div className={cn('min-w-0', catalog && 'min-h-0 xl:overflow-y-auto xl:pr-1')}>
          {queueActive && (
            <section className="mb-3 rounded-lg border border-border bg-card p-3">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="text-ui-xs text-muted-foreground">当前播放</span>
                    <Badge variant="secondary">{playbackLabels[queue.playback.status] || queue.playback.status}</Badge>
                  </div>
                  <p className="mt-0.5 truncate text-ui-sm font-medium">{queue.current?.name}</p>
                  <p className="text-ui-xs text-muted-foreground">{queue.queue_name} · 第 {queue.index + 1} / {queue.queue?.length || 0} 项</p>
                </div>
                <div className="flex flex-wrap gap-1.5">
                  <Button size="sm" variant="outline" disabled={!!busy || queue.index <= 0} onClick={() => void control('previous')}><SkipBack className="size-3.5" />上一项</Button>
                  <Button size="sm" variant="outline" disabled={!!busy} onClick={() => void control(['playing', 'loading'].includes(queue.playback.status) ? 'pause' : 'resume')}>
                    {['playing', 'loading'].includes(queue.playback.status) ? <Pause className="size-3.5" /> : <Play className="size-3.5" />}
                    {['playing', 'loading'].includes(queue.playback.status) ? '暂停' : '继续'}
                  </Button>
                  <Button size="sm" variant="outline" disabled={!!busy || queue.index + 1 >= (queue.queue?.length || 0)} onClick={() => void control('next')}><SkipForward className="size-3.5" />下一项</Button>
                  <Button size="sm" variant="outline" onClick={() => { window.location.hash = 'music' }}>播放队列</Button>
                </div>
              </div>
              {queue.error && <p role="alert" className="mt-2 text-ui-sm text-destructive">{queue.error}</p>}
            </section>
          )}

          {items.length > 0 && (
            <div className="mb-2 flex items-center justify-between gap-2">
              <div className="text-ui-sm font-medium">搜索结果</div>
              <div className="text-ui-xs text-muted-foreground">{items.length} 个视频</div>
            </div>
          )}

          {!items.length && (
            <div className="flex min-h-48 flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-border text-ui-sm text-muted-foreground">
              <Tv className="size-7 opacity-35" />
              {catalog ? '当前没有搜索结果，可输入链接直接读取合集' : '搜索 B 站公开视频，选择后提取音轨'}
            </div>
          )}

          <div className={cn('grid gap-3', catalog ? 'grid-cols-1' : 'lg:grid-cols-2')}>
            {items.map((item) => {
              const playBusy = busy === `${item.bvid}:play`
              const downloadBusy = busy === `${item.bvid}:download`
              const downloaded = downloads[item.bvid]
              return (
                <article
                  key={item.bvid}
                  className={cn(
                    'flex min-w-0 flex-col overflow-hidden rounded-lg border border-border bg-card',
                    catalog && 'sm:grid sm:grid-cols-[210px_minmax(0,1fr)]',
                    catalog?.video.bvid === item.bvid && 'border-primary/60',
                  )}
                >
                  <div className={cn('relative overflow-hidden bg-muted', catalog ? 'aspect-video sm:aspect-auto sm:h-full sm:min-h-[124px]' : 'aspect-video')}>
                    {item.cover && (
                      <img
                        src={item.cover}
                        alt=""
                        className="size-full object-cover"
                        loading="lazy"
                        referrerPolicy="no-referrer"
                      />
                    )}
                    <span className="absolute right-1.5 bottom-1.5 rounded bg-black/70 px-1.5 py-0.5 font-mono text-ui-xs text-white">
                      {item.duration}
                    </span>
                  </div>
                  <div className={cn('flex min-h-0 flex-1 flex-col', catalog ? 'p-3' : 'p-2.5')}>
                    <div className="line-clamp-2 min-h-10 text-ui-sm font-medium leading-5">{item.title}</div>
                    <div className="mt-1 flex items-center justify-between gap-2 text-ui-xs text-muted-foreground">
                      <span className="flex min-w-0 items-center gap-1.5">
                        {item.recommended && <Badge variant="secondary" className="shrink-0 text-ui-xs">推荐</Badge>}
                        <span className="truncate">{item.author}</span>
                      </span>
                      <a href={item.url} target="_blank" rel="noreferrer" className="inline-flex shrink-0 items-center gap-0.5 hover:text-foreground">
                        原视频 <ExternalLink className="size-3" />
                      </a>
                    </div>
                    <div className="mt-2 flex flex-wrap items-center gap-1.5">
                      <Button size="sm" disabled={!!busy || !canPlay} onClick={() => void act(item, 'play')}>
                        {playBusy ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
                        播放音频
                      </Button>
                      <Button size="sm" variant="outline" disabled={!!busy || !canPlay} onClick={() => void act(item, 'download')}>
                        {downloadBusy ? <Loader2 className="size-3.5 animate-spin" /> : <Download className="size-3.5" />}
                        保存到音乐库
                      </Button>
                      <Button size="sm" variant={catalog?.video.bvid === item.bvid ? 'secondary' : 'outline'} disabled={searching} onClick={() => void inspect(item.bvid)}>
                        <ListMusic className="size-3.5" />合集 / 分 P
                      </Button>
                    </div>
                    {downloaded && (
                      <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-ui-xs text-success">
                        <span className="min-w-0 truncate">已保存：{downloaded.name}</span>
                        {downloaded.quality && <span className="text-muted-foreground">{downloaded.quality}</span>}
                        <a href={downloaded.url} download={downloaded.name} className="inline-flex shrink-0 items-center gap-0.5 text-primary hover:underline">
                          <Download className="size-3" /> 下载副本
                        </a>
                      </div>
                    )}
                  </div>
                </article>
              )
            })}
          </div>
          <p className="mt-3 text-ui-xs text-muted-foreground">
            自动选择 B 站可用的最高音轨：有 Hi-Res 无损时保存为 FLAC，否则保存 320kbps MP3。播放缓存统一转换为 MP3，兼容音箱。
          </p>
        </div>

        {catalog && (
          <aside className="order-first flex h-[min(70vh,620px)] min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-card xl:order-none xl:h-auto">
            <div className="shrink-0 border-b border-border p-3">
              <div className="flex items-start gap-2.5">
                <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-primary-soft text-primary">
                  <ListMusic className="size-4" />
                </span>
                <div className="min-w-0 flex-1">
                  <h2 className="line-clamp-2 text-ui-sm font-semibold leading-5">{catalogTitle}</h2>
                  <p className="mt-0.5 text-ui-xs text-muted-foreground">
                    {kind === 'collection' ? `${catalogItems.length} 个视频` : `${catalogItems.length} 个分 P`}
                    {catalog.video.author && ` · ${catalog.video.author}`}
                  </p>
                </div>
                <Button size="icon-sm" variant="ghost" title="关闭合集" aria-label="关闭合集" onClick={() => setCatalog(null)}>
                  <X />
                </Button>
              </div>
              <div className="mt-3 flex gap-1 rounded-md bg-muted p-0.5">
                {catalog.collection && (
                  <button
                    type="button"
                    onClick={() => setKind('collection')}
                    className={cn(
                      'flex h-7 min-w-0 flex-1 items-center justify-center gap-1 rounded-sm px-2 text-ui-sm transition-colors',
                      kind === 'collection' ? 'bg-card font-medium text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground',
                    )}
                  >
                    <ListMusic className="size-3.5 shrink-0" />合集 · {catalog.collection.total}
                  </button>
                )}
                <button
                  type="button"
                  onClick={() => setKind('parts')}
                  className={cn(
                    'flex h-7 min-w-0 flex-1 items-center justify-center rounded-sm px-2 text-ui-sm transition-colors',
                    kind === 'parts' ? 'bg-card font-medium text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground',
                  )}
                >
                  分 P · {catalog.parts.length}
                </button>
              </div>
            </div>

            <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain [scrollbar-gutter:stable]">
              {catalogItems.map((item, index) => (
                <div
                  key={`${item.bvid}-${item.cid}`}
                  className="grid grid-cols-[24px_minmax(0,1fr)_auto] items-center gap-x-2 border-b border-border/65 px-3 py-2.5 last:border-b-0 hover:bg-muted/45"
                >
                  <span className="text-ui-xs tabular-nums text-muted-foreground">{String(index + 1).padStart(2, '0')}</span>
                  <div className="min-w-0">
                    <p className="line-clamp-2 text-ui-sm leading-5">{item.title}</p>
                    <p className="mt-0.5 text-ui-xs text-muted-foreground">
                      {item.duration_seconds ? formatDuration(item.duration_seconds) : '时长待获取'}
                      {item.section && ` · ${item.section}`}
                    </p>
                  </div>
                  <div className="flex items-center justify-end gap-1">
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      title="仅播放此项"
                      aria-label={`仅播放 ${item.title}`}
                      disabled={!!busy || !canPlay}
                      onClick={() => void playItem(item)}
                    >
                      {busy === `item:${item.bvid}:${item.cid}` ? <Loader2 className="animate-spin" /> : <Play />}
                    </Button>
                    <Button
                      size="icon-sm"
                      variant="outline"
                      title="从这里连续播放"
                      aria-label={`从 ${item.title} 开始连续播放`}
                      disabled={!!busy || !canPlay}
                      onClick={() => void enqueue(index)}
                    >
                      <ListStart />
                    </Button>
                  </div>
                </div>
              ))}
            </div>

            <div className="shrink-0 border-t border-border p-3">
              <div className="grid grid-cols-2 gap-2">
                <Button disabled={!!busy || !canPlay || catalogItems.length === 0} onClick={() => void enqueue()}>
                  {busy === 'queue' ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
                  顺序播放
                </Button>
                <Button variant="outline" disabled={!!busy || !canPlay || catalogItems.length === 0} onClick={() => void enqueue(0, true)}>
                  <Shuffle className="size-3.5" />随机播放
                </Button>
              </div>
              <div className="mt-2 flex items-center justify-between text-ui-xs text-muted-foreground">
                <span>音轨缓存</span>
                <span>{status?.cached_files || 0} 个文件</span>
              </div>
            </div>
          </aside>
        )}
      </div>
    </div>
  )
}
