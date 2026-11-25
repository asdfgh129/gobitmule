<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import TaskTable from '@/components/TaskTable.vue'
import Drawer from '@/components/Drawer.vue'
import StateBadge from '@/components/StateBadge.vue'
import ProgressBar from '@/components/ProgressBar.vue'
import EventFeed from '@/components/EventFeed.vue'
import Icon from '@/components/Icon.vue'
import { useTasksStore } from '@/stores/tasks'
import { useEventsStore } from '@/stores/events'
import { useSessionStore } from '@/stores/session'
import { formatBytes, formatEta, formatRate, formatTime } from '@/utils/format'
import type { InputKind, TaskState } from '@/types/api'

const route = useRoute()
const router = useRouter()
const tasks = useTasksStore()
const events = useEventsStore()
const session = useSessionStore()
const detailTab = ref<'overview' | 'files' | 'sources' | 'events'>('overview')
let sourceTimer: ReturnType<typeof setInterval> | null = null

const sourceStateLabels: Record<string, string> = {
  disconnecting: '断开中', downloading: '下载中', queued: '排队中',
  handshaking: '握手中', requesting: '请求中', connected: '已连接',
}
function sourceLabel(value: string): string {
  return sourceStateLabels[value] || value || '未知'
}
function sourceChip(state: string): string {
  if (state === 'downloading') return 'chip--success'
  if (state === 'queued' || state === 'handshaking' || state === 'disconnecting') return 'chip--warn'
  return 'chip--soft'
}

const states: Array<TaskState | ''> = [
  '', 'Pending', 'Resolving', 'Ready', 'Running', 'Paused',
  'PostProcessing', 'Seeding', 'Completed', 'Failed', 'Canceled',
]
const kinds: Array<InputKind | ''> = ['', 'url', 'magnet', 'torrent', 'metalink', 'thunder', 'site', 'ed2k']

function openTask(id: string): void {
  void tasks.open(id)
  void router.replace({ name: 'tasks', query: { task: id } })
}

function closeTask(): void {
  tasks.close()
  void router.replace({ name: 'tasks' })
}

function removeTask(id: string): void {
  if (window.confirm('确认删除该任务？此操作不会删除已下载的文件。')) void tasks.remove(id)
}

watch(() => tasks.filter.state, () => void tasks.fetchList())
watch(() => tasks.filter.kind, () => void tasks.fetchList())

// eMule refreshes its client list continuously; poll the task detail only
// while the source tab is visible so speeds and queue ranks stay fresh.
watch(detailTab, (tab) => {
  if (sourceTimer) { clearInterval(sourceTimer); sourceTimer = null }
  if (tab === 'sources' && tasks.selectedId) {
    void tasks.refreshDetail()
    sourceTimer = setInterval(() => {
      if (!document.hidden && tasks.selectedId) void tasks.refreshDetail()
    }, 3000)
  }
})
onUnmounted(() => { if (sourceTimer) clearInterval(sourceTimer) })

onMounted(() => {
  const id = route.query.task
  if (typeof id === 'string' && id) void tasks.open(id)
})

const taskEvents = () => events.items.filter((e) => e.taskId === tasks.detail?.id)
const sidecars = () => Object.keys(session.health?.sidecars ?? {})
</script>

