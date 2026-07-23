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
  project: ProjectSnapshot
  sequence?: number
}>()

const portsByService = ref<Record<string, ServicePort[]>>({})
const loading = ref(false)
const failedServices = ref<string[]>([])
const copied = ref('')
const copyError = ref<string | null>(null)
let refreshGeneration = 0
let copiedTimer: number | undefined

const serviceConnections = computed(() => projectServiceConnections(
  props.project,
  portsByService.value,
))

watch(
  [() => props.active, () => props.project.id, () => props.project.updated_at, () => props.sequence],
  ([active]) => {
    if (active) void refresh()
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  refreshGeneration += 1
  if (copiedTimer !== undefined) window.clearTimeout(copiedTimer)
})

async function refresh() {
  const generation = ++refreshGeneration
  loading.value = true
  failedServices.value = []
  const observations = await Promise.all(props.project.services.map(async (service) => {
    try {
      const logs = await harborBridge.getServiceLogs(props.project.id, '', service.id, 0)
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
    <CardHeader class="!flex !items-center !justify-between border-b px-4 py-3">
      <div>
        <CardTitle class="text-sm">Service connections</CardTitle>
        <p class="mt-1 text-xs text-muted-foreground">Hostnames and ports currently published by Harbor</p>
      </div>
      <Button variant="ghost" size="sm" :disabled="loading" @click="refresh">
        <LoaderCircle v-if="loading" class="size-3.5 animate-spin" />
        <RefreshCw v-else class="size-3.5" />
        Refresh
      </Button>
    </CardHeader>
    <CardContent class="p-0">
      <p v-if="copyError" class="border-b px-4 py-2 text-xs text-destructive">{{ copyError }}</p>
      <p v-if="failedServices.length" class="border-b px-4 py-2 text-xs text-amber-700 dark:text-amber-300">
        Harbor could not inspect ports for {{ failedServices.length }} {{ failedServices.length === 1 ? 'service' : 'services' }}.
      </p>
      <div class="divide-y">
        <div v-for="row in serviceConnections" :key="row.service.id" class="px-4 py-4">
          <div class="flex items-center gap-3">
            <StatusBadge :status="row.service.state" />
            <div class="min-w-0 flex-1">
              <p class="text-sm font-medium">{{ row.service.name }}</p>
              <p class="text-xs text-muted-foreground">{{ row.service.kind }}</p>
            </div>
          </div>

          <div v-if="row.connections.length" class="mt-3 space-y-2">
            <div
              v-for="connection in row.connections"
              :key="connection.id"
              class="flex min-w-0 flex-wrap items-center gap-3 rounded-md border bg-muted/20 px-3 py-2"
            >
              <div class="min-w-0 flex-1">
                <code class="block truncate text-sm font-medium text-foreground">{{ connection.hostname }}</code>
                <p class="mt-0.5 text-xs text-muted-foreground">
                  Port {{ connection.port }} · {{ connection.protocol }}<span v-if="connection.source === 'resource'"> · {{ connection.label }}</span>
                </p>
              </div>
              <Button variant="ghost" size="sm" @click="copy(connection.hostname, `${connection.id}:host`)">
                <Check v-if="copied === `${connection.id}:host`" class="size-3.5" />
                <Clipboard v-else class="size-3.5" />
                {{ copied === `${connection.id}:host` ? 'Copied' : 'Copy host' }}
              </Button>
              <Button variant="outline" size="sm" @click="copy(connection.endpoint, `${connection.id}:endpoint`)">
                <Check v-if="copied === `${connection.id}:endpoint`" class="size-3.5" />
                <Clipboard v-else class="size-3.5" />
                {{ copied === `${connection.id}:endpoint` ? 'Copied' : 'Copy address' }}
              </Button>
            </div>
          </div>
          <p v-else class="mt-3 text-xs text-muted-foreground">
            {{ loading ? 'Inspecting published ports…' : 'No host connection is currently published for this service.' }}
          </p>
        </div>
      </div>
    </CardContent>
  </Card>
</template>
