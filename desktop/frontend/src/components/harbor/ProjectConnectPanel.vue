<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Check, Clipboard, LoaderCircle, RefreshCw } from '@lucide/vue'
import StatusBadge from '@/components/harbor/StatusBadge.vue'
import { harborBridge } from '@/bridge'
import { copyText } from '@/bridge/clipboard'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import type { ProjectSnapshot, ServicePort } from '@/domain/harbor'
import { projectServiceConnections } from '@/lib/projectConnections'

const props = defineProps<{
  active: boolean
  fresh: boolean
  project: ProjectSnapshot
  sequence?: number
}>()

const portsByService = ref<Record<string, ServicePort[]>>({})
const loading = ref(false)
const failedServices = ref<string[]>([])
const copied = ref('')
const copyError = ref<string | null>(null)
const observable = computed(() => props.active && props.fresh
  && (props.project.state === 'ready' || props.project.state === 'degraded'))
let refreshGeneration = 0
let copiedTimer: number | undefined

const serviceConnections = computed(() => projectServiceConnections(
  props.project,
  portsByService.value,
).map((row) => ({
  ...row,
  connections: observable.value && !failedServices.value.includes(row.service.id) ? row.connections : [],
})))

watch(
  [observable, () => props.project.id, () => props.project.updated_at, () => props.sequence],
  () => {
    refreshGeneration += 1
    portsByService.value = {}
    failedServices.value = []
    loading.value = false
    copied.value = ''
    if (observable.value) void refresh()
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  refreshGeneration += 1
  if (copiedTimer !== undefined) window.clearTimeout(copiedTimer)
})

async function refresh() {
  if (!observable.value) return
  const generation = ++refreshGeneration
  const projectID = props.project.id
  loading.value = true
  failedServices.value = []
  const observations = await Promise.all(props.project.services.map(async (service) => {
    try {
      const logs = await harborBridge.getServiceLogs(projectID, '', service.id, 0)
      if (logs.project_id !== projectID || logs.service_id !== service.id
        || !logs.supported || !logs.available || logs.problem) {
        return { serviceID: service.id, ports: null }
      }
      return { serviceID: service.id, ports: logs.ports ?? [] }
    }
    catch {
      return { serviceID: service.id, ports: null }
    }
  }))
  if (generation !== refreshGeneration) return

  const nextPorts: Record<string, ServicePort[]> = {}
  const failures: string[] = []
  for (const observation of observations) {
    if (observation.ports == null) {
      failures.push(observation.serviceID)
      continue
    }
    nextPorts[observation.serviceID] = observation.ports
  }
  portsByService.value = nextPorts
  failedServices.value = failures
  loading.value = false
}

async function copy(value: string, key: string) {
  copyError.value = null
  try {
    await copyText(value)
    copied.value = key
    if (copiedTimer !== undefined) window.clearTimeout(copiedTimer)
    copiedTimer = window.setTimeout(() => {
      if (copied.value === key) copied.value = ''
    }, 1600)
  }
  catch (error) {
    copyError.value = error instanceof Error ? error.message : 'Could not copy the connection value.'
  }
}
</script>

<template>
  <Card class="gap-0 rounded-lg py-0 shadow-none">
    <CardHeader class="!flex !flex-row !items-center !justify-between gap-3 border-b px-3 py-2.5">
      <div>
        <CardTitle class="text-sm">Service connections</CardTitle>
        <p class="text-xs text-muted-foreground">Hostnames and ports published by Harbor</p>
      </div>
      <Button variant="ghost" size="sm" :disabled="loading || !observable" @click="refresh">
        <LoaderCircle v-if="loading" class="size-3.5 animate-spin" />
        <RefreshCw v-else class="size-3.5" />
        Refresh
      </Button>
    </CardHeader>
    <CardContent class="p-0">
      <p v-if="!fresh" role="status" class="border-b px-4 py-3 text-xs text-muted-foreground">Waiting for a fresh Harbor connection before showing service addresses.</p>
      <p v-else-if="!observable" role="status" class="border-b px-4 py-3 text-xs text-muted-foreground">Start the project to inspect its current service addresses.</p>
      <p v-if="copyError" class="border-b px-4 py-2 text-xs text-destructive">{{ copyError }}</p>
      <p v-if="failedServices.length" class="border-b px-4 py-2 text-xs text-amber-700 dark:text-amber-300">
        Harbor could not inspect ports for {{ failedServices.length }} {{ failedServices.length === 1 ? 'service' : 'services' }}.
      </p>
      <div class="divide-y">
        <div
          v-for="row in serviceConnections"
          :key="row.service.id"
          class="grid gap-1 px-3 py-2 sm:grid-cols-[minmax(10rem,0.7fr)_minmax(0,2fr)] sm:items-start sm:gap-3"
        >
          <div class="flex min-h-8 items-center gap-2">
            <StatusBadge :status="row.service.state" />
            <p class="min-w-0 truncate text-sm font-medium" :title="row.service.name">{{ row.service.name }}</p>
          </div>

          <div v-if="row.connections.length" class="divide-y">
            <div
              v-for="connection in row.connections"
              :key="connection.id"
              class="flex min-h-8 min-w-0 items-center gap-2 py-1 first:pt-0 last:pb-0"
            >
              <button
                type="button"
                class="group flex min-w-0 flex-1 items-center gap-1.5 text-left"
                :aria-label="`Copy ${connection.hostname} hostname`"
                :title="`Copy ${connection.hostname}`"
                @click="copy(connection.hostname, `${connection.id}:host`)"
              >
                <code class="truncate text-sm font-medium text-foreground">{{ connection.hostname }}</code>
                <Check v-if="copied === `${connection.id}:host`" class="size-3.5" />
                <Clipboard v-else class="size-3.5 shrink-0 text-muted-foreground opacity-60 group-hover:opacity-100" />
              </button>
              <span class="shrink-0 text-xs tabular-nums text-muted-foreground">{{ connection.port }} · {{ connection.protocol }}</span>
              <Button
                variant="ghost"
                size="icon-sm"
                :aria-label="`Copy ${connection.endpoint} address`"
                :title="`Copy ${connection.endpoint}`"
                @click="copy(connection.endpoint, `${connection.id}:endpoint`)"
              >
                <Check v-if="copied === `${connection.id}:endpoint`" class="size-3.5" />
                <Clipboard v-else class="size-3.5" />
              </Button>
            </div>
          </div>
          <p v-else class="flex min-h-8 items-center text-xs text-muted-foreground">
            {{ loading ? 'Inspecting published ports…' : failedServices.includes(row.service.id) ? 'Connection details are unavailable. Recheck this service.' : 'No host connection is currently published for this service.' }}
          </p>
        </div>
      </div>
    </CardContent>
  </Card>
</template>
