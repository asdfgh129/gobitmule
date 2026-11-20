import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { EventSocket, type SocketState } from '@/api/eventsocket'
import { rpc } from '@/api/rpc'
import { useSessionStore } from './session'
import { useTasksStore } from './tasks'
import type { UDEvent } from '@/types/api'

const MAX_EVENTS = 200

export const useEventsStore = defineStore('events', () => {
  const items = ref<UDEvent[]>([])
  const typeFilter = ref('')
  const socketState = ref<SocketState>('closed')
  const dropped = ref(0)
  let socket: EventSocket | null = null

  const visible = computed(() => {
    const list = typeFilter.value ? items.value.filter((e) => e.type === typeFilter.value) : items.value
    return [...list].reverse()
  })
  const types = computed(() => [...new Set(items.value.map((e) => e.type))].sort())

  function accept(event: UDEvent): void {
    const key = `${event.type}|${event.taskId ?? ''}|${event.ts}`
    if (items.value.some((e) => `${e.type}|${e.taskId ?? ''}|${e.ts}` === key)) return
    items.value.push(event)
    if (items.value.length > MAX_EVENTS) items.value.splice(0, items.value.length - MAX_EVENTS)
    const tasks = useTasksStore()
    tasks.applyEvent(event)
    const session = useSessionStore()
    session.lastEventAt = event.ts
    if (event.type === 'config.changed' || event.type === 'capability.changed') {
      void session.refresh()
    }
    if (event.type === 'task.chunk.failed') dropped.value++
  }

  async function loadHistory(limit = 100): Promise<void> {
    try {
      const result = await rpc.events.history(limit)
      for (const event of result.events) accept(event)
    } catch {
      // History is additive; an older daemon may not expose it (docs/07 §9).
    }
  }

  function start(): void {
    if (socket) return
    socket = new EventSocket({
      onEvent: accept,
      onState: (state) => { socketState.value = state },
    })
    socket.start()
  }

  function stop(): void {
    socket?.stop()
    socket = null
  }

  return { items, visible, types, typeFilter, socketState, dropped, accept, loadHistory, start, stop }
})
