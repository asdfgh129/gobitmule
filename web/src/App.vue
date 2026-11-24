<script setup lang="ts">
import { computed, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Icon from '@/components/Icon.vue'
import { useSessionStore } from '@/stores/session'
import { useTasksStore } from '@/stores/tasks'
import { useEventsStore } from '@/stores/events'
import { useUiStore } from '@/stores/ui'
import type { RouteMeta } from '@/router'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const tasks = useTasksStore()
const events = useEventsStore()
const ui = useUiStore()

const meta = computed(() => route.meta as unknown as RouteMeta)
const nav = [
  { name: 'dashboard', label: '概览', icon: 'dashboard' },
  { name: 'tasks', label: '任务', icon: 'tasks' },
  { name: 'new', label: '新建下载', icon: 'plus' },
  { name: 'search', label: 'eMule 搜索', icon: 'search' },
  { name: 'servers', label: 'eD2k 服务器', icon: 'server' },
  { name: 'kad', label: 'KAD 节点', icon: 'wifi' },
  { name: 'settings', label: '设置', icon: 'settings' },
  { name: 'operations', label: '运维', icon: 'tools' },
]

const liveLabel = computed(() => {
  if (!session.online) return '未连接'
  if (events.socketState === 'open') return '实时已连接'
  if (events.socketState === 'connecting') return '连接中'
  return '轮询模式'
})
const liveTone = computed(() => (session.online
  ? (events.socketState === 'open' ? 'ok' : 'warn')
  : 'bad'))

let poll: ReturnType<typeof setInterval> | null = null

async function refreshAll(): Promise<void> {
  await Promise.allSettled([session.refresh(), tasks.fetchList()])
}

onMounted(async () => {
  await refreshAll()
  events.start()
  await events.loadHistory()
  // task.progress frames carry only byte counters; speed/ETA come from GET.
  poll = setInterval(() => {
    if (document.hidden) return
    void session.refresh()
    void tasks.fetchList()
    if (tasks.selectedId) void tasks.refreshDetail()
  }, 5000)
})

onUnmounted(() => {
  events.stop()
  if (poll) clearInterval(poll)
})
</script>

<template>
  <div class="shell">
    <aside class="sidebar">
      <div class="brand">
        <span class="brand__mark"><Icon name="download" :size="20" /></span>
        <div>
          <strong>GoBitMule</strong>
          <small>eMule · BitTorrent 控制台</small>
        </div>
      </div>

      <nav class="nav">
        <p class="nav__label">工作台</p>
        <RouterLink
          v-for="item in nav"
          :key="item.name"
          :to="{ name: item.name }"
          class="nav__item"
          active-class="is-active"
        >
          <Icon :name="item.icon" :size="18" />
          <span>{{ item.label }}</span>
          <span v-if="item.name === 'tasks' && tasks.activeCount" class="nav__count">
            {{ tasks.activeCount }}
          </span>
        </RouterLink>
      </nav>

      <div class="sidebar__foot">
        <div class="sidebar__status">
          <span class="pulse" :class="`pulse--${liveTone}`" />
          <span>{{ liveLabel }}</span>
        </div>
        <p>v{{ session.health?.version || '—' }} · {{ session.capabilities?.rpcLevel || 'L0' }}</p>
        <p>{{ session.capabilities?.credentials || '—' }}</p>
      </div>
    </aside>

    <div class="main">
      <header class="topbar">
        <div>
          <h1>{{ meta.title }}</h1>
          <p>{{ meta.subtitle }}</p>
        </div>
        <div class="topbar__actions">
          <span class="pill" :class="`pill--${liveTone}`">
            <Icon :name="session.online ? 'activity' : 'wifi-off'" :size="14" />
            {{ session.online ? '守护进程正常' : '守护进程不可用' }}
          </span>
          <button class="icon-btn" title="刷新" @click="refreshAll"><Icon name="refresh" /></button>
          <button class="btn btn--primary" @click="router.push({ name: 'new' })">
            <Icon name="plus" :size="16" /> 新建下载
          </button>
        </div>
      </header>

      <main class="content">
        <RouterView />
      </main>
    </div>

    <div class="toast-stack">
      <div v-for="toast in ui.toasts" :key="toast.id" class="toast" :class="`toast--${toast.kind}`">
        <Icon :name="toast.kind === 'error' ? 'alert' : toast.kind === 'success' ? 'check' : 'info'" :size="16" />
        <span>{{ toast.text }}</span>
        <button class="toast__close" @click="ui.dismiss(toast.id)"><Icon name="close" :size="13" /></button>
      </div>
    </div>
  </div>
</template>
