import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { RpcError } from '@/api/client'

export interface Toast {
  id: number
  kind: 'success' | 'error' | 'info'
  text: string
}

export const useUiStore = defineStore('ui', () => {
  const toasts = ref<Toast[]>([])
  let seq = 0

  function push(kind: Toast['kind'], text: string): void {
    const id = ++seq
    toasts.value.push({ id, kind, text })
    setTimeout(() => dismiss(id), kind === 'error' ? 6000 : 3200)
  }

  function dismiss(id: number): void {
    toasts.value = toasts.value.filter((t) => t.id !== id)
  }

  function success(text: string): void { push('success', text) }
  function info(text: string): void { push('info', text) }
  function error(cause: unknown): void {
    const text = cause instanceof Error ? cause.message : String(cause)
    const code = (cause as RpcError | undefined)?.code
    push('error', code ? `${code} · ${text}` : text)
  }

  return { toasts, success, info, error, dismiss }
})
