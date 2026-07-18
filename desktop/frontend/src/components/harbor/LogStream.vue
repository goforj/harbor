<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { FileText } from '@lucide/vue'
import type { LogLine } from '@/domain/harbor'

const props = withDefaults(defineProps<{
  lines: LogLine[]
  follow?: boolean
  emptyMessage?: string
  ariaLabel?: string
}>(), {
  follow: true,
  emptyMessage: 'No logs yet',
  ariaLabel: 'Application logs',
})

const scrollElement = ref<HTMLElement | null>(null)
const virtualizerOptions = computed(() => ({
  count: props.lines.length,
  getScrollElement: () => scrollElement.value,
  estimateSize: () => 27,
  overscan: 14,
  getItemKey: (index: number) => props.lines[index]?.id ?? index,
}))
const rowVirtualizer = useVirtualizer(virtualizerOptions)
const virtualRows = computed(() => rowVirtualizer.value.getVirtualItems())
const totalHeight = computed(() => rowVirtualizer.value.getTotalSize())

watch(() => props.lines.length, () => {
  void followTail()
})

onMounted(() => {
  void followTail()
})

async function followTail() {
  if (!props.follow || props.lines.length === 0) {
    return
  }

  await nextTick()
  rowVirtualizer.value.scrollToIndex(props.lines.length - 1, { align: 'end' })
}

function displayTime(timestamp: string) {
  const date = new Date(timestamp)
  if (Number.isNaN(date.getTime())) {
    return timestamp
  }
  return date.toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  })
}
</script>

<template>
  <div
    v-if="lines.length"
    ref="scrollElement"
    role="log"
    :aria-label="ariaLabel"
    aria-live="off"
    class="h-full min-h-0 overflow-auto bg-zinc-950 font-mono text-[0.75rem] text-zinc-300 [scrollbar-color:var(--color-border)_transparent]"
  >
    <ol class="relative min-w-max" :style="{ height: `${totalHeight}px` }">
      <li
        v-for="virtualRow in virtualRows"
        :key="String(virtualRow.key)"
        class="absolute top-0 left-0 grid h-[27px] min-w-full grid-cols-[5.25rem_5rem_max-content] items-center border-b border-white/5 px-3 leading-[27px] hover:bg-white/5"
        :style="{ transform: `translateY(${virtualRow.start}px)` }"
      >
        <time class="text-zinc-600" :datetime="lines[virtualRow.index].timestamp">
          {{ displayTime(lines[virtualRow.index].timestamp) }}
        </time>
        <span class="truncate pr-3 text-zinc-500">{{ lines[virtualRow.index].source }}</span>
        <span :class="['whitespace-pre', lines[virtualRow.index].stream === 'stderr' ? 'text-red-300' : undefined]">
          {{ lines[virtualRow.index].message }}
        </span>
      </li>
    </ol>
  </div>

  <div v-else class="flex h-full min-h-36 flex-col items-center justify-center gap-2 bg-muted/20 px-6 text-center">
    <FileText aria-hidden="true" class="size-5 text-muted-foreground" />
    <p class="text-sm font-medium">{{ emptyMessage }}</p>
    <p class="text-xs text-muted-foreground">Output will appear here when the process starts.</p>
  </div>
</template>
