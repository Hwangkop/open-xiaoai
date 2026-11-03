import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react'
import {
  AlertTriangle,
  BookOpenText,
  CheckCircle2,
  ChevronRight,
  FileText,
  Loader2,
  Power,
  RefreshCw,
  Search,
  Send,
  ShieldCheck,
  Upload,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'

interface Props {
  onNotify: (message: string, kind?: 'success' | 'error' | 'info') => void
  onError: (err: unknown) => void
}

interface KnowledgeFile {
  path: string
  bytes: number
  format: 'markdown' | 'text' | 'pdf' | 'image' | 'other'
  state: 'indexed' | 'only_saved' | 'missing' | 'error'
  message?: string
}

interface KnowledgeVersion {
  id: string
  label: string
  status: string
  published: boolean
  created_at: string
  published_at?: string
  saved_files: number
  indexed_files: number
  sections: number
  error?: string
  files?: KnowledgeFile[]
}

interface KnowledgeSource {
  id: string
  name: string
  author?: string
  kind: string
  description?: string
  topics: string[]
  use_cases: string[]
  avoid: string[]
  enabled: boolean
  published_version?: string
  current?: KnowledgeVersion
  versions: KnowledgeVersion[]
  coverage: { saved_files: number; indexed_files: number; sections: number }
}

interface KnowledgeRuntime {
  state: 'loaded' | 'pending' | 'error' | 'unconfirmed'
  message: string
  active_sources: Record<string, string>
  errors?: { error: string }[]
}

interface RuleItem {
  title: string
  text: string
}

interface KnowledgeResponse {
  enabled: boolean
  message?: string
  generation: number
  sources: KnowledgeSource[]
  runtime: KnowledgeRuntime
  total_saved: number
  total_indexed: number
  total_sections: number
  warning?: string
  rules: { default: RuleItem[]; prompt_path: string }
}

interface SearchMatch {
  source_id: string
  source_title: string
  title: string
  author?: string
  version_id: string
  edition?: string
  section_id: string
  chapter: string
  section: string
  filename: string
  start_line: number
  end_line: number
  preview: string
}

interface SearchResponse {
  query: string
  source_id?: string | null
  generation: number
  matches: SearchMatch[]
}

interface SectionRead {
  source_title: string
  section_id: string
  chapter: string
  section: string
  start_line: number
  end_line: number
  offset: number
  text: string
  next_offset?: number | null
  has_more: boolean
}

interface FileRead {
  source_id: string
  source_title: string
  path: string
  offset: number
  next_offset?: number | null
  total_lines: number
  text: string
}

type Tab = 'books' | 'search' | 'rules'

const TABS: { id: Tab; label: string; icon: typeof BookOpenText }[] = [
  { id: 'books', label: '书架', icon: BookOpenText },
  { id: 'search', label: '检索试验', icon: Search },
  { id: 'rules', label: '使用规则', icon: ShieldCheck },
]

function WorkerBadge({ runtime }: { runtime: KnowledgeRuntime }) {
  const loaded = runtime.state === 'loaded'
  const failed = runtime.state === 'error'
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 text-ui-xs',
        loaded && 'text-success',
        failed && 'text-destructive',
        !loaded && !failed && 'text-muted-foreground',
      )}
      title={runtime.message}
    >
      <span
        className={cn(
          'size-1.5 rounded-full',
          loaded && 'bg-success',
          failed && 'bg-destructive',
          !loaded && !failed && 'bg-muted-foreground/60',
        )}
      />
      {loaded ? '已生效' : failed ? '加载失败' : '等待生效'}
    </span>
  )
}

function EmptyState({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-44 flex-col items-center justify-center gap-3 border border-dashed border-border px-6 text-center text-ui-sm text-muted-foreground">
      <BookOpenText className="size-8 opacity-35" />
      {children}
    </div>
  )
}

