import { useCallback, useEffect, useState } from 'react'
import { Loader2, Music, Play, RefreshCw, Square, Volume2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { api } from '@/lib/api'

interface Props {
  onNotify: (message: string, kind?: 'success' | 'error' | 'info') => void
  onError: (err: unknown) => void
}

interface PlayRow {
  id: string
  name: string
  device: string
  volume_control: string
  volume_max: number
  volume: number
  note: string
}

interface AudioTestState {
  music: {
    playing: boolean
    volume: number
    queue_name?: string
    index?: number
    total?: number
    name?: string
    artists?: string
  }
  tts_volume: number
  local_asr_enabled: boolean
  plays: PlayRow[]
}

interface PlayResult {
  ok: boolean
  text: string
}

const DEFAULT_TEXT = '你好，我是豆包'

export function AudioTestView({ onNotify, onError }: Props) {
  const [state, setState] = useState<AudioTestState | null>(null)
  const [loading, setLoading] = useState(false)
  const [musicBusy, setMusicBusy] = useState<'start' | 'stop' | null>(null)
  const [asrBusy, setAsrBusy] = useState(false)
  const [musicQuery, setMusicQuery] = useState('小幸运')
  const [texts, setTexts] = useState<Record<string, string>>({})
  const [volumes, setVolumes] = useState<Record<string, number>>({})
  const [busy, setBusy] = useState<Record<string, 'tts' | 'volume' | undefined>>({})
  const [results, setResults] = useState<Record<string, PlayResult | undefined>>({})

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await api<AudioTestState>('/admin/api/debug/audio-test')
      setState(data)
      setVolumes((prev) => {
        const next = { ...prev }
        for (const p of data.plays) {
          if (next[p.id] == null && p.volume >= 0) next[p.id] = p.volume
        }
        return next
      })
      setTexts((prev) => {
        const next = { ...prev }
        for (const p of data.plays) {
          if (next[p.id] == null) next[p.id] = DEFAULT_TEXT
        }
        return next
      })
    } catch (err) {
      onError(err)
    } finally {
      setLoading(false)
    }
  }, [onError])

  useEffect(() => {
    void load()
  }, [load])

  const music = state?.music

  const controlMusic = async (op: 'start' | 'stop') => {
    setMusicBusy(op)
    try {
      await api('/admin/api/debug/audio-test', {
        method: 'POST',
        body: JSON.stringify(
          op === 'start'
            ? { action: 'music', op: 'start', query: musicQuery.trim() || '小幸运' }
            : { action: 'music', op: 'stop' },
        ),
      })
      onNotify(op === 'start' ? '已开始播放测试音乐' : '已停止音乐', 'success')
      await load()
    } catch (err) {
      onError(err)
    } finally {
      setMusicBusy(null)
    }
  }

  const toggleASR = async () => {
    if (!state) return
    setAsrBusy(true)
    try {
      const enabled = !state.local_asr_enabled
      await api('/admin/api/debug/audio-test', {
        method: 'POST',
        body: JSON.stringify({ action: 'asr', enabled }),
      })
      onNotify(enabled ? '本地 ASR 已开启' : '本地 ASR 已关闭（忽略 mic/kws）', 'success')
      await load()
    } catch (err) {
      onError(err)
    } finally {
      setAsrBusy(false)
    }
  }

  const applyVolume = async (play: PlayRow) => {
    const volume = Math.max(0, Math.min(play.volume_max, Number(volumes[play.id] ?? play.volume) || 0))
    setBusy((prev) => ({ ...prev, [play.id]: 'volume' }))
    try {
      await api('/admin/api/debug/audio-test', {
        method: 'POST',
        body: JSON.stringify({ action: 'volume', play: play.id, volume }),
      })
      onNotify(`${play.name} 音量已设为 ${volume}`, 'success')
      await load()
    } catch (err) {
      onError(err)
    } finally {
      setBusy((prev) => ({ ...prev, [play.id]: undefined }))
    }
  }

  const playTTS = async (play: PlayRow) => {
    const text = (texts[play.id] ?? '').trim()
    if (!text) {
      onNotify('请输入要试播的文字', 'info')
      return
    }
    const volume = Number(volumes[play.id] ?? play.volume)
    setBusy((prev) => ({ ...prev, [play.id]: 'tts' }))
    setResults((prev) => ({ ...prev, [play.id]: undefined }))
    try {
      const res = await api<{
        ok: boolean
        error?: string
        command?: string
        elapsed_ms?: number
        cached?: boolean
      }>('/admin/api/debug/audio-test', {
        method: 'POST',
        body: JSON.stringify({ action: 'tts', play: play.id, text, volume }),
      })
      setResults((prev) => ({
        ...prev,
        [play.id]: {
          ok: !!res.ok,
          text: res.ok
            ? `${res.cached ? '缓存命中' : '新合成'} · 请求 ${res.elapsed_ms ?? 0}ms`
            : `失败：${res.error || '未知错误'}`,
        },
      }))
      await load()
    } catch (err) {
      onError(err)
      setResults((prev) => ({ ...prev, [play.id]: { ok: false, text: '请求失败' } }))
    } finally {
      setBusy((prev) => ({ ...prev, [play.id]: undefined }))
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-col gap-4 overflow-y-auto pr-1">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="text-ui-lg font-semibold">音频通道测试</div>
          <div className="text-ui-sm text-muted-foreground">
            开着音乐逐个试播，听哪个通道能和音乐共存；音量是设备原始值
          </div>
        </div>
        <Button variant="outline" size="sm" type="button" onClick={() => void load()} disabled={loading}>
          {loading ? <Loader2 className="size-3.5 animate-spin" /> : <RefreshCw className="size-3.5" />}
          刷新
        </Button>
      </div>

      <div className="grid gap-3 lg:grid-cols-2">
      <Card size="sm" className="h-full">
        <CardContent className="flex h-full flex-wrap items-center justify-between gap-3 py-3">
          <div className="min-w-0">
            <div className="text-ui-sm font-medium">本地 ASR</div>
            <div className="text-ui-xs text-muted-foreground">
              关闭后服务端忽略 mic 流和 kws，不再触发本地唤醒/对话/路由；原生小爱 ASR 不受影响
            </div>
          </div>
          <div className="flex items-center gap-2">
            <span className={state?.local_asr_enabled ? 'text-ui-xs text-success' : 'text-ui-xs text-warning'}>
              {state?.local_asr_enabled ? '已开启' : '已关闭'}
            </span>
            <Button
              size="sm"
              variant={state?.local_asr_enabled ? 'outline' : 'default'}
              type="button"
              onClick={() => void toggleASR()}
              disabled={asrBusy || !state}
            >
              {asrBusy && <Loader2 className="size-3.5 animate-spin" />}
              {state?.local_asr_enabled ? '关闭本地 ASR' : '开启本地 ASR'}
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card size="sm">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-ui-sm">
            <Music className="size-3.5 text-muted-foreground" />
            背景音乐
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2 text-ui-sm">
            <span className={music?.playing ? 'text-success' : 'text-muted-foreground'}>
              {music?.playing ? '播放中' : '未播放'}
            </span>
            {music?.name && (
              <span className="min-w-0 truncate text-muted-foreground">
                《{music.name}》{music.artists ? ` - ${music.artists}` : ''}
              </span>
            )}
            <span className="text-muted-foreground">音乐音量 {music?.volume ?? '-'}</span>
            {music?.queue_name && (
              <span className="text-muted-foreground">
                队列：{music.queue_name} {music.index != null ? `${(music.index ?? 0) + 1}/${music.total}` : ''}
              </span>
            )}
            <span className="text-muted-foreground">豆包 TTS 配置音量 {state?.tts_volume ?? '-'}%</span>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <input
              value={musicQuery}
              onChange={(e) => setMusicQuery(e.target.value)}
              placeholder="测试音乐关键词"
              className="h-8 min-w-[180px] flex-1 rounded-md border border-input-border bg-input px-2 text-ui-sm text-foreground outline-none"
            />
            <Button
              size="sm"
              type="button"
              onClick={() => void controlMusic('start')}
              disabled={musicBusy != null}
            >
              {musicBusy === 'start' ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
              开音乐
            </Button>
            <Button
              size="sm"
              variant="outline"
              type="button"
              onClick={() => void controlMusic('stop')}
              disabled={musicBusy != null}
            >
              {musicBusy === 'stop' ? <Loader2 className="size-3.5 animate-spin" /> : <Square className="size-3.5" />}
              关音乐
            </Button>
          </div>
        </CardContent>
      </Card>
      </div>

      <div className="grid auto-rows-min content-start gap-3 lg:grid-cols-2">
        {(state?.plays ?? []).map((play) => {
          const rowBusy = busy[play.id]
          const result = results[play.id]
          return (
            <Card key={play.id} size="sm" className="h-fit self-start">
              <CardHeader>
                <CardTitle className="flex flex-wrap items-center gap-2 text-ui-sm">
                  <Volume2 className="size-3.5 text-muted-foreground" />
                  {play.name}
                  <span className="rounded bg-muted px-1.5 py-0.5 text-ui-xs text-muted-foreground">
                    {play.volume_control}
                  </span>
                </CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <div className="text-ui-xs text-muted-foreground">
                  {play.device} · {play.note}
                </div>
                <div className="flex items-center gap-2">
                  <label className="shrink-0 text-ui-xs text-muted-foreground">音量</label>
                  <input
                    type="number"
                    min={0}
                    max={play.volume_max}
                    value={volumes[play.id] ?? ''}
                    onChange={(e) =>
                      setVolumes((prev) => ({ ...prev, [play.id]: Number(e.target.value) }))
                    }
                    className="h-8 w-24 rounded-md border border-input-border bg-input px-2 text-ui-sm text-foreground outline-none"
                  />
                  <span className="text-ui-xs text-muted-foreground">/ {play.volume_max}</span>
                  <Button
                    size="sm"
                    variant="outline"
                    type="button"
                    onClick={() => void applyVolume(play)}
                    disabled={rowBusy != null}
                  >
                    {rowBusy === 'volume' && <Loader2 className="size-3.5 animate-spin" />}
                    应用音量
                  </Button>
                </div>
                <div className="flex items-center gap-2">
                  <input
                    value={texts[play.id] ?? ''}
                    onChange={(e) => setTexts((prev) => ({ ...prev, [play.id]: e.target.value }))}
                    placeholder="输入要试播的文字"
                    className="h-8 min-w-0 flex-1 rounded-md border border-input-border bg-input px-2 text-ui-sm text-foreground outline-none"
                  />
                  <Button
                    size="sm"
                    type="button"
                    onClick={() => void playTTS(play)}
                    disabled={rowBusy != null}
                  >
                    {rowBusy === 'tts' ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
                    试播
                  </Button>
                </div>
                {result && (
                  <div className={result.ok ? 'text-ui-xs text-success' : 'text-ui-xs text-destructive'}>
                    {result.text}
                  </div>
                )}
              </CardContent>
            </Card>
          )
        })}
      </div>
    </div>
  )
}
