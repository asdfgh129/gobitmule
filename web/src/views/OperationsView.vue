<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import Icon from '@/components/Icon.vue'
import EventFeed from '@/components/EventFeed.vue'
import { useSessionStore } from '@/stores/session'
import { useEventsStore } from '@/stores/events'
import { useUiStore } from '@/stores/ui'
import { formatBytes, formatTime } from '@/utils/format'

const router = useRouter()
const session = useSessionStore()
const events = useEventsStore()
const ui = useUiStore()
const reloading = ref(false)

onMounted(() => void events.loadHistory(200))

const healthRows = computed(() => [
  { label: '服务状态', value: session.health?.status ?? '—', ok: session.online },
  { label: '数据库连接', value: session.health?.db ? '正常' : '异常', ok: !!session.health?.db },
  { label: '限速器', value: session.health?.limiter ? '已启用' : '未启用', ok: !!session.health?.limiter },
  { label: '版本', value: session.health?.version ?? '—', ok: true },
  { label: '运行时长', value: session.uptime, ok: true },
])

const commands = [
  {
    title: '创建备份', icon: 'database', tone: 'teal',
    description: '在线可用。使用 VACUUM INTO 生成一致性快照，并对每个文件计算 SHA-256。',
    command: 'udctl backup ./ud-backup --db --config',
    hint: '备份不包含远程令牌；凭据仅导出系统钥匙串引用。',
  },
  {
    title: '恢复备份', icon: 'refresh', tone: 'amber',
    description: '必须先停止守护进程。恢复前会自动快照旧库并逐片校验 .part 文件。',
    command: 'udctl stop && udctl restore ./ud-backup',
    hint: '运行中执行会被明确拒绝，避免与写入中的数据库竞争。',
  },
  {
    title: '迁移 schema', icon: 'layers', tone: 'indigo',
    description: '离线操作。按备份清单中的 schema 版本升级数据库结构。',
    command: 'udctl migrate --db ~/.local/share/ud/ud.db',
    hint: '使用 --from 指定来源版本可跳过自动探测。',
  },
]

async function reloadHistory(): Promise<void> {
  reloading.value = true
  await events.loadHistory(200)
  reloading.value = false
}

async function copy(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    ui.success('命令已复制到剪贴板')
  } catch {
    ui.error('浏览器拒绝了剪贴板访问权限')
  }
}

function openTask(id: string): void {
  void router.push({ name: 'tasks', query: { task: id } })
}
</script>

<template>
  <div class="view ops-view">
    <section class="panel">
      <header class="panel__head">
        <div><h2>健康检查</h2><p>来自 GET /api/v1/healthz 的守护进程实时状态</p></div>
        <span class="pill" :class="session.online ? 'pill--ok' : 'pill--bad'">
          <Icon name="activity" :size="14" /> {{ session.online ? '运行正常' : '不可用' }}
        </span>
      </header>
      <div class="health-grid">
        <div v-for="row in healthRows" :key="row.label" class="health-item">
          <span class="health-item__label">{{ row.label }}</span>
          <strong :class="{ ok: row.ok, bad: !row.ok }">{{ row.value }}</strong>
        </div>
      </div>
      <div class="sidecar-row">
        <span>已探测 Sidecar</span>
        <span v-if="Object.keys(session.health?.sidecars ?? {}).length" class="cap-group__chips">
          <span v-for="(detail, name) in session.health?.sidecars" :key="name" class="chip chip--soft" :title="String(detail)">
            {{ name }}
          </span>
        </span>
        <span v-else class="muted">无</span>
      </div>
    </section>

    <section class="panel">
      <header class="panel__head">
        <div>
          <h2>事件历史</h2>
          <p>持久化事件与实时流合并展示，最多保留最近 200 条</p>
        </div>
        <div class="toolbar">
          <select v-model="events.typeFilter" class="select" aria-label="按事件类型筛选">
            <option value="">全部事件</option>
            <option v-for="type in events.types" :key="type" :value="type">{{ type }}</option>
          </select>
          <button class="icon-btn" title="重新载入历史" @click="reloadHistory" :disabled="reloading">
            <Icon name="refresh" :size="16" />
          </button>
        </div>
      </header>
      <div class="event-history">
        <EventFeed :events="events.visible" :limit="200" @open-task="openTask" />
      </div>
      <p class="panel__foot">累计记录 {{ session.stats?.tasks ?? 0 }} 个任务 · {{ formatBytes(session.stats?.bytesDone ?? 0) }} 已下载</p>
    </section>

    <section class="panel">
      <header class="panel__head">
        <div><h2>离线运维命令</h2><p>备份 / 恢复 / 迁移由 udctl 提供，Web 控制台展示准确的调用方式</p></div>
      </header>
      <div class="ops-grid">
        <article v-for="op in commands" :key="op.title" class="ops-card" :class="`ops-card--${op.tone}`">
          <header>
            <span class="ops-card__icon"><Icon :name="op.icon" :size="18" /></span>
            <h3>{{ op.title }}</h3>
          </header>
          <p>{{ op.description }}</p>
          <div class="code-command">
            <code>{{ op.command }}</code>
            <button class="icon-btn" title="复制命令" @click="copy(op.command)"><Icon name="copy" :size="15" /></button>
          </div>
          <p class="ops-card__hint"><Icon name="info" :size="13" /> {{ op.hint }}</p>
        </article>
      </div>
      <p class="panel__foot">最近事件时间：{{ formatTime(session.lastEventAt) }}</p>
    </section>
  </div>
</template>
