<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{ name: string; size?: number }>(), { size: 18 })

// Compact stroke icon set (no external icon dependency, works fully offline).
const paths: Record<string, string[]> = {
  dashboard: ['M4 13h6V4H4v9Z', 'M14 20h6V11h-6v9Z', 'M4 20h6v-4H4v4Z', 'M14 8h6V4h-6v4Z'],
  tasks: ['M8 6h12', 'M8 12h12', 'M8 18h12', 'M3 6l1.5 1.5L7 5', 'M3 12l1.5 1.5L7 11', 'M3 18l1.5 1.5L7 17'],
  plus: ['M12 5v14', 'M5 12h14'],
  settings: ['M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Z', 'M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.12 2.12-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.04 1.56V21h-3v-.1A1.7 1.7 0 0 0 10.6 19.3a1.7 1.7 0 0 0-1.88.34l-.06.06-2.12-2.12.06-.06A1.7 1.7 0 0 0 6.94 15a1.7 1.7 0 0 0-1.56-1.04H5v-3h.1A1.7 1.7 0 0 0 6.7 9.9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.12-2.12.06.06A1.7 1.7 0 0 0 10.36 6.2 1.7 1.7 0 0 0 11.4 4.64V4.6h3v.1a1.7 1.7 0 0 0 1.04 1.56 1.7 1.7 0 0 0 1.88-.34l.06-.06 2.12 2.12-.06.06A1.7 1.7 0 0 0 19.04 9.9 1.7 1.7 0 0 0 20.6 11h.1v3h-.1A1.7 1.7 0 0 0 19.4 15Z'],
  tools: ['M14.7 6.3a4 4 0 0 0-5 5L4 17v3h3l5.7-5.7a4 4 0 0 0 5-5l-2.6 2.6-2.9-.7-.7-2.9 2.6-2.7Z'],
  activity: ['M3 12h4l3-8 4 16 3-8h4'],
  pause: ['M8 5v14', 'M16 5v14'],
  play: ['M7 4l12 8-12 8V4Z'],
  close: ['M6 6l12 12', 'M18 6 6 18'],
  trash: ['M4 7h16', 'M9 7V4h6v3', 'M6 7l1 13h10l1-13', 'M10 11v5', 'M14 11v5'],
  refresh: ['M20 11a8 8 0 1 0-2.3 5.7', 'M20 4v7h-7'],
  check: ['M4 12l5 5L20 6'],
  copy: ['M9 9h11v11H9z', 'M5 15H4V4h11v1'],
  alert: ['M12 3 2 20h20L12 3Z', 'M12 9v5', 'M12 17h.01'],
  bolt: ['M13 2 4 14h7l-1 8 9-12h-7l1-8Z'],
  server: ['M4 4h16v6H4z', 'M4 14h16v6H4z', 'M8 7h.01', 'M8 17h.01'],
  clock: ['M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Z', 'M12 7v5l3 2'],
  database: ['M12 3c4.4 0 8 1.3 8 3s-3.6 3-8 3-8-1.3-8-3 3.6-3 8-3Z', 'M4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6', 'M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3'],
  shield: ['M12 2 4 5v6c0 5 3.4 9.4 8 11 4.6-1.6 8-6 8-11V5l-8-3Z', 'M9 12l2 2 4-4'],
  link: ['M10 13a5 5 0 0 0 7.5.5l2-2A5 5 0 0 0 12.5 4l-1.2 1.2', 'M14 11a5 5 0 0 0-7.5-.5l-2 2A5 5 0 0 0 11.5 20l1.2-1.2'],
  layers: ['M12 3 2 8l10 5 10-5-10-5Z', 'M2 12l10 5 10-5', 'M2 16l10 5 10-5'],
  search: ['M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16Z', 'M21 21l-4.3-4.3'],
  download: ['M12 3v12', 'M7 10l5 5 5-5', 'M4 21h16'],
  upload: ['M12 21V9', 'M7 14l5-5 5 5', 'M4 3h16'],
  wifi: ['M2 8a16 16 0 0 1 20 0', 'M5 12a11 11 0 0 1 14 0', 'M8.5 16a6 6 0 0 1 7 0', 'M12 20h.01'],
  'wifi-off': ['M2 8a16 16 0 0 1 6-3.5', 'M12 4a16 16 0 0 1 10 4', 'M5 12a11 11 0 0 1 4-2.5', 'M8.5 16a6 6 0 0 1 7 0', 'M12 20h.01', 'M3 3l18 18'],
  file: ['M6 3h8l5 5v13H6V3Z', 'M14 3v5h5'],
  info: ['M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Z', 'M12 11v5', 'M12 8h.01'],
  external: ['M14 4h6v6', 'M20 4 10 14', 'M18 14v6H4V6h6'],
  chevron: ['M9 6l6 6-6 6'],
  folder: ['M3 6h6l2 2h10v12H3V6Z'],
}

const d = computed(() => paths[props.name] ?? paths.info)
</script>

<template>
  <svg
    class="icon"
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.7"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path v-for="(p, i) in d" :key="i" :d="p" />
  </svg>
</template>
