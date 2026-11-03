import { useCallback, useEffect, useState } from 'react'
import { BookOpenText, Loader2, Pause, Play, RefreshCw, SkipBack, SkipForward, Square } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { api } from '@/lib/api'

interface Props { onNotify: (message: string, kind?: 'success' | 'error' | 'info') => void; onError: (err: unknown) => void }
interface Chapter { id: string; number: number; title: string; characters: number; file: string }
interface Book { id: string; title: string; author: string; version: string; chapters: Chapter[] }
interface Playback { status: string; book_id?: string; title?: string; chapter_title?: string; part: number; segment: number; total_segments: number; buffered: number; error?: string; bookmarks: Record<string, { chapter_id: string; part: number; completed: boolean }> }
interface Response { enabled: boolean; message?: string; books?: Book[]; playback?: Playback }
const labels: Record<string, string> = { idle: '未开始', preparing: '准备音频', playing: '正在朗读', paused: '已暂停', interrupted: '对话后继续', stopped: '已停止', completed: '全书已播完', error: '需要处理' }

export function AudiobooksView({ onNotify, onError }: Props) {
 const [data, setData] = useState<Response>()
 const [playback, setPlayback] = useState<Playback>()
 const [bookID, setBookID] = useState('')
 const [pending, setPending] = useState(false)
 const refresh = useCallback(async () => {
  const next = await api<Response>('/admin/api/audiobooks')
  setData(next); setPlayback(next.playback)
  setBookID(old => old || next.playback?.book_id || next.books?.[0]?.id || '')
 }, [])
 useEffect(() => { void refresh().catch(onError) }, [refresh, onError])
 useEffect(() => {
  if (!data?.enabled) return
  let alive = true, running = false
  const timer = window.setInterval(() => {
   if (running) return
   running = true
   void api<Playback>('/admin/api/audiobooks/status').then(s => { if (alive) setPlayback(s) }).catch(() => {}).finally(() => { running = false })
  }, 2000)
  return () => { alive = false; window.clearInterval(timer) }
 }, [data?.enabled])
 const act = async (action: string, chapter_id?: string) => {
  setPending(true)
  try {
   const result = await api<{ message: string; playback: Playback }>('/admin/api/audiobooks', { method: 'POST', body: JSON.stringify({ action, book: bookID, chapter_id }) })
   setPlayback(result.playback); onNotify(result.message)
  } catch (err) { onError(err) } finally { setPending(false) }
 }
 const books = data?.books || [], book = books.find(b => b.id === bookID)
 return <div className="flex h-full min-h-0 flex-col gap-3 overflow-hidden">
  <div className="flex shrink-0 items-center justify-between gap-3">
   <div><h1 className="text-lg font-semibold">听书</h1><p className="text-sm text-muted-foreground">知识库原文 · 豆包声音 · 按需生成，重复收听复用音频</p></div>
   <Button variant="outline" size="sm" onClick={() => { void refresh().catch(onError) }}><RefreshCw className="size-4" />刷新书架</Button>
  </div>
  {!data ? <Loader2 className="size-5 shrink-0 animate-spin" /> : !data.enabled ? <p className="text-sm text-muted-foreground">{data.message}</p> : <>
   {playback?.book_id && <Card className="shrink-0"><CardHeader className="shrink-0"><CardTitle className="text-base">{playback.title}</CardTitle><CardDescription>{labels[playback.status] || playback.status}{playback.chapter_title && ` · ${playback.chapter_title} · 第 ${playback.part} 段`}</CardDescription></CardHeader>
    <CardContent className="space-y-3">
     {playback && playback.total_segments > 0 && <p className="text-sm text-muted-foreground">正文分段 {Math.min(playback.segment, playback.total_segments)} / {playback.total_segments} · 当前准备好 {playback.buffered} 段</p>}
     {playback?.error && <p role="alert" className="text-sm text-destructive">{playback.error}</p>}
     <div className="flex flex-wrap gap-2">
      <Button variant="outline" size="sm" disabled={pending || !playback?.book_id} onClick={() => void act('previous_chapter')}><SkipBack className="size-4" />上一章</Button>
      <Button variant="outline" size="sm" disabled={pending || !playback?.book_id} onClick={() => void act('pause')}><Pause className="size-4" />暂停</Button>
      <Button variant="outline" size="sm" disabled={pending || !['paused', 'interrupted'].includes(playback?.status || '')} onClick={() => void act('resume')}><Play className="size-4" />继续</Button>
      <Button variant="outline" size="sm" disabled={pending || !playback?.book_id} onClick={() => void act('next_chapter')}><SkipForward className="size-4" />下一章</Button>
      <Button variant="outline" size="sm" disabled={pending || !playback?.book_id} onClick={() => void act('stop')}><Square className="size-4" />停止</Button>
     </div>
     <p className="text-xs text-muted-foreground">插话时暂停听书，回答结束后继续；停止保留音频和书签。重启后从当前段续听，可能重听这一小段。听书替换音乐，使用媒体音量；聊天回答仍使用独立 TTS 音量。</p>
    </CardContent></Card>}
   <div className="grid min-h-0 flex-1 grid-rows-2 gap-3 overflow-hidden lg:grid-cols-[260px_minmax(0,1fr)] lg:grid-rows-1">
    <Card className="min-h-0"><CardHeader className="shrink-0"><CardTitle className="text-base">书架</CardTitle></CardHeader><CardContent className="min-h-0 flex-1 space-y-2 overflow-y-auto overscroll-contain [scrollbar-gutter:stable]">
     {books.length === 0 && <p className="text-sm text-muted-foreground">请先在知识库导入并发布书籍。</p>}
     {books.map(b => <button key={b.id} onClick={() => setBookID(b.id)} className={`w-full rounded-md border p-3 text-left ${bookID === b.id ? 'border-primary bg-primary-soft' : 'border-border hover:bg-muted'}`}><div className="flex min-w-0 gap-2"><BookOpenText className="mt-0.5 size-4 shrink-0" /><span className="min-w-0 break-words text-sm font-medium">{b.title}</span></div><p className="mt-1 text-xs text-muted-foreground">{b.author} · {b.chapters.length} 章{playback?.bookmarks[b.id] && !playback.bookmarks[b.id].completed ? ' · 有书签' : ''}</p></button>)}
    </CardContent></Card>
    <Card className="min-h-0"><CardHeader className="shrink-0"><div className="flex flex-wrap items-center justify-between gap-2"><CardTitle className="text-base">{book?.title || '章节目录'}</CardTitle><Button size="sm" disabled={pending || !book} onClick={() => void act('play')}><Play className="size-4" />{book && playback?.bookmarks[book.id] && !playback.bookmarks[book.id].completed ? '从书签续听' : '从头开始'}</Button></div><CardDescription>章节直接读取已发布正文的标题。第一章请按标题选择；开篇、前言和第〇章也会保留。</CardDescription></CardHeader>
     <CardContent className="min-h-0 flex-1 space-y-1 overflow-y-auto overscroll-contain [scrollbar-gutter:stable]">{book?.chapters.map(c => <div key={c.id} className="flex items-center justify-between gap-3 rounded-md px-2 py-2 hover:bg-muted"><div className="min-w-0"><p className="break-words text-sm">{c.title}</p><p className="text-xs text-muted-foreground">约 {c.characters.toLocaleString()} 字</p></div><Button className="shrink-0" variant="ghost" size="sm" disabled={pending} onClick={() => void act('play', c.id)}><Play className="size-4" />从这里听</Button></div>)}</CardContent>
    </Card>
   </div>
  </>}
 </div>
}
