<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import Icon from '@/components/Icon.vue'
import EmptyState from '@/components/EmptyState.vue'
import { rpc } from '@/api/rpc'
import { useSearchStore } from '@/stores/search'
import { useUiStore } from '@/stores/ui'
import { formatBytes, formatTime } from '@/utils/format'
import type { ED2KSearchResult, ED2KSearchState } from '@/types/api'

const search = useSearchStore()
const ui = useUiStore()
const router = useRouter()
const advanced = ref(false)
const filtersOpen = ref(false)

const fileTypeOptions = [
  { value: 'video', label: '视频' },
  { value: 'audio', label: '音频' },
  { value: 'image', label: '图片' },
  { value: 'doc', label: '文档' },
  { value: 'archive', label: '压缩包' },
  { value: 'software', label: '软件' },
]
const sourceOptions = [
  { value: 'server', label: '服务器' },
  { value: 'kad', label: 'KAD' },
  { value: 'both', label: '双通道' },
]

const stateLabels: Record<ED2KSearchState, string> = {
  IDLE: '未搜索', RUNNING: '搜索中', FINISHED: '已完成', STOPPED: '已停止', FAILED: '失败',
}
const stateTone = computed(() => {
  switch (search.snapshot.state) {
    case 'RUNNING': return 'active'
    case 'FINISHED': return 'success'
    case 'FAILED': return 'danger'
    case 'STOPPED': return 'warn'
    default: return 'muted'
  }
})

const elapsed = computed(() => {
  const s = search.snapshot
  if (!s.startedAt) return '—'
  const end = s.updatedAt || Date.now()
  const sec = Math.max(0, Math.round((end - s.startedAt) / 1000))
  return sec < 60 ? `${sec} 秒` : `${Math.floor(sec / 60)} 分 ${sec % 60} 秒`
})

const fileKinds: Record<string, { label: string; tone: string }> = {
  video: { label: '视频', tone: 'video' },
  audio: { label: '音频', tone: 'audio' },
  image: { label: '图片', tone: 'image' },
  doc: { label: '文档', tone: 'doc' },
  archive: { label: '压缩包', tone: 'archive' },
  software: { label: '软件', tone: 'software' },
}
const extKinds: Record<string, string> = {
  mp4: 'video', mkv: 'video', avi: 'video', mov: 'video', wmv: 'video', rmvb: 'video', flv: 'video', ts: 'video',
  mp3: 'audio', flac: 'audio', wav: 'audio', aac: 'audio', ogg: 'audio', wma: 'audio',
  jpg: 'image', jpeg: 'image', png: 'image', gif: 'image', bmp: 'image', webp: 'image',
  pdf: 'doc', doc: 'doc', docx: 'doc', txt: 'doc', epub: 'doc', mobi: 'doc', xls: 'doc', ppt: 'doc',
  zip: 'archive', rar: 'archive', '7z': 'archive', tar: 'archive', gz: 'archive', iso: 'archive',
  exe: 'software', msi: 'software', apk: 'software', dmg: 'software',
}

function extensionOf(row: ED2KSearchResult): string {
  if (row.extension) return row.extension.replace(/^\./, '').toLocaleLowerCase()
  const name = row.fileName.split('/').pop() ?? row.fileName
  const dot = name.lastIndexOf('.')
  return dot > 0 ? name.slice(dot + 1).toLocaleLowerCase() : ''
}

function kindOf(row: ED2KSearchResult): { label: string; tone: string } {
  const raw = (row.fileType || '').toLocaleLowerCase()
  if (fileKinds[raw]) return fileKinds[raw]
  const tone = extKinds[extensionOf(row)] ?? 'other'
  return fileKinds[tone] ?? { label: tone.toLocaleUpperCase() || '文件', tone: 'other' }
}

function sourceLabel(source: ED2KSearchResult['source']): string {
  if (source === 'both') return '服务器 + KAD'
  if (source === 'kad') return 'KAD'
  if (source === 'server') return '服务器'
  return '未知'
}

const scopeLabels: Record<string, string> = {
  all: '服务器 + KAD', server: 'eD2k 服务器', kad: 'KAD 网络',
}
function scopeLabel(scope: string): string {
  return scopeLabels[scope] ?? 'eD2k 网络'
}

async function copyLink(row: ED2KSearchResult): Promise<void> {
  try {
    await navigator.clipboard.writeText(row.ed2kLink)
    ui.success('eD2K 链接已复制')
  } catch {
    ui.error(new Error('浏览器拒绝了剪贴板写入，请手动复制该行的 eD2K 链接'))
  }
}

