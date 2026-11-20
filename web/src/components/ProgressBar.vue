<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{ done: number; total: number; compact?: boolean }>(), { compact: false })

const percent = computed(() => {
  if (props.total <= 0) return 0
  return Math.min(100, Math.max(0, Math.round((props.done / props.total) * 100)))
})
</script>

<template>
  <div class="progress" :class="{ 'progress--compact': compact }">
    <div class="progress__track">
      <div class="progress__fill" :style="{ width: `${percent}%` }" />
    </div>
    <span class="progress__label">{{ total > 0 ? `${percent}%` : '—' }}</span>
  </div>
</template>
