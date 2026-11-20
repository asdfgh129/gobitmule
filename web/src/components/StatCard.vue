<script setup lang="ts">
import Icon from './Icon.vue'

withDefaults(defineProps<{
  label: string
  value: string | number
  hint?: string
  icon: string
  tone?: 'indigo' | 'teal' | 'amber' | 'slate'
}>(), { tone: 'indigo', hint: '' })
</script>

<template>
  <article class="stat-card" :class="`stat-card--${tone}`">
    <div class="stat-card__top">
      <span class="stat-card__label">{{ label }}</span>
      <span class="stat-card__icon"><Icon :name="icon" :size="18" /></span>
    </div>
    <strong class="stat-card__value">{{ value }}</strong>
    <span class="stat-card__hint">{{ hint }}</span>
  </article>
</template>
