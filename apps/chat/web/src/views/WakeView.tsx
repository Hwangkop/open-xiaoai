import { useCallback, useEffect, useState } from 'react'
import { AudioLines, Mic, Plus, Trash2, FlaskConical, Loader2, CheckCircle2, XCircle, Route } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { api } from '@/lib/api'

interface Props {
  onNotify: (message: string, kind?: 'success' | 'error' | 'info') => void
  onError: (err: unknown) => void
}

interface WakeList {
  custom: string[]
  defaults: string[]
  weak: string[]
}

interface Utterance {
  text: string
  at: number
  matched: boolean
  ack: boolean
  dialog?: boolean
  source?: string
}

interface RouteStats {
  total: number
  noise?: number
  stage?: Record<string, number>
  intent?: Record<string, number>
}

interface RouteEntry {
  at: number
  text: string
  stage: string
  via?: string
  action?: string
  query?: string
  confidence?: number
  result?: string
  latency_ms?: number
}

const ROUTE_STAGE_LABEL: Record<string, string> = {
  whitelist: '白名单',
  lyric: '歌词',
  router: '路由',
  dropped: '丢弃',
}

const ROUTE_RESULT_LABEL: Record<string, string> = {
  executed: '已执行',
  dispatched: '已下发',
  dialog: '对话',
  ignored: '忽略',
  low_confidence: '低置信',
  error: '失败',
  no_tool: '无工具',
  router_disabled: '路由关闭',
  too_short: '太短',
  not_intent: '不像指令',
  no_wake_word: '没唤醒词',
  unknown: '未知',
}

const ROUTE_ACTION_LABEL: Record<string, string> = {
  next: '下一首',
  stop: '停止',
  volume_up: '大声',
  volume_down: '小声',
  mute: '静音',
  unmute: '取消静音',
  play: '换歌',
  dialog: '对话',
  ignore: '忽略',
}

interface WakeTest {
  text: string
  match: boolean
  pinyin: string[]
  rest: string
  ack: boolean
}

interface ASRInfo {
  enabled: boolean
  message?: string
  provider?: string
  dialog_provider?: string
  wake_provider?: string
  available?: string[]
  labels?: Record<string, string>
}

// 唤醒之后的识别可选哪几套引擎