function BookCard({
  source,
  runtime,
  busy,
  onOpen,
  onToggle,
  onPublish,
}: {
  source: KnowledgeSource
  runtime: KnowledgeRuntime
  busy: string
  onOpen: () => void
  onToggle: () => void
  onPublish: (version: KnowledgeVersion) => void
}) {
  const staged = source.versions.find((version) => version.status === 'staged')
  const active = runtime.active_sources[source.id] === source.current?.id
  return (
    <article className="group flex min-h-[154px] min-w-0 flex-col rounded-lg border border-border bg-card p-3 transition-colors hover:border-border-hover">
      <div className="flex min-w-0 items-start gap-2.5">
        <div className="relative flex h-14 w-10 shrink-0 items-center justify-center overflow-hidden rounded-r-md border border-border bg-muted/55 text-muted-foreground">
          <span className="absolute inset-y-0 left-0 w-1 bg-primary/35" />
          <BookOpenText className="ml-1 size-4" />
        </div>
        <button type="button" onClick={onOpen} className="min-w-0 flex-1 text-left">
          <span className="flex min-w-0 items-start gap-1.5">
            <span className="line-clamp-2 min-w-0 flex-1 text-ui-base font-medium leading-5">{source.name}</span>
            {!source.enabled && <Badge variant="outline" className="shrink-0 text-ui-xs">已停用</Badge>}
          </span>
          <span className="mt-0.5 block truncate text-ui-xs text-muted-foreground">
            {source.author || '未填写作者'}
          </span>
        </button>
        <Button size="icon-xs" variant="ghost" onClick={onOpen} title="查看内容" aria-label="查看内容">
          <ChevronRight className="size-3.5" />
        </Button>
      </div>

      <p className="mt-2 line-clamp-2 min-h-8 text-ui-xs leading-4 text-muted-foreground">
        {source.description || '没有填写说明'}
      </p>

      <div className="mt-auto flex items-center gap-1 border-t border-border pt-2">
        <span className="min-w-0 flex-1 truncate text-ui-xs text-muted-foreground">
          {source.coverage.saved_files} 个文件 · {source.coverage.sections} 个片段
        </span>
        {staged ? (
          <Button size="xs" variant="outline" disabled={busy !== ''} onClick={() => onPublish(staged)}>
            {busy === `publish:${source.id}:${staged.id}` ? <Loader2 className="size-3 animate-spin" /> : <Send className="size-3" />}
            发布
          </Button>
        ) : source.current && !active ? (
          <span className="shrink-0 text-ui-xs text-muted-foreground">待生效</span>
        ) : null}
        <Button
          size="icon-xs"
          variant="ghost"
          disabled={busy !== ''}
          onClick={onToggle}
          title={source.enabled ? '停用这本书' : '启用这本书'}
          aria-label={source.enabled ? '停用这本书' : '启用这本书'}
        >
          {busy === `toggle:${source.id}` ? <Loader2 className="size-3 animate-spin" /> : <Power className="size-3" />}
        </Button>
      </div>
    </article>
  )
}

