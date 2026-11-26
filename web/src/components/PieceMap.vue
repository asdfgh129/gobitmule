<script setup lang="ts">
import { computed } from 'vue'
import { pieceCoverage } from '@/utils/pieceMap'

/**
 * PieceMap draws the parts a peer claims to own, the way eMule's client list
 * does it: a horizontal bar where filled segments are parts the peer has.
 */
const props = withDefaults(
  defineProps<{
    pieceMap?: string
    pieces?: number
    progressKnown?: boolean
    /** Bin cap; the SVG stretches to the cell, so this only bounds DOM work. */
    bins?: number
  }>(),
  { pieceMap: '', pieces: 0, progressKnown: false, bins: 96 },
)

const coverage = computed(() => pieceCoverage(props.pieceMap || '', props.pieces || 0, props.bins))

const bars = computed(() => {
  if (!props.progressKnown) return []
  // eMule has no part bitmap for a file it already completed: it answers
  // FileStatusAnswer with a bare `uint16 0`. That is a full bar, not an empty one.
  if ((props.pieces || 0) <= 0) return [1]
  return coverage.value.bars
})

const title = computed(() => {
  if (!props.progressKnown) return '该源还没有回复 FileStatusRequest'
  if ((props.pieces || 0) <= 0) return '对方已拥有完整文件'
  return `对方拥有 ${coverage.value.have} / ${props.pieces} 块`
})

/** How opaque a bin is: fully covered bins are solid, sparse ones fade out. */
function binOpacity(fraction: number): number {
  if (fraction <= 0) return 0
  return Math.min(1, Math.max(0.22, fraction))
}
</script>

<template>
  <div v-if="progressKnown" class="piecemap" :title="title">
    <svg
      class="piecemap__svg"
      :viewBox="`0 0 ${bars.length} 1`"
      preserveAspectRatio="none"
      shape-rendering="crispEdges"
      aria-hidden="true"
    >
      <rect
        v-for="(fraction, index) in bars"
        :key="index"
        :x="index"
        y="0"
        width="1"
        height="1"
        fill="var(--teal)"
        :fill-opacity="binOpacity(fraction)"
      />
    </svg>
  </div>
</template>