export function WakeView({ onNotify, onError }: Props) {
  const [data, setData] = useState<WakeList>({ custom: [], defaults: [], weak: [] })
  const [loading, setLoading] = useState(true)
  const [word, setWord] = useState('')
  const [busy, setBusy] = useState(false)
  const [testText, setTestText] = useState('')
  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState<WakeTest | null>(null)
  const [recent, setRecent] = useState<Utterance[]>([])
  const [routeItems, setRouteItems] = useState<RouteEntry[]>([])
  const [routeStats, setRouteStats] = useState<RouteStats | null>(null)
  const [asr, setAsr] = useState<ASRInfo | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api<WakeList>('/admin/api/wake')
      setData(res)
    } catch (err) {
      onError(err)
    } finally {
      setLoading(false)
    }
  }, [onError])

  const loadRecent = useCallback(async () => {
    try {
      const res = await api<{ items: Utterance[] }>('/admin/api/wake/recent')
      setRecent(res.items || [])
    } catch {
      /* 忽略：不影响主流程 */
    }
  }, [])

  const loadRoute = useCallback(async () => {
    try {
      const res = await api<{ stats: RouteStats; items: RouteEntry[] }>('/admin/api/route/recent')
      setRouteStats(res.stats || null)
      setRouteItems((res.items || []).slice().reverse())
    } catch {
      /* 忽略：不影响主流程 */
    }
  }, [])

  const loadASR = useCallback(async () => {
    try {
      setAsr(await api<ASRInfo>('/admin/api/asr'))
    } catch {
      /* 忽略：没启用服务端唤醒时这里本来就没有内容 */
    }
  }, [])

  useEffect(() => {
    void load()
    void loadRecent()
    void loadRoute()
    void loadASR()
  }, [load, loadRecent, loadRoute, loadASR])

  // addWord 直接添加一个词（点“加为唤醒词”时不用再确认）
  const addWord = useCallback(
    async (raw: string) => {
      const value = raw.trim().replace(/[。！？~～…]+$/g, '')
      if (!value) {
        onNotify('这个词没有有效内容', 'info')
        return
      }
      setBusy(true)
      try {
        const res = await api<{ added: boolean; custom: string[]; message: string }>('/admin/api/wake', {
          method: 'POST',
          body: JSON.stringify({ word: value }),
        })
        setData((prev) => ({ ...prev, custom: res.custom }))
        onNotify(res.message, res.added ? 'success' : 'info')
      } catch (err) {
        onError(err)
      } finally {
        setBusy(false)
      }
    },
    [onError, onNotify],
  )

  const add = useCallback(async () => {
    const value = word.trim()
    if (!value) {
      onNotify('请输入唤醒词', 'info')
      return
    }
    setBusy(true)
    try {
      const res = await api<{ added: boolean; custom: string[]; message: string }>('/admin/api/wake', {
        method: 'POST',
        body: JSON.stringify({ word: value }),
      })
      setData((prev) => ({ ...prev, custom: res.custom }))
      onNotify(res.message, res.added ? 'success' : 'info')
      setWord('')
    } catch (err) {
      onError(err)
    } finally {
      setBusy(false)
    }
  }, [onError, onNotify, word])

  const remove = useCallback(
    async (w: string) => {
      try {
        const res = await api<{ custom: string[] }>(`/admin/api/wake?word=${encodeURIComponent(w)}`, {
          method: 'DELETE',
        })
        setData((prev) => ({ ...prev, custom: res.custom }))
        onNotify(`已删除「${w}」`, 'success')
      } catch (err) {
        onError(err)
      }
    },
    [onError, onNotify],
  )

  const test = useCallback(async () => {
    const value = testText.trim()
    if (!value) {
      onNotify('请输入要测试的句子', 'info')
      return
    }
    setTesting(true)
    setResult(null)
    try {
      const res = await api<WakeTest>(`/admin/api/wake/test?text=${encodeURIComponent(value)}`)
      setResult(res)
    } catch (err) {
      onError(err)
    } finally {
      setTesting(false)
    }
  }, [onError, onNotify, testText])

  return (
    <div className="grid h-full min-h-0 grid-cols-1 gap-3 overflow-hidden lg:grid-cols-2">
      <div className="flex h-full min-h-0 flex-col gap-3">
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center gap-2 text-sm">
            <FlaskConical className="size-4" /> 唤醒测试
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex gap-2">
            <Input
              value={testText}
              placeholder="输入一句 ASR 可能识别出来的文字，例如「豆康豆吧」"
              onChange={(e) => setTestText(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void test()
              }}
            />
            <Button onClick={() => void test()} disabled={testing}>
              {testing ? <Loader2 className="size-4 animate-spin" /> : '测试'}
            </Button>
          </div>
          {result && (
            <div className="rounded-md border p-3 text-sm">
              <div className="flex items-center gap-2">
                {result.match ? (
                  <CheckCircle2 className="size-4 text-green-500" />
                ) : (
                  <XCircle className="size-4 text-muted-foreground" />
                )}
                <span>{result.match ? '会唤醒' : '不会唤醒'}</span>
                {result.match && (
                  <span className="text-muted-foreground">
                    {result.ack ? '· 只应一声「我在」' : `· 剩余「${result.rest}」当问题`}
                  </span>
                )}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">
                拼音：{result.pinyin.join(' ') || '（无）'}
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <Card className="flex min-h-0 flex-1 flex-col">
        <CardHeader className="shrink-0 pb-2">
          <CardTitle className="flex items-center justify-between text-sm">
            <span>最近识别（{recent.length}）</span>
            <span className="flex gap-1">
              <Button size="sm" variant="ghost" onClick={() => void loadRecent()}>
                刷新
              </Button>
              <Button
                size="sm"
                variant="ghost"
                className="text-muted-foreground"
                onClick={async () => {
                  try {
                    await api('/admin/api/wake/recent', { method: 'DELETE' })
                    setRecent([])
                    onNotify('已清空识别记录', 'success')
                  } catch (err) {
                    onError(err)
                  }
                }}
              >
                清空
              </Button>
            </span>
          </CardTitle>
        </CardHeader>
        <CardContent className="min-h-0 flex-1 space-y-2 overflow-y-auto pr-1">
          {recent.length === 0 ? (
            <div className="text-sm text-muted-foreground">
              还没有记录。对着音箱说几句话，这里会显示 PC ASR 和小爱原生 ASR 的原文。
            </div>
          ) : (
            recent.map((u, i) => {
              const isXiaoai = u.source === 'xiaoai'
              return (
                <div
                  key={`${u.at}-${i}`}
                  onClick={
                    isXiaoai
                      ? undefined
                      : () => {
                          setTestText(u.text)
                          void (async () => {
                            setTesting(true)
                            setResult(null)
                            try {
                              const res = await api<WakeTest>(
                                `/admin/api/wake/test?text=${encodeURIComponent(u.text)}`,
                              )
                              setResult(res)
                            } catch (err) {
                              onError(err)
                            } finally {
                              setTesting(false)
                            }
                          })()
                        }
                  }
                  title={isXiaoai ? '小爱原生 ASR（有 AEC，音乐大声时也能听清）' : '点击测试这句话'}
                  className={`flex items-center gap-2 rounded-md border border-border/40 px-3 py-2 text-sm transition-colors ${
                    isXiaoai
                      ? ''
                      : 'cursor-pointer hover:border-foreground/60 hover:bg-foreground/[0.06]'
                  }`}
                >
                  <span className="w-14 shrink-0 text-xs text-muted-foreground">
                    {new Date(u.at).toLocaleTimeString('zh-CN', { hour12: false })}
                  </span>
                  <span className="min-w-0 flex-1 truncate" title={u.text}>
                    {u.text}
                  </span>
                  {isXiaoai ? (
                    <Badge variant="outline" className="shrink-0 text-xs">
                      小爱 ASR
                    </Badge>
                  ) : u.dialog ? (
                    <Badge variant="outline" className="shrink-0 text-xs">
                      对话
                    </Badge>
                  ) : u.matched ? (
                    <Badge variant="secondary" className="shrink-0 text-xs">
                      {u.ack ? '唤醒' : '命中'}
                    </Badge>
                  ) : (
                    <>
                      <Badge variant="outline" className="shrink-0 text-xs text-muted-foreground">
                        未命中
                      </Badge>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-6 shrink-0 px-2 text-xs"
                        onClick={() => void addWord(u.text)}
                      >
                        加为唤醒词
                      </Button>
                    </>
                  )}
                </div>
              )
            })
          )}
        </CardContent>
      </Card>

      <Card className="flex min-h-0 flex-1 flex-col">
        <CardHeader className="shrink-0 pb-2">
          <CardTitle className="flex items-center justify-between text-sm">
            <span className="flex items-center gap-2">
              <Route className="size-4" /> 音乐态路由
              <span className="text-xs font-normal text-muted-foreground">
                唤醒词 → 路由
              </span>
            </span>
            <span className="flex gap-1">
              <Button size="sm" variant="ghost" onClick={() => void loadRoute()}>
                刷新
              </Button>
              <Button
                size="sm"
                variant="ghost"
                className="text-muted-foreground"
                onClick={async () => {
                  try {
                    await api('/admin/api/route/recent', { method: 'DELETE' })
                    setRouteItems([])
                    setRouteStats(null)
                    onNotify('已清空路由记录', 'success')
                  } catch (err) {
                    onError(err)
                  }
                }}
              >
                清空
              </Button>
            </span>
          </CardTitle>
        </CardHeader>
        <CardContent className="min-h-0 flex-1 space-y-2 overflow-y-auto pr-1">
          {routeStats && (
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge variant="outline">进入 {routeStats.total}</Badge>
              <Badge variant="secondary">白名单 {routeStats.stage?.whitelist || 0}</Badge>
              <Badge>路由 {routeStats.stage?.router || 0}</Badge>
              <Badge variant="outline">噪声 {routeStats.noise || 0}</Badge>
            </div>
          )}
          {routeItems.length === 0 ? (
            <div className="text-sm text-muted-foreground">
              还没有记录。放首歌说几句话，这里会显示每句话走到哪一层、路由器判成了什么。
            </div>
          ) : (
            routeItems.map((e, i) => (
              <div
                key={`${e.at}-${i}`}
                className="flex items-center gap-2 rounded-md border border-border/40 px-3 py-2 text-sm"
              >
                <span className="w-14 shrink-0 text-xs text-muted-foreground">
                  {new Date(e.at).toLocaleTimeString('zh-CN', { hour12: false })}
                </span>
                <span className="min-w-0 flex-1 truncate" title={e.text}>
                  {e.text}
                </span>
                <Badge
                  variant={e.stage === 'router' ? 'default' : e.stage === 'lyric' ? 'secondary' : 'outline'}
                  className="shrink-0 text-xs"
                >
                  {ROUTE_STAGE_LABEL[e.stage] || e.stage}
                </Badge>
                {e.via === 'wake' && (
                  <Badge variant="secondary" className="shrink-0 text-xs">
                    唤醒
                  </Badge>
                )}
                {e.action && (
                  <span className="shrink-0 text-xs">{ROUTE_ACTION_LABEL[e.action] || e.action}</span>
                )}
                {e.query ? (
                  <span className="max-w-24 shrink-0 truncate text-xs text-muted-foreground" title={e.query}>
                    {e.query}
                  </span>
                ) : null}
                {typeof e.confidence === 'number' && e.confidence > 0 ? (
                  <span className="shrink-0 text-xs text-muted-foreground">
                    {Math.round(e.confidence * 100)}%
                  </span>
                ) : null}
                {e.latency_ms ? (
                  <span className="shrink-0 text-xs text-muted-foreground">{e.latency_ms}ms</span>
                ) : null}
                {e.result && (
                  <Badge variant="outline" className="shrink-0 text-xs text-muted-foreground">
                    {ROUTE_RESULT_LABEL[e.result] || e.result}
                  </Badge>
                )}
              </div>
            ))
          )}
        </CardContent>
      </Card>

      </div>

      <div className="flex h-full min-h-0 flex-col gap-3">
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center gap-2 text-sm">
            <AudioLines className="size-4" /> 识别引擎
            <span className="text-xs font-normal text-muted-foreground">
              （在「配置 → 语音识别」里改，保存即热生效）
            </span>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-1 text-sm">
          {asr && !asr.enabled ? (
            <div className="text-muted-foreground">{asr.message || '服务端唤醒未启用'}</div>
          ) : (
            <>
              <div>
                唤醒监听：
                <span className="text-muted-foreground">
                  {asr?.labels?.[asr?.wake_provider || ''] || asr?.wake_provider || '—'}
                </span>
              </div>
              <div>
                唤醒之后的识别：
                <span className="text-muted-foreground">
                  {asr?.labels?.[asr?.dialog_provider || ''] || asr?.dialog_provider || '—'}
                </span>
              </div>
            </>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center gap-2 text-sm">
            <Mic className="size-4" /> 自定义唤醒词
            <span className="text-xs font-normal text-muted-foreground">
              （热生效，不用重启）
            </span>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex gap-2">
            <Input
              value={word}
              placeholder="例如：豆包包包 / 小助手"
              onChange={(e) => setWord(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void add()
              }}
            />
            <Button onClick={() => void add()} disabled={busy}>
              <Plus className="size-4" /> 添加
            </Button>
          </div>

          {loading ? (
            <div className="text-sm text-muted-foreground">加载中…</div>
          ) : data.custom.length === 0 ? (
            <div className="text-sm text-muted-foreground">还没有自定义词</div>
          ) : (
            <div className="flex flex-wrap gap-2">
              {data.custom.map((w) => (
                <Badge key={w} variant="secondary" className="gap-1 py-1">
                  {w}
                  <button
                    className="ml-1 text-muted-foreground hover:text-destructive"
                    onClick={() => void remove(w)}
                    title="删除"
                  >
                    <Trash2 className="size-3" />
                  </button>
                </Badge>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <Card className="flex min-h-0 flex-1 flex-col">
        <CardHeader className="shrink-0 pb-2">
          <CardTitle className="text-sm">内置唤醒词（{data.defaults.length} 个）</CardTitle>
        </CardHeader>
        <CardContent className="min-h-0 flex-1 overflow-y-auto">
          <div className="flex flex-wrap gap-1.5">
            {data.defaults.map((w) => (
              <Badge key={w} variant="outline" className="text-xs">
                {w}
              </Badge>
            ))}
          </div>
          <div className="mt-3 text-xs text-muted-foreground">
            整句才生效的弱唤醒词：
            {data.weak.map((w) => (
              <Badge key={w} variant="outline" className="mx-1 text-xs">
                {w}
              </Badge>
            ))}
          </div>
        </CardContent>
      </Card>
      </div>
    </div>
  )
}
