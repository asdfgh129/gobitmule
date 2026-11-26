<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{ done: number; total: number; compact?: boolean }>(), { compact: false })

const percent = computed(() => {
  if (props.total <= 0) return 0
  return Math.min(100, Math.max(0, Math.round((props.done / props.total) * 100)))
})

// A 6.5 GB transfer only reaches 1% after 65 MB, so rounding prints "0%" for
// minutes while bytes are clearly arriving. Say "<1%" instead of lying.
const label = computed(() => {
  if (props.total <= 0) return '—'
  if (props.done <= 0) return '0%'
  if (props.done >= props.total) return '100%'
  const raw = (props.done / props.total) * 100
  return raw < 1 ? '<1%' : `${Math.round(raw)}%`
})
</script>

<template>
  <div class="progress" :class="{ 'progress--compact': compact }">
    <div class="progress__track">
      <div class="progress__fill" :style="{ width: `${percent}%` }" />
    </div>
    <span class="progress__label">{{ label }}</span>
  </div>
</template>
