<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { CircleDot, Eraser, Radio, SquareTerminal } from '@lucide/vue'
import TerminalOutput from '@/components/harbor/TerminalOutput.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { useServiceLogs } from '@/composables/useServiceLogs'
import { useHarborStore } from '@/stores/harbor'

const props = defineProps<{
  projectId: string
  serviceId: string
  serviceName: string
  fill?: boolean
}>()

const store = useHarborStore()
const viewport = ref<HTMLElement | null>(null)
const follow = ref(true)
const projectId = computed(() => props.projectId)
const serviceId = computed(() => props.serviceId)
const logSupported = computed(() => store.daemonStatus?.capabilities.includes('control.service-logs.v1') === true)
const logWaitSupported = computed(() => store.daemonStatus?.capabilities.includes('control.service-logs-wait.v1') === true)
const daemonConnected = computed(() => store.connectionState === 'connected')
const snapshotSequence = computed(() => store.snapshot?.sequence)
const {
  output,
  outputResetKey,
  error,
  truncated,
  state,
  clear,
} = useServiceLogs({
  projectId,
  serviceId,
  supported: logSupported,
  waitSupported: logWaitSupported,
  connected: daemonConnected,
  snapshotSequence,
  read: (selectedProjectId, sessionId, selectedServiceId, cursor) => store.readServiceLogs(selectedProjectId, sessionId, selectedServiceId, cursor),
  wait: (selectedProjectId, sessionId, selectedServiceId, cursor, waitMilliseconds) => store.waitServiceLogs(selectedProjectId, sessionId, selectedServiceId, cursor, waitMilliseconds),
})

const statusLabel = computed(() => {
  switch (state.value) {
    case 'live': return 'Live'
    case 'waiting': return 'Waiting'
    case 'reconnecting': return 'Reconnecting'
    case 'unsupported': return 'Unsupported'
    case 'ended': return 'Ended'
    case 'error': return 'Error'
    case 'connecting': return 'Connecting'
  }
})

const emptyMessage = computed(() => {
  switch (state.value) {
    case 'unsupported': return 'This Harbor build does not support service logs.'
    case 'ended': return 'This service log stream has ended.'
    case 'error': return 'Harbor could not continue this service log stream.'
    case 'reconnecting': return 'Reconnecting to the Harbor daemon…'
    case 'live': return 'Waiting for new service log output…'
    case 'waiting': return 'Logs will appear when this Compose service is running.'
    case 'connecting': return `Connecting to ${props.serviceName} logs…`
  }
})

watch([projectId, serviceId], () => {
  follow.value = true
})

// updateFollow pauses automatic tailing when the user moves away from the end of the transcript.
function updateFollow() {
  const element = viewport.value
  if (!element) return
  follow.value = element.scrollHeight - element.scrollTop - element.clientHeight <= 24
}

// scrollToEnd waits for terminal rows to render before moving the viewport.
async function scrollToEnd(force = false) {
  if (!force && !follow.value) return
  await nextTick()
  const element = viewport.value
  if (!element) return
  element.scrollTop = element.scrollHeight
}

// resumeFollow makes the choice explicit after a user has paused automatic tailing.
function resumeFollow() {
  follow.value = true
  void scrollToEnd(true)
}

// clearOutput removes only this desktop view while the daemon cursor remains attached to the live stream.
function clearOutput() {
  clear()
  follow.value = true
}
</script>

<template>
  <Card :class="['gap-0 overflow-hidden rounded-lg py-0 shadow-none', { 'flex min-h-0 flex-1 flex-col': fill }]">
    <CardHeader class="flex-row items-start justify-between gap-3 border-b px-4 py-3">
      <div class="min-w-0">
        <div class="flex items-center gap-2">
          <SquareTerminal class="size-4 text-muted-foreground" />
          <CardTitle class="text-sm">Logs</CardTitle>
        </div>
        <p class="mt-1 text-xs text-muted-foreground">Live output from this Compose service</p>
      </div>
      <div class="flex shrink-0 items-center gap-2">
        <Badge variant="outline" class="gap-1.5 capitalize">
          <span
            class="size-1.5 rounded-full"
            :class="{
              'animate-pulse bg-emerald-500': state === 'live',
              'animate-pulse bg-amber-500': state === 'connecting' || state === 'reconnecting',
              'bg-destructive': state === 'error',
              'bg-muted-foreground': state === 'unsupported' || state === 'ended' || state === 'waiting',
            }"
            aria-hidden="true"
          />
          {{ statusLabel }}
        </Badge>
        <Button variant="ghost" size="sm" :disabled="!output" @click="clearOutput">
          <Eraser class="size-3.5" />
          Clear
        </Button>
        <Button
          :variant="follow ? 'secondary' : 'outline'"
          size="sm"
          :aria-pressed="follow"
          @click="resumeFollow"
        >
          <Radio v-if="follow" class="size-3.5" />
          <CircleDot v-else class="size-3.5" />
          {{ follow ? 'Following' : 'Follow logs' }}
        </Button>
      </div>
    </CardHeader>
    <CardContent :class="['p-0', { 'flex min-h-0 flex-1 flex-col': fill }]">
      <p v-if="error" class="border-b border-destructive/30 bg-destructive/10 px-4 py-2 text-xs text-destructive">{{ error }}</p>
      <p v-if="truncated" class="border-b border-amber-500/30 bg-amber-500/10 px-4 py-2 text-xs text-amber-700 dark:text-amber-300">Earlier logs are no longer retained. The live stream continues from the newest visible output.</p>
      <div
        ref="viewport"
        :class="fill ? 'min-h-0 flex-1 overflow-auto bg-zinc-950 px-4 py-3 font-mono text-xs leading-5 text-zinc-200 outline-none' : 'max-h-[30rem] min-h-72 overflow-auto bg-zinc-950 px-4 py-3 font-mono text-xs leading-5 text-zinc-200 outline-none'"
        tabindex="0"
        :aria-label="`${serviceName} service logs`"
        @scroll="updateFollow"
      >
        <TerminalOutput
          v-if="output"
          :output="output"
          :reset-key="outputResetKey"
          @rendered="scrollToEnd"
        />
        <p v-else class="flex min-h-64 items-center justify-center text-center text-zinc-500">{{ emptyMessage }}</p>
      </div>
    </CardContent>
  </Card>
</template>
