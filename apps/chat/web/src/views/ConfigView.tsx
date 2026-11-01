import { useCallback, useEffect, useState } from 'react'
import {
  ChevronDown,
  FileCog,
  FileText,
  House,
  Loader2,
  MessagesSquare,
  Mic,
  Music,
  RotateCcw,
  Save,
  ScrollText,
  Search,
  Server,
  SlidersHorizontal,
  Volume2,
} from 'lucide-react'
import { parseDocument } from 'yaml'

import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { api, type AppConfigResponse } from '@/lib/api'
import { cn } from '@/lib/utils'

interface Props {
  onNotify: (message: string, kind?: 'success' | 'error' | 'info') => void
  onError: (err: unknown) => void
}

type FieldType = 'text' | 'password' | 'number' | 'bool' | 'select' | 'textarea'

interface FieldDef {
  path: string[]
  label: string
  type: FieldType
  options?: { value: string; label: string }[]
  hint?: string
  placeholder?: string
  rows?: number
  // defaultBool 该 bool 在配置里缺省时的运行时默认值（默认 false）。
  // 影响表单回显，也影响"取消勾选"要不要显式写 false。
  defaultBool?: boolean
}

interface SectionDef {
  title: string
  short?: string
  desc: string
  icon: React.ComponentType<{ className?: string }>
  full?: boolean
  fields: FieldDef[]
}

const SECTIONS: SectionDef[] = [
  {
    title: '服务',
    desc: '监听地址和端口，改这两项需要重启服务',
    icon: Server,
    fields: [
      { path: ['server', 'host'], label: '监听地址', type: 'text' },
      { path: ['server', 'port'], label: '端口', type: 'number' },
    ],
  },
  {
    title: '对话',
    desc: '回答走哪个后端、对话窗口多长',
    icon: MessagesSquare,
    fields: [
      {
        path: ['chat', 'provider'],
        label: '对话后端',
        type: 'select',
        options: [
          { value: 'codex', label: 'codex（当前）' },
          { value: 'doubao_web', label: '豆包网页版' },
          { value: 'llm', label: 'OpenAI 兼容 LLM' },
        ],
      },
      { path: ['prompt', 'inject_time'], label: '注入当前时间', type: 'bool' },
      { path: ['continuation_seconds'], label: '对话窗口（秒）', type: 'number' },
    ],
  },
  {
    title: '语音识别',
    desc: '唤醒监听和唤醒之后的识别分开配置',
    icon: Mic,
    fields: [
      { path: ['asr', 'enabled'], label: '启用服务端唤醒', type: 'bool' },
      {
        path: ['asr', 'wake_provider'],
        label: '唤醒监听',
        type: 'select',
        options: [
          { value: 'local', label: 'SenseVoice + Paraformer 兜底' },
          { value: 'local_alt', label: 'SenseVoice' },
          { value: 'doubao_stream', label: '火山引擎流式' },
          { value: 'doubao', label: '火山引擎整段' },
        ],
      },
      {
        path: ['asr', 'dialog_provider'],
        label: '唤醒之后的识别',
        type: 'select',
        options: [
          { value: 'local_alt', label: 'SenseVoice + Paraformer 兜底' },
          { value: 'local', label: 'SenseVoice' },
          { value: 'doubao_stream', label: '火山引擎流式' },
          { value: 'doubao', label: '火山引擎非流式' },
        ],
      },
      { path: ['asr', 'api_key'], label: '火山引擎 API Key', type: 'password' },
    ],
  },
  {
    title: '语音合成',
    desc: '回复用哪种声音念出来',
    icon: Volume2,
    fields: [
      {
        path: ['tts', 'provider'],
        label: 'TTS',
        type: 'select',
        options: [
          { value: 'xiaoai', label: '音箱自带' },
          { value: 'doubao', label: '豆包' },
        ],
      },
      { path: ['tts', 'speaker'], label: '豆包音色 ID', type: 'text' },
      { path: ['tts', 'speech_rate'], label: '语速倍率', type: 'number' },
    ],
  },
  {
    title: '音乐',
    desc: '放歌时把音量压小，麦克风才听得清「下一首」「停止」',
    icon: Music,
    fields: [
      {
        path: ['music_duck', 'enabled'],
        label: '放音乐时自动降音量',
        type: 'bool',
        defaultBool: true,
        hint: '停播或放完还原成原来的音量',
      },
      {
        path: ['music_duck', 'volume'],
        label: '压到的音量',
        type: 'number',
        placeholder: '35',
        hint: '0-100，建议 30-40；留空用默认 35',
      },
    ],
  },
  {
    title: '家电（Home Assistant）',
    short: '家电',
    desc: '配好这两项，list_devices / control_device 就走 HA',
    icon: House,
    fields: [
      {
        path: ['homeassistant', 'base_url'],
        label: 'HA 地址',
        type: 'text',
        placeholder: 'http://192.168.1.230:8123',
        hint: 'Windows 解析不了 .local，这里用 IP',
      },
      {
        path: ['homeassistant', 'token'],
        label: '长期访问令牌',
        type: 'password',
        hint: 'HA → 个人资料 → 安全 → 长期访问令牌',
      },
    ],
  },
  {
    title: '联网搜索',
    desc: '实时信息的搜索接口',
    icon: Search,
    fields: [
      { path: ['search', 'api_key'], label: 'DeepSeek Search Key', type: 'password' },
      { path: ['search', 'count'], label: '结果条数', type: 'number' },
    ],
  },
  {
    title: '提示词',
    desc: '每轮对话的 baseInstructions，改完下一条语音就生效',
    icon: ScrollText,
    full: true,
    fields: [
      {
        path: ['prompt', 'system'],
        label: '系统提示词',
        type: 'textarea',
        rows: 16,
      },
    ],
  },
]