export function KnowledgeView({ onNotify, onError }: Props) {
  const [data, setData] = useState<KnowledgeResponse | null>(null)
  const [tab, setTab] = useState<Tab>('books')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [addOpen, setAddOpen] = useState(false)
  const [detail, setDetail] = useState<KnowledgeSource | null>(null)
  const [query, setQuery] = useState('记录开销 第三组练习')
  const [searchSource, setSearchSource] = useState('')
  const [searching, setSearching] = useState(false)
  const [searchResult, setSearchResult] = useState<SearchResponse | null>(null)
  const [searchError, setSearchError] = useState('')
  const [section, setSection] = useState<SectionRead | null>(null)
  const [sectionBusy, setSectionBusy] = useState(false)
  const [fileRead, setFileRead] = useState<FileRead | null>(null)
  const [fileBusy, setFileBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setData(await api<KnowledgeResponse>('/admin/api/knowledge'))
    } catch (err) {
      onError(err)
    } finally {
      setLoading(false)
    }
  }, [onError])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') {
        void api<KnowledgeResponse>('/admin/api/knowledge').then(setData).catch(() => {})
      }
    }, 15000)
    return () => window.clearInterval(timer)
  }, [])

  const enabledSources = useMemo(
    () => (data?.sources ?? []).filter((source) => source.enabled && source.current),
    [data],
  )

  const run = useCallback(
    async (key: string, task: () => Promise<string>) => {
      setBusy(key)
      try {
        const message = await task()
        onNotify(message, 'success')
        await load()
      } catch (err) {
        onError(err)
      } finally {
        setBusy('')
      }
    },
    [load, onError, onNotify],
  )

  const toggleSource = (source: KnowledgeSource) => {
    if (!data) return
    if (source.enabled && !window.confirm(`停用「${source.name}」后，agent 不会再查它。继续？`)) return
    void run(`toggle:${source.id}`, async () => {
      const result = await api<{ message: string }>(`/admin/api/knowledge/${source.id}`, {
        method: 'PATCH',
        body: JSON.stringify({
          expected_generation: data.generation,
          enabled: !source.enabled,
        }),
      })
      return result.message
    })
  }

  const publishVersion = (source: KnowledgeSource, version: KnowledgeVersion) => {
    if (!data) return
    void run(`publish:${source.id}:${version.id}`, async () => {
      const result = await api<{ message: string }>(`/admin/api/knowledge/${source.id}/publish`, {
        method: 'POST',
        body: JSON.stringify({
          expected_generation: data.generation,
          version_id: version.id,
        }),
      })
      return result.message
    })
  }

  const search = async (event: FormEvent) => {
    event.preventDefault()
    if (!query.trim() || searching) return
    setSearching(true)
    setSearchError('')
    try {
      setSearchResult(
        await api<SearchResponse>('/admin/api/knowledge/search', {
          method: 'POST',
          body: JSON.stringify({ query: query.trim(), source_id: searchSource, limit: 5 }),
        }),
      )
    } catch (err) {
      setSearchError(err instanceof Error ? err.message : String(err))
      setSearchResult(null)
    } finally {
      setSearching(false)
    }
  }

  const readSection = async (match: SearchMatch, offset = 0) => {
    setSectionBusy(true)
    try {
      setSection(
        await api<SectionRead>('/admin/api/knowledge/read', {
          method: 'POST',
          body: JSON.stringify({ section_id: match.section_id, offset, max_chars: 3000 }),
        }),
      )
    } catch (err) {
      onError(err)
    } finally {
      setSectionBusy(false)
    }
  }

  const readFile = async (source: KnowledgeSource, file: KnowledgeFile, offset = 0) => {
    if (file.format !== 'markdown' && file.format !== 'text') return
    setFileBusy(true)
    try {
      setFileRead(
        await api<FileRead>(
          `/admin/api/knowledge/${source.id}/file?path=${encodeURIComponent(file.path)}&offset=${offset}`,
        ),
      )
    } catch (err) {
      onError(err)
    } finally {
      setFileBusy(false)
    }
  }

  if (loading && !data) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-ui-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin" /> 正在打开书架…
      </div>
    )
  }

  if (!data?.enabled) {
    return (
      <div className="flex h-full min-h-0 flex-col gap-4 overflow-y-auto">
        <div>
          <h1 className="text-ui-lg font-semibold">知识库</h1>
          <div className="text-ui-sm text-muted-foreground">家里的书和笔记</div>
        </div>
        <EmptyState>
          <span>{data?.message || '知识库尚未启用'}</span>
          <Button size="sm" variant="outline" onClick={() => { window.location.hash = 'config' }}>去配置</Button>
        </EmptyState>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col gap-3 overflow-hidden">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h1 className="text-ui-lg font-semibold">知识库</h1>
            <WorkerBadge runtime={data.runtime} />
          </div>
          <div className="text-ui-xs text-muted-foreground">
            {data.sources.length} 本 · {data.total_indexed} 个可检索文件 · {data.total_sections} 个片段
          </div>
        </div>
        <div className="flex items-center gap-1.5">
          <Button size="sm" variant="ghost" onClick={() => void load()} disabled={loading} title="刷新">
            <RefreshCw className={cn('size-3.5', loading && 'animate-spin')} />
            刷新
          </Button>
          <Button size="sm" onClick={() => setAddOpen(true)}><Upload className="size-3.5" /> 添加书籍</Button>
        </div>
      </div>

      {data.warning && (
        <div className="flex items-start gap-1.5 text-ui-xs text-warning">
          <AlertTriangle className="mt-0.5 size-3 shrink-0" />
          <span>{data.warning}</span>
        </div>
      )}

      <div className="flex w-fit shrink-0 gap-0.5 rounded-md bg-muted p-0.5" role="tablist" aria-label="知识库视图">
        {TABS.map((item) => {
          const Icon = item.icon
          return (
            <button
              key={item.id}
              type="button"
              role="tab"
              aria-selected={tab === item.id}
              onClick={() => setTab(item.id)}
              className={cn(
                'flex h-7 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-sm px-2 text-ui-sm transition-colors sm:px-3',
                tab === item.id
                  ? 'bg-card font-medium text-foreground shadow-sm'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              <Icon className="size-3.5" /> {item.label}
            </button>
          )
        })}
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto pr-1">
        {tab === 'books' && (
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
            {data.sources.length === 0 && (
              <div className="md:col-span-2 xl:col-span-3 2xl:col-span-4">
                <EmptyState>
                  <span>书架还是空的。可以一次选择一本书的多个章节文件。</span>
                  <Button size="sm" onClick={() => setAddOpen(true)}><Upload className="size-3.5" /> 添加第一本书</Button>
                </EmptyState>
              </div>
            )}
            {data.sources.map((source) => (
              <BookCard
                key={source.id}
                source={source}
                runtime={data.runtime}
                busy={busy}
                onOpen={() => setDetail(source)}
                onToggle={() => toggleSource(source)}
                onPublish={(version) => publishVersion(source, version)}
              />
            ))}
            {data.sources.length > 0 && (
              <button
                type="button"
                onClick={() => setAddOpen(true)}
                className="flex min-h-[154px] flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-border text-ui-sm text-muted-foreground transition-colors hover:border-border-hover hover:bg-muted/30 hover:text-foreground"
              >
                <span className="flex size-9 items-center justify-center rounded-full bg-muted">
                  <Upload className="size-4" />
                </span>
                添加书籍
              </button>
            )}
          </div>
        )}

        {tab === 'search' && (
          <div className="mx-auto flex w-full max-w-3xl flex-col gap-3">
            <form onSubmit={(event) => void search(event)} className="space-y-2">
              <div className="flex flex-col gap-2 sm:flex-row">
                <Input
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  placeholder="问一本书里的问题"
                  aria-label="检索问题"
                  maxLength={300}
                />
                <select
                  value={searchSource}
                  onChange={(event) => setSearchSource(event.target.value)}
                  className="h-7 min-w-36 rounded-md border border-input-border bg-input px-2 text-ui-sm outline-none focus:border-input-border-focused"
                  aria-label="检索范围"
                >
                  <option value="">全部书籍</option>
                  {enabledSources.map((source) => <option key={source.id} value={source.id}>{source.name}</option>)}
                </select>
                <Button type="submit" disabled={searching || !query.trim()}>
                  {searching ? <Loader2 className="size-3.5 animate-spin" /> : <Search className="size-3.5" />}
                  搜索
                </Button>
              </div>
              <div className="text-ui-xs text-muted-foreground">只测试 agent 能找到什么，不会播放声音或操作设备。</div>
            </form>
            {searchError && <div role="alert" className="text-ui-sm text-destructive">{searchError}</div>}
            {searchResult && (
              <div className="border-y border-border py-1.5 text-ui-xs text-muted-foreground">
                {searchResult.matches.length} 个结果
              </div>
            )}
            {searchResult && searchResult.matches.length === 0 && (
              <EmptyState>没找到。换一个更具体的问法，或者确认这本书已经启用。</EmptyState>
            )}
            {searchResult?.matches.map((match) => (
              <button
                key={match.section_id}
                type="button"
                onClick={() => void readSection(match)}
                className="group w-full border border-border bg-card px-3 py-2.5 text-left transition-colors hover:border-border-hover hover:bg-muted/35"
              >
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="text-ui-sm font-medium">{match.title}</span>
                  <span className="text-ui-xs text-muted-foreground">{match.section}</span>
                  <ChevronRight className="ml-auto size-3.5 text-muted-foreground" />
                </div>
                <div className="mt-1 line-clamp-3 whitespace-pre-wrap text-ui-sm text-muted-foreground">{match.preview}</div>
              </button>
            ))}
          </div>
        )}

        {tab === 'rules' && (
          <div className="mx-auto flex w-full max-w-3xl flex-col gap-3">
            <div className="flex items-center justify-between gap-2">
              <div>
                <div className="text-ui-sm font-medium">默认使用方式</div>
                <div className="text-ui-xs text-muted-foreground">规则来自系统提示词，这里只查看，不另开一套编辑器。</div>
              </div>
              <Button size="sm" variant="outline" onClick={() => { window.location.hash = 'config' }}>配置提示词</Button>
            </div>
            <div className="divide-y divide-border border-y border-border">
              {data.rules.default.map((rule) => (
                <div key={rule.title} className="grid gap-1 py-2.5 sm:grid-cols-[110px_1fr]">
                  <div className="text-ui-sm font-medium">{rule.title}</div>
                  <div className="text-ui-sm text-muted-foreground">{rule.text}</div>
                </div>
              ))}
            </div>
            <p className="text-ui-xs text-muted-foreground">
              已进入当前对话的内容不会因为停用而消失；停用只阻止后续检索。修改系统提示词后需要重启服务并新建主会话。
            </p>
          </div>
        )}
      </div>

      <AddBookDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        onNotify={onNotify}
        onError={onError}
        onSaved={load}
      />
      <BookDetailDialog
        source={detail}
        runtime={data.runtime}
        busy={busy}
        fileBusy={fileBusy}
        onOpenChange={(open) => { if (!open) setDetail(null) }}
        onPublish={(version) => detail && publishVersion(detail, version)}
        onToggle={() => detail && toggleSource(detail)}
        onReadFile={(file) => detail && void readFile(detail, file)}
      />
      <ReaderDialog
        open={!!section}
        title={section?.source_title || '原文'}
        meta={section ? section.section : ''}
        text={section?.text || ''}
        busy={sectionBusy}
        hasNext={!!section?.has_more}
        onOpenChange={(open) => { if (!open) setSection(null) }}
        onNext={() => {
          if (!section) return
          const match = searchResult?.matches.find((item) => item.section_id === section.section_id)
          if (match) void readSection(match, section.next_offset ?? 0)
        }}
      />
      <ReaderDialog
        open={!!fileRead}
        title={fileRead?.source_title || '原文'}
        meta={fileRead ? `${fileRead.path} · 第 ${fileRead.offset + 1}-${Math.min(fileRead.total_lines, fileRead.offset + 120)} 行` : ''}
        text={fileRead?.text || ''}
        busy={fileBusy}
        hasNext={fileRead?.next_offset != null}
        onOpenChange={(open) => { if (!open) setFileRead(null) }}
        onNext={() => {
          if (!fileRead || fileRead.next_offset == null) return
          const source = data.sources.find((item) => item.id === fileRead.source_id)
          const entry = source?.current?.files?.find((item) => item.path === fileRead.path)
          if (source && entry) void readFile(source, entry, fileRead.next_offset)
        }}
      />
    </div>
  )
}

function AddBookDialog({
  open,
  onOpenChange,
  onNotify,
  onError,
  onSaved,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onNotify: Props['onNotify']
  onError: Props['onError']
  onSaved: () => Promise<void>
}) {
  const [busy, setBusy] = useState(false)
  const [files, setFiles] = useState<File[]>([])
  const formRef = useRef<HTMLFormElement>(null)

  useEffect(() => {
    if (!open) {
      setFiles([])
      formRef.current?.reset()
    }
  }, [open])

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (busy || files.length === 0) return
    const form = new FormData(event.currentTarget)
    form.delete('files')
    for (const file of files) form.append('files', file)
    form.set('publish', 'true')
    setBusy(true)
    try {
      const result = await api<{ message: string; published: boolean }>('/admin/api/knowledge/imports', {
        method: 'POST',
        body: form,
      })
      onNotify(result.message, result.published ? 'success' : 'info')
      onOpenChange(false)
      await onSaved()
    } catch (err) {
      onError(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100vh-2rem)] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>添加一本书</DialogTitle>
          <DialogDescription>同一本书可以一次选择多个 Markdown/TXT 章节。导入校验后直接发布。</DialogDescription>
        </DialogHeader>
        <form ref={formRef} onSubmit={(event) => void submit(event)} className="space-y-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="书名 *"><Input name="name" required placeholder="例如：家庭手册" /></Field>
            <Field label="作者"><Input name="author" placeholder="可不填" /></Field>
          </div>
          <Field label="说明">
            <Textarea name="description" rows={2} placeholder="这本书大概讲什么，可不填" />
          </Field>
          <Field label="内容文件 *">
            <label className="flex cursor-pointer items-center gap-2 border border-dashed border-border px-3 py-3 text-ui-sm text-muted-foreground hover:border-border-hover">
              <Upload className="size-4 shrink-0" />
              <span className="min-w-0 flex-1 truncate">
                {files.length === 0 ? '选择一个或多个 .md / .txt 文件' : `已选择 ${files.length} 个文件`}
              </span>
              <span className="shrink-0 text-ui-xs">浏览</span>
              <input
                className="sr-only"
                type="file"
                required
                multiple
                accept=".md,.markdown,.txt,text/markdown,text/plain"
                onChange={(event) => setFiles(Array.from(event.target.files ?? []))}
              />
            </label>
            {files.length > 0 && (
              <div className="max-h-24 overflow-y-auto text-ui-xs text-muted-foreground">
                {files.map((file) => <div key={`${file.name}-${file.size}`} className="truncate py-0.5">{file.name}</div>)}
              </div>
            )}
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>取消</Button>
            <Button type="submit" disabled={busy || files.length === 0}>
              {busy ? <Loader2 className="size-3.5 animate-spin" /> : <Upload className="size-3.5" />}
              导入并发布
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="block space-y-1 text-ui-sm">
      <span className="font-medium">{label}</span>
      {children}
    </div>
  )
}

function BookDetailDialog({
  source,
  runtime,
  busy,
  fileBusy,
  onOpenChange,
  onPublish,
  onToggle,
  onReadFile,
}: {
  source: KnowledgeSource | null
  runtime: KnowledgeRuntime
  busy: string
  fileBusy: boolean
  onOpenChange: (open: boolean) => void
  onPublish: (version: KnowledgeVersion) => void
  onToggle: () => void
  onReadFile: (file: KnowledgeFile) => void
}) {
  const [showAll, setShowAll] = useState(false)
  useEffect(() => setShowAll(false), [source?.id])
  if (!source) return null
  const current = source.current
  const files = current?.files ?? []
  const staged = source.versions.find((version) => version.status === 'staged')
  const active = runtime.active_sources[source.id] === current?.id
  const visible = showAll ? files : files.slice(0, 8)
  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100vh-2rem)] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{source.name}</DialogTitle>
          <DialogDescription>
            {[source.author, `${source.coverage.saved_files} 个文件`, `${source.coverage.sections} 个片段`].filter(Boolean).join(' · ')}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          {(source.description || source.use_cases.length > 0) && (
            <div className="space-y-1 text-ui-sm">
              {source.description && <p>{source.description}</p>}
              {source.use_cases.length > 0 && <p className="text-muted-foreground">适合：{source.use_cases.join('；')}</p>}
            </div>
          )}

          <div>
            <div className="mb-1.5 flex items-center justify-between">
              <div className="text-ui-sm font-medium">内容</div>
              <div className="text-ui-xs text-muted-foreground">{active ? 'agent 正在使用' : current ? '等待 agent 加载' : '尚未发布'}</div>
            </div>
            {files.length === 0 && <div className="border-y border-border py-4 text-ui-sm text-muted-foreground">还没有已发布内容。</div>}
            {files.length > 0 && (
              <div className="divide-y divide-border border-y border-border">
                {visible.map((file) => {
                  const readable = file.format === 'markdown' || file.format === 'text'
                  return (
                    <button
                      key={file.path}
                      type="button"
                      disabled={!readable || fileBusy}
                      onClick={() => onReadFile(file)}
                      className="flex w-full items-center gap-2 py-1.5 text-left text-ui-sm hover:text-primary disabled:cursor-default disabled:text-muted-foreground"
                    >
                      <FileText className="size-3.5 shrink-0" />
                      <span className="min-w-0 flex-1 truncate">{file.path}</span>
                      {file.state !== 'indexed' && <span className="text-ui-xs text-muted-foreground">不参与检索</span>}
                    </button>
                  )
                })}
              </div>
            )}
            {files.length > 8 && (
              <Button size="xs" variant="ghost" className="mt-1" onClick={() => setShowAll((value) => !value)}>
                {showAll ? '收起' : `显示全部 ${files.length} 个文件`}
              </Button>
            )}
          </div>

          <details className="border-t border-border pt-2">
            <summary className="cursor-pointer text-ui-sm text-muted-foreground hover:text-foreground">版本记录</summary>
            <div className="mt-2 divide-y divide-border">
              {source.versions.slice().reverse().map((version) => (
                <div key={version.id} className="flex items-center gap-2 py-1.5 text-ui-sm">
                  <span className="min-w-0 flex-1">{version.label || '版本'} · {version.saved_files} 个文件</span>
                  {version.published ? (
                    <span className="text-ui-xs text-success">当前版本</span>
                  ) : (version.status === 'ready' || version.status === 'staged') ? (
                    <Button
                      size="xs"
                      variant="ghost"
                      disabled={busy !== ''}
                      onClick={() => onPublish(version)}
                    >
                      {version.status === 'staged' ? '发布' : '切回此版本'}
                    </Button>
                  ) : (
                    <span className="text-ui-xs text-muted-foreground">{version.status}</span>
                  )}
                </div>
              ))}
            </div>
          </details>
        </div>
        <DialogFooter>
          <Button size="sm" variant="outline" onClick={onToggle} disabled={busy !== ''}>
            <Power className="size-3.5" /> {source.enabled ? '停用这本书' : '重新启用'}
          </Button>
          {staged && (
            <Button size="sm" onClick={() => onPublish(staged)} disabled={busy !== ''}>
              <Send className="size-3.5" /> 发布
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ReaderDialog({
  open,
  title,
  meta,
  text,
  busy,
  hasNext,
  onOpenChange,
  onNext,
}: {
  open: boolean
  title: string
  meta: string
  text: string
  busy: boolean
  hasNext: boolean
  onOpenChange: (open: boolean) => void
  onNext: () => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100vh-2rem)] sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{meta}</DialogDescription>
        </DialogHeader>
        <pre className="max-h-[60vh] overflow-auto whitespace-pre-wrap break-words border-y border-border bg-muted/20 px-3 py-3 font-mono text-ui-sm leading-6">
          {text}
        </pre>
        <DialogFooter>
          <span className="mr-auto text-ui-xs text-muted-foreground">只读原文</span>
          <Button size="sm" variant="outline" disabled={busy || !hasNext} onClick={onNext}>
            {busy ? <Loader2 className="size-3.5 animate-spin" /> : <ChevronRight className="size-3.5" />}
            下一页
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
