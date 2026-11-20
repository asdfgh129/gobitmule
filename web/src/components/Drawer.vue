<script setup lang="ts">
import Icon from './Icon.vue'
defineProps<{ title: string; subtitle?: string; width?: string }>()
const emit = defineEmits<{ close: [] }>()
</script>

<template>
  <div class="drawer-layer" @click.self="emit('close')">
    <aside class="drawer" :style="width ? { width } : undefined" role="dialog" aria-modal="true">
      <header class="drawer__head">
        <div>
          <h2>{{ title }}</h2>
          <p v-if="subtitle">{{ subtitle }}</p>
        </div>
        <button class="icon-btn" aria-label="关闭" @click="emit('close')"><Icon name="close" /></button>
      </header>
      <div class="drawer__body"><slot /></div>
    </aside>
  </div>
</template>
