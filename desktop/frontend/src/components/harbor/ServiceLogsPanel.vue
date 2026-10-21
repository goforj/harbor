<script setup lang="ts">
import { computed } from 'vue'
import { Eraser, SquareTerminal } from '@lucide/vue'
import LogViewer from '@/components/harbor/LogViewer.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { useServiceLogs } from '@/composables/useServiceLogs'
import { useHarborStore } from '@/stores/harbor'

const props = defineProps<{
  projectId: string
  serviceId: string
  serviceName: string
  fill?: boolean
}>()

const store = useHarborStore()
const projectId = computed(() => props.projectId)
const serviceId = computed(() => props.serviceId)
const logSupported = computed(() => store.daemonStatus?.capabilities.includes('control.service-logs.v1') === true)
const logWaitSupported = computed(() => store.daemonStatus?.capabilities.includes('control.service-logs-wait.v1') === true)
const daemonConnected = computed(() => store.connectionState === 'connected')
const snapshotSequence = computed(() => store.snapshot?.sequence)
const {
  logs,
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

</script>

<template>
  <Card :class="['gap-0 overflow-hidden rounded-lg py-0 shadow-none', { 'flex min-h-0 flex-1 flex-col': fill }]">
    <LogViewer
      :output="output"
      :reset-key="outputResetKey"
      :identity="`${projectId}:${serviceId}:${logs?.session_id ?? ''}`"
      :label="`${serviceName} service logs`"
      :fill="fill ?? false"
    >
      <template #label>
        <SquareTerminal class="size-4" /><span class="text-sm font-semibold text-foreground">Logs</span>
        <Badge variant="outline" class="gap-1.5 capitalize">
          <span class="size-1.5 rounded-full" :class="{
            'animate-pulse bg-emerald-500': state === 'live',
            'animate-pulse bg-amber-500': state === 'connecting' || state === 'reconnecting',
            'bg-destructive': state === 'error',
            'bg-muted-foreground': state === 'unsupported' || state === 'ended' || state === 'waiting',
          }" aria-hidden="true" />
          {{ statusLabel }}
        </Badge>
      </template>
      <template #actions><Button variant="ghost" size="sm" class="h-7 text-xs" :disabled="!output" @click="clear"><Eraser class="size-3.5" />Clear</Button></template>
      <template #notices>
        <p v-if="error" class="border-b border-destructive/30 bg-destructive/10 px-4 py-2 text-xs text-destructive">{{ error }}</p>
        <p v-if="truncated" class="border-b border-amber-500/30 bg-amber-500/10 px-4 py-2 text-xs text-amber-700 dark:text-amber-300">Earlier logs are no longer retained. Search covers retained output only; the live stream continues.</p>
      </template>
      <template #empty><p class="flex min-h-64 items-center justify-center text-center text-muted-foreground">{{ emptyMessage }}</p></template>
    </LogViewer>
  </Card>
</template>
