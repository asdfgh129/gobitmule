import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { rpc } from '@/api/rpc'
import {
  EMPTY_ED2K_SEARCH_STATUS,
  type ED2KSearchParams,
  type ED2KSearchResult,
  type ED2KSearchScope,
  type ED2KSearchStatus,
} from '@/types/api'

const FORM_KEY = 'ud.ed2k.search.form'
type SortKey = 'fileName' | 'fileSize' | 'sources' | 'completeSources'

interface SavedForm {
  query?: string
  scope?: ED2KSearchScope
  fileType?: string
  extension?: string
  minSizeMB?: string
  maxSizeMB?: string
  minSources?: string
  minCompleteSources?: string
}

function cloneEmpty(): ED2KSearchStatus {
  return JSON.parse(JSON.stringify(EMPTY_ED2K_SEARCH_STATUS)) as ED2KSearchStatus
}

function loadForm(): SavedForm {
  try {
    const raw = localStorage.getItem(FORM_KEY)
    return raw ? (JSON.parse(raw) as SavedForm) : {}
  } catch {
    return {}
  }
}

export const useSearchStore = defineStore('ed2k-search', () => {
  const saved = loadForm()
  const query = ref(saved.query ?? '')
  const scope = ref<ED2KSearchScope>(saved.scope ?? 'all')
  const fileType = ref(saved.fileType ?? '')
  const extension = ref(saved.extension ?? '')
  const minSizeMB = ref(saved.minSizeMB ?? '')
  const maxSizeMB = ref(saved.maxSizeMB ?? '')
  const minSources = ref(saved.minSources ?? '')
  const minCompleteSources = ref(saved.minCompleteSources ?? '')

  const status = ref<ED2KSearchStatus>(cloneEmpty())
  const loading = ref(false)
  const stopping = ref(false)
  const error = ref('')
  const keyword = ref('')
  const sortKey = ref<SortKey>('sources')
  const sortDir = ref<'asc' | 'desc'>('desc')
  let timer: ReturnType<typeof setInterval> | null = null

  const snapshot = computed(() => status.value.snapshot)
  const available = computed(() => status.value.available)
  const running = computed(() => snapshot.value.state === 'RUNNING')
  const results = computed(() => {
    const term = keyword.value.trim().toLocaleLowerCase()
    const rows = term
      ? snapshot.value.results.filter((r) =>
        r.fileName.toLocaleLowerCase().includes(term) || r.hash.toLocaleLowerCase().includes(term))
      : snapshot.value.results.slice()
    const key = sortKey.value
    const sign = sortDir.value === 'asc' ? 1 : -1
    return rows.sort((a, b) => {
      if (key === 'fileName') return sign * a.fileName.localeCompare(b.fileName)
      return sign * ((a[key] ?? 0) - (b[key] ?? 0))
    })
  })

  function toBytes(value: string): number {
    const n = Number(value)
    return Number.isFinite(n) && n > 0 ? Math.round(n * 1024 * 1024) : 0
  }

  function toCount(value: string): number {
    const n = Number(value)
    return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
  }

  function params(): ED2KSearchParams {
    return {
      query: query.value.trim(),
      scope: scope.value,
      minSize: toBytes(minSizeMB.value),
      maxSize: toBytes(maxSizeMB.value),
      minSources: toCount(minSources.value),
      minCompleteSources: toCount(minCompleteSources.value),
      fileType: fileType.value.trim(),
      extension: extension.value.trim().replace(/^\./, ''),
    }
  }

  function rememberForm(): void {
    try {
      localStorage.setItem(FORM_KEY, JSON.stringify({
        query: query.value, scope: scope.value, fileType: fileType.value,
        extension: extension.value, minSizeMB: minSizeMB.value,
        maxSizeMB: maxSizeMB.value, minSources: minSources.value,
        minCompleteSources: minCompleteSources.value,
      }))
    } catch {
      // Private browsing can disable storage; searching must still work.
    }
  }

  function stopPolling(): void {
    if (timer !== null) {
      clearInterval(timer)
      timer = null
    }
  }

  function startPolling(): void {
    if (timer !== null) return
    timer = setInterval(() => { void refresh() }, 1000)
  }

  async function refresh(): Promise<void> {
    try {
      status.value = await rpc.ed2kSearch.status()
      error.value = status.value.snapshot.error
      if (!running.value) stopPolling()
    } catch (cause) {
      stopPolling()
      error.value = cause instanceof Error ? cause.message : String(cause)
    }
  }

  async function start(): Promise<void> {
    if (!query.value.trim()) {
      error.value = '请输入搜索关键词'
      return
    }
    if (toBytes(minSizeMB.value) > 0 && toBytes(maxSizeMB.value) > 0
      && toBytes(minSizeMB.value) > toBytes(maxSizeMB.value)) {
      error.value = '最小大小不能大于最大大小'
      return
    }
    loading.value = true
    error.value = ''
    try {
      rememberForm()
      status.value = await rpc.ed2kSearch.start(params())
      error.value = status.value.snapshot.error
      startPolling()
    } catch (cause) {
      error.value = cause instanceof Error ? cause.message : String(cause)
      throw cause
    } finally {
      loading.value = false
    }
  }

  async function stop(): Promise<void> {
    stopping.value = true
    try {
      status.value = await rpc.ed2kSearch.stop()
      error.value = status.value.snapshot.error
    } catch (cause) {
      error.value = cause instanceof Error ? cause.message : String(cause)
      throw cause
    } finally {
      stopPolling()
      stopping.value = false
    }
  }

  function toggleSort(key: SortKey): void {
    if (sortKey.value === key) {
      sortDir.value = sortDir.value === 'asc' ? 'desc' : 'asc'
    } else {
      sortKey.value = key
      sortDir.value = key === 'fileName' ? 'asc' : 'desc'
    }
  }

  function dispose(): void {
    stopPolling()
  }

  return {
    query, scope, fileType, extension, minSizeMB, maxSizeMB,
    minSources, minCompleteSources, keyword, sortKey, sortDir,
    status, snapshot, loading, stopping, error, available, running, results,
    refresh, start, stop, toggleSort, dispose,
  }
})

export type { ED2KSearchResult }
