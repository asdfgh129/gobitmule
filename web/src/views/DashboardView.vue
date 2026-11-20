<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import StatCard from '@/components/StatCard.vue'
import TaskTable from '@/components/TaskTable.vue'
import EventFeed from '@/components/EventFeed.vue'
import Icon from '@/components/Icon.vue'
import { useSessionStore } from '@/stores/session'
import { useTasksStore } from '@/stores/tasks'
import { useEventsStore } from '@/stores/events'
import { formatRate } from '@/utils/format'

const router = useRouter()
const session = useSessionStore()
const tasks = useTasksStore()
const events = useEventsStore()

const recent = computed(() => [...tasks.items].sort((a, b) => b.updatedAt - a.updatedAt).slice(0, 6))

function openTask(id: string): void {
  void router.push({ name: 'tasks', query: { task: id } })
}
</script>

<template>
  <div class="view">
    <div v-if="!session.online && !session.loading" class="notice notice--danger">
      <Icon name="alert" :size="18" />
      <div>
        <strong>守护进程不可用</strong>
        <p>{{ session.lastError || '请确认 ud daemon 正在运行，或检查连接令牌。' }}</p>
      </div>
    </div>

    <section class="stats-grid">
      <StatCard label="活跃任务" :value="tasks.activeCount" hint="下载 / 解析 / 后处理" icon="activity" tone="indigo" />
      <StatCard label="任务总数" :value="session.stats?.tasks ?? 0" :hint="`已完成 ${session.stats?.completed ?? 0} · 失败 ${session.stats?.failed ?? 0}`" icon="tasks" tone="teal" />
      <StatCard label="实时速率" :value="formatRate(session.stats?.rate ?? 0)" :hint="`${session.stats?.connections ?? 0} 个连接`" icon="bolt" tone="amber" />
      <StatCard label="运行时长" :value="session.uptime" :hint="session.health?.version || '—'" icon="clock" tone="slate" />
    </section>

    <section class="panel">
      <header class="panel__head">
        <div>
          <h2>最近任务</h2>
          <p>按最近更新时间排序，点击任意一行查看细节</p>
        </div>
        <RouterLink class="btn btn--ghost" :to="{ name: 'tasks' }">
          查看全部 <Icon name="chevron" :size="15" />
        </RouterLink>
      </header>
      <TaskTable
        :tasks="recent"
        :loading="tasks.loading"
        compact
        @open="openTask"
        @act="(id, act) => tasks.act(id, act, act === 'pause' ? '暂停' : act === 'resume' ? '继续' : '取消')"
        @remove="tasks.remove"
      />
    </section>

    <section class="dashboard-grid">
      <article class="panel">
        <header class="panel__head">
          <div>
            <h2>能力矩阵</h2>
            <p>来自守护进程的实时注册结果</p>
          </div>
          <span class="chip">{{ session.capabilities?.rpcLevel || 'L0' }}</span>
        </header>
        <div class="cap-grid">
          <div v-for="group in [
            { icon: 'link', label: '解析器', items: session.capabilities?.resolvers ?? [] },
            { icon: 'layers', label: '传输器', items: session.capabilities?.transports ?? [] },
            { icon: 'bolt', label: '后处理', items: session.capabilities?.postProcessors ?? [] },
            { icon: 'server', label: 'Sidecar', items: session.capabilities?.sidecars ?? [] },
          ]" :key="group.label" class="cap-group">
            <div class="cap-group__title"><Icon :name="group.icon" :size="15" /> {{ group.label }}</div>
            <div class="cap-group__chips">
              <span v-for="item in group.items" :key="item" class="chip chip--soft">{{ item }}</span>
              <span v-if="!group.items.length" class="muted">无</span>
            </div>
          </div>
        </div>
      </article>

      <article class="panel">
        <header class="panel__head">
          <div>
            <h2>实时事件</h2>
            <p>{{ events.socketState === 'open' ? 'WebSocket 已连接' : '连接中 / 轮询回退' }}</p>
          </div>
          <RouterLink class="link-btn" :to="{ name: 'operations' }">全部事件</RouterLink>
        </header>
        <EventFeed :events="events.items" :limit="7" compact @open-task="openTask" />
      </article>
    </section>
  </div>
</template>