async function download(row: ED2KSearchResult): Promise<void> {
  if (!row.ed2kLink) {
    ui.error(new Error('该结果缺少有效的 eD2K 链接'))
    return
  }
  try {
    await rpc.tasks.create(row.ed2kLink)
    ui.success('已加入下载队列')
    await router.push({ name: 'tasks' })
  } catch (cause) {
    ui.error(cause)
  }
}

function toggleFilterType(value: string): void {
  search.toggle(search.filterFileTypes, value)
}

function toggleFilterSource(value: 'server' | 'kad' | 'both'): void {
  search.toggle(search.filterSources, value)
}

function toggleFilterExtension(ext: string): void {
  search.toggle(search.filterExtensions, ext)
}

async function submit(): Promise<void> {
  try {
    await search.start()
    ui.success('搜索已发起，结果会实时更新')
  } catch (cause) {
    ui.error(cause)
  }
}

async function stop(): Promise<void> {
  try {
    await search.stop()
  } catch (cause) {
    ui.error(cause)
  }
}

function sortMark(key: 'fileName' | 'fileSize' | 'sources' | 'completeSources'): string {
  if (search.sortKey !== key) return ''
  return search.sortDir === 'asc' ? '↑' : '↓'
}

onMounted(() => { void search.refresh() })
onUnmounted(() => search.dispose())
</script>

