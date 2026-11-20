<script setup lang="ts">
import StateBadge from './StateBadge.vue'
import ProgressBar from './ProgressBar.vue'
import EmptyState from './EmptyState.vue'
import Icon from './Icon.vue'
import { formatBytes, shortId } from '@/utils/format'
import type { Task } from '@/types/api'

withDefaults(defineProps<{
  tasks: Task[]
  loading?: boolean
  compact?: boolean
  selectedId?: string | null
}>(), { loading: false, compact: false, selectedId: null })

const emit = defineEmits<{
  open: [id: string]
  act: [id: string, action: 'pause' | 'resume' | 'cancel']
  remove: [id: string]
}>()
</script>

<template>
  <div v-if="loading && tasks.length === 0" class="table-loading">正在载入任务…</div>
  <EmptyState
    v-else-if="tasks.length === 0"
    icon="tasks"
    title="这里还没有任务"
    hint="点击右上角「新建下载」添加第一个链接。"
  />
  <div v-else class="task-table__wrap">
    <table class="task-table">
      <thead>
        <tr>
          <th>任务</th>
          <th v-if="!compact">类型</th>
          <th>状态</th>
          <th style="min-width: 150px">进度</th>
          <th v-if="!compact">大小</th>
          <th class="task-table__actions-head">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="task in tasks"
          :key="task.id"
          :class="{ 'is-selected': selectedId === task.id }"
          @click="emit('open', task.id)"
        >
          <td>
            <div class="task-name" :title="task.input">{{ task.input }}</div>
            <div class="task-id">{{ shortId(task.id) }}</div>
          </td>
          <td v-if="!compact"><span class="chip chip--soft">{{ task.inputKind }}</span></td>
          <td><StateBadge :state="task.state" /></td>
          <td><ProgressBar :done="task.bytesDone" :total="task.bytesTotal" compact /></td>
          <td v-if="!compact" class="mono">{{ formatBytes(task.bytesTotal) }}</td>
          <td class="task-table__actions" @click.stop>
            <button
              v-if="task.state === 'Running'"
              class="icon-btn"
              title="暂停"
              @click="emit('act', task.id, 'pause')"
            ><Icon name="pause" :size="15" /></button>
            <button
              v-else-if="task.state !== 'Completed'"
              class="icon-btn"
              title="继续 / 重试"
              @click="emit('act', task.id, 'resume')"
            ><Icon name="play" :size="15" /></button>
            <button
              v-if="!['Completed', 'Canceled'].includes(task.state)"
              class="icon-btn"
              title="取消"
              @click="emit('act', task.id, 'cancel')"
            ><Icon name="close" :size="15" /></button>
            <button class="icon-btn icon-btn--danger" title="删除" @click="emit('remove', task.id)">
              <Icon name="trash" :size="15" />
            </button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
