import { useCallback, useEffect, useState } from 'react'
import {
  BookOpenText,
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
  Terminal,
  Tv,
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
  min?: number
  max?: number
  step?: number
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
		title: '听书', desc: '复用知识库的书籍，用豆包声音按需生成并缓存；修改后重启服务', icon: BookOpenText,
		fields: [
			{ path: ['audiobook', 'enabled'], label: '启用听书', type: 'bool', defaultBool: true },
			{ path: ['audiobook', 'cache_dir'], label: '音频与进度目录', type: 'text', hint: '留空为 config 同级的 audiobooks；Windows 请使用 Windows 路径' },
			{ path: ['audiobook', 'chunk_chars'], label: '单段字符上限', type: 'number', min: 80, max: 400, step: 10, hint: '默认 180，改变后需要重新选章，旧音频保留' },
			{ path: ['audiobook', 'prefetch'], label: '提前生成段数', type: 'number', min: 1, max: 3, step: 1, hint: '默认 2，暂停后不继续发起合成' },
			{ path: ['audiobook', 'synthesis_gap_ms'], label: '合成间隔（毫秒）', type: 'number', min: 500, step: 100, hint: '默认 1000，最多一个听书合成请求在途' },
		],
	},
  {
    title: '服务',
    desc: '监听地址和端口，改这两项需要重启服务',
    icon: Server,
    fields: [
      { path: ['server', 'host'], label: '监听地址', type: 'text' },
      { path: ['server', 'port'], label: '端口', type: 'number', min: 1, max: 65535, step: 1 },
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
          { value: 'codex', label: 'codex（完整工具与知识库）' },
          { value: 'doubao_web', label: '豆包网页版' },
          { value: 'llm', label: 'OpenAI 兼容 LLM' },
        ],
        hint: '切换对话后端需要重启服务；codex 才能使用完整 MCP 工具与知识库',
      },
      { path: ['prompt', 'inject_time'], label: '注入当前时间', type: 'bool', defaultBool: true },
      { path: ['continuation_seconds'], label: '对话窗口（秒）', type: 'number', min: 0, step: 1, hint: '回答结束后继续等待下一句的时长；0 使用默认值' },
      {
        path: ['llm', 'base_url'],
        label: 'LLM 接口地址',
        type: 'text',
        placeholder: 'https://api.deepseek.com',
        hint: 'OpenAI 兼容接口；改完需重启服务',
      },
      {
        path: ['llm', 'api_key'],
        label: 'LLM API Key',
        type: 'password',
        hint: '路由/分类器/降级都用它；改完需重启服务',
      },
      {
        path: ['llm', 'model'],
        label: 'LLM / 降级模型',
        type: 'text',
        placeholder: 'deepseek-flash',
        hint: '仅用于 llm 对话后端、音乐路由和 Codex 降级。当前主对话走 Codex，实际模型由 CODEX_HOME/config.toml 的 model 决定，不在本表单中',
      },
    ],
  },
  {
    title: 'codex',
    desc: '常驻 codex 后端（chat.provider = codex 时生效）；主模型读取 CODEX_HOME/config.toml 的 model；修改本分区后需重启服务',
    icon: Terminal,
    fields: [
      { path: ['codex', 'bin'], label: '可执行文件', type: 'text', placeholder: 'codex' },
      {
        path: ['codex', 'workspace'],
        label: '工作目录',
        type: 'text',
        hint: 'Codex 的专用工作目录，知识库也放在这里；只放需要让它读取的资料和配置',
      },
      {
        path: ['codex', 'codex_home'],
        label: 'CODEX_HOME',
        type: 'text',
        hint: '里面放 config.toml（模型供应商 + MCP）与 models.json；修改后需重启服务',
      },
      {
        path: ['codex', 'timeout_seconds'],
        label: '单轮超时（秒）',
        type: 'number',
        placeholder: '90',
        min: 0,
        step: 1,
        hint: '0 或留空使用默认 90 秒',
      },
      {
        path: ['codex', 'first_token_seconds'],
        label: '首字超时（秒）',
        type: 'number',
        placeholder: '20',
        min: 1,
        step: 1,
        hint: '调工具时不吐字，别设太小；超时会降级到直连模型',
      },
    ],
  },
  {
    title: '语音识别',
    desc: '唤醒和对话识别引擎可热切换；启用开关、API Key、Python/模型路径等基础设施变更需要重启',
    icon: Mic,
    fields: [
      {
        path: ['asr', 'enabled'],
        label: '启用服务端唤醒',
        type: 'bool',
        hint: '开启或关闭需要重启服务才会建立/停止服务端唤醒链路',
      },
      {
        path: ['asr', 'wake_provider'],
        label: '唤醒监听',
        type: 'select',
        options: [
          { value: 'local', label: 'SenseVoice' },
          { value: 'local_alt', label: 'SenseVoice + Paraformer 兜底' },
          { value: 'doubao_stream', label: '火山引擎流式' },
          { value: 'doubao', label: '火山引擎整段' },
        ],
      },
      {
        path: ['asr', 'dialog_provider'],
        label: '唤醒之后的识别',
        type: 'select',
        options: [
          { value: 'local', label: 'SenseVoice' },
          { value: 'local_alt', label: 'SenseVoice + Paraformer 兜底' },
          { value: 'doubao_stream', label: '火山引擎流式' },
          { value: 'doubao', label: '火山引擎整段（非流式）' },
        ],
      },
      {
        path: ['asr', 'api_key'],
        label: '火山引擎 API Key',
        type: 'password',
        hint: '用于豆包/火山识别；修改后需重启服务才会应用到现有识别实例',
      },
    ],
  },
  {
    title: '语音合成',
    desc: '豆包 TTS 的生成参数与播放通道；音箱自带 TTS 会忽略这些高级项',
    icon: Volume2,
    fields: [
      {
        path: ['tts', 'provider'],
        label: 'TTS',
        type: 'select',
        options: [
          { value: 'xiaoai', label: '音箱自带 TTS' },
          { value: 'doubao', label: '豆包 TTS（推荐）' },
        ],
        hint: '选音箱自带时只使用系统 TTS，下面的豆包音色、情绪和播放通道配置不生效',
      },
      {
        path: ['tts', 'speaker'],
        label: '豆包音色 ID',
        type: 'text',
        hint: '不同音色对情绪和音调的支持程度不同',
      },
      {
        path: ['tts', 'format'],
        label: '音频格式',
        type: 'select',
        options: [
          { value: 'wav', label: 'WAV（notify，可与音乐叠播）' },
          { value: 'mp3', label: 'MP3（miplayer）' },
          { value: 'ogg_opus', label: 'OGG Opus（miplayer）' },
          { value: 'pcm', label: 'PCM（miplayer）' },
        ],
        hint: 'WAV 走 aplay -D notify，TTS 音量独立且可压低音乐；其它格式走 miplayer，会暂停音乐',
      },
      {
        path: ['tts', 'emotion'],
        label: '情绪',
        type: 'select',
        options: [
          { value: '', label: '默认（音色自带）' },
          { value: 'happy', label: '开心' },
          { value: 'sad', label: '悲伤' },
          { value: 'angry', label: '生气' },
          { value: 'surprised', label: '惊讶' },
          { value: 'neutral', label: '中性' },
        ],
        hint: '部分豆包音色支持；同一句话不同情绪音频会不同',
      },
      {
        path: ['tts', 'speech_rate'],
        label: '语速倍率',
        type: 'number',
        step: 0.1,
        hint: '1.0 为正常，建议约 0.5–2.0；>1 更快',
      },
      {
        path: ['tts', 'pitch'],
        label: '音调',
        type: 'number',
        step: 0.1,
        hint: '0 为正常，约 -1.0～1.0；过低或过高是否自然取决于音色',
      },
      {
        path: ['tts', 'bit_rate'],
        label: '音频码率',
        type: 'number',
        min: 1,
        step: 1000,
        hint: '豆包 TTS 默认 32000；通常不需要修改',
      },
      {
        path: ['tts', 'sample_rate'],
        label: '采样率',
        type: 'number',
        min: 1,
        step: 1000,
        hint: '豆包 TTS 默认 24000；通常不需要修改',
      },
      {
        path: ['tts', 'volume'],
        label: '豆包 TTS 音量',
        type: 'number',
        min: 0,
        max: 100,
        step: 1,
        hint: '0-100，映射到 notifyvol；仅 WAV + notify 有效。MP3/PCM 走 miplayer 时忽略。手动/MCP 调过的音量会落盘，保存本页后以这里为准',
      },
      {
        path: ['tts', 'player'],
        label: '播放方式',
        type: 'select',
        options: [
          { value: 'auto', label: '自动（推荐，按格式选择）' },
          { value: 'aplay', label: '强制 aplay（仅 WAV）' },
          { value: 'miplayer', label: '强制 miplayer（非 WAV）' },
        ],
        hint: '代码会按实际音频格式纠正：aplay + 非 WAV 会回退 miplayer，miplayer + WAV 会回退 aplay',
      },
      {
        path: ['tts', 'wav_device'],
        label: 'WAV 输出设备',
        type: 'select',
        options: [
          { value: 'notify', label: 'notify（独立 TTS 音量）' },
          { value: 'playback', label: 'Playback（旧实验，会影响音乐音量）' },
        ],
        hint: '仅 WAV + aplay 时使用；日常保持 notify。Playback 使用 mysoftvol，会同时改变音乐音量',
      },
      {
        path: ['tts', 'music_duck', 'enabled'],
        label: '豆包说话时压低音乐',
        type: 'bool',
        defaultBool: false,
        hint: '仅 WAV + aplay -D notify 且正在放音乐时生效；首句开播时压低，整轮结束恢复。miplayer 路径不会触发',
      },
      {
        path: ['tts', 'music_duck', 'percent'],
        label: '播报期间音乐保留比例',
        type: 'number',
        min: 1,
        max: 100,
        step: 1,
        hint: '1-100，默认 50；例如音乐音量 20 → 10 → 整轮结束恢复 20。与 notifyvol 独立',
      },
    ],
  },
  {
    title: '音乐',
    desc: '音乐自动降音量、原生唤醒接管和音乐态指令路由',
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
        min: 1,
        max: 100,
        step: 1,
        placeholder: '35',
        hint: '1-100，建议 30-40；填 0 或留空会回落到默认 35，不会静音',
      },
      {
        path: ['native_wake', 'takeover_enabled'],
        label: '「小爱同学」唤醒后交给豆包',
        type: 'bool',
        defaultBool: false,
        hint: '主要用于音乐态；需要音箱固件支持，失败时回退原生助手',
      },
      {
        path: ['native_wake', 'isolation_mode'],
        label: '原生回答隔离方式',
        type: 'select',
        options: [
          { value: 'freeze', label: '冻结原生助手（兼容）' },
          { value: 'instruction', label: '按对话拦截指令（实验）' },
        ],
        hint: '实验方式需音箱加载指令钩子；未加载会明确回退兼容路径',
      },
      {
        path: ['native_wake', 'max_seconds'],
        label: '接管最长时长（秒）',
        type: 'number',
        min: 1,
        step: 1,
        placeholder: '25',
        hint: '超时强制解冻，避免音乐永久停住',
      },
      {
        path: ['music_router', 'engine'],
        label: '音乐态路由',
        type: 'select',
        options: [
          { value: 'llm', label: '工具路由 / 分类器优先（快）' },
          { value: 'codex', label: '全部交给 codex 线程（慢但工具全）' },
        ],
        hint: '只在放音乐且带唤醒词时生效',
      },
      {
        path: ['music_router', 'min_confidence'],
        label: '路由置信度门槛',
        type: 'number',
        min: 0,
        max: 1,
        step: 0.05,
        placeholder: '0.6',
        hint: '低于这个值不执行动作；填 0 使用默认 0.6',
      },
    ],
  },
  {
    title: 'B站音乐',
    short: 'B站',
    desc: '搜索 B 站公开视频、播放音轨，并按需保存最高音质；修改后需重启服务',
    icon: Tv,
    fields: [
      {
        path: ['bilibili', 'enabled'],
        label: '启用 B 站音乐',
        type: 'bool',
        defaultBool: true,
        hint: '启用后管理台和 agent 可以搜索、播放和下载公开视频音轨',
      },
      {
        path: ['bilibili', 'cookie'],
        label: 'B 站 Cookie',
        type: 'password',
        hint: '可选；公开内容通常不需要。登录 Cookie 只保存在本机配置里',
      },
      {
        path: ['bilibili', 'cache_dir'],
        label: '缓存目录',
        type: 'text',
        placeholder: 'E:\\open-xiaoai\\bilibili',
        hint: '播放用临时 MP3 保存在这里，可定期清理',
      },
      {
        path: ['bilibili', 'music_dir'],
        label: '永久音乐目录',
        type: 'text',
        placeholder: 'E:\\open-xiaoai\\music',
        hint: '用户点击“保存到音乐库”后，无损源保存为 FLAC，其余保存为 320kbps MP3',
      },
      {
        path: ['bilibili', 'ffmpeg'],
        label: 'ffmpeg 路径',
        type: 'text',
        placeholder: 'ffmpeg',
        hint: '需要支持 libmp3lame；PATH 中有 ffmpeg 时可直接填 ffmpeg',
      },
      {
        path: ['bilibili', 'max_size_mb'],
        label: '单次音频上限（MB）',
        type: 'number',
        min: 1,
        step: 1,
        placeholder: '800',
      },
      {
        path: ['bilibili', 'search_limit'],
        label: '搜索结果数',
        type: 'number',
        min: 1,
        max: 20,
        step: 1,
        placeholder: '12',
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
      {
        path: ['search', 'api_key'],
        label: 'DeepSeek Search Key',
        type: 'password',
        hint: '留空时从 CODEX_HOME/config.toml 的 experimental_bearer_token 读取',
      },
      {
        path: ['search', 'count'],
        label: '结果条数',
        type: 'number',
        min: 1,
        step: 1,
        hint: '默认 6；传给 DeepSeek Search',
      },
    ],
  },
  {
    title: '知识库',
    desc: '本地参考资料的导入、发布与只读检索 worker；修改本分区后需重启服务',
    icon: BookOpenText,
    fields: [
      {
        path: ['knowledge', 'enabled'],
        label: '启用知识库管理',
        type: 'bool',
        defaultBool: true,
        hint: '关闭后管理 API 与知识库页不可用，需要重启服务',
      },
      {
        path: ['knowledge', 'root'],
        label: '资料根目录',
        type: 'text',
        placeholder: '/absolute/path/to/knowledge',
        hint: '部署机建议写绝对路径；目录内保存 registry、原始版本与运行时状态。修改后需重启',
      },
      {
        path: ['knowledge', 'node'],
        label: 'Node 可执行文件',
        type: 'text',
        placeholder: 'node',
        hint: '使用现有 Node 环境；路径变化后需重启服务',
      },
      {
        path: ['knowledge', 'script'],
        label: '只读 worker',
        type: 'text',
        placeholder: '/absolute/path/to/knowledge/server.cjs',
        hint: '内容更新后替换 worker 需要重启 chat',
      },
      {
        path: ['knowledge', 'timeout_seconds'],
        label: '试搜超时（秒）',
        type: 'number',
        placeholder: '8',
        min: 1,
        step: 1,
      },
      {
        path: ['knowledge', 'max_upload_mb'],
        label: '单文件上传上限（MB）',
        type: 'number',
        placeholder: '20',
        min: 1,
        step: 1,
      },
    ],
  },
  {
    title: '提示词',
    desc: 'Codex thread 的 baseInstructions；改完需重启服务并新建主会话',
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

// selectOptions 对 asr 的两个引擎字段优先用后端 labels（和「唤醒与识别」页同一份文案）。
function selectOptions(
  field: FieldDef,
  asrLabels: Record<string, string> | null,
): { value: string; label: string }[] {
  if (field.path[0] === 'asr' && asrLabels) {
    const order = ['local', 'local_alt', 'doubao_stream', 'doubao']
    const opts = order.filter((k) => asrLabels[k]).map((k) => ({ value: k, label: asrLabels[k] }))
    if (opts.length > 0) return opts
  }
  return field.options ?? []
}

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
  const [asrLabels, setAsrLabels] = useState<Record<string, string> | null>(null)
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

  // 识别引擎的显示名以后端的 labels 为准，避免前端另写一份文案导致两边不一致
  useEffect(() => {
    let alive = true
    api<{ labels?: Record<string, string> }>('/admin/api/asr')
      .then((r) => {
        if (alive && r?.labels) setAsrLabels(r.labels)
      })
      .catch(() => {
        /* 未启用服务端唤醒时没有这个接口，忽略 */
      })
    return () => {
      alive = false
    }
  }, [])

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
    const options = field.type === 'select' ? selectOptions(field, asrLabels) : []

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
              {!options.some((o) => o.value === '') && <option value="">（未设置）</option>}
              {options.map((o) => (
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
            min={field.type === 'number' ? field.min : undefined}
            max={field.type === 'number' ? field.max : undefined}
            step={field.type === 'number' ? field.step : undefined}
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
                认证、CORS、代理、完整音乐目录与 ASR 模型路径仍请用 YAML。
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