<template>
  <div class="view ed-search-view">
    <!-- 搜索区：保持 eMule 搜索页的关键词 + 简单选项布局 -->
    <section class="panel ed-query-panel">
      <form class="ed-query" @submit.prevent="submit">
        <div class="ed-query__row">
          <label class="ed-query__input">
            <Icon name="search" :size="18" />
            <input
              v-model="search.query"
              type="search"
              placeholder="输入文件名或关键词，多个词默认 AND，用 OR 表示或者"
              autocomplete="off"
              :disabled="!search.available"
            />
          </label>
          <button class="btn btn--primary ed-query__submit" type="submit" :disabled="search.loading || search.running || !search.available">
            <Icon name="search" :size="16" /> {{ search.loading ? '发起中…' : '搜索' }}
          </button>
          <button class="btn btn--ghost" type="button" :disabled="search.stopping || !search.running" @click="stop">
            <Icon name="close" :size="15" /> 停止
          </button>
        </div>

        <div class="ed-query__filters">
          <label class="ed-field">
            <span>搜索范围</span>
            <select v-model="search.scope" class="select">
              <option value="all">服务器 + KAD</option>
              <option value="server">仅 eD2k 服务器</option>
              <option value="kad">仅 KAD 网络</option>
            </select>
          </label>
          <label class="ed-field">
            <span>文件类型</span>
            <select v-model="search.fileType" class="select">
              <option value="">任意类型</option>
              <option value="video">视频</option>
              <option value="audio">音频</option>
              <option value="image">图片</option>
              <option value="doc">文档</option>
              <option value="archive">压缩包</option>
              <option value="software">软件</option>
            </select>
          </label>
          <label class="ed-field">
            <span>最小大小 (MiB)</span>
            <input v-model="search.minSizeMB" type="number" min="0" step="0.1" placeholder="不限" />
          </label>
          <label class="ed-field">
            <span>最大大小 (MiB)</span>
            <input v-model="search.maxSizeMB" type="number" min="0" step="0.1" placeholder="不限" />
          </label>
          <label class="ed-field">
            <span>最少来源</span>
            <input v-model="search.minSources" type="number" min="0" step="1" placeholder="不限" />
          </label>
        </div>

        <div v-if="advanced" class="ed-query__filters ed-query__filters--advanced">
          <label class="ed-field">
            <span>完整来源数</span>
            <input v-model="search.minCompleteSources" type="number" min="0" step="1" placeholder="不限" />
          </label>
          <label class="ed-field">
            <span>扩展名</span>
            <input v-model="search.extension" type="text" placeholder="例如 mkv" />
          </label>
        </div>

        <div class="ed-query__foot">
          <button class="link-btn" type="button" @click="advanced = !advanced">
            <Icon name="chevron" :size="14" :class="{ rotated: advanced }" />
            {{ advanced ? '收起高级筛选' : '展开高级筛选' }}
          </button>
          <span class="muted">关键词通过 eD2k 服务器搜索与 KAD 关键字查找并行下发</span>
        </div>
      </form>
    </section>

    <div v-if="!search.available" class="notice notice--danger">
      <Icon name="alert" :size="16" />
      <div>
        <strong>eD2k 引擎未启用</strong>
        <p>
          请到
          <RouterLink class="link-btn" :to="{ name: 'settings', query: { tab: 'emule' } }">设置 → eMule</RouterLink>
          启用引擎并配置 nodesDat / serverMet；保存后即可搜索（下载通道需重启）。
        </p>
      </div>
    </div>
    <div v-if="search.error" class="notice notice--danger">
      <Icon name="alert" :size="16" />
      <div><strong>搜索异常</strong><p>{{ search.error }}</p></div>
    </div>

    <!-- 结果区：模仿 eMule 搜索页的多列文件列表 -->
    <section class="panel ed-results-panel">
      <header class="panel__head panel__head--toolbar">
        <div>
          <h2>搜索结果 <span class="count-pill">{{ search.results.length }}</span></h2>
          <p v-if="search.snapshot.id">
            {{ scopeLabel(search.snapshot.params.scope) }} · 关键字 “{{ search.snapshot.params.query }}” · 用时 {{ elapsed }}
          </p>
          <p v-else>从上方输入关键词开始搜索</p>
        </div>
        <div class="ed-results__tools">
          <span class="badge" :class="`badge--${stateTone}`">
            <span class="dot" />{{ stateLabels[search.snapshot.state] }}
          </span>
          <button class="btn btn--ghost btn--sm" type="button" @click="filtersOpen = !filtersOpen">
            <Icon name="filter" :size="14" /> 筛选结果
            <span v-if="search.filtersActive" class="count-pill count-pill--tiny">{{ search.results.length }}</span>
          </button>
          <label class="search ed-results__filter">
            <Icon name="search" :size="14" />
            <input v-model="search.keyword" placeholder="筛选结果" />
          </label>
        </div>
      </header>

      <!-- 结果筛选：仅对已返回的结果做本地组合过滤，不重新搜索 -->
      <div v-if="filtersOpen" class="ed-result-filters">
        <div class="ed-filter-grid">
          <div class="ed-filter-group">
            <span class="ed-filter-group__title">关键字（全部满足）</span>
            <div class="ed-chips">
              <button v-for="word in search.filterKeywords" :key="word" type="button" class="ed-chip ed-chip--keyword active"
                :title="`移除 ${word}`" @click="search.removeFilterKeyword(word)">
                {{ word }} <span class="ed-chip__x">×</span>
              </button>
              <span class="ed-chip-add">
                <input v-model="search.newFilterKeyword" type="text" placeholder="添加关键字" @keyup.enter.prevent="search.addFilterKeyword" />
                <button class="icon-btn" type="button" title="添加关键字" @click="search.addFilterKeyword">+</button>
              </span>
              <span v-if="!search.filterKeywords.length" class="muted">不限</span>
            </div>
          </div>

          <div class="ed-filter-group">
            <span class="ed-filter-group__title">结果来源</span>
            <div class="ed-chip-row">
              <button v-for="opt in sourceOptions" :key="opt.value" type="button" class="ed-chip"
                :class="{ active: search.filterSources.includes(opt.value as typeof search.filterSources[number]) }"
                @click="toggleFilterSource(opt.value as typeof search.filterSources[number])">
                {{ opt.label }}
              </button>
            </div>
          </div>

          <div class="ed-filter-group">
            <span class="ed-filter-group__title">文件类型</span>
            <div class="ed-chip-row">
              <button v-for="opt in fileTypeOptions" :key="opt.value" type="button" class="ed-chip"
                :class="{ active: search.filterFileTypes.includes(opt.value) }" @click="toggleFilterType(opt.value)">
                {{ opt.label }}
              </button>
            </div>
          </div>

          <div class="ed-filter-group">
            <span class="ed-filter-group__title">大小 / 来源数</span>
            <div class="ed-filter-inputs">
              <label class="ed-field"><span>最小 MiB</span><input v-model="search.filterMinSizeMB" type="number" min="0" step="0.1" placeholder="不限" /></label>
              <label class="ed-field"><span>最大 MiB</span><input v-model="search.filterMaxSizeMB" type="number" min="0" step="0.1" placeholder="不限" /></label>
              <label class="ed-field"><span>最少来源</span><input v-model="search.filterMinSources" type="number" min="0" step="1" placeholder="不限" /></label>
              <label class="ed-field"><span>完整源</span><input v-model="search.filterMinCompleteSources" type="number" min="0" step="1" placeholder="不限" /></label>
            </div>
          </div>

          <div class="ed-filter-group ed-filter-group--wide">
            <span class="ed-filter-group__title">扩展名（选中文件类型会自动包含常用扩展名）</span>
            <div class="ed-chip-row">
              <button v-for="ext in search.filterSelectedExtensions" :key="ext" type="button" class="ed-chip"
                :class="{ active: search.filterExtensions.includes(ext) }" @click="toggleFilterExtension(ext)">
                .{{ ext }}
              </button>
              <span class="ed-chip-add">
                <input v-model="search.newFilterExtension" type="text" placeholder="自定义扩展名" @keyup.enter.prevent="search.addFilterExtension" />
                <button class="icon-btn" type="button" title="添加扩展名" @click="search.addFilterExtension">+</button>
              </span>
              <span v-if="!search.filterSelectedExtensions.length" class="muted">未选择文件类型</span>
            </div>
          </div>
        </div>
        <div class="ed-result-filters__foot">
          <button v-if="search.filtersActive" class="link-btn" type="button" @click="search.clearFilters">清空结果筛选</button>
          <span class="muted">筛选只作用于已返回的结果，不会重新发起搜索</span>
        </div>
      </div>

      <div v-if="search.running" class="ed-progress">
        <span class="ed-progress__spinner" />
        <span>{{ search.snapshot.dhtBusy ? 'KAD 关键字查找中' : '服务器查询中' }}…</span>
        <span class="muted">已收到 {{ search.snapshot.results.length }} 条结果</span>
      </div>

      <div v-if="search.results.length === 0" class="ed-results__empty">
        <EmptyState
          :icon="search.running ? 'search' : 'file'"
          :title="search.running ? '正在查找文件…' : '暂无搜索结果'"
          :hint="search.running
            ? '结果会随着服务器与 KAD 节点响应逐步出现'
            : '调整关键词或放宽结果筛选条件后重试'"
        />
      </div>
      <div v-else class="task-table__wrap">
        <table class="task-table ed-table">
          <thead>
            <tr>
              <th>
                <button class="ed-sort" @click="search.toggleSort('fileName')">文件名 {{ sortMark('fileName') }}</button>
              </th>
              <th>
                <button class="ed-sort" @click="search.toggleSort('fileSize')">大小 {{ sortMark('fileSize') }}</button>
              </th>
              <th>
                <button class="ed-sort" @click="search.toggleSort('sources')">来源 {{ sortMark('sources') }}</button>
              </th>
              <th>
                <button class="ed-sort" @click="search.toggleSort('completeSources')">完整源 {{ sortMark('completeSources') }}</button>
              </th>
              <th>类型 / 编码</th>
              <th>文件哈希</th>
              <th class="task-table__actions-head">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, i) in search.results" :key="`${row.hash}-${i}`">
              <td>
                <div class="ed-file">
                  <span class="ed-file__type" :class="`ed-file__type--${kindOf(row).tone}`">{{ extensionOf(row).slice(0, 4).toUpperCase() || 'FILE' }}</span>
                  <div class="ed-file__meta">
                    <span class="ed-file__name" :title="row.fileName">{{ row.fileName || '(未命名文件)' }}</span>
                    <span class="ed-file__hash mono">{{ row.hash }}</span>
                  </div>
                </div>
              </td>
              <td class="ed-num">{{ formatBytes(row.fileSize) }}</td>
              <td>
                <div class="ed-sources">
                  <strong>{{ row.sources }}</strong>
                  <span class="chip chip--soft">{{ sourceLabel(row.source) }}</span>
                </div>
              </td>
              <td class="ed-num">{{ row.completeSources || '—' }}</td>
              <td>
                <div class="ed-format">
                  <span class="chip">{{ kindOf(row).label }}</span>
                  <span v-if="row.mediaCodec" class="muted">{{ row.mediaCodec }}</span>
                </div>
              </td>
              <td>
                <span class="mono ed-hash">{{ row.hash ? `${row.hash.slice(0, 8)}…${row.hash.slice(-6)}` : '—' }}</span>
              </td>
              <td class="task-table__actions">
                <button class="icon-btn" title="复制 eD2K 链接" :disabled="!row.ed2kLink" @click="copyLink(row)">
                  <Icon name="copy" :size="14" />
                </button>
                <button class="icon-btn" title="加入下载队列" :disabled="!row.ed2kLink" @click="download(row)">
                  <Icon name="download" :size="14" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <footer class="panel__foot ed-results__foot">
        <span>找到 {{ search.snapshot.results.length }} 个文件 · 显示 {{ search.results.length }} 个</span>
        <span v-if="search.snapshot.kadKeyword">KAD 关键字：{{ search.snapshot.kadKeyword }}</span>
        <span v-if="search.snapshot.updatedAt">更新于 {{ formatTime(search.snapshot.updatedAt) }}</span>
      </footer>
    </section>
  </div>
</template>
