import type { CoreError, WsTicket } from '@/types/api'

/** Every non-2xx daemon response is normalised to one typed error. */
export class RpcError extends Error {
  readonly code: string
  readonly retryable: boolean
  readonly details?: Record<string, unknown>

  constructor(err: CoreError) {
    super(err.message || err.code)
    this.name = 'RpcError'
    this.code = err.code
    this.retryable = err.retryable
    this.details = err.details
  }
}

const TOKEN_KEY = 'ud.console.token'
let token = localStorage.getItem(TOKEN_KEY) ?? ''

export function getToken(): string {
  return token
}

export function setToken(value: string): void {
  token = value.trim()
  if (token) localStorage.setItem(TOKEN_KEY, token)
  else localStorage.removeItem(TOKEN_KEY)
}

function authHeaders(): Record<string, string> {
  return token ? { Authorization: `Bearer ${token}` } : {}
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  extraHeaders: Record<string, string> = {},
): Promise<T> {
  const headers: Record<string, string> = { ...authHeaders(), ...extraHeaders }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  let response: Response
  try {
    response = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch (cause) {
    throw new RpcError({
      code: 'E_NETWORK',
      message: `无法连接守护进程：${cause instanceof Error ? cause.message : String(cause)}`,
      retryable: true,
    })
  }
  if (response.status === 204) return undefined as T

  let payload: unknown
  try {
    payload = await response.json()
  } catch {
    throw new RpcError({
      code: 'E_INTERNAL',
      message: `守护进程返回了无法解析的响应（HTTP ${response.status}）`,
      retryable: false,
    })
  }
  if (!response.ok) {
    const e = (payload as { error?: CoreError })?.error
    throw new RpcError(e ?? {
      code: 'E_INTERNAL',
      message: `HTTP ${response.status}`,
      retryable: false,
    })
  }
  return payload as T
}

export const http = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown, headers?: Record<string, string>) =>
    request<T>('POST', path, body, headers),
  patch: <T>(path: string, body: unknown) => request<T>('PATCH', path, body),
  delete: <T>(path: string) => request<T>('DELETE', path),
}

/** Mints the short-lived credential used by the browser event socket. */
export function issueWsTicket(): Promise<WsTicket> {
  return http.post<WsTicket>('/api/v1/ws-ticket')
}
