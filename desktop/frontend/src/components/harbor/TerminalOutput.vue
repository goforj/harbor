<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { terminalLinkSegments, TerminalModel } from '@/lib/terminal'
import { searchTerminalLines, terminalSearchSegments } from '@/lib/terminalSearch'
import type { TerminalMatch } from '@/lib/terminalSearch'
import { harborBridge } from '@/bridge'
import type { TerminalLine } from '@/lib/terminal'

const props = defineProps<{
  output: string
  resetKey: number
  query?: string
}>()

const emit = defineEmits<{
  rendered: []
}>()

const model = new TerminalModel()
const lines = shallowRef<TerminalLine[]>(model.renderLines())
const root = ref<HTMLElement | null>(null)
const selectedMatch = ref<TerminalMatch | null>(null)
const search = computed(() => searchTerminalLines(lines.value, props.query ?? ''))
const activeMatchIndex = computed(() => search.value.matches.findIndex(match => (
  match.lineId === selectedMatch.value?.lineId && match.start === selectedMatch.value.start
)))
const searchState = computed(() => ({ total: search.value.total, count: search.value.matches.length, index: activeMatchIndex.value }))
const matchesByLine = computed(() => {
  const grouped = new Map<number, Array<TerminalMatch & { index: number }>>()
  search.value.matches.forEach((match, index) => {
    const row = grouped.get(match.lineId) ?? []
    row.push({ ...match, index })
    grouped.set(match.lineId, row)
  })
  return grouped
})
watch(search, async (result) => {
  if (activeMatchIndex.value >= 0) return
  selectedMatch.value = result.matches[0] ?? null
  await nextTick()
  scrollActiveMatch()
})
watch(() => props.query, async () => {
  selectedMatch.value = search.value.matches[0] ?? null
  await nextTick()
  scrollActiveMatch()
})
defineExpose({ searchState, navigateMatch })
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

// openTerminalURL delegates WebView navigation to the native desktop opener.
function openTerminalURL(event: MouseEvent, url: string) {
  event.preventDefault()
  void harborBridge.openTerminalURL(url)
}

// navigateMatch wraps within the retained results without changing the log transport cursor.
async function navigateMatch(direction: number) {
  const matches = search.value.matches
  if (!matches.length) return
  selectedMatch.value = matches[(activeMatchIndex.value + direction + matches.length) % matches.length] ?? null
  await nextTick()
  scrollActiveMatch()
}

// scrollActiveMatch moves only the owning log viewport, leaving the surrounding workspace in place.
function scrollActiveMatch() {
  const match = root.value?.querySelector<HTMLElement>('[data-active-match="true"]')
  const viewport = root.value?.closest<HTMLElement>('[data-log-viewport]')
  if (!match || !viewport) return
  const bounds = viewport.getBoundingClientRect()
  const target = match.getBoundingClientRect()
  if (target.top < bounds.top) viewport.scrollTop += target.top - bounds.top - 8
  else if (target.bottom > bounds.bottom) viewport.scrollTop += target.bottom - bounds.bottom + 8
}
</script>

<template>
  <div ref="root" class="harbor-terminal-output whitespace-pre-wrap break-words" role="log" aria-live="off">
    <div v-for="line in lines" :key="line.id" v-memo="[line, matchesByLine.get(line.id), activeMatchIndex]" class="harbor-terminal-line">
      <template v-if="matchesByLine.has(line.id)">
        <span v-for="(segment, index) in terminalSearchSegments(line, matchesByLine.get(line.id)!)" :key="index" :style="segment.style">
          <a v-if="segment.url" :href="segment.url" target="_blank" rel="noopener noreferrer" class="underline decoration-dotted underline-offset-2 hover:decoration-solid" @click="openTerminalURL($event, segment.url)"><template v-for="(part, partIndex) in segment.parts" :key="partIndex"><mark v-if="part.matchIndex != null" class="harbor-log-match" :data-active-match="part.matchIndex === activeMatchIndex">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></a>
          <template v-else><template v-for="(part, partIndex) in segment.parts" :key="partIndex"><mark v-if="part.matchIndex != null" class="harbor-log-match" :data-active-match="part.matchIndex === activeMatchIndex">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></template>
        </span>
      </template>
      <template v-else>
        <span v-for="(run, index) in line.runs" :key="index" :style="run.style"><template v-for="(segment, segmentIndex) in terminalLinkSegments(run.text)" :key="segmentIndex"><a v-if="segment.url" :href="segment.url" target="_blank" rel="noopener noreferrer" class="underline decoration-dotted underline-offset-2 hover:decoration-solid" @click="openTerminalURL($event, segment.url)">{{ segment.text }}</a><template v-else>{{ segment.text }}</template></template></span>
      </template>
    </div>
  </div>
</template>
