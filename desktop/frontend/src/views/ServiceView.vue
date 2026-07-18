<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import {
  ArrowLeft,
  ArrowUpRight,
  Check,
  Clipboard,
  Container,
  Database,
  ExternalLink,
  FolderKanban,
  HeartPulse,
  Link2,
  Network,
  Server,
} from '@lucide/vue'
import StatusBadge from '@/components/harbor/StatusBadge.vue'
import { copyText } from '@/bridge/clipboard'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import { Separator } from '@/components/ui/separator'
import { useHarborStore } from '@/stores/harbor'

const route = useRoute()
const store = useHarborStore()
const copied = ref<'public' | 'private' | null>(null)

const serviceId = computed(() => {
  const value = route.params.serviceId
  return Array.isArray(value) ? value[0] : value
})
const service = computed(() => (serviceId.value ? store.serviceById(serviceId.value) : undefined))
const project = computed(() => (service.value ? store.projectById(service.value.projectId) : undefined))
const matchingResource = computed(() => {
  if (!service.value || !project.value) {
    return undefined
  }

  return project.value.resources.find((resource) => resource.serviceId === service.value?.id)
})
const siblingServices = computed(() => {
  if (!service.value || !project.value) {
    return []
  }

  return project.value.services.filter((entry) => entry.id !== service.value?.id)
})
const nativePort = computed(() => service.value?.endpoint.match(/:(\d+)$/)?.[1] ?? 'HTTP/TLS')
const kindLabel = computed(() => {
  const labels = {
    database: 'Database',
    cache: 'Cache',
    mail: 'Mail',
    observability: 'Observability',
  }

  return service.value ? labels[service.value.kind] : 'Service'
})
const healthDetail = computed(() => {
  switch (service.value?.status) {
    case 'ready':
      return 'Endpoint is accepting connections.'
    case 'working':
      return 'Harbor is reconciling this service.'
    case 'degraded':
      return 'The service is available with a reported limitation.'
    case 'failed':
      return 'The service is not accepting connections.'
    case 'stopped':
      return 'The project has stopped this service.'
    default:
      return 'Health has not been reported.'
  }
})

async function copyEndpoint(value: string, target: 'public' | 'private') {
  await copyText(value)
  copied.value = target
  window.setTimeout(() => {
    if (copied.value === target) {
      copied.value = null
    }
  }, 1600)
}

async function openResource(resourceId: string) {
  await store.openResource(resourceId)
}
</script>

