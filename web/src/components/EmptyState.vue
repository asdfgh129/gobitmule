<script setup lang="ts">
import Icon from './Icon.vue'
withDefaults(defineProps<{ icon?: string; title: string; hint?: string }>(), { icon: 'file', hint: '' })
</script>

<template>
  <div class="empty-state">
    <span class="empty-state__icon"><Icon :name="icon" :size="26" /></span>
    <strong>{{ title }}</strong>
    <p v-if="hint">{{ hint }}</p>
    <slot />
  </div>
</template>
