import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { rpc } from '@/api/rpc'
import {
  EMPTY_ED2K_SEARCH_STATUS,
  type ED2KSearchParams,
  type ED2KSearchResult,
  type ED2KSearchResultSource,
  type ED2KSearchScope,
  type ED2KSearchStatus,
} from '@/types/api'

const FORM_KEY = 'ud.ed2k.search.form'
const FILTER_KEY = 'ud.ed2k.result.filters'
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

interface SavedFilters {
  keywords?: string[]
  fileTypes?: string[]
  extensions?: string[]
  sources?: ED2KSearchResultSource[]
  minSizeMB?: string
  maxSizeMB?: string
  minSources?: string
  minCompleteSources?: string
}

const typeExtensions: Record<string, string[]> = {
  video: ['mp4', 'mkv', 'avi', 'mov', 'wmv', 'rmvb', 'flv', 'ts', 'm4v'],
  audio: ['mp3', 'flac', 'wav', 'aac', 'ogg', 'wma', 'm4a'],
  image: ['jpg', 'jpeg', 'png', 'gif', 'bmp', 'webp'],
  doc: ['pdf', 'doc', 'docx', 'txt', 'epub', 'mobi', 'xls', 'xlsx', 'ppt', 'pptx'],
  archive: ['zip', 'rar', '7z', 'tar', 'gz', 'iso'],
  software: ['exe', 'msi', 'apk', 'dmg', 'deb', 'rpm'],
}

function cloneEmpty(): ED2KSearchStatus {
  return JSON.parse(JSON.stringify(EMPTY_ED2K_SEARCH_STATUS)) as ED2KSearchStatus
}

function load<T extends object>(key: string): T {
  try {
    const raw = localStorage.getItem(key)
    return raw ? (JSON.parse(raw) as T) : ({} as T)
  } catch {
    return {} as T
  }
}

function unique(values: string[]): string[] {
  return Array.from(new Set(values.map((v) => v.trim().replace(/^\./, '').toLocaleLowerCase()).filter(Boolean)))
}

function extensionOf(row: ED2KSearchResult): string {
  if (row.extension) return row.extension.replace(/^\./, '').toLocaleLowerCase()
  const name = row.fileName.split('/').pop() ?? row.fileName
  const dot = name.lastIndexOf('.')
  return dot > 0 ? name.slice(dot + 1).toLocaleLowerCase() : ''
}