<template>
  <main class="h-full min-w-0 overflow-y-auto" :aria-labelledby="service ? 'service-title' : 'service-empty-title'">
    <template v-if="service">
      <header class="border-b px-5 py-4 lg:px-7">
        <div class="flex min-w-0 flex-wrap items-start justify-between gap-3">
          <div class="flex min-w-0 items-start gap-2">
            <Button variant="ghost" size="icon-sm" class="-ml-2 shrink-0 min-[1100px]:hidden" as-child>
              <RouterLink to="/services" aria-label="Back to services">
                <ArrowLeft class="size-4" aria-hidden="true" />
              </RouterLink>
            </Button>
            <div class="min-w-0">
              <p class="mb-1 text-xs text-muted-foreground">{{ service.projectName }}</p>
              <div class="flex min-w-0 items-center gap-2">
                <h1 id="service-title" class="truncate text-base font-semibold tracking-tight">
                  {{ service.name }}
                </h1>
                <StatusBadge :status="service.status" />
                <Badge variant="secondary" class="h-5 px-1.5 text-[10px]">{{ service.owner }}</Badge>
              </div>
            </div>
          </div>
          <Button
            v-if="matchingResource"
            size="sm"
            @click="openResource(matchingResource.id)"
          >
            Open resource
            <ExternalLink class="size-3.5" aria-hidden="true" />
          </Button>
        </div>

        <div class="mt-4 flex min-w-0 items-center rounded-lg border bg-muted/35 p-1">
          <Link2 class="ml-2 size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
          <span class="min-w-0 flex-1 truncate px-2 font-mono text-xs">{{ service.endpoint }}</span>
          <Button
            variant="ghost"
            size="icon-sm"
            :aria-label="`Copy endpoint ${service.endpoint}`"
            @click="copyEndpoint(service.endpoint, 'public')"
          >
            <Check v-if="copied === 'public'" class="size-3.5" aria-hidden="true" />
            <Clipboard v-else class="size-3.5" aria-hidden="true" />
          </Button>
          <Button
            v-if="matchingResource"
            variant="ghost"
            size="icon-sm"
            :aria-label="`Open ${matchingResource.name}`"
            @click="openResource(matchingResource.id)"
          >
            <ArrowUpRight class="size-3.5" aria-hidden="true" />
          </Button>
        </div>
      </header>

      <div class="space-y-5 p-5 lg:p-7">
        <section aria-label="Service summary" class="grid overflow-hidden rounded-lg border sm:grid-cols-4">
          <div class="p-4 sm:border-r">
            <p class="text-xs font-medium text-muted-foreground">Type</p>
            <p class="mt-1 text-sm font-semibold">{{ kindLabel }}</p>
          </div>
          <div class="border-t p-4 sm:border-t-0 sm:border-r">
            <p class="text-xs font-medium text-muted-foreground">Native port</p>
            <p class="mt-1 font-mono text-sm font-semibold tabular-nums">{{ nativePort }}</p>
          </div>
          <div class="border-t p-4 sm:border-t-0 sm:border-r">
            <p class="text-xs font-medium text-muted-foreground">Runtime</p>
            <p class="mt-1 text-sm font-semibold">{{ service.owner === 'managed' ? 'Project managed' : 'External' }}</p>
          </div>
          <div class="border-t p-4 sm:border-t-0">
            <p class="text-xs font-medium text-muted-foreground">Health</p>
            <div class="mt-1"><StatusBadge :status="service.status" /></div>
          </div>
        </section>

        <div class="grid min-w-0 gap-5 xl:grid-cols-[minmax(0,1.2fr)_minmax(18rem,0.8fr)]">
          <Card class="gap-0 rounded-lg py-0 shadow-none">
            <CardHeader class="border-b px-4 py-3">
              <div class="flex items-center gap-2">
                <Network class="size-4 text-muted-foreground" aria-hidden="true" />
                <CardTitle class="text-sm">Connections</CardTitle>
              </div>
              <p class="text-xs text-muted-foreground">Stable native address backed by a private publication</p>
            </CardHeader>
            <CardContent class="p-0">
              <div class="grid min-w-0 grid-cols-[7rem_minmax(0,1fr)_auto] items-center gap-x-3 border-b px-4 py-3">
                <span class="text-xs font-medium text-muted-foreground">Project endpoint</span>
                <code class="truncate text-[11px]">{{ service.endpoint }}</code>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  :aria-label="`Copy project endpoint ${service.endpoint}`"
                  @click="copyEndpoint(service.endpoint, 'public')"
                >
                  <Check v-if="copied === 'public'" class="size-3.5" aria-hidden="true" />
                  <Clipboard v-else class="size-3.5" aria-hidden="true" />
                </Button>
              </div>
              <div class="grid min-w-0 grid-cols-[7rem_minmax(0,1fr)_auto] items-center gap-x-3 px-4 py-3">
                <span class="text-xs font-medium text-muted-foreground">Private target</span>
                <code class="truncate text-[11px]">{{ service.privateEndpoint }}</code>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  :aria-label="`Copy private target ${service.privateEndpoint}`"
                  @click="copyEndpoint(service.privateEndpoint, 'private')"
                >
                  <Check v-if="copied === 'private'" class="size-3.5" aria-hidden="true" />
                  <Clipboard v-else class="size-3.5" aria-hidden="true" />
                </Button>
              </div>
            </CardContent>
          </Card>

          <Card class="gap-0 rounded-lg py-0 shadow-none">
            <CardHeader class="border-b px-4 py-3">
              <div class="flex items-center gap-2">
                <HeartPulse class="size-4 text-muted-foreground" aria-hidden="true" />
                <CardTitle class="text-sm">Health</CardTitle>
              </div>
            </CardHeader>
            <CardContent class="p-4">
              <div class="flex items-start gap-3">
                <StatusBadge :status="service.status" />
                <div>
                  <p class="text-sm font-medium">{{ service.status }}</p>
                  <p class="mt-1 text-xs leading-5 text-muted-foreground">{{ healthDetail }}</p>
                </div>
              </div>
            </CardContent>
          </Card>
        </div>

        <div class="grid min-w-0 gap-5 xl:grid-cols-2">
          <Card class="gap-0 rounded-lg py-0 shadow-none">
            <CardHeader class="border-b px-4 py-3">
              <div class="flex items-center gap-2">
                <Container class="size-4 text-muted-foreground" aria-hidden="true" />
                <CardTitle class="text-sm">Runtime ownership</CardTitle>
              </div>
            </CardHeader>
            <CardContent class="p-0">
              <dl class="divide-y text-sm">
                <div class="grid grid-cols-[7rem_1fr] gap-3 px-4 py-3">
                  <dt class="text-xs text-muted-foreground">Owner</dt>
                  <dd class="text-xs font-medium">{{ service.owner === 'managed' ? 'Harbor project runtime' : 'External process' }}</dd>
                </div>
                <div class="grid grid-cols-[7rem_1fr] gap-3 px-4 py-3">
                  <dt class="text-xs text-muted-foreground">Scope</dt>
                  <dd class="text-xs font-medium">{{ service.projectName }}</dd>
                </div>
                <div class="grid grid-cols-[7rem_1fr] gap-3 px-4 py-3">
                  <dt class="text-xs text-muted-foreground">Service ID</dt>
                  <dd class="truncate font-mono text-[11px]">{{ service.id }}</dd>
                </div>
              </dl>
            </CardContent>
          </Card>

          <Card class="gap-0 rounded-lg py-0 shadow-none">
            <CardHeader class="border-b px-4 py-3">
              <div class="flex items-center gap-2">
                <FolderKanban class="size-4 text-muted-foreground" aria-hidden="true" />
                <CardTitle class="text-sm">Project</CardTitle>
              </div>
              <p class="text-xs text-muted-foreground">Services remain owned and versioned per project</p>
            </CardHeader>
            <CardContent class="p-0">
              <RouterLink
                v-if="project"
                :to="`/projects/${encodeURIComponent(project.id)}`"
                class="group flex min-w-0 items-center gap-3 px-4 py-4 transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
              >
                <StatusBadge :status="project.status" />
                <div class="min-w-0 flex-1">
                  <p class="truncate text-sm font-medium">{{ project.name }}</p>
                  <p class="truncate text-xs text-muted-foreground">{{ project.domain }}</p>
                </div>
                <ArrowUpRight class="size-3.5 shrink-0 text-muted-foreground group-hover:text-foreground" aria-hidden="true" />
              </RouterLink>
              <p v-else class="px-4 py-8 text-center text-sm text-muted-foreground">Project details are unavailable.</p>
            </CardContent>
          </Card>
        </div>

        <Card v-if="siblingServices.length" class="gap-0 rounded-lg py-0 shadow-none">
          <CardHeader class="border-b px-4 py-3">
            <div class="flex items-center gap-2">
              <Database class="size-4 text-muted-foreground" aria-hidden="true" />
              <CardTitle class="text-sm">Other project services</CardTitle>
            </div>
          </CardHeader>
          <CardContent class="p-0">
            <div class="divide-y">
              <RouterLink
                v-for="sibling in siblingServices"
                :key="sibling.id"
                :to="`/services/${encodeURIComponent(sibling.id)}`"
                class="group flex min-w-0 items-center gap-3 px-4 py-3 transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
              >
                <StatusBadge :status="sibling.status" />
                <div class="min-w-0 flex-1">
                  <p class="truncate text-sm font-medium">{{ sibling.name }}</p>
                  <p class="truncate font-mono text-[11px] text-muted-foreground">{{ sibling.endpoint }}</p>
                </div>
                <ArrowUpRight class="size-3.5 shrink-0 text-muted-foreground group-hover:text-foreground" aria-hidden="true" />
              </RouterLink>
            </div>
          </CardContent>
        </Card>

        <Separator />
        <p class="flex items-center gap-2 text-xs text-muted-foreground">
          <Server class="size-3.5" aria-hidden="true" />
          Public endpoints preserve native service ports while private publications avoid host conflicts.
        </p>
      </div>
    </template>

    <Empty v-else class="min-h-full border-0">
      <EmptyHeader>
        <EmptyTitle id="service-empty-title">
          {{ store.loading ? 'Loading service…' : serviceId ? 'Service not found' : 'Select a service' }}
        </EmptyTitle>
        <EmptyDescription>
          {{ serviceId ? 'Harbor does not have a service with this identifier.' : 'Choose a project-owned service to inspect its endpoints, runtime, and health.' }}
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent v-if="serviceId && !store.loading">
        <Button variant="outline" as-child>
          <RouterLink to="/services">
            <ArrowLeft class="size-4" aria-hidden="true" />
            Back to services
          </RouterLink>
        </Button>
      </EmptyContent>
    </Empty>
  </main>
</template>
