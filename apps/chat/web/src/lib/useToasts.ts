import { useCallback, useState } from 'react'

export type ToastKind = 'success' | 'error' | 'info'

export interface ToastItem {
  id: number
  kind: ToastKind
  message: string
}

let seq = 0

export function useToasts() {
  const [toasts, setToasts] = useState<ToastItem[]>([])

  const dismiss = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id))
  }, [])

  const notify = useCallback(
    (message: string, kind: ToastKind = 'info', duration = 3600) => {
      const id = ++seq
      setToasts((prev) => [...prev, { id, kind, message }])
      window.setTimeout(() => dismiss(id), duration)
    },
    [dismiss],
  )

  const notifyError = useCallback(
    (err: unknown) => {
      const message = err instanceof Error ? err.message : String(err)
      notify(message, 'error', 5200)
    },
    [notify],
  )

  return { toasts, notify, notifyError, dismiss }
}
