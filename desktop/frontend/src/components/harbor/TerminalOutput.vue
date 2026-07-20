<script setup lang="ts">
import { nextTick, onBeforeUnmount, shallowRef, watch } from 'vue'
import { terminalLinkSegments, TerminalModel } from '@/lib/terminal'
import type { TerminalLine } from '@/lib/terminal'

const props = defineProps<{
  output: string
  resetKey: number
}>()

const emit = defineEmits<{
  rendered: []
}>()

const model = new TerminalModel()
const lines = shallowRef<TerminalLine[]>(model.renderLines())
let animationFrame: number | null = null
let observedOutputLength = 0
let observedResetKey = props.resetKey
let pendingAppend = ''
let pendingReset: string | null = null

watch([() => props.output, () => props.resetKey], ([output, resetKey]) => {
  if (resetKey === observedResetKey && output.length >= observedOutputLength) {
    pendingAppend += output.slice(observedOutputLength)
  }
  else {
    pendingReset = output
    pendingAppend = ''
  }
  observedOutputLength = output.length
  observedResetKey = resetKey
  scheduleRender()
}, { immediate: true })

onBeforeUnmount(() => {
  if (animationFrame !== null) window.cancelAnimationFrame(animationFrame)
  animationFrame = null
})

// scheduleRender coalesces transport bursts into one browser paint without adding a time-based debounce.
function scheduleRender() {
  if (animationFrame !== null) return
  animationFrame = window.requestAnimationFrame(() => {
    animationFrame = null
    if (pendingReset !== null) {
      model.reset()
      model.feed(pendingReset)
      pendingReset = null
    }
    if (pendingAppend) {
      model.feed(pendingAppend)
      pendingAppend = ''
    }
    lines.value = model.renderLines()
    void nextTick(() => emit('rendered'))
  })
}
</script>

<template>
  <div class="harbor-terminal-output whitespace-pre-wrap break-words" role="log" aria-live="off">
    <div v-for="line in lines" :key="line.id" v-memo="[line]" class="min-h-5">
      <span v-for="(run, index) in line.runs" :key="index" :style="run.style"><template v-for="(segment, segmentIndex) in terminalLinkSegments(run.text)" :key="segmentIndex"><a v-if="segment.url" :href="segment.url" target="_blank" rel="noopener noreferrer" class="underline decoration-dotted underline-offset-2 hover:decoration-solid">{{ segment.text }}</a><template v-else>{{ segment.text }}</template></template></span>
    </div>
  </div>
</template>
