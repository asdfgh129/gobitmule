import { defineStore } from 'pinia'
import { computed, reactive, ref } from 'vue'
import { rpc } from '@/api/rpc'
import { useUiStore } from './ui'
import type { InputKind, Task, TaskDetail, TaskOptions, TaskState, UDEvent } from '@/types/api'

const ACTIVE: TaskState[] = ['Pending', 'Resolving', 'Ready', 'Running', 'Paused', 'PostProcessing', 'Seeding']

export const useTasksStore = defineStore('tasks', () => {
  const items = ref<Task[]>([])
  const total = ref(0)
  const filter = reactive<{ state: TaskState | ''; kind: InputKind | ''; search: string }>({
    state: '', kind: '', search: '',
  })
  const selectedId = ref<string | null>(null)
  const detail = ref<TaskDetail | null>(null)
  const loading = ref(false)
  let refreshTimer: ReturnType<typeof setTimeout> | null = null

  const activeCount = computed(() => items.value.filter((t) => ACTIVE.includes(t.state)).length)
  const visible = computed(() => {
    const q = filter.search.trim().toLowerCase()
    return items.value.filter((t) => !q || t.input.toLowerCase().includes(q) || t.id.toLowerCase().includes(q))
  })

  async function fetchList(): Promise<void> {
    loading.value = true
    try {
      const result = await rpc.tasks.list({ state: filter.state, kind: filter.kind, limit: 200 })
      items.value = result.tasks
      total.value = result.total
    } finally {
      loading.value = false
    }
  }

  function scheduleFetch(): void {
    if (refreshTimer) clearTimeout(refreshTimer)
    refreshTimer = setTimeout(() => void fetchList(), 250)
  }

  async function open(id: string): Promise<void> {
    selectedId.value = id
    detail.value = await rpc.tasks.get(id)
  }

  function close(): void {
    selectedId.value = null
    detail.value = null
  }

  async function refreshDetail(): Promise<void> {
    if (!selectedId.value) return
    try {
      detail.value = await rpc.tasks.get(selectedId.value)
    } catch {
      close()
    }
  }

  function applyEvent(event: UDEvent): void {
    const id = event.taskId
    if (!id) return
    const row = items.value.find((t) => t.id === id)
    if (row && event.type === 'task.progress') {
      if (typeof event.payload?.bytesDone === 'number') row.bytesDone = event.payload.bytesDone
      if (typeof event.payload?.bytesTotal === 'number') row.bytesTotal = event.payload.bytesTotal
    }
    if (row && event.type === 'task.state' && typeof event.payload?.state === 'string') {
      row.state = event.payload.state as TaskState
    }
    if (event.type === 'task.created' || event.type === 'task.completed' ||
        event.type === 'task.failed' || event.type === 'task.canceled') {
      scheduleFetch()
    }
    if (selectedId.value === id) void refreshDetail()
  }

  async function act(id: string, action: 'pause' | 'resume' | 'cancel', label: string): Promise<void> {
    const ui = useUiStore()
    try {
      await rpc.tasks[action](id)
      ui.success(`${label}已提交`)
      await Promise.all([fetchList(), refreshDetail()])
    } catch (cause) {
      ui.error(cause)
    }
  }

  async function remove(id: string): Promise<void> {
    const ui = useUiStore()
    try {
      await rpc.tasks.remove(id)
      ui.success('任务已删除')
      if (selectedId.value === id) close()
      await fetchList()
    } catch (cause) {
      ui.error(cause)
    }
  }

  async function patch(id: string, options: TaskOptions): Promise<void> {
    await rpc.tasks.patch(id, options)
    await Promise.all([fetchList(), refreshDetail()])
  }

  return {
    items, total, filter, selectedId, detail, loading, activeCount, visible,
    fetchList, open, close, refreshDetail, applyEvent, act, remove, patch,
  }
})
