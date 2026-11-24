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
type SortKey = 'fileName' | 'fileSize' | 'sources' | 'completeSources'

interface SavedForm {
  query?: string
  scope?: ED2KSearchScope
  fileType?: string
  fileTypes?: string[]
  extension?: string
  extensions?: string[]
  sources?: ED2KSearchResultSource[]
  minSizeMB?: string
  maxSizeMB?: string
  minSources?: string
  minCompleteSources?: string
}

interface QueryChip {
  text: string
  relation: 'and' | 'or'
}

const typeExtensions: Record<string, string[]> = {
  video: ['mp4', 'mkv', 'avi', 'mov', 'wmv', 'rmvb', 'flv', 'ts', 'm4v'],
  audio: ['mp3', 'flac', 'wav', 'aac', 'ogg', 'wma', 'm4a'],
  image: ['jpg', 'jpeg', 'png', 'gif', 'bmp', 'webp'],
  doc: ['pdf', 'doc', 'docx', 'txt', 'epub', 'mobi', 'xls', 'xlsx', 'ppt', 'pptx'],
  archive: ['zip', 'rar', '7z', 'tar', 'gz', 'iso'],
  software: ['exe', 'msi', 'apk', 'dmg', 'deb', 'rpm'],
}

const sourceLabels: Record<string, string> = {
  server: '服务器',
  kad: 'KAD',
  both: '双通道',
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

function parseQueryChips(query: string): QueryChip[] {
  const fields = query.trim().split(/[\s,;|]+/).filter(Boolean)
  const chips: QueryChip[] = []
  let relation: 'and' | 'or' = 'and'
  for (const field of fields) {
    const upper = field.toLocaleUpperCase()
    if (upper === 'OR') {
      relation = 'or'
      continue
    }
    if (upper === 'AND') continue
    chips.push({ text: field, relation })
    relation = 'and'
  }
  return chips
}

function unique(values: string[]): string[] {
  return Array.from(new Set(values.map((v) => v.trim().replace(/^\./, '')).filter(Boolean)))
}

function extensionOf(row: ED2KSearchResult): string {
  if (row.extension) return row.extension.replace(/^\./, '').toLocaleLowerCase()
  const name = row.fileName.split('/').pop() ?? row.fileName
  const dot = name.lastIndexOf('.')
  return dot > 0 ? name.slice(dot + 1).toLocaleLowerCase() : ''
}

export const useSearchStore = defineStore('ed2k-search', () => {
  const saved = loadForm()
  const query = ref(saved.query ?? '')
  const scope = ref<ED2KSearchScope>(saved.scope ?? 'all')
  const fileType = ref(saved.fileType ?? '')
  const fileTypes = ref<string[]>(saved.fileTypes ?? [])
  const extension = ref(saved.extension ?? '')
  const extensions = ref<string[]>(saved.extensions ?? [])
  const sources = ref<ED2KSearchResultSource[]>(saved.sources ?? [])
  const minSizeMB = ref(saved.minSizeMB ?? '')
  const maxSizeMB = ref(saved.maxSizeMB ?? '')
  const minSources = ref(saved.minSources ?? '')
  const minCompleteSources = ref(saved.minCompleteSources ?? '')
  const newKeyword = ref('')
  const keywordRelation = ref<'and' | 'or'>('and')

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
  const queryChips = computed(() => parseQueryChips(query.value))
  const selectedExtensions = computed(() => unique([
    ...fileTypes.value.flatMap((type) => typeExtensions[type] ?? []),
    ...extensions.value,
  ]))

  const filtersActive = computed(() => fileTypes.value.length > 0
    || sources.value.length > 0
    || selectedExtensions.value.length > 0
    || !!minSizeMB.value
    || !!maxSizeMB.value
    || !!minSources.value
    || !!minCompleteSources.value)

  function matchesLocalFilters(row: ED2KSearchResult): boolean {
    if (sources.value.length > 0) {
      const hit = sources.value.some((source) => {
        if (source === 'both') return row.source === 'both'
        if (source === 'server') return row.source === 'server' || row.source === 'both'
        if (source === 'kad') return row.source === 'kad' || row.source === 'both'
        return false
      })
      if (!hit) return false
    }
    if (fileTypes.value.length > 0 || selectedExtensions.value.length > 0) {
      const ext = extensionOf(row)
      const typeHit = fileTypes.value.includes((row.fileType || '').toLocaleLowerCase())
      const extHit = selectedExtensions.value.includes(ext)
      if (!typeHit && !extHit) return false
    }
    const min = toBytes(minSizeMB.value)
    const max = toBytes(maxSizeMB.value)
    if (min > 0 && row.fileSize > 0 && row.fileSize < min) return false
    if (max > 0 && row.fileSize > max) return false
    if (toCount(minSources.value) > 0 && row.sources < toCount(minSources.value)) return false
    if (toCount(minCompleteSources.value) > 0
      && row.completeSources < toCount(minCompleteSources.value)) return false
    return true
  }

  const results = computed(() => {
    const term = keyword.value.trim().toLocaleLowerCase()
    const rows = snapshot.value.results.filter((row) => {
      if (!matchesLocalFilters(row)) return false
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
      fileType: fileTypes.value[0] ?? '',
      extension: selectedExtensions.value[0] ?? '',
      fileTypes: [...fileTypes.value],
      sources: [...sources.value],
      extensions: selectedExtensions.value,
    }
  }

  // Pinia setup store 会自动解包 ref，这里直接操作解包后的响应式数组。
  function toggle<T>(list: T[], value: T): void {
    const index = list.indexOf(value)
    if (index >= 0) list.splice(index, 1)
    else list.push(value)
  }

  function addKeyword(): void {
    const term = newKeyword.value.trim()
    if (!term) return
    const current = query.value.trim()
    query.value = current
      ? `${current} ${keywordRelation.value === 'or' ? 'OR ' : ''}${term}`
      : term
    newKeyword.value = ''
  }

  function removeKeyword(chip: QueryChip): void {
    const chips = queryChips.value.filter((item) => item.text !== chip.text)
    query.value = chips.map((item, index) =>
      `${index > 0 && item.relation === 'or' ? 'OR ' : ''}${item.text}`).join(' ')
  }

  function clearFilters(): void {
    fileTypes.value = []
    extensions.value = []
    sources.value = []
    minSizeMB.value = ''
    maxSizeMB.value = ''
    minSources.value = ''
    minCompleteSources.value = ''
  }

  function rememberForm(): void {
    try {
      localStorage.setItem(FORM_KEY, JSON.stringify({
        query: query.value, scope: scope.value,
        fileType: fileType.value, fileTypes: fileTypes.value,
        extension: extension.value, extensions: extensions.value,
        sources: sources.value,
        minSizeMB: minSizeMB.value,
        maxSizeMB: maxSizeMB.value,
        minSources: minSources.value,
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
    query, scope, fileType, fileTypes, extension, extensions, sources,
    minSizeMB, maxSizeMB, minSources, minCompleteSources,
    newKeyword, keywordRelation, queryChips, selectedExtensions, filtersActive,
    keyword, sortKey, sortDir,
    status, snapshot, loading, stopping, error, available, running, results,
    sourceLabels, typeExtensions,
    toggle, addKeyword, removeKeyword, clearFilters,
    refresh, start, stop, toggleSort, dispose,
  }
})

export type { ED2KSearchResult }
