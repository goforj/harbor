<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { Check, ChevronDown, LoaderCircle, TriangleAlert } from '@lucide/vue'
import type { StartupEvent, StartupProgress } from '@/domain/harbor'

const props = defineProps<{ progress: StartupProgress; settled?: boolean; compact?: boolean }>()

// steps joins typed spans in order; terminal text never participates in this projection.
const steps = computed(() => {
  const result: StartupEvent[] = []
  for (const event of props.progress.events) {
    if (event.phase === 'startup') continue
    if (event.status === 'started') {
      result.push({ ...event })
      continue
    }
    const reverseIndex = [...result].reverse().findIndex(step => step.phase === event.phase && step.label === event.label && step.status === 'started')
    const open = result.length - 1 - reverseIndex
    if (reverseIndex >= 0) result[open] = { ...event, sequence: result[open]!.sequence }
  }
  return result
})
const terminal = computed(() => [...props.progress.events].reverse().find(event => event.phase === 'startup' && event.status !== 'started'))
const active = computed(() => [...steps.value].reverse().find(step => step.status === 'started'))
const failedStep = computed(() => [...steps.value].reverse().find(step => step.status === 'failed'))
const heading = computed(() => terminal.value?.status === 'completed' ? 'Startup steps completed' : terminal.value?.status === 'failed' ? 'Startup failed' : terminal.value?.status === 'cancelled' ? 'Startup interrupted' : props.settled ? 'Startup details' : 'Starting development runtime')
const compactHeading = computed(() => failedStep.value ? `${failedStep.value.label} failed` : terminal.value?.status === 'completed' ? 'Startup complete' : terminal.value?.status === 'failed' ? 'Startup failed' : terminal.value?.status === 'cancelled' ? 'Startup interrupted' : props.settled ? 'Startup details' : active.value?.label ?? 'Starting development runtime')
const failed = computed(() => steps.value.some(step => step.status === 'failed') || terminal.value?.status === 'failed')

const expanded = ref(false)
const contentID = useId()
watch([() => props.compact, failed, () => terminal.value?.status, () => props.progress.events[0]?.session], () => {
  expanded.value = failed.value || (!props.compact && !terminal.value)
}, { immediate: true })

function duration(milliseconds: number) {
  return milliseconds < 1000 ? `${milliseconds} ms` : `${(milliseconds / 1000).toFixed(1)} s`
}
</script>

<template>
  <section v-if="progress.events.length || progress.invalid" class="shrink-0 rounded-lg border bg-card/40 text-sm" aria-label="Startup progress" data-testid="startup-timeline">
    <button type="button" class="flex w-full cursor-pointer items-center gap-2 rounded-lg px-4 py-3 text-left focus-visible:outline-2 focus-visible:outline-ring" :aria-expanded="expanded" :aria-controls="contentID" @click="expanded = !expanded">
      <TriangleAlert v-if="failed || progress.invalid" class="size-4 text-destructive" aria-hidden="true" />
      <Check v-else-if="terminal?.status === 'completed'" class="size-4 text-status-ready" aria-hidden="true" />
      <LoaderCircle v-else-if="!settled" class="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" aria-hidden="true" />
      <span class="font-medium">{{ compact ? compactHeading : heading }}</span>
      <span class="ml-auto flex shrink-0 items-center gap-3">
        <span v-if="terminal" class="font-mono text-xs text-muted-foreground">{{ duration(terminal.elapsed_ms) }}</span>
        <ChevronDown class="size-3.5 text-muted-foreground" :class="expanded ? 'rotate-180' : ''" aria-hidden="true" />
      </span>
    </button>
    <div v-if="expanded" :id="contentID">
    <ol class="max-h-52 space-y-2 overflow-auto border-t px-4 py-3" aria-label="Development startup steps">
      <li v-for="step in steps" :key="step.sequence" class="flex items-center gap-2">
        <span class="size-1.5 shrink-0 rounded-full" :class="step.status === 'failed' ? 'bg-destructive' : step.status === 'completed' ? 'bg-status-ready' : 'bg-muted-foreground'" aria-hidden="true" />
        <span class="min-w-0 flex-1 break-words">{{ step.label }}</span>
        <span class="text-xs text-muted-foreground">{{ step.status === 'started' ? (settled ? 'Interrupted' : 'Running') : step.status }}</span>
        <span v-if="step.status !== 'started'" class="w-16 shrink-0 text-right font-mono text-xs text-muted-foreground">{{ duration(step.elapsed_ms) }}</span>
      </li>
    </ol>
    <p v-if="progress.invalid" class="border-t px-4 py-2 text-xs text-muted-foreground">Some startup details are unavailable. Development output is still available below.</p>
    </div>
  </section>
</template>