<template>
  <div class="view">
    <section class="panel">
      <header class="panel__head panel__head--toolbar">
        <div>
          <h2>任务列表 <span class="count-pill">{{ tasks.total }}</span></h2>
          <p>状态与进度通过 REST + WebSocket 实时同步</p>
        </div>
        <div class="toolbar">
          <label class="search">
            <Icon name="search" :size="16" />
            <input v-model="tasks.filter.search" type="search" placeholder="搜索链接或任务 ID" />
          </label>
          <select v-model="tasks.filter.state" class="select" aria-label="按状态筛选">
            <option v-for="s in states" :key="s" :value="s">{{ s || '全部状态' }}</option>
          </select>
          <select v-model="tasks.filter.kind" class="select" aria-label="按类型筛选">
            <option v-for="k in kinds" :key="k" :value="k">{{ k || '全部类型' }}</option>
          </select>
          <button class="icon-btn" title="刷新列表" @click="tasks.fetchList()"><Icon name="refresh" /></button>
        </div>
      </header>
      <TaskTable
        :tasks="tasks.visible"
        :loading="tasks.loading"
        :selected-id="tasks.selectedId"
        @open="openTask"
        @act="(id, act) => tasks.act(id, act, act === 'pause' ? '暂停' : act === 'resume' ? '继续' : '取消')"
        @remove="removeTask"
      />
    </section>

    <Drawer
      v-if="tasks.detail"
      :title="tasks.detail.input"
      :subtitle="tasks.detail.id"
      width="min(560px, 94vw)"
      @close="closeTask"
    >
      <div class="detail-head">
        <StateBadge :state="tasks.detail.state" />
        <span class="chip">{{ tasks.detail.inputKind }}</span>
        <span class="chip chip--soft">{{ tasks.detail.resource?.kind || '未解析' }}</span>
        <button class="btn btn--ghost btn--sm" @click="tasks.refreshDetail()">
          <Icon name="refresh" :size="14" /> 刷新
        </button>
      </div>

      <div class="tabs">
        <button :class="{ active: detailTab === 'overview' }" @click="detailTab = 'overview'">概览</button>
        <button :class="{ active: detailTab === 'files' }" @click="detailTab = 'files'">资源与文件</button>
        <button :class="{ active: detailTab === 'sources' }" @click="detailTab = 'sources'">
          源列表 <span class="count-pill">{{ tasks.detail.sources?.length || 0 }}</span>
        </button>
        <button :class="{ active: detailTab === 'events' }" @click="detailTab = 'events'">事件</button>
      </div>

      <div v-if="detailTab === 'overview'" class="detail-section">
        <div class="detail-progress">
          <ProgressBar :done="tasks.detail.bytesDone" :total="tasks.detail.bytesTotal" />
          <div class="detail-progress__meta">
            <span>{{ formatBytes(tasks.detail.bytesDone) }} / {{ formatBytes(tasks.detail.bytesTotal) }}</span>
            <span v-if="tasks.detail.bytesUploaded">已上传 {{ formatBytes(tasks.detail.bytesUploaded) }}</span>
          </div>
        </div>
        <div class="kv-grid">
          <div class="kv"><span>实时速度</span><strong>{{ formatRate(tasks.detail.speed) }}</strong></div>
          <div class="kv"><span>预计剩余</span><strong>{{ formatEta(tasks.detail.eta) }}</strong></div>
          <div class="kv"><span>连接数</span><strong>{{ tasks.detail.connections }}</strong></div>
          <div class="kv"><span>已校验分片</span><strong>{{ tasks.detail.chunksVerified }}</strong></div>
          <div class="kv"><span>创建时间</span><strong>{{ formatTime(tasks.detail.createdAt) }}</strong></div>
          <div class="kv"><span>后处理</span><strong>{{ tasks.detail.options.postProcess || 'none' }}</strong></div>
          <div v-if="tasks.detail.peers?.length" class="kv"><span>Peer</span><strong>{{ tasks.detail.peers.length }}</strong></div>
          <div v-if="tasks.detail.trackers?.length" class="kv"><span>Tracker</span><strong>{{ tasks.detail.trackers.length }}</strong></div>
        </div>
        <div v-if="tasks.detail.errorMessage" class="notice notice--danger">
          <Icon name="alert" :size="16" />
          <div><strong>{{ tasks.detail.errorKind }}</strong><p>{{ tasks.detail.errorMessage }}</p></div>
        </div>
        <div class="detail-actions">
          <button v-if="tasks.detail.state === 'Running'" class="btn btn--ghost" @click="tasks.act(tasks.detail.id, 'pause', '暂停')">
            <Icon name="pause" :size="15" /> 暂停
          </button>
          <button v-else-if="tasks.detail.state !== 'Completed'" class="btn btn--ghost" @click="tasks.act(tasks.detail.id, 'resume', '继续')">
            <Icon name="play" :size="15" /> 继续 / 重试
          </button>
          <button v-if="!['Completed', 'Canceled'].includes(tasks.detail.state)" class="btn btn--ghost" @click="tasks.act(tasks.detail.id, 'cancel', '取消')">
            <Icon name="close" :size="15" /> 取消
          </button>
        </div>
      </div>

      <div v-else-if="detailTab === 'files'" class="detail-section">
        <h3 class="subhead">输出文件</h3>
        <ul v-if="tasks.detail.resource?.files?.length" class="file-list">
          <li v-for="file in tasks.detail.resource.files" :key="file.index">
            <Icon name="file" :size="16" />
            <span class="file-list__name">{{ file.path }}</span>
            <span class="mono muted">{{ formatBytes(file.size) }}</span>
            <span class="chip chip--soft">{{ file.selected ? '已选' : '未选' }}</span>
          </li>
        </ul>
        <p v-else class="muted">解析完成后显示文件列表。</p>

        <h3 class="subhead">镜像源</h3>
        <ul v-if="tasks.detail.resource?.mirrors?.length" class="file-list">
          <li v-for="(mirror, i) in tasks.detail.resource.mirrors" :key="i">
            <Icon name="link" :size="16" />
            <span class="file-list__name" :title="mirror.url">{{ mirror.url }}</span>
            <span class="chip chip--soft">P{{ mirror.priority ?? 0 }}</span>
          </li>
        </ul>
        <p v-else class="muted">无镜像信息。</p>

        <h3 class="subhead">协议标识</h3>
        <pre class="code-block">{{ JSON.stringify(tasks.detail.resource?.identity ?? {}, null, 2) }}</pre>
        <p v-if="sidecars().length" class="hint-line">
          <Icon name="info" :size="14" /> 已探测 sidecar：{{ sidecars().join(', ') }}
        </p>
      </div>

      <div v-else-if="detailTab === 'sources'" class="detail-section">
        <h3 class="subhead">已连接的源</h3>
        <p class="muted source-hint">每 3 秒自动刷新；状态、速度与队列位置来自 eD2k 下载引擎。</p>
        <p v-if="!tasks.detail.sources?.length" class="muted">
          当前没有已握手的下载源。任务开始下载后，这里会列出每个源的状态、速度和远端进度。
        </p>
        <div v-else class="task-table__wrap source-table__wrap">
          <table class="task-table ed-table source-table">
            <thead>
              <tr>
                <th>端点</th>
                <th>客户端</th>
                <th>来源</th>
                <th>状态</th>
                <th>队列</th>
                <th>进度</th>
                <th>下载速度</th>
                <th>上传速度</th>
                <th>失败</th>
                <th>文件</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(source, i) in tasks.detail.sources" :key="`${source.endpoint}-${i}`">
                <td class="mono">{{ source.endpoint }}</td>
                <td>
                  {{ source.client || '未知客户端' }}
                  <span v-if="source.version" class="muted-cell">{{ source.version }}</span>
                </td>
                <td>
                  <span v-for="tag in source.sources" :key="tag" class="chip chip--soft">{{ sourceLabel(tag) }}</span>
                  <span v-if="!source.sources?.length" class="muted-cell">—</span>
                </td>
                <td><span class="chip" :class="sourceChip(source.state)">{{ sourceLabel(source.state) }}</span></td>
                <td class="mono">
                  <template v-if="source.queueRank || source.remoteQueueRank">
                    <span v-if="source.queueRank">本地 {{ source.queueRank }}</span>
                    <span v-if="source.remoteQueueRank"> / 远端 {{ source.remoteQueueRank }}</span>
                  </template>
                  <span v-else class="muted-cell">—</span>
                </td>
                <td>{{ Math.round((source.progress || 0) * 100) }}%</td>
                <td class="mono">{{ formatRate(source.downloadSpeed) }}</td>
                <td class="mono">{{ formatRate(source.uploadSpeed) }}</td>
                <td>{{ source.failCount || 0 }}</td>
                <td class="muted-cell" :title="source.fileName">{{ source.fileName || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-else class="detail-section">
        <EventFeed :events="taskEvents()" :limit="50" @open-task="openTask" />
      </div>
    </Drawer>
  </div>
</template>
