<script setup lang="ts">
import { computed } from 'vue'
import type { TaskState } from '@/types/api'

const props = defineProps<{ state: TaskState }>()

const labels: Record<TaskState, string> = {
  Pending: '等待中', Resolving: '解析中', Ready: '就绪', Running: '下载中',
  Paused: '已暂停', PostProcessing: '后处理', Completed: '已完成',
  Failed: '失败', Canceled: '已取消', Seeding: '做种中',
}
const tone = computed(() => {
  switch (props.state) {
    case 'Running': case 'Seeding': case 'PostProcessing': return 'active'
    case 'Completed': return 'success'
    case 'Failed': return 'danger'
    case 'Paused': case 'Pending': case 'Resolving': case 'Ready': return 'warn'
    default: return 'muted'
  }
})
</script>

<template>
  <span class="badge" :class="`badge--${tone}`"><span class="dot" />{{ labels[state] }}</span>
</template>