const ALL_FIELDS = SECTIONS.flatMap((s) => s.fields)

type FormValues = Record<string, string | boolean>

function keyOf(path: string[]) {
  return path.join('.')
}

function formFromContent(content: string): FormValues {
  const values: FormValues = {}
  let doc: ReturnType<typeof parseDocument>
  try {
    doc = parseDocument(content)
  } catch {
    return values
  }
  for (const f of ALL_FIELDS) {
    const v = doc.getIn(f.path)
    const key = keyOf(f.path)
    if (f.type === 'password') {
      values[key] = ''
      values['__has__' + key] = v !== undefined && v !== null && String(v) !== ''
      continue
    }
    if (f.type === 'bool') {
      values[key] = v === undefined || v === null ? f.defaultBool === true : v === true
    } else if (v === undefined || v === null) {
      values[key] = ''
    } else {
      values[key] = String(v)
    }
  }
  return values
}

// applyForm 把表单值写回 YAML 文本（保留原有注释与结构）
function applyForm(content: string, values: FormValues): string {
  const doc = parseDocument(content)
  for (const f of ALL_FIELDS) {
    const key = keyOf(f.path)
    if (!(key in values)) continue
    const raw = values[key]
    // 密码类字段留空 = 不修改
    if (f.type === 'password' && String(raw).trim() === '') continue
    if (f.type !== 'bool' && String(raw).trim() === '' && !doc.hasIn(f.path)) continue
    if (f.type === 'bool' && raw === false && !doc.hasIn(f.path) && f.defaultBool !== true) continue
    let value: unknown = raw
    if (f.type === 'bool') {
      value = raw === true
    } else if (f.type === 'number') {
      const s = String(raw).trim()
      value = s === '' ? null : Number(s)
    } else {
      value = String(raw)
      // 多行文本保留结尾换行，YAML 才能输出成 | 块
      if (f.type === 'textarea' && !value.endsWith('\n')) value = value + '\n'
    }
    doc.setIn(f.path, value)
  }
  return doc.toString({ lineWidth: 0 })
}

