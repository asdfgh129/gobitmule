<script setup lang="ts">
import { computed } from 'vue'
import Icon from './Icon.vue'
import { eventLabel, formatBytes, relativeTime, shortId } from '@/utils/format'
import type { UDEvent } from '@/types/api'

const props = withDefaults(defineProps<{ events: UDEvent[]; limit?: number; compact?: boolean }>(), {
  limit: 8, compact: false,
})
const emit = defineEmits<{ openTask: [id: string] }>()

const shown = computed(() => [...props.events].reverse().slice(0, props.limit))

function detail(event: UDEvent): string {
  const p = event.payload ?? {}
  switch (event.type) {
    case 'task.progress':
      return `${formatBytes(Number(p.bytesDone ?? 0))} / ${formatBytes(Number(p.bytesTotal ?? 0))}`
    case 'task.state': return String(p.state ?? '')
    case 'task.failed': return String(p.message ?? p.errorCode ?? '')
    case 'task.chunk.failed': return `分片 ${p.chunk ?? '?'} · 第 ${p.attempt ?? '?'} 次`
    case 'config.changed': return `${p.keys ?? 0} 项参数已更新`
    case 'task.created': return String(p.kind ?? '')
    default: return ''
  }
}

function tone(event: UDEvent): string {
  if (event.type === 'task.completed') return 'success'
  if (event.type === 'task.failed' || event.type === 'task.chunk.failed') return 'danger'
  if (event.type === 'config.changed' || event.type === 'capability.changed') return 'info'
  return 'neutral'
}
</script>

<template>
  <ul class="event-feed" :class="{ 'event-feed--compact': compact }">
    <li v-for="(event, i) in shown" :key="`${event.ts}-${event.type}-${i}`" class="event-feed__item">
      <span class="event-feed__marker" :class="`event-feed__marker--${tone(event)}`" />
      <div class="event-feed__body">
        <div class="event-feed__line">
          <strong>{{ eventLabel(event.type) }}</strong>
          <time>{{ relativeTime(event.ts) }}</time>
        </div>
        <p v-if="detail(event)">{{ detail(event) }}</p>
        <button
          v-if="event.taskId"
          class="link-btn"
          @click="emit('openTask', event.taskId!)"
        >任务 {{ shortId(event.taskId) }} <Icon name="chevron" :size="12" /></button>
      </div>
    </li>
    <li v-if="shown.length === 0" class="event-feed__empty">暂无事件，连接实时流后将自动更新。</li>
  </ul>
</template>