export const useSearchStore = defineStore('ed2k-search', () => {
  // 搜索发起选项：保持原有的简洁表单，原样下发到引擎。
  const saved = load<SavedForm>(FORM_KEY)
  const query = ref(saved.query ?? '')
  const scope = ref<ED2KSearchScope>(saved.scope ?? 'all')
  const fileType = ref(saved.fileType ?? '')
  const extension = ref(saved.extension ?? '')
  const minSizeMB = ref(saved.minSizeMB ?? '')
  const maxSizeMB = ref(saved.maxSizeMB ?? '')
  const minSources = ref(saved.minSources ?? '')
  const minCompleteSources = ref(saved.minCompleteSources ?? '')

  // 结果筛选：只作用于本次搜索已返回的结果，不重新发起搜索。
  const savedFilters = load<SavedFilters>(FILTER_KEY)
  const filterKeywords = ref<string[]>(savedFilters.keywords ?? [])
  const filterFileTypes = ref<string[]>(savedFilters.fileTypes ?? [])
  const filterExtensions = ref<string[]>(savedFilters.extensions ?? [])
  const filterSources = ref<ED2KSearchResultSource[]>(savedFilters.sources ?? [])
  const filterMinSizeMB = ref(savedFilters.minSizeMB ?? '')
  const filterMaxSizeMB = ref(savedFilters.maxSizeMB ?? '')
  const filterMinSources = ref(savedFilters.minSources ?? '')
  const filterMinCompleteSources = ref(savedFilters.minCompleteSources ?? '')
  const newFilterKeyword = ref('')
  const newFilterExtension = ref('')

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
  const filterSelectedExtensions = computed(() => unique([
    ...filterFileTypes.value.flatMap((type) => typeExtensions[type] ?? []),
    ...filterExtensions.value,
  ]))
  const filtersActive = computed(() => filterKeywords.value.length > 0
    || filterFileTypes.value.length > 0
    || filterExtensions.value.length > 0
    || filterSources.value.length > 0
    || !!filterMinSizeMB.value
    || !!filterMaxSizeMB.value
    || !!filterMinSources.value
    || !!filterMinCompleteSources.value)

  function toBytes(value: string): number {
    const n = Number(value)
    return Number.isFinite(n) && n > 0 ? Math.round(n * 1024 * 1024) : 0
  }

  function toCount(value: string): number {
    const n = Number(value)
    return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
  }

  function matchesFilters(row: ED2KSearchResult): boolean {
    if (filterKeywords.value.length > 0) {
      const haystack = `${row.fileName} ${row.hash}`.toLocaleLowerCase()
      for (const term of filterKeywords.value) {
        if (!haystack.includes(term.trim().toLocaleLowerCase())) return false
      }
    }
    if (filterSources.value.length > 0) {
      const hit = filterSources.value.some((source) => {
        if (source === 'both') return row.source === 'both'
        if (source === 'server') return row.source === 'server' || row.source === 'both'
        if (source === 'kad') return row.source === 'kad' || row.source === 'both'
        return false
      })
      if (!hit) return false
    }
    if (filterFileTypes.value.length > 0 || filterSelectedExtensions.value.length > 0) {
      const ext = extensionOf(row)
      const typeHit = filterFileTypes.value.includes((row.fileType || '').toLocaleLowerCase())
      const extHit = filterSelectedExtensions.value.includes(ext)
      if (!typeHit && !extHit) return false
    }
    const min = toBytes(filterMinSizeMB.value)
    const max = toBytes(filterMaxSizeMB.value)
    if (min > 0 && row.fileSize > 0 && row.fileSize < min) return false
    if (max > 0 && row.fileSize > max) return false
    const minSrc = toCount(filterMinSources.value)
    const minComplete = toCount(filterMinCompleteSources.value)
    if (minSrc > 0 && row.sources < minSrc) return false
    if (minComplete > 0 && row.completeSources < minComplete) return false
    return true
  }

  const results = computed(() => {
    const term = keyword.value.trim().toLocaleLowerCase()
    const rows = snapshot.value.results.filter((row) => {
      if (!matchesFilters(row)) return false
      if (!term) return true
      return row.fileName.toLocaleLowerCase().includes(term) || row.hash.toLocaleLowerCase().includes(term)
    })
    const key = sortKey.value
    const sign = sortDir.value === 'asc' ? 1 : -1
    return rows.sort((a, b) => {
      if (key === 'fileName') return sign * a.fileName.localeCompare(b.fileName)
      return sign * ((a[key] ?? 0) - (b[key] ?? 0))
    })
  })

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
      // 组合筛选只在结果区本地生效，不作为搜索参数下发。
      fileTypes: [], sources: [], extensions: [],
    }
  }

  // Pinia setup store 会自动解包 ref，这里直接操作解包后的响应式数组。
  function toggle<T>(list: T[], value: T): void {
    const index = list.indexOf(value)
    if (index >= 0) list.splice(index, 1)
    else list.push(value)
    rememberFilters()
  }

  function addFilterKeyword(): void {
    const term = newFilterKeyword.value.trim()
    if (!term || filterKeywords.value.includes(term)) return
    filterKeywords.value.push(term)
    newFilterKeyword.value = ''
    rememberFilters()
  }

  function removeFilterKeyword(value: string): void {
    filterKeywords.value = filterKeywords.value.filter((item) => item !== value)
    rememberFilters()
  }

  function addFilterExtension(): void {
    const value = newFilterExtension.value.trim().replace(/^\./, '').toLocaleLowerCase()
    if (!value) return
    if (!filterExtensions.value.includes(value)) filterExtensions.value.push(value)
    newFilterExtension.value = ''
    rememberFilters()
  }

  function removeFilterExtension(value: string): void {
    filterExtensions.value = filterExtensions.value.filter((item) => item !== value)
    rememberFilters()
  }

  function clearFilters(): void {
    filterKeywords.value = []
    filterFileTypes.value = []
    filterExtensions.value = []
    filterSources.value = []
    filterMinSizeMB.value = ''
    filterMaxSizeMB.value = ''
    filterMinSources.value = ''
    filterMinCompleteSources.value = ''
    rememberFilters()
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

  function rememberFilters(): void {
    try {
      localStorage.setItem(FILTER_KEY, JSON.stringify({
        keywords: filterKeywords.value, fileTypes: filterFileTypes.value,
        extensions: filterExtensions.value, sources: filterSources.value,
        minSizeMB: filterMinSizeMB.value, maxSizeMB: filterMaxSizeMB.value,
        minSources: filterMinSources.value, minCompleteSources: filterMinCompleteSources.value,
      }))
    } catch {
      // Storage is optional; filters still work for the current session.
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
    filterKeywords, filterFileTypes, filterExtensions, filterSources,
    filterMinSizeMB, filterMaxSizeMB, filterMinSources, filterMinCompleteSources,
    newFilterKeyword, newFilterExtension,
    filterSelectedExtensions, filtersActive,
    status, snapshot, loading, stopping, error, available, running, results,
    typeExtensions,
    toggle, addFilterKeyword, removeFilterKeyword,
    addFilterExtension, removeFilterExtension, clearFilters,
    refresh, start, stop, toggleSort, dispose,
  }
})

export type { ED2KSearchResult }
