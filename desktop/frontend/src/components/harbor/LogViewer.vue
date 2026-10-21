<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { ArrowDownToLine, Check, ChevronDown, ChevronUp, Clipboard, Pause, Play, Search, X } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { copyText } from '@/bridge/clipboard'
import { terminalPlainText } from '@/lib/terminal'
import TerminalOutput from './TerminalOutput.vue'

const props = withDefaults(defineProps<{
  output: string
  resetKey: number
  identity: string
  label: string
  fill?: boolean
}>(), { fill: true })

const viewport = ref<HTMLElement | null>(null)
const terminal = ref<InstanceType<typeof TerminalOutput> | null>(null)
const searchInput = ref<HTMLInputElement | null>(null)
const searchOpen = ref(false)
const query = ref('')
const follow = ref(true)
const copied = ref(false)
const copyError = ref('')
let copyTimer: number | undefined
const searchState = computed(() => terminal.value?.searchState ?? { total: 0, count: 0, index: -1 })
const matchLabel = computed(() => {
  if (!query.value) return 'Search retained output'
  const { total, index, count } = searchState.value
  if (!total) return 'No matches'
  return `${index + 1} of ${total.toLocaleString()}${total > count ? ` (first ${count.toLocaleString()} shown)` : ''}`
})

watch(() => props.identity, () => {
  follow.value = true
  searchOpen.value = false
  query.value = ''
  copied.value = false
  copyError.value = ''
})
onBeforeUnmount(() => window.clearTimeout(copyTimer))

// openSearch keeps inspection in the selected transcript while the stream continues receiving output.
async function openSearch() {
  follow.value = false
  searchOpen.value = true
  await nextTick()
  searchInput.value?.focus()
  searchInput.value?.select()
}

// closeSearch restores keyboard focus without unexpectedly resuming automatic scrolling.
function closeSearch() {
  searchOpen.value = false
  query.value = ''
  viewport.value?.focus({ preventScroll: true })
}

// handleKeydown scopes Find to this viewer so other pages and editable controls keep their normal shortcuts.
function handleKeydown(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && !event.altKey && event.key.toLowerCase() === 'f') {
    event.preventDefault()
    event.stopPropagation()
    void openSearch()
  }
  else if (searchOpen.value && event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    closeSearch()
  }
}

// updateFollow lets scrolling away pause tailing; returning to the bottom does not override an explicit pause.
function updateFollow() {
  const element = viewport.value
  if (element && element.scrollHeight - element.scrollTop - element.clientHeight > 24) follow.value = false
}

// scrollToEnd waits for the terminal model's next paint and rechecks the user's current follow choice.
async function scrollToEnd() {
  await nextTick()
  if (follow.value && viewport.value) viewport.value.scrollTop = viewport.value.scrollHeight
}

// resumeFollow returns to the newest output and resumes tailing without discarding the search query.
function resumeFollow() {
  follow.value = true
  void scrollToEnd()
}

// navigateMatch never resumes following while the user is navigating search results.
function navigateMatch(direction: number) {
  follow.value = false
  void terminal.value?.navigateMatch(direction)
}

// copyOutput preserves the established plain-text copy behavior independently of search highlighting.
async function copyOutput() {
  const identity = props.identity
  copyError.value = ''
  try {
    await copyText(terminalPlainText(props.output))
    if (identity !== props.identity) return
    copied.value = true
    window.clearTimeout(copyTimer)
    copyTimer = window.setTimeout(() => { copied.value = false }, 1600)
  }
  catch (error) {
    if (identity === props.identity) copyError.value = error instanceof Error ? error.message : 'Could not copy output.'
  }
}
</script>

<template>
  <section class="flex min-h-0 flex-1 flex-col overflow-hidden" :aria-label="`${label} viewer`" @keydown="handleKeydown">
    <div class="harbor-log-controls project-log-toolbar flex min-h-11 shrink-0 flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b px-4 py-1 text-foreground">
      <div class="flex min-w-0 items-center gap-2 text-xs text-muted-foreground"><slot name="label">{{ label }}</slot></div>
      <div class="flex flex-wrap items-center gap-1">
        <slot name="actions" />
        <Button variant="ghost" size="sm" class="h-7 text-xs" aria-label="Search logs" title="Search logs (Cmd/Ctrl+F)" :aria-expanded="searchOpen" @click="openSearch"><Search class="size-3.5" />Search</Button>
        <Button variant="ghost" size="sm" class="h-7 text-xs" :aria-pressed="follow" @click="follow ? follow = false : resumeFollow()"><Pause v-if="follow" class="size-3.5" /><Play v-else class="size-3.5" />{{ follow ? 'Pause scrolling' : 'Resume following' }}</Button>
        <Button v-if="!follow" variant="ghost" size="sm" class="h-7 text-xs" @click="resumeFollow"><ArrowDownToLine class="size-3.5" />Jump to latest</Button>
        <Button variant="ghost" size="sm" class="h-7 text-xs" :disabled="!output" @click="copyOutput"><Check v-if="copied" class="size-3.5" /><Clipboard v-else class="size-3.5" />{{ copied ? 'Copied' : 'Copy' }}</Button>
      </div>
    </div>
    <div v-if="searchOpen" role="search" :aria-label="`Search ${label}`" class="harbor-log-controls project-log-toolbar flex shrink-0 flex-wrap items-center gap-2 border-b px-4 py-2 text-foreground">
      <div class="flex min-w-32 flex-1 items-center gap-2 rounded-md border bg-background px-2 focus-within:ring-1 focus-within:ring-ring"><Search class="size-3.5 shrink-0 text-muted-foreground" /><input ref="searchInput" v-model="query" type="text" aria-label="Find in logs" placeholder="Find in retained output…" class="h-8 min-w-0 flex-1 bg-transparent text-sm outline-none" @input="follow = false" @keydown.enter.prevent="navigateMatch($event.shiftKey ? -1 : 1)" /></div>
      <span role="status" class="text-xs text-muted-foreground">{{ matchLabel }}</span>
      <div class="flex items-center gap-1"><Button variant="ghost" size="icon-sm" aria-label="Previous match" title="Previous match (Shift+Enter)" :disabled="!searchState.count" @click="navigateMatch(-1)"><ChevronUp class="size-4" /></Button><Button variant="ghost" size="icon-sm" aria-label="Next match" title="Next match (Enter)" :disabled="!searchState.count" @click="navigateMatch(1)"><ChevronDown class="size-4" /></Button><Button variant="ghost" size="icon-sm" aria-label="Close search" title="Close search (Escape)" @click="closeSearch"><X class="size-4" /></Button></div>
    </div>
    <slot name="notices" />
    <p v-if="copyError" class="border-b px-4 py-2 text-xs text-destructive">{{ copyError }}</p>
    <div ref="viewport" data-log-viewport :class="['harbor-log-viewport overflow-auto px-4 py-3 font-mono text-xs leading-5 outline-none focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-ring', fill ? 'min-h-0 flex-1' : 'max-h-[30rem] min-h-72']" tabindex="0" :aria-label="label" @scroll="updateFollow">
      <slot name="viewport-notices" />
      <TerminalOutput ref="terminal" :output="output" :reset-key="resetKey" :query="query" @rendered="scrollToEnd" />
      <slot v-if="!output" name="empty" />
    </div>
  </section>
</template>
