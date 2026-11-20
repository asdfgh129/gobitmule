import { getToken, issueWsTicket } from './client'
import type { UDEvent } from '@/types/api'

export type SocketState = 'connecting' | 'open' | 'closed'

interface Handlers {
  onEvent: (event: UDEvent) => void
  onState: (state: SocketState) => void
}

/**
 * EventSocket keeps one live subscription to /api/v1/events.
 * Authenticated (non-loopback) daemons require a short-lived ticket because
 * browsers cannot send Authorization on a WebSocket handshake. If the daemon
 * predates that endpoint we fall back to a plain connection (loopback mode).
 */
export class EventSocket {
  private ws: WebSocket | null = null
  private retry: ReturnType<typeof setTimeout> | null = null
  private stopped = true
  private attempts = 0

  constructor(private readonly handlers: Handlers) {}

  start(): void {
    this.stopped = false
    this.attempts = 0
    void this.open()
  }

  stop(): void {
    this.stopped = true
    if (this.retry) clearTimeout(this.retry)
    this.retry = null
    this.ws?.close()
    this.ws = null
    this.handlers.onState('closed')
  }

  private scheduleRetry(): void {
    if (this.stopped) return
    const delay = Math.min(10_000, 800 * 2 ** Math.min(this.attempts++, 4))
    this.retry = setTimeout(() => void this.open(), delay)
  }

  private async socketURL(): Promise<string> {
    const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const base = `${scheme}//${location.host}/api/v1/events`
    if (!getToken()) return base
    try {
      const ticket = await issueWsTicket()
      return `${base}?ticket=${encodeURIComponent(ticket.ticket)}`
    } catch {
      // Older daemon without /ws-ticket: loopback still allows a bare socket.
      return base
    }
  }

  private async open(): Promise<void> {
    if (this.stopped) return
    this.handlers.onState('connecting')
    let url: string
    try {
      url = await this.socketURL()
    } catch {
      this.scheduleRetry()
      return
    }
    if (this.stopped) return

    const ws = new WebSocket(url)
    this.ws = ws
    ws.onopen = () => {
      this.attempts = 0
      this.handlers.onState('open')
    }
    ws.onmessage = (message) => {
      try {
        const event = JSON.parse(String(message.data)) as UDEvent
        if (event && typeof event.type === 'string') this.handlers.onEvent(event)
      } catch {
        // A malformed frame must never tear down the console.
      }
    }
    ws.onerror = () => ws.close()
    ws.onclose = () => {
      if (this.ws === ws) this.ws = null
      this.handlers.onState('closed')
      this.scheduleRetry()
    }
  }
}