export function ConfigView({ onNotify, onError }: Props) {
  const [path, setPath] = useState('')
  const [content, setContent] = useState('')
  const [original, setOriginal] = useState('')
  const [form, setForm] = useState<FormValues>({})
  const [tab, setTab] = useState<'form' | 'source'>('form')
  const [sectionIdx, setSectionIdx] = useState(0)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  const dirty = content !== original

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await api<AppConfigResponse>('/admin/api/config')
      const text = data.content ?? ''
      setPath(data.path)
      setContent(text)
      setOriginal(text)
      setForm(formFromContent(text))
    } catch (err) {
      onError(err)
    } finally {
      setLoading(false)
    }
  }, [onError])

  useEffect(() => {
    void load()
  }, [load])

  const save = useCallback(async () => {
    setSaving(true)
    try {
      const body = tab === 'form' ? applyForm(content, form) : content
      const data = await api<AppConfigResponse>('/admin/api/config', {
        method: 'PUT',
        body: JSON.stringify({ content: body }),
      })
      setContent(body)
      setOriginal(body)
      setForm(formFromContent(body))
      onNotify(data.message || '配置已保存', 'success')
    } catch (err) {
      onError(err)
    } finally {
      setSaving(false)
    }
  }, [content, form, onError, onNotify, tab])

  const switchTab = useCallback(
    (next: 'form' | 'source') => {
      if (next === tab) return
      if (next === 'form') {
        // 源码可能被改过，切回表单以源码为准重新解析
        setForm(formFromContent(content))
      }
      setTab(next)
    },
    [content, tab],
  )

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') {
        event.preventDefault()
        if (dirty && !saving) void save()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [dirty, save, saving])

  const lines = content ? content.split('\n').length : 0

  // 表单改动立刻落回 YAML，保证两个视图始终一致；dirty 只看 content 与 original
  const setValue = (key: string, value: string | boolean) => {
    const next = { ...form, [key]: value }
    setForm(next)
    setContent(applyForm(content, next))
  }

  function renderField(field: FieldDef) {
    const key = keyOf(field.path)

    if (field.type === 'bool') {
      return (
        <label
          key={key}
          className="flex cursor-pointer items-center justify-between gap-3 rounded-md border border-border/50 bg-background/40 px-3 py-2"
        >
          <span className="flex min-w-0 flex-col">
            <span className="text-ui-base text-foreground">{field.label}</span>
            <span className="truncate font-mono text-ui-caption text-muted-foreground/60">
              {key}
            </span>
          </span>
          <input
            type="checkbox"
            checked={form[key] === true}
            onChange={(e) => setValue(key, e.target.checked)}
            className="size-4 shrink-0 accent-[var(--color-primary,#3b82f6)]"
          />
        </label>
      )
    }

    return (
      <div key={key} className="space-y-1.5">
        <div className="flex items-baseline justify-between gap-2">
          <label className="text-ui-base font-medium text-foreground/90">{field.label}</label>
          <span className="truncate font-mono text-ui-caption text-muted-foreground/60">{key}</span>
        </div>
        {field.type === 'select' ? (
          <div className="relative">
            <select
              value={String(form[key] ?? '')}
              onChange={(e) => setValue(key, e.target.value)}
              className="h-10 w-full appearance-none rounded-md border border-input-border bg-input pr-9 pl-3 text-ui-base text-foreground outline-none transition-colors hover:border-input-border-hover focus-visible:border-input-border-focused"
            >
              <option value="">（未设置）</option>
              {field.options?.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
            <ChevronDown className="pointer-events-none absolute top-1/2 right-3 size-4 -translate-y-1/2 text-muted-foreground" />
          </div>
        ) : field.type === 'textarea' ? (
          <textarea
            value={String(form[key] ?? '')}
            rows={field.rows ?? 8}
            placeholder={field.placeholder}
            onChange={(e) => setValue(key, e.target.value)}
            className={cn(
              'w-full resize-y rounded-md border border-input-border bg-input px-3 py-2',
              field.type === 'textarea'
                ? 'font-mono text-ui-caption leading-6'
                : 'text-ui-base',
              'outline-none transition-colors hover:border-input-border-hover focus-visible:border-input-border-focused',
            )}
          />
        ) : (
          <Input
            type={
              field.type === 'password' ? 'password' : field.type === 'number' ? 'number' : 'text'
            }
            className="h-10 text-ui-base"
            value={String(form[key] ?? '')}
            autoComplete={field.type === 'password' ? 'new-password' : undefined}
            placeholder={
              field.type === 'password'
                ? form['__has__' + key] === true
                  ? '已设置，留空保持不变'
                  : '未设置'
                : field.placeholder
            }
            onChange={(e) => setValue(key, e.target.value)}
          />
        )}
        {field.hint && <p className="text-[11px] text-muted-foreground/80">{field.hint}</p>}
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <Card className="flex min-h-0 flex-1 flex-col gap-0 overflow-hidden py-0">
        <CardHeader className="shrink-0 border-b border-border/70 px-4 py-3">
          <CardTitle className="flex items-center gap-2 text-ui-lg">
            <FileCog className="size-3.5 text-muted-foreground" />
            配置
            {dirty && (
              <span className="text-ui-xs font-normal text-warning">有未保存的修改</span>
            )}
          </CardTitle>
          <CardAction className="flex items-center gap-2">
            <div className="flex rounded-md border border-border/70 p-0.5">
              <button
                type="button"
                onClick={() => switchTab('form')}
                className={cn(
                  'flex items-center gap-1 rounded px-2 py-1 text-ui-xs transition-colors',
                  tab === 'form'
                    ? 'bg-foreground/10 text-foreground'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                <SlidersHorizontal className="size-3.5" />
                表单
              </button>
              <button
                type="button"
                onClick={() => switchTab('source')}
                className={cn(
                  'flex items-center gap-1 rounded px-2 py-1 text-ui-xs transition-colors',
                  tab === 'source'
                    ? 'bg-foreground/10 text-foreground'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                <FileText className="size-3.5" />
                YAML
              </button>
            </div>
            <Button variant="outline" size="sm" type="button" onClick={() => void load()}>
              <RotateCcw />
              重新加载
            </Button>
            <Button size="sm" type="button" onClick={() => void save()} disabled={!dirty || saving}>
              {saving ? <Loader2 className="animate-spin" /> : <Save />}
              保存
            </Button>
          </CardAction>
        </CardHeader>

        <div className="flex shrink-0 items-center justify-between gap-4 border-b border-border/70 bg-muted/30 px-4 py-1.5 text-ui-xs text-muted-foreground">
          <span className="truncate font-mono">{path || '加载中...'}</span>
          <span className="shrink-0">
            {lines} 行 · {content.length} 字符
          </span>
        </div>

        {loading ? (
          <div className="flex min-h-0 flex-1 items-center justify-center text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
          </div>
        ) : tab === 'form' ? (
          <div className="flex min-h-0 flex-1 flex-col md:flex-row">
            <nav className="flex shrink-0 gap-1 overflow-x-auto border-b border-border/70 bg-muted/20 px-2 py-2 md:w-52 md:flex-col md:overflow-y-auto md:border-b-0 md:border-r">
              {SECTIONS.map((section, i) => {
                const Icon = section.icon
                const active = i === sectionIdx
                return (
                  <button
                    key={section.title}
                    type="button"
                    onClick={() => setSectionIdx(i)}
                    className={cn(
                      'flex shrink-0 items-center gap-2 rounded-md px-3 py-2.5 text-left text-ui-base transition-colors',
                      active
                        ? 'bg-foreground/10 font-medium text-foreground'
                        : 'text-muted-foreground hover:bg-foreground/5 hover:text-foreground',
                    )}
                  >
                    <Icon className="size-4 shrink-0" />
                    <span className="truncate">{section.short ?? section.title}</span>
                  </button>
                )
              })}
              <p className="mt-auto hidden px-3 pt-4 text-ui-caption leading-5 text-muted-foreground/70 md:block">
                低频配置（codex、音乐、认证）请用 YAML。
              </p>
            </nav>

            <div className="min-h-0 flex-1 overflow-y-auto px-5 py-5">
              <div className="mx-auto max-w-3xl">
                {(() => {
                  const section = SECTIONS[Math.min(sectionIdx, SECTIONS.length - 1)]
                  const Icon = section.icon
                  return (
                    <>
                      <div className="mb-1 flex items-center gap-2">
                        <Icon className="size-4 text-muted-foreground" />
                        <h3 className="text-ui-lg font-medium text-foreground">
                          {section.title}
                        </h3>
                      </div>
                      <p className="mb-6 text-ui-caption text-muted-foreground">{section.desc}</p>
                      <div className="space-y-4">{section.fields.map(renderField)}</div>
                    </>
                  )
                })()}
              </div>
            </div>
          </div>
        ) : (
          <textarea
            value={content}
            onChange={(e) => setContent(e.target.value)}
            spellCheck={false}
            className={cn(
              'min-h-0 flex-1 resize-none border-0 bg-transparent px-4 py-3',
              'font-mono text-[13px] leading-6 text-foreground outline-none selection:bg-primary-soft',
            )}
          />
        )}
      </Card>
    </div>
  )
}
