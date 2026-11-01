export interface Conversation {
  id: string
  title: string
  preview: string
  last_time: number
  message_count: number
}

export interface ChatMessage {
  role: 'user' | 'assistant'
  time: number
  text: string
}

export interface DoubaoHealth {
  enabled: boolean
  reachable?: boolean
  logged_in?: boolean
  base_url?: string
  error?: string
  message?: string
  status?: string
}

export interface QrStart {
  status: string
  qr_image_base64?: string
  error?: string
  message?: string
}

export interface QrPoll {
  status: string
  cookies_count?: number
  error?: string
  message?: string
}

export interface AppConfigResponse {
  path: string
  content?: string
  message?: string
}

const STORAGE = {
  base: 'openxiaoai.apiBase',
  user: 'openxiaoai.apiUser',
  pass: 'openxiaoai.apiPass',
}

// 页面由 chat 服务自己托管时（/ui/），同源就是后端；独立部署时回落到本机。
export const DEFAULT_BASE = (() => {
  if (typeof window !== 'undefined' && /^https?:$/.test(window.location.protocol)) {
    return window.location.origin
  }
  return 'http://127.0.0.1:4399'
})()

export function getBase(): string {
  const saved = localStorage.getItem(STORAGE.base)
  if (saved) {
    // 页面不在本机、却存着 localhost 的地址：多半是旧配置，忽略它
    const pageIsLocal = /^(localhost|127\.0\.0\.1)$/.test(window.location.hostname)
    if (!pageIsLocal && /^https?:\/\/(127\.0\.0\.1|localhost)/.test(saved)) {
      return DEFAULT_BASE.replace(/\/+$/, '')
    }
    return saved.replace(/\/+$/, '')
  }
  return DEFAULT_BASE.replace(/\/+$/, '')
}

export function getCredentials(): { user: string; pass: string } {
  return {
    user: localStorage.getItem(STORAGE.user) || '',
    pass: localStorage.getItem(STORAGE.pass) || '',
  }
}

export function saveSettings(base: string, user: string, pass: string) {
  localStorage.setItem(STORAGE.base, base.trim().replace(/\/+$/, ''))
  localStorage.setItem(STORAGE.user, user.trim())
  localStorage.setItem(STORAGE.pass, pass)
}

function base64Utf8(text: string): string {
  const bytes = new TextEncoder().encode(text)
  let binary = ''
  bytes.forEach((b) => {
    binary += String.fromCharCode(b)
  })
  return btoa(binary)
}

export class ApiError extends Error {}

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  const { user, pass } = getCredentials()
  if (user || pass) headers.set('Authorization', 'Basic ' + base64Utf8(`${user}:${pass}`))
  if (init.body != null && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  let res: Response
  try {
    res = await fetch(getBase() + path, { ...init, headers })
  } catch {
    throw new ApiError(`无法连接后端（${getBase()}），请确认地址正确且服务已启动`)
  }

  const text = await res.text()
  let data: unknown = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    data = null
  }

  if (!res.ok) {
    const payload = data as { error?: string; message?: string } | null
    throw new ApiError(payload?.error || payload?.message || `HTTP ${res.status}`)
  }
  if (data == null) throw new ApiError('返回内容不是 JSON，请确认 API 地址指向 chat 服务')
  return data as T
}

export function formatTime(seconds: number): string {
  if (!seconds) return ''
  const date = new Date(seconds * 1000)
  const now = new Date()
  const hh = String(date.getHours()).padStart(2, '0')
  const mm = String(date.getMinutes()).padStart(2, '0')
  if (date.toDateString() === now.toDateString()) return `${hh}:${mm}`
  const mo = String(date.getMonth() + 1).padStart(2, '0')
  const dd = String(date.getDate()).padStart(2, '0')
  return `${mo}-${dd} ${hh}:${mm}`
}

export interface NeteaseStatus {
  logged_in: boolean
  cookies: number
  saved_at?: string
}

export interface NeteaseQrStart {
  key: string
  image: string
  status: string
}

export interface NeteaseQrPoll {
  status: 'waiting' | 'scanned' | 'confirmed' | 'expired'
  code: number
}
